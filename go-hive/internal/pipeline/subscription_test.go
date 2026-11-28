package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

func writeUserFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Parity: framework/config.py::get_api_key — an enabled subscription's token
// is resolved after the literal api_key and before the env-var fallback.
func TestLLMProviderResolvesSubscriptionTokens(t *testing.T) {
	cases := []struct {
		name  string
		flag  string
		path  string
		body  string
		token string
	}{
		{
			name:  "claude_code",
			flag:  "use_claude_code_subscription",
			path:  filepath.Join(".claude", ".credentials.json"),
			body:  `{"claudeAiOauth": {"accessToken": "sk-ant-oat-claude"}}`,
			token: "sk-ant-oat-claude",
		},
		{
			name:  "codex",
			flag:  "use_codex_subscription",
			path:  filepath.Join(".codex", "auth.json"),
			body:  `{"tokens": {"access_token": "codex-token"}}`,
			token: "codex-token",
		},
		{
			name:  "kimi_code",
			flag:  "use_kimi_code_subscription",
			path:  filepath.Join(".kimi", "config.toml"),
			body:  "api_key = \"kimi-token\"\n",
			token: "kimi-token",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, dir := testHome(t)
			userHome := t.TempDir()
			t.Setenv("HOME", userHome)
			writeUserFile(t, filepath.Join(userHome, testCase.path), testCase.body)
			writeTestConfig(t, dir, map[string]any{
				"llm": map[string]any{
					"provider":    "anthropic",
					"model":       "claude-sonnet-5-5",
					testCase.flag: true,
				},
			})
			stage := &LLMProviderStage{BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10}}
			if err := stage.Initialize(); err != nil {
				t.Fatalf("initialize: %v", err)
			}
			provider, ok := stage.LLM.(*llm.Provider)
			if !ok {
				t.Fatalf("completer = %T, want *llm.Provider", stage.LLM)
			}
			if provider.APIKey != testCase.token {
				t.Fatalf("api key = %q, want %q", provider.APIKey, testCase.token)
			}
		})
	}
}

// Parity-exempt: Go-only subscription-token fallback wiring; the server owns the same resolution.
func TestLLMProviderLiteralKeyBeatsSubscriptionToken(t *testing.T) {
	_, dir := testHome(t)
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	writeUserFile(t, filepath.Join(userHome, ".claude", ".credentials.json"),
		`{"claudeAiOauth": {"accessToken": "subscription-token"}}`)
	writeTestConfig(t, dir, map[string]any{
		"llm": map[string]any{
			"provider":                     "anthropic",
			"model":                        "claude-sonnet-5-5",
			"api_key":                      "literal-key",
			"use_claude_code_subscription": true,
		},
	})
	stage := &LLMProviderStage{BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10}}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if provider := stage.LLM.(*llm.Provider); provider.APIKey != "literal-key" {
		t.Fatalf("api key = %q, want the literal key", provider.APIKey)
	}
}

// Parity-exempt: Go-only subscription-token flag gating; the server owns the same resolution.
func TestLLMProviderSubscriptionTokenRequiresTheFlag(t *testing.T) {
	_, dir := testHome(t)
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	writeUserFile(t, filepath.Join(userHome, ".claude", ".credentials.json"),
		`{"claudeAiOauth": {"accessToken": "subscription-token"}}`)
	writeTestConfig(t, dir, map[string]any{
		"llm": map[string]any{"provider": "anthropic", "model": "claude-sonnet-5-5"},
	})
	stage := &LLMProviderStage{BaseStage: BaseStage{StageName: "llm_provider", StageOrder: 10}}
	if err := stage.Initialize(); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if provider := stage.LLM.(*llm.Provider); provider.APIKey != "" {
		t.Fatalf("api key = %q, want empty without the subscription flag", provider.APIKey)
	}
}
