package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func harnessTool(name string, params ...string) HarnessTool {
	paramSet := map[string]struct{}{}
	properties := map[string]any{}
	for _, p := range params {
		paramSet[p] = struct{}{}
		properties[p] = map[string]any{"type": "string"}
	}
	return HarnessTool{
		Name:        name,
		Description: name,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   []any{},
		},
		Params: paramSet,
		Invoke: func(map[string]any) (any, error) { return map[string]any{"ok": true}, nil },
	}
}

func installHarnessBuilder(r *ToolRegistry, groups map[string][]HarnessTool) {
	r.harnessGroupBuilderFn = func(group string, _ func() map[string]string) []HarnessTool {
		return groups[group]
	}
}

// Parity: core/tests/test_harness_tools.py::test_every_group_builds
func TestEveryHarnessGroupBuilds(t *testing.T) {
	for group := range HarnessGroupNames {
		registry := NewToolRegistry()
		installHarnessBuilder(registry, map[string][]HarnessTool{
			group: {{Name: group + "_tool", Description: "d",
				InputSchema: map[string]any{
					"type":       "object",
					"title":      "should be dropped",
					"properties": map[string]any{"x": map[string]any{"type": "string"}},
					"required":   []any{},
				},
				Params: map[string]struct{}{"x": {}}}},
		})
		count := registry.RegisterMCPServer(MCPServerSpec{Name: group, Transport: "harness"},
			RegisterMCPServerOptions{})
		if count != 1 {
			t.Fatalf("%s registered %d tools, want 1", group, count)
		}
		tool, ok := registry.GetTool(group + "_tool")
		if !ok {
			t.Fatalf("%s produced no tool", group)
		}
		if tool.Parameters["type"] != "object" {
			t.Fatalf("%s schema type = %v", group, tool.Parameters["type"])
		}
		if _, ok := tool.Parameters["title"]; ok {
			t.Fatalf("%s schema retained top-level title", group)
		}
		if _, ok := tool.Parameters["properties"].(map[string]any)["x"]; !ok {
			t.Fatalf("%s schema lost its properties", group)
		}
	}
}

