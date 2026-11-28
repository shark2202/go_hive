package hiveconf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

var tokenTestNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func tokenTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func claudeCredentialsPath(home string) string {
	return filepath.Join(home, ".claude", ".credentials.json")
}

func codexAuthPath(home string) string {
	return filepath.Join(home, ".codex", "auth.json")
}

func writeTokenDoc(t *testing.T, path string, doc map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir token dir: %v", err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("encode token doc: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write token doc: %v", err)
	}
}

func readTokenDoc(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read token doc: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode token doc: %v", err)
	}
	return doc
}

// tokenEndpointStub is a fake OAuth token endpoint. respond receives the
// zero-based attempt index and the parsed form; every request is recorded.
type tokenEndpointStub struct {
	server       *httptest.Server
	mu           sync.Mutex
	forms        []url.Values
	methods      []string
	contentTypes []string
}

func newTokenEndpointStub(t *testing.T, respond func(attempt int, form url.Values) (int, string)) *tokenEndpointStub {
	t.Helper()
	stub := &tokenEndpointStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
			http.Error(writer, "bad form", http.StatusBadRequest)
			return
		}
		stub.mu.Lock()
		attempt := len(stub.forms)
		stub.forms = append(stub.forms, request.PostForm)
		stub.methods = append(stub.methods, request.Method)
		stub.contentTypes = append(stub.contentTypes, request.Header.Get("Content-Type"))
		stub.mu.Unlock()

		status, body := respond(attempt, request.PostForm)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *tokenEndpointStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forms)
}

// installTokenTestHooks points the package seams at the fake endpoint and a
// fixed clock. The returned counter records retry sleeps (which never actually
// block in tests).
func installTokenTestHooks(t *testing.T, claudeURL, codexURL string, now time.Time) *int {
	t.Helper()
	previousClaude := claudeOAuthEndpoint
	previousCodex := codexOAuthEndpoint
	previousClient := subscriptionHTTPClient
	previousNow := subscriptionNow
	previousSleep := subscriptionSleep

	claudeOAuthEndpoint = claudeURL
	codexOAuthEndpoint = codexURL
	subscriptionHTTPClient = &http.Client{Timeout: 5 * time.Second}
	subscriptionNow = func() time.Time { return now }
	sleeps := 0
	subscriptionSleep = func(ctx context.Context, _ time.Duration) error {
		sleeps++
		return ctx.Err()
	}
	resetSubscriptionRefreshFlights()
	t.Cleanup(func() {
		claudeOAuthEndpoint = previousClaude
		codexOAuthEndpoint = previousCodex
		subscriptionHTTPClient = previousClient
		subscriptionNow = previousNow
		subscriptionSleep = previousSleep
		resetSubscriptionRefreshFlights()
	})
	return &sleeps
}

// Parity: core/framework/loader/agent_loader.py::get_claude_code_token — a
// token more than the 5-minute refresh buffer from expiry is returned
// untouched; an expired or nearly-expired token is refreshed first.
func TestClaudeCodeTokenExpiryDetection(t *testing.T) {
	cases := []struct {
		name     string
		oauth    map[string]any
		want     string
		requests int
	}{
		{
			name: "valid beyond the refresh buffer",
			oauth: map[string]any{
				"accessToken": "old-access", "refreshToken": "refresh-1",
				"expiresAt": tokenTestNow.Add(10 * time.Minute).UnixMilli(),
			},
			want: "old-access", requests: 0,
		},
		{
			name: "inside the refresh buffer",
			oauth: map[string]any{
				"accessToken": "old-access", "refreshToken": "refresh-1",
				"expiresAt": tokenTestNow.Add(2 * time.Minute).UnixMilli(),
			},
			want: "new-access", requests: 1,
		},
		{
			name: "expired without a refresh token",
			oauth: map[string]any{
				"accessToken": "stale-access",
				"expiresAt":   tokenTestNow.Add(-time.Minute).UnixMilli(),
			},
			want: "stale-access", requests: 0,
		},
		{
			name:     "missing expiresAt reads as expired",
			oauth:    map[string]any{"accessToken": "old-access", "refreshToken": "refresh-1"},
			want:     "new-access",
			requests: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := tokenTestHome(t)
			writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": testCase.oauth})
			stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
				return http.StatusOK, `{"access_token":"new-access"}`
			})
			installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

			if got := ClaudeCodeToken(); got != testCase.want {
				t.Fatalf("ClaudeCodeToken = %q, want %q", got, testCase.want)
			}
			if got := stub.requestCount(); got != testCase.requests {
				t.Fatalf("token requests = %d, want %d", got, testCase.requests)
			}
		})
	}
}

