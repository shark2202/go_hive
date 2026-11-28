package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

const (
	m5UpstreamKey = "upstream-messages-key-0123456789"
	m5ImagesKey   = "upstream-images-key-0123456789"
)

type capturedRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   []byte
}

// newCaptureUpstream records each request and lets the test choose the
// response. Tests run locally: no external network is contacted.
func newCaptureUpstream(t *testing.T, respond func(w http.ResponseWriter, req capturedRequest)) (*httptest.Server, <-chan capturedRequest) {
	t.Helper()
	captured := make(chan capturedRequest, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("upstream read body: %v", err)
			http.Error(w, "read error", http.StatusInternalServerError)
			return
		}
		req := capturedRequest{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Body: raw}
		captured <- req
		respond(w, req)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// m5Env points the proxy at the fake upstreams and gives the test the real
// httptest server/client. Nil upstream means "left unconfigured".
func m5Env(t *testing.T, messages, images *httptest.Server, tweak ...func(*httpapi.Config)) *env {
	t.Helper()
	return newEnv(t, append([]func(*httpapi.Config){func(cfg *httpapi.Config) {
		if messages != nil {
			cfg.LLMUpstreamURL = messages.URL
			cfg.LLMUpstreamKey = m5UpstreamKey
			cfg.LLMUpstreamAuth = "bearer"
			cfg.LLMRequestTimeout = 5 * time.Second
			cfg.LLMUpstreamClient = messages.Client()
		}
		if images != nil {
			cfg.ImagesUpstreamURL = images.URL + "/v1/images/generations"
			cfg.ImagesUpstreamKey = m5ImagesKey
			cfg.ImagesRequestTimeout = 5 * time.Second
			cfg.ImagesUpstreamClient = images.Client()
		}
	}}, tweak...)...)
}

func (e *env) doWithHeaders(t *testing.T, method, path string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

func decodeProxyError(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode proxy error: %v (%s)", err, raw)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj == nil {
		t.Fatalf("proxy error missing error object: %v", body)
	}
	return errObj
}

// TestLLMMessagesNonStreamPassThrough pins the Messages contract this proxy
// owns: body/header pass-through, explicit upstream credential (both observed
// auth styles), response status/body/headers, and no client-token leakage.
func TestLLMMessagesNonStreamPassThrough(t *testing.T) {
	const clientTokenMarker = "client-stream-token-must-not-leak"
	const responseBody = `{"id":"msg_1","type":"message","role":"assistant","model":"queen","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`
	const requestBody = `{"model":"queen","max_tokens":16,"messages":[{"role":"user","content":"ping"}],"future_top_level":{"keep":true}}`

	for _, style := range []string{"bearer", "x-api-key", "default"} {
		t.Run(style, func(t *testing.T) {
			upstream, captured := newCaptureUpstream(t, func(w http.ResponseWriter, _ capturedRequest) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("request-id", "req_msg_1")
				w.Header().Set("anthropic-ratelimit-remaining", "9")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, responseBody)
			})
			e := m5Env(t, upstream, nil, func(cfg *httpapi.Config) {
				switch style {
				case "bearer":
					cfg.LLMUpstreamAuth = "bearer"
				case "x-api-key":
					cfg.LLMUpstreamAuth = "x-api-key"
				case "default":
					cfg.LLMUpstreamAuth = ""
				}
			})
			token := e.mintStream(time.Hour)
			resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/messages", []byte(requestBody), map[string]string{
				"Authorization":     "Bearer " + token,
				"Content-Type":      "application/json",
				"Anthropic-Version": "2023-06-01",
				"Anthropic-Beta":    "tools-2024-04-04",
				"X-Api-Key":         clientTokenMarker,
				"Cookie":            "session=must-not-forward",
			})
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
			}
			if string(raw) != responseBody {
				t.Fatalf("response body was rewritten:\n got %s\nwant %s", raw, responseBody)
			}
			if got := resp.Header.Get("request-id"); got != "req_msg_1" {
				t.Fatalf("request-id = %q", got)
			}
			if got := resp.Header.Get("anthropic-ratelimit-remaining"); got != "9" {
				t.Fatalf("ratelimit header = %q", got)
			}

			got := <-captured
			if got.Method != http.MethodPost || got.Path != "/v1/messages" {
				t.Fatalf("upstream request = %s %s", got.Method, got.Path)
			}
			if string(got.Body) != requestBody {
				t.Fatalf("request body was rewritten:\n got %s\nwant %s", got.Body, requestBody)
			}
			if ct := got.Header.Get("Content-Type"); ct != "application/json" {
				t.Fatalf("upstream content-type = %q", ct)
			}
			if v := got.Header.Get("Anthropic-Version"); v != "2023-06-01" {
				t.Fatalf("upstream anthropic-version = %q", v)
			}
			if v := got.Header.Get("Anthropic-Beta"); v != "tools-2024-04-04" {
				t.Fatalf("upstream anthropic-beta = %q", v)
			}
			switch style {
			case "bearer":
				if v := got.Header.Get("Authorization"); v != "Bearer "+m5UpstreamKey {
					t.Fatalf("upstream authorization = %q", v)
				}
				if v := got.Header.Get("X-Api-Key"); v != "" {
					t.Fatalf("upstream x-api-key = %q, want empty", v)
				}
			case "x-api-key", "default":
				if v := got.Header.Get("X-Api-Key"); v != m5UpstreamKey {
					t.Fatalf("upstream x-api-key = %q", v)
				}
				if v := got.Header.Get("Authorization"); v != "" {
					t.Fatalf("upstream authorization = %q, want empty", v)
				}
			}
			assertNoHeaderLeak(t, got.Header, token, clientTokenMarker)
		})
	}
}

