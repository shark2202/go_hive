package server

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: framework/config.py::RuntimeConfig — the aggregate carries the
// preferred model, the 0.7 temperature default, the main slot's token/context
// budgets, the resolved key/base and the provider extra kwargs.
func TestLoadRuntimeConfigAggregatesTheMainSlot(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, home.ConfigurationFile(), `{"llm": {
		"provider": "anthropic",
		"model": "claude-sonnet-5-5",
		"api_key": "sk-ant-test",
		"api_base": "https://example.invalid/anthropic",
		"max_tokens": 1234,
		"max_context_tokens": 5678,
		"extra_body": {"thinking": {"type": "disabled"}}
	}}`)

	config := LoadRuntimeConfig(home, "")
	if config.Model != "anthropic/claude-sonnet-5-5" {
		t.Fatalf("model = %q", config.Model)
	}
	if config.Temperature != 0.7 {
		t.Fatalf("temperature = %v, want upstream dataclass default 0.7", config.Temperature)
	}
	if config.MaxTokens != 1234 || config.MaxContextTokens != 5678 {
		t.Fatalf("budgets = %d/%d", config.MaxTokens, config.MaxContextTokens)
	}
	if config.APIKey != "sk-ant-test" || config.APIBase != "https://example.invalid/anthropic" {
		t.Fatalf("key/base = %q/%q", config.APIKey, config.APIBase)
	}
	extraBody, ok := config.ExtraKwargs["extra_body"].(map[string]any)
	if !ok || extraBody["thinking"] == nil {
		t.Fatalf("extra kwargs = %#v", config.ExtraKwargs)
	}
	if _, present := config.ExtraKwargs["temperature"]; present {
		t.Fatal("temperature must not be injected: upstream build_llm does not pass the dataclass value")
	}

	// An explicit request model wins as written, as RuntimeConfig(model=...) does.
	if config := LoadRuntimeConfig(home, "custom/local-model"); config.Model != "custom/local-model" {
		t.Fatalf("requested model = %q", config.Model)
	}
}

// Parity: framework/config.py::get_max_context_tokens — llm.max_context_tokens,
// else the catalog window, else DEFAULT_MAX_CONTEXT_TOKENS (32k).
func TestLLMMaxContextTokensFallsBackToTheUpstreamDefault(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := LLMMaxContextTokens(home, DefaultRuntimeMaxContextTokens); got != 32_000 {
		t.Fatalf("max_context_tokens = %d, want 32000", got)
	}
}
