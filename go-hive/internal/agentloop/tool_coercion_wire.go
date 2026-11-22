package agentloop

import (
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

// indexToolDefsByName builds the per-turn name→schema lookup upstream keeps
// as “_tool_by_name“ for its coercion pass.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (_tool_by_name)
func indexToolDefsByName(toolDefs []llm.ToolDef) map[string]llm.ToolDef {
	if len(toolDefs) == 0 {
		return nil
	}
	byName := make(map[string]llm.ToolDef, len(toolDefs))
	for _, def := range toolDefs {
		byName[def.Name] = def
	}
	return byName
}

// coerceToolCallInput heals one tool call's decoded arguments against the
// schema the turn advertised for that tool, returning the input map the loop
// should dispatch (coercion mutates nested values in place).
//
// Upstream runs this schema-driven pass on every tool call before dispatch —
// before the grace/budget gates and before the tool_started event — so the
// started event, the executor, the doom-loop fingerprint and the stored
// records all observe the healed input. The pass is conservative: a value
// that already matches the schema is untouched, a tool the turn never
// advertised is untouched, and a shape the coercer cannot recognize is left
// as-is so the tool's own validation reports it instead of silent munging.
//
// Parity: framework/agent_loop/internals/tool_input_coercer.py::coerce_tool_input
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (coerce_tool_input before dispatch)
func coerceToolCallInput(byName map[string]llm.ToolDef, toolName string, input map[string]any) map[string]any {
	def, ok := byName[toolName]
	if !ok {
		return input
	}
	return tools.CoerceToolInput(tools.Tool{Name: def.Name, Parameters: def.Parameters}, input)
}

// coerceToolCallInputs returns each call's dispatched input for one model tool
// batch, in call order. Parsing and coercion run for every call before the
// grace and budget gates, mirroring upstream's in-loop coerce_tool_input call
// site; ExecuteTurn keeps only the call so its body stays under the frozen
// function-size budget.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (coerce_tool_input before dispatch)
func coerceToolCallInputs(tools []llm.ToolDef, toolCalls []llm.ToolCall) []map[string]any {
	if len(toolCalls) == 0 {
		return nil
	}
	byName := indexToolDefsByName(tools)
	inputs := make([]map[string]any, len(toolCalls))
	for i, tc := range toolCalls {
		inputs[i] = coerceToolCallInput(byName, tc.Name, parseToolInput(tc.Arguments))
	}
	return inputs
}
