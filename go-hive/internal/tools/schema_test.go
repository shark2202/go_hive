package tools

import (
	"reflect"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: core/tests/test_tool_registry.py::test_convert_mcp_tool_strips_context_params
func TestStripContextParams(t *testing.T) {
	defs := []llm.ToolDef{
		{
			Name: "terminal_exec",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command":     map[string]any{"type": "string"},
					"session_cwd": map[string]any{"type": "string"},
					"env":         map[string]any{"type": "object"},
				},
				"required": []string{"command", "session_cwd"},
			},
		},
	}
	stripped := StripContextParams(defs)
	properties := stripped[0].Parameters["properties"].(map[string]any)
	if _, leaked := properties["session_cwd"]; leaked {
		t.Fatal("session_cwd survived stripping")
	}
	if _, kept := properties["command"]; !kept {
		t.Fatal("command was stripped")
	}
	if required := stripped[0].Parameters["required"].([]string); !reflect.DeepEqual(required, []string{"command"}) {
		t.Fatalf("required = %v, want [command]", required)
	}

	// The input definitions are left untouched: defs are built per catalogue
	// and the raw declaration must stay available to the host.
	raw := defs[0].Parameters["properties"].(map[string]any)
	if _, ok := raw["session_cwd"]; !ok {
		t.Fatal("StripContextParams mutated the input schema")
	}

	// A tool with no context params is unchanged.
	plain := StripContextParams([]llm.ToolDef{{
		Name: "plain",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"x": map[string]any{"type": "string"}},
		},
	}})
	if _, ok := plain[0].Parameters["properties"].(map[string]any)["x"]; !ok {
		t.Fatal("plain tool lost its property")
	}
}
