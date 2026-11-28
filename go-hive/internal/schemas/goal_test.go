package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

// Python's reference measurements for this file (baseline 00eeecc8, pydantic
// 2.12.5), produced by framework/schemas/goal.py and
// framework/orchestrator/goal.py:
//
//	SuccessCriterion(id="sc-0", description="d", metric="llm_judge", target="") -> weight 1.0
//	SuccessCriterion(..., weight=0.0)                                           -> weight 0.0
//	SuccessCriterion(..., weight=0.5)                                           -> weight 0.5
//	SuccessCriterion(id="", ...)                                                -> "id":""
//	Goal(context={"zone": ..., "audience": ..., "deadline": ...}).to_prompt_context()
//	  renders the context lines in insertion order.
//
// The criterion's JSON number text differs by design: encoding/json writes 1
// and 0 where Python writes 1.0 and 0.0 (float64 formatting is a Go-wide
// artifact, not goal-specific). The decoded values must still match, which is
// what the reflect-based assertions below pin.

// Parity: framework/schemas/goal.py::SuccessCriterion (weight default=1.0,
// explicit 0.0 preserved).
func TestSuccessCriterionWeightKeepsExplicitZero(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		weight  float64
		rawText string
	}{
		{
			name:    "missing weight carries the schema default",
			input:   `{"id":"sc-0","description":"d","metric":"llm_judge","target":""}`,
			weight:  1,
			rawText: `"weight":1`,
		},
		{
			name:    "explicit zero is not rewritten",
			input:   `{"id":"sc-0","description":"d","metric":"llm_judge","target":"","weight":0}`,
			weight:  0,
			rawText: `"weight":0`,
		},
		{
			name:    "explicit half stays half",
			input:   `{"id":"sc-0","description":"d","metric":"llm_judge","target":"","weight":0.5}`,
			weight:  0.5,
			rawText: `"weight":0.5`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var criterion SuccessCriterion
			if err := json.Unmarshal([]byte(testCase.input), &criterion); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if criterion.EffectiveWeight() != testCase.weight {
				t.Fatalf("EffectiveWeight = %v, want %v", criterion.EffectiveWeight(), testCase.weight)
			}
			encoded, err := json.Marshal(criterion)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(encoded), testCase.rawText) {
				t.Fatalf("marshalled %s, want %s", encoded, testCase.rawText)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("decode round trip: %v", err)
			}
			if decoded["weight"] != testCase.weight {
				t.Fatalf("round-trip weight = %v, want %v", decoded["weight"], testCase.weight)
			}
		})
	}
}

// Parity: framework/schemas/goal.py::SuccessCriterion (weight default).
func TestSuccessCriterionProgrammaticZeroWeightIsExplicit(t *testing.T) {
	// An unset Weight is the schema default (1.0); SetWeight(0) states the
	// explicit zero, and both the marshalled JSON and the weighted rule must
	// agree with the field.
	unset := SuccessCriterion{ID: "unset", Description: "d", Metric: "output_contains"}
	if got := unset.EffectiveWeight(); got != 1 {
		t.Fatalf("unset EffectiveWeight = %v, want 1", got)
	}
	explicitZero := SuccessCriterion{ID: "zero", Description: "d", Metric: "output_contains"}
	explicitZero.SetWeight(0)
	if got := explicitZero.EffectiveWeight(); got != 0 {
		t.Fatalf("SetWeight(0) EffectiveWeight = %v, want 0", got)
	}
	encoded, err := json.Marshal(explicitZero)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(encoded), `"weight":0`) {
		t.Fatalf("marshalled %s, want an explicit zero weight", encoded)
	}

	// IsSuccess sums effective weights, so a goal whose only met criterion
	// weighs nothing scores 0/0 — the same 0 >= 0*0.9 result Python's
	// is_success() returns.
	goal := Goal{SuccessCriteria: []SuccessCriterion{explicitZero, {Met: true, Description: "e", Metric: "output_contains"}}}
	if !goal.IsSuccess() {
		t.Fatal("IsSuccess = false, want true (0 met weight of 0 total)")
	}
}

