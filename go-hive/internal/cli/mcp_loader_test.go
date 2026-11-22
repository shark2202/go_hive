package cli

// MCP loading is a serve-side capability: these tests drive the real loader (a
// fake MCP server over the HTTP transport) and the real session catalogue, so
// what they prove is the production path — config read, tools/list fetch,
// namespaced registration, and a tool call that reaches the server.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// fakeMCPServer answers the JSON-RPC subset the HTTP client speaks
// (tools/list, tools/call, ping) and records what it was asked to run.
type fakeMCPServer struct {
	server *httptest.Server
	tools  []string

	mu        sync.Mutex
	listCalls int
	calls     []string
	args      []map[string]any
}

func newFakeMCPServer(t *testing.T, toolNames ...string) *fakeMCPServer {
	t.Helper()
	fake := &fakeMCPServer{tools: toolNames}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/mcp/v1", fake.handle)
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeMCPServer) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	method, _ := body["method"].(string)
	reply := map[string]any{"jsonrpc": "2.0", "id": body["id"]}
	switch method {
	case "tools/list":
		f.mu.Lock()
		f.listCalls++
		f.mu.Unlock()
		entries := make([]any, 0, len(f.tools))
		for _, name := range f.tools {
			entries = append(entries, map[string]any{
				"name":        name,
				"description": name + " tool",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
				},
			})
		}
		reply["result"] = map[string]any{"tools": entries}
	case "tools/call":
		params, _ := body["params"].(map[string]any)
		name, _ := params["name"].(string)
		args, _ := params["arguments"].(map[string]any)
		f.mu.Lock()
		f.calls = append(f.calls, name)
		f.args = append(f.args, args)
		f.mu.Unlock()
		reply["result"] = map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "pong:" + name}},
		}
	case "ping":
		reply["result"] = map[string]any{}
	default:
		reply["error"] = map[string]any{"code": -32601, "message": "unknown method " + method}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (f *fakeMCPServer) callNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMCPServer) firstArgs() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.args) == 0 {
		return nil
	}
	return f.args[0]
}

func writeConfiguration(t *testing.T, home hivehome.Home, content string) {
	t.Helper()
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(content), 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
}

