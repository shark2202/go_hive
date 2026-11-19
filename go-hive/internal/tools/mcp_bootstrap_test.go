package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// Parity-exempt: the namespace prefix is a Go-side integration decision (the
// Python registry registers a server's own tool names); upstream has no test
// for a prefixed registry key.

func TestNamespacedMCPToolName(t *testing.T) {
	cases := []struct {
		namespace string
		server    string
		tool      string
		want      string
	}{
		{"mcp", "demo", "echo", "mcp__demo__echo"},
		{"", "demo", "echo", "mcp__demo__echo"},
		{"mcp", "my server", "read.file", "mcp__my_server__read_file"},
		{"MCP Tools", "a/b", "c:d", "MCP_Tools__a_b__c_d"},
		{"mcp", "", "", "mcp__server__tool"},
	}
	for _, tc := range cases {
		if got := NamespacedMCPToolName(tc.namespace, tc.server, tc.tool); got != tc.want {
			t.Errorf("NamespacedMCPToolName(%q, %q, %q) = %q, want %q",
				tc.namespace, tc.server, tc.tool, got, tc.want)
		}
	}
}

// TestNamespacedMCPRegistrationKeepsBuiltinTool proves the namespace is what
// stops an MCP server from taking over a builtin: the remote "read_file" lands
// as "mcp__demo__read_file", the builtin keeps its executor, and the MCP tool
// still calls the server's own tool name.
//
// Parity-exempt: the namespace prefix is a Go integration decision; upstream
// registers a server's raw tool names.
func TestNamespacedMCPRegistrationKeepsBuiltinTool(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("read_file", "builtin read", nil, func(map[string]any) (any, error) {
		return "builtin", nil
	})
	client := newFakeMCPClient(&MCPServerConfig{Name: "demo"}, tool("read_file", "path"))
	registry.newMCPClient = fakeFactory(map[string]MCPClient{"demo": client})

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "demo", Transport: "stdio", Command: "fake"}},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 1 || results[0].Status != "loaded" || results[0].ToolsLoaded != 1 {
		t.Fatalf("results = %+v", results)
	}
	if !registry.HasTool("mcp__demo__read_file") {
		t.Fatalf("namespaced tool missing: %v", registry.RegisteredNames())
	}

	builtinResult, err := registry.GetExecutor().Execute(context.Background(), ToolUse{ID: "1", Name: "read_file"})
	if err != nil || builtinResult.Content != "builtin" {
		t.Fatalf("builtin read_file = %q, %v", builtinResult.Content, err)
	}

	mcpResult, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "2", Name: "mcp__demo__read_file", Input: map[string]any{"path": "/tmp/x"},
	})
	if err != nil {
		t.Fatalf("mcp tool execute: %v", err)
	}
	if !strings.Contains(mcpResult.Content, `"result": "ok"`) {
		t.Fatalf("mcp tool content = %q", mcpResult.Content)
	}
	if args, ok := client.callArgs("read_file"); !ok || args["path"] != "/tmp/x" {
		t.Fatalf("server saw call %q with %v", "read_file", args)
	}
	if _, ok := client.callArgs("mcp__demo__read_file"); ok {
		t.Fatal("executor sent the registry name to the server instead of the remote name")
	}
}

// TestMCPRegistrationShadowedByExistingTool covers the second line of defence:
// when the namespaced key is already taken (a builtin or another server owns
// it), the existing tool wins and the server's count does not claim it.
//
// Parity-exempt: prefixed registry keys do not exist upstream, so neither does
// the shadow case they can produce.
func TestMCPRegistrationShadowedByExistingTool(t *testing.T) {
	registry := NewToolRegistry()
	registry.sleepFn = func(time.Duration) {}
	registry.RegisterFunction("mcp__demo__echo", "already here", nil, func(map[string]any) (any, error) {
		return "owner", nil
	})
	registry.newMCPClient = fakeFactory(map[string]MCPClient{
		"demo": newFakeMCPClient(&MCPServerConfig{Name: "demo"}, tool("echo")),
	})

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "demo", Transport: "stdio", Command: "fake"}},
		LoadRegistryOptions{LogSummary: boolPtr(false), PreserveExistingTools: boolPtr(true), ToolNamespace: "mcp"},
	)
	if len(results) != 1 || results[0].Status != "skipped" || results[0].ToolsLoaded != 0 {
		t.Fatalf("results = %+v", results)
	}
	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{ID: "1", Name: "mcp__demo__echo"})
	if err != nil || result.Content != "owner" {
		t.Fatalf("colliding tool was replaced: %q, %v", result.Content, err)
	}
}

// fakeMCPLoader is a minimal tools.MCPAttacher: it registers one namespaced tool
// per server so the process seam (attach on construction, re-attach on a new
// generation) can be tested without a live server.
type fakeMCPLoader struct {
	mu      sync.Mutex
	gen     int64
	servers map[string][]string
	failing map[string]string
}

func (f *fakeMCPLoader) Generation() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func (f *fakeMCPLoader) Reload() error {
	f.mu.Lock()
	f.gen++
	f.mu.Unlock()
	return nil
}