func assertNoHeaderLeak(t *testing.T, header http.Header, secrets ...string) {
	t.Helper()
	for key, values := range header {
		for _, value := range values {
			for _, secret := range secrets {
				if strings.Contains(value, secret) {
					t.Fatalf("upstream header %s leaked client credential", key)
				}
			}
		}
	}
}

// TestLLMMessagesSSEPassThrough proves the proxy relays SSE incrementally: the
// first event must reach the client before the upstream is allowed to write the
// rest, and the byte stream must be unchanged.
func TestLLMMessagesSSEPassThrough(t *testing.T) {
	first := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"queen\",\"stop_reason\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n"
	rest := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	release := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})
	bodyCh := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodyCh <- raw
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req_stream_1")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, first)
		flusher.Flush()
		<-release
		_, _ = io.WriteString(w, rest)
		flusher.Flush()
	}))
	t.Cleanup(upstream.Close)

	e := m5Env(t, upstream, nil)
	token := e.mintStream(time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.server.URL+"/v1/messages",
		strings.NewReader(`{"model":"queen","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := e.server.Client().Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	firstLine := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		firstLine <- line
	}()
	select {
	case line := <-firstLine:
		if line != "event: message_start\n" {
			t.Fatalf("first SSE line = %q", line)
		}
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("proxy buffered the first SSE event; per-event flush is broken")
	}
	release <- struct{}{}
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stream tail: %v", err)
	}
	wantTail := strings.TrimPrefix(first, "event: message_start\n") + rest
	if string(tail) != wantTail {
		t.Fatalf("SSE bytes were rewritten:\n got %q\nwant %q", tail, wantTail)
	}
	if got := <-bodyCh; string(got) != `{"model":"queen","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"ping"}]}` {
		t.Fatalf("streaming request body was rewritten: %s", got)
	}
	if got := resp.Header.Get("request-id"); got != "req_stream_1" {
		t.Fatalf("stream request-id = %q", got)
	}
}

// TestLLMMessagesUpstreamErrorsForwarded pins 4xx/5xx pass-through: the proxy
// must not rewrite an Anthropic error into a local transport error.
func TestLLMMessagesUpstreamErrorsForwarded(t *testing.T) {
	const errorBody = `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`
	for _, status := range []int{400, 401, 403, 429, 500, 529} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream, _ := newCaptureUpstream(t, func(w http.ResponseWriter, _ capturedRequest) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("retry-after", "7")
				w.Header().Set("request-id", "req_err_1")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, errorBody)
			})
			e := m5Env(t, upstream, nil)
			token := e.mintStream(time.Hour)
			resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/messages", []byte(`{"model":"queen"}`), map[string]string{
				"Authorization": "Bearer " + token,
				"Content-Type":  "application/json",
			})
			if resp.StatusCode != status {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, raw)
			}
			if string(raw) != errorBody {
				t.Fatalf("error body was rewritten: %s", raw)
			}
			if got := resp.Header.Get("retry-after"); got != "7" {
				t.Fatalf("retry-after = %q", got)
			}
			if got := resp.Header.Get("request-id"); got != "req_err_1" {
				t.Fatalf("request-id = %q", got)
			}
		})
	}
}

func TestLLMMessagesUpstreamTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(upstream.Close)
	e := m5Env(t, upstream, nil, func(cfg *httpapi.Config) {
		cfg.LLMRequestTimeout = 100 * time.Millisecond
	})
	token := e.mintStream(time.Hour)
	resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/messages", []byte(`{"model":"queen"}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body %s)", resp.StatusCode, raw)
	}
	errObj := decodeProxyError(t, raw)
	if errObj["type"] != httpapi.LLMUpstreamTimeoutType {
		t.Fatalf("error.type = %v", errObj["type"])
	}
}

func TestLLMMessagesUpstreamUnavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	closedURL := upstream.URL
	upstream.Close()

	e := newEnv(t, func(cfg *httpapi.Config) {
		cfg.LLMUpstreamURL = closedURL
		cfg.LLMUpstreamKey = m5UpstreamKey
		cfg.LLMUpstreamAuth = "bearer"
		cfg.LLMRequestTimeout = 2 * time.Second
	})
	token := e.mintStream(time.Hour)
	resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/messages", []byte(`{"model":"queen"}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (body %s)", resp.StatusCode, raw)
	}
	errObj := decodeProxyError(t, raw)
	if errObj["type"] != httpapi.LLMUpstreamUnavailableType {
		t.Fatalf("error.type = %v", errObj["type"])
	}
}

// TestLLMMessagesClientCancellation pins cancellation propagation: when the
// caller goes away, the upstream request context must be canceled too.
func TestLLMMessagesClientCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(upstream.Close)
	e := m5Env(t, upstream, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.server.URL+"/v1/messages",
		strings.NewReader(`{"model":"queen","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.mintStream(time.Hour))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	done := make(chan struct{})
	go func() {
		resp, err := e.server.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream never saw the request")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not observe client cancellation")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("proxy request did not return after cancellation")
	}
}

// TestLLMUnconfiguredAndAuthBoundary covers the credential boundary for both
// endpoints and the honest "not configured" answers (never an empty 200).
func TestLLMUnconfiguredAndAuthBoundary(t *testing.T) {
	e := newEnv(t) // no upstream at all
	token := e.mintStream(time.Hour)

	for name, endpoint := range map[string]string{
		"messages": "/v1/messages",
		"images":   "/v1/images/generations",
	} {
		t.Run(name+" invalid token", func(t *testing.T) {
			resp, raw := e.doWithHeaders(t, http.MethodPost, endpoint, []byte(`{"model":"queen"}`), map[string]string{
				"Authorization": "Bearer garbage",
				"Content-Type":  "application/json",
			})
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body %s)", resp.StatusCode, raw)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			errObj, _ := body["error"].(map[string]any)
			if errObj["type"] != httpapi.LLMTokenInvalidType {
				t.Fatalf("error.type = %v", errObj["type"])
			}
		})
	}

	resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/messages", []byte(`{"model":"queen"}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("messages unconfigured status = %d, want 400 (body %s)", resp.StatusCode, raw)
	}
	if errObj := decodeProxyError(t, raw); errObj["type"] != httpapi.LLMUpstreamNotConfiguredType {
		t.Fatalf("messages error.type = %v", errObj["type"])
	}

	resp, raw = e.doWithHeaders(t, http.MethodPost, "/v1/images/generations", []byte(`{"prompt":"x"}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("images unconfigured status = %d, want 503 (body %s)", resp.StatusCode, raw)
	}
	if errObj := decodeProxyError(t, raw); errObj["type"] != httpapi.LLMUpstreamNotConfiguredType {
		t.Fatalf("images error.type = %v", errObj["type"])
	}

	// Registered POST endpoints must answer 405 for other methods, never 404
	// (which would make them look unregistered) and never a 2xx.
	for _, endpoint := range []string{"/v1/messages", "/v1/images/generations"} {
		resp, _ := e.doWithHeaders(t, http.MethodGet, endpoint, nil, nil)
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s = %d, want 405", endpoint, resp.StatusCode)
		}
	}
	resp, _ = e.doWithHeaders(t, http.MethodPost, "/v1/complete", []byte(`{}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unregistered /v1/complete = %d, want 404", resp.StatusCode)
	}
}

// TestLLMImagesPassThrough covers the observed image client contract: JSON
// body forwarded verbatim, Bearer upstream credential, status/body relay.
func TestLLMImagesPassThrough(t *testing.T) {
	const responseBody = `{"data":[{"b64_json":"aGVsbG8="}],"usage":{"total_tokens":7}}`
	const requestBody = `{"model":"gpt-image-2","prompt":"a cat","size":"1024x1024","quality":"low","n":1,"output_format":"png","image":["cmVm"],"future_field":{"keep":true}}`

	upstream, captured := newCaptureUpstream(t, func(w http.ResponseWriter, _ capturedRequest) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("openai-processing-ms", "12")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, responseBody)
	})
	e := m5Env(t, nil, upstream)
	token := e.mintStream(time.Hour)
	resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/images/generations", []byte(requestBody), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, raw)
	}
	if string(raw) != responseBody {
		t.Fatalf("image response was rewritten: %s", raw)
	}
	if got := resp.Header.Get("openai-processing-ms"); got != "12" {
		t.Fatalf("processing header = %q", got)
	}
	got := <-captured
	if got.Method != http.MethodPost || got.Path != "/v1/images/generations" {
		t.Fatalf("upstream request = %s %s", got.Method, got.Path)
	}
	if string(got.Body) != requestBody {
		t.Fatalf("image body was rewritten:\n got %s\nwant %s", got.Body, requestBody)
	}
	if v := got.Header.Get("Authorization"); v != "Bearer "+m5ImagesKey {
		t.Fatalf("upstream authorization = %q", v)
	}
	assertNoHeaderLeak(t, got.Header, token)
}

func TestLLMImagesUpstreamTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	t.Cleanup(upstream.Close)
	e := m5Env(t, nil, upstream, func(cfg *httpapi.Config) {
		cfg.ImagesRequestTimeout = 100 * time.Millisecond
	})
	token := e.mintStream(time.Hour)
	resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/images/generations", []byte(`{"prompt":"x"}`), map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	})
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504 (body %s)", resp.StatusCode, raw)
	}
	if errObj := decodeProxyError(t, raw); errObj["type"] != httpapi.LLMUpstreamTimeoutType {
		t.Fatalf("error.type = %v", errObj["type"])
	}
}

func TestLLMImagesUpstreamErrorsForwarded(t *testing.T) {
	const errorBody = `{"error":{"message":"out of credits","type":"insufficient_quota"}}`
	for _, status := range []int{400, 402, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			upstream, _ := newCaptureUpstream(t, func(w http.ResponseWriter, _ capturedRequest) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, errorBody)
			})
			e := m5Env(t, nil, upstream)
			token := e.mintStream(time.Hour)
			resp, raw := e.doWithHeaders(t, http.MethodPost, "/v1/images/generations", []byte(`{"prompt":"x"}`), map[string]string{
				"Authorization": "Bearer " + token,
				"Content-Type":  "application/json",
			})
			if resp.StatusCode != status {
				t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, raw)
			}
			if string(raw) != errorBody {
				t.Fatalf("error body was rewritten: %s", raw)
			}
		})
	}
}

func TestLLMProxyConfigValidation(t *testing.T) {
	base := httpapi.Config{DataDir: t.TempDir(), JWTSecret: []byte("test-jwt-secret-0123456789abcdef0123")}
	cases := []struct {
		name  string
		tweak func(*httpapi.Config)
	}{
		{"bad messages scheme", func(cfg *httpapi.Config) { cfg.LLMUpstreamURL = "ftp://example.invalid" }},
		{"messages url without host", func(cfg *httpapi.Config) { cfg.LLMUpstreamURL = "http:///v1/messages" }},
		{"bad images scheme", func(cfg *httpapi.Config) { cfg.ImagesUpstreamURL = "file:///tmp/images" }},
		{"bad auth style", func(cfg *httpapi.Config) { cfg.LLMUpstreamAuth = "basic" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.tweak(&cfg)
			srv, err := httpapi.New(cfg)
			if err == nil {
				_ = srv.Close()
				t.Fatal("expected configuration error")
			}
		})
	}
}
