package server

import (
	"os"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// Parity-exempt: the Python reference has no unit test for the configuration
// readers (get_queen_llm_section / get_aux_max_tokens /
// get_max_tool_result_chars); these tests pin the reader semantics the Go
// runtime now consumes, anchored to framework/config.py.
func TestQueenLLMSectionRequiresProviderAndModel(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"queen_llm": map[string]any{
			"archivist": map[string]any{
				"provider":   "openrouter",
				"model":      "openrouter/sonnet-4",
				"api_base":   "https://queen.example/v1",
				"extra_body": map[string]any{"reasoning_effort": "high"},
			},
			"halfway":  map[string]any{"provider": "openai"},
			"blank":    map[string]any{"provider": "  ", "model": "gpt"},
			"nonmodel": map[string]any{"provider": "openai", "model": "   "},
		},
		"queen_llm_broken": "not an object",
	})

	section := QueenLLMSection(home, "archivist")
	if section == nil {
		t.Fatal("archivist section = nil")
	}
	if got := SectionModel(section); got != "openrouter/sonnet-4" {
		t.Fatalf("section model = %q, want the openrouter prefix stripped", got)
	}
	if extra := SectionExtraBody(section); extra["reasoning_effort"] != "high" {
		t.Fatalf("extra body = %#v", extra)
	}

	for _, queenID := range []string{"", "   ", "halfway", "blank", "nonmodel", "unknown"} {
		if got := QueenLLMSection(home, queenID); got != nil {
			t.Fatalf("QueenLLMSection(%q) = %#v, want nil", queenID, got)
		}
	}

	// The openrouter base resolves to the built-in endpoint when the section
	// does not pin one.
	plain := map[string]any{"provider": "openrouter", "model": "sonnet-4"}
	if got := SectionAPIBase(plain); got != "https://openrouter.ai/api/v1" {
		t.Fatalf("openrouter api base = %q", got)
	}
	if got := SectionAPIBase(map[string]any{"provider": "openai", "model": "gpt-5"}); got != "" {
		t.Fatalf("openai api base without api_base = %q, want empty", got)
	}
}

// Parity: core/tests/test_llm_judge.py::TestLLMJudgeWithProvider::test_evaluate_passes_correct_arguments
func TestAuxMaxTokensPrefersTheFileThenClampsToTheMainCap(t *testing.T) {
	home := newFeatureHome(t)

	// No llm section at all: DEFAULT_MAX_TOKENS, then the 8192 aux cap.
	writeFeatureConfig(t, home, map[string]any{})
	if got := AuxMaxTokens(home); got != 8192 {
		t.Fatalf("AuxMaxTokens(default) = %d, want 8192", got)
	}

	// A pinned aux budget wins outright.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"aux_max_tokens": 512}})
	if got := AuxMaxTokens(home); got != 512 {
		t.Fatalf("AuxMaxTokens(pinned) = %d, want 512", got)
	}

	// Otherwise min(8192, llm.max_tokens): a small main cap bounds aux calls.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"max_tokens": 1024}})
	if got := AuxMaxTokens(home); got != 1024 {
		t.Fatalf("AuxMaxTokens(max_tokens=1024) = %d, want 1024", got)
	}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"max_tokens": 20000}})
	if got := AuxMaxTokens(home); got != 8192 {
		t.Fatalf("AuxMaxTokens(max_tokens=20000) = %d, want 8192", got)
	}
	// A non-integer value is not an int: the default stays.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"aux_max_tokens": "512"}})
	if got := AuxMaxTokens(home); got != 8192 {
		t.Fatalf("AuxMaxTokens(string) = %d, want 8192", got)
	}
}