func hasToolDef(defs []llm.ToolDef, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

// attachFakeRuntime loads MCP for the test home and installs it as the process
// loader for the duration of the test.
func attachFakeRuntime(t *testing.T, home hivehome.Home) *mcpRuntime {
	t.Helper()
	runtime := newMCPRuntime(home, io.Discard)
	if err := runtime.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	t.Cleanup(runtime.Close)
	tools.SetProcessMCP(runtime)
	t.Cleanup(func() { tools.SetProcessMCP(nil) })
	return runtime
}

// Parity-exempt: MCP tool loading into a session catalogue is a Go-side
// capability (upstream registers server tools through its own loader); the
// fake server replaces the subprocess upstream tests spawn.
func TestMCPRuntimeLoadsToolsIntoSessionCatalogue(t *testing.T) {
	server := newFakeMCPServer(t, "echo", "add")
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"servers":{"demo":{"transport":"http","url":"`+server.server.URL+`"}}}}`)

	runtime := attachFakeRuntime(t, home)
	statuses := runtime.Status()
	if len(statuses) != 1 || statuses[0].Name != "demo" || !statuses[0].Connected || statuses[0].Tools != 2 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Namespace != tools.MCPToolNamespace {
		t.Fatalf("status namespace = %q, want %q", statuses[0].Namespace, tools.MCPToolNamespace)
	}

	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	defs := catalogue.Defs()
	if !hasToolDef(defs, "mcp__demo__echo") || !hasToolDef(defs, "mcp__demo__add") {
		t.Fatalf("namespaced MCP tools missing from the session catalogue")
	}
	if hasToolDef(defs, "echo") {
		t.Fatal("server tool leaked into the catalogue without its namespace")
	}

	content, isError, err := catalogue.Execute(context.Background(), "mcp__demo__echo", map[string]any{"text": "hi"})
	if err != nil || isError {
		t.Fatalf("execute = %q, isError=%v, err=%v", content, isError, err)
	}
	if content != "pong:echo" {
		t.Fatalf("execute content = %q", content)
	}
	if calls := server.callNames(); len(calls) != 1 || calls[0] != "echo" {
		t.Fatalf("server calls = %v", calls)
	}
	if args := server.firstArgs(); args["text"] != "hi" {
		t.Fatalf("server args = %v", args)
	}
}

// Parity-exempt: the colony worker tool pool is the same catalogue the queen
// uses (framework/host/colony_runtime.py resolves it at spawn); this pins that
// an MCP tool is selectable there too.
func TestMCPRuntimeReachesColonyWorkerPool(t *testing.T) {
	server := newFakeMCPServer(t, "echo")
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"servers":{"demo":{"transport":"http","url":"`+server.server.URL+`"}}}}`)
	attachFakeRuntime(t, home)

	colonyPath := filepath.Join(home.Root, "colonies", "c1")
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	base, declared, execute := colonyWorkerToolSurface(colonyPath, builtin.New(colonyPath), home)
	if !hasToolDef(declared, "mcp__demo__echo") {
		t.Fatalf("worker tool pool has no MCP tool")
	}
	// The real loader reports remote names in Status().ToolNames; promotion
	// must still land the namespaced def in the worker base surface.
	merged := withMCPWorkerTools(base, declared)
	if !hasToolDef(merged, "mcp__demo__echo") {
		t.Fatalf("MCP tool did not reach the worker base surface: %+v", merged)
	}
	if hasToolDef(base, "mcp__demo__echo") {
		t.Fatal("worker base surface already had the MCP tool before promotion")
	}
	result, err := execute(agentloop.ToolCallRequest{Name: "mcp__demo__echo", Input: map[string]any{}})
	if err != nil || result.Content != "pong:echo" {
		t.Fatalf("worker tool result = %+v, err = %v", result, err)
	}
}

// Parity-exempt: degradation policy for an unreachable server is a serve-side
// decision (upstream's loader logs and continues in-process).
func TestMCPRuntimeDegradesWhenServerUnreachable(t *testing.T) {
	good := newFakeMCPServer(t, "echo")
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"servers":{`+
		`"demo":{"transport":"http","url":"`+good.server.URL+`"},`+
		`"broken":{"transport":"http","url":"`+deadURL+`"}}}}`)

	runtime := attachFakeRuntime(t, home)
	statuses := runtime.Status()
	if len(statuses) != 2 {
		t.Fatalf("statuses = %+v", statuses)
	}
	byName := map[string]tools.MCPServerStatus{}
	for _, status := range statuses {
		byName[status.Name] = status
	}
	if !byName["demo"].Connected || byName["demo"].Tools != 1 {
		t.Fatalf("demo status = %+v", byName["demo"])
	}
	if byName["broken"].Connected || byName["broken"].Error == "" {
		t.Fatalf("broken status = %+v", byName["broken"])
	}

	// The reachable server still loads and runs; the unreachable one costs
	// nothing beyond its status entry.
	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	if !hasToolDef(catalogue.Defs(), "mcp__demo__echo") {
		t.Fatal("reachable server's tool did not load")
	}
	for _, def := range catalogue.Defs() {
		if def.Name == "mcp__broken__echo" {
			t.Fatal("unreachable server registered a tool")
		}
	}
	if content, isError, err := catalogue.Execute(context.Background(), "mcp__demo__echo", nil); err != nil || isError || content != "pong:echo" {
		t.Fatalf("reachable tool execute = %q, isError=%v, err=%v", content, isError, err)
	}
}

// Parity-exempt: writing the MCP "servers" block shape is covered by the client
// tests; this covers the two shapes the Go config reader accepts.
func TestReadMCPFileSettings(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{
		"enabled": true,
		"namespace": "remote",
		"timeout_seconds": 42,
		"servers": {"demo": {"transport": "http", "url": "http://127.0.0.1:1"}}
	}}`)

	settings := readMCPFileSettings(home.ConfigurationFile())
	if !settings.Enabled || settings.Namespace != "remote" {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.Timeout == nil || *settings.Timeout != 42*time.Second {
		t.Fatalf("timeout = %v", settings.Timeout)
	}
	if len(settings.Servers) != 1 || settings.Servers[0].Name != "demo" || settings.Servers[0].URL != "http://127.0.0.1:1" {
		t.Fatalf("servers = %+v", settings.Servers)
	}

	home2 := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home2, `{"mcp":{"servers":[{"name":"listy","transport":"stdio","command":"uvx"}]}}`)
	listSettings := readMCPFileSettings(home2.ConfigurationFile())
	if len(listSettings.Servers) != 1 || listSettings.Servers[0].Name != "listy" {
		t.Fatalf("list-form servers = %+v", listSettings.Servers)
	}
	if listSettings.Namespace != tools.MCPToolNamespace {
		t.Fatalf("default namespace = %q", listSettings.Namespace)
	}

	// A configuration without an "mcp" block leaves the default enabled state
	// and no file-level servers.
	home3 := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home3, `{"llm":{"model":"x"}}`)
	bare := readMCPFileSettings(home3.ConfigurationFile())
	if !bare.Enabled || bare.Namespace != tools.MCPToolNamespace || len(bare.Servers) != 0 {
		t.Fatalf("bare settings = %+v", bare)
	}
}

