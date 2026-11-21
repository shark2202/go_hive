package webintel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type loggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type requestRecorder struct {
	mu       sync.Mutex
	requests []loggedRequest
}

func (rec *requestRecorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, loggedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *requestRecorder) last(t *testing.T) loggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *requestRecorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

func (rec *requestRecorder) countPath(path string) int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	total := 0
	for _, request := range rec.requests {
		if request.Path == path {
			total++
		}
	}
	return total
}

// newFakeServer starts a fake upstream that records every exchange.
func newFakeServer(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*httptest.Server, *requestRecorder) {
	t.Helper()
	recorder := &requestRecorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	return server, recorder
}

// testRegistry builds a lane-6 registry with an empty environment so
// ambient credentials cannot leak into a test.
func testRegistry(t *testing.T, opts ...Option) *Registry {
	t.Helper()
	base := []Option{WithGetenv(func(string) string { return "" })}
	return NewRegistry(append(base, opts...)...)
}

func executeTool(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func errorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func objectValue(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map", value)
	}
	return object
}

func arrayValue(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want array", value)
	}
	return items
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type timeoutTransport struct{}

func (timeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, timeoutError{}
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise the MCP tool functions, not the registry surface.
func TestCatalogIntegrity(t *testing.T) {
	registry := NewRegistry()
	if got := len(registry.Names()); got != 3 {
		t.Fatalf("tools = %d, want 3 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 42 {
		t.Fatalf("actions = %d, want 42", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	wantActions := map[string]int{"similarweb": 29, "news": 7, "google_maps": 6}
	for name, want := range wantActions {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("%s tool missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" {
			t.Fatalf("%s missing identity: %+v", name, tool)
		}
		seen := map[string]bool{}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if seen[action.Name] {
				t.Fatalf("%s.%s duplicated", name, action.Name)
			}
			seen[action.Name] = true
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
	if got := len(similarWebActions); got != 29 {
		t.Fatalf("similarweb table rows = %d, want 29", got)
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallIntoSharedRegistry(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := NewRegistry().Install(shared); installed != 3 {
		t.Fatalf("installed = %d, want 3", installed)
	}
	for name, want := range map[string]int{"similarweb": 29, "news": 7, "google_maps": 6} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
		tool, ok := shared.GetTool(name)
		if !ok {
			t.Fatalf("%s tool missing", name)
		}
		properties, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %v", name, tool.Parameters["properties"])
		}
		action, ok := properties["action"].(map[string]any)
		if !ok {
			t.Fatalf("%s missing action selector: %v", name, properties)
		}
		if enum, _ := action["enum"].([]string); len(enum) != want {
			t.Fatalf("%s action enum = %v", name, action["enum"])
		}
	}
}

// -------------------------------------------------------------- similarweb ---

// swInputs supplies non-domain required arguments; the default
// input below covers the 25 domain-shaped actions.
var swInputs = map[string]map[string]any{
	"similarweb_v5_traffic_and_engagement": {
		"domain": "amazon.com", "country": "us", "granularity": "daily", "start_date": "2026-01-01",
	},
	"similarweb_v5_top_sites_by_category": {"category": "Games"},
	"similarweb_v5_deduplicated_audience": {"domains": "amazon.com,ebay.com"},
	"similarweb_v5_audience_overlap":      {"domain": "amazon.com", "compare_to": "ebay.com"},
	"similarweb_v5_segment_analysis":      {"segment_id": "seg1"},
}

// swExpectedParam reads the value an action should place on the wire,
// honouring catalog defaults for omitted inputs.
func swExpectedParam(t *testing.T, endpoint swAction, input map[string]any, name string) string {
	t.Helper()
	if value, present := input[name]; present {
		return fmt.Sprint(value)
	}
	schema, ok := endpoint.params[name].(map[string]any)
	if !ok {
		t.Fatalf("%s: param %q has no schema", endpoint.action, name)
	}
	if def, present := schema["default"]; present {
		return fmt.Sprint(def)
	}
	return ""
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_similarweb_v5_traffic_and_engagement_success
func TestSimilarWebActions(t *testing.T) {
	for _, endpoint := range similarWebActions {
		endpoint := endpoint
		t.Run(endpoint.action, func(t *testing.T) {
			server, recorder := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				writeJSONResponse(w, http.StatusOK, map[string]any{"action": endpoint.action})
			})
			registry := testRegistry(t,
				WithBaseURL("similarweb_tool", server.URL),
				WithCredential("similarweb_tool", "test-key"),
				WithHTTPClient(server.Client()),
			)
			input := map[string]any{"action": endpoint.action, "domain": "example.com"}
			for name, value := range swInputs[endpoint.action] {
				input[name] = value
			}
			result := executeTool(t, registry, "similarweb", input)
			if result["action"] != endpoint.action {
				t.Fatalf("body = %v, want echoed action", result)
			}
			request := recorder.last(t)
			if request.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", request.Method)
			}
			if request.Path != endpoint.path {
				t.Fatalf("path = %s, want %s", request.Path, endpoint.path)
			}
			if got := request.Header.Get("api-key"); got != "test-key" {
				t.Fatalf("api-key = %q", got)
			}
			if got := request.Header.Get("Accept"); got != "application/json" {
				t.Fatalf("Accept = %q", got)
			}
			for _, name := range endpoint.send {
				key := name
				if renamed, ok := endpoint.rename[name]; ok {
					key = renamed
				}
				want := swExpectedParam(t, endpoint, input, name)
				if got := request.Query.Get(key); got != want {
					t.Fatalf("query %q = %q, want %q", key, got, want)
				}
			}
			for name, value := range endpoint.constants {
				if got := request.Query.Get(name); got != fmt.Sprint(value) {
					t.Fatalf("constant %q = %q, want %v", name, got, value)
				}
			}
		})
	}
}

// Parity: tools/tests/tools/test_similarweb_tool.py::test_make_request_error_handling
func TestSimilarWebStatusErrors(t *testing.T) {
	statuses := []struct {
		code int
		text string
	}{
		{http.StatusUnauthorized, "Unauthorized"},
		{http.StatusNotFound, "Not Found"},
		{http.StatusTooManyRequests, "Too Many Requests"},
	}
	for _, status := range statuses {
		status := status
		t.Run(fmt.Sprintf("%d", status.code), func(t *testing.T) {
			server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(status.code)
				_, _ = w.Write([]byte(status.text))
			})
			registry := testRegistry(t,
				WithBaseURL("similarweb_tool", server.URL),
				WithCredential("similarweb_tool", "test-key"),
				WithHTTPClient(server.Client()),
			)
			for _, endpoint := range similarWebActions {
				endpoint := endpoint
				t.Run(endpoint.action, func(t *testing.T) {
					input := map[string]any{"action": endpoint.action, "domain": "example.com"}
					for name, value := range swInputs[endpoint.action] {
						input[name] = value
					}
					result := executeTool(t, registry, "similarweb", input)
					want := fmt.Sprintf("HTTP error %d: %s", status.code, status.text)
					if got := errorText(t, result); got != want {
						t.Fatalf("error = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

// Parity-exempt: upstream has no missing-credential case for SimilarWeb; the
// credential-store error dict is ported but untested there.
func TestSimilarWebCredentials(t *testing.T) {
	registry := testRegistry(t)
	result := executeTool(t, registry, "similarweb", map[string]any{
		"action": "similarweb_v5_website_rank",
		"domain": "amazon.com",
	})
	if got := errorText(t, result); got != "SimilarWeb credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	help, _ := result["help"].(string)
	if !strings.Contains(help, "SIMILARWEB_API_KEY") {
		t.Fatalf("help = %q", help)
	}
}

// -------------------------------------------------------------------- news ---

// newsHandler serves both providers from one fake upstream.
func newsHandler(newsdataStatus, finlightStatus int, sentiment bool) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/news"), strings.HasSuffix(r.URL.Path, "/archive"):
			if newsdataStatus != http.StatusOK {
				writeJSONResponse(w, newsdataStatus, map[string]any{"status": "error"})
				return
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"results": []any{map[string]any{
				"title":       "Funding Round",
				"source_id":   "techcrunch",
				"pubDate":     "2026-02-01",
				"link":        "https://example.com/article",
				"description": "A funding round was announced.",
			}}})
		case strings.HasSuffix(r.URL.Path, "/articles"):
			if finlightStatus != http.StatusOK {
				writeJSONResponse(w, finlightStatus, map[string]any{"status": "error"})
				return
			}
			article := map[string]any{
				"title":       "Market Update",
				"source":      "finlight",
				"publishDate": "2026-02-02",
				"link":        "https://example.com/fin",
				"summary":     "Markets moved today.",
			}
			if sentiment {
				article["sentiment"] = 0.5
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"articles": []any{article}})
		default:
			http.NotFound(w, r)
		}
	}
}

// Parity: tools/tests/tools/test_news_tool.py::test_news_search_newsdata_success
func TestNewsActions(t *testing.T) {
	cases := []struct {
		action   string
		input    map[string]any
		provider string
		path     string
		extra    map[string]any
	}{
		{"news_search", map[string]any{"query": "funding"}, "newsdata", "/news", map[string]any{"query": "funding"}},
		{"news_headlines", map[string]any{"category": "business", "country": "us"}, "newsdata", "/news", map[string]any{"category": "business", "country": "us"}},
		{"news_by_company", map[string]any{"company_name": "Acme", "days_back": 7}, "newsdata", "/archive", map[string]any{"company_name": "Acme", "days_back": 7}},
		{"news_sentiment", map[string]any{"query": "Acme"}, "finlight", "/articles", map[string]any{"query": "Acme"}},
		{"news_latest", map[string]any{"language": "en"}, "newsdata", "/news", map[string]any{}},
		{"news_by_source", map[string]any{"sources": "bbc,cnn"}, "newsdata", "/archive", map[string]any{"sources": "bbc,cnn"}},
		{"news_by_topic", map[string]any{"topic": "ai", "days_back": 3}, "newsdata", "/archive", map[string]any{"topic": "ai", "days_back": 3}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.action, func(t *testing.T) {
			server, recorder := newFakeServer(t, newsHandler(http.StatusOK, http.StatusOK, true))
			registry := testRegistry(t,
				WithBaseURL("news_tool", server.URL),
				WithBaseURL("finlight_tool", server.URL),
				WithCredential("news_tool", "news-key"),
				WithCredential("finlight_tool", "finlight-key"),
				WithHTTPClient(server.Client()),
				WithClock(func() time.Time { return time.Date(2026, 2, 10, 12, 0, 0, 0, time.UTC) }),
			)
			input := map[string]any{"action": tc.action}
			for name, value := range tc.input {
				input[name] = value
			}
			result := executeTool(t, registry, "news", input)
			if result["provider"] != tc.provider {
				t.Fatalf("provider = %v, want %s (%v)", result["provider"], tc.provider, result)
			}
			if result["total"] != 1 {
				t.Fatalf("total = %v, want 1", result["total"])
			}
			items := arrayValue(t, result["results"])
			if objectValue(t, items[0])["title"] == "" {
				t.Fatalf("article title missing: %v", items[0])
			}
			for name, want := range tc.extra {
				if got := result[name]; got != want {
					t.Fatalf("%s = %v, want %v", name, got, want)
				}
			}
			if request := recorder.last(t); request.Path != tc.path {
				t.Fatalf("path = %s, want %s", request.Path, tc.path)
			}
		})
	}
}

// Parity: tools/tests/tools/test_news_tool.py::test_news_by_company_date_filter
func TestNewsByCompanyDateFilter(t *testing.T) {
	server, recorder := newFakeServer(t, newsHandler(http.StatusOK, http.StatusOK, false))
	registry := testRegistry(t,
		WithBaseURL("news_tool", server.URL),
		WithBaseURL("finlight_tool", server.URL),
		WithCredential("news_tool", "news-key"),
		WithHTTPClient(server.Client()),
		WithClock(func() time.Time { return time.Date(2026, 2, 10, 6, 0, 0, 0, time.UTC) }),
	)
	result := executeTool(t, registry, "news", map[string]any{
		"action": "news_by_company", "company_name": "Acme", "days_back": 7,
	})
	if result["provider"] != "newsdata" {
		t.Fatalf("provider = %v", result["provider"])
	}
	request := recorder.last(t)
	if request.Path != "/archive" {
		t.Fatalf("path = %s, want /archive", request.Path)
	}
	for name, want := range map[string]string{
		"q":         `"Acme"`,
		"from_date": "2026-02-03",
		"to_date":   "2026-02-10",
	} {
		if got := request.Query.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

// Parity: tools/tests/tools/test_news_tool.py::test_newsdata_retries_on_429_then_succeeds
func TestNewsRateLimits(t *testing.T) {
	previous := backoffSleep
	backoffSleep = func(time.Duration) {}
	t.Cleanup(func() { backoffSleep = previous })

	t.Run("newsdata retries then succeeds", func(t *testing.T) {
		attempts := 0
		server, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			attempts++
			if attempts == 1 {
				writeJSONResponse(w, http.StatusTooManyRequests, map[string]any{})
				return
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"results": []any{map[string]any{"title": "OK", "source_id": "s"}}})
		})
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
		if result["provider"] != "newsdata" {
			t.Fatalf("provider = %v", result["provider"])
		}
	})

	t.Run("newsdata exhausts retries", func(t *testing.T) {
		attempts := 0
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			attempts++
			writeJSONResponse(w, http.StatusTooManyRequests, map[string]any{})
		})
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if attempts != 4 {
			t.Fatalf("attempts = %d, want 4", attempts)
		}
		if got := errorText(t, result); got != "NewsData rate limit exceeded. Try again later." {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("finlight retries then succeeds", func(t *testing.T) {
		attempts := 0
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			attempts++
			if attempts == 1 {
				writeJSONResponse(w, http.StatusTooManyRequests, map[string]any{})
				return
			}
			writeJSONResponse(w, http.StatusOK, map[string]any{"articles": []any{map[string]any{"title": "OK", "source": "fin", "sentiment": 0.5}}})
		})
		registry := testRegistry(t,
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
		if attempts != 2 {
			t.Fatalf("attempts = %d, want 2", attempts)
		}
		if result["provider"] != "finlight" {
			t.Fatalf("provider = %v", result["provider"])
		}
	})

	t.Run("finlight exhausts retries", func(t *testing.T) {
		attempts := 0
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			attempts++
			writeJSONResponse(w, http.StatusTooManyRequests, map[string]any{})
		})
		registry := testRegistry(t,
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
		if attempts != 4 {
			t.Fatalf("attempts = %d, want 4", attempts)
		}
		if got := errorText(t, result); got != "Finlight rate limit exceeded. Try again later." {
			t.Fatalf("error = %q", got)
		}
	})
}

// Parity: tools/tests/tools/test_news_tool.py::test_news_search_falls_back_to_finlight
func TestNewsFallback(t *testing.T) {
	t.Run("newsdata failure falls back to finlight", func(t *testing.T) {
		server, recorder := newFakeServer(t, newsHandler(http.StatusUnauthorized, http.StatusOK, false))
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "markets"})
		if result["provider"] != "finlight" {
			t.Fatalf("provider = %v, want finlight (%v)", result["provider"], result)
		}
		if recorder.countPath("/articles") != 1 {
			t.Fatalf("finlight calls = %d, want 1", recorder.countPath("/articles"))
		}
	})

	t.Run("both providers fail", func(t *testing.T) {
		server, _ := newFakeServer(t, newsHandler(http.StatusUnauthorized, http.StatusUnauthorized, false))
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "markets"})
		if got := errorText(t, result); got != "All providers failed" {
			t.Fatalf("error = %q", got)
		}
		providers := objectValue(t, result["providers"])
		primary := objectValue(t, providers["primary"])
		fallback := objectValue(t, providers["fallback"])
		if primary["error"] != "Invalid NewsData API key" {
			t.Fatalf("primary = %v", primary)
		}
		if fallback["error"] != "Invalid Finlight API key" {
			t.Fatalf("fallback = %v", fallback)
		}
	})

	t.Run("finlight skipped when newsdata succeeds", func(t *testing.T) {
		server, recorder := newFakeServer(t, newsHandler(http.StatusOK, http.StatusOK, false))
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if result["provider"] != "newsdata" {
			t.Fatalf("provider = %v", result["provider"])
		}
		if recorder.countPath("/articles") != 0 {
			t.Fatalf("finlight calls = %d, want 0", recorder.countPath("/articles"))
		}
	})
}

