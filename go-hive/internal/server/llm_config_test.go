package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	previous, had := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(name, previous)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func clearLLMEnv(t *testing.T) {
	t.Helper()
	unsetEnv(t, "HIVE_CREDENTIAL_KEY")
	seen := map[string]bool{}
	for _, entry := range providerEnvVars {
		if seen[entry.EnvVar] {
			continue
		}
		seen[entry.EnvVar] = true
		unsetEnv(t, entry.EnvVar)
	}
}

func testLLMDeps() LLMRouteDeps {
	return LLMRouteDeps{
		CredentialKey:       func(string) (string, bool) { return "", false },
		SubscriptionToken:   func(string) (string, bool) { return "", false },
		DetectSubscriptions: func() []string { return nil },
		Validate:            func(string, string, *string, *string) map[string]any { return map[string]any{"valid": true} },
		HotSwap:             func(string, *string, *string) int { return 0 },
	}
}

type hotSwapCall struct {
	fullModel string
	apiKey    *string
	apiBase   *string
}

func recordingHotSwap(calls *[]hotSwapCall, swapped int) func(string, *string, *string) int {
	return func(fullModel string, apiKey, apiBase *string) int {
		*calls = append(*calls, hotSwapCall{fullModel: fullModel, apiKey: apiKey, apiBase: apiBase})
		return swapped
	}
}

type validateCall struct {
	provider string
	apiKey   string
	apiBase  *string
	model    *string
}

func requireNumber(t *testing.T, got any, want float64) {
	t.Helper()
	value, ok := got.(float64)
	if !ok || value != want {
		t.Fatalf("value = %#v, want %v", got, want)
	}
}

func requireAbsent(t *testing.T, object map[string]any, key string) {
	t.Helper()
	if value, present := object[key]; present {
		t.Fatalf("%q should be absent, got %#v", key, value)
	}
}

func writeRawConfig(t *testing.T, home hivehome.Home, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(home.ConfigurationFile()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func llmBlockOnDisk(t *testing.T, home hivehome.Home) map[string]any {
	t.Helper()
	block, ok := readFeatureConfig(t, home)["llm"].(map[string]any)
	if !ok {
		t.Fatalf("configuration.json has no llm object: %#v", readFeatureConfig(t, home))
	}
	return block
}

func deref(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// Parity-exempt: pins the config-file spelling of a numeric provider, which
func TestTheLLMConfigRouteKeepsAConfigProviderNumberAsWritten(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": 123,
		"api_base": "http://x/v1",
	}})

	var calls []hotSwapCall
	deps := testLLMDeps()
	deps.HotSwap = recordingHotSwap(&calls, 0)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"custom": true,
		"model":  "m2",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "123" {
		t.Fatalf("provider = %#v, want the config integer as Python str()", body["provider"])
	}
	if disk := llmBlockOnDisk(t, home); disk["provider"] != "123" {
		t.Fatalf("disk provider = %#v, want \"123\"", disk["provider"])
	}
	if len(calls) != 1 || calls[0].fullModel != "123/m2" {
		t.Fatalf("hot-swap calls = %#v", calls)
	}
	if deref(calls[0].apiBase) != "http://x/v1" {
		t.Fatalf("hot-swap api base = %#v", deref(calls[0].apiBase))
	}
}

// Parity: framework/server/tests/test_api.py::TestConfigRoutes::test_get_llm_config_exposes_subscription_defaults_from_presets
func TestTheLLMConfigRouteExposesSubscriptionDefaultsFromPresets(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	rows, ok := body["subscriptions"].([]any)
	if !ok {
		t.Fatalf("subscriptions = %#v, want a list", body["subscriptions"])
	}
	byID := make(map[string]map[string]any, len(rows))
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("subscription row = %#v, want an object", row)
		}
		byID[entry["id"].(string)] = entry
	}

	if got := byID["codex"]["default_model"]; got != "gpt-5.3-codex" {
		t.Fatalf("codex.default_model = %#v", got)
	}
	if got := byID["codex"]["api_base"]; got != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("codex.api_base = %#v", got)
	}
	if got := byID["kimi_code"]["default_model"]; got != "kimi-k2.6" {
		t.Fatalf("kimi_code.default_model = %#v", got)
	}
}

