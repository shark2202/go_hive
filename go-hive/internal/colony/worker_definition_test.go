package colony

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: core/tests/test_worker_definition.py::test_worker_goal_is_queen_worker
func TestWorkerGoalIsQueenWorker(t *testing.T) {
	if WorkerGoal.ID != "queen-worker" {
		t.Fatalf("WorkerGoal.ID = %q, want queen-worker", WorkerGoal.ID)
	}
	description := strings.ToLower(WorkerGoal.Description)
	for _, want := range []string{"ephemeral", "no escalation"} {
		if !strings.Contains(description, want) {
			t.Errorf("description %q does not mention %q", WorkerGoal.Description, want)
		}
	}
}

// Parity: core/tests/test_worker_definition.py::TestSystemPrompt::test_teaches_report_to_parent_not_set_output, core/tests/test_worker_definition.py::TestSystemPrompt::test_teaches_fail_fast, core/tests/test_worker_definition.py::TestSystemPrompt::test_mentions_attached_skills, core/tests/test_worker_definition.py::TestSystemPrompt::test_mentions_tracker_tools, core/tests/test_worker_definition.py::TestSystemPrompt::test_includes_task_footer, core/tests/test_worker_definition.py::TestSystemPrompt::test_default_task_when_missing, core/tests/test_worker_definition.py::TestSystemPrompt::test_is_identity_free, core/tests/test_worker_definition.py::TestSystemPrompt::test_module_level_constants_exist
func TestWorkerSystemPrompt(t *testing.T) {
	t.Run("report_to_parent_not_set_output", func(t *testing.T) {
		prompt := BuildSystemPrompt(strPtr("do something"))
		if !strings.Contains(prompt, "report_to_parent") {
			t.Error("prompt does not teach report_to_parent")
		}
		if !strings.Contains(prompt, "status='success'") {
			t.Error("prompt does not teach status='success'")
		}
		if strings.Contains(prompt, "set_output") {
			t.Error("prompt must not mention set_output")
		}
	})

	t.Run("teaches_fail_fast", func(t *testing.T) {
		prompt := BuildSystemPrompt(strPtr("do something"))
		for _, want := range []string{"FAIL FAST", "NO escalation", "status='failed'"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt does not contain %q", want)
			}
		}
	})

	t.Run("mentions_attached_skills", func(t *testing.T) {
		prompt := strings.ToLower(BuildSystemPrompt(strPtr("do something")))
		for _, want := range []string{"skill", "protocol"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt does not mention %q", want)
			}
		}
	})

	t.Run("mentions_tracker_tools", func(t *testing.T) {
		prompt := BuildSystemPrompt(strPtr("do something"))
		for _, want := range []string{"tracker_upsert", "tracker_query"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt does not mention %q", want)
			}
		}
	})

	t.Run("includes_task_footer", func(t *testing.T) {
		prompt := BuildSystemPrompt(strPtr("research 25 competitors"))
		if !strings.Contains(prompt, "research 25 competitors") {
			t.Error("prompt does not carry the task")
		}
		if !strings.Contains(prompt, "Task:") {
			t.Error("prompt does not carry the Task: footer")
		}
	})

	t.Run("default_task_when_missing", func(t *testing.T) {
		for _, task := range []*string{nil, strPtr("")} {
			prompt := BuildSystemPrompt(task)
			if !strings.Contains(prompt, defaultWorkerTask) {
				t.Errorf("prompt for task %v does not carry the default task", task)
			}
		}
	})

	t.Run("is_identity_free", func(t *testing.T) {
		prompt := BuildSystemPrompt(strPtr("do something"))
		for _, name := range []string{"Charlotte", "Alexandra"} {
			if strings.Contains(prompt, name) {
				t.Errorf("prompt leaks persona %q", name)
			}
		}
		if strings.Contains(strings.ToLower(prompt), "you remember") {
			t.Error("prompt leaks memory phrasing")
		}
	})

	t.Run("module_level_constants_exist", func(t *testing.T) {
		if len(WorkerSystemPrompt) <= 200 {
			t.Fatalf("WorkerSystemPrompt length = %d, want > 200", len(WorkerSystemPrompt))
		}
		if len(DefaultWorkerLoopConfig) == 0 {
			t.Fatal("DefaultWorkerLoopConfig is empty")
		}
	})
}