// Parity: core/framework/loader/agent_loader.py::_save_refreshed_credentials —
// the rotated access/refresh tokens and expiresAt are written back, unrelated
// fields survive, and the next call reuses the persisted token.
func TestClaudeCodeTokenRefreshPersistsRotatedCredentials(t *testing.T) {
	home := tokenTestHome(t)
	path := claudeCredentialsPath(home)
	writeTokenDoc(t, path, map[string]any{"claudeAiOauth": map[string]any{
		"accessToken":      "old-access",
		"refreshToken":     "refresh-1",
		"expiresAt":        tokenTestNow.Add(-time.Minute).UnixMilli(),
		"subscriptionType": "max",
	}})
	stub := newTokenEndpointStub(t, func(_ int, form url.Values) (int, string) {
		if form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q, want refresh_token", form.Get("grant_type"))
		}
		if form.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh_token = %q, want refresh-1", form.Get("refresh_token"))
		}
		if form.Get("client_id") != claudeOAuthClientID {
			t.Errorf("client_id = %q, want %q", form.Get("client_id"), claudeOAuthClientID)
		}
		return http.StatusOK, `{"access_token":"new-access","refresh_token":"refresh-2","expires_in":3600,"token_type":"Bearer"}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	if got := ClaudeCodeToken(); got != "new-access" {
		t.Fatalf("ClaudeCodeToken = %q, want new-access", got)
	}
	doc := readTokenDoc(t, path)
	oauth, _ := doc["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "new-access" {
		t.Fatalf("accessToken = %v, want new-access", oauth["accessToken"])
	}
	if oauth["refreshToken"] != "refresh-2" {
		t.Fatalf("refreshToken = %v, want refresh-2", oauth["refreshToken"])
	}
	if got, want := int64(oauth["expiresAt"].(float64)), tokenTestNow.Add(time.Hour).UnixMilli(); got != want {
		t.Fatalf("expiresAt = %d, want %d", got, want)
	}
	if oauth["subscriptionType"] != "max" {
		t.Fatalf("unrelated field lost: %#v", oauth)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credentials mode = %o, want 600", perm)
	}

	if got := ClaudeCodeToken(); got != "new-access" {
		t.Fatalf("second ClaudeCodeToken = %q, want new-access", got)
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("token requests = %d, want one grant", got)
	}
	if stub.methods[0] != http.MethodPost {
		t.Fatalf("token method = %s, want POST", stub.methods[0])
	}
	if !strings.HasPrefix(stub.contentTypes[0], "application/x-www-form-urlencoded") {
		t.Fatalf("content type = %q, want form-urlencoded", stub.contentTypes[0])
	}
}

// Parity: core/framework/loader/agent_loader.py::_refresh_claude_code_token —
// transient failures (5xx/429) are retried with bounded backoff.
func TestClaudeCodeTokenRefreshRetriesTransientFailures(t *testing.T) {
	home := tokenTestHome(t)
	writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "refresh-1",
		"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
	}})
	stub := newTokenEndpointStub(t, func(attempt int, _ url.Values) (int, string) {
		switch attempt {
		case 0:
			return http.StatusBadGateway, `{"error":"server_error"}`
		case 1:
			return http.StatusTooManyRequests, `{"error":"rate_limited"}`
		default:
			return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
		}
	})
	sleeps := installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	if got := ClaudeCodeToken(); got != "new-access" {
		t.Fatalf("ClaudeCodeToken = %q, want new-access after retries", got)
	}
	if got := stub.requestCount(); got != 3 {
		t.Fatalf("token requests = %d, want 3", got)
	}
	if *sleeps != 2 {
		t.Fatalf("retry sleeps = %d, want 2", *sleeps)
	}
}

// Parity: core/framework/loader/agent_loader.py::get_claude_code_token — when
// every transient attempt fails, the stale on-disk token is returned after the
// bounded retry count, never an empty credential.
func TestClaudeCodeTokenRefreshStopsAfterBoundedRetries(t *testing.T) {
	home := tokenTestHome(t)
	writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "refresh-1",
		"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
	}})
	stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
		return http.StatusServiceUnavailable, `{"error":"temporarily_unavailable"}`
	})
	sleeps := installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	if got := ClaudeCodeToken(); got != "old-access" {
		t.Fatalf("ClaudeCodeToken = %q, want the stale fallback", got)
	}
	if got := stub.requestCount(); got != tokenRefreshAttempts {
		t.Fatalf("token requests = %d, want %d", got, tokenRefreshAttempts)
	}
	if *sleeps != tokenRefreshAttempts-1 {
		t.Fatalf("retry sleeps = %d, want %d", *sleeps, tokenRefreshAttempts-1)
	}
}

// Parity: core/framework/loader/agent_loader.py::get_claude_code_token — a
// permanent failure returns the stale token without retrying and without
// rewriting the credential file.
func TestClaudeCodeTokenRefreshPermanentFailureFallsBack(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"invalid grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"revoked"}`},
		{"unauthorized", http.StatusUnauthorized, `{"error":"invalid_token"}`},
		{"forbidden", http.StatusForbidden, `{"error":"access_denied"}`},
		{"success status without access token", http.StatusOK, `{}`},
		{"malformed success body", http.StatusOK, `not-json`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := tokenTestHome(t)
			path := claudeCredentialsPath(home)
			writeTokenDoc(t, path, map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "old-access", "refreshToken": "refresh-1",
				"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
			}})
			stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
				return testCase.status, testCase.body
			})
			sleeps := installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

			if got := ClaudeCodeToken(); got != "old-access" {
				t.Fatalf("ClaudeCodeToken = %q, want the stale fallback", got)
			}
			if got := stub.requestCount(); got != 1 {
				t.Fatalf("token requests = %d, want 1 (no retry)", got)
			}
			if *sleeps != 0 {
				t.Fatalf("retry sleeps = %d, want 0", *sleeps)
			}
			oauth, _ := readTokenDoc(t, path)["claudeAiOauth"].(map[string]any)
			if oauth["accessToken"] != "old-access" || oauth["refreshToken"] != "refresh-1" {
				t.Fatalf("credentials rewritten on failure: %#v", oauth)
			}
		})
	}
}

