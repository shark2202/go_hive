package tools

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCall struct {
	tool string
	args map[string]any
}

type fakeMCPClient struct {
	mu            sync.Mutex
	config        *MCPServerConfig
	tools         []MCPTool
	connectErr    error
	callErr       error
	connectCalls  int
	disconnectCnt int
	calls         []fakeCall
}

func newFakeMCPClient(config *MCPServerConfig, tools ...MCPTool) *fakeMCPClient {
	if config == nil {
		config = &MCPServerConfig{}
	}
	return &fakeMCPClient{config: config, tools: tools}
}

func tool(name string, props ...string) MCPTool {
	properties := map[string]any{}
	for _, p := range props {
		properties[p] = map[string]any{"type": "string"}
	}
	return MCPTool{
		Name:        name,
		Description: name,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   []any{},
		},
	}
}

func (c *fakeMCPClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connectCalls++
	return c.connectErr
}

func (c *fakeMCPClient) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disconnectCnt++
	return nil
}

func (c *fakeMCPClient) ListTools() []MCPTool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]MCPTool(nil), c.tools...)
}

func (c *fakeMCPClient) CallTool(name string, arguments map[string]any) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, fakeCall{tool: name, args: arguments})
	if c.callErr != nil {
		return nil, c.callErr
	}
	return map[string]any{"result": "ok"}, nil
}

func (c *fakeMCPClient) Config() *MCPServerConfig { return c.config }

func (c *fakeMCPClient) callArgs(name string) (map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, call := range c.calls {
		if call.tool == name {
			return call.args, true
		}
	}
	return nil, false
}

type fakeConnectionManager struct {
	mu       sync.Mutex
	acquires []string
	releases []string
	client   MCPClient
	acquire  int
	err      error
}

func (m *fakeConnectionManager) Acquire(config *MCPServerConfig) (MCPClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquire++
	m.acquires = append(m.acquires, config.Name)
	if m.err != nil {
		return nil, m.err
	}
	if m.client != nil {
		return m.client, nil
	}
	return newFakeMCPClient(config), nil
}

func (m *fakeConnectionManager) Release(serverName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases = append(m.releases, serverName)
	return nil
}

type logCapture struct {
	mu      sync.Mutex
	records []LogRecord
}

func (c *logCapture) sink(rec LogRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.records = append(c.records, rec)
}

func (c *logCapture) all() []LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]LogRecord(nil), c.records...)
}

func (c *logCapture) findMessage(substr string) (LogRecord, bool) {
	for _, rec := range c.all() {
		if strings.Contains(rec.Message, substr) {
			return rec, true
		}
	}
	return LogRecord{}, false
}

func installLogCapture(r *ToolRegistry) *logCapture {
	capture := &logCapture{}
	r.SetLogSink(capture.sink)
	return capture
}

