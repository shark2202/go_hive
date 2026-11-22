//go:build e2e

package e2e

// Memory end-to-end: timeline extraction, past-conversation recall and
// profile-memory recall, exercised through a real `hive serve` binary against
// a deterministic local OpenAI-compatible gateway (e2e/memgw).
//
// The gateway plays the model. It answers the timeline extraction prompt with
// items whose dates are derived from the session date printed in that prompt,
// so a passing assertion proves the prompt carried the date and the reader
// resolved it; it records every prompt so the same run can be diffed against
// the Python implementation.
//
// Run:
//
//	go test -tags e2e ./e2e -run TestMemoryEndToEndGoRuntime -v
//	HIVE_MEMORY_E2E_OUT=/tmp/memory-e2e-go go test -tags e2e ./e2e -run ... -v

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/e2e/memgw"
	"github.com/aden-hive/hive-go/internal/agentloop"
)

type memRun struct {
	t       *testing.T
	root    string
	home    string
	outDir  string
	gw      *memgw.Server
	gwURL   string
	baseURL string
	client  *http.Client
	logs    *credLogBuffer
	suffix  string
}

func newMemRun(t *testing.T, suffix string, extraEnv ...string) *memRun {
	t.Helper()

	gw, err := memgw.New(memgw.Options{})
	if err != nil {
		t.Fatalf("start memory gateway: %v", err)
	}
	gwServer := httptest.NewServer(gw.Handler())
	t.Cleanup(gwServer.Close)
	t.Cleanup(func() { _ = gw.Close() })

	root := t.TempDir()
	if keep := os.Getenv("HIVE_MEMORY_E2E_HOME"); keep != "" {
		root = filepath.Join(keep, suffix)
	}
	homeDir := filepath.Join(root, "hive-home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("create hive home: %v", err)
	}
	config := fmt.Sprintf(
		`{"llm":{"provider":"custom","model":"ollama/memory-e2e","api_base":%q},"memory_timeline":true}`,
		gwServer.URL+"/v1",
	)
	if err := os.WriteFile(filepath.Join(homeDir, "configuration.json"), []byte(config), 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}

	outDir := os.Getenv("HIVE_MEMORY_E2E_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("create evidence dir: %v", err)
	}

	moduleRoot := memModuleRoot(t)
	binary := filepath.Join(t.TempDir(), "hive")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hive")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/hive: %v\n%s", err, output)
	}

	port := credFreePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	serverCmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	serverCmd.Dir = t.TempDir()
	serverCmd.Env = memChildEnv(homeDir, extraEnv)
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
	t.Cleanup(func() { credStopHive(t, serverCmd, exited, logs) })

	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
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
				run := &memRun{
					t: t, root: root, home: homeDir, outDir: outDir,
					gw: gw, gwURL: gwServer.URL, baseURL: baseURL,
					client: client, logs: logs, suffix: suffix,
				}
				t.Cleanup(func() { run.writeRequests(t) })
				return run
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hive serve did not become healthy; logs:\n%s", logs.String())
	return nil
}

func memModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above e2e/")
		}
		dir = parent
	}
}

// memChildEnv isolates the server from the developer's shell and pins the
// temporary home. The model is a local (ollama/) model, so no provider key is
// needed; the gateway URL rides in configuration.json.
func memChildEnv(homeDir string, extra []string) []string {
	dropped := map[string]bool{
		"OPENAI_API_KEY": true, "OPENAI_BASE_URL": true, "OPENAI_API_BASE": true,
		"HIVE_API_KEY": true, "HIVE_HOME": true, "HIVE_DESKTOP_TOKEN": true,
		"HIVE_DESKTOP_PARENT_PID": true, "HIVE_LLM_BASE_URL": true,
		"HIVE_MEMORY_TIMELINE": true, "HIVE_TIMELINE_BATCH": true, "HIVE_TIMELINE_COOLDOWN": true,
		"HIVE_PAST_RECALL_TIMEOUT": true,
	}
	env := make([]string, 0, len(os.Environ())+1+len(extra))
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
	env = append(env, "HIVE_HOME="+homeDir)
	return append(env, extra...)
}

