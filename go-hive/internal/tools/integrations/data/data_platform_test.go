package data

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// batch6Lane4TestRegistry builds a lane-4 registry pointed at one fake
// upstream, with environment injection for multi-field credentials.
func batch6Lane4TestRegistry(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	options := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
	}
	return NewPlatformRegistry(append(options, opts...)...), recorder
}

// batch6Lane4TokenRegistry is the single-token variant: WithCredential supplies
// the bearer token, env supplies everything else.
func batch6Lane4TokenRegistry(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*Registry, *batch4Recorder) {
	t.Helper()
	return batch6Lane4TestRegistry(t, module, env, handler, WithCredential(module, "test-token"))
}

// Parity-exempt: registry/catalog invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not this registry surface.
func TestBatch6Lane4Catalog(t *testing.T) {
	registry := NewPlatformRegistry()
	names := registry.Names()
	wantNames := []string{"snowflake", "bigquery", "databricks", "redshift", "azure_sql", "powerbi", "sap"}
	if len(names) != len(wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	for index, name := range wantNames {
		if names[index] != name {
			t.Fatalf("tools = %v, want %v", names, wantNames)
		}
	}
	if got := registry.ActionCount(); got != 33 {
		t.Fatalf("actions = %d, want 33", got)
	}
	if got := registry.ImplementedActions(); got != 33 {
		t.Fatalf("implemented = %d, want 33", got)
	}
	for _, tool := range registry.Tools() {
		for _, action := range tool.Actions {
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
}
