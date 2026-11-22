package agentloop

// Wiring probes for the schema-driven tool-input coercer
// (core/framework/agent_loop/internals/tool_input_coercer.py). Upstream runs
// coerce_tool_input on every tool call before dispatch — before the
// grace/budget gates and before the tool_started event
// (core/framework/agent_loop/agent_loop.py::_run_turn_loop, the call at
// agent_loop.py:4805). These tests pin the same contract at the Go dispatch
// point: the started event, the executor, the turn records and the completed
// event all observe the healed input, while a shape the coercer cannot heal
// is left untouched so the tool's own validation reports it.

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// coercionInputObserver records the full request every tool lifecycle event
// carries, so a test can pin exactly what the loop announced and dispatched.
type coercionInputObserver struct {
	mu        sync.Mutex
	started   []ToolCallRequest
	completed []ToolCallRequest
}

func (o *coercionInputObserver) IterationStarted(int)                {}
func (o *coercionInputObserver) LLMTurnComplete(llm.Completion, int) {}
func (o *coercionInputObserver) AssistantText(string, string, int)   {}

func (o *coercionInputObserver) ToolCallStarted(call ToolCallRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.started = append(o.started, call)
}

func (o *coercionInputObserver) ToolCallCompleted(call ToolCallRequest, _ ToolResult) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.completed = append(o.completed, call)
}

func (o *coercionInputObserver) calls() (started, completed []ToolCallRequest) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]ToolCallRequest(nil), o.started...), append([]ToolCallRequest(nil), o.completed...)
}

// coercionCapture records the input each executor call receives, keyed by
// tool name (the loop may run concurrency-safe batches in any order).
type coercionCapture struct {
	mu     sync.Mutex
	inputs map[string]map[string]any
	order  []string
}

func newCoercionCapture() *coercionCapture {
	return &coercionCapture{inputs: map[string]map[string]any{}}
}

func (c *coercionCapture) executor() ToolExecutor {
	return func(req ToolCallRequest) (ToolResult, error) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.inputs[req.Name] = req.Input
		c.order = append(c.order, req.Name)
		return ToolResult{Content: "ok"}, nil
	}
}

func (c *coercionCapture) input(t *testing.T, toolName string) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	input, ok := c.inputs[toolName]
	if !ok {
		t.Fatalf("executor never ran %q (ran %v)", toolName, c.order)
	}
	return input
}

func coercionTestContext(completer llm.Completer, toolDefs []llm.ToolDef) *AgentContext {
	return &AgentContext{
		AgentID:        "agent",
		AgentSpec:      NewAgentSpec("agent", "Agent", "d"),
		InputData:      map[string]any{"task": "start"},
		LLM:            completer,
		AvailableTools: toolDefs,
		StreamID:       "coercion",
	}
}

// runCoercionTurn feeds one scripted tool batch through ExecuteTurn and
// returns the turn result plus the executor and observer captures.
func runCoercionTurn(t *testing.T, toolDefs []llm.ToolDef, calls []llm.ToolCall) (*TurnResult, *coercionCapture, *coercionInputObserver) {
	t.Helper()
	capture := newCoercionCapture()
	completer := &scriptedCompleter{scenarios: []scriptedResponse{
		{toolCalls: calls},
		{content: "done"},
	}}
	loop := NewAgentLoop(DefaultLoopConfig(), capture.executor())
	observer := &coercionInputObserver{}
	ctx := coercionTestContext(completer, toolDefs)
	ctx.Observer = observer
	conv := NewNodeConversation("You are a test assistant.")

	result, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}
	if !result.Success {
		t.Fatalf("turn did not succeed: %+v", result)
	}
	return &result, capture, observer
}