// Parity: core/framework/loader/agent_loader.py::get_claude_code_token — a
// burst of callers observing the same expired token performs exactly one
// grant and every caller receives the refreshed token.
func TestClaudeCodeTokenRefreshIsSingleFlight(t *testing.T) {
	home := tokenTestHome(t)
	writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "refresh-1",
		"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
	}})
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
		startOnce.Do(func() { close(started) })
		<-release
		return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	const workers = 24
	results := make(chan string, workers)
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- ClaudeCodeTokenContext(context.Background())
		}()
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no token request started")
	}
	// Let the remaining callers reach the in-flight grant before it completes.
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()
	close(results)

	for token := range results {
		if token != "new-access" {
			t.Fatalf("caller got %q, want new-access", token)
		}
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("token requests = %d, want exactly 1", got)
	}
}

// Parity: core/framework/loader/agent_loader.py::get_claude_code_token —
// cancellation abandons a refresh and falls back to the on-disk token,
// including when the caller is only waiting on another caller's grant.
func TestClaudeCodeTokenRefreshHonorsCancellation(t *testing.T) {
	t.Run("canceled before the grant", func(t *testing.T) {
		home := tokenTestHome(t)
		writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "old-access", "refreshToken": "refresh-1",
			"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
		}})
		stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
			return http.StatusOK, `{"access_token":"new-access"}`
		})
		installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if got := ClaudeCodeTokenContext(ctx); got != "old-access" {
			t.Fatalf("ClaudeCodeTokenContext = %q, want the stale fallback", got)
		}
		if got := stub.requestCount(); got != 0 {
			t.Fatalf("token requests = %d, want 0", got)
		}
	})

	t.Run("waiter cancels while a grant is in flight", func(t *testing.T) {
		home := tokenTestHome(t)
		writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "old-access", "refreshToken": "refresh-1",
			"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
		}})
		started := make(chan struct{})
		release := make(chan struct{})
		var startOnce sync.Once
		stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
			startOnce.Do(func() { close(started) })
			<-release
			return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
		})
		installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

		leader := make(chan string, 1)
		go func() { leader <- ClaudeCodeTokenContext(context.Background()) }()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("no token request started")
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		begin := time.Now()
		if got := ClaudeCodeTokenContext(ctx); got != "old-access" {
			t.Fatalf("canceled waiter = %q, want the stale fallback", got)
		}
		if elapsed := time.Since(begin); elapsed > time.Second {
			t.Fatalf("canceled waiter blocked for %s", elapsed)
		}

		close(release)
		if got := <-leader; got != "new-access" {
			t.Fatalf("leader = %q, want new-access", got)
		}
		if got := stub.requestCount(); got != 1 {
			t.Fatalf("token requests = %d, want 1", got)
		}
	})
}

