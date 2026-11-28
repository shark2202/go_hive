package nativehttp

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.AllowPrivateHosts = true
	cfg.Cookies.Enabled = true
	cfg.Cookies.HostOnly = true
	cfg.MaxInlineBytes = 64 << 10
	cfg.MaxResponseBytes = 16 << 20
	cfg.MaxDownloadBytes = 1 << 20
	cfg.MaxUploadBytes = 1 << 20
	cfg.DefaultTimeout = 5 * time.Second
	cfg.MaxTimeout = 30 * time.Second
	cfg.ConnectTimeout = 5 * time.Second
	return cfg
}

func newTestClient(t *testing.T, cfg Config) (*Client, string) {
	t.Helper()
	root := t.TempDir()
	client := New(cfg, root, root)
	t.Cleanup(func() { _ = client.Close() })
	return client, root
}

func baseRequest(rawURL string) Request {
	return Request{
		URL:             rawURL,
		Method:          http.MethodGet,
		FollowRedirects: true,
		MaxRedirects:    10,
		NoProxy:         true,
		Compressed:      true,
		Timeout:         5 * time.Second,
		Response: ResponseOptions{
			Mode:           "auto",
			MaxInlineBytes: 64 << 10,
			MaxBytes:       16 << 20,
			IncludeHeaders: true,
			IncludeBody:    true,
		},
	}
}

// Parity-exempt: Go-only native HTTP transport; method/body encoding matrix.
func TestRequestMethodBodyMatrix(t *testing.T) {
	var lastMethod, lastContentType, lastBody, lastQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastMethod = r.Method
		lastContentType = r.Header.Get("Content-Type")
		lastBody = string(body)
		lastQuery = r.URL.RawQuery
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client, _ := newTestClient(t, testConfig())

	cases := []struct {
		name        string
		mutate      func(*Request)
		method      string
		contentType string
		body        string
		query       string
	}{
		{"get", func(r *Request) { r.Query = url.Values{"a": {"1", "2"}} }, "GET", "", "", "a=1&a=2"},
		{"post json", func(r *Request) { r.Method = "POST"; r.JSON = map[string]any{"a": 1} }, "POST", "application/json", `{"a":1}`, ""},
		{"put form", func(r *Request) { r.Method = "PUT"; r.Form = url.Values{"x": {"y"}} }, "PUT", "application/x-www-form-urlencoded", "x=y", ""},
		{"patch raw", func(r *Request) { r.Method = "PATCH"; r.Body = []byte("raw") }, "PATCH", "", "raw", ""},
		{"delete", func(r *Request) { r.Method = "DELETE" }, "DELETE", "", "", ""},
		{"head", func(r *Request) { r.Method = "HEAD" }, "HEAD", "", "", ""},
		{"options", func(r *Request) { r.Method = "OPTIONS" }, "OPTIONS", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baseRequest(server.URL)
			tc.mutate(&req)
			result := client.Do(context.Background(), req)
			if result.Error != "" {
				t.Fatalf("Do: %s", result.Error)
			}
			if lastMethod != tc.method {
				t.Fatalf("method = %q, want %q", lastMethod, tc.method)
			}
			if tc.contentType != "" && !strings.HasPrefix(lastContentType, tc.contentType) {
				t.Fatalf("content-type = %q, want %q", lastContentType, tc.contentType)
			}
			if lastBody != tc.body {
				t.Fatalf("body = %q, want %q", lastBody, tc.body)
			}
			if lastQuery != tc.query {
				t.Fatalf("query = %q, want %q", lastQuery, tc.query)
			}
		})
	}

	conflict := baseRequest(server.URL)
	conflict.Body = []byte("x")
	conflict.JSON = map[string]any{"y": 1}
	if result := client.Do(context.Background(), conflict); result.ErrorKind != ErrInvalidInput {
		t.Fatalf("conflicting body kind = %q, want %q", result.ErrorKind, ErrInvalidInput)
	}
}

// Parity-exempt: Go-only native HTTP transport; auth and header assembly.
func TestAuthAndHeaders(t *testing.T) {
	var authorization, userAgent, referer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		userAgent = r.Header.Get("User-Agent")
		referer = r.Header.Get("Referer")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client, _ := newTestClient(t, testConfig())

	req := baseRequest(server.URL)
	req.Auth = &Auth{Type: "basic", Username: "u", Password: "p"}
	req.Headers = http.Header{"Referer": {"https://example.com/"}}
	if result := client.Do(context.Background(), req); result.Error != "" {
		t.Fatal(result.Error)
	}
	if !strings.HasPrefix(authorization, "Basic ") {
		t.Fatalf("authorization = %q", authorization)
	}
	if userAgent != DefaultConfig().UserAgent {
		t.Fatalf("user-agent = %q", userAgent)
	}
	if referer != "https://example.com/" {
		t.Fatalf("referer = %q", referer)
	}

	req = baseRequest(server.URL)
	req.Auth = &Auth{Type: "bearer", Token: "t"}
	if result := client.Do(context.Background(), req); result.Error != "" {
		t.Fatal(result.Error)
	}
	if authorization != "Bearer t" {
		t.Fatalf("authorization = %q", authorization)
	}
}

