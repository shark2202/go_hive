package builtin

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// schemaTypeLabel renders a JSON-schema type the way the upstream pydantic
// generator spells it: a bare type or a union of type names. Optional fields in
// the Python contract are `anyOf: [<schema>, {"type":"null"}]`, so the anyOf
// branches are flattened into the same union spelling.
func schemaTypeLabel(schema map[string]any) string {
	if branches, ok := schema["anyOf"].([]any); ok {
		parts := make([]string, 0, len(branches))
		for _, branch := range branches {
			node, ok := branch.(map[string]any)
			if !ok {
				continue
			}
			if label, ok := node["type"].(string); ok && label != "" {
				parts = append(parts, label)
			}
		}
		return strings.Join(parts, "|")
	}
	switch typed := schema["type"].(type) {
	case string:
		return typed
	case []string:
		return strings.Join(typed, "|")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			if text, ok := part.(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "|")
	}
	return ""
}

func defByName(t *testing.T, defs []llm.ToolDef, name string) llm.ToolDef {
	t.Helper()
	for _, def := range defs {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("tool %q not found", name)
	return llm.ToolDef{}
}

// Parity: core/tests/test_tool_registry.py::test_session_cwd_is_context_param_and_stripped_from_schema
func TestTerminalExecModelVisibleSchemaMatchesUpstream(t *testing.T) {
	visible := defByName(t, Defs(), ToolTerminalExec)
	properties := visible.Parameters["properties"].(map[string]any)

	wantTypes := map[string]string{
		"command":                   "string",
		"cwd":                       "string|null",
		"env":                       "object|null",
		"timeout_sec":               "integer",
		"auto_background_after_sec": "integer",
		"shell":                     "boolean",
		"stdin":                     "string|null",
		"limits":                    "object|null",
		"max_output_kb":             "integer",
		"crm_principal":             "string|null",
	}
	for name, want := range wantTypes {
		raw, ok := properties[name]
		if !ok {
			t.Fatalf("terminal_exec is missing model-visible parameter %q", name)
		}
		schema := raw.(map[string]any)
		if got := schemaTypeLabel(schema); got != want {
			t.Fatalf("%s type = %q, want %q", name, got, want)
		}
	}
	if len(properties) != len(wantTypes) {
		names := make([]string, 0, len(properties))
		for name := range properties {
			names = append(names, name)
		}
		t.Fatalf("terminal_exec properties = %v, want exactly %d upstream params", names, len(wantTypes))
	}
	if _, leaked := properties["session_cwd"]; leaked {
		t.Fatal("session_cwd must be stripped from the model-visible schema")
	}
	if _, leaked := properties["timeout_seconds"]; leaked {
		t.Fatal("timeout_seconds is not an upstream parameter and must not be advertised")
	}

	defaults := map[string]any{
		"timeout_sec":               60,
		"auto_background_after_sec": 30,
		"shell":                     false,
		"max_output_kb":             256,
	}
	for name, want := range defaults {
		schema := properties[name].(map[string]any)
		if got := schema["default"]; got != want {
			t.Fatalf("%s default = %#v, want %#v", name, got, want)
		}
	}
	// The Python schema carries no per-property descriptions: the guidance is
	// in the docstring's Args section. A description here would be model-visible
	// drift, so keep the schema bare.
	for name, raw := range properties {
		if _, documented := raw.(map[string]any)["description"]; documented {
			t.Fatalf("%s: property description must not be model-visible (Python has none)", name)
		}
	}
	for _, nullable := range []string{"cwd", "env", "stdin", "limits", "crm_principal"} {
		if _, ok := properties[nullable].(map[string]any)["default"]; !ok {
			t.Fatalf("%s must carry the upstream null default", nullable)
		}
	}
	required := visible.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "command" {
		t.Fatalf("terminal_exec required = %v, want [command]", required)
	}

	// The declaration survives internally so the host knows what to inject;
	// only the model-visible copy is stripped.
	raw := defByName(t, rawDefs(), ToolTerminalExec)
	rawProperties := raw.Parameters["properties"].(map[string]any)
	if _, ok := rawProperties["session_cwd"]; !ok {
		t.Fatal("raw terminal_exec definition must keep session_cwd for context injection")
	}
	if _, ok := rawProperties["crm_principal"]; !ok {
		t.Fatal("crm_principal is model-visible upstream and must stay declared")
	}
}

// Parity: core/tests/test_tool_registry.py::test_session_cwd_is_context_param_and_stripped_from_schema
func TestContextParamsHiddenFromTerminalAndFileSchemas(t *testing.T) {
	visible := Defs()
	raw := rawDefs()
	for _, name := range []string{ToolTerminalExec, ToolTerminalGlob, ToolTerminalRG, ToolTerminalJobStart, ToolTerminalPTYOpen} {
		def := defByName(t, visible, name)
		properties := def.Parameters["properties"].(map[string]any)
		if _, leaked := properties["session_cwd"]; leaked {
			t.Fatalf("%s model-visible schema leaks session_cwd", name)
		}
		if _, declared := defByName(t, raw, name).Parameters["properties"].(map[string]any)["session_cwd"]; !declared {
			t.Fatalf("%s raw schema lost the session_cwd declaration", name)
		}
	}

	contextParams := []string{"agent_id", "data_dir", "profile", "profile_display_name", "session_cwd", "colony_id", "principal"}
	for _, def := range visible {
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			continue
		}
		for _, hidden := range contextParams {
			if _, leaked := properties[hidden]; leaked {
				t.Fatalf("%s model-visible schema leaks context param %q", def.Name, hidden)
			}
		}
		if required, ok := def.Parameters["required"].([]string); ok {
			for _, hidden := range contextParams {
				for _, name := range required {
					if name == hidden {
						t.Fatalf("%s required list leaks context param %q", def.Name, hidden)
					}
				}
			}
		}
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_session_cwd_used_when_cwd_omitted
func TestTerminalExecSessionCwdAndCwdOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pwd semantics differ on Windows")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	realSub, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}

	// A zero-root box mirrors how chat sessions build their toolbox: the
	// session directory arrives only through the injected session_cwd.
	tb := New("")
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":     "pwd",
		"session_cwd": root,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(section(res.Content, "stdout:"), realRoot) {
		t.Fatalf("session_cwd result = %+v, want stdout in %s", res, realRoot)
	}

	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":     "pwd",
		"cwd":         sub,
		"session_cwd": root,
	})
	if err != nil {
		t.Fatalf("Execute with cwd: %v", err)
	}
	if res.IsError || !strings.Contains(section(res.Content, "stdout:"), realSub) {
		t.Fatalf("explicit cwd result = %+v, want stdout in %s", res, realSub)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_auto_shell_for_pipelines
func TestTerminalExecAutoShellDetection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts POSIX pipeline semantics")
	}
	tb := New(t.TempDir())

	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{"command": "echo a | tr a b"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "auto_shell: true") || !strings.Contains(res.Content, "shell_kind: bash") {
		t.Fatalf("pipeline result = %+v, want auto_shell + bash", res)
	}
	if strings.TrimSpace(section(res.Content, "stdout:")) != "b" {
		t.Fatalf("pipeline stdout = %q, want b", section(res.Content, "stdout:"))
	}

	// Semicolons attached to the preceding word are shell syntax too; the
	// old bash-always implementation ran these, and they must keep working.
	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{"command": "pwd; echo hello; echo oops >&2"})
	if err != nil {
		t.Fatalf("Execute semicolons: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "auto_shell: true") || !strings.Contains(res.Content, "hello") {
		t.Fatalf("semicolon result = %+v, want auto_shell and hello", res)
	}

	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("Execute plain argv: %v", err)
	}
	if strings.Contains(res.Content, "auto_shell:") || !strings.Contains(res.Content, "shell_kind: direct") {
		t.Fatalf("plain argv result = %+v, want direct exec without auto_shell", res)
	}

	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{"command": "echo a | tr a b", "shell": true})
	if err != nil {
		t.Fatalf("Execute shell=true: %v", err)
	}
	if strings.Contains(res.Content, "auto_shell:") || !strings.Contains(res.Content, "shell_kind: bash") {
		t.Fatalf("shell=true result = %+v, want bash without auto_shell", res)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_envelope_shape_simple_echo
func TestTerminalExecEnvStdinAndCrmPrincipal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("asserts POSIX shell syntax")
	}
	tb := New(t.TempDir())

	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "printf '%s' \"$HIVE_TEST_ENV\"",
		"shell":   true,
		"env":     map[string]any{"HIVE_TEST_ENV": "from-env"},
	})
	if err != nil {
		t.Fatalf("Execute env: %v", err)
	}
	if res.IsError || strings.TrimSpace(section(res.Content, "stdout:")) != "from-env" {
		t.Fatalf("env result = %+v, want stdout from-env", res)
	}

	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "cat",
		"stdin":   "stdin-payload",
	})
	if err != nil {
		t.Fatalf("Execute stdin: %v", err)
	}
	if res.IsError || strings.TrimSpace(section(res.Content, "stdout:")) != "stdin-payload" {
		t.Fatalf("stdin result = %+v, want stdout stdin-payload", res)
	}

	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":       "printf '%s' \"$HIVE_CRM_PRINCIPAL\"",
		"shell":         true,
		"crm_principal": "agent:test",
	})
	if err != nil {
		t.Fatalf("Execute crm_principal: %v", err)
	}
	if res.IsError || strings.TrimSpace(section(res.Content, "stdout:")) != "agent:test" {
		t.Fatalf("crm_principal result = %+v, want stdout agent:test", res)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_shell_kind_reported
func TestTerminalExecLimitsReachChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ulimit is POSIX-only; upstream setrlimit is a no-op on Windows")
	}
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "ulimit -t; ulimit -n",
		"shell":   true,
		"limits":  map[string]any{"cpu_sec": 5, "nofile": 128},
	})
	if err != nil {
		t.Fatalf("Execute limits: %v", err)
	}
	if res.IsError {
		t.Fatalf("limits result = %+v", res)
	}
	stdout := section(res.Content, "stdout:")
	if !strings.Contains(stdout, "5") || !strings.Contains(stdout, "128") {
		t.Fatalf("limits stdout = %q, want cpu_sec=5 and nofile=128", stdout)
	}

	// The direct-exec path is wrapped in bash so the same caps apply without
	// turning the command itself into a shell string.
	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "sh -c 'ulimit -t'",
		"limits":  map[string]any{"cpu_sec": 7},
	})
	if err != nil {
		t.Fatalf("Execute direct limits: %v", err)
	}
	if res.IsError || strings.TrimSpace(section(res.Content, "stdout:")) != "7" {
		t.Fatalf("direct limits result = %+v, want stdout 7", res)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_auto_promotion
func TestTerminalExecAutoPromotion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based timing test")
	}
	tb := New(t.TempDir())
	start := time.Now()
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":                   "sleep 0.5",
		"timeout_sec":               2,
		"auto_background_after_sec": 0.1,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("auto-promotion took %s, want a prompt return past the budget", elapsed)
	}
	if !strings.Contains(res.Content, "auto_backgrounded: true") {
		t.Fatalf("result = %+v, want auto_backgrounded", res)
	}
	match := regexp.MustCompile(`job_id: (job_[0-9a-f]+)`).FindStringSubmatch(res.Content)
	if match == nil {
		t.Fatalf("result = %+v, want a job_id", res)
	}

	logs, err := tb.Execute(context.Background(), ToolTerminalJobLogs, map[string]any{
		"job_id":           match[1],
		"wait_until_exit":  true,
		"wait_timeout_sec": 2,
	})
	if err != nil {
		t.Fatalf("job logs: %v", err)
	}
	if logs.IsError || !strings.Contains(logs.Content, `"status":"exited"`) || !strings.Contains(logs.Content, `"exit_code":0`) {
		t.Fatalf("job logs = %+v, want exited with exit_code 0", logs)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_output_handle_round_trip
func TestTerminalExecOutputHandleRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX yes/head")
	}
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":       "yes x | head -c 300000",
		"shell":         true,
		"max_output_kb": 64,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "output_truncated: true (limit 65536 bytes per stream)") {
		t.Fatalf("result = %+v, want truncation at 64 KiB", res)
	}
	match := regexp.MustCompile(`output_handle: (out_[0-9a-f]+)`).FindStringSubmatch(res.Content)
	if match == nil {
		t.Fatalf("result = %+v, want an output_handle", res)
	}
	page, err := tb.Execute(context.Background(), ToolTerminalOutputGet, map[string]any{
		"output_handle": match[1],
		"max_kb":        64,
	})
	if err != nil {
		t.Fatalf("output_get: %v", err)
	}
	if page.IsError || !strings.Contains(page.Content, `"expired":false`) || !strings.Contains(page.Content, `"next_offset":`) {
		t.Fatalf("output_get = %+v, want a live page", page)
	}
	bogus, err := tb.Execute(context.Background(), ToolTerminalOutputGet, map[string]any{
		"output_handle": "out_doesnotexist",
	})
	if err != nil {
		t.Fatalf("bogus output_get: %v", err)
	}
	if !strings.Contains(bogus.Content, `"expired":true`) {
		t.Fatalf("bogus handle = %+v, want expired", bogus)
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_timed_out_marker
func TestTerminalExecFractionalTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep-based timing test")
	}
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":                   "sleep 5",
		"timeout_sec":               0.1,
		"auto_background_after_sec": 0,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "timed_out: true") {
		t.Fatalf("result = %+v, want timed_out", res)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_glob_by_name
func TestTerminalGlobUsesInjectedSessionCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb := New("")
	res, err := tb.Execute(context.Background(), ToolTerminalGlob, map[string]any{
		"pattern":     "*.txt",
		"session_cwd": dir,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "hello.txt") {
		t.Fatalf("glob result = %+v, want hello.txt from session_cwd", res)
	}
}