func fakeFactory(clients map[string]MCPClient) MCPClientFactory {
	return func(config *MCPServerConfig) MCPClient {
		if client, ok := clients[config.Name]; ok {
			return client
		}
		return newFakeMCPClient(config)
	}
}

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func hasNames(t *testing.T, got map[string]struct{}, want ...string) {
	t.Helper()
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("tool %q missing from %v", name, got)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_discover_from_module_handles_invalid_json
func TestDiscoverFromModuleHandlesInvalidJSON(t *testing.T) {
	registry := NewToolRegistry()
	module := &Module{
		Tools: map[string]Tool{
			"bad_tool": {Name: "bad_tool", Description: "malformed"},
		},
		ToolExecutor: func(ToolUse) ToolResult {
			return ToolResult{ToolUseID: "x", Content: "not {valid json"}
		},
	}
	if count := registry.DiscoverFromModule(module); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	registered, ok := registry.tools["bad_tool"]
	if !ok {
		t.Fatal("bad_tool not registered")
	}
	result, err := registered.Executor(map[string]any{})
	if err != nil {
		t.Fatalf("executor returned error: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T, want map", result)
	}
	if payload["raw_content"] != "not {valid json" {
		t.Fatalf("raw_content = %v", payload["raw_content"])
	}
	if _, ok := payload["error"]; !ok {
		t.Fatal("error key missing")
	}
}

// Parity: core/tests/test_tool_registry.py::test_discover_from_module_handles_empty_content
func TestDiscoverFromModuleHandlesEmptyContent(t *testing.T) {
	registry := NewToolRegistry()
	module := &Module{
		Tools: map[string]Tool{
			"empty_tool": {Name: "empty_tool"},
		},
		ToolExecutor: func(ToolUse) ToolResult { return ToolResult{} },
	}
	if count := registry.DiscoverFromModule(module); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	result, err := registry.tools["empty_tool"].Executor(map[string]any{})
	if err != nil {
		t.Fatalf("executor returned error: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok || len(payload) != 0 {
		t.Fatalf("result = %v (%T), want empty map", result, result)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_mcp_server_uses_connection_manager_when_enabled
func TestRegisterMCPServerUsesConnectionManagerWhenEnabled(t *testing.T) {
	registry := NewToolRegistry()
	client := newFakeMCPClient(&MCPServerConfig{Name: "shared"}, tool("pooled_tool"))
	manager := &fakeConnectionManager{client: client}
	registry.mcpManager = manager

	enable := true
	count := registry.RegisterMCPServer(MCPServerSpec{Name: "shared", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &enable})
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if len(manager.acquires) != 1 || manager.acquires[0] != "shared" {
		t.Fatalf("acquires = %v", manager.acquires)
	}

	registry.Cleanup()
	if len(manager.releases) != 1 || manager.releases[0] != "shared" {
		t.Fatalf("releases = %v", manager.releases)
	}
	if client.disconnectCnt != 0 {
		t.Fatalf("disconnect calls = %d, want 0 for a managed client", client.disconnectCnt)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_mcp_server_defaults_to_connection_manager
func TestRegisterMCPServerDefaultsToConnectionManager(t *testing.T) {
	registry := NewToolRegistry()
	manager := &fakeConnectionManager{client: newFakeMCPClient(&MCPServerConfig{Name: "direct"}, tool("direct_tool"))}
	registry.mcpManager = manager

	count := registry.RegisterMCPServer(MCPServerSpec{Name: "direct", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{})
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if manager.acquire != 1 {
		t.Fatalf("manager acquisitions = %d, want 1", manager.acquire)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_mcp_server_direct_client_when_manager_disabled
func TestRegisterMCPServerDirectClientWhenManagerDisabled(t *testing.T) {
	registry := NewToolRegistry()
	client := newFakeMCPClient(&MCPServerConfig{Name: "direct"}, tool("direct_tool"))
	registry.newMCPClient = func(*MCPServerConfig) MCPClient { return client }

	disable := false
	count := registry.RegisterMCPServer(MCPServerSpec{Name: "direct", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &disable})
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if client.connectCalls != 1 {
		t.Fatalf("connect calls = %d, want 1", client.connectCalls)
	}

	registry.Cleanup()
	if client.disconnectCnt != 1 {
		t.Fatalf("disconnect calls = %d, want 1", client.disconnectCnt)
	}
}

// Parity: core/tests/test_tool_registry.py::test_full_mcp_catalog_omits_tools_shadowed_by_earlier_server
func TestFullMCPCatalogOmitsShadowedTools(t *testing.T) {
	registry := NewToolRegistry()
	disable := false
	clients := map[string]MCPClient{
		"alpha-tools": newFakeMCPClient(&MCPServerConfig{Name: "alpha-tools"},
			tool("read_file"), tool("write_file")),
		"beta-tools": newFakeMCPClient(&MCPServerConfig{Name: "beta-tools"},
			tool("read_file"), tool("write_file"), tool("pdf_read")),
	}
	registry.newMCPClient = fakeFactory(clients)

	for _, name := range []string{"alpha-tools", "beta-tools"} {
		registry.RegisterMCPServer(MCPServerSpec{Name: name, Transport: "stdio", Command: "echo"},
			RegisterMCPServerOptions{UseConnectionManager: &disable})
	}

	catalog := registry.GetFullMCPCatalog()
	alphaNames := catalogNames(catalog["alpha-tools"])
	betaNames := catalogNames(catalog["beta-tools"])
	if strings.Join(alphaNames, ",") != "read_file,write_file" {
		t.Fatalf("alpha catalog = %v", alphaNames)
	}
	if strings.Join(betaNames, ",") != "pdf_read" {
		t.Fatalf("beta catalog = %v", betaNames)
	}
	hasNames(t, registry.GetServerToolNames("alpha-tools"), "read_file", "write_file")
	hasNames(t, registry.GetServerToolNames("beta-tools"), "pdf_read")
	if _, ok := registry.GetServerToolNames("beta-tools")["read_file"]; ok {
		t.Fatal("beta-tools should not own the shadowed read_file")
	}
}

func catalogNames(entries []map[string]any) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, fmt.Sprint(entry["name"]))
	}
	return out
}

// Parity: core/tests/test_tool_registry.py::test_load_registry_servers_retries_when_registration_returns_zero
func TestLoadRegistryServersRetriesWhenRegistrationReturnsZero(t *testing.T) {
	registry := NewToolRegistry()
	attempts := 0
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		attempts++
		if attempts == 1 {
			return newFakeMCPClient(config)
		}
		return newFakeMCPClient(config, tool("jira_a"), tool("jira_b"))
	}
	registry.sleepFn = func(time.Duration) {}

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "jira", Transport: "http", URL: "http://localhost:4010"}},
		LoadRegistryOptions{LogSummary: boolPtr(false)})

	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	got := results[0]
	if got.Server != "jira" || got.Status != "loaded" || got.ToolsLoaded != 2 || got.SkippedReason != nil {
		t.Fatalf("result = %+v", got)
	}
}

// Parity: core/tests/test_tool_registry.py::test_load_registry_servers_marks_failures_as_skipped
func TestLoadRegistryServersMarksFailuresAsSkipped(t *testing.T) {
	registry := NewToolRegistry()
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		return newFakeMCPClient(config)
	}
	registry.sleepFn = func(time.Duration) {}

	results := registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "jira", Transport: "http", URL: "http://localhost:4010"}},
		LoadRegistryOptions{LogSummary: boolPtr(false)})

	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	got := results[0]
	if got.Server != "jira" || got.Status != "skipped" || got.ToolsLoaded != 0 {
		t.Fatalf("result = %+v", got)
	}
	if got.SkippedReason == nil || *got.SkippedReason != "registered 0 tools" {
		t.Fatalf("skipped_reason = %v", got.SkippedReason)
	}
}

