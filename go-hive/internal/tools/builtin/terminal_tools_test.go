package builtin

import (
	"context"
	"strings"
	"testing"
)

// Parity: tools/tests/test_terminal_tools_smoke.py::test_register_terminal_tools_lands_all_ten
func TestTerminalToolsRegistered(t *testing.T) {
	want := []string{
		ToolTerminalRG,
		ToolTerminalGlob,
		ToolTerminalOutputGet,
		ToolTerminalJobStart,
		ToolTerminalJobLogs,
		ToolTerminalJobManage,
		ToolTerminalPTYOpen,
		ToolTerminalPTYRun,
		ToolTerminalPTYClose,
	}
	defs := Defs()
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		if def.Description == "" {
			t.Fatalf("tool %s has empty description", def.Name)
		}
		if def.Parameters["type"] != "object" {
			t.Fatalf("tool %s parameters type = %v, want object", def.Name, def.Parameters["type"])
		}
		seen[def.Name] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Fatalf("terminal tool %q missing from Defs()", name)
		}
	}
	if len(defs) != 3+len(want) {
		t.Fatalf("Defs() = %d tools, want %d", len(defs), 3+len(want))
	}
}

// Parity: tools/tests/test_terminal_tools_smoke.py::test_all_tools_have_terminal_prefix
func TestTerminalToolsPrefixedAndDispatched(t *testing.T) {
	for name := range terminalToolHandlers {
		if !strings.HasPrefix(name, "terminal_") {
			t.Fatalf("tool %q missing terminal_ prefix", name)
		}
	}
	handle := terminalOutputs.Put([]byte("dispatch-payload"))
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalOutputGet, map[string]any{"output_handle": handle})
	if err != nil {
		t.Fatalf("Execute terminal_output_get: %v", err)
	}
	if !strings.Contains(res.Content, "dispatch-payload") {
		t.Fatalf("content = %q", res.Content)
	}

	if _, err := tb.Execute(context.Background(), "nope_not_a_tool", nil); err == nil {
		t.Fatal("unknown tool returned no error")
	} else if !strings.Contains(err.Error(), `builtin: unknown tool "nope_not_a_tool"`) {
		t.Fatalf("unknown tool error = %v", err)
	}
}

// Parity: tools/tests/test_terminal_tools_search.py::test_rg_finds_pattern
func TestTerminalToolRequiredParameters(t *testing.T) {
	required := map[string]string{
		ToolTerminalRG:        "pattern",
		ToolTerminalGlob:      "pattern",
		ToolTerminalOutputGet: "output_handle",
		ToolTerminalJobStart:  "command",
		ToolTerminalJobLogs:   "job_id",
		ToolTerminalJobManage: "action",
		ToolTerminalPTYRun:    "session_id",
		ToolTerminalPTYClose:  "session_id",
	}
	for _, def := range Defs() {
		want, ok := required[def.Name]
		if !ok {
			continue
		}
		list, ok := def.Parameters["required"].([]string)
		if !ok || len(list) != 1 || list[0] != want {
			t.Fatalf("%s required = %v, want [%s]", def.Name, def.Parameters["required"], want)
		}
	}
}
