package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// fakeKeySources is a scripted credential store plus endpoint table: the
// resolver's credential-backed steps read it instead of the encrypted store.
type fakeKeySources struct {
	endpoints     map[string]string
	stored        map[string]string
	endpointCalls []string
	providerCalls []string
}

func (f *fakeKeySources) sources() *LLMKeySources {
	return &LLMKeySources{
		EndpointKey: func(name string) string {
			f.endpointCalls = append(f.endpointCalls, name)
			if key, ok := f.endpoints[name]; ok && key != "" {
				return key
			}
			if name == "magpie" {
				return "magpie"
			}
			return "no-key"
		},
		StoredProviderKey: func(provider string) (string, bool) {
			f.providerCalls = append(f.providerCalls, provider)
			key, ok := f.stored[provider]
			return key, ok && key != ""
		},
	}
}

// resolveLLMKey initializes a stage over dir's configuration.json with the
// supplied key sources and returns the built HTTP provider.
func resolveLLMKey(t *testing.T, dir string, section map[string]any, sources *LLMKeySources) *llm.Provider {
	t.Helper()
	writeTestConfig(t, dir, map[string]any{"llm": section})
	stage := &LLMProviderStage{
		BaseStage:  BaseStage{StageName: "llm_provider", StageOrder: 10},
		KeySources: sources,
	}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	provider, ok := stage.LLM.(*llm.Provider)
	if !ok {
		t.Fatalf("completer = %T, want *llm.Provider", stage.LLM)
	}
	return provider
}

// Parity: framework/config.py::get_api_key — with nothing more explicit
// configured, a provider's credential-store key is the chain's result.
func TestLLMProviderUsesCredentialStoreKey(t *testing.T) {
	_, dir := testHome(t)
	fake := &fakeKeySources{stored: map[string]string{"anthropic": "sk-stored"}}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider": "anthropic",
		"model":    "claude-sonnet-5-5",
	}, fake.sources())

	if provider.APIKey != "sk-stored" {
		t.Fatalf("api key = %q, want the credential-store key", provider.APIKey)
	}
	if len(fake.providerCalls) != 1 || fake.providerCalls[0] != "anthropic" {
		t.Fatalf("credential-store lookups = %v, want [anthropic]", fake.providerCalls)
	}
	if len(fake.endpointCalls) != 0 {
		t.Fatalf("endpoint lookups = %v, want none without an llm.endpoint", fake.endpointCalls)
	}
}

// Parity: framework/server/tests/test_models_page.py::test_every_slot_kind_takes_an_endpoints_key_not_an_ambient_one
// — a slot served from a named endpoint takes that endpoint's key ahead of the
// pool and the literal api_key.
func TestLLMProviderUsesEndpointKey(t *testing.T) {
	_, dir := testHome(t)
	fake := &fakeKeySources{endpoints: map[string]string{"ollama": "ollama-key"}}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider": "openai",
		"model":    "kimi/k3",
		"endpoint": "ollama",
		"api_key":  "sk-literal",
		"api_keys": []any{"sk-pool-1", "sk-pool-2"},
	}, fake.sources())

	if provider.APIKey != "ollama-key" {
		t.Fatalf("api key = %q, want the endpoint key", provider.APIKey)
	}
	if len(fake.endpointCalls) != 1 || fake.endpointCalls[0] != "ollama" {
		t.Fatalf("endpoint lookups = %v, want [ollama]", fake.endpointCalls)
	}
	if len(fake.providerCalls) != 0 {
		t.Fatalf("credential-store lookups = %v, want none once the endpoint answers", fake.providerCalls)
	}
}

// Parity: framework/llm/endpoints.py::key_for — an endpoint with no stored key
// is sent its placeholder, never an ambient key from the surrounding section.
func TestLLMProviderEndpointWithoutStoredKeyUsesPlaceholder(t *testing.T) {
	cases := []struct {
		endpoint string
		want     string
	}{
		{endpoint: "magpie", want: "magpie"},
		{endpoint: "local-gateway", want: "no-key"},
	}
	for _, testCase := range cases {
		t.Run(testCase.endpoint, func(t *testing.T) {
			_, dir := testHome(t)
			fake := &fakeKeySources{}
			provider := resolveLLMKey(t, dir, map[string]any{
				"provider": "openai",
				"model":    "kimi/k3",
				"endpoint": testCase.endpoint,
				"api_key":  "sk-ambient",
			}, fake.sources())

			if provider.APIKey != testCase.want {
				t.Fatalf("api key = %q, want the placeholder %q", provider.APIKey, testCase.want)
			}
		})
	}
}

// Parity: framework/config.py::get_api_key — the credential store is checked
// before api_key_env_var, so a desktop-pushed key wins over the frozen env.
func TestLLMProviderCredentialStoreBeatsEnvVar(t *testing.T) {
	_, dir := testHome(t)
	t.Setenv("W163_PIPELINE_LLM_KEY", "env-key")
	fake := &fakeKeySources{stored: map[string]string{"openai": "sk-stored"}}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider":        "openai",
		"model":           "gpt-4o-mini",
		"api_key_env_var": "W163_PIPELINE_LLM_KEY",
	}, fake.sources())

	if provider.APIKey != "sk-stored" {
		t.Fatalf("api key = %q, want the credential-store key", provider.APIKey)
	}
}

