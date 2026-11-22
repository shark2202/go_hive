//go:build e2e

package e2e

// Worker escalation end-to-end: the real `hive serve` binary, a colony bound to
// the session, and one worker whose repeated tool call trips the deterministic
// doom-loop detector. Parallel workers have no manual escalate tool (upstream
// gates it to the legacy `worker` stream), so the doom-loop producer is the
// production channel under test:
//
//	worker repeats one call -> ESCALATION_REQUESTED -> colony-scoped routing on
//	the queen session -> list_worker_questions -> reply_to_worker -> worker
//	resumes and reports.
//
// Only the models are scripted (e2e/escalationgw, a deterministic local
// OpenAI-compatible gateway); everything else is the real runtime, real HTTP and
// a real on-disk colony.
//
// Run:
//
//	CGO_ENABLED=0 GOPROXY=off go test -tags e2e ./e2e -run TestWorkerEscalationEndToEnd -v -count=1
//	HIVE_ESCALATION_E2E_OUT=/tmp/escalation-e2e go test -tags e2e ./e2e -run ... -v
//
// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_escalation_requested)
// Parity: core/tests/test_run_worker_tool.py::test_parallel_worker_stall_auto_fails_with_synthetic_report

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/e2e/escalationgw"
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

const (
	escalationE2EModel    = "escalation-e2e-model"
	escalationE2EColonyID = "escalation_e2e"
	escalationE2EUser     = "Dispatch a worker to query the ledger."
	escalationDoomNotice  = "[SYSTEM] Escalated tool doom loop to queen for intervention."
)

func escalationE2EChildEnv(homeDir string) []string {
	dropped := map[string]bool{
		"OPENAI_API_KEY": true, "OPENAI_BASE_URL": true, "OPENAI_API_BASE": true,
		"HIVE_API_KEY": true, "HIVE_HOME": true, "HIVE_DESKTOP_TOKEN": true,
		"HIVE_DESKTOP_PARENT_PID": true, "HIVE_LLM_BASE_URL": true,
	}
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			name = entry[:index]
		}
		if dropped[name] {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "HIVE_HOME="+homeDir)
}

func escalationE2EOutRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("HIVE_ESCALATION_E2E_OUT")
	if root == "" {
		root = t.TempDir()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create evidence root: %v", err)
	}
	return root
}

// writeEscalationColony materialises the smallest colony that is a real worker:
// metadata.json plus a worker definition the colony host loads.
func writeEscalationColony(t *testing.T, homeDir string) string {
	t.Helper()
	colonyPath := filepath.Join(homeDir, "colonies", escalationE2EColonyID)
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("create colony dir: %v", err)
	}
	metadata := map[string]any{"name": escalationE2EColonyID, "created_at": float64(time.Now().Unix())}
	writeJSONFile(t, filepath.Join(colonyPath, "metadata.json"), metadata)
	worker := map[string]any{
		"name":          "ledger_worker",
		"description":   "Queries the ledger database",
		"system_prompt": "You are a focused ledger worker. Report when done; repeating a call does not unblock you.",
		"tools":         []string{},
		"goal":          map[string]any{"description": "Query the ledger database"},
	}
	writeJSONFile(t, filepath.Join(colonyPath, "worker.json"), worker)
	return colonyPath
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type escalationE2ERun struct {
	t       *testing.T
	outDir  string
	home    string
	colony  string
	gw      *escalationgw.Server
	baseURL string
	client  *http.Client
	logs    *credLogBuffer
	session string
}

func (r *escalationE2ERun) doJSON(method, path string, payload any) (int, []byte) {
	return credDoJSON(r.t, r.client, method, r.baseURL+path, payload)
}

func (r *escalationE2ERun) sessionDir() string {
	r.t.Helper()
	matches, err := filepath.Glob(filepath.Join(r.home, "queens", "*", "sessions", r.session))
	if err != nil || len(matches) == 0 {
		r.t.Fatalf("session dir for %s not found (err=%v)", r.session, err)
	}
	return matches[0]
}

func (r *escalationE2ERun) queenParts() []map[string]any {
	r.t.Helper()
	parts, err := agentloop.NewFileConversationStore(filepath.Join(r.sessionDir(), "conversations")).ReadParts()
	if err != nil {
		r.t.Fatalf("read queen transcript: %v", err)
	}
	return parts
}