// Parity-exempt: the registry-installed source is the Go port's config path for
// servers added from the dashboard; upstream keeps them in mcp registry state.
func TestConfiguredMCPServersReadsRegistry(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	registryDir := home.MCPRegistryDir()
	if err := os.MkdirAll(registryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	installed := `{"servers":{
		"stdio_srv":{"source":"local","transport":"stdio","mode":"tools","enabled":true,
			"manifest":{"transport":{"supported":["stdio"],"default":"stdio"},
				"stdio":{"command":"uvx","args":["srv"],"env":{"TOKEN":"x"}}}},
		"off_srv":{"source":"local","transport":"stdio","mode":"tools","enabled":false,
			"manifest":{"stdio":{"command":"never"}}},
		"cli_srv":{"source":"local","transport":"stdio","mode":"cli","enabled":true,
			"manifest":{"stdio":{"command":"cli"}}}
	}}`
	if err := os.WriteFile(filepath.Join(registryDir, "installed.json"), []byte(installed), 0o600); err != nil {
		t.Fatal(err)
	}

	specs := configuredMCPServers(home, mcpFileSettings{Enabled: true, Namespace: tools.MCPToolNamespace})
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	spec := specs[0]
	if spec.Name != "stdio_srv" || spec.Transport != "stdio" || spec.Command != "uvx" || spec.Env["TOKEN"] != "x" {
		t.Fatalf("spec = %+v", spec)
	}

	// configuration.json overrides the same-named registry entry and can drop
	// a registry server with "enabled": false.
	writeConfiguration(t, home, `{"mcp":{"servers":{
		"stdio_srv":{"transport":"http","url":"http://127.0.0.1:9","enabled":true}
	}}}`)
	settings := readMCPFileSettings(home.ConfigurationFile())
	merged := configuredMCPServers(home, settings)
	if len(merged) != 1 || merged[0].Transport != "http" || merged[0].URL != "http://127.0.0.1:9" {
		t.Fatalf("merged = %+v", merged)
	}

	writeConfiguration(t, home, `{"mcp":{"servers":{"stdio_srv":{"enabled":false}}}}`)
	if merged := configuredMCPServers(home, readMCPFileSettings(home.ConfigurationFile())); len(merged) != 0 {
		t.Fatalf("disabled registry server still loaded: %+v", merged)
	}
}

// Parity-exempt: the lifecycle seam (attach at construction, attach on reload,
// detach on shutdown) is Go-side wiring with no upstream counterpart.
func TestMCPRuntimeReloadReachesNewCatalogues(t *testing.T) {
	first := newFakeMCPServer(t, "echo")
	second := newFakeMCPServer(t, "ping")
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"servers":{"demo":{"transport":"http","url":"`+first.server.URL+`"}}}}`)

	runtime := attachFakeRuntime(t, home)
	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	before := newSessionToolCatalogue(root, builtin.New(root), home)
	if !hasToolDef(before.Defs(), "mcp__demo__echo") {
		t.Fatal("initial catalogue lacks the MCP tool")
	}

	writeConfiguration(t, home, `{"mcp":{"servers":{"other":{"transport":"http","url":"`+second.server.URL+`"}}}}`)
	if err := runtime.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after := newSessionToolCatalogue(root, builtin.New(root), home)
	if !hasToolDef(after.Defs(), "mcp__other__ping") {
		t.Fatalf("reloaded server missing from a new catalogue")
	}
	if hasToolDef(after.Defs(), "mcp__demo__echo") {
		t.Fatal("removed server still registered in a new catalogue")
	}
}

// fakeStatusAttacher supplies a status snapshot only; withMCPWorkerTools reads
// no more than that.
type fakeStatusAttacher struct {
	statuses []tools.MCPServerStatus
}

func (f *fakeStatusAttacher) Attach(*tools.ToolRegistry) []tools.ServerResolution {
	return nil
}
func (f *fakeStatusAttacher) Reload() error { return nil }
func (f *fakeStatusAttacher) Status() []tools.MCPServerStatus {
	return append([]tools.MCPServerStatus(nil), f.statuses...)
}
func (f *fakeStatusAttacher) Generation() int64 { return 1 }

// Parity-exempt: promoting loaded MCP tools into the colony worker base surface
// is Go-side (upstream's spawn_tools already list them).
//
// Status.ToolNames carries the names the SERVER advertises (the real loader
// fills it from tools/list), while session/worker defs are namespaced — so this
// fixture uses remote names exactly like production, and the comparison must
// namespace them with the server's own namespace.
func TestWithMCPWorkerToolsPromotesLoadedTools(t *testing.T) {
	tools.SetProcessMCP(&fakeStatusAttacher{statuses: []tools.MCPServerStatus{
		{Name: "demo", Connected: true, ToolNames: []string{"echo"}},
		{Name: "broken", Connected: false, ToolNames: []string{"nope"}},
		{Name: "custom", Connected: true, Namespace: "remote", ToolNames: []string{"ping"}},
	}})
	t.Cleanup(func() { tools.SetProcessMCP(nil) })

	base := []llm.ToolDef{{Name: "read_file"}, {Name: "mcp__demo__echo"}}
	declared := []llm.ToolDef{
		{Name: "read_file"},
		{Name: "mcp__demo__echo"},
		{Name: "mcp__broken__nope"},
		{Name: "remote__custom__ping"},
		{Name: "integration_tool"},
	}
	merged := withMCPWorkerTools(base, declared)
	names := map[string]bool{}
	for _, def := range merged {
		names[def.Name] = true
	}
	if !names["read_file"] || !names["mcp__demo__echo"] || !names["remote__custom__ping"] {
		t.Fatalf("loaded MCP tool missing from the worker base surface: %v", merged)
	}
	if len(merged) != 3 {
		t.Fatalf("merged = %v", merged)
	}
	if names["mcp__broken__nope"] || names["integration_tool"] {
		t.Fatalf("unloaded or unrelated tool promoted: %v", merged)
	}

	tools.SetProcessMCP(nil)
	if got := withMCPWorkerTools([]llm.ToolDef{{Name: "read_file"}}, declared); len(got) != 1 {
		t.Fatalf("without a loader the base changed: %v", got)
	}
}
