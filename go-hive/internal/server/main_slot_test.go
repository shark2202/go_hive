package server

// Tests for the main llm slot's runtime resolution (main_slot.go): the key
// order, base, model shape, pool rotation and extra request fields the session
// builder consumes.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// clearMainSlotEnv keeps the ambient environment out of the resolution tests.
func clearMainSlotEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HIVE_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "KIMI_API_KEY",
		"OPENROUTER_API_KEY", "ZAI_API_KEY", "MINIMAX_API_KEY",
		"ANTIGRAVITY_CLIENT_ID", "ANTIGRAVITY_CLIENT_SECRET", "HIVE_CREDENTIAL_KEY",
	} {
		t.Setenv(name, "")
	}
}

// Parity: core/tests/test_config.py::TestOpenRouterConfig::test_get_preferred_model_normalizes_openrouter_prefixed_model
func TestMainSlotModelJoinsProviderAndModelLikePython(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5.5",
	}})
	if got := MainSlotModel(home, ""); got != "openai/gpt-5.5" {
		t.Fatalf("MainSlotModel = %q, want openai/gpt-5.5", got)
	}

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openrouter", "model": "x-ai/grok-4.20-beta",
	}})
	if got := MainSlotModel(home, ""); got != "openrouter/x-ai/grok-4.20-beta" {
		t.Fatalf("MainSlotModel = %q, want the openrouter join", got)
	}

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openrouter", "model": "openrouter/x-ai/grok-4.20-beta",
	}})
	if got := MainSlotModel(home, ""); got != "openrouter/x-ai/grok-4.20-beta" {
		t.Fatalf("MainSlotModel = %q, want the prefixed id unwrapped once", got)
	}

	// An explicit request wins as given, exactly like RuntimeConfig(model=...).
	if got := MainSlotModel(home, "custom-model"); got != "custom-model" {
		t.Fatalf("MainSlotModel(request) = %q, want the explicit model", got)
	}

	// No usable pair: Python's preferred model.
	writeFeatureConfig(t, home, map[string]any{})
	if got := MainSlotModel(home, ""); got != DefaultPreferredModel {
		t.Fatalf("MainSlotModel fallback = %q, want %q", got, DefaultPreferredModel)
	}
}

// Parity-exempt: get_api_key has no Python unit test; the order is read from
// core/framework/config.py::get_api_key and pinned here for the runtime path.
func TestMainSlotKeyFollowsTheUpstreamOrder(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)

	// Literal beats every ambient source.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "api_key": "literal-key",
		"api_key_env_var": "OPENAI_API_KEY",
	}})
	t.Setenv("OPENAI_API_KEY", "env-key")
	if got := MainSlotKey(home); got != "literal-key" {
		t.Fatalf("MainSlotKey = %q, want the literal key", got)
	}

	// The key pool's first key beats the literal one (get_api_key checks the
	// pool before llm.api_key).
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "api_key": "literal-key",
		"api_keys": []any{"pool-1", "", "pool-2"},
	}})
	if got := MainSlotKey(home); got != "pool-1" {
		t.Fatalf("MainSlotKey = %q, want the first pool key", got)
	}
	if keys := MainSlotAPIKeys(home); len(keys) != 2 || keys[1] != "pool-2" {
		t.Fatalf("MainSlotAPIKeys = %#v, want empties filtered", keys)
	}

	// api_key_env_var, then the provider's conventional variable.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "api_key_env_var": "MY_OPENAI_KEY",
	}})
	t.Setenv("MY_OPENAI_KEY", "custom-env-key")
	if got := MainSlotKey(home); got != "custom-env-key" {
		t.Fatalf("MainSlotKey = %q, want the api_key_env_var value", got)
	}
	t.Setenv("MY_OPENAI_KEY", "")
	t.Setenv("OPENAI_API_KEY", "provider-env-key")
	if got := MainSlotKey(home); got != "provider-env-key" {
		t.Fatalf("MainSlotKey = %q, want the provider env var value", got)
	}

	// A section that only names a model resolves the provider from it.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"model": "claude-sonnet-4-5"}})
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-env-key")
	if got := MainSlotKey(home); got != "anthropic-env-key" {
		t.Fatalf("MainSlotKey = %q, want the inferred provider's env var", got)
	}

	// The named endpoint's own key is a placeholder that never leaks ambient
	// keys (get_api_key checks the endpoint first).
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "openai", "model": "gpt-5", "endpoint": "ollama"},
	})
	if got := MainSlotKey(home); got != "no-key" {
		t.Fatalf("MainSlotKey = %q, want the endpoint placeholder", got)
	}
}