// Parity: framework/server/tests/test_api.py::TestConfigRoutes::test_a_key_written_in_the_config_counts_as_configured
func TestAKeyWrittenInTheConfigCountsAsConfigured(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider": "openai",
		"model":    "local-qwen",
		"api_base": "http://127.0.0.1:9/v1",
		"api_key":  "sk-in-config",
	}})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["has_api_key"] != true {
		t.Fatalf("has_api_key = %#v, want true", body["has_api_key"])
	}
}

// Parity-exempt: exercises handle_update_llm_config's custom-endpoint branch,
func TestTheLLMConfigRouteSwitchesToACustomEndpoint(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("HIVE_TEST_LLM_KEY", "sk-custom")
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider":               "openai",
		"model":                  "old-model",
		"api_base":               "http://old.example/v1",
		"use_codex_subscription": true,
	}})

	var calls []hotSwapCall
	deps := testLLMDeps()
	deps.HotSwap = recordingHotSwap(&calls, 1)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"custom":             true,
		"model":              "  my-local  ",
		"provider":           "openrouter",
		"api_base":           " http://127.0.0.1:8000/v1 ",
		"api_key_env_var":    "HIVE_TEST_LLM_KEY",
		"max_tokens":         4321,
		"max_context_tokens": 8765,
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "openrouter" || body["model"] != "my-local" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	if body["has_api_key"] != true {
		t.Fatalf("has_api_key = %#v, want true", body["has_api_key"])
	}
	requireNumber(t, body["max_tokens"], 4321)
	requireNumber(t, body["max_context_tokens"], 8765)
	requireNumber(t, body["sessions_swapped"], 1)
	if body["active_subscription"] != nil {
		t.Fatalf("active_subscription = %#v, want null", body["active_subscription"])
	}
	if body["api_base"] != "http://127.0.0.1:8000/v1" {
		t.Fatalf("api_base = %#v", body["api_base"])
	}
	if body["api_key_env_var"] != "HIVE_TEST_LLM_KEY" {
		t.Fatalf("api_key_env_var = %#v", body["api_key_env_var"])
	}

	disk := llmBlockOnDisk(t, home)
	if disk["provider"] != "openrouter" || disk["model"] != "my-local" {
		t.Fatalf("disk provider/model = %#v/%#v", disk["provider"], disk["model"])
	}
	requireNumber(t, disk["max_tokens"], 4321)
	requireNumber(t, disk["max_context_tokens"], 8765)
	if disk["api_base"] != "http://127.0.0.1:8000/v1" {
		t.Fatalf("disk api_base = %#v", disk["api_base"])
	}
	if disk["api_key_env_var"] != "HIVE_TEST_LLM_KEY" {
		t.Fatalf("disk api_key_env_var = %#v", disk["api_key_env_var"])
	}
	requireAbsent(t, disk, "use_codex_subscription")

	if len(calls) != 1 {
		t.Fatalf("hot-swap calls = %d, want 1", len(calls))
	}
	if calls[0].fullModel != "openrouter/my-local" {
		t.Fatalf("hot-swap model = %q", calls[0].fullModel)
	}
	if deref(calls[0].apiKey) != "sk-custom" {
		t.Fatalf("hot-swap api key = %#v", deref(calls[0].apiKey))
	}
	if deref(calls[0].apiBase) != "http://127.0.0.1:8000/v1" {
		t.Fatalf("hot-swap api base = %#v", deref(calls[0].apiBase))
	}
}

// Parity-exempt: custom mode rejects an empty model before touching the config;
func TestTheLLMConfigRouteRequiresAModelForACustomEndpoint(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"custom": true,
		"model":  "   ",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "'model' is required" {
		t.Fatalf("error = %#v", body["error"])
	}
	if _, err := os.Stat(home.ConfigurationFile()); !os.IsNotExist(err) {
		t.Fatalf("configuration.json must not be written, stat err = %v", err)
	}
}

