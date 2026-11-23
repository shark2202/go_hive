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
	server := newFakeMCPServer(t, "echo", "add", "x-y", "x_y")
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"servers":{"demo":{"transport":"http","url":"`+server.server.URL+`"}}}}`)

	runtime := attachFakeRuntime(t, home)
	statuses := runtime.Status()
	if len(statuses) != 1 || statuses[0].Name != "demo" || !statuses[0].Connected || statuses[0].Tools != 4 {
		t.Fatalf("statuses = %+v", statuses)
	}
	if statuses[0].Namespace != "" {
		t.Fatalf("status namespace = %q, want empty (bare default)", statuses[0].Namespace)
	}

	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	defs := catalogue.Defs()
	for _, want := range []string{"echo", "add", "x-y", "x_y"} {
		if !hasToolDef(defs, want) {
			t.Fatalf("bare MCP tool %q missing from the session catalogue", want)
		}
	}
	if hasToolDef(defs, "mcp__demo__echo") {
		t.Fatal("default mode prefixed a bare server tool in the catalogue")
	}

	for _, name := range []string{"echo", "x-y", "x_y"} {
		content, isError, err := catalogue.Execute(context.Background(), name, map[string]any{"text": "hi"})
		if err != nil || isError {
			t.Fatalf("execute %q = %q, isError=%v, err=%v", name, content, isError, err)
		}
		if content != "pong:"+name {
			t.Fatalf("execute %q content = %q", name, content)
		}
	}
	if calls := server.callNames(); len(calls) != 3 || calls[0] != "echo" || calls[1] != "x-y" || calls[2] != "x_y" {
		t.Fatalf("server calls = %v (want the remote names verbatim)", calls)
	}
	if args := server.firstArgs(); args["text"] != "hi" {
		t.Fatalf("server args = %v", args)
	}
}

// TestMCPRuntimePrefixModeWhenConfigured pins requirement 2 of the D3 decision:
// a non-empty mcp.namespace keeps the historical prefixed, folded surface, so
// an operator who relied on the old names does not regress.
//
// Parity-exempt: the namespace prefix is a Go-side integration switch; the
// Python reference always registers bare names.
func TestMCPRuntimePrefixModeWhenConfigured(t *testing.T) {
	server := newFakeMCPServer(t, "echo")
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"namespace":"mcp","servers":{"demo":{"transport":"http","url":"`+server.server.URL+`"}}}}`)

	runtime := attachFakeRuntime(t, home)
	statuses := runtime.Status()
	if len(statuses) != 1 || statuses[0].Namespace != "mcp" {
		t.Fatalf("statuses = %+v", statuses)
	}

	root := filepath.Join(home.Root, "session")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	if !hasToolDef(catalogue.Defs(), "mcp__demo__echo") {
		t.Fatalf("prefixed MCP tool missing in prefix mode: %+v", catalogue.Defs())
	}
	if hasToolDef(catalogue.Defs(), "echo") {
		t.Fatal("bare name leaked into the catalogue in prefix mode")
	}
	content, isError, err := catalogue.Execute(context.Background(), "mcp__demo__echo", map[string]any{"text": "hi"})
	if err != nil || isError || content != "pong:echo" {
		t.Fatalf("execute = %q, isError=%v, err=%v", content, isError, err)
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
	if !hasToolDef(declared, "echo") {
		t.Fatalf("worker tool pool has no bare MCP tool")
	}
	// The real loader reports remote names in Status().ToolNames; promotion
	// must land the same bare name in the worker base surface.
	merged := withMCPWorkerTools(base, declared)
	if !hasToolDef(merged, "echo") {
		t.Fatalf("MCP tool did not reach the worker base surface: %+v", merged)
	}
	if hasToolDef(base, "echo") {
		t.Fatal("worker base surface already had the MCP tool before promotion")
	}
	result, err := execute(agentloop.ToolCallRequest{Name: "echo", Input: map[string]any{}})
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
	echoCount := 0
	for _, def := range catalogue.Defs() {
		if def.Name == "echo" {
			echoCount++
		}
	}
	if echoCount != 1 {
		t.Fatalf("reachable server's bare tool loaded %d time(s), want 1", echoCount)
	}
	if content, isError, err := catalogue.Execute(context.Background(), "echo", nil); err != nil || isError || content != "pong:echo" {
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
	if listSettings.Namespace != "" {
		t.Fatalf("default namespace = %q, want empty (bare registration)", listSettings.Namespace)
	}

	// A configuration without an "mcp" block leaves the default enabled state
	// and no file-level servers.
	home3 := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home3, `{"llm":{"model":"x"}}`)
	bare := readMCPFileSettings(home3.ConfigurationFile())
	if !bare.Enabled || bare.Namespace != "" || len(bare.Servers) != 0 {
		t.Fatalf("bare settings = %+v", bare)
	}
}