// workerDir finds the spawned worker's on-disk home (<colony>/workers/<id>).
func (r *escalationE2ERun) workerDir() string {
	r.t.Helper()
	matches, err := filepath.Glob(filepath.Join(r.colony, "workers", "worker_*"))
	if err != nil || len(matches) == 0 {
		r.t.Fatalf("worker dir not found under %s (err=%v)", r.colony, err)
	}
	return matches[0]
}

func (r *escalationE2ERun) workerParts() []map[string]any {
	r.t.Helper()
	parts, err := agentloop.NewFileConversationStore(filepath.Join(r.workerDir(), "conversations")).ReadParts()
	if err != nil {
		r.t.Fatalf("read worker transcript: %v", err)
	}
	return parts
}

// waitForModelRequest waits until a recorded request satisfies want.
func (r *escalationE2ERun) waitForModelRequest(what string, want func(escalationgw.Record) bool) escalationgw.Record {
	r.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		for _, record := range r.gw.Requests() {
			if want(record) {
				return record
			}
		}
		if time.Now().After(deadline) {
			var seen strings.Builder
			for _, record := range r.gw.Requests() {
				fmt.Fprintf(&seen, "seq %d kind=%s tools=%d response=%q tools=%s\n", record.Seq, record.Kind, len(record.Tools), record.Response, strings.Join(record.Tools, ","))
			}
			r.t.Fatalf("no model request %s within 45s; recorded requests:\n%s\nlogs:\n%s", what, seen.String(), r.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// requestCarries reports whether the request's decoded body carries needle in
// any message content (system or user) or in a tool result.
func escalationE2ERequestCarries(t *testing.T, record escalationgw.Record, needle string) bool {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(record.Body), &body); err != nil {
		return false
	}
	messages, _ := body["messages"].([]any)
	return credMessagesContain(messages, needle)
}

func escalationE2ERecordRespondedWith(record escalationgw.Record, tool string) bool {
	for _, name := range record.ToolCalls {
		if name == tool {
			return true
		}
	}
	return false
}

// escalationE2ERecordToolArgs returns the arguments of the record's own first
// tool call (its response, not its history), so the test can count the
// identical calls that tripped the doom-loop detector.
func escalationE2ERecordToolArgs(t *testing.T, record escalationgw.Record) (string, string) {
	t.Helper()
	var message map[string]any
	if err := json.Unmarshal([]byte(record.Message), &message); err != nil {
		return "", ""
	}
	calls, _ := message["tool_calls"].([]any)
	if len(calls) == 0 {
		return "", ""
	}
	call, _ := calls[0].(map[string]any)
	function, _ := call["function"].(map[string]any)
	name, _ := function["name"].(string)
	arguments, _ := function["arguments"].(string)
	return name, arguments
}

func countPartsContaining(parts []map[string]any, needle string) int {
	count := 0
	for _, part := range parts {
		if content, _ := part["content"].(string); strings.Contains(content, needle) {
			count++
		}
	}
	return count
}

// Parity: core/framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom
// loop -> emit_escalation_requested), then the session handoff chain.
func TestWorkerEscalationEndToEnd(t *testing.T) {
	binary := stallE2EBuild(t)

	gw := escalationgw.New(escalationgw.Options{Model: escalationE2EModel})
	gwServer := httptest.NewServer(gw.Handler())
	t.Cleanup(gwServer.Close)

	outDir := filepath.Join(escalationE2EOutRoot(t), "escalation-chain")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}

	homeDir := filepath.Join(t.TempDir(), "hive-home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("create hive home: %v", err)
	}
	colonyPath := writeEscalationColony(t, homeDir)
	configuration := map[string]any{
		"llm": map[string]any{
			"provider": "custom",
			"model":    "ollama/" + escalationE2EModel,
			"api_base": gwServer.URL + "/v1",
		},
	}
	writeJSONFile(t, filepath.Join(homeDir, "configuration.json"), configuration)

	port := credFreePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	serverCmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	serverCmd.Dir = t.TempDir()
	serverCmd.Env = escalationE2EChildEnv(homeDir)
	serverCmd.Stdout = logs
	serverCmd.Stderr = logs
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start hive serve: %v", err)
	}
	exited := make(chan struct{})
	run := &escalationE2ERun{
		t: t, outDir: outDir, home: homeDir, colony: colonyPath,
		gw: gw, baseURL: baseURL, client: &http.Client{Timeout: 60 * time.Second}, logs: logs,
	}
	t.Cleanup(func() {
		credStopHive(t, serverCmd, exited, logs)
		if err := os.WriteFile(filepath.Join(outDir, "server-output.txt"), []byte(logs.String()), 0o644); err != nil {
			t.Errorf("write server log evidence: %v", err)
		}
		run.dumpModelRequests()
	})
	go func() {
		_ = serverCmd.Wait()
		close(exited)
	}()

	deadline := time.Now().Add(30 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("hive serve exited before /api/health; logs:\n%s", logs.String())
		default:
		}
		resp, err := run.client.Get(baseURL + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("hive serve did not answer /api/health; logs:\n%s", logs.String())
	}

	// 1. Colony-bound session.
	status, raw := run.doJSON(http.MethodPost, "/api/sessions", map[string]any{"colony_id": escalationE2EColonyID})
	var created map[string]any
	_ = json.Unmarshal(raw, &created)
	sessionID, _ := created["session_id"].(string)
	if (status != http.StatusOK && status != http.StatusCreated) || sessionID == "" {
		t.Fatalf("POST /api/sessions = %d %s", status, raw)
	}
	run.session = sessionID

	// 2. Drive the queen: her scripted first turn dispatches a worker.
	status, raw = run.doJSON(http.MethodPost, "/api/sessions/"+sessionID+"/chat", map[string]any{"message": escalationE2EUser})
	if status != http.StatusOK {
		t.Fatalf("POST /chat = %d %s; logs:\n%s", status, raw, logs.String())
	}

	// 3. The worker's surface has no manual escalate tool (parallel workers
	// fail fast); its first calls are one identical task_list, which is what
	// trips the deterministic detector.
	repeating := run.waitForModelRequest("from the worker calling task_list", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindWorker && escalationE2ERecordRespondedWith(record, "task_list")
	})
	if containsString(repeating.Tools, "escalate") {
		t.Fatalf("worker tool surface = %v, must not advertise escalate", repeating.Tools)
	}
	if !containsString(repeating.Tools, "report_to_parent") {
		t.Fatalf("worker tool surface = %v, want report_to_parent", repeating.Tools)
	}

	// 4. The escalation reaches the queen as an addressed handoff, carrying the
	// doom-loop reason.
	handoff := run.waitForModelRequest("carrying [WORKER_ESCALATION] for the queen", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindQueen && escalationE2ERequestCarries(t, record, "[WORKER_ESCALATION]")
	})
	if !escalationE2ERequestCarries(t, handoff, escalationgw.DefaultDoomLoopReason) {
		t.Fatalf("the queen's handoff does not carry the doom-loop reason:\n%s", handoff.Body)
	}
	requestID := escalationRequestIDFromRecord(t, handoff)
	if requestID == "" {
		t.Fatalf("the queen's escalation turn carries no request_id:\n%s", handoff.Body)
	}

	// 6. The queen lists the pending question.
	listed := run.waitForModelRequest("where the queen calls list_worker_questions", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindQueen && escalationE2ERecordRespondedWith(record, "list_worker_questions")
	})
	if listed.Seq < handoff.Seq {
		t.Fatalf("list_worker_questions (seq %d) preceded the handoff (seq %d)", listed.Seq, handoff.Seq)
	}

	// 7. The queen answers it with reply_to_worker, using the request_id from
	// the listed inbox.
	replied := run.waitForModelRequest("where the queen calls reply_to_worker", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindQueen && escalationE2ERecordRespondedWith(record, "reply_to_worker")
	})
	if !escalationE2ERequestCarries(t, replied, requestID) {
		t.Fatalf("reply_to_worker turn does not carry the escalated request_id %s", requestID)
	}
	if replied.Seq <= listed.Seq {
		t.Fatalf("reply_to_worker (seq %d) did not follow list_worker_questions (seq %d)", replied.Seq, listed.Seq)
	}
	if !strings.Contains(replied.Message, escalationgw.DefaultQueenGuidance) {
		t.Fatalf("reply_to_worker call does not carry the guidance text: %s", replied.Message)
	}

	// 8. The worker resumes with the queen's reply and reports.
	resumed := run.waitForModelRequest("from the worker carrying [QUEEN_REPLY]", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindWorker && escalationE2ERequestCarries(t, record, "[QUEEN_REPLY]")
	})
	if !escalationE2ERequestCarries(t, resumed, requestID) {
		t.Fatalf("resumed worker turn does not carry the request_id %s", requestID)
	}
	if !escalationE2ERecordRespondedWith(resumed, "report_to_parent") {
		t.Fatalf("resumed worker turn did not report; tool calls = %v", resumed.ToolCalls)
	}

	// 9a. Exactly one identical call per threshold step: three repeats tripped
	// the detector (4+ would have parked the worker), and every call after the
	// trip used different arguments.
	identical, varied := 0, 0
	for _, record := range gw.RequestsOfKind(escalationgw.KindWorker) {
		name, arguments := escalationE2ERecordToolArgs(t, record)
		if name != "task_list" {
			continue
		}
		if arguments == "{}" {
			identical++
		} else {
			varied++
		}
	}
	if identical != 3 {
		t.Fatalf("identical task_list calls = %d, want exactly 3 (the detector threshold)", identical)
	}
	if varied < 1 {
		t.Fatal("the worker never changed arguments after the doom-loop escalation")
	}

	// 10. The colony lead sees the worker's terminal report.
	run.waitForModelRequest("from the colony lead carrying [WORKER_REPORT]", func(record escalationgw.Record) bool {
		if record.Kind != escalationgw.KindOverseer && record.Kind != escalationgw.KindQueen {
			return false
		}
		return escalationE2ERequestCarries(t, record, "[WORKER_REPORT]")
	})

	// 11. Durable evidence on disk: the handoff is a transcript record for the
	// queen (so a restart rebuilds what the user saw), the worker's own
	// conversation holds the escalation notice and the reply, and the notice
	// appears exactly once (no duplicate/escalation storm).
	parts := run.queenParts()
	handoffs := countPartsContaining(parts, "[WORKER_ESCALATION]")
	if handoffs != 1 {
		t.Fatalf("queen transcript carries %d [WORKER_ESCALATION] records, want exactly 1", handoffs)
	}
	workerParts := run.workerParts()
	if !partsContain(workerParts, "", "[QUEEN_REPLY] request_id="+requestID) {
		t.Fatal("worker transcript does not record the queen's reply")
	}
	notices := countPartsContaining(workerParts, escalationDoomNotice)
	if notices != 1 {
		t.Fatalf("worker transcript carries %d doom-loop escalation notices, want exactly 1", notices)
	}

	// 12. The session event log carries the same detection as
	// node_tool_doom_loop exactly once: published on the worker's own stream
	// and mirrored onto the session bus, so GET /events/history sees what the
	// transcript already shows.
	doom := waitForSessionDoomLoop(t, run)
	streamID, _ := doom["stream_id"].(string)
	if !strings.HasPrefix(streamID, "worker:") {
		t.Fatalf("node_tool_doom_loop stream_id = %q, want worker:<id>", streamID)
	}
	if nodeID, _ := doom["node_id"].(string); nodeID != streamID {
		t.Fatalf("node_tool_doom_loop node_id = %q, want the worker stream %q", nodeID, streamID)
	}
	if colonyID, _ := doom["colony_id"].(string); colonyID != escalationE2EColonyID {
		t.Fatalf("node_tool_doom_loop colony_id = %q, want %q", colonyID, escalationE2EColonyID)
	}
	doomData, _ := doom["data"].(map[string]any)
	description, _ := doomData["description"].(string)
	if !strings.Contains(description, "task_list") || !strings.Contains(description, "3 times") {
		t.Fatalf("node_tool_doom_loop description = %q, want the repeating call and streak", description)
	}

	run.writeEvidence(handoff, listed, replied, resumed, repeating)
}