// Parity-exempt: exercises the subscription mode's success path, including the
func TestTheLLMConfigRouteActivatesASubscription(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"api_key_env_var": "OPENAI_API_KEY",
		"api_base":        "http://stale.example/v1",
	}})

	var validations []validateCall
	var calls []hotSwapCall
	deps := testLLMDeps()
	deps.SubscriptionToken = func(id string) (string, bool) {
		if id == "codex" {
			return "tok-codex", true
		}
		return "", false
	}
	deps.Validate = func(provider, apiKey string, apiBase, model *string) map[string]any {
		validations = append(validations, validateCall{
			provider: provider, apiKey: apiKey, apiBase: apiBase, model: model,
		})
		return map[string]any{"valid": true}
	}
	deps.HotSwap = recordingHotSwap(&calls, 2)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": "codex",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "openai" || body["model"] != "gpt-5.3-codex" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	requireNumber(t, body["max_tokens"], 16384)
	requireNumber(t, body["max_context_tokens"], 120000)
	requireNumber(t, body["sessions_swapped"], 2)
	if body["active_subscription"] != "codex" {
		t.Fatalf("active_subscription = %#v", body["active_subscription"])
	}
	if body["has_api_key"] != true {
		t.Fatalf("has_api_key = %#v, want true", body["has_api_key"])
	}

	disk := llmBlockOnDisk(t, home)
	if disk["provider"] != "openai" || disk["model"] != "gpt-5.3-codex" {
		t.Fatalf("disk provider/model = %#v/%#v", disk["provider"], disk["model"])
	}
	requireNumber(t, disk["max_tokens"], 16384)
	requireNumber(t, disk["max_context_tokens"], 120000)
	if disk["use_codex_subscription"] != true {
		t.Fatalf("disk use_codex_subscription = %#v", disk["use_codex_subscription"])
	}
	if disk["api_base"] != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("disk api_base = %#v", disk["api_base"])
	}
	requireAbsent(t, disk, "api_key_env_var")

	if len(validations) != 1 {
		t.Fatalf("validation calls = %d, want 1", len(validations))
	}
	if validations[0].provider != "openai" || validations[0].apiKey != "tok-codex" {
		t.Fatalf("validation call = %#v", validations[0])
	}
	if deref(validations[0].apiBase) != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("validation api base = %#v", deref(validations[0].apiBase))
	}
	if deref(validations[0].model) != "gpt-5.3-codex" {
		t.Fatalf("validation model = %#v", deref(validations[0].model))
	}
	if len(calls) != 1 || calls[0].fullModel != "openai/gpt-5.3-codex" {
		t.Fatalf("hot-swap calls = %#v", calls)
	}
	if deref(calls[0].apiKey) != "tok-codex" || deref(calls[0].apiBase) != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("hot-swap credentials = %#v", calls[0])
	}
}

// Parity-exempt: covers the subscription branch's 400 for an id that is not in
func TestTheLLMConfigRouteRejectsAnUnknownSubscription(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": "nope",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "Unknown subscription: nope" {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: a list subscription id is unhashable upstream (TypeError ->
func TestTheLLMConfigRouteRejectsAnUnhashableSubscription(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": []any{"codex"},
	})
	if status != 500 {
		t.Fatalf("status = %d, want 500 (body %#v)", status, body)
	}
	if body["error"] != internalServerError {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: covers the subscription branch's missing-token 400.
func TestTheLLMConfigRouteReportsAMissingSubscriptionCredential(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": "codex",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	want := "No credential found for OpenAI Codex Subscription. Please check your subscription or API key."
	if body["error"] != want {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: covers the subscription branch's validation-failure 400 and
func TestTheLLMConfigRouteReportsASubscriptionValidationFailure(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	deps := testLLMDeps()
	deps.SubscriptionToken = func(id string) (string, bool) {
		return "expired", id == "codex"
	}
	deps.Validate = func(string, string, *string, *string) map[string]any {
		return map[string]any{"valid": false, "message": "token expired"}
	}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": "codex",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "OpenAI Codex Subscription key validation failed: token expired" {
		t.Fatalf("error = %#v", body["error"])
	}
	if _, err := os.Stat(home.ConfigurationFile()); !os.IsNotExist(err) {
		t.Fatalf("configuration.json must not be written, stat err = %v", err)
	}
}

// Parity-exempt: exercises the API-key mode's success path, including the
func TestTheLLMConfigRouteStoresAnAPIKeyModel(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider":               "anthropic",
		"model":                  "old",
		"api_base":               "https://chatgpt.com/backend-api/codex",
		"use_codex_subscription": true,
	}})

	var calls []hotSwapCall
	deps := testLLMDeps()
	deps.CredentialKey = func(id string) (string, bool) {
		if id == "openai" {
			return "sk-openai", true
		}
		return "", false
	}
	deps.HotSwap = recordingHotSwap(&calls, 3)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": "openai",
		"model":    "gpt-5.5",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "openai" || body["model"] != "gpt-5.5" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	if body["has_api_key"] != true {
		t.Fatalf("has_api_key = %#v, want true", body["has_api_key"])
	}
	requireNumber(t, body["max_tokens"], 128000)
	requireNumber(t, body["max_context_tokens"], 1050000)
	requireNumber(t, body["sessions_swapped"], 3)
	if body["active_subscription"] != nil {
		t.Fatalf("active_subscription = %#v, want null", body["active_subscription"])
	}

	disk := llmBlockOnDisk(t, home)
	if disk["provider"] != "openai" || disk["model"] != "gpt-5.5" {
		t.Fatalf("disk provider/model = %#v/%#v", disk["provider"], disk["model"])
	}
	requireNumber(t, disk["max_tokens"], 128000)
	requireNumber(t, disk["max_context_tokens"], 1050000)
	if disk["api_key_env_var"] != "OPENAI_API_KEY" {
		t.Fatalf("disk api_key_env_var = %#v", disk["api_key_env_var"])
	}
	requireAbsent(t, disk, "api_base")
	requireAbsent(t, disk, "use_codex_subscription")

	if len(calls) != 1 || calls[0].fullModel != "openai/gpt-5.5" {
		t.Fatalf("hot-swap calls = %#v", calls)
	}
	if deref(calls[0].apiKey) != "sk-openai" {
		t.Fatalf("hot-swap key = %#v", deref(calls[0].apiKey))
	}
	if calls[0].apiBase != nil {
		t.Fatalf("hot-swap api base = %#v, want nil", deref(calls[0].apiBase))
	}
}

