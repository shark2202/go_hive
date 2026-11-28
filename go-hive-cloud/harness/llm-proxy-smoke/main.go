// Command llm-proxy-smoke drives the M5 LLM proxy end-to-end with the REAL
// go_hive Anthropic client and the REAL hive-cloud binary. A local fake
// upstream plays both Anthropic Messages and the OpenAI-compatible images
// endpoint; no external network is contacted.
//
// Checks cover: non-streaming Messages (real client), SSE streaming with
// per-event flush (real client), upstream 4xx forwarding, invalid-credential
// 401, cancellation propagation, the unconfigured answers, image pass-through
// and upstream-credential isolation. Evidence never contains a token or key.
//
// Module note: nested under github.com/shark2202/go_hive/go-hive/ so the Go
// internal-package rule allows importing internal/llm.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const (
	jwtSecret    = "m5-smoke-jwt-secret-0123456789abcdef"
	streamSecret = "m5-smoke-stream-secret-0123456789abcdef"
	upstreamKey  = "m5-smoke-upstream-messages-key"
	imagesKey    = "m5-smoke-upstream-images-key"
	teamID       = "team_m5"
	userID       = "u_m5"
)

const messageJSON = `{"id":"msg_smoke","type":"message","role":"assistant","model":"queen","content":[{"type":"text","text":"hello from fake upstream"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":5}}`

const streamHead = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_smoke\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"queen\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"

const streamTail = "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

type requestRecord struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

type fakeUpstream struct {
	server *httptest.Server

	mu   sync.Mutex
	last requestRecord
	mode atomic.Value

	streamRelease  chan struct{}
	streamReleased atomic.Bool

	hangStarted  chan struct{}
	hangCanceled chan struct{}
	hangOnce     sync.Once
}

func newFakeUpstream() *fakeUpstream {
	f := &fakeUpstream{
		streamRelease: make(chan struct{}, 1),
		hangStarted:   make(chan struct{}),
		hangCanceled:  make(chan struct{}),
	}
	f.mode.Store("messages_ok")
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	return f
}

func (f *fakeUpstream) close() { f.server.Close() }

func (f *fakeUpstream) setMode(mode string) {
	f.mode.Store(mode)
	f.mu.Lock()
	f.last = requestRecord{}
	f.mu.Unlock()
}

func (f *fakeUpstream) snapshot() requestRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return requestRecord{Method: f.last.Method, Path: f.last.Path, Header: f.last.Header.Clone(), Body: append([]byte(nil), f.last.Body...)}
}

func (f *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.last = requestRecord{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: raw}
	f.mu.Unlock()

	switch f.mode.Load().(string) {
	case "messages_ok":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_msg_smoke")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, messageJSON)
	case "messages_stream":
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_stream_smoke")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, streamHead)
		flusher.Flush()
		select {
		case <-f.streamRelease:
			f.streamReleased.Store(true)
		case <-time.After(2 * time.Second):
		}
		_, _ = io.WriteString(w, streamTail)
		flusher.Flush()
	case "messages_429":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("retry-after", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	case "messages_hang":
		f.hangOnce.Do(func() { close(f.hangStarted) })
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(f.hangCanceled)
	case "images_ok":
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("openai-processing-ms", "12")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"aGVsbG8="}],"usage":{"total_tokens":7}}`)
	case "images_402":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"out of credits","type":"insufficient_quota"}}`)
	default:
		http.Error(w, "unknown smoke mode", http.StatusInternalServerError)
	}
}

type cloudServer struct {
	cmd     *exec.Cmd
	baseURL string
	logPath string
}

var listenRE = regexp.MustCompile(`listening on (http://[^ ]+)`)

func startCloud(bin, dir string, extra ...string) (*cloudServer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	logPath := filepath.Join(dir, "serve.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	args := append([]string{"serve", "--listen", "127.0.0.1:0", "--data-dir", filepath.Join(dir, "data"),
		"--jwt-secret", jwtSecret, "--stream-secret", streamSecret}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(logPath)
		if match := listenRE.FindStringSubmatch(string(raw)); len(match) == 2 {
			return &cloudServer{cmd: cmd, baseURL: match[1], logPath: logPath}, nil
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	logFile.Close()
	raw, _ := os.ReadFile(logPath)
	return nil, fmt.Errorf("hive-cloud did not listen: %s", strings.TrimSpace(string(raw)))
}

func (c *cloudServer) stop() {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
}

func mintToken(bin, typ, ttl string) (string, error) {
	dir, err := os.MkdirTemp("", "m5-token-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	args := []string{"token", "--data-dir", dir, "--type", typ, "--ttl", ttl,
		"--user-id", userID, "--team-id", teamID, "--jwt-secret", jwtSecret, "--stream-secret", streamSecret}
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return "", fmt.Errorf("mint %s token: %w", typ, err)
	}
	return strings.TrimSpace(string(out)), nil
}