// Parity-exempt: Go-only native HTTP transport; redirect policy.
func TestRedirectSemantics(t *testing.T) {
	var finalMethod string
	mux := http.NewServeMux()
	mux.HandleFunc("/start302", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/start307", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		finalMethod = r.Method
		_, _ = w.Write([]byte("ok"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, _ := newTestClient(t, testConfig())

	req := baseRequest(server.URL + "/start302")
	req.Method = "POST"
	result := client.Do(context.Background(), req)
	if result.Error != "" || finalMethod != "GET" {
		t.Fatalf("302: err=%q method=%q", result.Error, finalMethod)
	}
	if len(result.Redirects) != 1 || result.Redirects[0].Status != http.StatusFound {
		t.Fatalf("redirects = %+v", result.Redirects)
	}

	req = baseRequest(server.URL + "/start307")
	req.Method = "POST"
	result = client.Do(context.Background(), req)
	if result.Error != "" || finalMethod != "POST" {
		t.Fatalf("307: err=%q method=%q", result.Error, finalMethod)
	}

	req = baseRequest(server.URL + "/start302")
	req.FollowRedirects = false
	result = client.Do(context.Background(), req)
	if result.Status != http.StatusFound || result.OK {
		t.Fatalf("follow=false status = %d ok=%v", result.Status, result.OK)
	}
}

// Parity-exempt: Go-only transport; cross-host redirect credential stripping.
func TestRedirectStripsCrossHostAuth(t *testing.T) {
	var secondAuthorization, secondCookie string
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondAuthorization = r.Header.Get("Authorization")
		secondCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte("ok"))
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/final", http.StatusFound)
	}))
	defer first.Close()
	client, _ := newTestClient(t, testConfig())

	req := baseRequest(first.URL)
	req.Auth = &Auth{Type: "bearer", Token: "secret"}
	req.Headers = http.Header{"Cookie": {"session=1"}}
	if result := client.Do(context.Background(), req); result.Error != "" {
		t.Fatal(result.Error)
	}
	if secondAuthorization != "" || secondCookie != "" {
		t.Fatalf("cross-host leak: authorization=%q cookie=%q", secondAuthorization, secondCookie)
	}
}

// Parity-exempt: Go-only transport; HTTPS-to-HTTP downgrade refusal.
func TestHTTPSDowngradeRefused(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer httpServer.Close()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL, http.StatusFound)
	}))
	defer tlsServer.Close()
	client, _ := newTestClient(t, testConfig())

	req := baseRequest(tlsServer.URL)
	req.Insecure = true
	result := client.Do(context.Background(), req)
	if result.ErrorKind != ErrHTTPSDowngrade {
		t.Fatalf("kind = %q, want %q", result.ErrorKind, ErrHTTPSDowngrade)
	}
}

// Parity-exempt: Go-only transport; SSRF guard wired through netguard.
func TestSSRFBlocked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("request reached an internal host")
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.AllowPrivateHosts = false
	client, _ := newTestClient(t, cfg)

	result := client.Do(context.Background(), baseRequest(server.URL))
	if result.ErrorKind != ErrSSRFBlocked {
		t.Fatalf("kind = %q, want %q (%s)", result.ErrorKind, ErrSSRFBlocked, result.Error)
	}
}

