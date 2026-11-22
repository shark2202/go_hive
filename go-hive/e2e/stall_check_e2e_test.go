//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/e2e/stallgw"
	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/server"
)

// Stall-check end-to-end: the real `hive serve` binary against a temporary
// HIVE_HOME, driven through POST /api/sessions and /api/sessions/{id}/chat,
// with the queen's chat model and the `judge` slot both pointed at the local
// scripted OpenAI-compatible gateway in e2e/stallgw.
//
// The queen model is a script (announce the work, call no tool, stop) — exactly
// the turn the stall check exists for; this is a pipeline proof, not a claim
// about any real model's behavior. Three runs cover the contract:
//
//   - judge says yes: the stall check's question reaches the real judge slot,
//     the reminder lands in the queen's next model call and in the session
//     transcript, and the judge is asked exactly once for the turn;
//   - judge says no: the judge is asked and nothing else happens (no reminder
//     text in any model request or transcript part, no second turn);
//   - stall_check off: the judge slot is configured but never called.
//
// Run:
//
//	CGO_ENABLED=0 GOPROXY=off go test -tags e2e ./e2e -run TestStallCheckEndToEnd -v -count=1
//	HIVE_STALL_E2E_OUT=/tmp/stall-check-e2e go test -tags e2e ./e2e -run ... -v
//
// Parity: core/framework/server/tests/test_stall_check.py::test_a_turn_that_announces_work_and_stops_gets_one_reminder
func TestStallCheckEndToEnd(t *testing.T) {
	binary := stallE2EBuild(t)
	t.Run("judge_true_reminds_the_next_turn", func(t *testing.T) { stallE2EJudgeTrue(t, binary) })
	t.Run("judge_false_leaves_the_turn_alone", func(t *testing.T) { stallE2EJudgeFalse(t, binary) })
	t.Run("switch_off_never_asks_the_judge", func(t *testing.T) { stallE2ESwitchOff(t, binary) })
}

const (
	stallE2EModel        = "stall-e2e-model"
	stallE2EAnnouncement = "On it — drawing the fox now."
	stallE2ELaterReply   = "Done."
	stallE2EUser         = "draw me a fox"
)

func stallE2EBuild(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hive")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hive")
	build.Dir = credModuleRoot(t)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/hive: %v\n%s", err, output)
	}
	return binary
}

// stallE2EChildEnv isolates the serve process from the developer's shell: no
// ambient provider keys, no shared hive home, no stall-check override.
func stallE2EChildEnv(homeDir string) []string {
	dropped := map[string]bool{
		"OPENAI_API_KEY": true, "OPENAI_BASE_URL": true, "OPENAI_API_BASE": true,
		"HIVE_API_KEY": true, "HIVE_HOME": true, "HIVE_DESKTOP_TOKEN": true,
		"HIVE_DESKTOP_PARENT_PID": true, "HIVE_LLM_BASE_URL": true,
		"HIVE_STALL_CHECK": true,
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

type stallE2ERun struct {
	t       *testing.T
	name    string
	outDir  string
	home    string
	gw      *stallgw.Server
	baseURL string
	client  *http.Client
	logs    *credLogBuffer
	session string
}

func stallE2EOutRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("HIVE_STALL_E2E_OUT")
	if root == "" {
		root = t.TempDir()
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("create evidence root %s: %v", root, err)
	}
	return root
}

// newStallE2ERun boots the real binary with one case's configuration: the
// queen model and the judge slot both live on this run's gateway.
func newStallE2ERun(t *testing.T, binary, name string, probability float64, threshold any, enabled bool) *stallE2ERun {
	t.Helper()

	outDir := filepath.Join(stallE2EOutRoot(t), name)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create case evidence dir: %v", err)
	}

	gw := stallgw.New(stallgw.Options{
		Model:            stallE2EModel,
		Announcement:     stallE2EAnnouncement,
		StallProbability: probability,
	})
	gwServer := httptest.NewServer(gw.Handler())
	t.Cleanup(gwServer.Close)

	homeDir := filepath.Join(t.TempDir(), "hive-home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("create hive home: %v", err)
	}
	judge := map[string]any{
		"protocol": "typesafe",
		"model":    "judge-e2e",
		"api_base": gwServer.URL + "/judge",
		"api_key":  "judge-key",
	}
	if threshold != nil {
		judge["threshold"] = threshold
	}
	configuration := map[string]any{
		"llm": map[string]any{
			"provider": "custom",
			"model":    "ollama/" + stallE2EModel,
			"api_base": gwServer.URL + "/v1",
		},
		"stall_check": enabled,
		"judge":       judge,
	}
	encoded, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		t.Fatalf("encode configuration.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(homeDir, "configuration.json"), encoded, 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}

	port := credFreePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	serverCmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	serverCmd.Dir = t.TempDir()
	serverCmd.Env = stallE2EChildEnv(homeDir)
	serverCmd.Stdout = logs
	serverCmd.Stderr = logs
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start hive serve: %v", err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = serverCmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		credStopHive(t, serverCmd, exited, logs)
		if err := os.WriteFile(filepath.Join(outDir, "server-output.txt"), []byte(logs.String()), 0o644); err != nil {
			t.Errorf("write server log evidence: %v", err)
		}
	})

	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("hive serve exited before /api/health (err=%v); logs:\n%s", exitErr, logs.String())
		default:
		}
		resp, err := client.Get(baseURL + "/api/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("hive serve did not answer GET /api/health within 30s; logs:\n%s", logs.String())
	}

	run := &stallE2ERun{t: t, name: name, outDir: outDir, home: homeDir, gw: gw, baseURL: baseURL, client: client, logs: logs}
	t.Cleanup(run.writeEvidence)
	return run
}