// Parity-exempt: pins the raw config api_base handed to validation and the
func TestTheLLMConfigRoutePassesAConfigAPINumberToValidationAsWritten(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("HIVE_API_KEY", "sk-hive")
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{"api_base": 123}})

	var validated []validateCall
	var calls []hotSwapCall
	deps := testLLMDeps()
	deps.Validate = func(provider, apiKey string, apiBase, model *string) map[string]any {
		validated = append(validated, validateCall{provider: provider, apiKey: apiKey, apiBase: apiBase, model: model})
		return map[string]any{"valid": true}
	}
	deps.HotSwap = recordingHotSwap(&calls, 0)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": "hive",
		"model":    "hive-2.1",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if len(validated) != 1 || deref(validated[0].apiBase) != "123" {
		t.Fatalf("validate calls = %#v, want api_base \"123\"", validated)
	}
	if len(calls) != 1 || deref(calls[0].apiBase) != "123" {
		t.Fatalf("hot-swap calls = %#v, want api_base \"123\"", calls)
	}
	disk := llmBlockOnDisk(t, home)
	requireNumber(t, disk["api_base"], 123)
}

// Parity-exempt: API-key mode requires both fields; test_api.py has no case.
func TestTheLLMConfigRouteRequiresProviderAndModel(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	cases := []map[string]any{
		{"model": "gpt-5.5"},
		{"provider": "openai"},
		{},
	}
	for _, body := range cases {
		status, response := configDo(t, http.MethodPut, server.URL+"/api/config/llm", body)
		if status != 400 {
			t.Fatalf("body %#v: status = %d, want 400", body, status)
		}
		if response["error"] != "Both 'provider' and 'model' are required" {
			t.Fatalf("body %#v: error = %#v", body, response["error"])
		}
	}
}

