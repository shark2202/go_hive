package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func writeTestConfig(t *testing.T, home string, config map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), data, 0o600); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
}

func testHome(t *testing.T) (hivehome.Home, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HIVE_HOME", dir)
	home, err := hivehome.Resolve()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home, dir
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; a
// pre-injected llm is kept untouched (initialize returns early).
func TestLLMProviderKeepsInjectedCompleter(t *testing.T) {
	_, dir := testHome(t)
	// A config that would build a different provider: it must be ignored.
	writeTestConfig(t, dir, map[string]any{
		"llm": map[string]any{"provider": "openai", "model": "gpt-4o-mini", "api_key": "sk-x"},
	})
	injected := llm.NewMockProvider("injected")
	stage := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
		LLM:       injected,
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if stage.LLM != injected {
		t.Fatalf("injected completer was replaced: %T", stage.LLM)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; mock_mode
// installs the deterministic completer with the resolved model.
func TestLLMProviderMockModeInstallsMock(t *testing.T) {
	testHome(t)
	stage, err := BuildStage(map[string]any{
		"type":   "llm_provider",
		"config": map[string]any{"model": "mock-model", "mock_mode": true},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	provider := stage.(*LLMProviderStage)
	if err := provider.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	mock, ok := provider.LLM.(*llm.MockProvider)
	if !ok {
		t.Fatalf("completer = %T, want *llm.MockProvider", provider.LLM)
	}
	if mock.ModelName() != "mock-model" {
		t.Fatalf("mock model = %s", mock.ModelName())
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; the
// default resolver reads the llm section like get_preferred_model /
// get_api_key: model join, api_key, api_base and the key pool.
func TestLLMProviderResolvesFromConfig(t *testing.T) {
	_, dir := testHome(t)
	writeTestConfig(t, dir, map[string]any{
		"llm": map[string]any{
			"provider": "anthropic",
			"model":    "claude-sonnet-5-5",
			"api_key":  "sk-ant-test",
			"api_base": "https://example.invalid/anthropic",
		},
	})
	stage := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	provider, ok := stage.LLM.(*llm.Provider)
	if !ok {
		t.Fatalf("completer = %T, want *llm.Provider", stage.LLM)
	}
	if provider.Model != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("model = %q", provider.Model)
	}
	if provider.APIKey != "sk-ant-test" {
		t.Fatalf("api key = %q", provider.APIKey)
	}
	if provider.APIBase != "https://example.invalid/anthropic" {
		t.Fatalf("api base = %q", provider.APIBase)
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; without
// an llm section the resolver falls back to get_preferred_model's default
// pair, and a configured key pool attaches for rotation.
func TestLLMProviderDefaultsAndKeyPool(t *testing.T) {
	_, dir := testHome(t)
	stage := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if provider := stage.LLM.(*llm.Provider); provider.Model != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("default model = %q", provider.Model)
	}

	writeTestConfig(t, dir, map[string]any{
		"llm": map[string]any{
			"provider": "openai",
			"model":    "gpt-4o-mini",
			"api_keys": []any{"sk-1", "sk-2"},
		},
	})
	pooled := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
	}
	if err := pooled.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	provider := pooled.LLM.(*llm.Provider)
	if provider.Model != "openai/gpt-4o-mini" || provider.APIKey != "sk-1" {
		t.Fatalf("pooled provider = model %q key %q", provider.Model, provider.APIKey)
	}
	if provider.KeyPool == nil {
		t.Fatal("key pool not attached")
	}
}

// Parity-exempt: upstream ships no unit tests for framework/pipeline; an
// injected resolver receives the configured model id.
func TestLLMProviderResolverInjectionUsed(t *testing.T) {
	stage := &LLMProviderStage{
		BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10},
		Model:     "custom/model",
	}
	seen := ""
	stage.Resolver = func(model string) (llm.Completer, error) {
		seen = model
		return llm.NewMockProvider(model), nil
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if seen != "custom/model" {
		t.Fatalf("resolver saw %q", seen)
	}
	if stage.LLM == nil {
		t.Fatal("resolver output not installed")
	}
}