// Parity: tools/tests/tools/test_news_tool.py::test_categorical_sentiment_normalized
func TestNewsSentimentNormalization(t *testing.T) {
	server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		writeJSONResponse(w, http.StatusOK, map[string]any{"articles": []any{
			map[string]any{"title": "A", "source": "s", "sentiment": 0.75},
			map[string]any{"title": "B", "source": "s", "sentiment": -0.3},
			map[string]any{"title": "C", "source": "s", "sentiment": "positive"},
			map[string]any{"title": "D", "source": "s", "sentiment": "negative"},
			map[string]any{"title": "E", "source": "s", "sentiment": "neutral"},
			map[string]any{"title": "F", "source": "s", "sentiment": 5.0},
			map[string]any{"title": "G", "source": "s"},
		}})
	})
	registry := testRegistry(t,
		WithBaseURL("finlight_tool", server.URL),
		WithCredential("finlight_tool", "finlight-key"),
		WithHTTPClient(server.Client()),
	)
	result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
	items := arrayValue(t, result["results"])
	want := []float64{0.75, -0.3, 1, -1, 0, 1}
	for index, expected := range want {
		article := objectValue(t, items[index])
		if got := article["sentiment"]; got != expected {
			t.Fatalf("article %d sentiment = %v, want %v", index, got, expected)
		}
	}
	last := objectValue(t, items[len(items)-1])
	if _, present := last["sentiment"]; present {
		t.Fatalf("missing sentiment should be omitted: %v", last)
	}
}