// Parity-exempt: no upstream test reads loop.max_tool_result_chars directly;
// the Go port consumes it in the queen and worker loop configs.
func TestLoopMaxToolResultCharsReadsTheLoopSection(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{})
	if got := LoopMaxToolResultChars(home, 30_000); got != 30_000 {
		t.Fatalf("fallback = %d", got)
	}
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": 4096}})
	if got := LoopMaxToolResultChars(home, 30_000); got != 4096 {
		t.Fatalf("configured = %d, want 4096", got)
	}
	// A wrong JSON type keeps the fallback (Python compares against int).
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": 4096.5}})
	if got := LoopMaxToolResultChars(home, 30_000); got != 30_000 {
		t.Fatalf("float = %d, want the fallback", got)
	}
	// Numeric text is coerced by the int field upstream, so it counts.
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": "4096"}})
	if got := LoopMaxToolResultChars(home, 30_000); got != 4096 {
		t.Fatalf("numeric string = %d, want 4096", got)
	}
	// Empty and null values keep the fallback: reading them as 0 would
	// silently disable the spillover cap.
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": ""}})
	if got := LoopMaxToolResultChars(home, 30_000); got != 30_000 {
		t.Fatalf("empty string = %d, want the fallback", got)
	}
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": nil}})
	if got := LoopMaxToolResultChars(home, 30_000); got != 30_000 {
		t.Fatalf("null = %d, want the fallback", got)
	}
}

// TestChatLoopConfigAppliesTheLoopSection proves the queen loop reads the key:
// the session runtime builds its loop through chatLoopConfig.
//
// Parity: core/tests/test_worker_definition.py::TestBuildLoopConfigDict::test_propagates_queen_values
func TestChatLoopConfigAppliesTheLoopSection(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"loop": map[string]any{"max_tool_result_chars": 7777}})
	config := chatLoopConfig(t.TempDir(), home.Root, "")
	if config.MaxToolResultChars != 7777 {
		t.Fatalf("chat loop MaxToolResultChars = %d, want 7777", config.MaxToolResultChars)
	}
	empty := chatLoopConfig(t.TempDir(), "", "")
	if empty.MaxToolResultChars != 30_000 {
		t.Fatalf("chat loop default = %d, want 30000", empty.MaxToolResultChars)
	}
	// A queen with a model of her own budgets by her window, not the 180k
	// queen default.
	writeFeatureConfig(t, home, map[string]any{
		"queen_llm": map[string]any{"archivist": map[string]any{
			"provider": "openai", "model": "gpt-queen", "max_context_tokens": 32000,
		}},
	})
	if got := chatLoopConfig(t.TempDir(), home.Root, "archivist").MaxContextTokens; got != 32000 {
		t.Fatalf("queen MaxContextTokens = %d, want her section's 32000", got)
	}
	if got := chatLoopConfig(t.TempDir(), home.Root, "someone_else").MaxContextTokens; got != agentloop.DefaultLoopConfig().MaxContextTokens {
		t.Fatalf("main MaxContextTokens = %d, want the default", got)
	}
}

// Parity-exempt: worker_tools.always_enabled_categories has no upstream unit
// test; this pins configured_worker_always_enabled_categories' contract.
func TestWorkerToolsAlwaysEnabledCategories(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{})
	if categories, ok := WorkerToolsAlwaysEnabledCategories(home); ok {
		t.Fatalf("categories without the key = %#v, ok=%v", categories, ok)
	}
	if keepSet := WorkerKeepSetFromConfig(home); keepSet != nil {
		t.Fatal("keep set without the key must stay nil (hardcoded default applies)")
	}

	writeFeatureConfig(t, home, map[string]any{
		"worker_tools": map[string]any{"always_enabled_categories": []any{"terminal_core"}},
	})
	categories, ok := WorkerToolsAlwaysEnabledCategories(home)
	if !ok || len(categories) != 1 || categories[0] != "terminal_core" {
		t.Fatalf("categories = %#v, ok=%v", categories, ok)
	}
	keepSet := WorkerKeepSetFromConfig(home)
	if keepSet == nil {
		t.Fatal("keep set for a configured list is nil")
	}
	if _, present := keepSet()["terminal_exec"]; !present {
		t.Fatalf("terminal_core did not expand to its tools: %#v", keepSet())
	}

	// An empty list turns the split off: the closure returns an empty set
	// instead of nothing.
	writeFeatureConfig(t, home, map[string]any{
		"worker_tools": map[string]any{"always_enabled_categories": []any{}},
	})
	keepSet = WorkerKeepSetFromConfig(home)
	if keepSet == nil || len(keepSet()) != 0 {
		t.Fatalf("empty list keep set = %v, want a non-nil empty set", keepSet != nil)
	}

	// A mixed list is not an override (upstream requires every entry to be a
	// string) and so is a non-list value.
	for _, value := range []any{[]any{"terminal_core", 7}, "terminal_core", map[string]any{}} {
		writeFeatureConfig(t, home, map[string]any{
			"worker_tools": map[string]any{"always_enabled_categories": value},
		})
		if _, ok := WorkerToolsAlwaysEnabledCategories(home); ok {
			t.Fatalf("value %#v was accepted as an override", value)
		}
		if WorkerKeepSetFromConfig(home) != nil {
			t.Fatalf("value %#v produced a keep set", value)
		}
	}
}