// Parity: framework/config.py::get_api_key — without a stored credential the
// chain falls through to the named environment variable.
func TestLLMProviderMissingStoredKeyFallsThroughToEnvVar(t *testing.T) {
	_, dir := testHome(t)
	t.Setenv("W163_PIPELINE_LLM_KEY", "env-key")
	fake := &fakeKeySources{}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider":        "openai",
		"model":           "gpt-4o-mini",
		"api_key_env_var": "W163_PIPELINE_LLM_KEY",
	}, fake.sources())

	if provider.APIKey != "env-key" {
		t.Fatalf("api key = %q, want the env var", provider.APIKey)
	}
	if len(fake.providerCalls) != 1 || fake.providerCalls[0] != "openai" {
		t.Fatalf("credential-store lookups = %v, want [openai]", fake.providerCalls)
	}
}

// Parity: framework/config.py::get_api_key — a pool key and the literal api_key
// both outrank the credential store.
func TestLLMProviderConfiguredKeysBeatCredentialStore(t *testing.T) {
	cases := []struct {
		name    string
		section map[string]any
		want    string
	}{
		{
			name:    "pool",
			section: map[string]any{"provider": "openai", "model": "gpt-4o-mini", "api_keys": []any{"sk-pool-1", "sk-pool-2"}},
			want:    "sk-pool-1",
		},
		{
			name:    "literal",
			section: map[string]any{"provider": "openai", "model": "gpt-4o-mini", "api_key": "sk-literal"},
			want:    "sk-literal",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, dir := testHome(t)
			fake := &fakeKeySources{stored: map[string]string{"openai": "sk-stored"}}
			provider := resolveLLMKey(t, dir, testCase.section, fake.sources())
			if provider.APIKey != testCase.want {
				t.Fatalf("api key = %q, want %q", provider.APIKey, testCase.want)
			}
			if len(fake.providerCalls) != 0 {
				t.Fatalf("credential-store lookups = %v, want none", fake.providerCalls)
			}
		})
	}
}

// Parity: framework/config.py::get_api_key — an enabled subscription's token is
// resolved before the credential store.
func TestLLMProviderSubscriptionBeatsCredentialStore(t *testing.T) {
	_, dir := testHome(t)
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	writeUserFile(t, filepath.Join(userHome, ".claude", ".credentials.json"),
		`{"claudeAiOauth": {"accessToken": "subscription-token"}}`)
	fake := &fakeKeySources{stored: map[string]string{"anthropic": "sk-stored"}}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider":                     "anthropic",
		"model":                        "claude-sonnet-5-5",
		"use_claude_code_subscription": true,
	}, fake.sources())

	if provider.APIKey != "subscription-token" {
		t.Fatalf("api key = %q, want the subscription token", provider.APIKey)
	}
	if len(fake.providerCalls) != 0 {
		t.Fatalf("credential-store lookups = %v, want none", fake.providerCalls)
	}
}

// Parity: framework/config.py::get_api_key — no configured source at all leaves
// the key empty, which the provider carries as-is (litellm's own fallback).
func TestLLMProviderWithoutAnyKeyLeavesItEmpty(t *testing.T) {
	_, dir := testHome(t)
	t.Setenv("W163_PIPELINE_LLM_KEY", "")
	fake := &fakeKeySources{}
	provider := resolveLLMKey(t, dir, map[string]any{
		"provider":        "openai",
		"model":           "gpt-4o-mini",
		"api_key_env_var": "W163_PIPELINE_LLM_KEY",
	}, fake.sources())

	if provider.APIKey != "" {
		t.Fatalf("api key = %q, want empty", provider.APIKey)
	}
}

// Parity: framework/config.py::get_api_key — the default sources (no injected
// hooks) read the encrypted credential store and the named endpoint's stored
// key.
func TestLLMProviderDefaultSourcesReadTheCredentialStore(t *testing.T) {
	_, dir := testHome(t)
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate credential key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	store, err := credentials.WithEncryptedStorage("")
	if err != nil {
		t.Fatalf("open credential store: %v", err)
	}
	stored := credentials.NewCredentialObject("openai")
	stored.Keys.Set(credentials.NewCredentialKey("api_key", "sk-file-stored"))
	if err := store.SaveCredential(stored); err != nil {
		t.Fatalf("save credential: %v", err)
	}

	provider := resolveLLMKey(t, dir, map[string]any{
		"provider": "openai",
		"model":    "gpt-4o-mini",
	}, nil)
	if provider.APIKey != "sk-file-stored" {
		t.Fatalf("api key = %q, want the stored credential", provider.APIKey)
	}

	endpoint := resolveLLMKey(t, dir, map[string]any{
		"provider": "openai",
		"model":    "kimi/k3",
		"endpoint": "magpie",
	}, nil)
	if endpoint.APIKey != "magpie" {
		t.Fatalf("api key = %q, want magpie's placeholder", endpoint.APIKey)
	}
}