// Parity-exempt: the subscription token sources have no Python unit test; the
// readers are read from core/framework/loader/agent_loader.py.
func TestMainSlotProviderReadsTheSubscriptionTokens(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Kimi Code: token from ~/.kimi/config.toml, base from the built-in.
	kimiDir := filepath.Join(fakeHome, ".kimi")
	if err := os.MkdirAll(kimiDir, 0o755); err != nil {
		t.Fatalf("mkdir kimi dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(kimiDir, "config.toml"), []byte(
		"[providers.kimi-for-coding]\napi_key = \"kimi-token\"\n"), 0o644); err != nil {
		t.Fatalf("write kimi config: %v", err)
	}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "kimi", "model": "kimi-k2.6", "use_kimi_code_subscription": true,
	}})
	provider, err := MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(kimi) = %v", err)
	}
	if provider.APIKey != "kimi-token" {
		t.Fatalf("kimi api key = %q, want the config.toml token", provider.APIKey)
	}
	if provider.APIBase != llm.KimiAPIBase {
		t.Fatalf("kimi api base = %q, want %q", provider.APIBase, llm.KimiAPIBase)
	}
	if provider.Model != "kimi/kimi-k2.6" {
		t.Fatalf("kimi model = %q, want the joined pair", provider.Model)
	}

	// Codex: token from ~/.codex/auth.json; api_base is the ChatGPT backend.
	codexDir := filepath.Join(fakeHome, ".codex")
	if err := os.MkdirAll(codexDir, 0o755); err != nil {
		t.Fatalf("mkdir codex dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(
		`{"tokens":{"access_token":"codex-token","account_id":"acct-1"}}`), 0o644); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5.3-codex", "use_codex_subscription": true,
	}})
	provider, err = MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(codex) = %v", err)
	}
	if provider.APIKey != "codex-token" {
		t.Fatalf("codex api key = %q", provider.APIKey)
	}
	if provider.APIBase != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("codex api base = %q", provider.APIBase)
	}
	headers, _ := provider.ExtraKwargs["extra_headers"].(map[string]any)
	if headers["Authorization"] != "Bearer codex-token" || headers["ChatGPT-Account-Id"] != "acct-1" {
		t.Fatalf("codex extra headers = %#v", provider.ExtraKwargs)
	}

	// Claude Code: token from ~/.claude/.credentials.json, Bearer header.
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(
		`{"claudeAiOauth":{"accessToken":"claude-token"}}`), 0o644); err != nil {
		t.Fatalf("write claude creds: %v", err)
	}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "anthropic", "model": "claude-opus-4-6", "use_claude_code_subscription": true,
	}})
	provider, err = MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(claude) = %v", err)
	}
	if provider.APIKey != "claude-token" {
		t.Fatalf("claude api key = %q", provider.APIKey)
	}
	claudeHeaders, _ := provider.ExtraKwargs["extra_headers"].(map[string]any)
	if claudeHeaders["authorization"] != "Bearer claude-token" {
		t.Fatalf("claude extra headers = %#v", provider.ExtraKwargs)
	}
}

// Parity-exempt: the Antigravity provider is not ported; this pins the loud
// refusal rather than a silent OpenAI request without a key.
func TestMainSlotProviderRefusesAntigravitySubscription(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)
	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{
			"provider": "openai", "model": "gemini-3-flash", "use_antigravity_subscription": true,
		},
		"antigravity-accounts": "unused",
	})
	if _, err := MainSlotProvider(home, ""); err == nil || !strings.Contains(err.Error(), "Antigravity") {
		t.Fatalf("MainSlotProvider(antigravity) err = %v, want the unsupported error", err)
	}
}

