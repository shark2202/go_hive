package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch6Lane3LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch6Lane3Recorder struct {
	mu       sync.Mutex
	requests []batch6Lane3LoggedRequest
}

func (rec *batch6Lane3Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch6Lane3LoggedRequest{
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

func (rec *batch6Lane3Recorder) last(t *testing.T) batch6Lane3LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch6Lane3Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch6Lane3TestCredentials seeds one module credential pair per tool; the
// helpers split on "|" exactly like the production credential path.
var batch6Lane3TestCredentials = map[string]string{
	"shopify_tool":    "test-token|test-store",
	"quickbooks_tool": "test-token|123456789",
	"plaid_tool":      "test-client|test-secret",
	"razorpay_tool":   "rzp_test_key|test-secret",
}

// batch6Lane3TestRegistry builds a lane-3 registry pointed at one fake
// upstream, with an empty environment so ambient variables cannot leak in.
func batch6Lane3TestRegistry(t *testing.T, module string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch6Lane3Recorder) {
	t.Helper()
	recorder := &batch6Lane3Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, batch6Lane3TestCredentials[module]),
		WithGetenv(func(string) string { return "" }),
	}
	return newBatch6Lane3Registry(append(base, opts...)...), recorder
}

func batch6Lane3Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

func batch6Lane3WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch6Lane3ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch6Lane3BodyMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map", value)
	}
	return object
}

func batch6Lane3Slice(t *testing.T, value any) []any {
	t.Helper()
	switch items := value.(type) {
	case []any:
		return items
	case []map[string]any:
		out := make([]any, len(items))
		for index, item := range items {
			out[index] = item
		}
		return out
	case []string:
		out := make([]any, len(items))
		for index, item := range items {
			out[index] = item
		}
		return out
	default:
		t.Fatalf("value = %T, want array", value)
		return nil
	}
}

type batch6Lane3TimeoutError struct{}

func (batch6Lane3TimeoutError) Error() string   { return "i/o timeout" }
func (batch6Lane3TimeoutError) Timeout() bool   { return true }
func (batch6Lane3TimeoutError) Temporary() bool { return true }

type batch6Lane3TimeoutTransport struct{}

func (batch6Lane3TimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, batch6Lane3TimeoutError{}
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise the MCP tool functions, not the registry surface.
func TestBatch6Lane3CatalogIntegrity(t *testing.T) {
	registry := newBatch6Lane3Registry()
	if got := len(registry.Names()); got != 4 {
		t.Fatalf("tools = %d, want 4 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 29 {
		t.Fatalf("actions = %d, want 29", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	wantActions := map[string]int{
		"shopify":    9,
		"quickbooks": 8,
		"plaid":      6,
		"razorpay":   6,
	}
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
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch6Lane3(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch6Lane3(shared); installed != 4 {
		t.Fatalf("installed = %d, want 4", installed)
	}
	for _, name := range []string{"shopify", "quickbooks", "plaid", "razorpay"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	for name, want := range map[string]int{
		"shopify": 9, "quickbooks": 8, "plaid": 6, "razorpay": 6,
	} {
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