// Parity-exempt: API-key mode rejects a model the catalogue does not know.
func TestTheLLMConfigRouteRejectsAnUnknownCatalogueModel(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": "openai",
		"model":    "gpt-0",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "Model 'gpt-0' is not available for provider 'openai'." {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: API-key mode's missing-key 400, with the credential store and
func TestTheLLMConfigRouteReportsAMissingAPIKey(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": "openai",
		"model":    "gpt-5.5",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "No API key found for openai. Please add one in Manage Keys." {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: API-key mode's validation-failure 400 and message passthrough.
func TestTheLLMConfigRouteReportsAPIKeyValidationFailure(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	deps := testLLMDeps()
	deps.CredentialKey = func(string) (string, bool) { return "bad-key", true }
	deps.Validate = func(string, string, *string, *string) map[string]any {
		return map[string]any{"valid": false, "message": "401 Unauthorized"}
	}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": "openai",
		"model":    "gpt-5.5",
	})
	if status != 400 {
		t.Fatalf("status = %d, want 400 (body %#v)", status, body)
	}
	if body["error"] != "API key validation failed for openai: 401 Unauthorized" {
		t.Fatalf("error = %#v", body["error"])
	}
	if _, err := os.Stat(home.ConfigurationFile()); !os.IsNotExist(err) {
		t.Fatalf("configuration.json must not be written, stat err = %v", err)
	}
}

// Parity-exempt: a list provider is unhashable upstream (TypeError -> generic
func TestTheLLMConfigRouteRejectsAnUnhashableProvider(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"provider": []any{"openai"},
		"model":    "gpt-5.5",
	})
	if status != 500 {
		t.Fatalf("status = %d, want 500 (body %#v)", status, body)
	}
	if body["error"] != internalServerError {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: a stored llm section that is not an object raises at .get
func TestTheLLMConfigRouteRejectsANonObjectLLMSection(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": "not-an-object"})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 500 {
		t.Fatalf("status = %d, want 500 (body %#v)", status, body)
	}
	if body["error"] != internalServerError {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: GET reports the active subscription from its flag and filters
func TestTheLLMConfigRouteReportsSubscriptions(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider":               "openai",
		"model":                  "gpt-5.3-codex",
		"max_tokens":             4321,
		"max_context_tokens":     8765,
		"api_base":               "https://chat.example/v1",
		"api_key_env_var":        "HIVE_LLM_TEST_VAR",
		"use_codex_subscription": true,
	}})
	deps := testLLMDeps()
	deps.DetectSubscriptions = func() []string { return []string{"codex", "kimi_code"} }
	deps.SubscriptionToken = func(id string) (string, bool) {
		if id == "codex" {
			return "tok-codex", true
		}
		return "", false
	}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["active_subscription"] != "codex" {
		t.Fatalf("active_subscription = %#v", body["active_subscription"])
	}
	detected, ok := body["detected_subscriptions"].([]any)
	if !ok || len(detected) != 1 || detected[0] != "codex" {
		t.Fatalf("detected_subscriptions = %#v", body["detected_subscriptions"])
	}
	if body["provider"] != "openai" || body["model"] != "gpt-5.3-codex" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	requireNumber(t, body["max_tokens"], 4321)
	requireNumber(t, body["max_context_tokens"], 8765)
	if body["api_base"] != "https://chat.example/v1" {
		t.Fatalf("api_base = %#v", body["api_base"])
	}
	if body["api_key_env_var"] != "HIVE_LLM_TEST_VAR" {
		t.Fatalf("api_key_env_var = %#v", body["api_key_env_var"])
	}
	if body["has_api_key"] != true {
		t.Fatalf("has_api_key = %#v, want true from the subscription token", body["has_api_key"])
	}
}

// Parity-exempt: GET's connected_providers skips the google and together_ai
func TestTheLLMConfigRouteSkipsProviderAliasesInConnectedProviders(t *testing.T) {
	clearLLMEnv(t)
	t.Setenv("GOOGLE_API_KEY", "google-key")
	t.Setenv("TOGETHER_API_KEY", "together-key")
	home := hivehome.Home{Root: t.TempDir()}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	connected, ok := body["connected_providers"].([]any)
	if !ok || len(connected) != 1 || connected[0] != "together" {
		t.Fatalf("connected_providers = %#v, want [\"together\"]", body["connected_providers"])
	}
}

// Parity-exempt: activating a subscription whose preset has no api_base must
func TestTheLLMConfigRouteClearsAStaleAPIBaseForASubscriptionWithoutOne(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeFeatureConfig(t, home, map[string]any{"llm": map[string]any{
		"provider":        "openai",
		"model":           "old-model",
		"api_base":        "http://stale.example/v1",
		"api_key_env_var": "OLD_VAR",
	}})

	var validations []validateCall
	deps := testLLMDeps()
	deps.SubscriptionToken = func(id string) (string, bool) {
		if id == "claude_code" {
			return "tok-claude", true
		}
		return "", false
	}
	deps.Validate = func(provider, apiKey string, apiBase, model *string) map[string]any {
		validations = append(validations, validateCall{provider: provider, apiKey: apiKey, apiBase: apiBase, model: model})
		return map[string]any{"valid": true}
	}
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
		"subscription": "claude_code",
	})
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "anthropic" || body["model"] != "claude-opus-4-6" {
		t.Fatalf("provider/model = %#v/%#v", body["provider"], body["model"])
	}
	if body["active_subscription"] != "claude_code" {
		t.Fatalf("active_subscription = %#v", body["active_subscription"])
	}

	disk := llmBlockOnDisk(t, home)
	requireAbsent(t, disk, "api_base")
	requireAbsent(t, disk, "api_key_env_var")
	if disk["use_claude_code_subscription"] != true {
		t.Fatalf("disk use_claude_code_subscription = %#v", disk["use_claude_code_subscription"])
	}
	if len(validations) != 1 || validations[0].apiBase != nil {
		t.Fatalf("validation calls = %#v, want one with a nil api_base", validations)
	}
}