// Parity: core/tests/test_session_unified_colony_runtime.py::test_unified_colony_uses_configured_worker_llm
func TestWorkerAndVisionSectionsResolve(t *testing.T) {
	home := newFeatureHome(t)
	t.Setenv("ANTIGRAVITY_CLIENT_ID", "")
	t.Setenv("ANTIGRAVITY_CLIENT_SECRET", "")
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{
			"provider": "anthropic",
			"model":    "claude-main",
			"api_key":  "main-key",
		},
		"worker_llm": map[string]any{
			"provider": "groq",
			"model":    "llama-worker",
			"num_ctx":  4096,
		},
		"vision_fallback": map[string]any{
			"provider":        "gemini",
			"model":           "gemini-3-flash",
			"api_key_env_var": "VISION_TEST_KEY",
			"api_base":        "https://vision.example/v1",
			"max_tokens":      640,
		},
		"llm_client": map[string]any{},
	})

	worker := WorkerLLMSection(home)
	if worker == nil || SectionModel(worker) != "groq/llama-worker" {
		t.Fatalf("worker section = %#v", worker)
	}
	if got := SectionAPIKey(worker, func() string { return "main-key" }); got != "main-key" {
		t.Fatalf("worker key fallback = %q", got)
	}

	vision := VisionFallbackSection(home)
	if vision == nil {
		t.Fatal("vision section = nil")
	}
	if got := VisionFallbackModel(home); got != "gemini/gemini-3-flash" {
		t.Fatalf("vision model = %q", got)
	}
	if got := SectionAPIBase(vision); got != "https://vision.example/v1" {
		t.Fatalf("vision base = %q", got)
	}
	if got := VisionFallbackMaxTokens(vision); got != 640 {
		t.Fatalf("vision max tokens = %d", got)
	}
	// api_key_env_var wins over the main key when it is set, and the main key
	// is the fallback when it is not.
	t.Setenv("VISION_TEST_KEY", "vision-key")
	if got := SectionAPIKey(vision, func() string { return "main-key" }); got != "vision-key" {
		t.Fatalf("vision key = %q, want the env var", got)
	}
	t.Setenv("VISION_TEST_KEY", "")
	if got := SectionAPIKey(vision, func() string { return "main-key" }); got != "main-key" {
		t.Fatalf("vision key fallback = %q", got)
	}
	// An empty vision block is not a configured slot.
	writeFeatureConfig(t, home, map[string]any{"vision_fallback": map[string]any{}})
	if section := VisionFallbackSection(home); section != nil {
		t.Fatalf("empty vision section = %#v", section)
	}
	if got := VisionFallbackModel(home); got != "" {
		t.Fatalf("empty vision model = %q", got)
	}
}

// Parity-exempt: the Antigravity client credentials have no upstream test; the
// order (env, then llm.antigravity_client_*) mirrors
// framework/config.py::get_antigravity_client_id.
func TestAntigravityClientCredentials(t *testing.T) {
	home := newFeatureHome(t)
	t.Setenv("ANTIGRAVITY_CLIENT_ID", "")
	t.Setenv("ANTIGRAVITY_CLIENT_SECRET", "")
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{
			"antigravity_client_id":     "file-id",
			"antigravity_client_secret": "file-secret",
		},
	})
	if got := AntigravityClientID(home); got != "file-id" {
		t.Fatalf("client id = %q", got)
	}
	if got := AntigravityClientSecret(home); got != "file-secret" {
		t.Fatalf("client secret = %q", got)
	}
	t.Setenv("ANTIGRAVITY_CLIENT_ID", "env-id")
	t.Setenv("ANTIGRAVITY_CLIENT_SECRET", "env-secret")
	if got := AntigravityClientID(home); got != "env-id" {
		t.Fatalf("client id with env = %q", got)
	}
	if got := AntigravityClientSecret(home); got != "env-secret" {
		t.Fatalf("client secret with env = %q", got)
	}
	writeFeatureConfig(t, home, map[string]any{})
	if got := AntigravityClientID(home); got != "env-id" {
		t.Fatalf("client id without file = %q", got)
	}
}

