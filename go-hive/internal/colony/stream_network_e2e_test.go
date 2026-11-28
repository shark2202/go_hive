package colony

// Real-network stream-stall E2E for the worker and overseer production loops.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop — the "ttft" /
// "inactive" watchdog verdicts and the continue-nudge re-stream of the same
// inner turn. The existing colony stream tests (stream_wiring_test.go) drive
// that path through a fake streaming completer; this test drives the same
// production wiring (newWorkerAgentLoop / startOverseerReminders) through the
// real llm.Provider OpenAI-compatible SSE transport over a loopback TCP
// socket, so a stall is a genuinely withheld HTTP response body rather than a
// mock that returns when told to.
//
// Parity-exempt: the network harness itself has no Python counterpart — the
// reference stream watchdog is not exercised by a socket-level test.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	streamNetworkBudget      = 500 * time.Millisecond
	streamNetworkEvidenceEnv = "HIVE_WORKER_STREAM_E2E_OUT"
)

const (
	sseRecoveredChunk = `{"id":"chatcmpl-net-e2e","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"recovered"},"finish_reason":null}]}`
	ssePartialChunk   = `{"id":"chatcmpl-net-e2e","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"content":"stalled-partial"},"finish_reason":null}]}`
	sseStopChunk      = `{"id":"chatcmpl-net-e2e","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	sseUsageChunk     = `{"id":"chatcmpl-net-e2e","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":2,"total_tokens":14}}`
)

// networkRequestObservation is the server-side view of one HTTP request. Every
// field is guarded by streamNetworkServer.mu because the handler goroutine and
// the test goroutine both touch it.
type networkRequestObservation struct {
	index           int
	path            string
	model           string
	streamRequested bool
	messageCount    int
	nudgeText       string
	responseText    string
	receivedAt      time.Time
	headerAt        time.Time
	firstBodyByteAt time.Time
	clientClosedAt  time.Time
	finishedAt      time.Time
	bytesWritten    int64
	completed       bool
}

// streamNetworkServer is a real loopback HTTP server that speaks the
// OpenAI-compatible SSE wire format. Mode selects the failure it injects into
// the first request only:
//
//	ttft      - writes no response byte at all, so the client never gets a
//	            first event (the TTFT watchdog must cancel it).
//	inactive  - sends one SSE delta, flushes it, then goes silent (the
//	            inter-event watchdog must cancel it).
//	healthy   - answers every request with a complete SSE stream.
//
// A worker's successful stream ends in a report_to_parent tool call, which is
// how a worker turn legitimately terminates; the overseer's ends in text.
type streamNetworkServer struct {
	*httptest.Server
	mode string
	role string

	mu      sync.Mutex
	records []*networkRequestObservation
	wg      sync.WaitGroup
}

func newStreamNetworkServer(t *testing.T, mode, role string) *streamNetworkServer {
	t.Helper()
	s := &streamNetworkServer{mode: mode, role: role}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Server.Close)
	return s
}

func (s *streamNetworkServer) handle(w http.ResponseWriter, r *http.Request) {
	s.wg.Add(1)
	defer s.wg.Done()

	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	rec := &networkRequestObservation{receivedAt: time.Now(), path: r.URL.Path}
	rec.messageCount, rec.streamRequested, rec.model, rec.nudgeText = inspectNetworkRequestBody(raw)

	s.mu.Lock()
	rec.index = len(s.records)
	s.records = append(s.records, rec)
	s.mu.Unlock()

	flusher, _ := w.(http.Flusher)
	stall := s.mode != "healthy" && rec.index == 0

	if stall && s.mode == "inactive" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		s.markHeader(rec)
		s.setResponseText(rec, "stalled-partial")
		s.writeSSE(w, flusher, rec, ssePartialChunk)
		// Deliberately silent from here: only the inter-event watchdog can
		// end this stream.
	}
	if stall {
		// ttft: nothing is written at all — no status line, no header, no
		// body byte. The request stays parked until the watchdog cancels it.
		<-r.Context().Done()
		s.mu.Lock()
		rec.clientClosedAt = time.Now()
		s.mu.Unlock()
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	s.markHeader(rec)
	s.setResponseText(rec, "recovered")
	s.writeSSE(w, flusher, rec, sseRecoveredChunk)
	if s.role == "worker" {
		// A worker turn ends by reporting to the parent; the retried call is
		// the same inner turn, so its terminal action is the report.
		s.writeSSE(w, flusher, rec, sseWorkerReportChunk())
	} else {
		s.writeSSE(w, flusher, rec, sseStopChunk)
	}
	s.writeSSE(w, flusher, rec, sseUsageChunk)
	s.writeSSE(w, flusher, rec, "[DONE]")

	s.mu.Lock()
	rec.completed = true
	rec.finishedAt = time.Now()
	s.mu.Unlock()
}

func (s *streamNetworkServer) markHeader(rec *networkRequestObservation) {
	s.mu.Lock()
	rec.headerAt = time.Now()
	s.mu.Unlock()
}

func (s *streamNetworkServer) setResponseText(rec *networkRequestObservation, text string) {
	s.mu.Lock()
	rec.responseText = text
	s.mu.Unlock()
}

// sseWorkerReportChunk is the terminal tool call a worker's successful stream
// carries. Build it through json.Marshal so the arguments string is valid JSON
// without hand-escaped quotes.
func sseWorkerReportChunk() string {
	arguments, _ := json.Marshal(`{"status":"success","summary":"recovered from the stalled stream","data":{}}`)
	return `{"id":"chatcmpl-net-e2e","object":"chat.completion.chunk","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_net_e2e","type":"function","function":{"name":"report_to_parent","arguments":` +
		string(arguments) + `}}]},"finish_reason":null}]}`
}

func (s *streamNetworkServer) writeSSE(w http.ResponseWriter, flusher http.Flusher, rec *networkRequestObservation, payload string) {
	n, err := io.WriteString(w, "data: "+payload+"\n\n")
	s.mu.Lock()
	if rec.firstBodyByteAt.IsZero() {
		rec.firstBodyByteAt = time.Now()
	}
	rec.bytesWritten += int64(n)
	s.mu.Unlock()
	if err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
}

// waitHandlers gives in-flight handlers a bounded chance to finish before the
// test snapshots them, so `completed` and finish times are not racy.
func (s *streamNetworkServer) waitHandlers(t *testing.T, timeout time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Errorf("network server still had in-flight handlers after %v", timeout)
	}
}

func (s *streamNetworkServer) snapshot() []*networkRequestObservation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*networkRequestObservation, len(s.records))
	copy(out, s.records)
	return out
}

// inspectNetworkRequestBody extracts the streaming flag, model, message count
// and a flattened message text (used to prove the continue-nudge reached the
// retried call).
func inspectNetworkRequestBody(raw []byte) (messages int, stream bool, model string, text string) {
	var payload struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return 0, false, "", ""
	}
	var builder strings.Builder
	for _, message := range payload.Messages {
		builder.WriteString(collectNetworkContentText(message.Content))
		builder.WriteString("\n")
	}
	return len(payload.Messages), payload.Stream, payload.Model, builder.String()
}

func collectNetworkContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var builder strings.Builder
		for _, block := range blocks {
			if block.Text != "" {
				builder.WriteString(block.Text)
				builder.WriteString("\n")
			}
		}
		return builder.String()
	}
	return ""
}

type networkBudgets struct {
	TTFTSeconds       float64 `json:"ttft_seconds"`
	InterEventSeconds float64 `json:"inter_event_seconds"`
}

type networkRequestEvidence struct {
	Index              int     `json:"index"`
	Path               string  `json:"path"`
	Model              string  `json:"model"`
	StreamRequested    bool    `json:"stream_requested"`
	MessageCount       int     `json:"message_count"`
	NudgeSeen          bool    `json:"nudge_seen"`
	NudgeExcerpt       string  `json:"nudge_excerpt,omitempty"`
	ResponseText       string  `json:"response_text,omitempty"`
	ReceivedAt         string  `json:"received_at"`
	HeaderAt           string  `json:"header_at,omitempty"`
	FirstBodyByteAt    string  `json:"first_body_byte_at,omitempty"`
	ClientClosedAt     string  `json:"client_closed_at,omitempty"`
	FinishedAt         string  `json:"finished_at,omitempty"`
	ObservedDurationMS float64 `json:"observed_duration_ms"`
	BytesWritten       int64   `json:"bytes_written"`
	Completed          bool    `json:"completed"`
}

type networkNudgeEvidence struct {
	Reason         string  `json:"reason"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	LimitSeconds   float64 `json:"limit_seconds"`
	NudgeCount     int     `json:"nudge_count"`
}

