//go:build e2e

package e2e

// Worker escalation end-to-end: the real `hive serve` binary, a colony bound to
// the session, and one worker that escalates a blocked question. The chain under
// test is the one the parity audit found unwired:
//
//	worker escalate tool -> ESCALATION_REQUESTED -> colony-scoped routing on the
//	queen session -> list_worker_questions -> reply_to_worker -> worker resumes
//	and reports.
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
// Parity: core/tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen

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

	"github.com/aden-hive/hive-go/e2e/escalationgw"
	"github.com/aden-hive/hive-go/internal/agentloop"
)

const (
	escalationE2EModel    = "escalation-e2e-model"
	escalationE2EColonyID = "escalation_e2e"
	escalationE2EUser     = "Dispatch a worker to query the ledger."
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
		"system_prompt": "You are a focused ledger worker. Escalate when you are blocked; report when done.",
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

// Parity: core/tests/test_session_manager_worker_handoff.py::test_worker_handoff_injects_addressed_request_into_queen
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

	// 3. The worker escalates through the real tool.
	escalating := run.waitForModelRequest("from the worker with the escalate tool", func(record escalationgw.Record) bool {
		if record.Kind != escalationgw.KindWorker || !escalationE2ERecordRespondedWith(record, "escalate") {
			return false
		}
		return true
	})
	if !containsString(escalating.Tools, "escalate") {
		t.Fatalf("worker tool surface = %v, want escalate", escalating.Tools)
	}

	// 4. The escalation reaches the queen as an addressed handoff.
	handoff := run.waitForModelRequest("carrying [WORKER_ESCALATION] for the queen", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindQueen && escalationE2ERequestCarries(t, record, "[WORKER_ESCALATION]")
	})
	requestID := escalationRequestIDFromRecord(t, handoff)
	if requestID == "" {
		t.Fatalf("the queen's escalation turn carries no request_id:\n%s", handoff.Body)
	}

	// 5. The queen lists the pending question.
	listed := run.waitForModelRequest("where the queen calls list_worker_questions", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindQueen && escalationE2ERecordRespondedWith(record, "list_worker_questions")
	})
	if listed.Seq < handoff.Seq {
		t.Fatalf("list_worker_questions (seq %d) preceded the handoff (seq %d)", listed.Seq, handoff.Seq)
	}

	// 6. The queen answers it with reply_to_worker, using the request_id from
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

	// 7. The worker resumes with the queen's reply and reports.
	resumed := run.waitForModelRequest("from the worker carrying [QUEEN_REPLY]", func(record escalationgw.Record) bool {
		return record.Kind == escalationgw.KindWorker && escalationE2ERequestCarries(t, record, "[QUEEN_REPLY]")
	})
	if !escalationE2ERequestCarries(t, resumed, requestID) {
		t.Fatalf("resumed worker turn does not carry the request_id %s", requestID)
	}
	if !escalationE2ERecordRespondedWith(resumed, "report_to_parent") {
		t.Fatalf("resumed worker turn did not report; tool calls = %v", resumed.ToolCalls)
	}

	// 8. The colony lead sees the worker's terminal report.
	run.waitForModelRequest("from the colony lead carrying [WORKER_REPORT]", func(record escalationgw.Record) bool {
		if record.Kind != escalationgw.KindOverseer && record.Kind != escalationgw.KindQueen {
			return false
		}
		return escalationE2ERequestCarries(t, record, "[WORKER_REPORT]")
	})

	// 9. Durable evidence on disk: the handoff is a transcript record for the
	// queen (so a restart rebuilds what the user saw), and the worker's own
	// conversation holds both the escalation and the reply.
	parts := run.queenParts()
	if !partsContain(parts, "", "[WORKER_ESCALATION]") {
		t.Fatal("queen transcript does not record the worker escalation")
	}
	workerParts := run.workerParts()
	if !partsContain(workerParts, "", "[QUEEN_REPLY] request_id="+requestID) {
		t.Fatal("worker transcript does not record the queen's reply")
	}

	run.writeEvidence(handoff, listed, replied, resumed, escalating)
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

// writeEvidence dumps the chain's model traffic, both transcripts and a
// human-readable summary.
func (r *escalationE2ERun) writeEvidence(handoff, listed, replied, resumed, escalating escalationgw.Record) {
	writeFile := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(r.outDir, name), data, 0o644); err != nil {
			r.t.Errorf("write evidence %s: %v", name, err)
		}
	}

	var summary, requests strings.Builder
	fmt.Fprintf(&summary, "worker escalation e2e: session=%s colony=%s\n\n", r.session, escalationE2EColonyID)
	fmt.Fprintf(&summary, "chain (model request seq):\n")
	fmt.Fprintf(&summary, "  %d worker  escalate tool call\n", escalating.Seq)
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