// Parity: core/tests/test_worker_definition.py::TestDefaultLoopConfig::test_reasonable_defaults, core/tests/test_worker_definition.py::TestDefaultLoopConfig::test_all_keys_are_ints
func TestWorkerDefaultLoopConfig(t *testing.T) {
	want := map[string]int{
		"max_iterations":            3,
		"grace_iterations":          1,
		"tool_call_budget":          30,
		"tool_call_hard_multiple":   3,
		"tool_call_lifetime_budget": 200,
		"max_context_tokens":        180_000,
	}
	if !reflect.DeepEqual(DefaultWorkerLoopConfig, want) {
		t.Fatalf("DefaultWorkerLoopConfig = %#v, want %#v", DefaultWorkerLoopConfig, want)
	}
	for key, value := range DefaultWorkerLoopConfig {
		if _, ok := any(value).(int); !ok {
			t.Errorf("key %q is %T, want int", key, value)
		}
	}
}

// Parity: core/tests/test_worker_definition.py::TestBuildInputData::test_carries_colony_binding, core/tests/test_worker_definition.py::TestBuildInputData::test_carries_task_id_when_provided
func TestWorkerBuildInputData(t *testing.T) {
	binding := hivehome.Colony{
		Name:      "colony_x",
		Dir:       "/c",
		TrackerDB: "/c/tracker/tracker.db",
	}

	data := BuildInputData(binding, "")
	got, ok := data["binding"].(map[string]string)
	if !ok {
		t.Fatalf("binding is %T, want map[string]string", data["binding"])
	}
	if !reflect.DeepEqual(got, binding.ToDict()) {
		t.Errorf("binding = %#v, want %#v", got, binding.ToDict())
	}
	if got["name"] != "colony_x" {
		t.Errorf("binding name = %q, want colony_x", got["name"])
	}
	if got["tracker_db"] != binding.TrackerDB {
		t.Errorf("binding tracker_db = %q, want %q", got["tracker_db"], binding.TrackerDB)
	}
	if _, present := data["task_id"]; present {
		t.Error("task_id must be absent when not provided")
	}

	withTask := BuildInputData(binding, "task_42")
	if withTask["task_id"] != "task_42" {
		t.Errorf("task_id = %v, want task_42", withTask["task_id"])
	}
}

// Parity: core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_uses_defaults_when_queen_config_missing, core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_propagates_queen_values, core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_lifetime_budget_kept_at_worker_default
func TestWorkerBuildLoopConfigDict(t *testing.T) {
	t.Run("uses_defaults_when_queen_config_missing", func(t *testing.T) {
		cfg := BuildLoopConfigDict(nil)
		for _, key := range []string{"max_iterations", "tool_call_budget", "max_context_tokens"} {
			if cfg[key] != DefaultWorkerLoopConfig[key] {
				t.Errorf("%s = %d, want %d", key, cfg[key], DefaultWorkerLoopConfig[key])
			}
		}
	})

	t.Run("propagates_queen_values", func(t *testing.T) {
		cfg := BuildLoopConfigDict(&QueenLoopConfig{
			MaxIterations:      42,
			ToolCallBudget:     30,
			MaxContextTokens:   64_000,
			MaxToolResultChars: 30_000,
		})
		if cfg["max_iterations"] != 42 {
			t.Errorf("max_iterations = %d, want 42", cfg["max_iterations"])
		}
		if cfg["max_tool_result_chars"] != 30_000 {
			t.Errorf("max_tool_result_chars = %d, want 30000", cfg["max_tool_result_chars"])
		}
		if cfg["tool_call_budget"] != 30 {
			t.Errorf("tool_call_budget = %d, want 30", cfg["tool_call_budget"])
		}
		if cfg["max_context_tokens"] != 64_000 {
			t.Errorf("max_context_tokens = %d, want 64000", cfg["max_context_tokens"])
		}
		if cfg["tool_call_hard_multiple"] != DefaultWorkerLoopConfig["tool_call_hard_multiple"] {
			t.Errorf("tool_call_hard_multiple = %d, want worker default", cfg["tool_call_hard_multiple"])
		}
	})

	t.Run("lifetime_budget_kept_at_worker_default", func(t *testing.T) {
		cfg := BuildLoopConfigDict(&QueenLoopConfig{MaxIterations: 42})
		if cfg["tool_call_lifetime_budget"] != DefaultWorkerLoopConfig["tool_call_lifetime_budget"] {
			t.Errorf("tool_call_lifetime_budget = %d, want worker default",
				cfg["tool_call_lifetime_budget"])
		}
		if cfg["grace_iterations"] != DefaultWorkerLoopConfig["grace_iterations"] {
			t.Errorf("grace_iterations = %d, want worker default", cfg["grace_iterations"])
		}
	})
}

