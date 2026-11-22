package mcpregistry

import (
	"errors"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

type fakeHealthClient struct {
	tools []tools.MCPTool
	err   error
}

func (f *fakeHealthClient) ListTools() ([]tools.MCPTool, error) { return f.tools, f.err }
func (f *fakeHealthClient) Close() error                        { return nil }

type fakePooledManager struct {
	connected    bool
	healthy      bool
	client       HealthClient
	acquireCalls int
	releaseCalls int
}

func (m *fakePooledManager) HasConnection(string) bool { return m.connected }
func (m *fakePooledManager) HealthCheck(string) bool   { return m.healthy }
func (m *fakePooledManager) Acquire(*ServerConfig) (HealthClient, error) {
	m.acquireCalls++
	return m.client, nil
}
func (m *fakePooledManager) Release(string) { m.releaseCalls++ }

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_healthy
func TestRunHealthCheckHealthy(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		return &fakeHealthClient{}, nil
	}
	result, err := registry.RunHealthCheck("db")
	if err != nil {
		t.Fatalf("RunHealthCheck: %v", err)
	}
	if result["status"] != "healthy" {
		t.Fatalf("result = %v", result)
	}
	if installedEntry(t, registry, "db")["last_health_check_at"] == nil {
		t.Fatalf("last_health_check_at was not recorded")
	}
}

// Parity: core/tests/test_mcp_registry.py::test_health_check_public_api
func TestHealthCheckPublicAPI(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		return &fakeHealthClient{}, nil
	}
	result, err := registry.HealthCheck("db")
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["status"] != "healthy" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_health_check_prefers_pooled_connection
func TestHealthCheckPrefersPooledConnection(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})

	manager := &fakePooledManager{
		connected: true,
		healthy:   true,
		client: &fakeHealthClient{tools: []tools.MCPTool{
			{Name: "one"}, {Name: "two"},
		}},
	}
	registry.Manager = manager
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		t.Errorf("fresh MCPClient should not be constructed")
		return nil, nil
	}

	result, err := registry.HealthCheck("db")
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["status"] != "healthy" {
		t.Fatalf("result = %v", result)
	}
	if result["tools"] != 2 {
		t.Fatalf("tools = %v", result["tools"])
	}
	if manager.acquireCalls != 1 || manager.releaseCalls != 1 {
		t.Fatalf("acquire=%d release=%d", manager.acquireCalls, manager.releaseCalls)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_health_check_uses_installed_transport_preference
func TestHealthCheckUsesInstalledTransportPreference(t *testing.T) {
	registry := newDefaultRegistry(t)
	manifest := map[string]any{
		"transport": map[string]any{"supported": []any{"stdio", "http"}, "default": "stdio"},
		"stdio":     map[string]any{"command": "uvx", "args": []any{"legacy-server"}},
		"http":      map[string]any{"url": "http://localhost:9001"},
	}
	if err := registry.AddLocal("api", AddLocalOptions{Manifest: manifest, Transport: "http"}); err != nil {
		t.Fatalf("AddLocal: %v", err)
	}

	var seen []string
	registry.ClientFactory = func(config *ServerConfig) (HealthClient, error) {
		seen = append(seen, config.Transport)
		return &fakeHealthClient{}, nil
	}
	result, err := registry.HealthCheck("api")
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if result["status"] != "healthy" {
		t.Fatalf("result = %v", result)
	}
	if len(seen) != 1 || seen[0] != "http" {
		t.Fatalf("seen transports = %v", seen)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_unhealthy
func TestRunHealthCheckUnhealthy(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9999"})
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		return nil, errors.New("connection refused")
	}
	result, err := registry.RunHealthCheck("db")
	if err != nil {
		t.Fatalf("RunHealthCheck: %v", err)
	}
	if result["status"] != "unhealthy" {
		t.Fatalf("result = %v", result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, "refused") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_list_tools_failure
func TestRunHealthCheckListToolsFailure(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "db", AddLocalOptions{Transport: "http", URL: "http://localhost:9090"})
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		return &fakeHealthClient{err: errors.New("tools discovery failed")}, nil
	}
	result, err := registry.RunHealthCheck("db")
	if err != nil {
		t.Fatalf("RunHealthCheck: %v", err)
	}
	if result["status"] != "unhealthy" {
		t.Fatalf("result = %v", result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, "tools discovery failed") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_unsupported_transport
func TestRunHealthCheckUnsupportedTransport(t *testing.T) {
	registry := newDefaultRegistry(t)
	data, err := registry.ReadInstalled()
	if err != nil {
		t.Fatalf("ReadInstalled: %v", err)
	}
	asMap(data["servers"])["weird"] = map[string]any{
		"source":    "local",
		"transport": "grpc",
		"enabled":   true,
		"manifest":  map[string]any{"name": "weird", "transport": map[string]any{"default": "grpc"}},
		"overrides": map[string]any{"env": map[string]any{}, "headers": map[string]any{}},
	}
	if err := registry.WriteInstalled(data); err != nil {
		t.Fatalf("WriteInstalled: %v", err)
	}
	result, err := registry.RunHealthCheck("weird")
	if err != nil {
		t.Fatalf("RunHealthCheck: %v", err)
	}
	if result["status"] != "unhealthy" {
		t.Fatalf("result = %v", result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, "Unsupported transport") {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_not_installed
func TestRunHealthCheckNotInstalled(t *testing.T) {
	registry := newDefaultRegistry(t)
	_, err := registry.RunHealthCheck("ghost")
	requireErrContains(t, err, "not installed")
}

// Parity: core/tests/test_mcp_registry.py::test_run_health_check_all_servers
func TestRunHealthCheckAllServers(t *testing.T) {
	registry := newDefaultRegistry(t)
	mustAddLocal(t, registry, "a", AddLocalOptions{Transport: "http", URL: "http://localhost:1"})
	mustAddLocal(t, registry, "b", AddLocalOptions{Transport: "http", URL: "http://localhost:2"})
	registry.ClientFactory = func(*ServerConfig) (HealthClient, error) {
		return &fakeHealthClient{}, nil
	}
	results, err := registry.RunHealthCheckAll()
	if err != nil {
		t.Fatalf("RunHealthCheckAll: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %v", results)
	}
	for _, name := range []string{"a", "b"} {
		if results[name]["status"] != "healthy" {
			t.Fatalf("%s = %v", name, results[name])
		}
	}
}