// TestMCPNamespaceDefaultsToBare pins the D3 final decision: the config default
// is the empty namespace, so model-visible MCP names are the server's raw names
// exactly like the Python reference. A non-empty value is the opt-in prefix
// switch.
//
// Parity: core/framework/loader/tool_registry.py::register_mcp_server — Python
// keys MCP tools by the bare mcp_tool.name (line 1053), with no configuration
// knob for a namespace.
func TestMCPNamespaceDefaultsToBare(t *testing.T) {
	// Missing file and missing block both resolve to bare.
	for name, content := range map[string]string{
		"missing-block": `{"llm":{"model":"x"}}`,
		"empty-block":   `{"mcp":{}}`,
		"blank-key":     `{"mcp":{"namespace":"   "}}`,
	} {
		home := hivehome.Home{Root: t.TempDir()}
		writeConfiguration(t, home, content)
		if got := readMCPFileSettings(home.ConfigurationFile()).Namespace; got != "" {
			t.Fatalf("%s: namespace = %q, want empty", name, got)
		}
	}

	// An explicit non-empty value still selects the prefixed mode.
	home := hivehome.Home{Root: t.TempDir()}
	writeConfiguration(t, home, `{"mcp":{"namespace":"mcp"}}`)
	if got := readMCPFileSettings(home.ConfigurationFile()).Namespace; got != "mcp" {
		t.Fatalf("explicit namespace = %q, want mcp", got)
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

	specs := configuredMCPServers(home, mcpFileSettings{Enabled: true})
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
	if !hasToolDef(before.Defs(), "echo") {
		t.Fatal("initial catalogue lacks the bare MCP tool")
	}

	writeConfiguration(t, home, `{"mcp":{"servers":{"other":{"transport":"http","url":"`+second.server.URL+`"}}}}`)
	if err := runtime.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	after := newSessionToolCatalogue(root, builtin.New(root), home)
	if !hasToolDef(after.Defs(), "ping") {
		t.Fatalf("reloaded server missing from a new catalogue")
	}
	if hasToolDef(after.Defs(), "echo") {
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
// fills it from tools/list), while session/worker defs carry registry keys — so
// this fixture uses remote names exactly like production, and the comparison
// must map them through the server's own namespace: bare by default, prefixed
// when the status declares one.
func TestWithMCPWorkerToolsPromotesLoadedTools(t *testing.T) {
	tools.SetProcessMCP(&fakeStatusAttacher{statuses: []tools.MCPServerStatus{
		{Name: "demo", Connected: true, Namespace: "mcp", ToolNames: []string{"echo"}},
		{Name: "flat", Connected: true, ToolNames: []string{"flat_ping"}},
		{Name: "broken", Connected: false, ToolNames: []string{"nope"}},
		{Name: "custom", Connected: true, Namespace: "remote", ToolNames: []string{"ping"}},
	}})
	t.Cleanup(func() { tools.SetProcessMCP(nil) })

	base := []llm.ToolDef{{Name: "read_file"}, {Name: "mcp__demo__echo"}, {Name: "flat_ping"}}
	declared := []llm.ToolDef{
		{Name: "read_file"},
		{Name: "mcp__demo__echo"},
		{Name: "mcp__broken__nope"},
		{Name: "remote__custom__ping"},
		{Name: "flat_ping"},
		{Name: "integration_tool"},
	}
	merged := withMCPWorkerTools(base, declared)
	names := map[string]bool{}
	for _, def := range merged {
		names[def.Name] = true
	}
	if !names["read_file"] || !names["mcp__demo__echo"] || !names["flat_ping"] || !names["remote__custom__ping"] {
		t.Fatalf("loaded MCP tool missing from the worker base surface: %v", merged)
	}
	if len(merged) != 4 {
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
