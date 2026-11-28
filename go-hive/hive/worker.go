package hive

import "github.com/shark2202/go_hive/go-hive/internal/colony"

// WorkerDefinition is the fallback worker role: identity, task framing and
// success criteria used when a colony has no custom worker profile.
//
// Parity: framework/agents/queen/worker_definition.py
type WorkerDefinition struct {
	ID              string
	Name            string
	Description     string
	SuccessCriteria []string
	Constraints     []string
}

// DefaultWorkerDefinition is the runtime's built-in worker role.
//
// Parity: framework/agents/queen/worker_definition.py
func DefaultWorkerDefinition() WorkerDefinition {
	goal := colony.WorkerGoal
	return WorkerDefinition{
		ID:              goal.ID,
		Name:            goal.Name,
		Description:     goal.Description,
		SuccessCriteria: append([]string(nil), goal.SuccessCriteria...),
		Constraints:     append([]string(nil), goal.Constraints...),
	}
}

// WorkerLoopConfig builds a worker's loop configuration: the 30/90 profile,
// its own spillover directory and any per-spawn overrides.
//
// Parity: framework/agents/queen/worker_definition.py::build_loop_config_dict
func WorkerLoopConfig(overrides map[string]any) (LoopConfig, error) {
	return colony.BuildWorkerLoopConfig(overrides)
}

// WorkerSystemPrompt renders the default worker task frame for an optional
// goal line.
//
// Parity: framework/agents/queen/worker_definition.py::build_system_prompt
func WorkerSystemPrompt(task string) string {
	return colony.BuildSystemPrompt(&task)
}

// WorkerInputData is the input frame a worker receives for one task.
//
// Parity: framework/agents/queen/worker_definition.py::build_input_data
func WorkerInputData(home Home, colonyID, taskID string) map[string]any {
	return colony.BuildInputData(home.Colony(colonyID), taskID)
}
