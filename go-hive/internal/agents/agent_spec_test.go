package agents

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

func writeWorkerSpecFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func specContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Parity: core/tests/test_colony_fork_flow.py::test_agent_loader_picks_worker_json_not_metadata_json
func TestLoadAgentSpecPicksWorkerJSONNotMetadata(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colonies", "honeycomb")
	mustMkdir(t, colonyDir)
	mustMkdir(t, filepath.Join(colonyDir, "data"))

	// Colony provenance sorts before worker.json and must never be read as a
	// worker definition.
	writeWorkerSpecFile(t, colonyDir, "metadata.json",
		`{"colony_name":"honeycomb","queen_name":"queen_finance_fundraising","queen_session_id":"session_xxx","workers":{"worker":{"task":"trade"}}}`)
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","version":"1.0.0","description":"trader",`+
			`"goal":{"description":"trade honeycomb","success_criteria":[],"constraints":[]},`+
			`"system_prompt":"be a careful trader",`+
			`"tools":["read_file","search_files"],`+
			`"loop_config":{"max_iterations":50}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.ID != "worker" {
		t.Fatalf("agent id = %q, want worker", spec.AgentSpec.ID)
	}
	if spec.Goal.Description != "trade honeycomb" {
		t.Fatalf("goal description = %q", spec.Goal.Description)
	}
	if !specContains(spec.AgentSpec.Tools, "read_file") {
		t.Fatalf("tools = %#v, want read_file", spec.AgentSpec.Tools)
	}
	if spec.ConfigPath != filepath.Join(spec.Dir, "worker.json") || filepath.Base(spec.ConfigPath) != "worker.json" {
		t.Fatalf("config path = %q (dir %q)", spec.ConfigPath, spec.Dir)
	}
	if spec.ColonyID != "honeycomb" {
		t.Fatalf("colony id = %q", spec.ColonyID)
	}
	if spec.AgentSpec.Name != "Worker" {
		t.Fatalf("display name = %q", spec.AgentSpec.Name)
	}
	if spec.AgentSpec.Description != "trader" {
		t.Fatalf("description = %q", spec.AgentSpec.Description)
	}
	if spec.AgentSpec.SystemPrompt == nil || *spec.AgentSpec.SystemPrompt != "be a careful trader" {
		t.Fatalf("system prompt = %#v", spec.AgentSpec.SystemPrompt)
	}
	if spec.AgentSpec.AgentType != "event_loop" {
		t.Fatalf("agent type = %q", spec.AgentSpec.AgentType)
	}
	if spec.AgentSpec.ToolAccessPolicy != "explicit" {
		t.Fatalf("tool policy = %q", spec.AgentSpec.ToolAccessPolicy)
	}
	if spec.AgentSpec.MaxRetries != 3 {
		t.Fatalf("max retries = %d", spec.AgentSpec.MaxRetries)
	}
	if spec.MaxTokens != 4096 || spec.ConversationMode != "continuous" {
		t.Fatalf("max tokens/mode = %d/%q", spec.MaxTokens, spec.ConversationMode)
	}
	if spec.Goal.ID != "honeycomb-goal" {
		t.Fatalf("goal id = %q", spec.Goal.ID)
	}
	if spec.Goal.Status != schemas.GoalDraft {
		t.Fatalf("goal status = %q", spec.Goal.Status)
	}
}

// Parity-exempt: upstream reads worker configs as JSON only; YAML is a Go-side
// config format, so this pins YAML parsing parity with the JSON path.
func TestLoadAgentSpecParsesYAMLWorkerConfig(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "honeycomb")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "metadata.yaml", "colony_name: honeycomb\n")
	writeWorkerSpecFile(t, colonyDir, "worker.yaml", strings.Join([]string{
		"name: worker",
		"description: trader",
		"system_prompt: be a careful trader",
		"identity_prompt: steady hands",
		"tools:",
		"  - read_file",
		"  - search_files",
		"goal:",
		"  description: trade honeycomb",
		"  success_criteria:",
		"    - no spilled jars",
		"  constraints:",
		"    - stay in budget",
		"loop_config:",
		"  max_iterations: 50",
		"  max_context_tokens: 200000",
		"max_tokens: 8192",
		"conversation_mode: continuous",
	}, "\n"))

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.ID != "worker" || spec.AgentSpec.Name != "Worker" {
		t.Fatalf("id/name = %q/%q", spec.AgentSpec.ID, spec.AgentSpec.Name)
	}
	if spec.AgentSpec.Description != "trader" || spec.IdentityPrompt != "steady hands" {
		t.Fatalf("description/identity = %q/%q", spec.AgentSpec.Description, spec.IdentityPrompt)
	}
	if !reflect.DeepEqual(spec.AgentSpec.Tools, []string{"read_file", "search_files"}) {
		t.Fatalf("tools = %#v", spec.AgentSpec.Tools)
	}
	if spec.Goal.Description != "trade honeycomb" {
		t.Fatalf("goal = %#v", spec.Goal)
	}
	if len(spec.Goal.SuccessCriteria) != 1 || spec.Goal.SuccessCriteria[0].Description != "no spilled jars" {
		t.Fatalf("criteria = %#v", spec.Goal.SuccessCriteria)
	}
	if len(spec.Goal.Constraints) != 1 || spec.Goal.Constraints[0].Description != "stay in budget" {
		t.Fatalf("constraints = %#v", spec.Goal.Constraints)
	}
	if got, ok := spec.LoopConfig["max_iterations"].(int); !ok || got != 50 {
		t.Fatalf("loop_config max_iterations = %#v", spec.LoopConfig["max_iterations"])
	}
	// YAML ints must reach the colony's loop-config builder intact.
	loopConfig, err := colony.BuildWorkerLoopConfig(spec.LoopConfig)
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig: %v", err)
	}
	if loopConfig.MaxIterations != 50 || loopConfig.MaxContextTokens != 200000 {
		t.Fatalf("loop config = %#v", loopConfig)
	}
	if spec.MaxTokens != 8192 {
		t.Fatalf("max tokens = %d", spec.MaxTokens)
	}
}

// Parity-exempt: no Python test covers malformed colony configs; pins the
// loader's error path and message shape.
func TestLoadAgentSpecRejectsMalformedConfig(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "worker.json", `{"name":`)

	_, err := LoadAgentSpec(colonyDir)
	if err == nil {
		t.Fatal("expected malformed config error")
	}
	if !strings.Contains(err.Error(), "worker.json") {
		t.Fatalf("error should name the config: %v", err)
	}

	writeWorkerSpecFile(t, colonyDir, "worker.json", `{"name":"worker"} {}`)
	if _, err := LoadAgentSpec(colonyDir); err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("trailing data error = %v", err)
	}
}

// Parity-exempt: upstream raises FileNotFoundError with no Python test; pins
// the sentinel error callers branch on.
func TestLoadAgentSpecNoWorkerConfig(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony")
	mustMkdir(t, colonyDir)
	mustMkdir(t, filepath.Join(colonyDir, "data"))
	writeWorkerSpecFile(t, colonyDir, "metadata.json", `{"queen_name":"q"}`)
	writeWorkerSpecFile(t, colonyDir, "agent.json", `{"name":"ignored"}`)
	writeWorkerSpecFile(t, colonyDir, "tasks.yaml", "task: ignored\n")
	writeWorkerSpecFile(t, colonyDir, "notes.txt", "ignored")

	_, err := LoadAgentSpec(colonyDir)
	if !errors.Is(err, ErrNoWorkerConfig) {
		t.Fatalf("error = %v, want ErrNoWorkerConfig", err)
	}
	if !strings.Contains(err.Error(), colonyDir) {
		t.Fatalf("error should name the directory: %v", err)
	}
	if configs := FindWorkerSpecFiles(colonyDir); len(configs) != 0 {
		t.Fatalf("configs = %#v", configs)
	}
}

// Parity-exempt: upstream's loader works on an existing colony dir; this pins
// the Go error surface for a missing/non-directory path.
func TestLoadAgentSpecMissingDir(t *testing.T) {
	if _, err := LoadAgentSpec(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing dir error")
	}
	root := t.TempDir()
	filePath := filepath.Join(root, "worker.json")
	writeWorkerSpecFile(t, root, "worker.json", `{"name":"worker"}`)
	if _, err := LoadAgentSpec(filePath); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file path error = %v", err)
	}
}

// Parity-exempt: exported agent configs carry tools as {policy, allowed}; the
// colony worker form is a bare list. Pins both shapes decoding.
func TestLoadAgentSpecAcceptsExportedAgentToolsShape(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "graph_agent")
	mustMkdir(t, agentDir)
	writeWorkerSpecFile(t, agentDir, "graph_agent.json",
		`{"name":"graph_agent","description":"graph",`+
			`"tools":{"policy":"explicit","allowed":["alpha","beta"],"denied":[]},`+
			`"goal":{"description":"ship it"}}`)

	spec, err := LoadAgentSpec(agentDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if !reflect.DeepEqual(spec.AgentSpec.Tools, []string{"alpha", "beta"}) {
		t.Fatalf("tools = %#v", spec.AgentSpec.Tools)
	}
	if spec.AgentSpec.ToolAccessPolicy != "explicit" {
		t.Fatalf("policy = %q", spec.AgentSpec.ToolAccessPolicy)
	}
}

// Parity-exempt: upstream's goal block fallbacks (name from worker name when
// the description is absent, generated criterion/constraint ids) have no
// Python test; pins them against agent_loader.load.
func TestLoadAgentSpecGoalFallbacks(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "worker.json", `{"name":"worker"}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.Goal.Name != "worker" {
		t.Fatalf("goal name = %q, want worker name fallback", spec.Goal.Name)
	}
	if spec.Goal.Description != "" {
		t.Fatalf("goal description = %q", spec.Goal.Description)
	}
	if len(spec.Goal.SuccessCriteria) != 0 || len(spec.Goal.Constraints) != 0 {
		t.Fatalf("goal = %#v", spec.Goal)
	}
	if spec.AgentSpec.SystemPrompt == nil || *spec.AgentSpec.SystemPrompt != "" {
		t.Fatalf("absent system prompt = %#v, want explicit empty prompt", spec.AgentSpec.SystemPrompt)
	}

	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","goal":{"description":"ship","success_criteria":["a","b"],"constraints":["c"]}}`)
	spec, err = LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.Goal.Name != "ship" || spec.Goal.Description != "ship" {
		t.Fatalf("goal name/description = %q/%q", spec.Goal.Name, spec.Goal.Description)
	}
	if len(spec.Goal.SuccessCriteria) != 2 {
		t.Fatalf("criteria = %#v", spec.Goal.SuccessCriteria)
	}
	if spec.Goal.SuccessCriteria[1].ID != "sc-1" ||
		spec.Goal.SuccessCriteria[1].Metric != "llm_judge" ||
		spec.Goal.SuccessCriteria[1].Description != "b" {
		t.Fatalf("criterion = %#v", spec.Goal.SuccessCriteria[1])
	}
	if len(spec.Goal.Constraints) != 1 ||
		spec.Goal.Constraints[0].ID != "c-0" ||
		spec.Goal.Constraints[0].ConstraintType != "hard" ||
		spec.Goal.Constraints[0].Category != "general" {
		t.Fatalf("constraint = %#v", spec.Goal.Constraints[0])
	}
}

// Parity-exempt: upstream falls back to the config filename stem when the
// config omits name; pins the stemming and Python title casing.
func TestLoadAgentSpecNameFallbackFromFilename(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "my_worker.json", `{"description":"no name"}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.ID != "my_worker" {
		t.Fatalf("id = %q", spec.AgentSpec.ID)
	}
	if spec.AgentSpec.Name != "My Worker" {
		t.Fatalf("name = %q", spec.AgentSpec.Name)
	}
	if spec.Goal.Name != "my_worker" {
		t.Fatalf("goal name = %q", spec.Goal.Name)
	}
}

// Parity-exempt: upstream json.load yields ints for integer literals; the Go
// reader must preserve that so BuildWorkerLoopConfig's int type check passes.
func TestLoadAgentSpecKeepsLoopConfigIntegers(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "colony")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","loop_config":{"max_iterations":12,"tool_call_budget":30,"grace_iterations":2}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	loopConfig, err := colony.BuildWorkerLoopConfig(spec.LoopConfig)
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig: %v", err)
	}
	if loopConfig.MaxIterations != 12 || loopConfig.ToolCallBudget != 30 || loopConfig.GraceIterations != 2 {
		t.Fatalf("loop config = %#v", loopConfig)
	}
}

// Parity-exempt: framework/server/app.py::validate_agent_path has Python tests
// for the roots themselves; this pins the loader's use of them.
func TestLoadAgentSpecWithinRejectsOutsideRoots(t *testing.T) {
	root := t.TempDir()
	coloniesDir := filepath.Join(root, "hive", "colonies")
	inside := filepath.Join(coloniesDir, "alpha")
	outside := filepath.Join(root, "elsewhere", "alpha")
	mustMkdir(t, inside)
	mustMkdir(t, outside)
	writeWorkerSpecFile(t, inside, "worker.json", `{"name":"worker"}`)
	writeWorkerSpecFile(t, outside, "worker.json", `{"name":"worker"}`)

	allowed := NewAllowedRoots(coloniesDir, root, filepath.Join(root, "hive", "agents"))
	if _, err := LoadAgentSpecWithin(outside, allowed); err == nil {
		t.Fatal("expected outside-root rejection")
	}
	spec, err := LoadAgentSpecWithin(inside, allowed)
	if err != nil {
		t.Fatalf("inside root rejected: %v", err)
	}
	if spec.AgentSpec.ID != "worker" {
		t.Fatalf("id = %q", spec.AgentSpec.ID)
	}
	if _, err := LoadAgentSpecWithin(inside, nil); err != nil {
		t.Fatalf("nil roots should skip validation: %v", err)
	}
}

// Parity-exempt: pins the explicit root-list variant of allowed-root checking.
func TestLoadAgentSpecInRoots(t *testing.T) {
	root := t.TempDir()
	coloniesDir := filepath.Join(root, "colonies")
	inside := filepath.Join(coloniesDir, "alpha")
	outside := filepath.Join(root, "outside", "alpha")
	mustMkdir(t, inside)
	mustMkdir(t, outside)
	writeWorkerSpecFile(t, inside, "worker.json", `{"name":"worker"}`)
	writeWorkerSpecFile(t, outside, "worker.json", `{"name":"worker"}`)

	if _, err := LoadAgentSpecInRoots(outside, []string{coloniesDir}); err == nil {
		t.Fatal("expected outside-root rejection")
	}
	if _, err := LoadAgentSpecInRoots(inside, []string{coloniesDir}); err != nil {
		t.Fatalf("inside root rejected: %v", err)
	}
	if _, err := LoadAgentSpecInRoots(outside, nil); err != nil {
		t.Fatalf("empty roots should skip validation: %v", err)
	}
}

// Parity-exempt: upstream's discovery walks one colonies dir; pins the Go
// agent-dir enumeration including the YAML extension.
func TestDiscoverAgentDirs(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "alpha"))
	writeWorkerSpecFile(t, root, "alpha/worker.json", `{"name":"worker"}`)
	mustMkdir(t, filepath.Join(root, "beta"))
	writeWorkerSpecFile(t, root, "beta/metadata.json", `{"queen_name":"q"}`)
	mustMkdir(t, filepath.Join(root, "gamma"))
	writeWorkerSpecFile(t, root, "gamma/worker.yaml", "name: worker\n")
	writeWorkerSpecFile(t, root, "notes.txt", "ignored")

	dirs := DiscoverAgentDirs(root)
	if len(dirs) != 2 {
		t.Fatalf("dirs = %#v", dirs)
	}
	wantAlpha, err := ResolvePath(filepath.Join(root, "alpha"))
	if err != nil {
		t.Fatalf("resolve alpha: %v", err)
	}
	wantGamma, err := ResolvePath(filepath.Join(root, "gamma"))
	if err != nil {
		t.Fatalf("resolve gamma: %v", err)
	}
	if dirs[0] != wantAlpha || dirs[1] != wantGamma {
		t.Fatalf("dirs = %#v", dirs)
	}
	if dirs := DiscoverAgentDirs(filepath.Join(root, "missing")); dirs != nil {
		t.Fatalf("missing root dirs = %#v", dirs)
	}
}

// Parity-exempt: host.Config takes an AgentSpec and a Goal; pins the adapter
// the CLI uses so the two stay in lockstep.
func TestWorkerSpecHostFields(t *testing.T) {
	root := t.TempDir()
	colonyDir := filepath.Join(root, "honeycomb")
	mustMkdir(t, colonyDir)
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","goal":{"description":"trade honeycomb"}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	fields := spec.HostFields()
	if fields.ColonyID != "honeycomb" {
		t.Fatalf("colony id = %q", fields.ColonyID)
	}
	if fields.AgentSpec.ID != "worker" || fields.AgentSpec.SystemPrompt == nil {
		t.Fatalf("agent spec = %#v", fields.AgentSpec)
	}
	if fields.Goal == nil || fields.Goal.Description != "trade honeycomb" {
		t.Fatalf("goal = %#v", fields.Goal)
	}
	var nilSpec *WorkerSpec
	if empty := nilSpec.HostFields(); empty.ColonyID != "" || empty.Goal != nil {
		t.Fatalf("nil spec fields = %#v", empty)
	}
}

// Parity-exempt: pins that the JSON and YAML readers produce the same spec for
// the same definition, so format choice cannot change worker behaviour.
func TestLoadAgentSpecJSONAndYAMLYieldSameSpec(t *testing.T) {
	root := t.TempDir()
	jsonDir := filepath.Join(root, "json", "honeycomb")
	yamlDir := filepath.Join(root, "yaml", "honeycomb")
	mustMkdir(t, jsonDir)
	mustMkdir(t, yamlDir)
	writeWorkerSpecFile(t, jsonDir, "worker.json",
		`{"name":"worker","description":"trader","system_prompt":"be careful",`+
			`"tools":["read_file"],"input_keys":["task_id"],"output_keys":["result"],`+
			`"goal":{"description":"trade","success_criteria":["a"],"constraints":["b"]},`+
			`"loop_config":{"max_iterations":7},"max_tokens":2048}`)
	writeWorkerSpecFile(t, yamlDir, "worker.yaml", strings.Join([]string{
		"name: worker",
		"description: trader",
		"system_prompt: be careful",
		"tools:",
		"  - read_file",
		"input_keys:",
		"  - task_id",
		"output_keys:",
		"  - result",
		"goal:",
		"  description: trade",
		"  success_criteria:",
		"    - a",
		"  constraints:",
		"    - b",
		"loop_config:",
		"  max_iterations: 7",
		"max_tokens: 2048",
	}, "\n"))

	jsonSpec, err := LoadAgentSpec(jsonDir)
	if err != nil {
		t.Fatalf("json spec: %v", err)
	}
	yamlSpec, err := LoadAgentSpec(yamlDir)
	if err != nil {
		t.Fatalf("yaml spec: %v", err)
	}
	if !reflect.DeepEqual(jsonSpec.AgentSpec, yamlSpec.AgentSpec) {
		t.Fatalf("agent specs differ:\njson=%#v\nyaml=%#v", jsonSpec.AgentSpec, yamlSpec.AgentSpec)
	}
	if !reflect.DeepEqual(jsonSpec.Goal, yamlSpec.Goal) {
		t.Fatalf("goals differ:\njson=%#v\nyaml=%#v", jsonSpec.Goal, yamlSpec.Goal)
	}
	if jsonSpec.MaxTokens != yamlSpec.MaxTokens || jsonSpec.MaxTokens != 2048 {
		t.Fatalf("max tokens json/yaml = %d/%d", jsonSpec.MaxTokens, yamlSpec.MaxTokens)
	}
	if !reflect.DeepEqual(jsonSpec.LoopConfig, yamlSpec.LoopConfig) {
		t.Fatalf("loop configs differ: json=%#v yaml=%#v", jsonSpec.LoopConfig, yamlSpec.LoopConfig)
	}
}
