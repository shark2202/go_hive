//go:build e2e

package e2e

// Shared plumbing for the real-model concept-flow dual run. Every scenario
// starts the real `hive serve` binary against a temp HIVE_HOME whose
// configuration points at a pass-through recording proxy; the proxy forwards
// to the real upstream and records each request/response. The Python dual run
// (go_hive/scripts/concept_flows_real_python.py) writes the same evidence
// shape, so go_hive/scripts/concept_flows_real_compare.py can diff the two.
//
// Opt-in only: HIVE_REAL_LLM_E2E=1 plus a reachable HIVE_REAL_LLM_UPSTREAM and
// a key in HIVE_REAL_LLM_KEY_ENV (default BROADTOKEN_API_KEY). The key is read
// by the proxy process only; the config under test carries a dummy value.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/e2e/recordproxy"
)

const (
	conceptProxyKeyEnv = "CF_PROXY_KEY"
	conceptProxyKey    = "cf-proxy-placeholder"
)

type conceptEnv struct {
	upstream string
	apiKey   string
	outRoot  string
	model    string
}

func conceptLoadEnv(t *testing.T) conceptEnv {
	t.Helper()
	if os.Getenv("HIVE_REAL_LLM_E2E") != "1" {
		t.Skip("real-model concept-flow E2E is opt-in: set HIVE_REAL_LLM_E2E=1")
	}
	upstream := strings.TrimSpace(os.Getenv("HIVE_REAL_LLM_UPSTREAM"))
	if upstream == "" {
		t.Fatal("HIVE_REAL_LLM_UPSTREAM is required (e.g. https://token.tokenbroad.com/v1)")
	}
	keyEnv := strings.TrimSpace(os.Getenv("HIVE_REAL_LLM_KEY_ENV"))
	if keyEnv == "" {
		keyEnv = "BROADTOKEN_API_KEY"
	}
	apiKey := strings.TrimSpace(os.Getenv(keyEnv))
	if apiKey == "" {
		t.Fatalf("%s is empty; the recording proxy needs the real upstream key", keyEnv)
	}
	outRoot := strings.TrimSpace(os.Getenv("HIVE_CONCEPT_OUT"))
	if outRoot == "" {
		outRoot = t.TempDir()
	}
	if err := os.MkdirAll(outRoot, 0o755); err != nil {
		t.Fatalf("create evidence root: %v", err)
	}
	model := strings.TrimSpace(os.Getenv("HIVE_REAL_LLM_MODEL"))
	if model == "" {
		model = "deepseek-flash-1017"
	}
	return conceptEnv{upstream: upstream, apiKey: apiKey, outRoot: outRoot, model: model}
}

// conceptProxy starts the recording proxy and returns its OpenAI-style base
// URL (…/v1) and the server so a test can read records.
func conceptProxy(t *testing.T, env conceptEnv, logPath string) (string, *recordproxy.Server) {
	t.Helper()
	proxy, err := recordproxy.New(recordproxy.Options{
		Upstream: env.upstream,
		APIKey:   env.apiKey,
		LogPath:  logPath,
	})
	if err != nil {
		t.Fatalf("start recording proxy: %v", err)
	}
	server := httptest.NewServer(proxy.Handler())
	t.Cleanup(func() {
		server.Close()
		_ = proxy.Close()
	})
	return server.URL + "/v1", proxy
}

// conceptWriteConfig writes configuration.json pointing every model slot at
// the proxy. extra merges additional top-level sections (worker_llm,
// queen_llm, judge…).
func conceptWriteConfig(t *testing.T, home, proxyURL, model string, extra map[string]any) {
	t.Helper()
	config := map[string]any{
		"llm": map[string]any{
			"provider":        "openai",
			"model":           model,
			"api_base":        proxyURL,
			"api_key_env_var": conceptProxyKeyEnv,
		},
	}
	for key, value := range extra {
		config[key] = value
	}
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("encode configuration.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), encoded, 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
}

// conceptWriteColony materialises a real colony: metadata.json plus the worker
// definition the colony host runs.
func conceptWriteColony(t *testing.T, home, colonyID, queenName string, worker map[string]any) string {
	t.Helper()
	colonyPath := filepath.Join(home, "colonies", colonyID)
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatalf("create colony dir: %v", err)
	}
	metadata := map[string]any{"name": colonyID, "created_at": float64(time.Now().Unix())}
	if queenName != "" {
		metadata["queen_name"] = queenName
	}
	conceptWriteJSON(t, filepath.Join(colonyPath, "metadata.json"), metadata)
	conceptWriteJSON(t, filepath.Join(colonyPath, "worker.json"), worker)
	return colonyPath
}

func conceptWriteJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, encoded, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// conceptServe is one started `hive serve` process.
type conceptServe struct {
	t      *testing.T
	home   string
	base   string
	client *http.Client
	cmd    *exec.Cmd
	logs   *credLogBuffer
	exited chan struct{}
}

func conceptBuildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "hive")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hive")
	build.Dir = credModuleRoot(t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOPROXY=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/hive: %v\n%s", err, output)
	}
	return binary
}

func conceptStartServe(t *testing.T, binary, home string) *conceptServe {
	t.Helper()
	port := credFreePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	cmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", fmt.Sprintf("%d", port))
	cmd.Dir = t.TempDir()
	cmd.Env = append(conceptChildEnv(home), "CF_PROXY_KEY="+conceptProxyKey)
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start hive serve: %v", err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	run := &conceptServe{t: t, home: home, base: base, client: &http.Client{Timeout: 120 * time.Second}, cmd: cmd, logs: logs, exited: exited}
	t.Cleanup(run.stop)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("hive serve exited before /api/health; logs:\n%s", logs.String())
		default:
		}
		resp, err := run.client.Get(base + "/api/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return run
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hive serve did not answer /api/health; logs:\n%s", logs.String())
	return nil
}

// conceptChildEnv isolates serve from the developer shell: no ambient provider
// keys, no shared hive home.
func conceptChildEnv(homeDir string) []string {
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
		if dropped[name] || strings.EqualFold(name, "BROADTOKEN_API_KEY") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "HIVE_HOME="+homeDir)
}

func (s *conceptServe) stop() {
	if s == nil || s.cmd == nil {
		return
	}
	select {
	case <-s.exited:
		return
	default:
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.exited:
	case <-time.After(20 * time.Second):
		_ = s.cmd.Process.Kill()
	}
}

func (s *conceptServe) createSession(payload map[string]any) (string, map[string]any) {
	s.t.Helper()
	status, raw := credDoJSON(s.t, s.client, http.MethodPost, s.base+"/api/sessions", payload)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if status != http.StatusOK && status != http.StatusCreated {
		s.t.Fatalf("POST /api/sessions = %d %s; logs:\n%s", status, raw, s.logs.String())
	}
	sessionID, _ := body["session_id"].(string)
	if sessionID == "" {
		s.t.Fatalf("POST /api/sessions returned no session_id: %s", raw)
	}
	return sessionID, body
}

func (s *conceptServe) chat(sessionID, message string) {
	s.t.Helper()
	status, raw := credDoJSON(s.t, s.client, http.MethodPost, s.base+"/api/sessions/"+sessionID+"/chat", map[string]any{"message": message})
	if status != http.StatusOK {
		s.t.Fatalf("POST /chat = %d %s; logs:\n%s", status, raw, s.logs.String())
	}
}