func (r *stallE2ERun) doJSON(method, path string, payload any) (int, []byte) {
	return credDoJSON(r.t, r.client, method, r.baseURL+path, payload)
}

func (r *stallE2ERun) createSession() string {
	r.t.Helper()
	status, raw := r.doJSON(http.MethodPost, "/api/sessions", map[string]any{})
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	sessionID, _ := body["session_id"].(string)
	if (status != http.StatusOK && status != http.StatusCreated) || sessionID == "" {
		r.t.Fatalf("POST /api/sessions = %d %s", status, raw)
	}
	r.session = sessionID
	return sessionID
}

func (r *stallE2ERun) chat(sessionID, message string) {
	r.t.Helper()
	status, raw := r.doJSON(http.MethodPost, "/api/sessions/"+sessionID+"/chat", map[string]any{"message": message})
	if status != http.StatusOK {
		r.t.Fatalf("POST /api/sessions/%s/chat = %d %s; logs:\n%s", sessionID, status, raw, r.logs.String())
	}
}

// sessionDir finds the created session's on-disk directory (<home>/queens/*/sessions/<id>).
func (r *stallE2ERun) sessionDir(sessionID string) string {
	matches, err := filepath.Glob(filepath.Join(r.home, "queens", "*", "sessions", sessionID))
	if err != nil || len(matches) == 0 {
		r.t.Fatalf("session dir for %s not found (err=%v)", sessionID, err)
	}
	return matches[0]
}

func (r *stallE2ERun) parts(sessionID string) []map[string]any {
	parts, err := agentloop.NewFileConversationStore(filepath.Join(r.sessionDir(sessionID), "conversations")).ReadParts()
	if err != nil {
		r.t.Fatalf("read transcript %s: %v", sessionID, err)
	}
	return parts
}