// Parity: core/framework/loader/agent_loader.py::_is_codex_token_expired — the
// Codex token's age comes from last_refresh (falling back to the file mtime),
// with the same 5-minute refresh buffer.
func TestCodexTokenExpiryDetection(t *testing.T) {
	cases := []struct {
		name        string
		auth        map[string]any
		useFileTime bool
		want        string
		requests    int
	}{
		{
			name: "fresh last_refresh",
			auth: map[string]any{
				"tokens":       map[string]any{"access_token": "old-codex", "refresh_token": "refresh-1"},
				"last_refresh": tokenTestNow.Add(-10 * time.Minute).UTC().Format(time.RFC3339),
			},
			want: "old-codex", requests: 0,
		},
		{
			name: "stale last_refresh",
			auth: map[string]any{
				"tokens":       map[string]any{"access_token": "old-codex", "refresh_token": "refresh-1"},
				"last_refresh": tokenTestNow.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
			},
			want: "new-codex", requests: 1,
		},
		{
			name: "inside the refresh buffer",
			auth: map[string]any{
				"tokens":       map[string]any{"access_token": "old-codex", "refresh_token": "refresh-1"},
				"last_refresh": tokenTestNow.Add(-58 * time.Minute).UTC().Format(time.RFC3339),
			},
			want: "new-codex", requests: 1,
		},
		{
			name: "missing last_refresh falls back to mtime",
			auth: map[string]any{
				"tokens": map[string]any{"access_token": "old-codex", "refresh_token": "refresh-1"},
			},
			useFileTime: true,
			want:        "old-codex", requests: 0,
		},
		{
			name: "unparsable last_refresh reads as expired",
			auth: map[string]any{
				"tokens":       map[string]any{"access_token": "old-codex", "refresh_token": "refresh-1"},
				"last_refresh": "not-a-time",
			},
			want: "new-codex", requests: 1,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			home := tokenTestHome(t)
			path := codexAuthPath(home)
			writeTokenDoc(t, path, testCase.auth)
			if testCase.useFileTime {
				if err := os.Chtimes(path, tokenTestNow, tokenTestNow); err != nil {
					t.Fatalf("chtimes: %v", err)
				}
			}
			stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
				return http.StatusOK, `{"access_token":"new-codex"}`
			})
			installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

			if got := CodexToken(); got != testCase.want {
				t.Fatalf("CodexToken = %q, want %q", got, testCase.want)
			}
			if got := stub.requestCount(); got != testCase.requests {
				t.Fatalf("token requests = %d, want %d", got, testCase.requests)
			}
		})
	}
}

