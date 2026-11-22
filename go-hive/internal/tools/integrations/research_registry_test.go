package integrations

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// batch2TestRegistry builds a batch2 registry whose modules all point at one
// fake upstream, with optional scanner dependencies injected.
func batch2TestRegistry(t *testing.T, server *httptest.Server, opts ...batch2Option) *Registry {
	t.Helper()
	registry := newBatch2Registry(opts...)
	for _, cfg := range registry.Runtime().Modules {
		cfg.BaseURL = server.URL
	}
	registry.Runtime().HTTP = newHTTPClient(server.Client())
	return registry
}

// batch2Execute runs one action and asserts it returned a map without a Go
// error.
func batch2Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise the MCP tool functions, not the registry surface.
func TestBatch2CatalogIntegrity(t *testing.T) {
	registry := newBatch2Registry()
	if got := len(registry.Names()); got != 10 {
		t.Fatalf("tools = %d, want 10", got)
	}
	if got := registry.ActionCount(); got != 19 {
		t.Fatalf("actions = %d, want 19", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	wantNames := []string{
		"arxiv", "duckduckgo", "search_wikipedia", "openmeteo", "yahoo_finance",
		"youtube_transcript", "dns_security_scan", "http_headers_scan", "ssl_tls_scan", "port_scan",
	}
	seen := map[string]bool{}
	for _, name := range registry.Names() {
		seen[name] = true
	}
	for _, name := range wantNames {
		if !seen[name] {
			t.Fatalf("missing tool %q (have %v)", name, registry.Names())
		}
	}
	for _, tool := range registry.Tools() {
		if tool.ID == "" || tool.Summary == "" || len(tool.Actions) == 0 {
			t.Fatalf("tool missing identity: %+v", tool)
		}
		for _, action := range tool.Actions {
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", tool.Name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch2(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch2(shared); installed != 10 {
		t.Fatalf("installed = %d, want 10", installed)
	}
	for _, name := range []string{"arxiv", "duckduckgo", "search_wikipedia", "openmeteo", "yahoo_finance", "youtube_transcript", "dns_security_scan", "http_headers_scan", "ssl_tls_scan", "port_scan"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("arxiv")
	if !ok {
		t.Fatal("arxiv tool missing")
	}
	schema := tool.Parameters
	if schema["type"] != "object" {
		t.Fatalf("schema = %v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", schema["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("multi-action tool missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 2 {
		t.Fatalf("action enum = %v", action["enum"])
	}
	single, _ := shared.GetTool("port_scan")
	if _, present := single.Parameters["properties"].(map[string]any)["action"]; present {
		t.Fatalf("single-action tool should not expose action selector: %v", single.Parameters)
	}
}

// Parity-exempt: HTTP transport plumbing; upstream tests mock the MCP client.
func TestBatch2RegistryIsolation(t *testing.T) {
	shared := tools.NewToolRegistry()
	InstallBatch2(shared)
	if names := shared.RegisteredNames(); len(names) != 10 {
		t.Fatalf("registered = %v", names)
	}
	registry := newBatch2Registry()
	if _, err := registry.Execute(context.Background(), "get_current_time", nil); err == nil {
		t.Fatal("batch2 registry must not expose batch1 tools")
	}
	if _, err := registry.Execute(context.Background(), "arxiv", map[string]any{"action": "nope"}); !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("err = %v", err)
	}
}