// Parity-exempt: upstream tests cover only the Finlight-specific missing-key
// path; these assertions pin the NewsData mapping and the 401/404 responses
// that upstream exercises solely through fallback.
func TestNewsProviderErrors(t *testing.T) {
	t.Run("newsdata 401", func(t *testing.T) {
		server, _ := newFakeServer(t, newsHandler(http.StatusUnauthorized, http.StatusOK, false))
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if got := errorText(t, result); got != "Invalid NewsData API key" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("newsdata 404", func(t *testing.T) {
		server, _ := newFakeServer(t, newsHandler(http.StatusNotFound, http.StatusOK, false))
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if got := errorText(t, result); got != "NewsData request failed: HTTP 404" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("newsdata 422 detail", func(t *testing.T) {
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]any{"results": map[string]any{"message": "bad q"}})
		})
		registry := testRegistry(t,
			WithBaseURL("news_tool", server.URL),
			WithCredential("news_tool", "news-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "test"})
		if got := errorText(t, result); got != "Invalid NewsData parameters: bad q" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("finlight 401", func(t *testing.T) {
		server, _ := newFakeServer(t, newsHandler(http.StatusOK, http.StatusUnauthorized, false))
		registry := testRegistry(t,
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
		if got := errorText(t, result); got != "Invalid Finlight API key" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("finlight 404", func(t *testing.T) {
		server, _ := newFakeServer(t, newsHandler(http.StatusOK, http.StatusNotFound, false))
		registry := testRegistry(t,
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
		if got := errorText(t, result); got != "Finlight request failed: HTTP 404" {
			t.Fatalf("error = %q", got)
		}
	})

	t.Run("finlight 422 detail", func(t *testing.T) {
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writeJSONResponse(w, http.StatusUnprocessableEntity, map[string]any{"message": "bad query"})
		})
		registry := testRegistry(t,
			WithBaseURL("finlight_tool", server.URL),
			WithCredential("finlight_tool", "finlight-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "test"})
		if got := errorText(t, result); got != "Invalid Finlight parameters: bad query" {
			t.Fatalf("error = %q", got)
		}
	})
}

// Parity: tools/tests/tools/test_news_tool.py::test_news_sentiment_requires_finlight
func TestNewsNoCredentials(t *testing.T) {
	registry := testRegistry(t)
	search := executeTool(t, registry, "news", map[string]any{"action": "news_search", "query": "acme"})
	if got := errorText(t, search); got != "No news credentials configured" {
		t.Fatalf("search error = %q", got)
	}
	sentiment := executeTool(t, registry, "news", map[string]any{"action": "news_sentiment", "query": "acme"})
	if got := errorText(t, sentiment); got != "Finlight credentials not configured" {
		t.Fatalf("sentiment error = %q", got)
	}
}

// Parity: tools/tests/tools/test_news_tool.py::test_fallback_on_newsdata_timeout
func TestNewsLatestFallback(t *testing.T) {
	server, _ := newFakeServer(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/articles"):
			writeJSONResponse(w, http.StatusOK, map[string]any{"articles": []any{map[string]any{"title": "Fallback", "source": "fin"}}})
		case r.URL.Query().Get("q") == "":
			writeJSONResponse(w, http.StatusInternalServerError, map[string]any{})
		default:
			writeJSONResponse(w, http.StatusUnauthorized, map[string]any{})
		}
	})
	registry := testRegistry(t,
		WithBaseURL("news_tool", server.URL),
		WithBaseURL("finlight_tool", server.URL),
		WithCredential("news_tool", "news-key"),
		WithCredential("finlight_tool", "finlight-key"),
		WithHTTPClient(server.Client()),
	)
	result := executeTool(t, registry, "news", map[string]any{"action": "news_latest", "language": "en"})
	if result["provider"] != "finlight" {
		t.Fatalf("provider = %v, want finlight (%v)", result["provider"], result)
	}
}

// Parity-exempt: upstream exercises 401/404/429 through fallback and retry
// paths rather than per-action status assertions; this pins every news action
// against each status (the port's coverage contract).
func TestNewsActionStatusErrors(t *testing.T) {
	previous := backoffSleep
	backoffSleep = func(time.Duration) {}
	t.Cleanup(func() { backoffSleep = previous })

	statuses := []struct {
		code     int
		newsdata string
		finlight string
	}{
		{http.StatusUnauthorized, "Invalid NewsData API key", "Invalid Finlight API key"},
		{http.StatusNotFound, "NewsData request failed: HTTP 404", "Finlight request failed: HTTP 404"},
		{http.StatusTooManyRequests, "NewsData rate limit exceeded. Try again later.", "Finlight rate limit exceeded. Try again later."},
	}
	actions := []struct {
		action   string
		input    map[string]any
		provider string
	}{
		{"news_search", map[string]any{"query": "test"}, "newsdata"},
		{"news_headlines", map[string]any{"category": "business", "country": "us"}, "newsdata"},
		{"news_by_company", map[string]any{"company_name": "Acme"}, "newsdata"},
		{"news_sentiment", map[string]any{"query": "test"}, "finlight"},
		{"news_latest", map[string]any{"language": "en"}, "newsdata"},
		{"news_by_source", map[string]any{"sources": "bbc"}, "newsdata"},
		{"news_by_topic", map[string]any{"topic": "ai"}, "newsdata"},
	}
	for _, status := range statuses {
		status := status
		for _, action := range actions {
			action := action
			t.Run(fmt.Sprintf("%s/%d", action.action, status.code), func(t *testing.T) {
				server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
					writeJSONResponse(w, status.code, map[string]any{})
				})
				options := []Option{
					WithBaseURL("news_tool", server.URL),
					WithBaseURL("finlight_tool", server.URL),
					WithHTTPClient(server.Client()),
				}
				want := status.newsdata
				if action.provider == "newsdata" {
					options = append(options, WithCredential("news_tool", "news-key"))
				} else {
					options = append(options, WithCredential("finlight_tool", "finlight-key"))
					want = status.finlight
				}
				registry := testRegistry(t, options...)
				input := map[string]any{"action": action.action}
				for name, value := range action.input {
					input[name] = value
				}
				result := executeTool(t, registry, "news", input)
				if got := errorText(t, result); got != want {
					t.Fatalf("error = %q, want %q", got, want)
				}
			})
		}
	}
}

// ------------------------------------------------------------- google maps ---

// mapsHandler serves the five Maps endpoints with representative
// payloads.
func mapsHandler(w http.ResponseWriter, r *http.Request, _ map[string]any) {
	switch r.URL.Path {
	case "/geocode/json":
		if r.URL.Query().Get("latlng") != "" {
			writeJSONResponse(w, http.StatusOK, map[string]any{"status": "OK", "results": []any{map[string]any{
				"formatted_address": "277 Bedford Ave",
				"geometry":          map[string]any{"location": map[string]any{"lat": 40.7142205, "lng": -73.9612905}, "location_type": "ROOFTOP"},
				"place_id":          "rev-1",
				"types":             []any{"street_address"},
			}}})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{"status": "OK", "results": []any{map[string]any{
			"formatted_address":  "1600 Amphitheatre Parkway, Mountain View, CA",
			"geometry":           map[string]any{"location": map[string]any{"lat": 37.4224764, "lng": -122.0842499}, "location_type": "ROOFTOP"},
			"place_id":           "geo-1",
			"types":              []any{"street_address"},
			"address_components": []any{map[string]any{"long_name": "1600", "types": []any{"street_number"}}},
		}}})
	case "/directions/json":
		writeJSONResponse(w, http.StatusOK, map[string]any{"status": "OK", "routes": []any{map[string]any{
			"summary": "US-101",
			"legs": []any{map[string]any{
				"start_address": "A",
				"end_address":   "B",
				"distance":      map[string]any{"text": "10 mi", "value": 16093},
				"duration":      map[string]any{"text": "20 mins", "value": 1200},
				"steps": []any{map[string]any{
					"html_instructions": "Head north",
					"distance":          map[string]any{"text": "1 mi", "value": 1609},
					"duration":          map[string]any{"text": "2 mins", "value": 120},
					"travel_mode":       "DRIVING",
				}},
			}},
			"overview_polyline": map[string]any{"points": "abc"},
			"warnings":          []any{},
			"waypoint_order":    []any{},
		}}})
	case "/distancematrix/json":
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"status":                "OK",
			"origin_addresses":      []any{"A"},
			"destination_addresses": []any{"B"},
			"rows": []any{map[string]any{"elements": []any{map[string]any{
				"status":              "OK",
				"distance":            map[string]any{"text": "5 mi", "value": 8046},
				"duration":            map[string]any{"text": "10 mins", "value": 600},
				"duration_in_traffic": map[string]any{"text": "12 mins", "value": 720},
			}}}},
		})
	case "/place/details/json":
		writeJSONResponse(w, http.StatusOK, map[string]any{"status": "OK", "result": map[string]any{
			"name": "Test Place", "place_id": "p1", "rating": 4.5,
		}})
	case "/place/textsearch/json":
		writeJSONResponse(w, http.StatusOK, map[string]any{"status": "OK", "results": []any{map[string]any{
			"name":               "Cafe",
			"formatted_address":  "1 Main St",
			"geometry":           map[string]any{"location": map[string]any{"lat": 1.5, "lng": 2.5}},
			"place_id":           "p2",
			"types":              []any{"cafe"},
			"rating":             4.2,
			"user_ratings_total": 10,
			"opening_hours":      map[string]any{"open_now": true},
		}}, "next_page_token": "next-1"})
	default:
		http.NotFound(w, r)
	}
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_geocode_success
func TestGoogleMapsActions(t *testing.T) {
	cases := []struct {
		action string
		input  map[string]any
		path   string
	}{
		{"maps_geocode", map[string]any{"address": "1600 Amphitheatre Parkway"}, "/geocode/json"},
		{"maps_reverse_geocode", map[string]any{"latitude": 40.714224, "longitude": -73.961452}, "/geocode/json"},
		{"maps_directions", map[string]any{"origin": "A", "destination": "B"}, "/directions/json"},
		{"maps_distance_matrix", map[string]any{"origins": "A", "destinations": "B"}, "/distancematrix/json"},
		{"maps_place_details", map[string]any{"place_id": "p1"}, "/place/details/json"},
		{"maps_place_search", map[string]any{"query": "cafe", "radius": 999999}, "/place/textsearch/json"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.action, func(t *testing.T) {
			server, recorder := newFakeServer(t, mapsHandler)
			registry := testRegistry(t,
				WithBaseURL("google_maps_tool", server.URL),
				WithCredential("google_maps_tool", "test-key"),
				WithHTTPClient(server.Client()),
			)
			input := map[string]any{"action": tc.action}
			for name, value := range tc.input {
				input[name] = value
			}
			result := executeTool(t, registry, "google_maps", input)
			if errText, failed := result["error"]; failed {
				t.Fatalf("unexpected error: %v", errText)
			}
			request := recorder.last(t)
			if request.Path != tc.path {
				t.Fatalf("path = %s, want %s", request.Path, tc.path)
			}
			if got := request.Query.Get("key"); got != "test-key" {
				t.Fatalf("key = %q", got)
			}
			switch tc.action {
			case "maps_geocode":
				if result["query"] != "1600 Amphitheatre Parkway" || result["total"] != 1 {
					t.Fatalf("result = %v", result)
				}
				item := objectValue(t, arrayValue(t, result["results"])[0])
				if item["formatted_address"] != "1600 Amphitheatre Parkway, Mountain View, CA" {
					t.Fatalf("item = %v", item)
				}
			case "maps_reverse_geocode":
				if got := request.Query.Get("latlng"); got != "40.714224,-73.961452" {
					t.Fatalf("latlng = %q", got)
				}
				coordinates := objectValue(t, result["coordinates"])
				if coordinates["lat"] != 40.714224 || coordinates["lng"] != -73.961452 {
					t.Fatalf("coordinates = %v", coordinates)
				}
			case "maps_directions":
				routes := arrayValue(t, result["routes"])
				route := objectValue(t, routes[0])
				if route["summary"] != "US-101" || route["overview_polyline"] != "abc" {
					t.Fatalf("route = %v", route)
				}
				leg := objectValue(t, arrayValue(t, route["legs"])[0])
				if _, present := leg["duration_in_traffic"]; !present {
					t.Fatalf("duration_in_traffic key missing: %v", leg)
				}
				step := objectValue(t, arrayValue(t, leg["steps"])[0])
				if step["instruction"] != "Head north" || step["travel_mode"] != "DRIVING" {
					t.Fatalf("step = %v", step)
				}
			case "maps_distance_matrix":
				rows := arrayValue(t, result["rows"])
				row := objectValue(t, rows[0])
				element := objectValue(t, arrayValue(t, row["elements"])[0])
				if _, present := element["duration_in_traffic"]; !present {
					t.Fatalf("duration_in_traffic key missing: %v", element)
				}
				if len(arrayValue(t, result["origin_addresses"])) != 1 {
					t.Fatalf("origin_addresses = %v", result["origin_addresses"])
				}
			case "maps_place_details":
				place := objectValue(t, result["result"])
				if place["name"] != "Test Place" || result["place_id"] != "p1" {
					t.Fatalf("result = %v", result)
				}
			case "maps_place_search":
				if got := request.Query.Get("radius"); got != "50000" {
					t.Fatalf("radius = %q, want 50000", got)
				}
				place := objectValue(t, arrayValue(t, result["results"])[0])
				if place["open_now"] != true || result["next_page_token"] != "next-1" {
					t.Fatalf("place = %v result = %v", place, result)
				}
			}
		})
	}

	// Upstream builds latlng with Python float formatting; integral values
	// keep their ".0" suffix.
	t.Run("reverse geocode integral coordinates", func(t *testing.T) {
		server, recorder := newFakeServer(t, mapsHandler)
		registry := testRegistry(t,
			WithBaseURL("google_maps_tool", server.URL),
			WithCredential("google_maps_tool", "test-key"),
			WithHTTPClient(server.Client()),
		)
		input := map[string]any{"action": "maps_reverse_geocode", "latitude": 1.0, "longitude": 2.0}
		result := executeTool(t, registry, "google_maps", input)
		if errText, failed := result["error"]; failed {
			t.Fatalf("unexpected error: %v", errText)
		}
		if got := recorder.last(t).Query.Get("latlng"); got != "1.0,2.0" {
			t.Fatalf("latlng = %q, want 1.0,2.0", got)
		}
	})
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_http_error
func TestGoogleMapsStatusErrors(t *testing.T) {
	inputs := map[string]map[string]any{
		"maps_geocode":         {"address": "x"},
		"maps_reverse_geocode": {"latitude": 1.0, "longitude": 2.0},
		"maps_directions":      {"origin": "A", "destination": "B"},
		"maps_distance_matrix": {"origins": "A", "destinations": "B"},
		"maps_place_details":   {"place_id": "p1"},
		"maps_place_search":    {"query": "cafe"},
	}
	statuses := []struct {
		code int
		text string
	}{
		{http.StatusUnauthorized, "Unauthorized"},
		{http.StatusNotFound, "Not Found"},
		{http.StatusTooManyRequests, "Too Many Requests"},
	}
	for _, status := range statuses {
		status := status
		t.Run(fmt.Sprintf("%d", status.code), func(t *testing.T) {
			server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(status.code)
				_, _ = w.Write([]byte(status.text))
			})
			registry := testRegistry(t,
				WithBaseURL("google_maps_tool", server.URL),
				WithCredential("google_maps_tool", "test-key"),
				WithHTTPClient(server.Client()),
			)
			for action, fields := range inputs {
				action, fields := action, fields
				t.Run(action, func(t *testing.T) {
					input := map[string]any{"action": action}
					for name, value := range fields {
						input[name] = value
					}
					result := executeTool(t, registry, "google_maps", input)
					want := fmt.Sprintf("HTTP %d: %s", status.code, status.text)
					if got := errorText(t, result); got != want {
						t.Fatalf("error = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_geocode_request_denied
func TestGoogleMapsAPIStatus(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			"request denied",
			map[string]any{"status": "REQUEST_DENIED"},
			"Request denied — check that the API is enabled and the key is valid",
		},
		{
			"request denied with message",
			map[string]any{"status": "REQUEST_DENIED", "error_message": "bad key"},
			"Request denied — check that the API is enabled and the key is valid. bad key",
		},
		{
			"over query limit",
			map[string]any{"status": "OVER_QUERY_LIMIT"},
			"Too many requests. Try again later",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				writeJSONResponse(w, http.StatusOK, tc.payload)
			})
			registry := testRegistry(t,
				WithBaseURL("google_maps_tool", server.URL),
				WithCredential("google_maps_tool", "test-key"),
				WithHTTPClient(server.Client()),
			)
			result := executeTool(t, registry, "google_maps", map[string]any{
				"action": "maps_geocode", "address": "x",
			})
			if got := errorText(t, result); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("zero results", func(t *testing.T) {
		server, _ := newFakeServer(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			writeJSONResponse(w, http.StatusOK, map[string]any{"status": "ZERO_RESULTS", "results": []any{}})
		})
		registry := testRegistry(t,
			WithBaseURL("google_maps_tool", server.URL),
			WithCredential("google_maps_tool", "test-key"),
			WithHTTPClient(server.Client()),
		)
		result := executeTool(t, registry, "google_maps", map[string]any{
			"action": "maps_geocode", "address": "nowhere",
		})
		if result["total"] != 0 {
			t.Fatalf("total = %v, want 0 (%v)", result["total"], result)
		}
	})
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_geocode_no_address_or_components
func TestGoogleMapsValidation(t *testing.T) {
	registry := testRegistry(t,
		WithBaseURL("google_maps_tool", "http://127.0.0.1:1"),
		WithCredential("google_maps_tool", "test-key"),
	)
	cases := []struct {
		name   string
		action string
		input  map[string]any
		want   string
	}{
		{"geocode empty", "maps_geocode", map[string]any{}, "Either address or components is required"},
		{"reverse missing latitude", "maps_reverse_geocode", map[string]any{}, "latitude is required"},
		{"reverse missing longitude", "maps_reverse_geocode", map[string]any{"latitude": 0}, "longitude is required"},
		{"reverse latitude range", "maps_reverse_geocode", map[string]any{"latitude": 91, "longitude": 0}, "Latitude must be between -90 and 90"},
		{"reverse longitude range", "maps_reverse_geocode", map[string]any{"latitude": 0, "longitude": -181}, "Longitude must be between -180 and 180"},
		{"directions missing origin", "maps_directions", map[string]any{}, "Origin is required"},
		{"directions missing destination", "maps_directions", map[string]any{"origin": "A"}, "Destination is required"},
		{"distance missing origins", "maps_distance_matrix", map[string]any{}, "Origins is required"},
		{"distance missing destinations", "maps_distance_matrix", map[string]any{"origins": "A"}, "Destinations is required"},
		{"details missing place id", "maps_place_details", map[string]any{}, "place_id is required"},
		{"search missing query", "maps_place_search", map[string]any{}, "Query or page_token is required"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			input := map[string]any{"action": tc.action}
			for name, value := range tc.input {
				input[name] = value
			}
			result := executeTool(t, registry, "google_maps", input)
			if got := errorText(t, result); got != tc.want {
				t.Fatalf("error = %q, want %q", got, tc.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_geocode_no_credentials_returns_error
func TestGoogleMapsCredentials(t *testing.T) {
	inputs := map[string]map[string]any{
		"maps_geocode":         {"address": "x"},
		"maps_reverse_geocode": {"latitude": 1.0, "longitude": 2.0},
		"maps_directions":      {"origin": "A", "destination": "B"},
		"maps_distance_matrix": {"origins": "A", "destinations": "B"},
		"maps_place_details":   {"place_id": "p1"},
		"maps_place_search":    {"query": "cafe"},
	}
	registry := testRegistry(t)
	for action, fields := range inputs {
		action, fields := action, fields
		t.Run(action, func(t *testing.T) {
			input := map[string]any{"action": action}
			for name, value := range fields {
				input[name] = value
			}
			result := executeTool(t, registry, "google_maps", input)
			if got := errorText(t, result); got != "Google Maps API key not configured" {
				t.Fatalf("error = %q", got)
			}
			help, _ := result["help"].(string)
			if !strings.Contains(help, "GOOGLE_MAPS_API_KEY") {
				t.Fatalf("help = %q", help)
			}
		})
	}
}

// Parity: tools/tests/tools/test_google_maps_tool.py::test_timeout_error
func TestGoogleMapsTimeout(t *testing.T) {
	registry := testRegistry(t,
		WithBaseURL("google_maps_tool", "http://example.invalid"),
		WithCredential("google_maps_tool", "test-key"),
		WithHTTPClient(&http.Client{Transport: timeoutTransport{}}),
	)
	result := executeTool(t, registry, "google_maps", map[string]any{
		"action": "maps_geocode", "address": "x",
	})
	if got := errorText(t, result); got != "Request timed out" {
		t.Fatalf("error = %q", got)
	}
}