func (s *conceptServe) sessionJSON(sessionID string) map[string]any {
	s.t.Helper()
	status, raw := credDoJSON(s.t, s.client, http.MethodGet, s.base+"/api/sessions/"+sessionID, nil)
	if status != http.StatusOK {
		s.t.Fatalf("GET /api/sessions/%s = %d %s", sessionID, status, raw)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body
}

func (s *conceptServe) snapshot(sessionID string) map[string]any {
	s.t.Helper()
	status, raw := credDoJSON(s.t, s.client, http.MethodGet, s.base+"/api/sessions/"+sessionID+"/snapshot", nil)
	if status != http.StatusOK {
		return map[string]any{"http_status": status}
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body
}

func conceptSessionDir(t *testing.T, home, sessionID string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(home, "queens", "*", "sessions", sessionID))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// conceptParts reads a FileConversationStore transcript.
func conceptParts(t *testing.T, dir string) []map[string]any {
	t.Helper()
	parts, err := readConversationParts(dir)
	if err != nil {
		return nil
	}
	return parts
}

// readConversationParts parses conversations/parts/*.json in seq order.
func readConversationParts(sessionDir string) ([]map[string]any, error) {
	paths, err := filepath.Glob(filepath.Join(sessionDir, "conversations", "parts", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var part map[string]any
		if err := json.Unmarshal(raw, &part); err != nil {
			continue
		}
		out = append(out, part)
	}
	return out, nil
}

// conceptWait polls until fn is true or the deadline expires.
func conceptWait(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s after %s", what, timeout)
}

// conceptWaitForPart waits for any transcript part (across dirs) whose content
// contains needle.
func conceptWaitForPart(t *testing.T, dirs []string, needle string, timeout time.Duration) map[string]any {
	t.Helper()
	var found map[string]any
	conceptWait(t, timeout, "transcript part containing "+needle, func() bool {
		for _, dir := range dirs {
			for _, part := range conceptParts(t, dir) {
				if content, _ := part["content"].(string); strings.Contains(content, needle) {
					found = part
					return true
				}
			}
		}
		return false
	})
	return found
}

// conceptWaitForModel waits for a proxy record satisfying want.
func conceptWaitForModel(t *testing.T, proxy *recordproxy.Server, timeout time.Duration, what string, want func(recordproxy.Record) bool) recordproxy.Record {
	t.Helper()
	var found recordproxy.Record
	conceptWait(t, timeout, "model request "+what, func() bool {
		for _, record := range proxy.Requests() {
			if want(record) {
				found = record
				return true
			}
		}
		return false
	})
	return found
}

// conceptReadEvents reads the session event log, truncating oversized payloads
// so one prompt dump cannot bloat the evidence file.
func conceptReadEvents(sessionDir string) []map[string]any {
	raw, err := os.ReadFile(filepath.Join(sessionDir, "events.jsonl"))
	if err != nil {
		return nil
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		events = append(events, conceptTrimEvent(event))
	}
	return events
}

func conceptTrimEvent(event map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"type", "stream_id", "node_id", "execution_id", "seq"} {
		if value, present := event[key]; present {
			out[key] = value
		}
	}
	if data, ok := event["data"].(map[string]any); ok {
		out["data"] = conceptTrimValue(data, 0)
	}
	return out
}

// conceptTrimValue keeps scalar fields and caps deep/large strings.
func conceptTrimValue(value any, depth int) any {
	switch typed := value.(type) {
	case string:
		if len(typed) > 1200 {
			return typed[:1200] + "…"
		}
		return typed
	case map[string]any:
		if depth >= 2 {
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			return map[string]any{"keys": keys}
		}
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = conceptTrimValue(item, depth+1)
		}
		return out
	case []any:
		if len(typed) > 8 {
			return typed[:8]
		}
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, conceptTrimValue(item, depth+1))
		}
		return out
	default:
		return value
	}
}

// conceptPartView is the diffable projection of a transcript part.
func conceptPartView(part map[string]any) map[string]any {
	out := map[string]any{"role": part["role"]}
	if content, _ := part["content"].(string); content != "" {
		if len(content) > 4000 {
			content = content[:4000] + "…"
		}
		out["content"] = content
	}
	if value, present := part["is_client_input"]; present {
		out["is_client_input"] = value
	}
	if calls, ok := part["tool_calls"].([]any); ok {
		names := make([]string, 0, len(calls))
		for _, entry := range calls {
			call, _ := entry.(map[string]any)
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			names = append(names, strings.TrimSpace(name+" "+arguments))
		}
		out["tool_calls"] = names
	}
	return out
}

func conceptPartsView(parts []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		out = append(out, conceptPartView(part))
	}
	return out
}

// conceptModelRequests projects the proxy log for cross-side comparison.
func conceptModelRequests(proxy *recordproxy.Server) []map[string]any {
	records := proxy.Requests()
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"seq":           record.Seq,
			"kind":          conceptClassify(record),
			"model":         record.Model,
			"path":          record.Path,
			"status":        record.Status,
			"tools":         record.Tools,
			"message_count": record.MessageCount,
			"tool_calls":    record.ToolCalls,
			"finish_reason": record.FinishReason,
			"content":       conceptCap(record.Content, 1200),
			"error":         record.Error,
		})
	}
	return out
}

