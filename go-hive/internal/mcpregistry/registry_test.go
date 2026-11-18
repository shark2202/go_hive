package mcpregistry

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Parity: core/tests/test_mcp_registry.py::test_init_creates_directory_structure
func TestInitCreatesDirectoryStructure(t *testing.T) {
	base := testBase(t)
	newRegistryAt(t, base)
	for _, path := range []string{
		filepath.Join(base, "config.json"),
		filepath.Join(base, "installed.json"),
		filepath.Join(base, "cache"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}
}

// Parity: core/tests/test_mcp_registry.py::test_init_default_config
func TestInitDefaultConfig(t *testing.T) {
	base := testBase(t)
	newRegistryAt(t, base)
	raw, err := os.ReadFile(filepath.Join(base, "config.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := config["index_url"]; !ok {
		t.Fatalf("config missing index_url: %v", config)
	}
	if _, ok := config["refresh_interval_hours"]; !ok {
		t.Fatalf("config missing refresh_interval_hours: %v", config)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_init_preserves_existing
func TestInitPreservesExisting(t *testing.T) {
	base := testBase(t)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeJSONAt(t, filepath.Join(base, "installed.json"), map[string]any{
		"servers": map[string]any{"jira": map[string]any{"enabled": true}},
	})
	registry := newRegistryAt(t, base)
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	if _, ok := asMap(data["servers"])["jira"]; !ok {
		t.Fatalf("initialize overwrote existing entry: %v", data)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_http
func TestAddLocalHTTP(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "my-db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	entry := installedEntry(t, registry, "my-db")
	if entry["source"] != "local" || entry["transport"] != "http" || entry["enabled"] != true {
		t.Fatalf("unexpected entry: %v", entry)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_duplicate_raises
func TestAddLocalDuplicateRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "my-db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	err := registry.AddLocal("my-db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	requireErrContains(t, err, "already exists")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_stdio
func TestAddLocalStdio(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "tools", AddLocalOptions{
		Transport:   "stdio",
		Command:     "uvx",
		Args:        []string{"my-mcp"},
		Cwd:         "/opt/tools",
		Description: "My tools",
	})
	entry := installedEntry(t, registry, "tools")
	manifest := asMap(entry["manifest"])
	stdio := asMap(manifest["stdio"])
	if stdio["command"] != "uvx" {
		t.Fatalf("command = %v", stdio["command"])
	}
	if stdio["cwd"] != "/opt/tools" {
		t.Fatalf("cwd = %v", stdio["cwd"])
	}
	if manifest["description"] != "My tools" {
		t.Fatalf("description = %v", manifest["description"])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_unix
func TestAddLocalUnix(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "unix", SocketPath: "/tmp/mcp.sock"})
	entry := installedEntry(t, registry, "db")
	if entry["transport"] != "unix" {
		t.Fatalf("transport = %v", entry["transport"])
	}
	if got := asMap(asMap(entry["manifest"])["unix"])["socket_path"]; got != "/tmp/mcp.sock" {
		t.Fatalf("socket_path = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_sse
func TestAddLocalSSE(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "stream", AddLocalOptions{Transport: "sse", URL: "http://localhost:8080/sse"})
	if got := installedEntry(t, registry, "stream")["transport"]; got != "sse" {
		t.Fatalf("transport = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_stdio_requires_command
func TestAddLocalStdioRequiresCommand(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("x", AddLocalOptions{Transport: "stdio"}), "command is required")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_http_requires_url
func TestAddLocalHTTPRequiresURL(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("x", AddLocalOptions{Transport: "http"}), "url is required")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_unix_requires_socket_path
func TestAddLocalUnixRequiresSocketPath(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("x", AddLocalOptions{Transport: "unix"}), "socket_path is required")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_unsupported_transport
func TestAddLocalUnsupportedTransport(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("x", AddLocalOptions{Transport: "grpc"}), "Unsupported transport")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_with_inline_manifest
func TestAddLocalWithInlineManifest(t *testing.T) {
	registry := newDefaultRegistry(t)
	manifest := map[string]any{
		"description": "Custom Jira server",
		"transport":   map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
		"stdio":       map[string]any{"command": "uvx", "args": []any{"jira-mcp"}, "env": map[string]any{"TOKEN": "abc"}},
		"tags":        []any{"pm"},
	}
	if err := registry.AddLocal("jira", AddLocalOptions{Manifest: manifest}); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}
	entry := installedEntry(t, registry, "jira")
	stored := asMap(entry["manifest"])
	if entry["source"] != "local" || entry["transport"] != "stdio" {
		t.Fatalf("entry = %v", entry)
	}
	if got := asMap(stored["stdio"])["command"]; got != "uvx" {
		t.Fatalf("command = %v", got)
	}
	if got := asStringSlice(stored["tags"]); len(got) != 1 || got[0] != "pm" {
		t.Fatalf("tags = %v", stored["tags"])
	}
	if stored["name"] != "jira" {
		t.Fatalf("name = %v", stored["name"])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_manifest_without_transport_defaults
func TestAddLocalManifestWithoutTransportDefaults(t *testing.T) {
	registry := newDefaultRegistry(t)
	if err := registry.AddLocal("simple", AddLocalOptions{
		Manifest: map[string]any{"stdio": map[string]any{"command": "echo"}},
	}); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}
	if got := installedEntry(t, registry, "simple")["transport"]; got != "stdio" {
		t.Fatalf("transport = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_requires_transport_without_manifest
func TestAddLocalRequiresTransportWithoutManifest(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("broken", AddLocalOptions{}), "transport is required")
}

// Parity: core/tests/test_mcp_registry.py::test_add_local_sse_requires_url
func TestAddLocalSSERequiresURL(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.AddLocal("x", AddLocalOptions{Transport: "sse"}), "url is required")
}

// Parity: core/tests/test_mcp_registry.py::test_install_from_index
func TestInstallFromIndex(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.2.0",
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
			"stdio":     map[string]any{"command": "uvx", "args": []any{"jira-mcp-server"}},
		},
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	entry := installedEntry(t, registry, "jira")
	manifest := asMap(entry["manifest"])
	if entry["source"] != "registry" || entry["transport"] != "stdio" {
		t.Fatalf("entry = %v", entry)
	}
	if manifest["name"] != "jira" || manifest["version"] != "1.2.0" {
		t.Fatalf("manifest = %v", manifest)
	}
	if got := asMap(manifest["stdio"])["command"]; got != "uvx" {
		t.Fatalf("command = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_install_not_found_raises
func TestInstallNotFoundRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{})
	requireErrContains(t, registry.Install("nonexistent", InstallOptions{}), "not found")
}

// Parity: core/tests/test_mcp_registry.py::test_install_duplicate_raises
func TestInstallDuplicateRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.0.0",
			"transport": map[string]any{"default": "stdio"},
			"stdio":     map[string]any{"command": "uvx"},
		},
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	requireErrContains(t, registry.Install("jira", InstallOptions{}), "already exists")
}

// Parity: core/tests/test_mcp_registry.py::test_install_version_pin
func TestInstallVersionPin(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"slack": map[string]any{"name": "slack", "version": "2.0.0", "transport": map[string]any{"default": "http"}},
	})
	if err := registry.Install("slack", InstallOptions{Version: "2.0.0"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := installedEntry(t, registry, "slack")["pinned"]; got != true {
		t.Fatalf("pinned = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_install_version_mismatch_raises
func TestInstallVersionMismatchRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{"name": "jira", "version": "1.2.0", "transport": map[string]any{"default": "stdio"}},
	})
	requireErrContains(t, registry.Install("jira", InstallOptions{Version: "2.0.0"}), "Version mismatch")
}

// Parity: core/tests/test_mcp_registry.py::test_install_version_pin_no_version_in_manifest
func TestInstallVersionPinNoVersionInManifest(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"noversion": map[string]any{"name": "noversion", "transport": map[string]any{"default": "stdio"}},
	})
	requireErrContains(t, registry.Install("noversion", InstallOptions{Version: "1.0.0"}), "no version field")
}

// Parity: core/tests/test_mcp_registry.py::test_install_with_transport_override
func TestInstallWithTransportOverride(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.0.0",
			"transport": map[string]any{"supported": []any{"stdio", "http"}, "default": "stdio"},
			"stdio":     map[string]any{"command": "uvx"},
			"http":      map[string]any{"url": "http://localhost:4010"},
		},
	})
	if err := registry.Install("jira", InstallOptions{Transport: "http"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := installedEntry(t, registry, "jira")["transport"]; got != "http" {
		t.Fatalf("transport = %v", got)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || configs[0].Transport != "http" || deref(configs[0].URL) != "http://localhost:4010" {
		t.Fatalf("configs = %+v", configs)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_install_with_unsupported_transport_raises
func TestInstallWithUnsupportedTransportRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.0.0",
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
		},
	})
	requireErrContains(t, registry.Install("jira", InstallOptions{Transport: "http"}), "not supported")
}

// Parity: core/tests/test_mcp_registry.py::test_install_registry_entry_uses_updated_cached_manifest
func TestInstallRegistryEntryUsesUpdatedCachedManifest(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.0.0",
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
			"stdio":     map[string]any{"command": "uvx", "args": []any{"jira-v1"}},
		},
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.1.0",
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
			"stdio":     map[string]any{"command": "uvx", "args": []any{"jira-v2"}},
		},
	})
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || configs[0].Transport != "stdio" {
		t.Fatalf("configs = %+v", configs)
	}
	if got := configs[0].Args; len(got) != 1 || got[0] != "jira-v2" {
		t.Fatalf("args = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_install_registry_entry_resolves_without_cache
func TestInstallRegistryEntryResolvesWithoutCache(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{
			"name":      "jira",
			"version":   "1.0.0",
			"transport": map[string]any{"supported": []any{"stdio"}, "default": "stdio"},
			"stdio":     map[string]any{"command": "uvx", "args": []any{"jira-mcp"}},
		},
	})
	if err := registry.Install("jira", InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.Remove(filepath.Join(registry.CacheDir(), "registry_index.json")); err != nil {
		t.Fatalf("Remove index: %v", err)
	}
	configs, err := registry.ResolveForAgent(ResolveOptions{Include: []string{"jira"}})
	if err != nil {
		t.Fatalf("ResolveForAgent: %v", err)
	}
	if len(configs) != 1 || configs[0].Transport != "stdio" {
		t.Fatalf("configs = %+v", configs)
	}
	if deref(configs[0].Command) != "uvx" {
		t.Fatalf("command = %v", deref(configs[0].Command))
	}
	if got := configs[0].Args; len(got) != 1 || got[0] != "jira-mcp" {
		t.Fatalf("args = %v", got)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Parity: core/tests/test_mcp_registry.py::test_remove
func TestRemove(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	if err := registry.Remove("db"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	if _, ok := asMap(data["servers"])["db"]; ok {
		t.Fatalf("db still present: %v", data)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_remove_nonexistent_raises
func TestRemoveNonexistentRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.Remove("ghost"), "not installed")
}

// Parity: core/tests/test_mcp_registry.py::test_enable_disable
func TestEnableDisable(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	if err := registry.Disable("db"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if got := installedEntry(t, registry, "db")["enabled"]; got != false {
		t.Fatalf("enabled after disable = %v", got)
	}
	if err := registry.Enable("db"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if got := installedEntry(t, registry, "db")["enabled"]; got != true {
		t.Fatalf("enabled after enable = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_enable_nonexistent_raises
func TestEnableNonexistentRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.Enable("ghost"), "not installed")
}

// Parity: core/tests/test_mcp_registry.py::test_disable_nonexistent_raises
func TestDisableNonexistentRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.Disable("ghost"), "not installed")
}

// Parity: core/tests/test_mcp_registry.py::test_list_installed
func TestListInstalled(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "a", AddLocalOptions{Transport: "http", URL: "http://localhost:1"})
	mustAddLocal(t, registry, "b", AddLocalOptions{Transport: "http", URL: "http://localhost:2"})
	rows, err := registry.ListInstalled()
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[asString(row["name"])] = true
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("names = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_list_installed_fresh_registry
func TestListInstalledFreshRegistry(t *testing.T) {
	registry := newDefaultRegistry(t)
	rows, err := registry.ListInstalled()
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no installed servers, got %v", rows)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_get_server
func TestGetServer(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "jira", AddLocalOptions{Transport: "http", URL: "http://localhost:4010"})
	row, err := registry.GetServer("jira")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if row == nil {
		t.Fatalf("expected jira, got nil")
	}
	missing, err := registry.GetServer("nonexistent")
	if err != nil {
		t.Fatalf("GetServer: %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil, got %v", missing)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_search_by_name
func TestSearchByName(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira":  map[string]any{"name": "jira", "description": "Issues", "tags": []any{}},
		"slack": map[string]any{"name": "slack", "description": "Chat", "tags": []any{}},
	})
	rows, err := registry.Search("jira")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 result, got %d", len(rows))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_search_by_tag
func TestSearchByTag(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira":   map[string]any{"name": "jira", "tags": []any{"pm"}},
		"linear": map[string]any{"name": "linear", "tags": []any{"pm"}},
		"slack":  map[string]any{"name": "slack", "tags": []any{"chat"}},
	})
	rows, err := registry.Search("pm")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 results, got %d", len(rows))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_search_by_description
func TestSearchByDescription(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira":  map[string]any{"name": "jira", "description": "Manage issues", "tags": []any{}},
		"slack": map[string]any{"name": "slack", "description": "Send messages", "tags": []any{}},
	})
	rows, err := registry.Search("issues")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 result, got %d", len(rows))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_search_by_tool_name
func TestSearchByToolName(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira":  map[string]any{"name": "jira", "tags": []any{}, "tools": []any{map[string]any{"name": "create_issue"}}},
		"slack": map[string]any{"name": "slack", "tags": []any{}, "tools": []any{map[string]any{"name": "send_message"}}},
	})
	rows, err := registry.Search("create_issue")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 result, got %d", len(rows))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_search_no_results
func TestSearchNoResults(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"jira": map[string]any{"name": "jira", "description": "Issues", "tags": []any{}},
	})
	rows, err := registry.Search("nonexistent_query_xyz")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no results, got %v", rows)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_list_available
func TestListAvailable(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeMockIndex(t, registry, map[string]any{
		"a": map[string]any{"name": "a"},
		"b": map[string]any{"name": "b"},
	})
	rows, err := registry.ListAvailable()
	if err != nil {
		t.Fatalf("ListAvailable: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2, got %d", len(rows))
	}
}

// Parity: core/tests/test_mcp_registry.py::test_list_available_empty_index
func TestListAvailableEmptyIndex(t *testing.T) {
	registry := newDefaultRegistry(t)
	rows, err := registry.ListAvailable()
	if err != nil {
		t.Fatalf("ListAvailable: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty list, got %v", rows)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_set_override_env
func TestSetOverrideEnv(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "jira", AddLocalOptions{Transport: "http", URL: "http://localhost:4010"})
	if err := registry.SetOverride("jira", "env", "TOKEN", "secret"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	entry := installedEntry(t, registry, "jira")
	if got := asMap(asMap(entry["overrides"])["env"])["TOKEN"]; got != "secret" {
		t.Fatalf("env TOKEN = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_set_override_headers
func TestSetOverrideHeaders(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "jira", AddLocalOptions{Transport: "http", URL: "http://localhost:4010"})
	if err := registry.SetOverride("jira", "headers", "Authorization", "Bearer x"); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	entry := installedEntry(t, registry, "jira")
	if got := asMap(asMap(entry["overrides"])["headers"])["Authorization"]; got != "Bearer x" {
		t.Fatalf("header = %v", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_set_override_invalid_type
func TestSetOverrideInvalidType(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "jira", AddLocalOptions{Transport: "http", URL: "http://localhost:4010"})
	requireErrContains(t, registry.SetOverride("jira", "cookies", "k", "v"), "Invalid override type")
}

// Parity: core/tests/test_mcp_registry.py::test_set_override_nonexistent_raises
func TestSetOverrideNonexistentRaises(t *testing.T) {
	registry := newDefaultRegistry(t)
	requireErrContains(t, registry.SetOverride("ghost", "env", "KEY", "val"), "not installed")
}

// Parity: core/tests/test_mcp_registry.py::test_update_index_writes_cache
func TestUpdateIndexWritesCache(t *testing.T) {
	registry := newDefaultRegistry(t)
	registry.Fetch = func(string, time.Duration) ([]byte, error) {
		return []byte(`{"servers":{"jira":{"name":"jira"}}}`), nil
	}
	if _, err := registry.UpdateIndex(); err != nil {
		t.Fatalf("UpdateIndex: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(registry.CacheDir(), "registry_index.json"))
	if err != nil {
		t.Fatalf("ReadFile index: %v", err)
	}
	var cached map[string]any
	if err := json.Unmarshal(raw, &cached); err != nil {
		t.Fatalf("Unmarshal index: %v", err)
	}
	if _, ok := asMap(cached["servers"])["jira"]; !ok {
		t.Fatalf("cached index missing jira: %v", cached)
	}
	if _, err := os.Stat(filepath.Join(registry.CacheDir(), "last_fetched")); err != nil {
		t.Fatalf("last_fetched missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(registry.CacheDir(), "last_fetched.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy last_fetched.json should not be written (err=%v)", err)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_update_index_network_error
func TestUpdateIndexNetworkError(t *testing.T) {
	registry := newDefaultRegistry(t)
	registry.Fetch = func(string, time.Duration) ([]byte, error) {
		return nil, errors.New("connect failed")
	}
	if _, err := registry.UpdateIndex(); err == nil {
		t.Fatalf("expected network error")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_is_index_stale_no_cache
func TestIsIndexStaleNoCache(t *testing.T) {
	registry := newDefaultRegistry(t)
	if !registry.IsIndexStale() {
		t.Fatalf("expected stale with no cache")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_is_index_stale_fresh
func TestIsIndexStaleFresh(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeJSONAt(t, registry.lastFetchedPath(), map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if registry.IsIndexStale() {
		t.Fatalf("expected fresh index")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_is_index_stale_expired
func TestIsIndexStaleExpired(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeJSONAt(t, registry.lastFetchedPath(), map[string]any{
		"timestamp": time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano),
	})
	if !registry.IsIndexStale() {
		t.Fatalf("expected stale after 48h")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_is_index_stale_supports_legacy_filename
func TestIsIndexStaleSupportsLegacyFilename(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeJSONAt(t, registry.legacyLastFetchedPath(), map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
	})
	if registry.IsIndexStale() {
		t.Fatalf("expected fresh legacy index")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_is_index_stale_missing_timestamp_returns_true
func TestIsIndexStaleMissingTimestampReturnsTrue(t *testing.T) {
	registry := newDefaultRegistry(t)
	writeJSONAt(t, registry.lastFetchedPath(), map[string]any{"bad": "data"})
	if !registry.IsIndexStale() {
		t.Fatalf("expected stale when timestamp is missing")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_initialize_retires_previously_seeded_bundled_servers
func TestInitializeRetiresPreviouslySeededBundledServers(t *testing.T) {
	base := testBase(t)
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	entry := func(targets ...string) map[string]any {
		args := []any{"run", "python"}
		for _, target := range targets {
			args = append(args, target)
		}
		args = append(args, "--stdio")
		return map[string]any{
			"source":  "local",
			"enabled": true,
			"manifest": map[string]any{
				"stdio": map[string]any{"command": "uv", "args": args},
			},
		}
	}
	writeJSONAt(t, filepath.Join(base, "installed.json"), map[string]any{
		"servers": map[string]any{
			"hive_tools":     entry("mcp_server.py"),
			"terminal-tools": entry("terminal_tools_server.py"),
			"shell-tools":    entry("shell_tools_server.py"),
			"chart-tools":    entry("my_own_charts.py"),
			"gcu-tools":      entry("-m", "gcu.server"),
			"jira":           entry("jira_server.py"),
		},
	})
	registry := newRegistryAt(t, base)
	rows, err := registry.ListInstalled()
	if err != nil {
		t.Fatalf("ListInstalled: %v", err)
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[asString(row["name"])] = true
	}
	want := map[string]bool{"chart-tools": true, "jira": true}
	if len(got) != len(want) || !got["chart-tools"] || !got["jira"] {
		t.Fatalf("remaining = %v, want %v", got, want)
	}
}
