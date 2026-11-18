package integrations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// batch3SearchEnv builds a fake environment map.
func batch3SearchEnv(values map[string]string) map[string]string { return values }

// Parity: tools/tests/tools/test_web_search_tool.py::test_no_credentials_returns_error
func TestBatch3WebSearchNoCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no provider should be called without credentials")
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(nil))

	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test query"})
	if message, _ := result["error"].(string); message != "No search credentials configured" {
		t.Fatalf("error = %v", result["error"])
	}
	if result["help"] == nil {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_empty_query_returns_error, tools/tests/tools/test_web_search_tool.py::test_long_query_returns_error
func TestBatch3WebSearchQueryValidation(t *testing.T) {
	registry := newBatch3MediaRegistry()
	cases := []string{"", strings.Repeat("x", 501)}
	for _, query := range cases {
		result := batch3Run(t, registry, "web_search", map[string]any{"query": query})
		message, _ := result["error"].(string)
		if !strings.Contains(message, "1-500") {
			t.Fatalf("error = %q", message)
		}
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_brave_missing_api_key
func TestBatch3WebSearchBraveMissingKey(t *testing.T) {
	registry := newBatch3MediaRegistry()
	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test", "provider": "brave"})
	if message, _ := result["error"].(string); message != "Brave credentials not configured" {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_google_missing_api_key, tools/tests/tools/test_web_search_tool.py::test_google_missing_cse_id
func TestBatch3WebSearchGoogleMissingCredentials(t *testing.T) {
	for _, env := range []map[string]string{{}, {"GOOGLE_API_KEY": "test-key"}} {
		withEnv := newBatch3MediaRegistry(withBatch3Getenv(func(name string) string { return env[name] }))
		result := batch3Run(t, withEnv, "web_search", map[string]any{"query": "test", "provider": "google"})
		if message, _ := result["error"].(string); message != "Google credentials not configured" {
			t.Fatalf("env = %v error = %v", env, result["error"])
		}
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_brave_explicit_provider
func TestBatch3WebSearchBrave(t *testing.T) {
	var seenPath, seenToken, seenAccept string
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenQuery = r.URL.Query()
		seenToken = r.Header.Get("X-Subscription-Token")
		seenAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"web": map[string]any{
				"results": []any{
					map[string]any{"title": "Bee", "url": "https://example.com/bee", "description": "A bee."},
					map[string]any{"title": "Hive", "url": "https://example.com/hive", "description": "A hive."},
				},
			},
		})
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{
		"query": "bees", "provider": "brave", "num_results": 1, "country": "uk",
	})
	if result["provider"] != "brave" || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	results, _ := result["results"].([]map[string]any)
	if len(results) != 1 || results[0]["title"] != "Bee" || results[0]["snippet"] != "A bee." {
		t.Fatalf("results = %v", result["results"])
	}
	if seenPath != "/res/v1/web/search" {
		t.Fatalf("path = %q", seenPath)
	}
	if seenQuery.Get("q") != "bees" || seenQuery.Get("count") != "1" || seenQuery.Get("country") != "uk" {
		t.Fatalf("query = %v", seenQuery)
	}
	if seenToken != "test-key" || seenAccept != "application/json" {
		t.Fatalf("headers = %q %q", seenToken, seenAccept)
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_google_explicit_provider
func TestBatch3WebSearchGoogle(t *testing.T) {
	var seenPath string
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		seenQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items": []any{
				map[string]any{"title": "Bee", "link": "https://example.com/bee", "snippet": "A bee."},
			},
		})
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"GOOGLE_API_KEY": "test-key",
		"GOOGLE_CSE_ID":  "test-cse-id",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{
		"query": "bees", "provider": "google", "language": "id", "country": "id",
	})
	if result["provider"] != "google" || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	results, _ := result["results"].([]map[string]any)
	if len(results) != 1 || results[0]["url"] != "https://example.com/bee" {
		t.Fatalf("results = %v", result["results"])
	}
	if seenPath != "/customsearch/v1" {
		t.Fatalf("path = %q", seenPath)
	}
	for key, want := range map[string]string{
		"key": "test-key", "cx": "test-cse-id", "q": "bees",
		"num": "10", "lr": "lang_id", "gl": "id",
	} {
		if got := seenQuery.Get(key); got != want {
			t.Fatalf("%s = %q, want %q (query %v)", key, got, want, seenQuery)
		}
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_auto_prefers_brave_for_backward_compatibility
func TestBatch3WebSearchAutoPrefersBrave(t *testing.T) {
	var braveCalls, googleCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/res/v1/web/search" {
			braveCalls.Add(1)
			_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Brave","url":"https://example.com/b","description":"b"}]}}`))
			return
		}
		googleCalls.Add(1)
		_, _ = w.Write([]byte(`{"items":[{"title":"Google","link":"https://example.com/g","snippet":"g"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
		"GOOGLE_API_KEY":       "test-google-key",
		"GOOGLE_CSE_ID":        "test-cse-id",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test"})
	if result["provider"] != "brave" {
		t.Fatalf("provider = %v", result["provider"])
	}
	if braveCalls.Load() != 1 || googleCalls.Load() != 0 {
		t.Fatalf("brave = %d, google = %d", braveCalls.Load(), googleCalls.Load())
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_auto_falls_back_to_google
func TestBatch3WebSearchAutoFallsBackToGoogle(t *testing.T) {
	var braveCalls, googleCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/res/v1/web/search" {
			braveCalls.Add(1)
			_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
			return
		}
		googleCalls.Add(1)
		_, _ = w.Write([]byte(`{"items":[{"title":"Google","link":"https://example.com/g","snippet":"g"}]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"GOOGLE_API_KEY": "test-google-key",
		"GOOGLE_CSE_ID":  "test-cse-id",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test", "provider": "auto"})
	if result["provider"] != "google" {
		t.Fatalf("provider = %v", result["provider"])
	}
	if braveCalls.Load() != 0 || googleCalls.Load() != 1 {
		t.Fatalf("brave = %d, google = %d", braveCalls.Load(), googleCalls.Load())
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_default_provider_is_auto
func TestBatch3WebSearchDefaultProviderIsAuto(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
	}))
	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test"})
	if result["provider"] != "brave" {
		t.Fatalf("provider = %v", result["provider"])
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_custom_language_and_country
func TestBatch3WebSearchCustomParams(t *testing.T) {
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"GOOGLE_API_KEY": "test-key",
		"GOOGLE_CSE_ID":  "test-cse-id",
	}))
	batch3Run(t, registry, "web_search", map[string]any{
		"query": "test", "provider": "google", "language": "id", "country": "id",
	})
	if seenQuery.Get("lr") != "lang_id" || seenQuery.Get("gl") != "id" {
		t.Fatalf("query = %v", seenQuery)
	}
}

// Parity: tools/tests/tools/test_web_search_tool.py::test_num_results_parameter
func TestBatch3WebSearchNumResults(t *testing.T) {
	var seenQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
	}))
	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test", "num_results": 5})
	if seenQuery.Get("count") != "5" {
		t.Fatalf("count = %q", seenQuery.Get("count"))
	}
	if result["provider"] != "brave" {
		t.Fatalf("provider = %v", result["provider"])
	}
}

// Parity-exempt: upstream tests never stub provider HTTP, so retry and error
// branches have no Python test to point at; behavior mirrors the Python loop.
func TestBatch3WebSearchRateLimitRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Bee","url":"https://example.com/bee","description":"b"}]}}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test"})
	if result["provider"] != "brave" || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

// Parity-exempt: see above.
func TestBatch3WebSearchRateLimitExhausted(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}`))
	}))
	defer server.Close()
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"GOOGLE_API_KEY": "test-key",
		"GOOGLE_CSE_ID":  "test-cse-id",
	}))

	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test", "provider": "google"})
	if message, _ := result["error"].(string); message != "Google rate limit exceeded. Try again later." {
		t.Fatalf("error = %v", result["error"])
	}
	if calls.Load() != 4 {
		t.Fatalf("calls = %d, want 4 (1 + 3 retries)", calls.Load())
	}
}

// Parity-exempt: see above.
func TestBatch3WebSearchProviderErrors(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		status   int
		env      map[string]string
		needle   string
	}{
		{"brave-unauthorized", "brave", http.StatusUnauthorized, map[string]string{"BRAVE_SEARCH_API_KEY": "k"}, "Invalid Brave API key"},
		{"brave-server-error", "brave", http.StatusInternalServerError, map[string]string{"BRAVE_SEARCH_API_KEY": "k"}, "HTTP 500"},
		{"google-unauthorized", "google", http.StatusUnauthorized, map[string]string{"GOOGLE_API_KEY": "k", "GOOGLE_CSE_ID": "c"}, "Invalid Google API key"},
		{"google-forbidden", "google", http.StatusForbidden, map[string]string{"GOOGLE_API_KEY": "k", "GOOGLE_CSE_ID": "c"}, "not authorized"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
			}))
			defer server.Close()
			registry := batch3TestRegistry(t, server, testCase.env)
			result := batch3Run(t, registry, "web_search", map[string]any{
				"query": "test", "provider": testCase.provider,
			})
			message, _ := result["error"].(string)
			if !strings.Contains(message, testCase.needle) {
				t.Fatalf("error = %q, want %q", message, testCase.needle)
			}
		})
	}
}

// Parity-exempt: upstream tests never stub HTTP, so the timeout branch has no
// Python counterpart; it mirrors the handler's TimeoutException mapping.
func TestBatch3WebSearchTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"web":{"results":[]}}`))
	}))
	defer server.Close()
	fast := server.Client()
	fast.Timeout = 10 * time.Millisecond
	registry := batch3TestRegistry(t, server, batch3SearchEnv(map[string]string{
		"BRAVE_SEARCH_API_KEY": "test-key",
	}), withBatch3HTTPClient(fast))
	result := batch3Run(t, registry, "web_search", map[string]any{"query": "test"})
	if message, _ := result["error"].(string); message != "Search request timed out" {
		t.Fatalf("error = %v", result["error"])
	}
}
