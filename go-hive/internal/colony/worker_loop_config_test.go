package colony

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// Parity: tests/test_loop_config_overrides.py::test_no_overrides_returns_worker_profile
func TestBuildWorkerLoopConfigNoOverridesReturnsWorkerProfile(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig({}) error: %v", err)
	}
	defaults := agentloop.DefaultLoopConfig()
	if cfg.MaxIterations != 3 {
		t.Errorf("MaxIterations = %d, want 3", cfg.MaxIterations)
	}
	if cfg.GraceIterations != 1 {
		t.Errorf("GraceIterations = %d, want 1", cfg.GraceIterations)
	}
	if cfg.MaxContextTokens != defaults.MaxContextTokens {
		t.Errorf("MaxContextTokens = %d, want the LoopConfig default %d",
			cfg.MaxContextTokens, defaults.MaxContextTokens)
	}
	if cfg.ToolCallBudget != 30 {
		t.Errorf("ToolCallBudget = %d, want 30", cfg.ToolCallBudget)
	}
	if cfg.ToolCallHardMultiple != 3 {
		t.Errorf("ToolCallHardMultiple = %d, want 3", cfg.ToolCallHardMultiple)
	}
	if cfg.ToolCallLifetimeBudget != 200 {
		t.Errorf("ToolCallLifetimeBudget = %d, want 200", cfg.ToolCallLifetimeBudget)
	}
	if defaults.ToolCallLifetimeBudget != 0 {
		t.Errorf("bare LoopConfig lifetime = %d, want 0", defaults.ToolCallLifetimeBudget)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_max_iterations_override
func TestBuildWorkerLoopConfigMaxIterationsOverride(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"max_iterations": 12})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxIterations != 12 {
		t.Errorf("MaxIterations = %d, want 12", cfg.MaxIterations)
	}
	if want := agentloop.DefaultLoopConfig().MaxContextTokens; cfg.MaxContextTokens != want {
		t.Errorf("MaxContextTokens = %d, want untouched %d", cfg.MaxContextTokens, want)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_tool_call_budget_zero_means_unlimited
func TestBuildWorkerLoopConfigToolCallBudgetZeroMeansUnlimited(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"tool_call_budget": 0})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.ToolCallBudget != 0 {
		t.Errorf("ToolCallBudget = %d, want 0 (unlimited)", cfg.ToolCallBudget)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_tool_call_lifetime_budget_override
func TestBuildWorkerLoopConfigToolCallLifetimeBudgetOverride(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"tool_call_lifetime_budget": 40})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.ToolCallLifetimeBudget != 40 {
		t.Errorf("ToolCallLifetimeBudget = %d, want 40", cfg.ToolCallLifetimeBudget)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_tool_call_lifetime_budget_zero_means_disabled
func TestBuildWorkerLoopConfigToolCallLifetimeBudgetZeroMeansDisabled(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"tool_call_lifetime_budget": 0})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.ToolCallLifetimeBudget != 0 {
		t.Errorf("ToolCallLifetimeBudget = %d, want 0 (disabled)", cfg.ToolCallLifetimeBudget)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_tool_call_hard_multiple_is_not_queen_tunable
func TestBuildWorkerLoopConfigToolCallHardMultipleIsNotQueenTunable(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"tool_call_hard_multiple": 9})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.ToolCallHardMultiple != 3 {
		t.Errorf("ToolCallHardMultiple = %d, want the framework-locked 3", cfg.ToolCallHardMultiple)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_max_context_tokens_override
func TestBuildWorkerLoopConfigMaxContextTokensOverride(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"max_context_tokens": 64_000})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxContextTokens != 64_000 {
		t.Errorf("MaxContextTokens = %d, want 64000", cfg.MaxContextTokens)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_combined_overrides
func TestBuildWorkerLoopConfigCombinedOverrides(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{
		"max_iterations":     8,
		"tool_call_budget":   5,
		"max_context_tokens": 12_000,
	})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxIterations != 8 {
		t.Errorf("MaxIterations = %d, want 8", cfg.MaxIterations)
	}
	if cfg.ToolCallBudget != 5 {
		t.Errorf("ToolCallBudget = %d, want 5", cfg.ToolCallBudget)
	}
	if cfg.MaxContextTokens != 12_000 {
		t.Errorf("MaxContextTokens = %d, want 12000", cfg.MaxContextTokens)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_unknown_keys_silently_dropped
func TestBuildWorkerLoopConfigUnknownKeysSilentlyDropped(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{
		"max_iterations":      7,
		"judge_every_n_turns": 99,
		"garbage_field":       "x",
		"session_idle_nudges": true,
	})
	if err != nil {
		t.Fatalf("unknown keys must not error; got %v", err)
	}
	if cfg.MaxIterations != 7 {
		t.Errorf("MaxIterations = %d, want 7", cfg.MaxIterations)
	}
	if want := agentloop.DefaultLoopConfig().JudgeEveryNTurns; cfg.JudgeEveryNTurns != want {
		t.Errorf("JudgeEveryNTurns = %d, want the framework default %d",
			cfg.JudgeEveryNTurns, want)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_out_of_range_raises
func TestBuildWorkerLoopConfigOutOfRangeRaises(t *testing.T) {
	cases := []struct {
		field string
		value int
	}{
		{"max_iterations", 0},
		{"max_iterations", 1001},
		{"max_iterations", -5},
		{"tool_call_budget", -1},
		{"tool_call_budget", 201},
		{"tool_call_lifetime_budget", -1},
		{"tool_call_lifetime_budget", 2001},
		{"max_context_tokens", 999},
		{"max_context_tokens", 1_000_001},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.field, func(t *testing.T) {
			_, err := BuildWorkerLoopConfig(map[string]any{tc.field: tc.value})
			if err == nil {
				t.Fatalf("%s=%d accepted, want an out-of-range error", tc.field, tc.value)
			}
			var overrideErr *LoopOverrideError
			if !errors.As(err, &overrideErr) {
				t.Fatalf("error = %T (%v), want *LoopOverrideError", err, err)
			}
			if !overrideErr.OutOfRange {
				t.Errorf("OutOfRange = false, want true (err = %v)", err)
			}
			if !strings.Contains(err.Error(), "out of range") {
				t.Errorf("error %q does not mention \"out of range\"", err.Error())
			}
		})
	}
}

// Parity: tests/test_loop_config_overrides.py::test_non_int_raises
func TestBuildWorkerLoopConfigNonIntRaises(t *testing.T) {
	_, err := BuildWorkerLoopConfig(map[string]any{"max_iterations": "100"})
	if err == nil {
		t.Fatal("string override accepted, want an error")
	}
	var overrideErr *LoopOverrideError
	if !errors.As(err, &overrideErr) {
		t.Fatalf("error = %T (%v), want *LoopOverrideError", err, err)
	}
	if overrideErr.OutOfRange {
		t.Error("OutOfRange = true, want false for a type error")
	}
	if overrideErr.TypeName != "str" {
		t.Errorf("TypeName = %q, want str", overrideErr.TypeName)
	}
	if !strings.Contains(err.Error(), "must be int") {
		t.Errorf("error %q does not mention \"must be int\"", err.Error())
	}
}

// Parity: tests/test_loop_config_overrides.py::test_grace_iterations_override
func TestBuildWorkerLoopConfigGraceIterationsOverride(t *testing.T) {
	cfg, err := BuildWorkerLoopConfig(map[string]any{"grace_iterations": 0})
	if err != nil {
		t.Fatalf("grace_iterations=0 error: %v", err)
	}
	if cfg.GraceIterations != 0 {
		t.Errorf("GraceIterations = %d, want 0 (grace opted out)", cfg.GraceIterations)
	}

	cfg, err = BuildWorkerLoopConfig(map[string]any{"grace_iterations": 2})
	if err != nil {
		t.Fatalf("grace_iterations=2 error: %v", err)
	}
	if cfg.GraceIterations != 2 {
		t.Errorf("GraceIterations = %d, want 2", cfg.GraceIterations)
	}
}

// Parity: tests/test_loop_config_overrides.py::test_allowed_set_is_the_documented_fields
func TestBuildWorkerLoopConfigAllowedSetIsTheDocumentedFields(t *testing.T) {
	want := []string{
		"grace_iterations",
		"max_context_tokens",
		"max_iterations",
		"tool_call_budget",
		"tool_call_lifetime_budget",
	}
	got := append([]string(nil), AllowedWorkerLoopOverrides...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AllowedWorkerLoopOverrides = %v, want %v", got, want)
	}
	for key := range workerLoopOverrideBounds {
		if !containsString(AllowedWorkerLoopOverrides, key) {
			t.Errorf("bounds table has %q but the whitelist does not", key)
		}
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Parity-exempt: the audit pins worker spillover as the Go port's gap (upstream
// spills inside the loop but has no per-worker directory contract). This locks
// the directory BuildWorkerLoopConfig now provides and the 30/90 budget split.
func TestBuildWorkerLoopConfigSetsWorkerSpilloverDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv(hivehome.EnvHome, home)

	cfg, err := BuildWorkerLoopConfig(map[string]any{})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.SpilloverDir == "" {
		t.Fatal("worker SpilloverDir is empty: oversized results would stay in context")
	}
	wantPrefix := filepath.Join(home, "cache", "worker-tool-results")
	if !strings.HasPrefix(cfg.SpilloverDir, wantPrefix) {
		t.Fatalf("SpilloverDir = %q, want it under %q", cfg.SpilloverDir, wantPrefix)
	}
	other, err := BuildWorkerLoopConfig(nil)
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig(nil) error: %v", err)
	}
	if other.SpilloverDir == cfg.SpilloverDir {
		t.Fatalf("two workers share the spill directory %q", cfg.SpilloverDir)
	}

	// The queen's explicit hard cap must not leak into the worker profile: the
	// worker keeps its documented 30 soft / 90 hard budget.
	if cfg.ToolCallHardLimit != 0 {
		t.Fatalf("worker ToolCallHardLimit = %d, want 0 (multiple rule)", cfg.ToolCallHardLimit)
	}
	if cfg.ToolCallBudget != 30 || cfg.ToolCallHardMultiple != 3 {
		t.Fatalf("worker budget = %d x %d, want 30 x 3", cfg.ToolCallBudget, cfg.ToolCallHardMultiple)
	}
}

// Parity-exempt: end-to-end worker spill — a result over MaxToolResultChars is
// written to the worker's spill directory and the tool message cites the path.
func TestWorkerLoopSpillsOversizedToolResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv(hivehome.EnvHome, home)
	cfg, err := BuildWorkerLoopConfig(nil)
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	payload := strings.Repeat("z", cfg.MaxToolResultChars+1000)
	model := newScriptedLLM(
		scriptedResponse{
			ToolCalls:  []llm.ToolCall{{ID: "c1", Name: "big_tool", Arguments: `{}`}},
			StopReason: "tool_calls",
		},
		textResponse("done"),
	)
	executor := func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		if req.Name == "big_tool" {
			return agentloop.ToolResult{Content: payload}, nil
		}
		return agentloop.ToolResult{Content: "ok"}, nil
	}
	ctx := &agentloop.AgentContext{
		AgentID:        "worker-1",
		AgentSpec:      agentloop.NewAgentSpec("worker", "Worker", "spill"),
		LLM:            model,
		AvailableTools: []llm.ToolDef{{Name: "big_tool", Parameters: map[string]any{"type": "object"}}},
		StreamID:       "worker_1",
	}
	conv := agentloop.NewNodeConversation("You are a worker.")
	loop := agentloop.NewAgentLoop(cfg, executor)
	if _, err := loop.ExecuteTurn(context.Background(), ctx, conv, nil); err != nil {
		t.Fatalf("ExecuteTurn: %v", err)
	}

	entries, err := os.ReadDir(cfg.SpilloverDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", cfg.SpilloverDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("spill files = %v, want exactly one under %s", entries, cfg.SpilloverDir)
	}
	path := filepath.Join(cfg.SpilloverDir, entries[0].Name())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile spill: %v", err)
	}
	if len(data) != len(payload) {
		t.Fatalf("spill file carries %d bytes, want %d", len(data), len(payload))
	}
	cited := false
	for _, message := range conv.Messages() {
		if message.Role == agentloop.RoleTool && strings.Contains(message.Content, "Full result at: ") {
			cited = true
		}
	}
	if !cited {
		t.Fatal("no tool message cites the spill file")
	}
}

