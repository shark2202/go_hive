package agents

// Parity: the same worker.json, loaded by both runtimes, must produce the same
// goal JSON. The expected document is the measured Python output of
// framework/loader/agent_loader.py::AgentLoader.load on this fixture (baseline
// 00eeecc8, pydantic 2.12.5), with the run-specific created_at/updated_at
// values removed:
//
//	{"id":"colony_fixture-goal","name":"ship it","description":"ship it",
//	 "status":"draft",
//	 "success_criteria":[{"id":"sc-0","description":"a","metric":"llm_judge",
//	   "type":"success_rate","target":"","weight":1.0,"met":false}, ...],
//	 "constraints":[{"id":"c-0","description":"c","constraint_type":"hard",
//	   "category":"general","check":""}],
//	 "context":{},"required_capabilities":[],"version":"1.0.0",
//	 "parent_version":null,"evolution_reason":null}
//
// Two known residuals this test does not assert: encoding/json writes weight 1
// where Python writes 1.0 (float formatting), and the Go goal is built with
// zero timestamps (Python stamps datetime.now() at construction).

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

// Parity: framework/loader/agent_loader.py::AgentLoader.load (worker.json goal
// block -> Goal model: ids sc-<i>/c-<i>, <colony>-goal, llm_judge metric).
func TestLoadAgentSpecGoalJSONMatchesPythonColonyPath(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony_fixture")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","goal":{"description":"ship it","success_criteria":["a","b"],"constraints":["c"]}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	encoded, err := json.Marshal(spec.Goal)
	if err != nil {
		t.Fatalf("marshal goal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode goal: %v", err)
	}
	// Timestamps are run-specific; compare everything else.
	delete(got, "created_at")
	delete(got, "updated_at")

	const pythonGoalJSON = `{
		"id": "colony_fixture-goal",
		"name": "ship it",
		"description": "ship it",
		"status": "draft",
		"success_criteria": [
			{"id": "sc-0", "description": "a", "metric": "llm_judge", "type": "success_rate", "target": "", "weight": 1.0, "met": false},
			{"id": "sc-1", "description": "b", "metric": "llm_judge", "type": "success_rate", "target": "", "weight": 1.0, "met": false}
		],
		"constraints": [
			{"id": "c-0", "description": "c", "constraint_type": "hard", "category": "general", "check": ""}
		],
		"context": {},
		"required_capabilities": [],
		"version": "1.0.0",
		"parent_version": null,
		"evolution_reason": null
	}`
	var want map[string]any
	if err := json.Unmarshal([]byte(pythonGoalJSON), &want); err != nil {
		t.Fatalf("decode Python fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("goal JSON = %s\nwant (Python) = %s", encoded, pythonGoalJSON)
	}

	// The colony path always names the goal and every criterion: an empty id
	// (which Python rejects at validation) never appears in the document.
	if spec.Goal.ID != "colony_fixture-goal" {
		t.Fatalf("goal id = %q", spec.Goal.ID)
	}
	if spec.Goal.SuccessCriteria[0].ID != "sc-0" || spec.Goal.SuccessCriteria[1].ID != "sc-1" {
		t.Fatalf("criterion ids = %q/%q", spec.Goal.SuccessCriteria[0].ID, spec.Goal.SuccessCriteria[1].ID)
	}
	if spec.Goal.Constraints[0].ID != "c-0" {
		t.Fatalf("constraint id = %q", spec.Goal.Constraints[0].ID)
	}
	// Every criterion in the colony path weighs the schema default (1.0).
	for i, criterion := range spec.Goal.SuccessCriteria {
		if criterion.EffectiveWeight() != 1 {
			t.Fatalf("criterion %d weight = %v, want the schema default 1", i, criterion.EffectiveWeight())
		}
	}
}