func (m *memRun) sessionDir(sessionID string) string {
	return filepath.Join(m.home, "queens", "default", "sessions", sessionID)
}

func (m *memRun) doJSON(t *testing.T, method, path string, payload any) (int, map[string]any) {
	t.Helper()
	status, raw := credDoJSON(t, m.client, method, m.baseURL+path, payload)
	body := map[string]any{}
	_ = json.Unmarshal(raw, &body)
	return status, body
}

func (m *memRun) createSession(t *testing.T) string {
	t.Helper()
	status, body := m.doJSON(t, http.MethodPost, "/api/sessions", map[string]any{})
	sessionID, _ := body["session_id"].(string)
	if (status != http.StatusOK && status != http.StatusCreated) || sessionID == "" {
		t.Fatalf("POST /api/sessions = %d %v, want 200/201 with session_id", status, body)
	}
	return sessionID
}

func (m *memRun) chat(t *testing.T, sessionID, message string) []map[string]any {
	t.Helper()
	before := len(m.assistantTexts(t, sessionID))
	status, body := m.doJSON(t, http.MethodPost, "/api/sessions/"+sessionID+"/chat", map[string]any{"message": message})
	if status != http.StatusOK {
		t.Fatalf("POST chat = %d %v", status, body)
	}
	return m.waitAssistant(t, sessionID, before+1)
}

