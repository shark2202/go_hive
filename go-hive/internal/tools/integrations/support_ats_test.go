package integrations

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

type batch6Lane2Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch6Lane2Recorder struct {
	mu       sync.Mutex
	requests []batch6Lane2Request
}

func (rec *batch6Lane2Recorder) wrap(
	next func(http.ResponseWriter, *http.Request, map[string]any),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch6Lane2Request{
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

func (rec *batch6Lane2Recorder) at(t *testing.T, index int) batch6Lane2Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch6Lane2Recorder) last(t *testing.T) batch6Lane2Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch6Lane2Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch6Lane2TestRegistry builds a lane-2 registry pointed at one fake
// upstream, with per-module env values for multi-part credentials.
func batch6Lane2TestRegistry(
	t *testing.T,
	module string,
	env map[string]string,
	handler func(http.ResponseWriter, *http.Request, map[string]any),
) (*Registry, *batch6Lane2Recorder) {
	t.Helper()
	recorder := &batch6Lane2Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	opts := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
		WithGetenv(func(name string) string { return env[name] }),
	}
	return newBatch6Lane2Registry(opts...), recorder
}

func batch6Lane2Execute(
	t *testing.T,
	registry *Registry,
	tool string,
	input map[string]any,
) map[string]any {
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

func batch6Lane2WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch6Lane2ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch6Lane2FieldMap(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := source[name].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %T, want map (%v)", name, source[name], source)
	}
	return value
}

func batch6Lane2FieldSlice(t *testing.T, source map[string]any, name string) []any {
	t.Helper()
	value, present := source[name]
	if !present || value == nil {
		t.Fatalf("field %q missing (%v)", name, source)
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		t.Fatalf("field %q = %T, want array (%v)", name, value, source)
	}
	out := make([]any, 0, rv.Len())
	for index := 0; index < rv.Len(); index++ {
		out = append(out, rv.Index(index).Interface())
	}
	return out
}

func batch6Lane2Number(t *testing.T, value any) int64 {
	t.Helper()
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		t.Fatalf("value = %T, want a number", value)
		return 0
	}
}

func batch6Lane2EqualInt(t *testing.T, value any, want int) {
	t.Helper()
	if got := batch6Lane2Number(t, value); got != int64(want) {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not the registry surface.
func TestBatch6Lane2CatalogIntegrity(t *testing.T) {
	registry := newBatch6Lane2Registry()
	if got := len(registry.Names()); got != 4 {
		t.Fatalf("tools = %d, want 4 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 45 {
		t.Fatalf("actions = %d, want 45", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	expected := map[string]int{
		"intercom":   11,
		"zendesk":    8,
		"freshdesk":  17,
		"greenhouse": 9,
	}
	for name, want := range expected {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" || tool.DisplayName == "" {
			t.Fatalf("%s missing identity: %+v", name, tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.impls[name][action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch6Lane2(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch6Lane2(shared); installed != 4 {
		t.Fatalf("installed = %d, want 4", installed)
	}
	for _, name := range []string{"intercom", "zendesk", "freshdesk", "greenhouse"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("freshdesk")
	if !ok {
		t.Fatal("freshdesk tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("freshdesk missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 17 {
		t.Fatalf("freshdesk action enum = %v", action["enum"])
	}
	if tool.Description == "" {
		t.Fatal("freshdesk empty description")
	}
}

// Parity-exempt: schema shape is a Go-side contract with tools.ToolRegistry.
func TestBatch6Lane2SchemaShape(t *testing.T) {
	registry := newBatch6Lane2Registry()
	for _, tool := range registry.Tools() {
		schema := tool.Schema()
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", tool.Name, schema["type"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing", tool.Name)
		}
		action, ok := properties["action"].(map[string]any)
		if !ok {
			t.Fatalf("%s action property missing", tool.Name)
		}
		enum, ok := action["enum"].([]string)
		if !ok || len(enum) != len(tool.Actions) {
			t.Fatalf("%s action enum = %v", tool.Name, action["enum"])
		}
		if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "action" {
			t.Fatalf("%s required = %v", tool.Name, schema["required"])
		}
	}
}

// Parity-exempt: unknown-action dispatch is a Go-side registry contract.
func TestBatch6Lane2UnknownAction(t *testing.T) {
	registry := newBatch6Lane2Registry()
	_, err := registry.Execute(context.Background(), "intercom", map[string]any{"action": "nope"})
	if err == nil {
		t.Fatal("expected unknown action error")
	}
}

// batch6Lane2StatusHandler answers every request with one status and body.
func batch6Lane2StatusHandler(status int, body any) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch6Lane2WriteJSON(w, status, body)
	}
}

// batch6Lane2ErrorTable runs one action against each failing status.
func batch6Lane2ErrorTable(
	t *testing.T,
	module, tool string,
	env map[string]string,
	input map[string]any,
	cases map[int]string,
) {
	t.Helper()
	for status, want := range cases {
		status, want := status, want
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			registry, _ := batch6Lane2TestRegistry(
				t, module, env,
				batch6Lane2StatusHandler(status, map[string]any{"error": "provider failure"}),
			)
			result := batch6Lane2Execute(t, registry, tool, input)
			if got := batch6Lane2ErrorText(t, result); got != want {
				t.Fatalf("error = %q, want %q", got, want)
			}
		})
	}
}
