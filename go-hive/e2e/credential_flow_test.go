//go:build e2e

package e2e

// Credential-flow gate: proves the real `hive serve` binary resolves
// OPENAI_API_KEY / OPENAI_BASE_URL and sends the key to the upstream as
// `Authorization: Bearer <key>`. No fake Completer and no production code
// injection: the test only talks HTTP to the built binary.
//
// (env api_key + api_base -> bearer-authorized chat completion)

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

const credTestKey = "test-key"

const credFakeCompletion = `{"id":"chatcmpl-cred-e2e","object":"chat.completion","created":0,` +
	`"choices":[{"index":0,"message":{"role":"assistant","content":"credential-ok"},` +
	`"finish_reason":"stop"}],"model":"gpt-4o-mini",` +
	`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

type credUpstreamRequest struct {
	path     string
	auth     string
	body     map[string]any
	parseErr string
}

// credUpstream records every request the real binary sends to the fake
// OpenAI-compatible upstream. Assertions run in the test goroutine, never in
// the handler, so a late request cannot log after the test has finished.
type credUpstream struct {
	mu       sync.Mutex
	requests []credUpstreamRequest
}

func (u *credUpstream) snapshot() []credUpstreamRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]credUpstreamRequest(nil), u.requests...)
}

func newCredUpstream(t *testing.T) (*httptest.Server, *credUpstream) {
	t.Helper()
	recorder := &credUpstream{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		record := credUpstreamRequest{
			path: r.URL.Path,
			auth: r.Header.Get("Authorization"),
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			record.parseErr = err.Error() + ": " + string(raw)
		} else {
			record.body = body
		}
		recorder.mu.Lock()
		recorder.requests = append(recorder.requests, record)
		recorder.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, credFakeCompletion)
	}))
	t.Cleanup(server.Close)
	return server, recorder
}

type credLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *credLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *credLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func credModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve e2e test file path")
	}
	return filepath.Dir(filepath.Dir(file))
}

func credFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release free port: %v", err)
	}
	return port
}

// credChildEnv isolates the server from the developer's shell: no ambient
// provider keys, no shared hive home, no desktop token.
func credChildEnv(homeDir, baseURL string) []string {
	dropped := map[string]bool{
		"OPENAI_API_KEY":          true,
		"OPENAI_BASE_URL":         true,
		"OPENAI_API_BASE":         true,
		"HIVE_API_KEY":            true,
		"HIVE_HOME":               true,
		"HIVE_DESKTOP_TOKEN":      true,
		"HIVE_DESKTOP_PARENT_PID": true,
		"HIVE_LLM_BASE_URL":       true,
	}
	env := make([]string, 0, len(os.Environ())+4)
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
	return append(env,
		"OPENAI_API_KEY="+credTestKey,
		"OPENAI_BASE_URL="+baseURL,
		"OPENAI_API_BASE="+baseURL,
		"HIVE_HOME="+homeDir,
	)
}

func credStopHive(t *testing.T, cmd *exec.Cmd, exited <-chan struct{}, logs *credLogBuffer) {
	t.Helper()
	select {
	case <-exited:
		return
	default:
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
	if t.Failed() {
		t.Logf("hive serve logs:\n%s", logs.String())
	}
}

func credDoJSON(t *testing.T, client *http.Client, method, url string, payload any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode %s %s body: %v", method, url, err)
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("build %s %s request: %v", method, url, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s response: %v", method, url, err)
	}
	return resp.StatusCode, raw
}

func credMessagesContain(messages []any, needle string) bool {
	for _, entry := range messages {
		message, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		content, _ := message["content"].(string)
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

func credPartsContain(parts []map[string]any, needle string) bool {
	for _, part := range parts {
		content, _ := part["content"].(string)
		if strings.Contains(content, needle) {
			return true
		}
	}
	return false
}

// TestCredentialFlowEnvKeyReachesUpstream is the credential gate: build the
// real binary, serve it against a fake OpenAI-compatible upstream, and prove
// OPENAI_API_KEY becomes `Authorization: Bearer test-key` on the wire.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.__init__
func TestCredentialFlowEnvKeyReachesUpstream(t *testing.T) {
	upstream, recorder := newCredUpstream(t)

	moduleRoot := credModuleRoot(t)
	workDir := t.TempDir()
	homeDir := filepath.Join(t.TempDir(), "hive-home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("create temp hive home: %v", err)
	}

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
	serverCmd.Dir = workDir
	serverCmd.Env = credChildEnv(homeDir, upstream.URL)
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

	client := &http.Client{Timeout: 5 * time.Second}

	// Wait for the real server to answer /api/health.
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

	status, raw := credDoJSON(t, client, http.MethodPost, baseURL+"/api/sessions", map[string]any{
		"model": "gpt-4o-mini",
	})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("POST /api/sessions = %d, want 200/201; body: %s; logs:\n%s", status, raw, logs.String())
	}
	var created map[string]any
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode POST /api/sessions response: %v; body: %s", err, raw)
	}
	sessionID, _ := created["session_id"].(string)
	if sessionID == "" {
		t.Fatalf("POST /api/sessions returned no session_id; body: %s", raw)
	}

	status, raw = credDoJSON(t, client, http.MethodPost,
		baseURL+"/api/sessions/"+sessionID+"/chat", map[string]any{"message": "hello"})
	if status != http.StatusOK {
		t.Fatalf("POST /api/sessions/%s/chat = %d, want 200; body: %s; logs:\n%s",
			sessionID, status, raw, logs.String())
	}
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		t.Fatalf("decode chat response: %v; body: %s", err, raw)
	}
	if delivered, _ := chat["delivered"].(bool); !delivered {
		t.Fatalf("chat response delivered != true; body: %s", raw)
	}

	// The upstream must actually receive the turn; a 200 from the route alone
	// does not prove the credential made it onto the wire.
	deadline = time.Now().Add(20 * time.Second)
	var requests []credUpstreamRequest
	for time.Now().Before(deadline) {
		requests = recorder.snapshot()
		if len(requests) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if len(requests) == 0 {
		t.Fatalf("fake upstream received no completion request; logs:\n%s", logs.String())
	}

	sawChatTurn := false
	for index, request := range requests {
		if request.parseErr != "" {
			t.Errorf("upstream request %d was not JSON: %s", index, request.parseErr)
			continue
		}
		if request.auth != "Bearer "+credTestKey {
			t.Errorf("upstream request %d Authorization = %q, want %q",
				index, request.auth, "Bearer "+credTestKey)
		}
		if !strings.HasSuffix(request.path, "/chat/completions") {
			t.Errorf("upstream request %d path = %q, want suffix /chat/completions", index, request.path)
		}
		model, _ := request.body["model"].(string)
		if strings.TrimSpace(model) == "" {
			t.Errorf("upstream request %d body has no model: %v", index, request.body)
		}
		messages, _ := request.body["messages"].([]any)
		if len(messages) == 0 {
			t.Errorf("upstream request %d body has no messages: %v", index, request.body)
		}
		if model == "gpt-4o-mini" && credMessagesContain(messages, "hello") {
			sawChatTurn = true
		}
	}
	if !sawChatTurn {
		t.Errorf("no upstream request carried model gpt-4o-mini with the user turn; requests: %+v", requests)
	}

	// The reply from the fake upstream must land in the on-disk transcript.
	transcriptDir := filepath.Join(homeDir, "queens", "default", "sessions", sessionID, "conversations")
	deadline = time.Now().Add(20 * time.Second)
	var parts []map[string]any
	for {
		read, err := agentloop.NewFileConversationStore(transcriptDir).ReadParts()
		if err == nil {
			parts = read
			if credPartsContain(parts, "credential-ok") {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("transcript %s never contained the upstream reply; parts: %v; logs:\n%s",
				transcriptDir, parts, logs.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}