// Parity: core/framework/loader/agent_loader.py::_save_refreshed_codex_credentials
// — the rotated tokens and last_refresh are persisted, account metadata is
// preserved, and the file is private.
func TestCodexTokenRefreshPersistsRotatedTokens(t *testing.T) {
	home := tokenTestHome(t)
	path := codexAuthPath(home)
	writeTokenDoc(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token":  "old-codex",
			"refresh_token": "refresh-1",
			"id_token":      "id-1",
			"account_id":    "acct-1",
		},
		"last_refresh": tokenTestNow.Add(-2 * time.Hour).UTC().Format(time.RFC3339),
		"metadata":     map[string]any{"keep": true},
	})
	stub := newTokenEndpointStub(t, func(_ int, form url.Values) (int, string) {
		if form.Get("client_id") != codexOAuthClientID {
			t.Errorf("client_id = %q, want %q", form.Get("client_id"), codexOAuthClientID)
		}
		if form.Get("refresh_token") != "refresh-1" {
			t.Errorf("refresh_token = %q, want refresh-1", form.Get("refresh_token"))
		}
		return http.StatusOK, `{"access_token":"new-codex","refresh_token":"refresh-2","id_token":"id-2","expires_in":3600}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	if got := CodexToken(); got != "new-codex" {
		t.Fatalf("CodexToken = %q, want new-codex", got)
	}
	doc := readTokenDoc(t, path)
	tokens, _ := doc["tokens"].(map[string]any)
	if tokens["access_token"] != "new-codex" || tokens["refresh_token"] != "refresh-2" || tokens["id_token"] != "id-2" {
		t.Fatalf("tokens = %#v, want rotated values", tokens)
	}
	if tokens["account_id"] != "acct-1" {
		t.Fatalf("account_id lost: %#v", tokens)
	}
	metadata, _ := doc["metadata"].(map[string]any)
	if metadata["keep"] != true {
		t.Fatalf("metadata lost: %#v", doc)
	}
	lastRefresh, ok := doc["last_refresh"].(string)
	if !ok {
		t.Fatalf("last_refresh = %#v, want an ISO timestamp", doc["last_refresh"])
	}
	parsed, err := time.Parse(time.RFC3339Nano, lastRefresh)
	if err != nil || !parsed.Equal(tokenTestNow) {
		t.Fatalf("last_refresh = %q (%v), want %s", lastRefresh, err, tokenTestNow)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat auth: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("auth mode = %o, want 600", perm)
	}

	if got := CodexToken(); got != "new-codex" {
		t.Fatalf("second CodexToken = %q, want new-codex", got)
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("token requests = %d, want one grant", got)
	}
}

// Parity: core/framework/config.py::get_api_key — a disabled subscription is
// never resolved, so an expired credential on disk must not trigger a refresh
// as a side effect (negative regression).
func TestSubscriptionTokenDisabledFlagDoesNotRefresh(t *testing.T) {
	home := tokenTestHome(t)
	path := claudeCredentialsPath(home)
	writeTokenDoc(t, path, map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "refresh-1",
		"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
	}})
	stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"new-access"}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	root, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if got := SubscriptionToken(map[string]any{}, root); got != "" {
		t.Fatalf("SubscriptionToken = %q, want empty without the flag", got)
	}
	if got := stub.requestCount(); got != 0 {
		t.Fatalf("token requests = %d, want 0", got)
	}
	oauth, _ := readTokenDoc(t, path)["claudeAiOauth"].(map[string]any)
	if oauth["accessToken"] != "old-access" {
		t.Fatalf("disabled subscription rewrote credentials: %#v", oauth)
	}
}

// Parity: core/framework/config.py::get_api_key — the pipeline's subscription
// consumer receives the refreshed token, not the expired one from disk.
func TestSubscriptionTokenReturnsRefreshedToken(t *testing.T) {
	home := tokenTestHome(t)
	writeTokenDoc(t, claudeCredentialsPath(home), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "refresh-1",
		"expiresAt": tokenTestNow.Add(-time.Minute).UnixMilli(),
	}})
	stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"new-access","expires_in":3600}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	root, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	section := map[string]any{"use_claude_code_subscription": true}
	if got := SubscriptionToken(section, root); got != "new-access" {
		t.Fatalf("SubscriptionToken = %q, want new-access", got)
	}
	if got := stub.requestCount(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

// Parity: core/framework/loader/agent_loader.py::_read_claude_credentials — a
// malformed credential file degrades to no token without panicking or making a
// network request (negative regression).
func TestClaudeCodeTokenMalformedCredentials(t *testing.T) {
	home := tokenTestHome(t)
	path := claudeCredentialsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir credentials dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write malformed credentials: %v", err)
	}
	stub := newTokenEndpointStub(t, func(int, url.Values) (int, string) {
		return http.StatusOK, `{"access_token":"new-access"}`
	})
	installTokenTestHooks(t, stub.server.URL, stub.server.URL, tokenTestNow)

	if got := ClaudeCodeToken(); got != "" {
		t.Fatalf("ClaudeCodeToken = %q, want empty", got)
	}
	if got := stub.requestCount(); got != 0 {
		t.Fatalf("token requests = %d, want 0", got)
	}
}