type check struct {
	Name   string         `json:"name"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
	Error  string         `json:"error,omitempty"`
}

type evidence struct {
	Harness    string         `json:"harness"`
	BaseURL    string         `json:"base_url"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	OK         bool           `json:"ok"`
	Checks     []check        `json:"checks"`
	Notes      []string       `json:"notes"`
	Env        map[string]any `json:"env,omitempty"`
}

func fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func main() {
	bin := strings.TrimSpace(os.Getenv("HIVE_CLOUD_BIN"))
	if bin == "" {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke: HIVE_CLOUD_BIN is required")
		os.Exit(2)
	}
	outPath := strings.TrimSpace(os.Getenv("EVIDENCE_OUT"))
	if outPath == "" {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke: EVIDENCE_OUT is required")
		os.Exit(2)
	}

	started := time.Now().UTC()
	out := evidence{
		Harness:   "llm-proxy-smoke (real go_hive Anthropic client + real hive-cloud binary + fake local upstream)",
		StartedAt: started.Format(time.RFC3339Nano),
		Notes: []string{
			"No external network: the upstream is an in-process httptest server.",
			"Evidence contains statuses, counters and key fingerprints; no token or key plaintext.",
		},
		Env: map[string]any{
			"upstream_key_fingerprint": fingerprint(upstreamKey),
			"images_key_fingerprint":   fingerprint(imagesKey),
		},
	}
	add := func(name string, ok bool, detail map[string]any, err error) {
		item := check{Name: name, OK: ok, Detail: detail}
		if err != nil {
			item.Error = err.Error()
		}
		out.Checks = append(out.Checks, item)
		if !ok {
			fmt.Printf("FAIL %s: %v\n", name, err)
		} else {
			fmt.Printf("ok   %s\n", name)
		}
	}

	upstream := newFakeUpstream()
	defer upstream.close()

	work, err := os.MkdirTemp("", "m5-smoke-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(work)

	server, err := startCloud(bin, filepath.Join(work, "configured"),
		"--llm-upstream-url", upstream.server.URL,
		"--llm-upstream-key", upstreamKey,
		"--llm-upstream-auth", "bearer",
		"--llm-request-timeout", "5s",
		"--images-upstream-url", upstream.server.URL+"/v1/images/generations",
		"--images-upstream-key", imagesKey,
		"--images-request-timeout", "5s",
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke:", err)
		os.Exit(1)
	}
	defer server.stop()
	out.BaseURL = server.baseURL

	unconfigured, err := startCloud(bin, filepath.Join(work, "unconfigured"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke:", err)
		os.Exit(1)
	}
	defer unconfigured.stop()

	streamToken, err := mintToken(bin, "stream", "2h")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke:", err)
		os.Exit(1)
	}
	userJWT, err := mintToken(bin, "user", "2h")
	if err != nil {
		fmt.Fprintln(os.Stderr, "llm-proxy-smoke:", err)
		os.Exit(1)
	}

	// The real client decides Bearer vs x-api-key from apiBaseNeedsBearerAuth;
	// pointing HIVE_LLM_BASE_URL at this local server makes the observed Hive
	// proxy auth path explicit.
	if err := os.Setenv("HIVE_LLM_BASE_URL", server.baseURL); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	provider := llm.NewHTTPProvider(llm.ProviderOptions{Model: "hive/queen", APIKey: streamToken, APIBase: server.baseURL})
	client := &http.Client{Timeout: 10 * time.Second}

	// ---- 1. non-streaming Messages with the real go_hive client ----------
	upstream.setMode("messages_ok")
	completion, completeErr := provider.Complete(context.Background(),
		[]map[string]any{{"role": "user", "content": "ping"}},
		llm.CompleteOptions{MaxTokens: 32})
	nonstreamDetail := map[string]any{
		"content":       completion.Content,
		"model":         completion.Model,
		"input_tokens":  completion.InputTokens,
		"output_tokens": completion.OutputTokens,
	}
	if completeErr != nil || completion.Content != "hello from fake upstream" ||
		completion.InputTokens != 3 || completion.OutputTokens != 5 {
		add("messages_nonstream_real_client", false, nonstreamDetail, fmt.Errorf("completion=%+v err=%v", completion, completeErr))
	} else {
		add("messages_nonstream_real_client", true, nonstreamDetail, nil)
	}
	firstUpstream := upstream.snapshot()
	isolationDetail := map[string]any{
		"method":                  firstUpstream.Method,
		"path":                    firstUpstream.Path,
		"client_auth_header_used": firstUpstream.Header.Get("Authorization") == "Bearer "+upstreamKey,
		"client_token_in_headers": headerContains(firstUpstream.Header, streamToken),
		"model_in_body":           bytes.Contains(firstUpstream.Body, []byte(`"model":"queen"`)),
	}
	if firstUpstream.Method != http.MethodPost || firstUpstream.Path != "/v1/messages" ||
		firstUpstream.Header.Get("Authorization") != "Bearer "+upstreamKey ||
		headerContains(firstUpstream.Header, streamToken) ||
		!bytes.Contains(firstUpstream.Body, []byte(`"model":"queen"`)) {
		add("upstream_credential_isolation", false, isolationDetail, fmt.Errorf("upstream request did not carry the configured credential/model"))
	} else {
		add("upstream_credential_isolation", true, isolationDetail, nil)
	}

	// ---- 2. streaming Messages with the real go_hive client --------------
	upstream.setMode("messages_stream")
	var deltas []string
	streamCompletion, streamErr := provider.ACompleteStream(context.Background(), llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: "ping"}},
		MaxTokens: 32,
	}, func(event llm.StreamEvent) {
		if delta, ok := event.(llm.TextDeltaEvent); ok {
			deltas = append(deltas, delta.Content)
			select {
			case upstream.streamRelease <- struct{}{}:
			default:
			}
		}
	})
	streamDetail := map[string]any{
		"content":       streamCompletion.Content,
		"deltas":        deltas,
		"incremental":   upstream.streamReleased.Load(),
		"stop_reason":   streamCompletion.StopReason,
		"output_tokens": streamCompletion.OutputTokens,
	}
	if streamErr != nil || streamCompletion.Content != "hello" || len(deltas) == 0 {
		add("messages_stream_real_client", false, streamDetail, fmt.Errorf("completion=%+v err=%v", streamCompletion, streamErr))
	} else {
		add("messages_stream_real_client", true, streamDetail, nil)
	}
	if !upstream.streamReleased.Load() {
		add("messages_stream_incremental", false, streamDetail, fmt.Errorf("the fake upstream timed out waiting for the first delta; the proxy buffered SSE"))
	} else {
		add("messages_stream_incremental", true, streamDetail, nil)
	}

	// ---- 3. upstream 429 forwarded verbatim --------------------------------
	upstream.setMode("messages_429")
	status, raw, reqErr := doJSON(context.Background(), client, server.baseURL+"/v1/messages", streamToken, `{"model":"queen","max_tokens":8}`)
	rateDetail := map[string]any{"status": status, "body": string(raw), "retry_after": ""}
	if reqErr == nil {
		rateDetail["retry_after"] = lastRetryAfter
	}
	if reqErr != nil || status != http.StatusTooManyRequests ||
		string(raw) != `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}` || lastRetryAfter != "7" {
		add("messages_upstream_429_forwarded", false, rateDetail, fmt.Errorf("status=%d body=%s err=%v retry-after=%s", status, raw, reqErr, lastRetryAfter))
	} else {
		add("messages_upstream_429_forwarded", true, rateDetail, nil)
	}

	// ---- 4. invalid credential remains 401 --------------------------------
	status, raw, reqErr = doJSON(context.Background(), client, server.baseURL+"/v1/messages", "garbage", `{"model":"queen"}`)
	authDetail := map[string]any{"status": status, "body": string(raw)}
	if reqErr != nil || status != http.StatusUnauthorized || !strings.Contains(string(raw), `"hive_stream_token_invalid"`) {
		add("messages_invalid_token_401", false, authDetail, fmt.Errorf("status=%d body=%s err=%v", status, raw, reqErr))
	} else {
		add("messages_invalid_token_401", true, authDetail, nil)
	}

	// ---- 5. operator JWT is accepted by the same boundary -----------------
	upstream.setMode("messages_ok")
	status, raw, reqErr = doJSON(context.Background(), client, server.baseURL+"/v1/messages", userJWT, `{"model":"queen"}`)
	jwtDetail := map[string]any{"status": status, "body_bytes": len(raw)}
	if reqErr != nil || status != http.StatusOK {
		add("messages_operator_jwt_accepted", false, jwtDetail, fmt.Errorf("status=%d body=%s err=%v", status, raw, reqErr))
	} else {
		add("messages_operator_jwt_accepted", true, jwtDetail, nil)
	}

	// ---- 6. client cancellation reaches the upstream ---------------------
	upstream.setMode("messages_hang")
	ctx, cancel := context.WithCancel(context.Background())
	cancelDone := make(chan struct{})
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.baseURL+"/v1/messages",
			strings.NewReader(`{"model":"queen","stream":true}`))
		req.Header.Set("Authorization", "Bearer "+streamToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
		}
		close(cancelDone)
	}()
	select {
	case <-upstream.hangStarted:
	case <-time.After(2 * time.Second):
		add("messages_cancel_propagates", false, nil, fmt.Errorf("upstream never saw the request"))
		cancel()
	}
	cancel()
	canceled := false
	select {
	case <-upstream.hangCanceled:
		canceled = true
	case <-time.After(2 * time.Second):
	}
	select {
	case <-cancelDone:
	case <-time.After(2 * time.Second):
	}
	if canceled {
		add("messages_cancel_propagates", true, map[string]any{"upstream_saw_cancel": true}, nil)
	} else {
		add("messages_cancel_propagates", false, nil, fmt.Errorf("upstream did not observe cancellation"))
	}

	// ---- 7. unconfigured answers are explicit, never 200 ------------------
	status, raw, reqErr = doJSON(context.Background(), client, unconfigured.baseURL+"/v1/messages", streamToken, `{"model":"queen"}`)
	unconfDetail := map[string]any{"messages_status": status, "messages_body": string(raw)}
	ok := reqErr == nil && status == http.StatusBadRequest && strings.Contains(string(raw), `"llm_upstream_not_configured"`)
	statusImg, rawImg, errImg := doJSON(context.Background(), client, unconfigured.baseURL+"/v1/images/generations", streamToken, `{"prompt":"x"}`)
	unconfDetail["images_status"] = statusImg
	unconfDetail["images_body"] = string(rawImg)
	if !ok || errImg != nil || statusImg != http.StatusServiceUnavailable || !strings.Contains(string(rawImg), `"llm_upstream_not_configured"`) {
		add("unconfigured_answers_explicit", false, unconfDetail, fmt.Errorf("messages=%d/%s images=%d/%s errs=%v/%v", status, raw, statusImg, rawImg, reqErr, errImg))
	} else {
		add("unconfigured_answers_explicit", true, unconfDetail, nil)
	}

	// ---- 8. images pass-through -----------------------------------------
	const imageRequest = `{"model":"gpt-image-2","prompt":"a cat","size":"1024x1024","quality":"low","n":1,"output_format":"png","future_field":{"keep":true}}`
	upstream.setMode("images_ok")
	status, raw, reqErr = doJSON(context.Background(), client, server.baseURL+"/v1/images/generations", streamToken, imageRequest)
	imagesDetail := map[string]any{"status": status, "body": string(raw)}
	if reqErr != nil || status != http.StatusOK {
		add("images_pass_through", false, imagesDetail, fmt.Errorf("status=%d body=%s err=%v", status, raw, reqErr))
	} else {
		imagesDetail["upstream_bearer"] = upstream.snapshot().Header.Get("Authorization") == "Bearer "+imagesKey
		imagesDetail["body_verbatim"] = string(upstream.snapshot().Body) == imageRequest
		if !imagesDetail["upstream_bearer"].(bool) || !imagesDetail["body_verbatim"].(bool) {
			add("images_pass_through", false, imagesDetail, fmt.Errorf("upstream did not receive the configured credential/verbatim body"))
		} else {
			add("images_pass_through", true, imagesDetail, nil)
		}
	}

	upstream.setMode("images_402")
	status, raw, reqErr = doJSON(context.Background(), client, server.baseURL+"/v1/images/generations", streamToken, `{"prompt":"x"}`)
	if reqErr != nil || status != http.StatusPaymentRequired || !strings.Contains(string(raw), `"out of credits"`) {
		add("images_upstream_402_forwarded", false, map[string]any{"status": status, "body": string(raw)}, fmt.Errorf("status=%d body=%s err=%v", status, raw, reqErr))
	} else {
		add("images_upstream_402_forwarded", true, map[string]any{"status": status}, nil)
	}

	out.OK = true
	for _, item := range out.Checks {
		if !item.OK {
			out.OK = false
		}
	}
	out.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(outPath, payload, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("llm-proxy-smoke: %d checks, ok=%v, evidence at %s\n", len(out.Checks), out.OK, outPath)
	if !out.OK {
		os.Exit(1)
	}
}

var lastRetryAfter string

func doJSON(ctx context.Context, client *http.Client, url, token, body string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	lastRetryAfter = resp.Header.Get("retry-after")
	return resp.StatusCode, raw, err
}

func headerContains(header http.Header, secret string) bool {
	for _, values := range header {
		for _, value := range values {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
	return false
}