type networkTurnEvidence struct {
	Success    bool    `json:"success"`
	FinalText  string  `json:"final_text"`
	InnerTurns int     `json:"inner_turns"`
	Error      string  `json:"error,omitempty"`
	DurationMS float64 `json:"duration_ms"`
}

type networkCaseEvidence struct {
	Case                     string                   `json:"case"`
	Role                     string                   `json:"role"`
	ServerAddr               string                   `json:"server_addr"`
	WatchdogBudgetsSeconds   networkBudgets           `json:"watchdog_budgets_seconds"`
	Requests                 []networkRequestEvidence `json:"requests"`
	Nudges                   []networkNudgeEvidence   `json:"nudges"`
	Turn                     networkTurnEvidence      `json:"turn"`
	CounterStreamStallNudged int                      `json:"counter_stream_stall_nudged"`
	WallClockMS              float64                  `json:"wall_clock_ms"`
}

type networkEvidenceFile struct {
	GeneratedAt   string                `json:"generated_at"`
	GoVersion     string                `json:"go_version"`
	GOOS          string                `json:"goos"`
	GOARCH        string                `json:"goarch"`
	GOMAXPROCS    int                   `json:"gomaxprocs"`
	BudgetSeconds networkBudgets        `json:"watchdog_budgets_seconds"`
	Cases         []networkCaseEvidence `json:"cases"`
}

func networkTimestamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func networkRequestEvidenceFor(rec *networkRequestObservation) networkRequestEvidence {
	durationBase := rec.clientClosedAt
	if rec.completed {
		durationBase = rec.finishedAt
	}
	var observedMS float64
	if !durationBase.IsZero() {
		observedMS = float64(durationBase.Sub(rec.receivedAt)) / float64(time.Millisecond)
	}
	nudgeSeen := strings.Contains(rec.nudgeText, "previous stream stalled")
	excerpt := ""
	if idx := strings.Index(rec.nudgeText, "previous stream stalled"); idx >= 0 {
		start := idx - 40
		if start < 0 {
			start = 0
		}
		end := idx + 200
		if end > len(rec.nudgeText) {
			end = len(rec.nudgeText)
		}
		excerpt = strings.TrimSpace(rec.nudgeText[start:end])
	}
	return networkRequestEvidence{
		Index:              rec.index,
		Path:               rec.path,
		Model:              rec.model,
		StreamRequested:    rec.streamRequested,
		MessageCount:       rec.messageCount,
		NudgeSeen:          nudgeSeen,
		NudgeExcerpt:       excerpt,
		ResponseText:       rec.responseText,
		ReceivedAt:         networkTimestamp(rec.receivedAt),
		HeaderAt:           networkTimestamp(rec.headerAt),
		FirstBodyByteAt:    networkTimestamp(rec.firstBodyByteAt),
		ClientClosedAt:     networkTimestamp(rec.clientClosedAt),
		FinishedAt:         networkTimestamp(rec.finishedAt),
		ObservedDurationMS: observedMS,
		BytesWritten:       rec.bytesWritten,
		Completed:          rec.completed,
	}
}

