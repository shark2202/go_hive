package agents

import (
	"testing"
)

// Parity: core/framework/loader/agent_loader.py::load_agent_config
func TestLoadAgentSpecCompilesGoalSuccessCriteria(t *testing.T) {
	root := t.TempDir()
	colonyDir := root + "/colonies/criteria_goal"
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","description":"deliver the brief",`+
			`"goal":{"description":"deliver the brief",`+
			`"success_criteria":["the brief names every risk","the brief lists sources"],`+
			`"constraints":[]},`+
			`"system_prompt":"work","tools":[]}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SuccessCriteria == nil {
		t.Fatal("AgentSpec.SuccessCriteria = nil, want the compiled goal criteria")
	}
	want := "the brief names every risk\nthe brief lists sources"
	if got := *spec.AgentSpec.SuccessCriteria; got != want {
		t.Fatalf("AgentSpec.SuccessCriteria = %q, want %q", got, want)
	}
	if len(spec.Goal.SuccessCriteria) != 2 {
		t.Fatalf("Goal.SuccessCriteria = %#v, want both criteria", spec.Goal.SuccessCriteria)
	}
}

// Parity-exempt: accepting a top-level success_criteria list is a Go-side
// superset; upstream's node-level field is a string.
func TestLoadAgentSpecAcceptsTopLevelSuccessCriteriaList(t *testing.T) {
	root := t.TempDir()
	colonyDir := root + "/colonies/criteria_list"
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","success_criteria":["tests pass","docs updated"],`+
			`"goal":{"description":"finish","success_criteria":["goal criterion"]},`+
			`"tools":[]}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SuccessCriteria == nil || *spec.AgentSpec.SuccessCriteria != "tests pass\ndocs updated" {
		t.Fatalf("AgentSpec.SuccessCriteria = %v, want the top-level list", spec.AgentSpec.SuccessCriteria)
	}
}

// Parity: core/framework/agent_loop/internals/types.py::AgentSpec.success_criteria
func TestLoadAgentSpecPrefersNodeSuccessCriteriaString(t *testing.T) {
	root := t.TempDir()
	colonyDir := root + "/colonies/criteria_node"
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","success_criteria":"the node paragraph",`+
			`"goal":{"description":"finish","success_criteria":["the goal item"]},`+
			`"tools":[]}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SuccessCriteria == nil || *spec.AgentSpec.SuccessCriteria != "the node paragraph" {
		t.Fatalf("AgentSpec.SuccessCriteria = %v, want the node-level string", spec.AgentSpec.SuccessCriteria)
	}
}

// Parity: core/framework/agents/queen/worker_definition.py::worker_goal
func TestLoadAgentSpecWithoutCriteriaLeavesNil(t *testing.T) {
	root := t.TempDir()
	colonyDir := root + "/colonies/criteria_none"
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","goal":{"description":"finish","success_criteria":[],"constraints":[]},`+
			`"tools":[]}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SuccessCriteria != nil {
		t.Fatalf("AgentSpec.SuccessCriteria = %v, want nil without criteria", spec.AgentSpec.SuccessCriteria)
	}
}

// Parity-exempt: YAML worker definitions are a Go-side extension, but the
// criteria block must compile exactly like the JSON form.
func TestLoadAgentSpecCompilesYAMLGoalSuccessCriteria(t *testing.T) {
	root := t.TempDir()
	colonyDir := root + "/colonies/criteria_yaml"
	writeWorkerSpecFile(t, colonyDir, "worker.yaml",
		"name: worker\n"+
			"goal:\n"+
			"  description: finish\n"+
			"  success_criteria:\n"+
			"    - tests pass\n"+
			"    - docs updated\n"+
			"tools: []\n")

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SuccessCriteria == nil || *spec.AgentSpec.SuccessCriteria != "tests pass\ndocs updated" {
		t.Fatalf("AgentSpec.SuccessCriteria = %v, want the compiled YAML criteria", spec.AgentSpec.SuccessCriteria)
	}
}
