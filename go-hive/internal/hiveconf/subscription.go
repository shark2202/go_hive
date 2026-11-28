package hiveconf

// Subscription-token readers shared by the pipeline's LLM stage and the server
// model-slot builder.
//
// Upstream's get_api_key resolves an enabled subscription's OAuth/plan token
// before the credential store and the environment fallback
// (framework/config.py::get_api_key), reading:
//   - Claude Code  ~/.claude/.credentials.json → claudeAiOauth.accessToken
//   - Codex        ~/.codex/auth.json          → tokens.access_token
//   - Kimi Code    ~/.kimi/config.toml         → api_key = "..."
//   - Antigravity  <HIVE_HOME>/antigravity-accounts.json → accounts[0].accessToken
//
// Claude Code and Codex tokens expire; both readers refresh an expired token
// through the provider's OAuth endpoint before returning it (falling back to
// the stale token on failure), so every consumer — the pipeline's LLM stage
// and the server model-slot builder — gets the refreshed value.
//
// Parity: framework/loader/agent_loader.py::get_claude_code_token,
// get_codex_token, get_kimi_code_token, get_antigravity_token

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// ClaudeCodeToken is the Claude Code OAuth access token, or "". An expired or
// nearly-expired token is refreshed through the upstream OAuth endpoint first;
// a failed refresh falls back to the stale token, as upstream does.
func ClaudeCodeToken() string {
	return ClaudeCodeTokenContext(context.Background())
}

// ClaudeCodeTokenContext is ClaudeCodeToken with a caller cancellation scope.
// Cancellation (or a refresh timeout) falls back to the token already on disk.
func ClaudeCodeTokenContext(ctx context.Context) string {
	home := homeDir()
	if home == "" {
		return ""
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	creds, err := readJSONObject(path)
	if err != nil {
		return ""
	}
	oauth, ok := creds["claudeAiOauth"].(map[string]any)
	if !ok {
		return ""
	}
	accessToken, _ := oauth["accessToken"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return ""
	}
	if !claudeTokenExpired(oauth, subscriptionNow()) {
		return accessToken
	}
	refreshToken, _ := oauth["refreshToken"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		// Upstream returns the stale token when no refresh token exists; the
		// proxy may still accept it briefly.
		return accessToken
	}
	refreshed, err := refreshSubscriptionToken(ctx, "claude_code\x00"+path, func(ctx context.Context) (string, error) {
		return refreshClaudeCredentials(ctx, path, refreshToken)
	})
	if err != nil || refreshed == "" {
		return accessToken
	}
	return refreshed
}

// CodexToken is the Codex/ChatGPT OAuth access token from auth.json, or "".
// An expired or nearly-expired token is refreshed through the Codex OAuth
// endpoint first; a failed refresh falls back to the stale token.
// The macOS Keychain source upstream also reads is not available to a
// dependency-free Go lookup; the token file is the portable fallback.
func CodexToken() string {
	return CodexTokenContext(context.Background())
}

// CodexTokenContext is CodexToken with a caller cancellation scope.
// Cancellation (or a refresh timeout) falls back to the token already on disk.
func CodexTokenContext(ctx context.Context) string {
	home := homeDir()
	if home == "" {
		return ""
	}
	path := filepath.Join(home, ".codex", "auth.json")
	auth, err := readJSONObject(path)
	if err != nil {
		return ""
	}
	tokens, ok := auth["tokens"].(map[string]any)
	if !ok {
		return ""
	}
	accessToken, _ := tokens["access_token"].(string)
	if strings.TrimSpace(accessToken) == "" {
		return ""
	}
	if !codexTokenExpired(path, auth, subscriptionNow()) {
		return accessToken
	}
	refreshToken, _ := tokens["refresh_token"].(string)
	if strings.TrimSpace(refreshToken) == "" {
		return accessToken
	}
	refreshed, err := refreshSubscriptionToken(ctx, "codex\x00"+path, func(ctx context.Context) (string, error) {
		return refreshCodexCredentials(ctx, path, refreshToken)
	})
	if err != nil || refreshed == "" {
		return accessToken
	}
	return refreshed
}

// KimiCodeToken is the api_key from ~/.kimi/config.toml, or "".
func KimiCodeToken() string {
	data, err := os.ReadFile(filepath.Join(homeDir(), ".kimi", "config.toml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "api_key") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "api_key"))
		if !strings.HasPrefix(rest, "=") {
			continue
		}
		value := strings.TrimSpace(rest[1:])
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			return value[1 : len(value)-1]
		}
	}
	return ""
}

// AntigravityToken is the first Antigravity account's access token, or "".
func AntigravityToken(home hivehome.Home) string {
	data, err := os.ReadFile(filepath.Join(home.Root, "antigravity-accounts.json"))
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	accounts, ok := doc["accounts"].([]any)
	if !ok || len(accounts) == 0 {
		return ""
	}
	first, ok := accounts[0].(map[string]any)
	if !ok {
		return ""
	}
	token, _ := first["accessToken"].(string)
	return token
}

// SubscriptionToken resolves the token of the first enabled subscription flag,
// in upstream get_api_key's order. A disabled feature or a missing token
// yields "". An expired Claude Code / Codex token is refreshed before it is
// handed to the LLM transport.
func SubscriptionToken(section map[string]any, home hivehome.Home) string {
	return SubscriptionTokenContext(context.Background(), section, home)
}

// SubscriptionTokenContext is SubscriptionToken with a caller cancellation
// scope: cancelling it abandons an in-flight subscription-token refresh and
// falls back to the token already on disk.
func SubscriptionTokenContext(ctx context.Context, section map[string]any, home hivehome.Home) string {
	if truthy(section["use_claude_code_subscription"]) {
		if token := ClaudeCodeTokenContext(ctx); token != "" {
			return token
		}
	}
	if truthy(section["use_codex_subscription"]) {
		if token := CodexTokenContext(ctx); token != "" {
			return token
		}
	}
	if truthy(section["use_kimi_code_subscription"]) {
		if token := KimiCodeToken(); token != "" {
			return token
		}
	}
	if truthy(section["use_antigravity_subscription"]) {
		if token := AntigravityToken(home); token != "" {
			return token
		}
	}
	return ""
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// truthy is Python truthiness for the JSON scalars a flag can hold.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		f, err := typed.Float64()
		return err != nil || f != 0
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return false
}