// TestWorkerOverseerStreamStallRealNetwork is the network-layer E2E: a real
// loopback HTTP server withholds the SSE response (before the first byte, or
// after the first event), the production worker/overseer loop trips its
// watchdog, injects the STREAM_STALLED nudge and re-streams the same inner
// turn, which then completes. No external model or network is contacted.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop (watchdog
// verdicts + continue-nudge re-stream), framework/host/colony_runtime.py
// (worker/overseer loops).
func TestWorkerOverseerStreamStallRealNetwork(t *testing.T) {
	outDir := strings.TrimSpace(os.Getenv(streamNetworkEvidenceEnv))
	evidence := &networkEvidenceFile{
		GeneratedAt: time.Now().Format(time.RFC3339Nano),
		GoVersion:   runtime.Version(),
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		GOMAXPROCS:  runtime.GOMAXPROCS(0),
		BudgetSeconds: networkBudgets{
			TTFTSeconds:       streamNetworkBudget.Seconds(),
			InterEventSeconds: streamNetworkBudget.Seconds(),
		},
	}
	defer func() {
		if outDir == "" {
			return
		}
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			t.Errorf("create evidence dir %s: %v", outDir, err)
			return
		}
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Errorf("encode evidence: %v", err)
			return
		}
		path := filepath.Join(outDir, "results.json")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			t.Errorf("write evidence %s: %v", path, err)
			return
		}
		t.Logf("stream-network evidence written to %s", path)
	}()

	for _, mode := range []string{"ttft", "inactive", "healthy"} {
		for _, role := range []string{"worker", "overseer"} {
			mode, role := mode, role
			t.Run(role+"/"+mode, func(t *testing.T) {
				evidence.Cases = append(evidence.Cases, runStreamNetworkCase(t, role, mode))
			})
		}
	}
}