func (f *fakeMCPLoader) Status() []MCPServerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MCPServerStatus, 0, len(f.servers)+len(f.failing))
	for name, tools := range f.servers {
		out = append(out, MCPServerStatus{Name: name, Connected: true, Enabled: true, Tools: len(tools)})
	}
	for name, reason := range f.failing {
		out = append(out, MCPServerStatus{Name: name, Enabled: true, Error: reason})
	}
	SortMCPServerStatuses(out)
	return out
}

func (f *fakeMCPLoader) Attach(registry *ToolRegistry) []ServerResolution {
	f.mu.Lock()
	defer f.mu.Unlock()
	results := make([]ServerResolution, 0, len(f.servers)+len(f.failing))
	for _, name := range sortedStrings(keysOf(f.servers)) {
		registry.ensureServerOrder(name)
		if _, ok := registry.mcpServerTools[name]; !ok {
			registry.mcpServerTools[name] = map[string]struct{}{}
		}
		loaded := 0
		for _, remote := range f.servers[name] {
			registered := NamespacedMCPToolName(MCPToolNamespace, name, remote)
			if registry.HasTool(registered) {
				continue
			}
			registry.Register(registered, Tool{Name: registered}, func(map[string]any) (any, error) {
				return name + ":" + remote, nil
			})
			registry.mcpToolNames[registered] = struct{}{}
			registry.mcpServerTools[name][registered] = struct{}{}
			loaded++
		}
		results = append(results, ServerResolution{Server: name, Status: "loaded", ToolsLoaded: loaded})
	}
	for name, reason := range f.failing {
		results = append(results, ServerResolution{Server: name, Status: "skipped", SkippedReason: strPtr(reason)})
	}
	return results
}

func keysOf(in map[string][]string) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		out = append(out, key)
	}
	return out
}

// TestToolRegistryAttachesProcessMCP proves a registry built through
// NewToolRegistry starts with the loaded servers' tools, so queen and worker
// catalogues get MCP without either assembly file wiring it.
//
// Parity-exempt: the process attach seam is Go-only (upstream loads MCP inside
// its loader).
func TestToolRegistryAttachesProcessMCP(t *testing.T) {
	loader := &fakeMCPLoader{gen: 1, servers: map[string][]string{"demo": {"echo"}}}
	SetProcessMCP(loader)
	t.Cleanup(func() { SetProcessMCP(nil) })

	registry := NewToolRegistry()
	if !registry.HasTool("mcp__demo__echo") {
		t.Fatalf("registry did not attach the process MCP loader: %v", registry.RegisteredNames())
	}
}

// TestToolRegistryReattachesOnNewGeneration proves a reload reaches a registry
// that is already serving: the next resync drops the old server's tools and
// registers the new set.
//
// Parity-exempt: the generation counter that makes reloads reach live sessions
// is a Go-only mechanism.
func TestToolRegistryReattachesOnNewGeneration(t *testing.T) {
	loader := &fakeMCPLoader{gen: 1, servers: map[string][]string{"demo": {"echo"}}}
	SetProcessMCP(loader)
	t.Cleanup(func() { SetProcessMCP(nil) })

	registry := NewToolRegistry()
	if !registry.HasTool("mcp__demo__echo") {
		t.Fatalf("initial attach missing: %v", registry.RegisteredNames())
	}

	loader.mu.Lock()
	loader.gen = 2
	loader.servers = map[string][]string{"other": {"ping"}}
	loader.mu.Unlock()

	if !registry.ResyncMCPServersIfNeeded(false) {
		t.Fatal("generation bump did not trigger a re-attach")
	}
	if registry.HasTool("mcp__demo__echo") {
		t.Fatalf("removed server's tool survived the reload: %v", registry.RegisteredNames())
	}
	if !registry.HasTool("mcp__other__ping") {
		t.Fatalf("reloaded server's tool missing: %v", registry.RegisteredNames())
	}
	if registry.ResyncMCPServersIfNeeded(false) {
		t.Fatal("resync ran again without a new generation")
	}
}

// TestToolRegistryWithoutProcessMCPLoadsNothing guards the default: outside
// serve (or a unit test) no MCP loader is installed and no tools appear.
//
// Parity-exempt: absence-of-loader default is a Go-only seam.
func TestToolRegistryWithoutProcessMCPLoadsNothing(t *testing.T) {
	SetProcessMCP(nil)
	registry := NewToolRegistry()
	if len(registry.mcpToolNames) != 0 || len(registry.mcpClients) != 0 {
		t.Fatalf("registry loaded MCP without a loader: %v", registry.RegisteredNames())
	}
}

// TestSetMCPCallResultTimeoutOverrides checks the configurable timeout and
// restores the default for the rest of the package's tests.
//
// Parity-exempt: the Python client reads its timeout once from the env at
// import; the Go port also accepts a configuration.json value.
func TestSetMCPCallResultTimeoutOverrides(t *testing.T) {
	previous := mcpCallResultTimeoutOverride.Load()
	t.Cleanup(func() { mcpCallResultTimeoutOverride.Store(previous) })

	SetMCPCallResultTimeout(7 * time.Second)
	if got := MCPCallResultTimeout(); got != 7*time.Second {
		t.Fatalf("timeout = %s", got)
	}
	SetMCPCallResultTimeout(0)
	if got := MCPCallResultTimeout(); got != 7*time.Second {
		t.Fatalf("non-positive timeout must be ignored, got %s", got)
	}
}