// Parity: core/tests/test_config.py::TestOpenRouterConfig::test_get_api_base_falls_back_to_openrouter_default
func TestMainSlotAPIBasePrefersSubscriptionEndpointsThenTheSection(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openrouter", "model": "x-ai/grok-4.20-beta",
	}})
	if got := MainSlotAPIBase(home); got != llm.OpenRouterAPIBase {
		t.Fatalf("openrouter base = %q", got)
	}

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openrouter", "model": "x", "api_base": "https://proxy.example/v1",
	}})
	if got := MainSlotAPIBase(home); got != "https://proxy.example/v1" {
		t.Fatalf("explicit base = %q", got)
	}

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "kimi", "model": "kimi-k2.6", "use_kimi_code_subscription": true,
		"api_base": "https://ignored.example",
	}})
	if got := MainSlotAPIBase(home); got != llm.KimiAPIBase {
		t.Fatalf("kimi base = %q, want the subscription base to win", got)
	}
}

// Parity: core/tests/test_config.py::TestOpenRouterConfig::test_get_preferred_model_for_openrouter
func TestMainSlotProviderAppliesExtraBodyAndOllamaNumCtx(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)
	t.Setenv("OPENAI_API_KEY", "openai-key")

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "deepseek-flash",
		"api_base":   "https://api.deepseek.com/v1",
		"extra_body": map[string]any{"reasoning_effort": "low"},
	}})
	provider, err := MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider = %v", err)
	}
	extra, _ := provider.ExtraKwargs["extra_body"].(map[string]any)
	if extra["reasoning_effort"] != "low" {
		t.Fatalf("extra_body = %#v", provider.ExtraKwargs)
	}

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "ollama", "model": "llama3", "num_ctx": 32768,
	}})
	provider, err = MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(ollama) = %v", err)
	}
	if provider.ExtraKwargs["num_ctx"] != 32768 {
		t.Fatalf("num_ctx = %#v", provider.ExtraKwargs)
	}
}

// Parity-exempt: llm.api_keys' rotation has no Python unit test in this tree;
// the behaviour ports framework/llm/key_pool.py.
func TestMainSlotProviderRotatesTheConfiguredKeyPool(t *testing.T) {
	var mu sync.Mutex
	seen := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	t.Cleanup(server.Close)

	home := newFeatureHome(t)
	clearMainSlotEnv(t)
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "api_base": server.URL + "/v1",
		"api_keys": []any{"key-a", "key-b"},
	}})
	provider, err := MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider = %v", err)
	}
	if provider.KeyPool == nil {
		t.Fatal("pool not installed for 2+ keys")
	}
	for i := 0; i < 2; i++ {
		if _, err := provider.Complete(context.Background(), []map[string]any{{"role": "user", "content": "hi"}}, llm.CompleteOptions{}); err != nil {
			t.Fatalf("Complete %d: %v", i, err)
		}
	}
	if len(seen) != 2 || seen[0] != "Bearer key-a" || seen[1] != "Bearer key-b" {
		t.Fatalf("authorization headers = %#v, want rotation through key-a then key-b", seen)
	}

	// A single key stays the literal key, no pool.
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "api_base": server.URL + "/v1",
		"api_keys": []any{"only-key"},
	}})
	provider, err = MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(single) = %v", err)
	}
	if provider.KeyPool != nil {
		t.Fatal("pool installed for a single key")
	}
}