// Parity: core/tests/test_harness_tools.py::test_bundled_server_config_never_spawns_a_subprocess
func TestBundledServerConfigNeverSpawnsSubprocess(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp_servers.json"
	content := `{"terminal-tools": {"transport": "stdio", "command": "definitely-not-a-binary", "args": ["x"]}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	registry.newMCPClient = func(*MCPServerConfig) MCPClient {
		t.Fatal("bundled server spawned an MCP client")
		return nil
	}
	installHarnessBuilder(registry, map[string][]HarnessTool{
		"terminal-tools": {harnessTool("terminal_exec", "command"), harnessTool("terminal_rg", "pattern")},
	})

	registry.LoadMCPConfig(path)

	if len(registry.mcpClients) != 0 {
		t.Fatalf("mcp clients = %d, want 0", len(registry.mcpClients))
	}
	if _, ok := registry.GetTool("terminal_exec"); !ok {
		t.Fatal("terminal_exec not registered")
	}
	hasNames(t, registry.GetServerToolNames("terminal-tools"), "terminal_exec", "terminal_rg")
}

// Parity: core/tests/test_harness_tools.py::test_context_params_hidden_from_schema_and_injected
func TestContextParamsHiddenFromSchemaAndInjected(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetMCPExtraEnv(map[string]string{"HIVE_QUEEN_ID": "queen_test"})

	var received map[string]any
	terminal := harnessTool("terminal_exec", "command", "shell", "env", "session_cwd")
	terminal.Invoke = func(inputs map[string]any) (any, error) {
		received = inputs
		return map[string]any{"stdout": "ok"}, nil
	}
	installHarnessBuilder(registry, map[string][]HarnessTool{"terminal-tools": {terminal}})

	sessionDir := t.TempDir()
	registry.SetSessionContext(map[string]any{
		"session_cwd": sessionDir,
		"principal":   "agent:test",
		"profile":     "sess-1",
	})
	registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "terminal-tools", Transport: "harness"}},
		LoadRegistryOptions{LogSummary: boolPtr(false)})

	schema := registry.GetTools()["terminal_exec"].Parameters
	properties := schema["properties"].(map[string]any)
	if _, ok := properties["session_cwd"]; ok {
		t.Fatal("session_cwd must not appear in the LLM-facing schema")
	}

	_, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "t", Name: "terminal_exec",
		Input: map[string]any{"command": "echo $HIVE_PRINCIPAL $HIVE_QUEEN_ID $HIVE_BROWSER_SESSION", "shell": true},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if received == nil {
		t.Fatal("harness tool was never invoked")
	}
	env, ok := received["env"].(map[string]any)
	if !ok {
		t.Fatalf("env = %v (%T)", received["env"], received["env"])
	}
	if env["HIVE_PRINCIPAL"] != "agent:test" {
		t.Fatalf("HIVE_PRINCIPAL = %v", env["HIVE_PRINCIPAL"])
	}
	if env["HIVE_QUEEN_ID"] != "queen_test" {
		t.Fatalf("HIVE_QUEEN_ID = %v", env["HIVE_QUEEN_ID"])
	}
	if env["HIVE_BROWSER_SESSION"] != "sess-1" {
		t.Fatalf("HIVE_BROWSER_SESSION = %v", env["HIVE_BROWSER_SESSION"])
	}
	if env["HIVE_STORAGE_PATH"] != sessionDir {
		t.Fatalf("HIVE_STORAGE_PATH = %v", env["HIVE_STORAGE_PATH"])
	}
	if received["session_cwd"] != sessionDir {
		t.Fatalf("session_cwd = %v, want %s", received["session_cwd"], sessionDir)
	}
}

// Parity: core/tests/test_harness_tools.py::test_image_results_become_image_content
func TestImageResultsBecomeImageContent(t *testing.T) {
	dir := t.TempDir()
	jpg := dir + "/shot.jpg"
	if err := os.WriteFile(jpg, []byte("\xff\xd8\xff\xe0fakejpeg"), 0o600); err != nil {
		t.Fatal(err)
	}

	attach := harnessTool("attach_file", "paths")
	attach.Invoke = func(map[string]any) (any, error) {
		return map[string]any{
			"_text": "attached",
			"_images": []map[string]any{{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:image/jpeg;base64,ZmFrZQ=="},
			}},
		}, nil
	}
	terminal := harnessTool("terminal_exec", "command", "shell")
	terminal.Invoke = func(map[string]any) (any, error) {
		payload, _ := json.Marshal(map[string]any{"ok": true, "_image": map[string]any{"path": jpg}})
		return string(payload), nil
	}
	registry := NewToolRegistry()
	installHarnessBuilder(registry, map[string][]HarnessTool{
		"files-tools":    {attach},
		"terminal-tools": {terminal},
	})
	registry.LoadRegistryServers([]MCPServerSpec{
		{Name: "files-tools", Transport: "harness"},
		{Name: "terminal-tools", Transport: "harness"},
	}, LoadRegistryOptions{LogSummary: boolPtr(false)})

	executor := registry.GetExecutor()
	attached, err := executor.Execute(context.Background(), ToolUse{
		ID: "a", Name: "attach_file", Input: map[string]any{"paths": jpg},
	})
	if err != nil {
		t.Fatalf("attach_file error: %v", err)
	}
	if len(attached.ImageContent) == 0 {
		t.Fatal("attach_file returned no image content")
	}
	if url, _ := attached.ImageContent[0]["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(url, "data:image/jpeg;base64,") {
		t.Fatalf("image url = %q", url)
	}

	outJSON := dir + "/out.json"
	if err := os.WriteFile(outJSON, []byte(`{"ok": true, "_image": {"path": "`+jpg+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	shot, err := executor.Execute(context.Background(), ToolUse{
		ID: "s", Name: "terminal_exec", Input: map[string]any{"command": "cat out.json", "shell": true},
	})
	if err != nil {
		t.Fatalf("terminal_exec error: %v", err)
	}
	if len(shot.ImageContent) == 0 {
		t.Fatal("hive-browser screenshot was not re-inlined")
	}
}