func (m *memRun) waitAssistantContaining(t *testing.T, sessionID string, needles ...string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		parts := m.parts(t, sessionID)
		for _, content := range memPartContents(parts, "assistant") {
			ok := true
			for _, needle := range needles {
				if !strings.Contains(content, needle) {
					ok = false
					break
				}
			}
			if ok {
				return parts
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s: no assistant reply containing %v; assistant parts: %v; logs:\n%s",
				sessionID, needles, memPartContents(m.parts(t, sessionID), "assistant"), m.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *memRun) stopSession(t *testing.T, sessionID string) {
	t.Helper()
	status, body := m.doJSON(t, http.MethodDelete, "/api/sessions/"+sessionID, nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE /api/sessions/%s = %d %v", sessionID, status, body)
	}
}

func (m *memRun) parts(t *testing.T, sessionID string) []map[string]any {
	t.Helper()
	parts, err := agentloop.NewFileConversationStore(filepath.Join(m.sessionDir(sessionID), "conversations")).ReadParts()
	if err != nil {
		t.Fatalf("read transcript %s: %v", sessionID, err)
	}
	return parts
}

// assistantTexts returns the assistant parts that carry prose; a tool-call
// iteration persists an assistant part with empty content and must not be
// counted as the turn's reply.
func (m *memRun) assistantTexts(t *testing.T, sessionID string) []string {
	t.Helper()
	out := []string{}
	for _, content := range memPartContents(m.parts(t, sessionID), "assistant") {
		if strings.TrimSpace(content) != "" {
			out = append(out, content)
		}
	}
	return out
}

func (m *memRun) waitAssistant(t *testing.T, sessionID string, want int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if len(m.assistantTexts(t, sessionID)) >= want {
			return m.parts(t, sessionID)
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s: assistant replies = %d, want %d; logs:\n%s",
				sessionID, len(m.assistantTexts(t, sessionID)), want, m.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func memPartContents(parts []map[string]any, role string) []string {
	out := []string{}
	for _, part := range parts {
		if value, _ := part["role"].(string); value != role {
			continue
		}
		if content, ok := part["content"].(string); ok {
			out = append(out, content)
		}
	}
	return out
}

func memCountContaining(parts []map[string]any, needle string) int {
	count := 0
	for _, part := range parts {
		if content, _ := part["content"].(string); strings.Contains(content, needle) {
			count++
		}
	}
	return count
}

func (m *memRun) waitFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(bytes.TrimSpace(data)) > 0 {
			return data
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("file %s did not appear within %s (last error: %v); logs:\n%s", path, timeout, lastErr, m.logs.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (m *memRun) waitTimelineUserTurns(t *testing.T, dir string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(filepath.Join(dir, "timeline.jsonl")); err == nil {
			meta := memReadTimelineMeta(t, dir)
			if memNumber(meta, "user_turns") >= want {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeline %s did not reach user_turns=%d within %s", dir, want, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func memReadTimeline(t *testing.T, dir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "timeline.jsonl"))
	if err != nil {
		t.Fatalf("read timeline.jsonl: %v", err)
	}
	items := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		item := map[string]any{}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("parse timeline line %q: %v", line, err)
		}
		items = append(items, item)
	}
	return items
}

func memReadTimelineMeta(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "timeline_meta.json"))
	if err != nil {
		t.Fatalf("read timeline_meta.json: %v", err)
	}
	meta := map[string]any{}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("parse timeline_meta.json: %v", err)
	}
	return meta
}

var memSessionStampedRe = regexp.MustCompile(`^session_(\d{8})_(\d{6})_`)

func memSessionStarted(t *testing.T, sessionID string) time.Time {
	t.Helper()
	match := memSessionStampedRe.FindStringSubmatch(sessionID)
	if match == nil {
		t.Fatalf("session id %q does not carry a start stamp", sessionID)
	}
	started, err := time.ParseInLocation("20060102_150405", match[1]+"_"+match[2], time.Local)
	if err != nil {
		t.Fatalf("parse session start: %v", err)
	}
	return started
}

func memFindItem(items []map[string]any, needle string) map[string]any {
	for _, item := range items {
		if text, _ := item["text"].(string); strings.Contains(text, needle) {
			return item
		}
	}
	return nil
}

func memString(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func memNumber(item map[string]any, key string) int {
	switch value := item[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return 0
}

func (m *memRun) copyEvidence(t *testing.T, name, source string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Logf("evidence %s skipped: %v", name, err)
		return
	}
	m.writeEvidence(t, name, data)
}

func (m *memRun) writeEvidence(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(m.outDir, name), data, 0o644); err != nil {
		t.Fatalf("write evidence %s: %v", name, err)
	}
}

func (m *memRun) writeRequests(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	for _, record := range m.gw.Requests() {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("encode gateway record: %v", err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	name := "go-gateway-requests"
	if m.suffix != "" {
		name += "-" + m.suffix
	}
	if err := os.WriteFile(filepath.Join(m.outDir, name+".jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write gateway evidence: %v", err)
	}
}

var memAssistantTokenRe = regexp.MustCompile(`ASSISTANT_REPLY_\d+`)

// TestMemoryEndToEndGoRuntime is the Go half of the double run: a real server,
// real session directories and the deterministic gateway. Each subtest names
// one mechanism (timeline live/stop/backfill, search tools, past excerpt
// recall, scoped memory recall).
//
// Parity: core/framework/agents/queen/timeline.py::update_session_timeline
func TestMemoryEndToEndGoRuntime(t *testing.T) {
	t.Run("timeline_live_search_backfill_past_recall", testGoMemoryMainFlow)
	t.Run("first_turn_extracts", testGoTimelineFirstTurn)
	t.Run("stop_flush", testGoTimelineStopFlush)
}

func testGoMemoryMainFlow(t *testing.T) {
	// A batch of one makes every completed turn extract, so the live path is
	// deterministic. The stop-flush case uses the opposite settings.
	run := newMemRun(t, "main", "HIVE_TIMELINE_BATCH=1")

	// ── 1. Live timeline ────────────────────────────────────────────────
	s1 := run.createSession(t)
	t.Logf("S1 %s", s1)
	run.chat(t, s1, "昨天我去参加了同事的婚礼，随礼 800 元。")
	run.chat(t, s1, "我打算下个月去云南旅行，顺便拍点照片。")
	run.chat(t, s1, "我家养了一只橘猫，叫团子。")

	s1Dir := run.sessionDir(s1)
	run.waitTimelineUserTurns(t, s1Dir, 3, 15*time.Second)
	meta := memReadTimelineMeta(t, s1Dir)
	if got := memNumber(meta, "version"); got != 1 {
		t.Errorf("timeline_meta version = %v, want 1", meta["version"])
	}
	if model, _ := meta["model"].(string); model == "" {
		t.Errorf("timeline_meta model is empty: %v", meta)
	}
	if updated, _ := meta["updated_at"].(string); updated == "" {
		t.Errorf("timeline_meta updated_at is empty: %v", meta)
	}

	items := memReadTimeline(t, s1Dir)
	if memFindItem(items, "不应写入") != nil {
		t.Errorf("item with an unknown session label was written: %v", items)
	}
	started := memSessionStarted(t, s1)
	yesterday := started.AddDate(0, 0, -1).Format("2006-01-02")
	saidOn := started.Format("2006-01-02")

	wedding := memFindItem(items, "婚礼")
	if wedding == nil {
		t.Fatalf("timeline has no wedding item: %v", items)
	}
	if got := memString(wedding, "kind"); got != "event" {
		t.Errorf("wedding kind = %q, want event", got)
	}
	if got := memString(wedding, "start"); got != yesterday {
		t.Errorf("wedding start = %q, want yesterday %q (session date %s)", got, yesterday, saidOn)
	}
	if got := memString(wedding, "end"); got != yesterday {
		t.Errorf("wedding end = %q, want %q", got, yesterday)
	}
	aliases, _ := wedding["aliases"].([]any)
	if len(aliases) != 4 {
		t.Errorf("wedding aliases = %v, want the first four of five", wedding["aliases"])
	}

	dentist := memFindItem(items, "牙医")
	if dentist == nil {
		t.Fatalf("timeline has no dentist item: %v", items)
	}
	if got, want := memString(dentist, "start"), started.AddDate(0, 0, -15).Format("2006-01-02"); got != want {
		t.Errorf("reversed range start = %q, want earlier date %q", got, want)
	}
	if got, want := memString(dentist, "end"), started.AddDate(0, 0, -14).Format("2006-01-02"); got != want {
		t.Errorf("reversed range end = %q, want later date %q", got, want)
	}
	if got := memString(dentist, "kind"); got != "plan" {
		t.Errorf("dentist kind = %q, want plan", got)
	}

	cat := memFindItem(items, "团子")
	if cat == nil {
		t.Fatalf("timeline has no cat item: %v", items)
	}
	if got := memString(cat, "kind"); got != "event" {
		t.Errorf("unknown kind normalized to %q, want event", got)
	}
	if got := memString(cat, "start"); got != saidOn {
		t.Errorf("unparsable start fell back to %q, want session date %q", got, saidOn)
	}
	if got := memNumber(cat, "turn"); got != 1 {
		t.Errorf("out-of-range turn = %d, want the batch minimum 1", got)
	}
	run.copyEvidence(t, "go-timeline-s1.jsonl", filepath.Join(s1Dir, "timeline.jsonl"))
	run.copyEvidence(t, "go-timeline-meta-s1.json", filepath.Join(s1Dir, "timeline_meta.json"))

	// ── 2. search_timeline and search_messages real tool calls ──────────
	s5 := run.createSession(t)
	t.Logf("S5 %s", s5)
	run.chat(t, s5, `CALL search_timeline {"pattern":"婚礼"}`)
	timelineParts := run.waitAssistantContaining(t, s5, "TOOL_RESULT_ECHO", "婚礼", s1)
	toolEcho := memLastAssistant(t, timelineParts)
	for _, needle := range []string{yesterday, `"kind"`} {
		if !strings.Contains(toolEcho, needle) {
			t.Errorf("search_timeline echo missing %q: %s", needle, toolEcho)
		}
	}

	replyToken := memAssistantTokenRe.FindString(memLastAssistant(t, run.parts(t, s1)))
	if replyToken == "" {
		t.Fatalf("S1 has no assistant reply token: %v", memPartContents(run.parts(t, s1), "assistant"))
	}
	run.chat(t, s5, fmt.Sprintf(`CALL search_messages {"pattern":%q,"role":"assistant"}`, replyToken))
	run.waitAssistantContaining(t, s5, "TOOL_RESULT_ECHO", replyToken, s1)

	run.chat(t, s5, `CALL search_messages {"pattern":"婚礼"}`)
	run.waitAssistantContaining(t, s5, "TOOL_RESULT_ECHO", "婚礼", s1)

	// ── 3. Backfill a session that predates the feature ─────────────────
	oldID := "session_20250101_093000_meme2e01"
	oldDir := filepath.Join(run.home, "queens", "default", "sessions", oldID)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("mkdir old session: %v", err)
	}
	events := []string{
		`{"type":"client_input_received","timestamp":"2025-01-01T09:30:01","data":{"content":"去年我在杭州买了一套房子。"}}`,
		`{"type":"client_input_received","timestamp":"2025-01-01T09:31:02","data":{"content":"前年我换了一辆黑色的车。"}}`,
	}
	if err := os.WriteFile(filepath.Join(oldDir, "events.jsonl"), []byte(strings.Join(events, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write old events: %v", err)
	}
	s3 := run.createSession(t) // starting this runtime arms the startup backfill
	t.Logf("S3 %s", s3)
	run.waitTimelineUserTurns(t, oldDir, 2, 15*time.Second)
	oldItems := memReadTimeline(t, oldDir)
	if wedding := memFindItem(oldItems, "婚礼"); wedding != nil {
		if got, want := memString(wedding, "start"), "2024-12-31"; got != want {
			t.Errorf("backfilled relative date = %q, want %q", got, want)
		}
	} else {
		t.Errorf("backfilled timeline has no wedding item: %v", oldItems)
	}
	run.copyEvidence(t, "go-timeline-backfill-2025.jsonl", filepath.Join(oldDir, "timeline.jsonl"))

	// ── 4. Past conversations + scoped recall ───────────────────────────
	globalDir := filepath.Join(run.home, "memories", "global")
	queenDir := filepath.Join(run.home, "memories", "agents", "queens", "default")
	for _, dir := range []string{globalDir, queenDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir memory dir: %v", err)
		}
	}
	memWrite := func(dir, name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write memory %s: %v", name, err)
		}
	}
	memWrite(globalDir, "profile_user.md", "---\nname: user profile\ndescription: stable facts about the user\ntype: profile\n---\n\n用户偏好简洁、直接的回复。\n")
	memWrite(globalDir, "project_atlas.md", "---\nname: Atlas project\ndescription: deployment notes for project Atlas\ntype: preference\n---\n\nAtlas 部署在 8080 端口。\n")
	memWrite(queenDir, "queen_notes.md", "---\nname: queen notes\ndescription: working notes for this queen\ntype: preference\n---\n\n这个 queen 负责记忆验证。\n")

	s4 := run.createSession(t)
	t.Logf("S4 %s", s4)
	ask := "帮我回忆一下我在婚礼上随礼的事情，还有北京的安排。"
	firstTurn := run.chat(t, s4, ask)
	if got := memCountContaining(firstTurn, "Recalled memories relevant"); got != 1 {
		t.Errorf("recall reminder blocks after first turn = %d, want 1", got)
	}
	if got := memCountContaining(firstTurn, "Excerpts from earlier conversations"); got != 1 {
		t.Errorf("past-conversation excerpt blocks after first turn = %d, want 1", got)
	}
	joined := strings.Join(memPartContents(firstTurn, "user"), "\n")
	for _, needle := range []string{"--- Global Memories ---", "project_atlas.md", "profile_user.md", s1, started.Format("2006-01-02 (Mon)")} {
		if !strings.Contains(joined, needle) {
			t.Errorf("first turn context missing %q:\n%s", needle, joined)
		}
	}
	if !strings.Contains(joined, "--- Queen Memories: default ---") || !strings.Contains(joined, "queen_notes.md") {
		t.Errorf("queen-scoped recall missing:\n%s", joined)
	}

	// The selector saw only the current query: one user message, no history.
	recallRequests := 0
	for _, record := range run.gw.RequestsOf(memgw.KindRecall) {
		if !strings.Contains(record.Body, ask) {
			continue
		}
		recallRequests++
		var body struct {
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal([]byte(record.Body), &body); err != nil {
			t.Fatalf("parse recall request: %v", err)
		}
		userMessages := 0
		for _, message := range body.Messages {
			if message.Role != "user" {
				continue
			}
			userMessages++
			var content string
			_ = json.Unmarshal(message.Content, &content)
			if !strings.Contains(content, "## User query") || !strings.Contains(content, ask) {
				t.Errorf("recall selector user message is not the current query: %q", content)
			}
			for _, stale := range []string{"云南", "团子", "相机"} {
				if strings.Contains(content, stale) {
					t.Errorf("recall selector carried earlier-turn text %q: %q", stale, content)
				}
			}
		}
		if userMessages != 1 {
			t.Errorf("recall selector user messages = %d, want 1 (query only)", userMessages)
		}
	}
	if recallRequests < 2 {
		t.Errorf("recall selector requests for the first turn = %d, want global + queen", recallRequests)
	}

	// Same content twice: scoped memory recall is not repeated (the upstream
	// _last_injected_recall guard), but the excerpt reminder re-renders on
	// every user prompt upstream — so two identical prompts yield two blocks.
	secondTurn := run.chat(t, s4, ask)
	if got := memCountContaining(secondTurn, "Recalled memories relevant"); got != 1 {
		t.Errorf("recall blocks after identical second turn = %d, want still 1", got)
	}
	if got := memCountContaining(secondTurn, "Excerpts from earlier conversations"); got != 2 {
		t.Errorf("past-conversation blocks after identical second turn = %d, want 2 (re-rendered per prompt)", got)
	}

	// Recall failure must not block the turn; pinned profile memory still rides.
	failParts := run.chat(t, s4, "FAILRECALL 今天天气怎么样？")
	if !memHasRoleContent(failParts, "assistant", "ASSISTANT_REPLY_") {
		t.Errorf("turn did not complete while the recall selector was down: %v", memPartContents(failParts, "assistant"))
	}
	lastRecallBlock := memLastContaining(failParts, "Recalled memories relevant")
	if !strings.Contains(lastRecallBlock, "profile_user.md") || strings.Contains(lastRecallBlock, "project_atlas.md") {
		t.Errorf("failed selector should fall back to pinned profile memory only:\n%s", lastRecallBlock)
	}

	run.writeEvidence(t, "go-conversation-s4.json", memMarshalParts(t, run.parts(t, s4)))

	// ── 5. Excerpt cap: eight matching earlier turns yield six excerpts ─
	// One turn per session, so the "extra turns from hot sessions" rule adds
	// nothing and the primary TopK (6) is the only limit in play.
	for index := 1; index <= 8; index++ {
		id := fmt.Sprintf("session_202502%02d_090000_topk%04d", index, index)
		dir := filepath.Join(run.home, "queens", "default", "sessions", id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir crafted session: %v", err)
		}
		event := fmt.Sprintf(
			`{"type":"client_input_received","timestamp":"2025-02-%02dT09:00:01","data":{"content":"我在第 %d 次帆船训练中完成了绕标。"}}`,
			index, index,
		)
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(event+"\n"), 0o644); err != nil {
			t.Fatalf("write crafted events: %v", err)
		}
	}
	s6 := run.createSession(t)
	t.Logf("S6 %s", s6)
	run.chat(t, s6, "帆船绕标训练记录")
	topParts := run.waitUserContaining(t, s6, "Excerpts from earlier conversations")
	topBlock := memLastContaining(topParts, "Excerpts from earlier conversations")
	datedLineRe := regexp.MustCompile(`^- \d{4}-\d{2}-\d{2} \([A-Z][a-z]{2}\), session_`)
	sailingLines := []string{}
	for _, line := range strings.Split(topBlock, "\n") {
		if datedLineRe.MatchString(line) {
			sailingLines = append(sailingLines, line)
		}
	}
	if len(sailingLines) != 6 {
		t.Errorf("excerpt lines for eight matching turns = %d, want the 6-deep TopK:\n%s", len(sailingLines), topBlock)
	}
	if got := strings.Count(topBlock, "帆船训练"); got != 6 {
		t.Errorf("sailing user lines in the excerpt block = %d, want 6:\n%s", got, topBlock)
	}
	run.writeEvidence(t, "go-conversation-s6-topk.json", memMarshalParts(t, run.parts(t, s6)))
}

func (m *memRun) waitUserContaining(t *testing.T, sessionID, needle string) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		parts := m.parts(t, sessionID)
		if memHasRoleContent(parts, "user", needle) {
			return parts
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %s: no user part containing %q within 10s", sessionID, needle)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func testGoTimelineFirstTurn(t *testing.T) {
	// Default batch (3) and cooldown (300s): the first completed turn must
	// still extract, because upstream's last_run starts at 0.0 and
	// time.monotonic() - 0.0 is "since boot". time.Since on the zero time
	// saturates the same way here.
	run := newMemRun(t, "first")
	sessionID := run.createSession(t)
	t.Logf("S %s", sessionID)
	run.chat(t, sessionID, "昨天我去参加了同事的婚礼，随礼 800 元。")
	dir := run.sessionDir(sessionID)
	run.waitTimelineUserTurns(t, dir, 1, 15*time.Second)
	run.copyEvidence(t, "go-timeline-first-turn.jsonl", filepath.Join(dir, "timeline.jsonl"))
}

func testGoTimelineStopFlush(t *testing.T) {
	// No completed turn: the message arrives as an event on disk (the shape a
	// turn already in flight leaves behind), so the live trigger cannot have
	// seen it. The stop path is the only writer left.
	run := newMemRun(t, "stop")
	sessionID := run.createSession(t)
	t.Logf("S2 %s", sessionID)
	dir := run.sessionDir(sessionID)
	event := `{"type":"client_input_received","timestamp":"2026-11-20T18:00:00","data":{"content":"我上周买了一台相机，花了 5000 元。"}}`
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open events.jsonl: %v", err)
	}
	if _, err := file.WriteString(event + "\n"); err != nil {
		t.Fatalf("append events.jsonl: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close events.jsonl: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "timeline.jsonl")); err == nil {
		t.Fatalf("timeline existed before stop; nothing to measure")
	}
	run.stopSession(t, sessionID)
	run.waitTimelineUserTurns(t, dir, 1, 10*time.Second)
	stopMeta := memReadTimelineMeta(t, dir)
	if got := memNumber(stopMeta, "user_turns"); got != 1 {
		t.Errorf("stop flush user_turns = %v, want 1 (%v)", stopMeta["user_turns"], stopMeta)
	}
	stopItems := memReadTimeline(t, dir)
	if wedding := memFindItem(stopItems, "婚礼"); wedding == nil {
		t.Errorf("stop flush wrote no items: %v", stopItems)
	}
	run.copyEvidence(t, "go-timeline-s2-stop-flush.jsonl", filepath.Join(dir, "timeline.jsonl"))
	run.writeEvidence(t, "go-timeline-s2-stop-meta.json", mustMarshal(t, memReadTimelineMeta(t, dir)))
}

func memHasRoleContent(parts []map[string]any, role, needle string) bool {
	for _, content := range memPartContents(parts, role) {
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func memLastAssistant(t *testing.T, parts []map[string]any) string {
	t.Helper()
	last := ""
	for _, content := range memPartContents(parts, "assistant") {
		if strings.TrimSpace(content) != "" {
			last = content
		}
	}
	if last == "" {
		t.Fatalf("no assistant prose in transcript")
	}
	return last
}

func memLastContaining(parts []map[string]any, needle string) string {
	last := ""
	for _, content := range memPartContents(parts, "user") {
		if strings.Contains(content, needle) {
			last = content
		}
	}
	return last
}

func memMarshalParts(t *testing.T, parts []map[string]any) []byte {
	t.Helper()
	encoded, err := json.MarshalIndent(parts, "", "  ")
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	return encoded
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return encoded
}
