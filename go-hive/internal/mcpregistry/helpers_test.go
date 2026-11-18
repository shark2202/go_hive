package mcpregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBase(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "mcp_registry")
}

func newRegistryAt(t *testing.T, base string) *Registry {
	t.Helper()
	registry := NewAtBase(base)
	if err := registry.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return registry
}

func newDefaultRegistry(t *testing.T) *Registry {
	t.Helper()
	return newRegistryAt(t, testBase(t))
}

func writeMockIndex(t *testing.T, registry *Registry, servers map[string]any) {
	t.Helper()
	writeJSONAt(t, filepath.Join(registry.CacheDir(), "registry_index.json"), map[string]any{
		"servers": servers,
	})
}

func writeJSONAt(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func mustAddLocal(t *testing.T, registry *Registry, name string, opts AddLocalOptions) {
	t.Helper()
	if err := registry.AddLocal(name, opts); err != nil {
		t.Fatalf("AddLocal(%s): %v", name, err)
	}
}

func setupRegistryWithServers(t *testing.T) *Registry {
	t.Helper()
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "jira", AddLocalOptions{Transport: "stdio", Command: "uvx", Args: []string{"jira-mcp"}})
	mustAddLocal(t, registry, "slack", AddLocalOptions{Transport: "http", URL: "http://localhost:4020"})
	mustAddLocal(t, registry, "github", AddLocalOptions{Transport: "http", URL: "http://localhost:4030"})

	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	servers := asMap(data["servers"])
	manifest := func(name string) map[string]any { return asMap(asMap(servers[name])["manifest"]) }
	manifest("jira")["tags"] = []any{"productivity", "pm"}
	manifest("slack")["tags"] = []any{"productivity", "messaging"}
	manifest("github")["tags"] = []any{"dev"}
	manifest("jira")["hive"] = map[string]any{"profiles": []any{"productivity"}}
	manifest("slack")["hive"] = map[string]any{"profiles": []any{"productivity"}}
	manifest("github")["hive"] = map[string]any{"profiles": []any{"developer"}}
	if err := registry.WriteInstalled(data); err != nil {
		t.Fatalf("WriteInstalled: %v", err)
	}
	return registry
}

func installedEntry(t *testing.T, registry *Registry, name string) map[string]any {
	t.Helper()
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	entry := asMap(asMap(data["servers"])[name])
	if entry == nil {
		t.Fatalf("server %q not installed", name)
	}
	return entry
}

func requireErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected error containing %q, got %q", want, err.Error())
	}
}

func names(configs []*ServerConfig) []string {
	out := make([]string, 0, len(configs))
	for _, config := range configs {
		out = append(out, config.Name)
	}
	return out
}

func containsName(configs []*ServerConfig, want string) bool {
	return contains(names(configs), want)
}
