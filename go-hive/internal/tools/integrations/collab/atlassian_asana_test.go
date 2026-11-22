package collab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch5Lane1LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch5Lane1Recorder struct {
	mu       sync.Mutex
	requests []batch5Lane1LoggedRequest
}

func (rec *batch5Lane1Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch5Lane1LoggedRequest{
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

func (rec *batch5Lane1Recorder) last(t *testing.T) batch5Lane1LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch5Lane1Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch5Lane1TestRegistry builds a batch-5 lane-1 registry pointed at one fake
// upstream, with a token and per-test environment overrides.
func batch5Lane1TestRegistry(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch5Lane1Recorder) {
	t.Helper()
	recorder := &batch5Lane1Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
		WithGetenv(func(name string) string { return env[name] }),
	}
	return NewAtlassianRegistry(append(base, opts...)...), recorder
}

func batch5Lane1Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

func batch5Lane1WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch5Lane1ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch5Lane1MapField(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("%q = %T, want map (%v)", key, object[key], object)
	}
	return value
}

func batch5Lane1SliceField(t *testing.T, object map[string]any, key string) []any {
	t.Helper()
	value := reflect.ValueOf(object[key])
	if value.Kind() != reflect.Slice {
		t.Fatalf("%q = %T, want slice (%v)", key, object[key], object)
	}
	items := make([]any, value.Len())
	for index := 0; index < value.Len(); index++ {
		items[index] = value.Index(index).Interface()
	}
	return items
}

// ------------------------------------------------------------ registry ---

// Parity-exempt: registry/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not a registry surface.
func TestBatch5Lane1CatalogAndInstall(t *testing.T) {
	registry := NewAtlassianRegistry()
	if got := len(registry.Names()); got != 3 {
		t.Fatalf("tools = %d, want 3", got)
	}
	if got := registry.ActionCount(); got != 26 {
		t.Fatalf("actions = %d, want 26", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	wantTools := []string{"jira", "confluence", "asana"}
	for index, name := range wantTools {
		if registry.Names()[index] != name {
			t.Fatalf("tool order = %v, want %v", registry.Names(), wantTools)
		}
	}
	wantCounts := map[string]int{"jira": 9, "confluence": 8, "asana": 9}
	for name, want := range wantCounts {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
	}

	shared := tools.NewToolRegistry()
	if installed := NewAtlassianRegistry().Install(shared); installed != 3 {
		t.Fatalf("installed = %d, want 3", installed)
	}
	for _, name := range wantTools {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	jira, ok := shared.GetTool("jira")
	if !ok {
		t.Fatal("jira tool missing")
	}
	properties, ok := jira.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("jira properties = %v", jira.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("jira missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 9 {
		t.Fatalf("jira action enum = %v", action["enum"])
	}
	if _, ok := properties["jql"]; !ok {
		t.Fatalf("jira schema missing jql: %v", properties)
	}
	confluence, _ := shared.GetTool("confluence")
	confluenceProps := confluence.Parameters["properties"].(map[string]any)
	if _, ok := confluenceProps["version_number"]; !ok {
		t.Fatalf("confluence schema missing version_number: %v", confluenceProps)
	}
	asana, _ := shared.GetTool("asana")
	asanaProps := asana.Parameters["properties"].(map[string]any)
	if _, ok := asanaProps["completed"]; !ok {
		t.Fatalf("asana schema missing completed: %v", asanaProps)
	}
}

// Parity-exempt: unknown action behaviour is a Go registry contract; upstream
// has per-function MCP tools rather than a dispatch selector.
func TestBatch5Lane1UnknownAction(t *testing.T) {
	registry := NewAtlassianRegistry()
	if _, err := registry.Execute(context.Background(), "jira", map[string]any{"action": "jira_nope"}); err == nil {
		t.Fatal("unknown jira action returned no error")
	}
	if _, err := registry.Execute(context.Background(), "nope", map[string]any{}); err == nil {
		t.Fatal("unknown tool returned no error")
	}
	if _, err := registry.Execute(context.Background(), "jira", map[string]any{}); err != nil {
		t.Fatalf("missing action selector should be a value, got %v", err)
	}
}

// batch5Lane1RequestSummary renders one recorded request for failure messages.
func batch5Lane1RequestSummary(request batch5Lane1LoggedRequest) string {
	return fmt.Sprintf("%s %s?%s body=%v", request.Method, request.Path, request.Query.Encode(), request.Body)
}