// Parity: framework/schemas/goal.py::Goal/SuccessCriterion/Constraint (id is a
// required field with no default: Python serializes it as "id":"" when empty
// and rejects a document that omits it).
func TestGoalIDsAreAlwaysSerialized(t *testing.T) {
	criterion := SuccessCriterion{Description: "d", Metric: "llm_judge", Target: ""}
	encoded, err := json.Marshal(criterion)
	if err != nil {
		t.Fatalf("marshal criterion: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":""`) {
		t.Fatalf("criterion %s lost the required id key", encoded)
	}

	criterion.ID = "sc-0"
	encoded, err = json.Marshal(criterion)
	if err != nil {
		t.Fatalf("marshal criterion: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":"sc-0"`) {
		t.Fatalf("criterion %s, want id sc-0", encoded)
	}

	// An explicit empty id is a value, not an absence: Python keeps
	// {"id": ""} verbatim.
	var explicitEmpty SuccessCriterion
	if err := json.Unmarshal([]byte(`{"id":"","description":"d","metric":"llm_judge","target":""}`), &explicitEmpty); err != nil {
		t.Fatalf("unmarshal explicit empty id: %v", err)
	}
	if explicitEmpty.Extra != nil {
		t.Fatalf("criterion extras = %v, want nil (id is a declared field)", explicitEmpty.Extra)
	}
	encoded, err = json.Marshal(explicitEmpty)
	if err != nil {
		t.Fatalf("marshal explicit empty id: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":""`) {
		t.Fatalf("criterion %s, want the explicit empty id preserved", encoded)
	}

	goal := Goal{Name: "g", Description: "d"}
	encoded, err = json.Marshal(goal)
	if err != nil {
		t.Fatalf("marshal goal: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":""`) {
		t.Fatalf("goal %s lost the required id key", encoded)
	}

	constraint := Constraint{Description: "c", ConstraintType: "hard"}
	encoded, err = json.Marshal(constraint)
	if err != nil {
		t.Fatalf("marshal constraint: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":""`) {
		t.Fatalf("constraint %s lost the required id key", encoded)
	}
}

// Parity: framework/schemas/goal.py (Pydantic rejects a missing required id
// with "Field required"; a null id fails str_type). This is the negative half
// of the contract: Go must not accept a document Python refuses.
func TestGoalIDsAreRequiredOnRead(t *testing.T) {
	cases := []struct {
		name  string
		value func() error
	}{
		{"goal missing id", func() error {
			var goal Goal
			return json.Unmarshal([]byte(`{"name":"g","description":"d"}`), &goal)
		}},
		{"goal null id", func() error {
			var goal Goal
			return json.Unmarshal([]byte(`{"id":null,"name":"g","description":"d"}`), &goal)
		}},
		{"criterion missing id", func() error {
			var criterion SuccessCriterion
			return json.Unmarshal([]byte(`{"description":"d","metric":"llm_judge","target":""}`), &criterion)
		}},
		{"constraint missing id", func() error {
			var constraint Constraint
			return json.Unmarshal([]byte(`{"description":"c","constraint_type":"hard"}`), &constraint)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.value(); err == nil {
				t.Fatal("unmarshal accepted a document without a required id")
			}
		})
	}
}

// Parity: framework/schemas/goal.py::Goal.to_prompt_context (context renders
// in JSON insertion order).
func TestGoalPromptContextPreservesContextKeyOrder(t *testing.T) {
	const input = `{
		"id": "ctx-goal",
		"name": "Context goal",
		"description": "Ship the report",
		"context": {"zone": "us-east", "audience": "CFO", "deadline": "Friday"}
	}`
	var goal Goal
	if err := json.Unmarshal([]byte(input), &goal); err != nil {
		t.Fatalf("unmarshal goal: %v", err)
	}
	if keys := goal.Context.Keys(); len(keys) != 3 || keys[0] != "zone" || keys[1] != "audience" || keys[2] != "deadline" {
		t.Fatalf("context keys = %v, want [zone audience deadline]", keys)
	}

	// Byte-for-byte the Python output for the same document (insertion order,
	// not sorted).
	const wantPrompt = "# Goal: Context goal\n" +
		"Ship the report\n" +
		"\n" +
		"## Success Criteria:\n" +
		"\n" +
		"## Context:\n" +
		"- zone: us-east\n" +
		"- audience: CFO\n" +
		"- deadline: Friday"
	if got := goal.ToPromptContext(); got != wantPrompt {
		t.Fatalf("prompt = %q, want %q", got, wantPrompt)
	}

	// The marshalled context keeps the same order instead of sorting keys.
	encoded, err := json.Marshal(goal)
	if err != nil {
		t.Fatalf("marshal goal: %v", err)
	}
	if !strings.Contains(string(encoded), `"context":{"zone":"us-east","audience":"CFO","deadline":"Friday"}`) {
		t.Fatalf("context JSON = %s, want insertion order", encoded)
	}

	// Programmatic construction keeps Set order too.
	built := Goal{Name: "Context goal", Description: "Ship the report"}
	built.Context.Set("zone", "us-east")
	built.Context.Set("audience", "CFO")
	built.Context.Set("deadline", "Friday")
	if got := built.ToPromptContext(); got != wantPrompt {
		t.Fatalf("programmatic prompt = %q, want %q", got, wantPrompt)
	}
}
