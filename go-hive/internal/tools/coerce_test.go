package tools

import (
	"reflect"
	"testing"
)

func coerceTool(parameters map[string]any) Tool {
	return Tool{Name: "t", Description: "test", Parameters: parameters}
}

func propSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func typeOnly(t string) map[string]any { return map[string]any{"type": t} }

func stringArraySchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func assertCoerced(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("coerced = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_empty_input_passes_through
func TestCoerceEmptyInputPassesThrough(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"x": typeOnly("string")}))
	assertCoerced(t, CoerceToolInput(tool, map[string]any{}), map[string]any{})
	got := CoerceToolInput(tool, nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("nil input = %#v, want empty map", got)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_missing_schema_is_noop
func TestCoerceMissingSchemaIsNoop(t *testing.T) {
	args := map[string]any{"anything": 123}
	result := CoerceToolInput(coerceTool(map[string]any{}), args)
	assertCoerced(t, result, map[string]any{"anything": 123})
	result["injected"] = true
	if _, ok := args["injected"]; !ok {
		t.Fatalf("result is not the input map: %#v", result)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_unknown_property_is_untouched
func TestCoerceUnknownPropertyUntouched(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"known": typeOnly("integer")}))
	args := map[string]any{"unknown": "42"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"unknown": "42"})
}

// Parity: core/tests/test_tool_input_coercer.py::test_type_already_matches_is_noop
func TestCoerceTypeAlreadyMatchesIsNoop(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("integer")}))
	args := map[string]any{"n": int64(42)}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": int64(42)})
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_integer
func TestCoerceStringToInteger(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("integer")}))
	args := map[string]any{"n": "42"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": int64(42)})
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_integer_rejects_fractional
func TestCoerceStringToIntegerRejectsFractional(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("integer")}))
	args := map[string]any{"n": "3.14"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": "3.14"})
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_number_float
func TestCoerceStringToNumberFloat(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("number")}))
	args := map[string]any{"n": "3.14"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": 3.14})
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_number_whole
func TestCoerceStringToNumberWhole(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("number")}))
	args := map[string]any{"n": "42"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": int64(42)})
}

// Parity: core/tests/test_tool_input_coercer.py::test_string_to_boolean
func TestCoerceStringToBoolean(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{
		"a": typeOnly("boolean"),
		"b": typeOnly("boolean"),
		"c": typeOnly("boolean"),
	}))
	args := map[string]any{"a": "true", "b": "False", "c": "nope"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"a": true, "b": false, "c": "nope"})
}

// Parity: core/tests/test_tool_input_coercer.py::test_union_type_first_match_wins
func TestCoerceUnionTypeFirstMatchWins(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"x": map[string]any{"type": []any{"integer", "string"}}}))
	args := map[string]any{"x": "42"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"x": int64(42)})
}

// Parity: core/tests/test_tool_input_coercer.py::test_nan_and_inf_rejected
func TestCoerceNanAndInfRejected(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"n": typeOnly("number")}))
	args := map[string]any{"n": "inf"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"n": "inf"})
}

// Parity: core/tests/test_tool_input_coercer.py::test_array_of_label_objects_unwraps_to_strings
func TestCoerceArrayOfLabelObjectsUnwraps(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"options": stringArraySchema()}))
	args := map[string]any{"options": []any{
		map[string]any{"label": "A"},
		map[string]any{"label": "B"},
		map[string]any{"label": "C"},
	}}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"options": []any{"A", "B", "C"}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_array_of_value_objects_unwraps
func TestCoerceArrayOfValueObjectsUnwraps(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	args := map[string]any{"xs": []any{
		map[string]any{"value": "A"},
		map[string]any{"text": "B"},
		map[string]any{"name": "C"},
	}}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{"A", "B", "C"}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_single_key_object_falls_back_to_sole_value
func TestCoerceSingleKeyObjectFallsBackToSoleValue(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	args := map[string]any{"xs": []any{map[string]any{"weirdkey": "A"}}}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{"A"}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_unrecognized_object_is_preserved
func TestCoerceUnrecognizedObjectPreserved(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	original := map[string]any{"a": "x", "b": "y"}
	args := map[string]any{"xs": []any{original}}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{map[string]any{"a": "x", "b": "y"}}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_json_string_array_is_parsed
func TestCoerceJSONStringArrayIsParsed(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	args := map[string]any{"xs": `["A","B","C"]`}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{"A", "B", "C"}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_scalar_wraps_into_singleton_array
func TestCoerceScalarWrapsIntoSingletonArray(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	args := map[string]any{"xs": "solo"}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{"solo"}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_invalid_json_string_wraps_as_singleton
func TestCoerceInvalidJSONStringWrapsAsSingleton(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"xs": stringArraySchema()}))
	args := map[string]any{"xs": "not json [["}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"xs": []any{"not json [["}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_nested_questions_array_with_wrapped_options
func TestCoerceNestedQuestionsArrayWithWrappedOptions(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{
		"questions": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     typeOnly("string"),
					"prompt": typeOnly("string"),
					"options": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
			},
		},
	}))
	args := map[string]any{
		"questions": []any{
			map[string]any{
				"id":     "q1",
				"prompt": "Pick one",
				"options": []any{
					map[string]any{"label": "Email (Recommended)"},
					map[string]any{"label": "Slack"},
				},
			},
			map[string]any{"id": "q2", "prompt": "Free form"},
		},
	}
	CoerceToolInput(tool, args)

	questions, ok := args["questions"].([]any)
	if !ok || len(questions) != 2 {
		t.Fatalf("questions = %#v", args["questions"])
	}
	first := questions[0].(map[string]any)
	if !reflect.DeepEqual(first["options"], []any{"Email (Recommended)", "Slack"}) {
		t.Fatalf("q1 options = %#v", first["options"])
	}
	second := questions[1].(map[string]any)
	if !reflect.DeepEqual(second, map[string]any{"id": "q2", "prompt": "Free form"}) {
		t.Fatalf("q2 = %#v", second)
	}
}

// Parity: core/tests/test_tool_input_coercer.py::test_json_string_for_object_is_parsed
func TestCoerceJSONStringForObjectIsParsed(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{
		"cfg": propSchema(map[string]any{"n": typeOnly("integer")}),
	}))
	args := map[string]any{"cfg": `{"n": "42"}`}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"cfg": map[string]any{"n": int64(42)}})
}

// Parity: core/tests/test_tool_input_coercer.py::test_single_string_property_unwraps_label_object
func TestCoerceSingleStringPropertyUnwrapsLabelObject(t *testing.T) {
	tool := coerceTool(propSchema(map[string]any{"choice": typeOnly("string")}))
	args := map[string]any{"choice": map[string]any{"label": "Email"}}
	CoerceToolInput(tool, args)
	assertCoerced(t, args, map[string]any{"choice": "Email"})
}