// Parity: core/tests/test_tool_registry.py::test_load_registry_servers_emits_structured_log_fields
func TestLoadRegistryServersEmitsStructuredLogFields(t *testing.T) {
	registry := NewToolRegistry()
	capture := installLogCapture(registry)
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		return newFakeMCPClient(config, tool("jira_a"), tool("jira_b"))
	}

	registry.LoadRegistryServers(
		[]MCPServerSpec{{Name: "jira", Transport: "http", URL: "http://localhost:4010"}},
		LoadRegistryOptions{LogSummary: boolPtr(true)})

	rec, ok := capture.findMessage("MCP registry server resolution")
	if !ok {
		t.Fatalf("resolution log missing; records = %+v", capture.all())
	}
	want := map[string]any{
		"event":          "mcp_registry_server_resolution",
		"server":         "jira",
		"status":         "loaded",
		"tools_loaded":   2,
		"skipped_reason": nil,
	}
	for key, expected := range want {
		if rec.Fields[key] != expected {
			t.Fatalf("field %s = %v, want %v", key, rec.Fields[key], expected)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_load_registry_servers_exempts_essential_servers_from_cap
func TestLoadRegistryServersExemptsEssentialServersFromCap(t *testing.T) {
	registry := NewToolRegistry()
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		switch config.Name {
		case "jira":
			return newFakeMCPClient(config, tool("jira_a"), tool("jira_b"))
		case "extra":
			return newFakeMCPClient(config, tool("e1"), tool("e2"), tool("e3"), tool("e4"), tool("e5"))
		}
		return newFakeMCPClient(config)
	}
	registry.harnessGroupBuilderFn = func(group string, _ func() map[string]string) []HarnessTool {
		return []HarnessTool{{Name: "chart_render", Description: "render"}}
	}
	registry.sleepFn = func(time.Duration) {}

	maxTools := 2
	results := registry.LoadRegistryServers(
		[]MCPServerSpec{
			{Name: "jira", Transport: "http", URL: "http://localhost:4010"},
			{Name: "extra", Transport: "http", URL: "http://localhost:4011"},
			{Name: "chart-tools", Transport: "stdio"},
		},
		LoadRegistryOptions{LogSummary: boolPtr(false), MaxTools: &maxTools})

	byName := map[string]ServerResolution{}
	for _, res := range results {
		byName[res.Server] = res
	}
	if byName["jira"].Status != "loaded" {
		t.Fatalf("jira status = %q", byName["jira"].Status)
	}
	if byName["extra"].Status != "skipped" {
		t.Fatalf("extra status = %q", byName["extra"].Status)
	}
	if byName["extra"].SkippedReason == nil || !strings.Contains(*byName["extra"].SkippedReason, "budget exhausted") {
		t.Fatalf("extra skipped_reason = %v", byName["extra"].SkippedReason)
	}
	if byName["chart-tools"].Status != "loaded" || byName["chart-tools"].ToolsLoaded != 1 {
		t.Fatalf("chart-tools result = %+v", byName["chart-tools"])
	}
}

func boolPtr(v bool) *bool { return &v }

// Parity: core/tests/test_tool_registry.py::test_tool_execution_error_logs_stack_trace_and_context
func TestToolExecutionErrorLogsStackAndContext(t *testing.T) {
	registry := NewToolRegistry()
	capture := installLogCapture(registry)
	registry.Register("failing_tool",
		Tool{Name: "failing_tool", Description: "fails"},
		func(map[string]any) (any, error) { return nil, fmt.Errorf("Intentional test failure") })

	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "test_call_123", Name: "failing_tool", Input: map[string]any{"param": "value"},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false")
	}
	if !strings.Contains(result.Content, "Intentional test failure") {
		t.Fatalf("content = %q", result.Content)
	}
	var sawTool, sawID bool
	for _, rec := range capture.all() {
		if strings.Contains(rec.Message, "failing_tool") {
			sawTool = true
		}
		if strings.Contains(rec.Message, "test_call_123") {
			sawID = true
		}
		if rec.Level == "error" && rec.Err == nil {
			t.Fatal("error record has no Err (Python exc_info analogue)")
		}
	}
	if !sawTool || !sawID {
		t.Fatalf("log records = %+v", capture.all())
	}
}