func (r *stallE2ERun) waitTranscript(sessionID string, want func([]map[string]any) bool, timeout time.Duration, what string) []map[string]any {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		parts := r.parts(sessionID)
		if want(parts) {
			return parts
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("transcript %s did not %s within %s; logs:\n%s", sessionID, what, timeout, r.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (r *stallE2ERun) waitJudges(want int, timeout time.Duration) []stallgw.Record {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		records := r.gw.RequestsOfKind(stallgw.KindJudge)
		if len(records) >= want {
			return records
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("judge asked %d times, want %d within %s; logs:\n%s",
				len(records), want, timeout, r.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// stallE2ERecordCarries reports whether a recorded chat request carries needle
// in its system prompt or in any message content. It decodes the body first:
// the raw JSON escapes newlines, so a literal Contains on the body would miss
// the reminder's exact text.
func stallE2ERecordCarries(record stallgw.Record, needle string) bool {
	var body map[string]any
	if err := json.Unmarshal([]byte(record.Body), &body); err != nil {
		return false
	}
	messages, _ := body["messages"].([]any)
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role != "system" {
			continue
		}
		if content, _ := message["content"].(string); strings.Contains(content, needle) {
			return true
		}
	}
	return credMessagesContain(messages, needle)
}

// waitQueenRequestContaining waits for a queen model call whose decoded body
// carries needle (the reminder reaches the next turn as a user message).
func (r *stallE2ERun) waitQueenRequestContaining(needle string, timeout time.Duration) stallgw.Record {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		for _, record := range r.gw.RequestsOfKind(stallgw.KindQueen) {
			if stallE2ERecordCarries(record, needle) {
				return record
			}
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("no queen model call carried %q within %s; logs:\n%s", needle, timeout, r.logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func stallE2EBody(t *testing.T, record stallgw.Record) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(record.Body), &body); err != nil {
		t.Fatalf("decode %s request body (seq %d): %v", record.Kind, record.Seq, err)
	}
	return body
}

// stallE2EPartsContain reports whether any transcript part with the given role
// ("": any role) carries needle.
func stallE2EPartsContain(parts []map[string]any, role, needle string) bool {
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

func stallE2EJudgeTrue(t *testing.T, binary string) {
	run := newStallE2ERun(t, binary, "judge-true", 0.95, 0.5, true)
	sessionID := run.createSession()
	run.chat(sessionID, stallE2EUser)

	// 1. The stalled turn reaches the real judge slot with the real question,
	// criteria and state.
	judges := run.waitJudges(1, 30*time.Second)
	judge := judges[0]
	if judge.Auth != "Bearer judge-key" {
		t.Fatalf("judge Authorization = %q, want the configured slot key", judge.Auth)
	}
	body := stallE2EBody(t, judge)
	questions, _ := body["questions"].(map[string]any)
	answer, _ := questions["answer"].(map[string]any)
	if got, _ := answer["type"].(string); got != "noul" {
		t.Fatalf("judge question type = %q, want noul", got)
	}
	if got, _ := answer["instructions"].(string); got != server.StallCheckQuestion {
		t.Fatalf("judge instructions = %q, want the stall check question", got)
	}
	criteria, _ := answer["criteria"].(map[string]any)
	wantCriteria := map[string]any{"true": server.StallCheckCriteria["true"], "false": server.StallCheckCriteria["false"]}
	if !reflect.DeepEqual(criteria, wantCriteria) {
		t.Fatalf("judge criteria = %#v, want %#v", criteria, wantCriteria)
	}
	state, _ := body["state"].(map[string]any)
	if got, _ := state["user_message"].(string); got != stallE2EUser {
		t.Fatalf("judge state user_message = %q, want %q", got, stallE2EUser)
	}
	if got, _ := state["assistant_reply"].(string); got != stallE2EAnnouncement {
		t.Fatalf("judge state assistant_reply = %q, want the announcement %q", got, stallE2EAnnouncement)
	}
	if tools, ok := state["tools_called_this_turn"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("judge state tools_called_this_turn = %#v, want []", state["tools_called_this_turn"])
	}
	if _, present := state["previous_turn"]; present {
		t.Fatalf("first turn carries previous_turn = %#v, want none", state["previous_turn"])
	}

	// 2. The reminder reaches the queen's next model call: a queen request
	// after the judge's, carrying the reminder as a message.
	reminder := run.waitQueenRequestContaining(server.StallReminder, 30*time.Second)
	if reminder.Seq <= judge.Seq {
		t.Fatalf("reminder request seq %d is not after the judge's %d", reminder.Seq, judge.Seq)
	}
	messages, _ := stallE2EBody(t, reminder)["messages"].([]any)
	if !credMessagesContain(messages, server.StallReminder) {
		t.Fatal("the reminder request carries no reminder text in its messages")
	}

	// 3. The transcript tells the same story: the reminder is an injected user
	// message and the queen answered it in a real next turn.
	run.waitTranscript(sessionID, func(parts []map[string]any) bool {
		return stallE2EPartsContain(parts, "user", server.StallReminder)
	}, 30*time.Second, "carry the injected stall reminder")
	run.waitTranscript(sessionID, func(parts []map[string]any) bool {
		return stallE2EPartsContain(parts, "assistant", stallE2ELaterReply)
	}, 30*time.Second, "record the reminder turn's reply")

	// 4. One judgment per user message: the reminder turn's own park must not
	// ask again.
	time.Sleep(300 * time.Millisecond)
	if got := len(run.gw.RequestsOfKind(stallgw.KindJudge)); got != 1 {
		t.Fatalf("judge asked %d times, want exactly once per user message", got)
	}
}

func stallE2EJudgeFalse(t *testing.T, binary string) {
	run := newStallE2ERun(t, binary, "judge-false", 0.05, 0.5, true)
	sessionID := run.createSession()
	run.chat(sessionID, stallE2EUser)

	run.waitJudges(1, 30*time.Second)
	run.waitTranscript(sessionID, func(parts []map[string]any) bool {
		return stallE2EPartsContain(parts, "assistant", stallE2EAnnouncement)
	}, 30*time.Second, "record the queen's announcement")

	// A judge that cleared the turn must leave everything else alone.
	time.Sleep(1500 * time.Millisecond)
	if got := len(run.gw.RequestsOfKind(stallgw.KindJudge)); got != 1 {
		t.Fatalf("judge asked %d times, want once", got)
	}
	for _, record := range run.gw.RequestsOfKind(stallgw.KindQueen) {
		if stallE2ERecordCarries(record, server.StallReminder) {
			t.Fatalf("a cleared turn still sent the reminder to the model (seq %d)", record.Seq)
		}
	}
	if stallE2EPartsContain(run.parts(sessionID), "", server.StallReminder) {
		t.Fatal("transcript carries a reminder after the judge cleared the turn")
	}
}

func stallE2ESwitchOff(t *testing.T, binary string) {
	run := newStallE2ERun(t, binary, "switch-off", 0.95, 0.5, false)
	sessionID := run.createSession()
	run.chat(sessionID, stallE2EUser)

	run.waitTranscript(sessionID, func(parts []map[string]any) bool {
		return stallE2EPartsContain(parts, "assistant", stallE2EAnnouncement)
	}, 30*time.Second, "record the queen's announcement")

	// The judge slot is configured and would say yes; the option alone decides.
	time.Sleep(1500 * time.Millisecond)
	if got := len(run.gw.RequestsOfKind(stallgw.KindJudge)); got != 0 {
		t.Fatalf("stall_check off: judge asked %d times, want none", got)
	}
	for _, record := range run.gw.RequestsOfKind(stallgw.KindQueen) {
		if stallE2ERecordCarries(record, server.StallReminder) {
			t.Fatalf("stall_check off: a model request carried the reminder (seq %d)", record.Seq)
		}
	}
}

// writeEvidence dumps the case's gateway traffic, transcript, session events
// and a human-readable summary into the evidence directory.
func (r *stallE2ERun) writeEvidence() {
	writeFile := func(name string, data []byte) {
		if err := os.WriteFile(filepath.Join(r.outDir, name), data, 0o644); err != nil {
			r.t.Errorf("write evidence %s: %v", name, err)
		}
	}

	var summary strings.Builder
	fmt.Fprintf(&summary, "stall-check e2e case: %s\n", r.name)
	fmt.Fprintf(&summary, "session: %s\n\n", r.session)

	var judgeLines, queenLines strings.Builder
	judgeRequests, queenRequests, otherRequests := 0, 0, 0
	for _, record := range r.gw.Requests() {
		if record.Kind == stallgw.KindJudge {
			judgeRequests++
			fmt.Fprintf(&judgeLines, "%s\n", record.Body)
			fmt.Fprintf(&summary, "seq %d judge  auth=%q\n", record.Seq, record.Auth)
			continue
		}
		body := stallE2EBody(r.t, record)
		model, _ := body["model"].(string)
		messages, _ := body["messages"].([]any)
		lastUser := ""
		for _, entry := range messages {
			message, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if role, _ := message["role"].(string); role == "user" {
				if content, _ := message["content"].(string); content != "" {
					lastUser = content
				}
			}
		}
		reminder := stallE2ERecordCarries(record, server.StallReminder)
		kind := string(record.Kind)
		if record.Kind == stallgw.KindQueen {
			queenRequests++
		} else {
			otherRequests++
		}
		fmt.Fprintf(&queenLines, "{\"seq\":%d,\"kind\":%q,\"model\":%q,\"messages\":%d,\"reminder\":%v,\"last_user_tail\":%s}\n",
			record.Seq, kind, model, len(messages), reminder, string(mustJSON(stallE2ETail(lastUser, 1500))))
		fmt.Fprintf(&summary, "seq %d %-5s model=%s messages=%d reminder=%v\n", record.Seq, kind, model, len(messages), reminder)
	}
	if r.session != "" {
		parts := r.parts(r.session)
		encoded, err := json.MarshalIndent(trimParts(parts), "", "  ")
		if err == nil {
			writeFile("transcript.json", encoded)
		}
		if events, err := os.ReadFile(filepath.Join(r.sessionDir(r.session), "events.jsonl")); err == nil {
			writeFile("events.jsonl", events)
		}
	}
	writeFile("judge-requests.jsonl", []byte(judgeLines.String()))
	writeFile("queen-requests.jsonl", []byte(queenLines.String()))
	fmt.Fprintf(&summary, "\njudge requests: %d\nqueen requests: %d\nother requests (titles): %d\n", judgeRequests, queenRequests, otherRequests)
	writeFile("summary.txt", []byte(summary.String()))
}

func mustJSON(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		return []byte(`""`)
	}
	return encoded
}

// stallE2ETail keeps the evidence readable: the end of a request's last user
// message is the part that names the turn (and carries the reminder).
func stallE2ETail(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return "…" + string(runes[len(runes)-limit:])
}

// trimParts keeps the evidence readable: each part's content is capped.
func trimParts(parts []map[string]any) []map[string]any {
	const cap = 4000
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		clone := map[string]any{}
		for key, value := range part {
			clone[key] = value
		}
		if content, ok := clone["content"].(string); ok && len(content) > cap {
			clone["content"] = content[:cap] + "…[truncated]"
		}
		out = append(out, clone)
	}
	return out
}