// Parity-exempt: Go-only transport; response modes and inline budget.
func TestResponseModes(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 1000)
	binaryPayload := []byte{0x00, 0x01, 0x02, 0x03}
	gzipPayload := func() []byte {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		_, _ = writer.Write([]byte("compressed"))
		_ = writer.Close()
		return buffer.Bytes()
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(binaryPayload)
	})
	mux.HandleFunc("/gzip", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(gzipPayload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client, root := newTestClient(t, testConfig())

	auto := baseRequest(server.URL + "/text")
	auto.Response.MaxInlineBytes = 100
	result := client.Do(context.Background(), auto)
	if result.Error != "" || result.SavedTo == nil || result.BodyEncoding != "file" {
		t.Fatalf("auto: %+v", result)
	}
	saved, err := os.ReadFile(*result.SavedTo)
	if err != nil || !bytes.Equal(saved, payload) {
		t.Fatalf("saved body: %v", err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(*result.SavedTo, realRoot) {
		t.Fatalf("spill path %q escaped root %q", *result.SavedTo, realRoot)
	}

	inline := baseRequest(server.URL + "/text")
	inline.Response.Mode = "inline"
	inline.Response.MaxInlineBytes = 100
	result = client.Do(context.Background(), inline)
	if result.Error != "" || !result.Truncated || len(result.Body) != 100 {
		t.Fatalf("inline: %+v", result)
	}

	explicit := baseRequest(server.URL + "/text")
	explicit.Response.Mode = "file"
	explicit.Response.Path = filepath.Join(root, "out", "body.txt")
	result = client.Do(context.Background(), explicit)
	if result.Error != "" || result.SavedTo == nil || !strings.HasSuffix(*result.SavedTo, filepath.Join("out", "body.txt")) {
		t.Fatalf("file: %+v", result)
	}

	binary := baseRequest(server.URL + "/binary")
	result = client.Do(context.Background(), binary)
	if result.Error != "" || result.BodyEncoding != "base64" {
		t.Fatalf("binary: %+v", result)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Body)
	if err != nil || !bytes.Equal(decoded, binaryPayload) {
		t.Fatalf("base64 body: %v", err)
	}

	compressed := baseRequest(server.URL + "/gzip")
	result = client.Do(context.Background(), compressed)
	if result.Error != "" || result.Body != "compressed" {
		t.Fatalf("gzip: %+v", result)
	}

	tooLarge := baseRequest(server.URL + "/text")
	tooLarge.Response.MaxBytes = 10
	result = client.Do(context.Background(), tooLarge)
	if result.ErrorKind != ErrResponseTooLarge {
		t.Fatalf("too large kind = %q", result.ErrorKind)
	}
}

// Parity-exempt: Go-only transport; retry and timeout policy.
func TestRetryAndTimeout(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) <= 2 {
			http.Error(w, "retry", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	client, _ := newTestClient(t, testConfig())

	req := baseRequest(server.URL)
	req.Retry = RetryPolicy{Count: 2, Delay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}
	result := client.Do(context.Background(), req)
	if result.Error != "" || result.Status != http.StatusOK || result.Retries != 2 {
		t.Fatalf("retry: %+v", result)
	}

	attempts.Store(0)
	post := baseRequest(server.URL)
	post.Method = "POST"
	post.Retry = RetryPolicy{Count: 2, Delay: 10 * time.Millisecond}
	result = client.Do(context.Background(), post)
	if result.Retries != 0 || result.Status != http.StatusInternalServerError {
		t.Fatalf("post retry: %+v", result)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer slow.Close()
	timeout := baseRequest(slow.URL)
	timeout.Timeout = 50 * time.Millisecond
	result = client.Do(context.Background(), timeout)
	if result.ErrorKind != ErrTimeout {
		t.Fatalf("timeout kind = %q (%s)", result.ErrorKind, result.Error)
	}
}

// Parity-exempt: Go-only transport; sensitive header redaction.
func TestRedaction(t *testing.T) {
	redactedURL := RedactURL("https://user:pass@example.com/x?token=abc&other=1", []string{"token"})
	if strings.Contains(redactedURL, "user:pass") || strings.Contains(redactedURL, "abc") {
		t.Fatalf("redacted url = %q", redactedURL)
	}
	if !strings.Contains(redactedURL, "other=1") {
		t.Fatalf("redacted url lost other: %q", redactedURL)
	}
	headers := RedactHeaders(http.Header{
		"Set-Cookie":   {"sid=secret"},
		"Content-Type": {"text/plain"},
	}, false)
	if headers["set-cookie"][0] != redacted {
		t.Fatalf("set-cookie = %v", headers["set-cookie"])
	}
	if headers["content-type"][0] != "text/plain" {
		t.Fatalf("content-type = %v", headers["content-type"])
	}
}

// Parity-exempt: Go-only HTTP tools config loader.
func TestConfigLoad(t *testing.T) {
	home := t.TempDir()
	cfg := LoadConfig(home)
	if !cfg.Enabled || cfg.LoadError != nil {
		t.Fatalf("missing config = %+v", cfg)
	}
	path := filepath.Join(home, "http_tools.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(home)
	if cfg.Enabled || cfg.LoadError == nil {
		t.Fatalf("malformed config = %+v", cfg)
	}
	if err := os.WriteFile(path, []byte(`{"enabled":true,"max_inline_bytes":12,"cookies":{"host_only":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = LoadConfig(home)
	if !cfg.Enabled || cfg.MaxInlineBytes != 12 {
		t.Fatalf("config = %+v", cfg)
	}
	t.Setenv("HIVE_HTTP_TOOLS", "0")
	if cfg = LoadConfig(home); cfg.Enabled {
		t.Fatal("HIVE_HTTP_TOOLS=0 did not disable the family")
	}
	t.Setenv("HIVE_HTTP_TOOLS", "1")
	if err := os.WriteFile(path, []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if cfg = LoadConfig(home); cfg.Enabled {
		t.Fatal("HIVE_HTTP_TOOLS=1 overrode enabled=false")
	}
}

// Parity-exempt: Go-only transport; artifact digest bookkeeping.
func TestResultSHA256(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()
	client, _ := newTestClient(t, testConfig())
	result := client.Do(context.Background(), baseRequest(server.URL))
	sum := sha256.Sum256([]byte("hello"))
	if result.BodySHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %q", result.BodySHA256)
	}
	if result.TimingMS.TotalMS < 0 {
		t.Fatalf("timing = %+v", result.TimingMS)
	}
}