// Parity-exempt: the readers below are the uncalled Python counterparts
// (get_worker_max_tokens); pinned so the fields keep their upstream rules.
func TestWorkerMaxTokensMirrorsTheUncalledPythonReader(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)

	writeFeatureConfig(t, home, map[string]any{
		"llm":        map[string]any{"provider": "openai", "model": "gpt-5", "max_tokens": 111},
		"worker_llm": map[string]any{"provider": "openai", "model": "gpt-5-mini", "max_tokens": 222},
	})
	if got := WorkerMaxTokens(home); got != 222 {
		t.Fatalf("WorkerMaxTokens(file) = %d", got)
	}

	writeFeatureConfig(t, home, map[string]any{
		"llm":        map[string]any{"provider": "openai", "model": "gpt-5", "max_tokens": 111},
		"worker_llm": map[string]any{"provider": "openai", "model": "gpt-5.5"},
	})
	if got := WorkerMaxTokens(home); got != 128000 {
		t.Fatalf("WorkerMaxTokens(catalog) = %d, want the worker catalog cap", got)
	}

	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "openai", "model": "gpt-5", "max_tokens": 111},
	})
	if got := WorkerMaxTokens(home); got != 111 {
		t.Fatalf("WorkerMaxTokens(main fallback) = %d", got)
	}
}

// Parity: core/tests/test_config.py::test_memory_reflection_config
func TestQueenMaxContextTokensFallsBackToTheMainSlot(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)

	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai", "model": "gpt-5", "max_context_tokens": 32000,
	}})
	if got := QueenMaxContextTokens(home, "default", 180000); got != 32000 {
		t.Fatalf("QueenMaxContextTokens = %d, want the main slot's window", got)
	}

	writeFeatureConfig(t, home, map[string]any{
		"llm": map[string]any{"provider": "openai", "model": "gpt-5", "max_context_tokens": 32000},
		"queen_llm": map[string]any{"archivist": map[string]any{
			"provider": "openai", "model": "gpt-5-mini", "max_context_tokens": 8000,
		}},
	})
	if got := QueenMaxContextTokens(home, "archivist", 180000); got != 8000 {
		t.Fatalf("QueenMaxContextTokens(own) = %d", got)
	}

	writeFeatureConfig(t, home, map[string]any{})
	if got := QueenMaxContextTokens(home, "default", 180000); got != 180000 {
		t.Fatalf("QueenMaxContextTokens(empty) = %d, want the fallback", got)
	}
}

// Parity-exempt: the malformed-JSON fallback has no Python path test for the
// main slot; the shape mirrors core/tests/test_config.py's get_hive_config.
func TestMainSlotProviderReadsTheLiteralKeyIntoTheTransport(t *testing.T) {
	home := newFeatureHome(t)
	clearMainSlotEnv(t)
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "anthropic", "model": "claude-opus-4-6", "api_key": "sk-literal",
	}})
	provider, err := MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider = %v", err)
	}
	if provider.APIKey != "sk-literal" {
		t.Fatalf("api key = %q", provider.APIKey)
	}
	if provider.Model != "anthropic/claude-opus-4-6" {
		t.Fatalf("model = %q", provider.Model)
	}

	// PUT /api/config/llm-sections round-trips the section verbatim, and the
	// runtime consumes the written fields (extra_body included).
	section := map[string]any{
		"provider": "openai", "model": "gpt-5", "api_key": "sk-written",
		"extra_body": map[string]any{"reasoning_effort": "high"},
	}
	if status, _ := WriteRoleSection(home, "llm", section, false, RoleWriteDeps{}); status != 200 {
		t.Fatalf("WriteRoleSection status = %d", status)
	}
	readback := GetLLMSections(HiveConfig(home))["llm"].(map[string]any)
	if readback["api_key"] != "sk-written" || readback["extra_body"].(map[string]any)["reasoning_effort"] != "high" {
		t.Fatalf("readback = %#v", readback)
	}
	provider, err = MainSlotProvider(home, "")
	if err != nil {
		t.Fatalf("MainSlotProvider(after PUT) = %v", err)
	}
	if provider.APIKey != "sk-written" {
		t.Fatalf("post-PUT api key = %q", provider.APIKey)
	}
	extra, _ := provider.ExtraKwargs["extra_body"].(map[string]any)
	if extra["reasoning_effort"] != "high" {
		t.Fatalf("post-PUT extra_body = %#v", provider.ExtraKwargs)
	}
}