func baseWorkerMetaInput() WorkerMetaInput {
	return WorkerMetaInput{
		WorkerName:          "worker",
		SourceSessionID:     "session_abc",
		Task:                strPtr("do the thing"),
		ToolNames:           []string{"read_file", "tracker_upsert"},
		SkillsCatalogPrompt: "<skills/>",
		ProtocolsPrompt:     "<protocols/>",
		SkillDirs:           []string{"/path/to/skills"},
		QueenPhase:          "colony",
		QueenID:             "queen_finance",
		InputData: map[string]any{
			"binding": map[string]any{
				"name":       "c",
				"dir":        "/x",
				"tracker_db": "/x/tracker/tracker.db",
			},
		},
	}
}

func buildWorkerMeta(override func(*WorkerMetaInput)) map[string]any {
	in := baseWorkerMetaInput()
	if override != nil {
		override(&in)
	}
	return BuildMeta(in)
}

// Parity: core/tests/test_worker_definition.py::TestBuildMeta::test_identity_fields_are_empty, core/tests/test_worker_definition.py::TestBuildMeta::test_inherits_skills_and_protocols, core/tests/test_worker_definition.py::TestBuildMeta::test_threads_input_data, core/tests/test_worker_definition.py::TestBuildMeta::test_concurrency_hint_optional, core/tests/test_worker_definition.py::TestBuildMeta::test_goal_description_from_task, core/tests/test_worker_definition.py::TestBuildMeta::test_goal_default_when_task_missing, core/tests/test_worker_definition.py::TestBuildMeta::test_loop_config_propagates_queen, core/tests/test_worker_definition.py::TestBuildMeta::test_tools_is_independent_copy, core/tests/test_worker_definition.py::TestBuildMeta::test_spawned_from_records_source_session, core/tests/test_worker_definition.py::TestBuildMeta::test_includes_required_field
func TestWorkerBuildMeta(t *testing.T) {
	t.Run("identity_fields_are_empty", func(t *testing.T) {
		meta := buildWorkerMeta(nil)
		if meta["identity_prompt"] != "" {
			t.Errorf("identity_prompt = %q, want empty", meta["identity_prompt"])
		}
		if meta["memory_prompt"] != "" {
			t.Errorf("memory_prompt = %q, want empty", meta["memory_prompt"])
		}
	})

	t.Run("inherits_skills_and_protocols", func(t *testing.T) {
		meta := buildWorkerMeta(func(in *WorkerMetaInput) {
			in.SkillsCatalogPrompt = "<s>x</s>"
			in.ProtocolsPrompt = "<p>y</p>"
			in.SkillDirs = []string{"/a", "/b"}
		})
		if meta["skills_catalog_prompt"] != "<s>x</s>" {
			t.Errorf("skills_catalog_prompt = %v", meta["skills_catalog_prompt"])
		}
		if meta["protocols_prompt"] != "<p>y</p>" {
			t.Errorf("protocols_prompt = %v", meta["protocols_prompt"])
		}
		if !reflect.DeepEqual(meta["skill_dirs"], []string{"/a", "/b"}) {
			t.Errorf("skill_dirs = %#v", meta["skill_dirs"])
		}
	})

	t.Run("threads_input_data", func(t *testing.T) {
		meta := buildWorkerMeta(func(in *WorkerMetaInput) {
			in.InputData = map[string]any{
				"binding": map[string]any{
					"name":       "yc",
					"dir":        "/y",
					"tracker_db": "/y/tracker/tracker.db",
				},
				"task_id": "task_99",
			}
		})
		inputData, ok := meta["input_data"].(map[string]any)
		if !ok {
			t.Fatalf("input_data is %T", meta["input_data"])
		}
		binding, ok := inputData["binding"].(map[string]any)
		if !ok {
			t.Fatalf("binding is %T", inputData["binding"])
		}
		if binding["tracker_db"] != "/y/tracker/tracker.db" {
			t.Errorf("binding tracker_db = %v", binding["tracker_db"])
		}
		if inputData["task_id"] != "task_99" {
			t.Errorf("task_id = %v", inputData["task_id"])
		}
	})

	t.Run("concurrency_hint_optional", func(t *testing.T) {
		if _, present := buildWorkerMeta(nil)["concurrency_hint"]; present {
			t.Error("concurrency_hint must be absent by default")
		}
		withHint := buildWorkerMeta(func(in *WorkerMetaInput) { in.ConcurrencyHint = 8 })
		if withHint["concurrency_hint"] != 8 {
			t.Errorf("concurrency_hint = %v, want 8", withHint["concurrency_hint"])
		}
		if _, present := buildWorkerMeta(func(in *WorkerMetaInput) { in.ConcurrencyHint = 0 })["concurrency_hint"]; present {
			t.Error("concurrency_hint must be absent when 0")
		}
	})

	t.Run("goal_description_from_task", func(t *testing.T) {
		meta := buildWorkerMeta(func(in *WorkerMetaInput) { in.Task = strPtr("research 25 competitors") })
		goal, ok := meta["goal"].(map[string]any)
		if !ok {
			t.Fatalf("goal is %T", meta["goal"])
		}
		if goal["description"] != "research 25 competitors" {
			t.Errorf("goal description = %v", goal["description"])
		}
		if criteria, ok := goal["success_criteria"].([]any); !ok || len(criteria) != 0 {
			t.Errorf("success_criteria = %#v, want []any{}", goal["success_criteria"])
		}
		if constraints, ok := goal["constraints"].([]any); !ok || len(constraints) != 0 {
			t.Errorf("constraints = %#v, want []any{}", goal["constraints"])
		}
	})

	t.Run("goal_default_when_task_missing", func(t *testing.T) {
		for _, task := range []*string{nil, strPtr("")} {
			meta := buildWorkerMeta(func(in *WorkerMetaInput) { in.Task = task })
			goal := meta["goal"].(map[string]any)
			if !strings.Contains(goal["description"].(string), "Continue the work") {
				t.Errorf("goal description for task %v = %v", task, goal["description"])
			}
		}
	})

	t.Run("loop_config_propagates_queen", func(t *testing.T) {
		meta := buildWorkerMeta(func(in *WorkerMetaInput) {
			in.QueenLoopConfig = &QueenLoopConfig{MaxIterations: 7}
		})
		cfg, ok := meta["loop_config"].(map[string]int)
		if !ok {
			t.Fatalf("loop_config is %T", meta["loop_config"])
		}
		if cfg["max_iterations"] != 7 {
			t.Errorf("loop_config max_iterations = %d, want 7", cfg["max_iterations"])
		}
	})

	t.Run("tools_is_independent_copy", func(t *testing.T) {
		src := []string{"read_file"}
		meta := buildWorkerMeta(func(in *WorkerMetaInput) { in.ToolNames = src })
		tools := meta["tools"].([]string)
		tools[0] = "hacked"
		if len(src) != 1 || src[0] != "read_file" {
			t.Errorf("source tools mutated: %#v", src)
		}
	})

	t.Run("spawned_from_records_source_session", func(t *testing.T) {
		meta := buildWorkerMeta(func(in *WorkerMetaInput) { in.SourceSessionID = "session_xyz" })
		if meta["spawned_from"] != "session_xyz" {
			t.Errorf("spawned_from = %v", meta["spawned_from"])
		}
		if !strings.Contains(meta["description"].(string), "session_xyz") {
			t.Errorf("description = %v", meta["description"])
		}
	})

	t.Run("includes_required_field", func(t *testing.T) {
		meta := buildWorkerMeta(nil)
		fields := []string{
			"name",
			"version",
			"description",
			"input_data",
			"goal",
			"system_prompt",
			"tools",
			"skills_catalog_prompt",
			"protocols_prompt",
			"skill_dirs",
			"identity_prompt",
			"memory_prompt",
			"queen_phase",
			"queen_id",
			"loop_config",
			"spawned_from",
			"spawned_at",
		}
		for _, field := range fields {
			if _, present := meta[field]; !present {
				t.Errorf("worker.json is missing %q", field)
			}
		}
	})
}