// Parity: core/tests/test_tool_registry.py::test_tool_execution_error_logs_inputs
func TestToolExecutionErrorLogsInputs(t *testing.T) {
	registry := NewToolRegistry()
	capture := installLogCapture(registry)
	registry.Register("input_logging_tool",
		Tool{Name: "input_logging_tool"},
		func(map[string]any) (any, error) { return nil, fmt.Errorf("Tool failed") })

	_, _ = registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_456", Name: "input_logging_tool",
		Input: map[string]any{"foo": "bar", "nested": map[string]any{"key": "value"}},
	})

	var combined strings.Builder
	for _, rec := range capture.all() {
		combined.WriteString(rec.Message)
		combined.WriteString(" ")
	}
	if !strings.Contains(combined.String(), `"foo": "bar"`) {
		t.Fatalf("inputs not logged: %s", combined.String())
	}
}

// Parity: core/tests/test_tool_registry.py::test_unknown_tool_error_returns_proper_result
func TestUnknownToolErrorReturnsProperResult(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register("existent_tool", Tool{Name: "existent_tool"},
		func(map[string]any) (any, error) { return "ok", nil })

	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "unknown_call", Name: "existant_tool", Input: map[string]any{},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.IsError {
		t.Fatal("result.IsError = false")
	}
	for _, want := range []string{"Unknown tool", "existant_tool", "Did you mean 'existent_tool'?"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("content %q missing %q", result.Content, want)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_tool_execution_error_truncates_large_inputs
func TestToolExecutionErrorTruncatesLargeInputs(t *testing.T) {
	registry := NewToolRegistry()
	capture := installLogCapture(registry)
	registry.Register("large_input_tool", Tool{Name: "large_input_tool"},
		func(map[string]any) (any, error) { return nil, fmt.Errorf("Tool failed") })

	_, _ = registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_789", Name: "large_input_tool",
		Input: map[string]any{"data": strings.Repeat("x", 1000)},
	})

	var combined strings.Builder
	for _, rec := range capture.all() {
		combined.WriteString(rec.Message)
		combined.WriteString(" ")
	}
	if !strings.Contains(combined.String(), "...(truncated)") {
		t.Fatalf("large inputs not truncated: %s", combined.String())
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_function_infers_type_hints
func TestRegisterFunctionInfersTypeHints(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("my_func", "", []FuncParam{
		{Name: "a", Type: "int"},
		{Name: "b", Type: "float"},
		{Name: "c", Type: "bool"},
		{Name: "d", Type: "dict"},
		{Name: "e", Type: "list"},
		{Name: "f", Type: "str", HasDefault: true},
	}, func(map[string]any) (any, error) { return nil, nil })

	tool := registry.GetTools()["my_func"]
	props := tool.Parameters["properties"].(map[string]any)
	want := map[string]string{
		"a": "integer", "b": "number", "c": "boolean",
		"d": "object", "e": "array", "f": "string",
	}
	for name, typ := range want {
		prop, ok := props[name].(map[string]any)
		if !ok || prop["type"] != typ {
			t.Fatalf("%s type = %v, want %s", name, props[name], typ)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_function_required_vs_optional
func TestRegisterFunctionRequiredVsOptional(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("my_func", "", []FuncParam{
		{Name: "required_param", Type: "str"},
		{Name: "optional_param", Type: "int", HasDefault: true},
	}, func(map[string]any) (any, error) { return nil, nil })

	required := registry.GetTools()["my_func"].Parameters["required"].([]string)
	sawRequired := false
	for _, name := range required {
		if name == "required_param" {
			sawRequired = true
		}
		if name == "optional_param" {
			t.Fatal("optional_param must not be required")
		}
	}
	if !sawRequired {
		t.Fatalf("required = %v", required)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_function_custom_name_and_description
func TestRegisterFunctionCustomNameAndDescription(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("custom_name", "Custom desc", nil,
		func(map[string]any) (any, error) { return nil, nil })

	tools := registry.GetTools()
	if _, ok := tools["custom_name"]; !ok {
		t.Fatal("custom_name not registered")
	}
	if _, ok := tools["original_name"]; ok {
		t.Fatal("original_name should not be registered")
	}
	if tools["custom_name"].Description != "Custom desc" {
		t.Fatalf("description = %q", tools["custom_name"].Description)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_function_falls_back_to_docstring
func TestRegisterFunctionFallsBackToDefaultDescription(t *testing.T) {
	registry := NewToolRegistry()
	registry.RegisterFunction("my_tool", "", nil, func(map[string]any) (any, error) { return nil, nil })

	if got := registry.GetTools()["my_tool"].Description; got != "Execute my_tool" {
		t.Fatalf("description = %q", got)
	}
}

// Parity: core/tests/test_tool_registry.py::test_register_function_executor_calls_function
func TestRegisterFunctionExecutorCallsFunction(t *testing.T) {
	registry := NewToolRegistry()
	var calls [][2]int
	registry.RegisterFunction("multiply", "", []FuncParam{
		{Name: "x", Type: "int"}, {Name: "y", Type: "int"},
	}, func(inputs map[string]any) (any, error) {
		x, _ := inputs["x"].(int)
		y, _ := inputs["y"].(int)
		calls = append(calls, [2]int{x, y})
		return x * y, nil
	})

	result, err := registry.GetExecutor().Execute(context.Background(), ToolUse{
		ID: "call_1", Name: "multiply", Input: map[string]any{"x": 3, "y": 4},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if len(calls) != 1 || calls[0] != [2]int{3, 4} {
		t.Fatalf("calls = %v", calls)
	}
	if !strings.Contains(result.Content, "12") {
		t.Fatalf("content = %q", result.Content)
	}
}

// Parity: core/tests/test_tool_registry.py::test_discover_from_module_finds_tool_decorated_functions
func TestDiscoverFromModuleFindsDecoratedFunctions(t *testing.T) {
	registry := NewToolRegistry()
	module := &Module{Decorated: []Decorate{{
		Name:        "greet",
		Description: "Say hello",
		Params:      []FuncParam{{Name: "name", Type: "str"}},
		Exec:        func(map[string]any) (any, error) { return "Hello", nil },
	}}}
	if count := registry.DiscoverFromModule(module); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := registry.GetTools()["greet"]; !ok {
		t.Fatal("greet not registered")
	}
}

// Parity: core/tests/test_tool_registry.py::test_discover_from_module_returns_zero_for_missing_file
func TestDiscoverFromModuleReturnsZeroForMissingFile(t *testing.T) {
	registry := NewToolRegistry()
	if count := registry.DiscoverFromModulePath(t.TempDir() + "/nonexistent.py"); count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

// Parity: core/tests/test_tool_registry.py::test_discover_from_module_registers_mock_executor_without_tool_executor
func TestDiscoverFromModuleRegistersMockExecutor(t *testing.T) {
	registry := NewToolRegistry()
	module := &Module{Tools: map[string]Tool{
		"mock_tool": {Name: "mock_tool", Description: "no executor"},
	}}
	if count := registry.DiscoverFromModule(module); count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	result, err := registry.tools["mock_tool"].Executor(map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("executor error: %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result type = %T", result)
	}
	if payload["mock"] != true {
		t.Fatalf("mock = %v", payload["mock"])
	}
	inputs, _ := payload["inputs"].(map[string]any)
	if inputs["foo"] != "bar" {
		t.Fatalf("inputs = %v", inputs)
	}
}

// Parity: core/tests/test_tool_registry.py::test_has_tool_returns_true_for_registered_tool
func TestHasToolReturnsTrueForRegisteredTool(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register("t", Tool{Name: "t"}, func(inputs map[string]any) (any, error) { return inputs, nil })
	if !registry.HasTool("t") {
		t.Fatal("HasTool(t) = false")
	}
}

// Parity: core/tests/test_tool_registry.py::test_has_tool_returns_false_for_missing_tool
func TestHasToolReturnsFalseForMissingTool(t *testing.T) {
	registry := NewToolRegistry()
	if registry.HasTool("not_there") {
		t.Fatal("HasTool(not_there) = true")
	}
}

// Parity: core/tests/test_tool_registry.py::test_get_registered_names_lists_all_tools
func TestRegisteredNamesListsAllTools(t *testing.T) {
	registry := NewToolRegistry()
	for _, name := range []string{"alpha", "beta", "gamma"} {
		n := name
		registry.Register(n, Tool{Name: n}, func(inputs map[string]any) (any, error) { return inputs, nil })
	}
	got := map[string]struct{}{}
	for _, name := range registry.RegisteredNames() {
		got[name] = struct{}{}
	}
	if len(got) != 3 {
		t.Fatalf("names = %v", got)
	}
	hasNames(t, got, "alpha", "beta", "gamma")
}

// Parity: core/tests/test_tool_registry.py::test_session_context_is_injected_into_mcp_tool_call
func TestSessionContextInjectedIntoMCPToolCall(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetSessionContext(map[string]any{"workspace_id": "ws-123", "agent_id": "agent-99"})
	client := newFakeMCPClient(&MCPServerConfig{Name: "ctx-server"}, tool("ctx_tool", "workspace_id", "agent_id"))
	registry.newMCPClient = func(*MCPServerConfig) MCPClient { return client }

	disable := false
	registry.RegisterMCPServer(MCPServerSpec{Name: "ctx-server", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &disable})

	_, err := registry.GetExecutor().Execute(context.Background(), ToolUse{ID: "c1", Name: "ctx_tool", Input: map[string]any{}})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	args, ok := client.callArgs("ctx_tool")
	if !ok {
		t.Fatal("call_tool was never called")
	}
	if args["workspace_id"] != "ws-123" || args["agent_id"] != "agent-99" {
		t.Fatalf("arguments = %v", args)
	}
}

// Parity: core/tests/test_tool_registry.py::test_principal_is_injected_into_terminal_tool_env
func TestPrincipalInjectedIntoTerminalToolEnv(t *testing.T) {
	registry := NewToolRegistry()
	client := newFakeMCPClient(&MCPServerConfig{Name: "term"},
		tool("terminal_exec", "command", "env"), tool("plain_tool", "x"))
	registry.newMCPClient = func(*MCPServerConfig) MCPClient { return client }
	disable := false
	registry.RegisterMCPServer(MCPServerSpec{Name: "term", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &disable})

	token := SetExecutionContext(map[string]any{"principal": "colony:acme:queen"})
	defer ResetExecutionContext(token)

	executor := registry.GetExecutor()
	_, _ = executor.Execute(context.Background(), ToolUse{ID: "c1", Name: "terminal_exec",
		Input: map[string]any{"command": "hive-crm whoami", "env": map[string]any{"FOO": "bar"}}})
	_, _ = executor.Execute(context.Background(), ToolUse{ID: "c2", Name: "plain_tool",
		Input: map[string]any{"x": "y"}})

	args, _ := client.callArgs("terminal_exec")
	env, ok := args["env"].(map[string]any)
	if !ok {
		t.Fatalf("env = %v (%T)", args["env"], args["env"])
	}
	if env["FOO"] != "bar" || env["HIVE_PRINCIPAL"] != "colony:acme:queen" {
		t.Fatalf("env = %v", env)
	}
	plainArgs, _ := client.callArgs("plain_tool")
	if _, ok := plainArgs["env"]; ok {
		t.Fatalf("plain_tool received env: %v", plainArgs)
	}
}

// Parity: core/tests/test_tool_registry.py::test_execution_context_overrides_session_context
func TestExecutionContextOverridesSessionContext(t *testing.T) {
	registry := NewToolRegistry()
	registry.SetSessionContext(map[string]any{"workspace_id": "session-ws"})
	client := newFakeMCPClient(&MCPServerConfig{Name: "exec-server"}, tool("exec_tool", "workspace_id"))
	registry.newMCPClient = func(*MCPServerConfig) MCPClient { return client }
	disable := false
	registry.RegisterMCPServer(MCPServerSpec{Name: "exec-server", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &disable})

	token := SetExecutionContext(map[string]any{"workspace_id": "exec-ws"})
	_, _ = registry.GetExecutor().Execute(context.Background(), ToolUse{ID: "c2", Name: "exec_tool", Input: map[string]any{}})
	ResetExecutionContext(token)

	args, ok := client.callArgs("exec_tool")
	if !ok {
		t.Fatal("call_tool was never called")
	}
	if args["workspace_id"] != "exec-ws" {
		t.Fatalf("workspace_id = %v", args["workspace_id"])
	}
}

// Parity: core/tests/test_tool_registry.py::test_convert_mcp_tool_strips_context_params
func TestConvertMCPToolStripsContextParams(t *testing.T) {
	registry := NewToolRegistry()
	mcpTool := MCPTool{
		Name:        "some_tool",
		Description: "a tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agent_id": map[string]any{"type": "string"},
				"data_dir": map[string]any{"type": "string"},
				"query":    map[string]any{"type": "string"},
			},
			"required": []any{"agent_id", "query"},
		},
	}
	converted := registry.ConvertMCPToolToFrameworkTool(mcpTool)
	props := converted.Parameters["properties"].(map[string]any)
	if _, ok := props["agent_id"]; ok {
		t.Fatal("agent_id not stripped")
	}
	if _, ok := props["data_dir"]; ok {
		t.Fatal("data_dir not stripped")
	}
	if _, ok := props["query"]; !ok {
		t.Fatal("query should be kept")
	}
	required := converted.Parameters["required"].([]string)
	joined := strings.Join(required, ",")
	if joined != "query" {
		t.Fatalf("required = %v", required)
	}
}

// Parity: core/tests/test_tool_registry.py::test_load_mcp_config_list_format
func TestLoadMCPConfigListFormat(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp_servers.json"
	if err := os.WriteFile(path, []byte(`{"servers": [{"name": "s1", "transport": "http", "url": "http://localhost:9000"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		return newFakeMCPClient(config, tool("s1_tool"))
	}
	registry.LoadMCPConfig(path)

	if !registry.HasTool("s1_tool") {
		t.Fatalf("registered tools = %v", registry.RegisteredNames())
	}
	hasNames(t, registry.GetServerToolNames("s1"), "s1_tool")
}

// Parity: core/tests/test_tool_registry.py::test_load_mcp_config_dict_format
func TestLoadMCPConfigDictFormat(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp_servers.json"
	if err := os.WriteFile(path, []byte(`{"my-server": {"transport": "http", "url": "http://localhost:9001"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	registry.newMCPClient = func(config *MCPServerConfig) MCPClient {
		return newFakeMCPClient(config, tool("dict_tool"))
	}
	registry.LoadMCPConfig(path)

	if !registry.HasTool("dict_tool") {
		t.Fatalf("registered tools = %v", registry.RegisteredNames())
	}
	hasNames(t, registry.GetServerToolNames("my-server"), "dict_tool")
}

// Parity: core/tests/test_tool_registry.py::test_load_mcp_config_handles_invalid_json
func TestLoadMCPConfigHandlesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/bad.json"
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewToolRegistry()
	capture := installLogCapture(registry)
	registry.LoadMCPConfig(path)

	rec, ok := capture.findMessage("Failed to load MCP config")
	if !ok {
		t.Fatalf("warning missing; records = %+v", capture.all())
	}
	if rec.Level != "warning" {
		t.Fatalf("level = %q", rec.Level)
	}
	if len(registry.RegisteredNames()) != 0 {
		t.Fatalf("tools registered from invalid config: %v", registry.RegisteredNames())
	}
}

// Parity: core/tests/test_tool_registry.py::test_resync_returns_false_when_no_clients
func TestResyncReturnsFalseWhenNoClients(t *testing.T) {
	registry := NewToolRegistry()
	if registry.ResyncMCPServersIfNeeded(false) {
		t.Fatal("resync returned true with no clients")
	}
}

// Parity: core/tests/test_tool_registry.py::test_resync_returns_false_when_credentials_unchanged
func TestResyncReturnsFalseWhenCredentialsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/mcp_servers.json"
	if err := os.WriteFile(path, []byte(`{"servers": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnv(t, "ADEN_API_KEY")

	registry := NewToolRegistry()
	registry.mcpConfigPath = path
	registry.hasMCPConfigPath = true
	registry.mcpClients = append(registry.mcpClients, &mcpClientRecord{
		client:     newFakeMCPClient(&MCPServerConfig{Name: "stub"}),
		serverName: "stub",
	})
	registry.mcpCredSnapshot = map[string]struct{}{}
	registry.mcpAdenKeySnap = nil
	registry.snapshotCredentialsFn = func() map[string]struct{} { return map[string]struct{}{} }

	if registry.ResyncMCPServersIfNeeded(false) {
		t.Fatal("resync returned true with unchanged credentials")
	}
}

func convertNamedTool(registry *ToolRegistry, name string) Tool {
	return registry.ConvertMCPToolToFrameworkTool(MCPTool{Name: name, Description: name,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}})
}

// Parity: core/tests/test_tool_registry.py::TestMcpToolProducesImageFlag::test_screenshot_flagged
func TestMCPToolScreenshotFlagged(t *testing.T) {
	registry := NewToolRegistry()
	if !convertNamedTool(registry, "browser_screenshot").ProducesImage {
		t.Fatal("browser_screenshot should produce images")
	}
}

// Parity: core/tests/test_tool_registry.py::TestMcpToolProducesImageFlag::test_snapshot_not_flagged
func TestMCPToolSnapshotNotFlagged(t *testing.T) {
	registry := NewToolRegistry()
	if convertNamedTool(registry, "browser_snapshot").ProducesImage {
		t.Fatal("browser_snapshot is a DOM tree, not an image")
	}
}

// Parity: core/tests/test_tool_registry.py::TestMcpToolProducesImageFlag::test_case_insensitive_match
func TestMCPToolImageMatchCaseInsensitive(t *testing.T) {
	registry := NewToolRegistry()
	if !convertNamedTool(registry, "TakeScreenshot").ProducesImage {
		t.Fatal("TakeScreenshot should match case-insensitively")
	}
}

// Parity: core/tests/test_tool_registry.py::TestMcpToolProducesImageFlag::test_plain_tool_not_flagged
func TestMCPToolPlainToolNotFlagged(t *testing.T) {
	registry := NewToolRegistry()
	if convertNamedTool(registry, "read_file").ProducesImage {
		t.Fatal("read_file is not an image tool")
	}
}

// Parity: core/tests/test_tool_registry.py::TestMcpToolProducesImageFlag::test_image_suffix_variants_flagged
func TestMCPToolImageSuffixVariantsFlagged(t *testing.T) {
	registry := NewToolRegistry()
	for _, name := range []string{"capture_image", "render_image", "get_image", "snapshot_image"} {
		if !convertNamedTool(registry, name).ProducesImage {
			t.Fatalf("%s should be flagged", name)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_mcp_tool_conversion_marks_known_safe_tools
func TestMCPToolConversionMarksKnownSafeTools(t *testing.T) {
	registry := NewToolRegistry()
	if !convertNamedTool(registry, "terminal_rg").ConcurrencySafe {
		t.Fatal("terminal_rg should be concurrency-safe")
	}
	if convertNamedTool(registry, "execute_command").ConcurrencySafe {
		t.Fatal("execute_command must not be concurrency-safe")
	}
}

// Parity: core/tests/test_tool_registry.py::test_concurrency_safe_allowlist_is_conservative
func TestConcurrencySafeAllowlistIsConservative(t *testing.T) {
	for _, name := range []string{"pdf_read", "terminal_rg", "terminal_glob", "web_scrape"} {
		if !IsConcurrencySafe(name) {
			t.Fatalf("%s should be concurrency-safe", name)
		}
	}
	for _, name := range []string{"execute_command", "write_file", "hashline_edit", "browser_interact", "browser_navigate"} {
		if IsConcurrencySafe(name) {
			t.Fatalf("%s must not be concurrency-safe", name)
		}
	}
}

// Parity: core/tests/test_tool_registry.py::test_session_cwd_is_context_param_and_stripped_from_schema
func TestSessionCwdIsContextParamAndStripped(t *testing.T) {
	if _, ok := ContextParams["session_cwd"]; !ok {
		t.Fatal("session_cwd must be a context param")
	}
	registry := NewToolRegistry()
	converted := registry.ConvertMCPToolToFrameworkTool(MCPTool{
		Name:        "terminal_exec",
		Description: "",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command":     map[string]any{"type": "string"},
				"cwd":         map[string]any{"type": "string"},
				"session_cwd": map[string]any{"type": "string"},
			},
			"required": []any{"command"},
		},
	})
	props := converted.Parameters["properties"].(map[string]any)
	if _, ok := props["command"]; !ok {
		t.Fatal("command missing")
	}
	if _, ok := props["cwd"]; !ok {
		t.Fatal("cwd should stay LLM-overridable")
	}
	if _, ok := props["session_cwd"]; ok {
		t.Fatal("session_cwd must be stripped")
	}
}

// Parity: core/framework/loader/tool_registry.py::_register_mcp_server_with_retry
// -- the Tool Library catalog rows carry name/description/input_schema/provider
// only; server_tool is a Go-only extra that upstream clients never see.
func TestFullMCPCatalogRowKeysMatchUpstream(t *testing.T) {
	registry := NewToolRegistry()
	disable := false
	registry.newMCPClient = fakeFactory(map[string]MCPClient{
		"srv": newFakeMCPClient(&MCPServerConfig{Name: "srv"}, tool("read_file")),
	})
	registry.RegisterMCPServer(MCPServerSpec{Name: "srv", Transport: "stdio", Command: "echo"},
		RegisterMCPServerOptions{UseConnectionManager: &disable})

	entries := registry.GetFullMCPCatalog()["srv"]
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	keys := make([]string, 0, len(entries[0]))
	for key := range entries[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "description,input_schema,name,provider" {
		t.Fatalf("catalog keys = %v", keys)
	}
}