func coercionProps(props map[string]any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": props,
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_integer
// Parity: core/tests/test_tool_input_coercer.py::test_string_to_number_float
// Parity: core/tests/test_tool_input_coercer.py::test_string_to_boolean
// Parity: core/tests/test_tool_input_coercer.py::test_single_string_property_unwraps_label_object
// Parity: core/tests/test_tool_input_coercer.py::test_array_of_label_objects_unwraps_to_strings
func TestToolInputCoercionHealsDriftBeforeDispatch(t *testing.T) {
	defs := []llm.ToolDef{{
		Name: "probe",
		Parameters: coercionProps(map[string]any{
			"count": map[string]any{"type": "integer"},
			"ratio": map[string]any{"type": "number"},
			"flag":  map[string]any{"type": "boolean"},
			"label": map[string]any{"type": "string"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		}),
	}}
	arguments := `{"count":"42","ratio":"3.5","flag":"true","label":{"value":"chosen"},"tags":[{"label":"A"},{"label":"B"}]}`
	result, capture, observer := runCoercionTurn(t, defs, []llm.ToolCall{{ID: "c1", Name: "probe", Arguments: arguments}})

	want := map[string]any{
		"count": int64(42),
		"ratio": 3.5,
		"flag":  true,
		"label": "chosen",
		"tags":  []any{"A", "B"},
	}
	if got := capture.input(t, "probe"); !reflect.DeepEqual(got, want) {
		t.Fatalf("executor input = %#v, want healed %#v", got, want)
	}
	started, completed := observer.calls()
	if len(started) != 1 || !reflect.DeepEqual(started[0].Input, want) {
		t.Fatalf("tool_started inputs = %#v, want healed %#v", started, want)
	}
	if len(completed) != 1 || !reflect.DeepEqual(completed[0].Input, want) {
		t.Fatalf("tool_completed inputs = %#v, want healed %#v", completed, want)
	}
	if len(result.ToolResults) != 1 || !reflect.DeepEqual(result.ToolResults[0].ToolInput, want) {
		t.Fatalf("turn record inputs = %#v, want healed %#v", result.ToolResults, want)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_integer
// Parity: core/tests/test_tool_input_coercer.py::test_string_to_boolean
func TestToolInputCoercionFailureLeavesTheCallForTheToolToReject(t *testing.T) {
	defs := []llm.ToolDef{{
		Name: "probe",
		Parameters: coercionProps(map[string]any{
			"count": map[string]any{"type": "integer"},
			"flag":  map[string]any{"type": "boolean"},
		}),
	}}
	// Neither string is a shape the coercer recognizes: coercion is
	// conservative and must leave them for the tool's own validation. It must
	// also never turn the call into a loop-level error.
	arguments := `{"count":"forty-two","flag":"1"}`
	result, capture, _ := runCoercionTurn(t, defs, []llm.ToolCall{{ID: "c1", Name: "probe", Arguments: arguments}})

	want := map[string]any{"count": "forty-two", "flag": "1"}
	if got := capture.input(t, "probe"); !reflect.DeepEqual(got, want) {
		t.Fatalf("executor input = %#v, want untouched %#v", got, want)
	}
	if len(result.ToolResults) != 1 || result.ToolResults[0].IsError {
		t.Fatalf("turn results = %#v, want one non-error result from the tool", result.ToolResults)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_type_already_matches_is_noop
// Parity: core/tests/test_tool_input_coercer.py::test_unknown_property_is_untouched
func TestToolInputCoercionLeavesMatchingAndUnadvertisedInputsUntouched(t *testing.T) {
	// "probe" is advertised with an integer schema but the value already has
	// a numeric shape; "ghost" was never advertised, so no schema applies and
	// its string stays a string.
	defs := []llm.ToolDef{{
		Name: "probe",
		Parameters: coercionProps(map[string]any{
			"count": map[string]any{"type": "integer"},
			"flag":  map[string]any{"type": "boolean"},
		}),
	}}
	result, capture, _ := runCoercionTurn(t, defs, []llm.ToolCall{
		{ID: "c1", Name: "probe", Arguments: `{"count":7,"flag":true}`},
		{ID: "c2", Name: "ghost", Arguments: `{"count":"42"}`},
	})

	if got, want := capture.input(t, "probe"), map[string]any{"count": float64(7), "flag": true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("matching input = %#v, want untouched %#v", got, want)
	}
	if got, want := capture.input(t, "ghost"), map[string]any{"count": "42"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unadvertised input = %#v, want untouched %#v", got, want)
	}
	if len(result.ToolResults) != 2 {
		t.Fatalf("turn records = %d, want 2", len(result.ToolResults))
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_nested_questions_array_with_wrapped_options
// Parity: core/tests/test_tool_input_coercer.py::test_json_string_for_object_is_parsed
func TestToolInputCoercionRecursesIntoNestedObjectsAndArrays(t *testing.T) {
	defs := []llm.ToolDef{{
		Name: "nested_probe",
		Parameters: coercionProps(map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"retries": map[string]any{"type": "integer"},
					"mode":    map[string]any{"type": "string"},
				},
			},
			"questions": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string"},
						"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					},
				},
			},
		}),
	}}
	arguments := `{"config":{"retries":"3","mode":{"label":"fast"}},"questions":[{"id":"q1","options":[{"label":"A"},{"label":"B"}]},{"id":"q2"}]}`
	result, capture, _ := runCoercionTurn(t, defs, []llm.ToolCall{{ID: "c1", Name: "nested_probe", Arguments: arguments}})

	want := map[string]any{
		"config": map[string]any{
			"retries": int64(3),
			"mode":    "fast",
		},
		"questions": []any{
			map[string]any{"id": "q1", "options": []any{"A", "B"}},
			map[string]any{"id": "q2"},
		},
	}
	if got := capture.input(t, "nested_probe"); !reflect.DeepEqual(got, want) {
		t.Fatalf("executor input = %#v, want recursively healed %#v", got, want)
	}
	if len(result.ToolResults) != 1 || !reflect.DeepEqual(result.ToolResults[0].ToolInput, want) {
		t.Fatalf("turn record inputs = %#v, want recursively healed %#v", result.ToolResults, want)
	}
}
