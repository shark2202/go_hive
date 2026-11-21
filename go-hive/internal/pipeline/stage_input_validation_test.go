package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func newTestInputValidation(t *testing.T, schemas map[string]any) *InputValidationStage {
	t.Helper()
	stage, err := BuildStage(map[string]any{
		"type":   "input_validation",
		"config": map[string]any{"schemas": schemas},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return stage.(*InputValidationStage)
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// missing required key rejects with the upstream message.
func TestInputValidationMissingKeyRejects(t *testing.T) {
	stage := newTestInputValidation(t, map[string]any{
		"api": map[string]any{"query": "str"},
	})
	result, err := stage.Process(NewContext("api", map[string]any{}))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionReject {
		t.Fatalf("action = %s, want reject", result.Action)
	}
	want := "Missing required input key: 'query'"
	if result.RejectionReason != want {
		t.Fatalf("reason = %q, want %q", result.RejectionReason, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// wrongly typed value rejects with both type names in the message.
func TestInputValidationWrongTypeRejects(t *testing.T) {
	stage := newTestInputValidation(t, map[string]any{
		"api": map[string]any{"query": "str"},
	})
	result, err := stage.Process(NewContext("api", map[string]any{"query": json.Number("7")}))
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionReject {
		t.Fatalf("action = %s, want reject", result.Action)
	}
	want := "Input key 'query' has type int, expected str"
	if result.RejectionReason != want {
		t.Fatalf("reason = %q, want %q", result.RejectionReason, want)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; matching
// schemas pass, and the isinstance quirks are preserved: bool satisfies int,
// an int does not satisfy float.
func TestInputValidationPassesAndKeepsIsinstanceQuirks(t *testing.T) {
	stage := newTestInputValidation(t, map[string]any{
		"api": map[string]any{
			"query":   "str",
			"count":   "int",
			"ratio":   "float",
			"enabled": "bool",
			"items":   "list",
			"extra":   "dict",
		},
	})
	ctx := NewContext("api", map[string]any{
		"query":   "help",
		"count":   true, // Python: isinstance(True, int) is True
		"ratio":   json.Number("0.5"),
		"enabled": false,
		"items":   []any{1},
		"extra":   map[string]any{},
	})
	result, err := stage.Process(ctx)
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}

	intForFloat := NewContext("api", map[string]any{
		"query":   "help",
		"count":   json.Number("3"),
		"ratio":   json.Number("3"), // Python: isinstance(3, float) is False
		"enabled": true,
		"items":   []any{},
		"extra":   map[string]any{},
	})
	result, err = stage.Process(intForFloat)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if result.Action != ActionReject || !strings.Contains(result.RejectionReason, "'ratio' has type int, expected float") {
		t.Fatalf("int-for-float result = %+v", result)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an entry
// point without a schema passes through.
func TestInputValidationUnknownEntryPointPasses(t *testing.T) {
	stage := newTestInputValidation(t, map[string]any{
		"api": map[string]any{"query": "str"},
	})
	result, err := stage.Process(NewContext("other", map[string]any{}))
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an
// unsupported type name errors (Python's isinstance would TypeError), it never
// silently passes.
func TestInputValidationUnknownTypeNameErrors(t *testing.T) {
	stage := newTestInputValidation(t, map[string]any{
		"api": map[string]any{"query": "widget"},
	})
	_, err := stage.Process(NewContext("api", map[string]any{"query": "x"}))
	if err == nil || !strings.Contains(err.Error(), "widget") {
		t.Fatalf("err = %v, want unsupported type error", err)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// malformed schemas block fails at build time.
func TestInputValidationConfigParsing(t *testing.T) {
	if _, err := BuildStage(map[string]any{
		"type":   "input_validation",
		"config": map[string]any{"schemas": map[string]any{"api": "query"}},
	}); err == nil {
		t.Fatal("non-object schema must fail")
	}
	// No schemas key: the stage builds and passes every request.
	stage, err := BuildStage(map[string]any{"type": "input_validation"})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	result, err := stage.Process(NewContext("api", map[string]any{}))
	if err != nil || result.Action != ActionContinue {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
