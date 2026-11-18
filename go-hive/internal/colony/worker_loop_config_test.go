package colony

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
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

// Parity-exempt: Go-specific contract for JSON-decoded overrides. Python
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
