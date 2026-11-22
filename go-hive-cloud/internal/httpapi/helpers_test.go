package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/auth"
	"github.com/shark2202/go_hive/go-hive-cloud/internal/httpapi"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata goldens with the current responses")

const (
	testUser   = "u_test"
	testTeam   = "team_test"
	testSecret = "test-jwt-secret-0123456789abcdef0123"
	testStream = "test-stream-secret-0123456789abcdef"
)

type env struct {
	t        *testing.T
	dir      string
	server   *httptest.Server
	handler  http.Handler
	app      *httpapi.Server
	jwt      string
	signer   *auth.Signer
	streamer *auth.Signer
}

func newEnv(t *testing.T, tweak ...func(*httpapi.Config)) *env {
	t.Helper()
	dir := t.TempDir()
	cfg := httpapi.Config{
		DataDir:          dir,
		JWTSecret:        []byte(testSecret),
		StreamSecrets:    [][]byte{[]byte(testStream)},
		StatementTimeout: 5 * time.Second,
		RowCapDefault:    1000,
		StreamTTL:        24 * time.Hour,
		KeepAlive:        100 * time.Millisecond,
		Version:          "test-m1",
		Logger:           log.New(io.Discard, "", 0),
	}
	for _, fn := range tweak {
		fn(&cfg)
	}
	srv, err := httpapi.New(cfg)
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	handler := srv.Handler()
	ts := httptest.NewServer(handler)
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	signer := auth.NewSigner(cfg.JWTSecret, nil)
	jwt, err := signer.MintUserToken(testUser, testTeam, 12*time.Hour)
	if err != nil {
		t.Fatalf("mint jwt: %v", err)
	}
	streamer := auth.NewSigner(cfg.StreamSecrets[0], nil)
	return &env{t: t, dir: dir, server: ts, handler: handler, app: srv, jwt: jwt, signer: signer, streamer: streamer}
}

func (e *env) do(method, path string, body any, scheme, token string) (*http.Response, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		switch raw := body.(type) {
		case string:
			reader = strings.NewReader(raw)
		case []byte:
			reader = bytes.NewReader(raw)
		default:
			payload, err := json.Marshal(body)
			if err != nil {
				e.t.Fatalf("marshal body: %v", err)
			}
			reader = bytes.NewReader(payload)
		}
	}
	req, err := http.NewRequest(method, e.server.URL+path, reader)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if token != "" {
		if scheme == "" {
			scheme = "jwt"
		}
		req.Header.Set("Authorization", scheme+" "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.server.Client().Do(req)
	if err != nil {
		e.t.Fatalf("%s %s: %v", method, path, err)
	}
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		e.t.Fatalf("read body: %v", err)
	}
	return resp, raw
}

// authed issues a request with the standard operator JWT.
func (e *env) authed(method, path string, body any) (*http.Response, []byte) {
	return e.do(method, path, body, "jwt", e.jwt)
}

func (e *env) decodeJSON(resp *http.Response, raw []byte, wantStatus int) map[string]any {
	e.t.Helper()
	if resp.StatusCode != wantStatus {
		e.t.Fatalf("%s %s: status %d want %d (body %s)", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, wantStatus, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		e.t.Fatalf("decode %s: %v (body %s)", resp.Request.URL.Path, err, raw)
	}
	return out
}

// mintStream signs a streamToken for the test team.
func (e *env) mintStream(ttl time.Duration) string {
	e.t.Helper()
	token, err := e.streamer.MintStreamToken(testUser, testTeam, ttl)
	if err != nil {
		e.t.Fatalf("mint stream token: %v", err)
	}
	return token
}

// ---- golden helpers -------------------------------------------------------

// dynamicKeys are replaced with "<dynamic>" before golden comparison.
var dynamicKeys = map[string]bool{
	"id": true, "message_id": true, "reply_id": true,
	"created_at": true, "updated_at": true, "resolved_at": true, "at": true,
	"time": true, "cursor": true, "exp": true, "iat": true,
	"token": true, "streamToken": true,
}

func maskDynamic(value any) any {
	switch node := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for key, item := range node {
			if dynamicKeys[key] {
				out[key] = "<dynamic>"
				continue
			}
			out[key] = maskDynamic(item)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			out[i] = maskDynamic(item)
		}
		return out
	default:
		return value
	}
}

func goldenJSON(t *testing.T, name string, value any) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	got, err := marshalIndentNoEscape(t, maskDynamic(value))
	if err != nil {
		t.Fatalf("marshal golden %s: %v", name, err)
	}
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./internal/httpapi -update-golden)", name, err)
	}
	if string(want) != string(got) {
		t.Fatalf("golden %s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// marshalIndentNoEscape renders JSON with stable indentation and without HTML
// escaping so goldens stay readable.
func marshalIndentNoEscape(t *testing.T, value any) ([]byte, error) {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// marshalCompactNoEscape renders one-line JSON without HTML escaping (the SSE
// wire format).
func marshalCompactNoEscape(t *testing.T, value any) ([]byte, error) {
	t.Helper()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSpace(buffer.String())), nil
}

func goldenText(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run: go test ./internal/httpapi -update-golden)", name, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s mismatch\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// sseReader parses SSE frames from one long-lived response body. A single
// scanner is kept per stream because bufio.Scanner buffers ahead.
type sseReader struct {
	scanner *bufio.Scanner
}

func newSSEReader(body io.Reader) *sseReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &sseReader{scanner: scanner}
}

// nextFrame returns the next event with a data payload, skipping comments
// (keepalives) and empty frames.
func (r *sseReader) nextFrame(t *testing.T) (string, string) {
	t.Helper()
	event := ""
	data := ""
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if data != "" {
				return event, data
			}
			event = ""
			continue
		}
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	t.Fatalf("stream ended before a complete frame: %v", r.scanner.Err())
	return "", ""
}

// openStream opens the inbox SSE stream and waits until the server has written
// its connected comment (which guarantees the subscription is registered).
func (e *env) openStream(t *testing.T, runtimeID, kind string) *http.Response {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := e.server.URL + "/v1/inbox/runtime/stream?runtime_id=" + runtimeID + "&kind=" + kind
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new stream request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.jwt)
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{} // no total timeout: the stream is long-lived by contract
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("stream status %d (body %s)", resp.StatusCode, raw)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("stream content-type = %q", ct)
	}
	return resp
}

var _ = fmt.Sprintf