// Parity-exempt: Go-specific contract for JSON-decoded overrides. Python
// decoder yields json.Number, which the override path must accept as int.
func TestBuildWorkerLoopConfigAcceptsJSONNumbers(t *testing.T) {
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"max_iterations": 9, "tool_call_budget": 12}`))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	cfg, err := BuildWorkerLoopConfig(decoded)
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig(json.Number) error: %v", err)
	}
	if cfg.MaxIterations != 9 || cfg.ToolCallBudget != 12 {
		t.Errorf("got max_iterations=%d budget=%d, want 9/12",
			cfg.MaxIterations, cfg.ToolCallBudget)
	}

	decoder = json.NewDecoder(strings.NewReader(`{"max_iterations": 9.0}`))
	decoder.UseNumber()
	decoded = nil
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := BuildWorkerLoopConfig(decoded); err == nil {
		t.Fatal("9.0 accepted, want a must-be-int error (Python float is not int)")
	} else if !strings.Contains(err.Error(), "must be int") {
		t.Errorf("error = %v, want a must-be-int error", err)
	}
}

// TestBuildWorkerLoopConfigAppliesLoopSection pins loop.max_tool_result_chars:
// the global spillover threshold reaches every worker loop, and an unset or
// wrongly-typed value keeps the 30k default.
//
// Parity: core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_propagates_queen_values
func TestBuildWorkerLoopConfigAppliesLoopSection(t *testing.T) {
	home, err := hivehome.ResolveAt(filepath.Join(t.TempDir(), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HIVE_HOME", home.Root)
	write := func(document string) {
		t.Helper()
		if err := os.WriteFile(home.ConfigurationFile(), []byte(document), 0o644); err != nil {
			t.Fatalf("write configuration: %v", err)
		}
	}

	write(`{"loop": {"max_tool_result_chars": 12345}}`)
	cfg, err := BuildWorkerLoopConfig(map[string]any{})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxToolResultChars != 12345 {
		t.Fatalf("MaxToolResultChars = %d, want 12345", cfg.MaxToolResultChars)
	}

	write(`{"loop": {"max_tool_result_chars": 12345.5}}`)
	cfg, err = BuildWorkerLoopConfig(map[string]any{})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if want := agentloop.DefaultLoopConfig().MaxToolResultChars; cfg.MaxToolResultChars != want {
		t.Fatalf("float MaxToolResultChars = %d, want the default %d", cfg.MaxToolResultChars, want)
	}
}

// TestBuildWorkerLoopConfigAppliesWorkerContextWindow pins
// worker_llm.max_context_tokens: the worker's own window replaces the queen
// default, and an explicit per-worker override still wins.
//
// Parity: core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_uses_defaults_when_queen_config_missing
func TestBuildWorkerLoopConfigAppliesWorkerContextWindow(t *testing.T) {
	home, err := hivehome.ResolveAt(filepath.Join(t.TempDir(), "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HIVE_HOME", home.Root)
	document := `{"worker_llm": {"provider": "groq", "model": "llama-worker", "max_context_tokens": 40000}}`
	if err := os.WriteFile(home.ConfigurationFile(), []byte(document), 0o644); err != nil {
		t.Fatalf("write configuration: %v", err)
	}

	cfg, err := BuildWorkerLoopConfig(map[string]any{})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxContextTokens != 40000 {
		t.Fatalf("MaxContextTokens = %d, want the worker section's 40000", cfg.MaxContextTokens)
	}

	cfg, err = BuildWorkerLoopConfig(map[string]any{"max_context_tokens": 12000})
	if err != nil {
		t.Fatalf("BuildWorkerLoopConfig error: %v", err)
	}
	if cfg.MaxContextTokens != 12000 {
		t.Fatalf("override MaxContextTokens = %d, want 12000", cfg.MaxContextTokens)
	}
}