// Parity: core/tests/test_harness_tools.py::test_memory_search_is_scoped_to_the_owning_registry
func TestHarnessGroupScopeComesFromOwningRegistry(t *testing.T) {
	t.Setenv("HIVE_QUEEN_ID", "wrong-process-queen")

	registry := NewToolRegistry()
	registry.SetMCPExtraEnv(map[string]string{"HIVE_QUEEN_ID": "queen_test"})

	var scopeEnv func() map[string]string
	registry.harnessGroupBuilderFn = func(_ string, scope func() map[string]string) []HarnessTool {
		scopeEnv = scope
		return []HarnessTool{harnessTool("search_messages", "pattern")}
	}
	registry.RegisterMCPServer(MCPServerSpec{Name: "memory-tools", Transport: "harness"},
		RegisterMCPServerOptions{})

	if scopeEnv == nil {
		t.Fatal("harness group builder was never called")
	}
	if got := scopeEnv()["HIVE_QUEEN_ID"]; got != "queen_test" {
		t.Fatalf("HIVE_QUEEN_ID = %q, want the registry's queen_test", got)
	}
}

// Parity-exempt: terminal_exec's argument coercion and its exact "Invalid
func TestHarnessArgumentsPassThroughUncoerced(t *testing.T) {
	registry := NewToolRegistry()
	var received map[string]any
	terminal := harnessTool("terminal_exec", "command")
	terminal.Invoke = func(inputs map[string]any) (any, error) {
		received = inputs
		return map[string]any{"ok": true}, nil
	}
	installHarnessBuilder(registry, map[string][]HarnessTool{"terminal-tools": {terminal}})
	registry.RegisterMCPServer(MCPServerSpec{Name: "terminal-tools", Transport: "harness"},
		RegisterMCPServerOptions{})

	_, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "t", Name: "terminal_exec", Input: map[string]any{"timeout_sec": "soon"},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got, _ := received["timeout_sec"].(string); got != "soon" {
		t.Fatalf("timeout_sec = %v, registry coerced it", received["timeout_sec"])
	}
}

// Parity-exempt: Go has no coroutine protocol; the harness bridge invokes tools
func TestHarnessInvocationIsSynchronous(t *testing.T) {
	registry := NewToolRegistry()
	called := false
	fetch := harnessTool("fetch", "url")
	fetch.Invoke = func(inputs map[string]any) (any, error) {
		called = true
		return map[string]any{"url": inputs["url"]}, nil
	}
	installHarnessBuilder(registry, map[string][]HarnessTool{"hive_tools": {fetch}})
	registry.RegisterMCPServer(MCPServerSpec{Name: "hive_tools", Transport: "harness"},
		RegisterMCPServerOptions{})

	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "f", Name: "fetch", Input: map[string]any{"url": "u"},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !called {
		t.Fatal("tool was not invoked synchronously")
	}
	if !strings.Contains(result.Content, `"url": "u"`) {
		t.Fatalf("content = %q", result.Content)
	}
}