func runStreamNetworkCase(t *testing.T, role, mode string) networkCaseEvidence {
	t.Helper()

	server := newStreamNetworkServer(t, mode, role)
	provider := llm.NewProvider(llm.ProviderOptions{
		Model:   "gpt-4o-mini",
		APIKey:  "test-key",
		APIBase: server.URL,
	})

	cfg := agentloop.DefaultLoopConfig()
	cfg.MaxIterations = 3
	cfg.LLMStreamTTFTTimeoutSeconds = streamNetworkBudget.Seconds()
	cfg.LLMStreamInterEventIdleSeconds = streamNetworkBudget.Seconds()
	// Zero disables the deprecated inactivity alias, so the two budgets under
	// test are exactly the pair installed on the completer.
	cfg.LLMStreamInactivityTimeoutSeconds = 0
	cfg.ContinueNudgeEnabled = true
	cfg.ContinueNudgeMaxPerTurn = 2
	cfg.SessionIdleNudgeSeconds = 3600

	var (
		loop     *agentloop.AgentLoop
		agentCtx *agentloop.AgentContext
		conv     *agentloop.NodeConversation
		stop     func()
	)
	switch role {
	case "worker":
		colonyRuntime := NewColonyRuntime(RuntimeOptions{
			AgentSpec:   testAgentSpec(),
			StoragePath: t.TempDir(),
			StreamID:    "colony_worker_stream_network",
		})
		worker := NewWorker("w1", "do the thing", "worker:w1")
		worker.StoragePath = colonyRuntime.workerStorage(worker.ID)
		if err := os.MkdirAll(worker.StoragePath, 0o755); err != nil {
			t.Fatalf("mkdir worker dir: %v", err)
		}
		worker.Context = colonyRuntime.buildContext(worker, colonyRuntime.agentSpec)
		worker.Context.LLM = provider
		worker.conversation = agentloop.NewNodeConversation(
			colonyRuntime.workerSystemPrompt(worker, colonyRuntime.agentSpec))
		loop, stop = colonyRuntime.newWorkerAgentLoop(worker, cfg, nil)
		agentCtx, conv = worker.Context, worker.conversation
	case "overseer":
		colonyRuntime := NewColonyRuntime(RuntimeOptions{
			AgentSpec:   testAgentSpec(),
			StoragePath: t.TempDir(),
			StreamID:    "colony_overseer_stream_network",
		})
		worker := NewWorker("overseer:colony_overseer_stream_network", "", "overseer")
		worker.Persistent = true
		worker.StoragePath = colonyRuntime.storagePath
		worker.Context = colonyRuntime.buildContext(worker, colonyRuntime.agentSpec)
		worker.Context.LLM = provider
		worker.conversation = agentloop.NewNodeConversation(systemPromptFor(colonyRuntime.agentSpec))
		loop = agentloop.NewAgentLoop(cfg, colonyRuntime.executorFor(worker))
		stop = colonyRuntime.startOverseerReminders(loop, worker, cfg, nil)
		agentCtx, conv = worker.Context, worker.conversation
	default:
		t.Fatalf("unknown role %q", role)
	}
	defer stop()

	// Prove the adapter is installed on the seam the loop actually calls: the
	// provider is streaming-capable, so production wiring must have wrapped it.
	if _, ok := agentCtx.LLM.(*agentloop.StreamingCompleter); !ok {
		t.Fatalf("%s LLM = %T, want agentloop.StreamingCompleter", role, agentCtx.LLM)
	}

	var (
		nudgesMu sync.Mutex
		nudges   []agentloop.StreamStallNudge
	)
	loop.OnStreamStallNudge = func(nudge agentloop.StreamStallNudge) {
		nudgesMu.Lock()
		nudges = append(nudges, nudge)
		nudgesMu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	startedAt := time.Now()
	result, turnErr := loop.ExecuteTurn(ctx, agentCtx, conv, nil)
	wallClock := time.Since(startedAt)

	// The stalled handler exits as soon as the watchdog cancels the request,
	// and the final answer's handler returns after [DONE]; both must be done
	// before the snapshot is meaningful.
	server.waitHandlers(t, 5*time.Second)
	records := server.snapshot()

	nudgesMu.Lock()
	recordedNudges := append([]agentloop.StreamStallNudge(nil), nudges...)
	nudgesMu.Unlock()

	caseEvidence := networkCaseEvidence{
		Case:       mode,
		Role:       role,
		ServerAddr: server.Listener.Addr().String(),
		WatchdogBudgetsSeconds: networkBudgets{
			TTFTSeconds:       streamNetworkBudget.Seconds(),
			InterEventSeconds: streamNetworkBudget.Seconds(),
		},
		Turn: networkTurnEvidence{
			Success:    result.Success,
			FinalText:  result.FinalText,
			InnerTurns: result.InnerTurns,
			DurationMS: float64(wallClock) / float64(time.Millisecond),
		},
		CounterStreamStallNudged: loop.Counter("stream_stall_nudged"),
		WallClockMS:              float64(wallClock) / float64(time.Millisecond),
	}
	if turnErr != nil {
		caseEvidence.Turn.Error = turnErr.Error()
	}
	for _, record := range records {
		caseEvidence.Requests = append(caseEvidence.Requests, networkRequestEvidenceFor(record))
	}
	for _, nudge := range recordedNudges {
		caseEvidence.Nudges = append(caseEvidence.Nudges, networkNudgeEvidence{
			Reason:         nudge.Reason,
			ElapsedSeconds: nudge.ElapsedSeconds,
			LimitSeconds:   nudge.LimitSeconds,
			NudgeCount:     nudge.NudgeCount,
		})
	}
	t.Logf("stream-network %s/%s: requests=%d nudges=%d counter=%d success=%v final=%q bytes=%v",
		role, mode, len(records), len(recordedNudges), caseEvidence.CounterStreamStallNudged,
		result.Success, result.FinalText, requestBytes(records))

	if turnErr != nil {
		t.Fatalf("ExecuteTurn returned error: %v (turn=%+v)", turnErr, result)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	if role == "overseer" && result.FinalText != "recovered" {
		t.Fatalf("overseer final text = %q, want the retried stream's answer", result.FinalText)
	}
	if len(records) == 0 {
		t.Fatalf("network server saw no requests; the probe provider never reached the socket")
	}
	for i, record := range records {
		if record.path != "/v1/chat/completions" {
			t.Errorf("request %d path = %q, want /v1/chat/completions", i, record.path)
		}
		if !record.streamRequested {
			t.Errorf("request %d did not ask for stream:true; the streaming transport was bypassed", i)
		}
	}

	switch mode {
	case "ttft", "inactive":
		if len(records) < 2 {
			t.Fatalf("requests = %d, want the stalled call plus at least one retry", len(records))
		}
		stalled := records[0]
		if stalled.clientClosedAt.IsZero() {
			t.Errorf("stalled request never observed a client disconnect; the watchdog did not cancel it")
		}
		observed := stalled.clientClosedAt.Sub(stalled.receivedAt)
		if observed < streamNetworkBudget*4/5 {
			t.Errorf("socket-level stall lasted %v, want it bounded by the %v watchdog budget", observed, streamNetworkBudget)
		}
		switch mode {
		case "ttft":
			if stalled.bytesWritten != 0 || !stalled.firstBodyByteAt.IsZero() {
				t.Errorf("ttft probe wrote %d body bytes (first at %v), want a completely withheld response",
					stalled.bytesWritten, networkTimestamp(stalled.firstBodyByteAt))
			}
		case "inactive":
			if stalled.bytesWritten == 0 || stalled.firstBodyByteAt.IsZero() {
				t.Errorf("inactive probe wrote no first event, so the inter-event budget was never the one exercised")
			}
			if stalled.completed {
				t.Errorf("inactive probe completed, want it silenced after the first event")
			}
		}

		if len(recordedNudges) != 1 {
			t.Fatalf("stream-stall nudges = %d (%+v), want exactly 1", len(recordedNudges), recordedNudges)
		}
		nudge := recordedNudges[0]
		if nudge.Reason != mode {
			t.Errorf("nudge reason = %q, want %q", nudge.Reason, mode)
		}
		if delta := nudge.LimitSeconds - streamNetworkBudget.Seconds(); delta > 1e-9 || delta < -1e-9 {
			t.Errorf("nudge limit = %vs, want %vs", nudge.LimitSeconds, streamNetworkBudget.Seconds())
		}
		if nudge.ElapsedSeconds < streamNetworkBudget.Seconds()*4/5 {
			t.Errorf("nudge elapsed = %vs, want >= %vs", nudge.ElapsedSeconds, streamNetworkBudget.Seconds()*4/5)
		}

		retry := records[1]
		if !retry.streamRequested {
			t.Errorf("retry did not stream")
		}
		if !strings.Contains(retry.nudgeText, "previous stream stalled") {
			t.Errorf("retried call did not carry the STREAM_STALLED continue-nudge")
		}
		wantLabel := map[string]string{
			"ttft":     "no tokens before TTFT budget",
			"inactive": "stream went silent after producing events",
		}[mode]
		if !strings.Contains(retry.nudgeText, wantLabel) {
			t.Errorf("retried call nudge missing reason label %q", wantLabel)
		}
		if retry.firstBodyByteAt.IsZero() || !retry.completed || retry.bytesWritten == 0 {
			t.Errorf("retry stream did not deliver body bytes: first=%v completed=%v bytes=%d",
				networkTimestamp(retry.firstBodyByteAt), retry.completed, retry.bytesWritten)
		}
		if !strings.Contains(retry.responseText, "recovered") {
			t.Errorf("retried stream response text = %q, want the recovered answer", retry.responseText)
		}
		// The retry must be the same inner turn: the stall recovery rewinds
		// the loop counter, and a worker's report_to_parent ends it. Exactly
		// two model calls therefore prove "cancel + re-stream", not a new turn.
		if len(records) != 2 {
			t.Errorf("requests = %d, want exactly 2 (stalled call + same-turn retry)", len(records))
		}
		if result.InnerTurns != 1 {
			t.Errorf("inner turns = %d, want 1 (the retry must not consume a new turn)", result.InnerTurns)
		}
		if got := loop.Counter("stream_stall_nudged"); got != 1 {
			t.Errorf("stream_stall_nudged = %d, want 1", got)
		}
	case "healthy":
		if len(recordedNudges) != 0 {
			t.Errorf("healthy stream produced nudges: %+v", recordedNudges)
		}
		if got := loop.Counter("stream_stall_nudged"); got != 0 {
			t.Errorf("healthy stream counted %d nudges, want 0", got)
		}
		first := records[0]
		if !first.completed || first.bytesWritten == 0 || first.firstBodyByteAt.IsZero() {
			t.Errorf("healthy stream did not deliver: completed=%v bytes=%d first=%v",
				first.completed, first.bytesWritten, networkTimestamp(first.firstBodyByteAt))
		}
		if len(records) != 1 {
			t.Errorf("healthy requests = %d, want 1 (no stall, no retry)", len(records))
		}
		if result.InnerTurns != 1 {
			t.Errorf("healthy inner turns = %d, want 1", result.InnerTurns)
		}
		for i, record := range records {
			if strings.Contains(record.nudgeText, "previous stream stalled") {
				t.Errorf("healthy request %d carried a stall nudge", i)
			}
		}
	}

	return caseEvidence
}

func requestBytes(records []*networkRequestObservation) []int64 {
	out := make([]int64, 0, len(records))
	for _, record := range records {
		out = append(out, record.bytesWritten)
	}
	return out
}