// TestSectionAPIKeyEndpointNeverLeaksAmbientKeys pins the endpoint rule: a slot
// served from a named endpoint gets that endpoint's key (or its placeholder),
// never an ambient provider env var.
//
// Parity-exempt: get_queen_api_key's endpoint branch in config.py has no
// upstream unit test; the Go test pins the resolution order ported from it.
func TestSectionAPIKeyEndpointNeverLeaksAmbientKeys(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-openai")
	section := map[string]any{
		"provider": "openai",
		"model":    "gpt-5",
		"endpoint": "magpie",
	}
	if got := SectionAPIKey(section, func() string { return "main-key" }); got == "ambient-openai" || got == "main-key" {
		t.Fatalf("endpoint key = %q, want the endpoint's own key", got)
	}
}

// TestSectionAPIKeyUsesTheProviderEnvVar is the non-endpoint half of the same
// chain: api_key_env_var first, then the slot provider's own environment
// variable, then the main key.
//
// Parity-exempt: get_queen_api_key / get_vision_fallback_api_key have no
// upstream unit test; the Go test pins the chain ported from config.py.
func TestSectionAPIKeyUsesTheProviderEnvVar(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "groq-env")
	section := map[string]any{"provider": "groq", "model": "llama-worker"}
	if got := SectionAPIKey(section, func() string { return "main-key" }); got != "groq-env" {
		t.Fatalf("key = %q, want the provider env var", got)
	}
	t.Setenv("GROQ_API_KEY", "")
	if got := SectionAPIKey(section, func() string { return "main-key" }); got != "main-key" {
		t.Fatalf("key fallback = %q", got)
	}
	if got := SectionAPIKey(map[string]any{"provider": "groq"}, nil); got != "" {
		t.Fatalf("key without any source = %q", got)
	}
}

// TestHiveMaxIterCapsTheQueenLoop pins HIVE_MAX_ITER: a positive integer caps
// the queen's reply loop; unset, non-numeric and non-positive values keep the
// queen profile's 999_999 autonomous default.
//
// Parity-exempt: upstream's HIVE_MAX_ITER read lives inline in
// queen_orchestrator and has no test of its own.
func TestHiveMaxIterCapsTheQueenLoop(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{})

	t.Setenv("HIVE_MAX_ITER", "3")
	if got := chatLoopConfig(t.TempDir(), home.Root, "").MaxIterations; got != 3 {
		t.Fatalf("MaxIterations = %d, want the env cap 3", got)
	}
	// The fallback is the queen profile's 999_999, not DefaultLoopConfig's 50:
	// the old base made a normal DM queen stop at 50 inner turns.
	if err := os.Unsetenv("HIVE_MAX_ITER"); err != nil {
		t.Fatalf("unset HIVE_MAX_ITER: %v", err)
	}
	if got, want := chatLoopConfig(t.TempDir(), home.Root, "").MaxIterations, agentloop.QueenLoopConfig().MaxIterations; got != want {
		t.Fatalf("unset HIVE_MAX_ITER gave MaxIterations %d, want the queen default %d", got, want)
	}
	for _, value := range []string{"", "  ", "abc", "0", "-2"} {
		t.Setenv("HIVE_MAX_ITER", value)
		if got, want := chatLoopConfig(t.TempDir(), home.Root, "").MaxIterations, agentloop.QueenLoopConfig().MaxIterations; got != want {
			t.Fatalf("HIVE_MAX_ITER=%q gave MaxIterations %d, want the queen default %d", value, got, want)
		}
	}
}
