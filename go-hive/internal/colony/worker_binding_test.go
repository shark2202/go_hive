package colony

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tracker"
)

func bindingTestRuntime(t *testing.T) *ColonyRuntime {
	t.Helper()
	return NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		Goal:        testGoal(),
		StoragePath: t.TempDir(),
	})
}

// Parity: core/tests/test_worker_tracker_db_propagation.py::test_worker_propagates_binding_from_input_data
func TestWorkerPropagatesBindingFromInputData(t *testing.T) {
	colonyDir := filepath.Join(t.TempDir(), "linkedin5")
	trackerPath := filepath.Join(colonyDir, "tracker", "tracker.db")
	binding := hivehome.Colony{Name: "linkedin5", Dir: colonyDir, TrackerDB: trackerPath}

	runtime := bindingTestRuntime(t)
	worker := runtime.BuildWorker(WorkerBuildOptions{
		WorkerID:  "w1",
		Task:      "t",
		InputData: map[string]any{"task": "t", "binding": binding.ToDict()},
	})

	resolved, err := worker.TrackerResolver()()
	if err != nil {
		t.Fatalf("resolve binding: %v", err)
	}
	if resolved == nil {
		t.Fatal("binding = nil, want the input_data binding rehydrated")
	}
	if resolved.Name != "linkedin5" {
		t.Errorf("binding name = %q, want %q", resolved.Name, "linkedin5")
	}
	if resolved.TrackerDB != trackerPath {
		t.Errorf("binding tracker_db = %q, want %q", resolved.TrackerDB, trackerPath)
	}

	provider := worker.Context.ColonyBindingProvider
	if provider == nil {
		t.Fatal("colony binding provider = nil, want the rehydrated binding")
	}
	if got := provider(); got == nil || got.TrackerDB != trackerPath {
		t.Errorf("provider tracker_db = %v, want %q", got, trackerPath)
	}

	encoded, err := json.Marshal(binding.ToDict())
	if err != nil {
		t.Fatalf("marshal binding: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode binding: %v", err)
	}
	worker.Context.InputData = map[string]any{"task": "t", "binding": decoded}
	roundTripped, err := worker.TrackerResolver()()
	if err != nil {
		t.Fatalf("resolve JSON binding: %v", err)
	}
	if roundTripped == nil || roundTripped.Name != "linkedin5" || roundTripped.TrackerDB != trackerPath {
		t.Errorf("JSON binding = %+v, want linkedin5 at %s", roundTripped, trackerPath)
	}
}

// Parity: core/tests/test_worker_tracker_db_propagation.py::test_worker_omits_binding_when_input_data_has_none
func TestWorkerOmitsBindingWhenInputDataHasNone(t *testing.T) {
	runtime := bindingTestRuntime(t)
	worker := runtime.BuildWorker(WorkerBuildOptions{
		WorkerID:  "w2",
		Task:      "t",
		InputData: map[string]any{"task": "t"},
	})

	resolved, err := worker.TrackerResolver()()
	if err != nil || resolved != nil {
		t.Fatalf("binding = %v, %v, want nil, nil when none was injected", resolved, err)
	}
	if worker.Context.ColonyBindingProvider != nil {
		t.Error("colony binding provider is set, want nil when no binding was injected")
	}

	tools := tracker.BuildTrackerTools(worker.TrackerResolver())
	calls := []struct {
		name   string
		inputs map[string]any
	}{
		{"tracker_upsert", map[string]any{"table": "companies", "row": map[string]any{"id": "1"}}},
		{"tracker_query", map[string]any{"sql": "SELECT 1"}},
	}
	for _, call := range calls {
		execute := findTrackerTool(tools, call.name)
		if execute == nil {
			t.Fatalf("%s not built", call.name)
		}
		result := execute(call.inputs)
		if ok, _ := result["success"].(bool); ok {
			t.Errorf("%s success = true, want a refusal: %v", call.name, result)
		}
		message, _ := result["error"].(string)
		if !strings.Contains(message, "no colony context") {
			t.Errorf("%s error = %q, want the no-colony-context guidance", call.name, message)
		}
	}
}

func findTrackerTool(list []tracker.Tool, name string) func(map[string]any) map[string]any {
	for _, tool := range list {
		if tool.Name == name {
			return tool.Execute
		}
	}
	return nil
}