// Parity: core/tests/test_harness_tools.py::test_group_registration_is_idempotent_and_survives_resync
func TestGroupRegistrationIdempotentAndSurvivesResync(t *testing.T) {
	dir := t.TempDir()
	configPath := dir + "/mcp_servers.json"
	if err := os.WriteFile(configPath, []byte(`{"servers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}

	registry := NewToolRegistry()
	installHarnessBuilder(registry, map[string][]HarnessTool{
		"terminal-tools": {harnessTool("terminal_exec", "command"), harnessTool("terminal_rg", "pattern")},
	})
	registry.RegisterMCPServer(MCPServerSpec{Name: "terminal-tools", Transport: "harness"},
		RegisterMCPServerOptions{})
	before := registry.GetTools()
	if count := registry.RegisterMCPServer(MCPServerSpec{Name: "terminal-tools", Transport: "stdio"},
		RegisterMCPServerOptions{}); count != 0 {
		t.Fatalf("second registration count = %d, want 0", count)
	}
	if len(registry.GetTools()) != len(before) {
		t.Fatalf("second registration changed the tool set: %v", registry.RegisteredNames())
	}

	registry.mcpClients = append(registry.mcpClients, &mcpClientRecord{
		client:     newFakeMCPClient(&MCPServerConfig{Name: "stub"}),
		serverName: "stub",
	})
	registry.mcpConfigPath = configPath
	registry.hasMCPConfigPath = true
	if !registry.ResyncMCPServersIfNeeded(true) {
		t.Fatal("forced resync returned false")
	}

	hasNames(t, registry.GetServerToolNames("terminal-tools"), "terminal_exec", "terminal_rg")
	if _, ok := registry.GetFullMCPCatalog()["terminal-tools"]; !ok {
		t.Fatal("terminal-tools catalog lost across resync")
	}
}

// Parity: core/tests/test_harness_tools.py::test_memory_scope_follows_a_live_colony_binding
func TestMemoryScopeFollowsLiveColonyBinding(t *testing.T) {
	registry := NewToolRegistry()
	colony := ""
	registry.SetIdentityEnvProvider(func() map[string]string {
		if colony != "" {
			return map[string]string{"HIVE_COLONY_NAME": colony}
		}
		return map[string]string{"HIVE_QUEEN_ID": "queen_ops"}
	})
	registry.harnessGroupBuilderFn = func(_ string, scope func() map[string]string) []HarnessTool {
		search := harnessTool("search_messages", "pattern")
		search.Invoke = func(map[string]any) (any, error) {
			env := scope()
			if name := env["HIVE_COLONY_NAME"]; name != "" {
				return map[string]any{"scope": "colony", "colony": name}, nil
			}
			return map[string]any{"scope": "queen", "queen": env["HIVE_QUEEN_ID"]}, nil
		}
		return []HarnessTool{search}
	}
	registry.RegisterMCPServer(MCPServerSpec{Name: "memory-tools", Transport: "harness"},
		RegisterMCPServerOptions{})

	call := func() map[string]any {
		result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
			ID: "m", Name: "search_messages", Input: map[string]any{"pattern": "FACT"},
		})
		if err != nil {
			t.Fatalf("Execute error: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(result.Content), &out); err != nil {
			t.Fatalf("content %q: %v", result.Content, err)
		}
		return out
	}

	if out := call(); out["scope"] != "queen" || out["queen"] != "queen_ops" {
		t.Fatalf("first call = %v", out)
	}
	colony = "acme"
	if out := call(); out["scope"] != "colony" || out["colony"] != "acme" {
		t.Fatalf("second call = %v", out)
	}
}

// Parity-exempt: the openpyxl-conditional part of the hive_tools catalog is
func TestHiveToolsGroupDispatchesInProcess(t *testing.T) {
	registry := NewToolRegistry()
	installHarnessBuilder(registry, map[string][]HarnessTool{
		"hive_tools": {harnessTool("csv_sql", "query")},
	})
	if _, ok := HarnessGroupNames["hive_tools"]; !ok {
		t.Fatal("hive_tools must be a harness group name")
	}
	registry.newMCPClient = func(*MCPServerConfig) MCPClient {
		t.Fatal("hive_tools spawned an MCP client")
		return nil
	}
	count := registry.RegisterMCPServer(MCPServerSpec{Name: "hive_tools", Transport: "harness"},
		RegisterMCPServerOptions{})
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := registry.GetTool("csv_sql"); !ok {
		t.Fatal("csv_sql not registered")
	}
}