// conceptClassify guesses the calling role from the advertised tool surface.
func conceptClassify(record recordproxy.Record) string {
	has := func(name string) bool {
		for _, tool := range record.Tools {
			if tool == name {
				return true
			}
		}
		return false
	}
	switch {
	case len(record.Tools) == 0:
		return "no-tools"
	case has("run_worker") && has("report_to_parent"):
		return "colony-lead"
	case has("run_worker"):
		return "queen"
	case has("report_to_parent"):
		return "worker"
	default:
		return "other"
	}
}

func conceptCap(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// conceptColonyFiles lists the colony tree (relative paths) after a run.
func conceptColonyFiles(colonyPath string) []string {
	var files []string
	_ = filepath.Walk(colonyPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		relative, relErr := filepath.Rel(colonyPath, path)
		if relErr != nil || relative == "." {
			return nil
		}
		if info.IsDir() {
			files = append(files, relative+"/")
			// The worker conversation bodies are recorded separately; skip
			// walking thousands of part files twice.
			return nil
		}
		files = append(files, relative)
		return nil
	})
	sort.Strings(files)
	return files
}

// conceptFindWorkers returns the worker directories under a colony.
func conceptFindWorkers(colonyPath string) []string {
	matches, _ := filepath.Glob(filepath.Join(colonyPath, "workers", "worker_*"))
	sort.Strings(matches)
	return matches
}

var conceptWorkerReportPattern = regexp.MustCompile(`report_to_parent`)

// conceptWorkerReport finds the last report_to_parent tool call in a worker
// transcript and returns its decoded arguments.
func conceptWorkerReport(parts []map[string]any) map[string]any {
	var report map[string]any
	for _, part := range parts {
		calls, _ := part["tool_calls"].([]any)
		for _, entry := range calls {
			call, _ := entry.(map[string]any)
			function, _ := call["function"].(map[string]any)
			if name, _ := function["name"].(string); name != "report_to_parent" {
				continue
			}
			arguments, _ := function["arguments"].(string)
			var decoded map[string]any
			if err := json.Unmarshal([]byte(arguments), &decoded); err == nil {
				report = decoded
			}
		}
	}
	return report
}

// conceptWorkerActions lists the tool names a worker actually called.
func conceptWorkerActions(parts []map[string]any) []string {
	var actions []string
	for _, part := range parts {
		calls, _ := part["tool_calls"].([]any)
		for _, entry := range calls {
			call, _ := entry.(map[string]any)
			function, _ := call["function"].(map[string]any)
			if name, _ := function["name"].(string); name != "" {
				actions = append(actions, name)
			}
		}
	}
	return actions
}

func conceptWriteEvidence(t *testing.T, dir string, evidence map[string]any) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Errorf("create evidence dir %s: %v", dir, err)
		return
	}
	evidence["generated_at"] = time.Now().UTC().Format(time.RFC3339)
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Errorf("encode evidence: %v", err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "evidence.json"), encoded, 0o644); err != nil {
		t.Errorf("write evidence: %v", err)
	}
}

// conceptSnapshotView keeps the park-relevant snapshot fields only.
func conceptSnapshotView(snapshot map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"activity", "interrupt_cause", "park_reason", "pending_questions", "is_streaming", "current_execution_id", "last_event_at", "seq"} {
		if value, present := snapshot[key]; present {
			out[key] = value
		}
	}
	return out
}

// conceptWaitIdle waits until the session snapshot reports no running
// execution (or the timeout expires).
func conceptWaitIdle(t *testing.T, serve *conceptServe, sessionID string, timeout time.Duration) {
	t.Helper()
	conceptWait(t, timeout, "session idle", func() bool {
		snapshot := serve.snapshot(sessionID)
		activity, _ := snapshot["activity"].(string)
		return activity == "" || activity == "awaiting_user" || activity == "idle" || activity == "parked"
	})
}