// sessionDoomLoopEvents parses the session event log's node_tool_doom_loop
// lines. Unparseable (mid-write) lines are skipped; the caller polls.
func (r *escalationE2ERun) sessionDoomLoopEvents() []map[string]any {
	raw, err := os.ReadFile(filepath.Join(r.sessionDir(), "events.jsonl"))
	if err != nil {
		return nil
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if !strings.Contains(line, `"node_tool_doom_loop"`) {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events
}

func waitForSessionDoomLoop(t *testing.T, run *escalationE2ERun) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		events := run.sessionDoomLoopEvents()
		if len(events) == 1 {
			return events[0]
		}
		if len(events) > 1 {
			t.Fatalf("session event log carries %d node_tool_doom_loop events, want exactly 1", len(events))
		}
		if time.Now().After(deadline) {
			t.Fatalf("session event log never carried a node_tool_doom_loop event; server logs:\n%s", run.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func partsContain(parts []map[string]any, role, needle string) bool {
	for _, part := range parts {
		if role != "" {
			if value, _ := part["role"].(string); value != role {
				continue
			}
		}
		if content, _ := part["content"].(string); strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func escalationRequestIDFromRecord(t *testing.T, record escalationgw.Record) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(record.Body), &body); err != nil {
		return ""
	}
	messages, _ := body["messages"].([]any)
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		content, _ := message["content"].(string)
		index := strings.Index(content, "request_id: ")
		if index < 0 {
			continue
		}
		rest := content[index+len("request_id: "):]
		if stop := strings.IndexAny(rest, "\n\r"); stop >= 0 {
			rest = rest[:stop]
		}
		if id := strings.TrimSpace(rest); id != "" {
			return id
		}
	}
	return ""
}

// dumpModelRequests writes the raw model traffic even when the test fails
// before the chain completes, so a broken chain is diagnosable from the
// evidence directory instead of only from the failure message.
func (r *escalationE2ERun) dumpModelRequests() {
	var requests strings.Builder
	for _, record := range r.gw.Requests() {
		fmt.Fprintf(&requests, "{\"seq\":%d,\"kind\":%q,\"tools\":%d,\"response\":%q,\"tool_calls\":%q,\"message\":%q}\n",
			record.Seq, record.Kind, len(record.Tools), record.Response, strings.Join(record.ToolCalls, ","), record.Message)
	}
	if err := os.WriteFile(filepath.Join(r.outDir, "model-requests.jsonl"), []byte(requests.String()), 0o644); err != nil {
		r.t.Errorf("write model-requests evidence: %v", err)
	}
}

// writeEvidence dumps the chain's model traffic, both transcripts and a
// human-readable summary.
func (r *escalationE2ERun) writeEvidence(handoff, listed, replied, resumed, repeating escalationgw.Record) {
	writeFile := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(r.outDir, name), data, 0o644); err != nil {
			r.t.Errorf("write evidence %s: %v", name, err)
		}
	}

	var summary, requests strings.Builder
	fmt.Fprintf(&summary, "worker escalation e2e: session=%s colony=%s\n\n", r.session, escalationE2EColonyID)
	fmt.Fprintf(&summary, "chain (model request seq):\n")
	fmt.Fprintf(&summary, "  %d worker  identical task_list x3 -> doom-loop escalation\n", repeating.Seq)
	fmt.Fprintf(&summary, "  %d queen   [WORKER_ESCALATION] handoff\n", handoff.Seq)
	fmt.Fprintf(&summary, "  %d queen   list_worker_questions\n", listed.Seq)
	fmt.Fprintf(&summary, "  %d queen   reply_to_worker\n", replied.Seq)
	fmt.Fprintf(&summary, "  %d worker  [QUEEN_REPLY] -> report_to_parent\n", resumed.Seq)

	kinds := map[escalationgw.Kind]int{}
	for _, record := range r.gw.Requests() {
		kinds[record.Kind]++
		fmt.Fprintf(&requests, "{\"seq\":%d,\"kind\":%q,\"tools\":%d,\"response\":%q,\"tool_calls\":%q}\n",
			record.Seq, record.Kind, len(record.Tools), record.Response, strings.Join(record.ToolCalls, ","))
	}
	fmt.Fprintf(&summary, "\nmodel requests: %d queen, %d overseer, %d worker, %d other\n",
		kinds[escalationgw.KindQueen], kinds[escalationgw.KindOverseer],
		kinds[escalationgw.KindWorker], kinds[escalationgw.KindOther])
	for _, event := range r.sessionDoomLoopEvents() {
		data, _ := event["data"].(map[string]any)
		fmt.Fprintf(&summary, "session events: node_tool_doom_loop stream=%v description=%q\n",
			event["stream_id"], data["description"])
	}
	writeFile("summary.txt", []byte(summary.String()))
	writeFile("model-requests.jsonl", []byte(requests.String()))

	if r.session != "" {
		if encoded, err := json.MarshalIndent(trimParts(r.queenParts()), "", "  "); err == nil {
			writeFile("queen-transcript.json", encoded)
		}
		if events, err := os.ReadFile(filepath.Join(r.sessionDir(), "events.jsonl")); err == nil {
			writeFile("session-events.jsonl", events)
		}
	}
	if encoded, err := json.MarshalIndent(trimParts(r.workerParts()), "", "  "); err == nil {
		writeFile("worker-transcript.json", encoded)
	}
}