// Parity-exempt: upstream rejects a key only when check["valid"] is exactly
func TestTheLLMConfigRouteHonoursOnlyABooleanFalseValidationVerdict(t *testing.T) {
	verdicts := []map[string]any{
		{"valid": 0},
		{"valid": "false"},
		{"valid": nil},
		{},
	}

	t.Run("subscription", func(t *testing.T) {
		for _, verdict := range verdicts {
			clearLLMEnv(t)
			home := hivehome.Home{Root: t.TempDir()}
			deps := testLLMDeps()
			deps.SubscriptionToken = func(id string) (string, bool) { return "tok", id == "claude_code" }
			deps.Validate = func(string, string, *string, *string) map[string]any { return verdict }
			server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

			status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
				"subscription": "claude_code",
			})
			if status != 200 {
				t.Fatalf("verdict %#v: status = %d, want 200 (body %#v)", verdict, status, body)
			}
		}
	})

	t.Run("api key", func(t *testing.T) {
		for _, verdict := range verdicts {
			clearLLMEnv(t)
			home := hivehome.Home{Root: t.TempDir()}
			deps := testLLMDeps()
			deps.CredentialKey = func(string) (string, bool) { return "sk-test", true }
			deps.Validate = func(string, string, *string, *string) map[string]any { return verdict }
			server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: deps})

			status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", map[string]any{
				"provider": "openai",
				"model":    "gpt-5.5",
			})
			if status != 200 {
				t.Fatalf("verdict %#v: status = %d, want 200 (body %#v)", verdict, status, body)
			}
		}
	})
}

// Parity-exempt: MutateHiveConfig must leave the file untouched when the
func TestTheLLMConfigRouteDoesNotWriteWhenTheCustomMutationAborts(t *testing.T) {
	cases := []struct {
		name   string
		config map[string]any
		body   map[string]any
	}{
		{
			name:   "after editing the section",
			config: map[string]any{"llm": map[string]any{"provider": "openai", "model": "old"}},
			body:   map[string]any{"custom": true, "model": "m2", "max_tokens": "not-a-number"},
		},
		{
			name:   "before editing the section",
			config: map[string]any{"llm": "not-an-object"},
			body:   map[string]any{"custom": true, "model": "m2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearLLMEnv(t)
			home := hivehome.Home{Root: t.TempDir()}
			writeFeatureConfig(t, home, tc.config)
			before, err := os.ReadFile(home.ConfigurationFile())
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})

			status, body := configDo(t, http.MethodPut, server.URL+"/api/config/llm", tc.body)
			if status != 500 {
				t.Fatalf("status = %d, want 500 (body %#v)", status, body)
			}
			if body["error"] != internalServerError {
				t.Fatalf("error = %#v", body["error"])
			}
			after, err := os.ReadFile(home.ConfigurationFile())
			if err != nil {
				t.Fatalf("read config: %v", err)
			}
			if string(after) != string(before) {
				t.Fatalf("configuration.json changed on an aborted mutation:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

// Parity-exempt: Python's json.load rejects trailing data after the top-level
func TestTheLLMConfigRouteTreatsAConfigWithTrailingGarbageAsEmpty(t *testing.T) {
	clearLLMEnv(t)
	home := hivehome.Home{Root: t.TempDir()}
	writeRawConfig(t, home, `{"llm":{"provider":"openai","model":"gpt-5.5"}} trailing`)

	server := newConfigRoutes(t, ConfigRouteDeps{Home: home, LLM: testLLMDeps()})
	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if body["provider"] != "" || body["model"] != "" {
		t.Fatalf("provider/model = %#v/%#v, want empty", body["provider"], body["model"])
	}
	if body["has_api_key"] != false {
		t.Fatalf("has_api_key = %#v, want false", body["has_api_key"])
	}
}
