package server

// The main llm slot as the runtime consumes it.
//
// The REST surface (llm_config.go) already mirrored Python's get_api_key for
// the status endpoints; the session builder used to resolve only the literal
// env var. This file is the single runtime reader so both paths cannot drift:
// key, base, pool and extra kwargs come from configuration.json's `llm`
// section in Python's order.
//
// Parity: framework/config.py::get_api_key, get_api_base,
// get_llm_extra_kwargs, get_api_keys, get_preferred_model; and the
// subscription token sources in framework/loader/agent_loader.py
// (get_claude_code_token, get_codex_token, get_kimi_code_token,
// get_antigravity_token).

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// DefaultPreferredModel is Python's get_preferred_model fallback when the llm
// section names no usable provider/model pair.
const DefaultPreferredModel = "anthropic/claude-sonnet-5-5"

// MainSlotSection is configuration.json's `llm` object, or nil.
func MainSlotSection(home hivehome.Home) map[string]any {
	return configSection(home, "llm")
}

// AntigravityProviderFor builds the Antigravity (Google Cloud Code Assist)
// transport one model slot runs on: the OAuth account file lives under the hive
// home and the client credentials come from the same env-then-file readers the
// REST surface uses.
//
// Upstream swaps the whole session to this provider when
// llm.use_antigravity_subscription is set (session_manager.build_llm); the
// provider refreshes its access token itself.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider.__init__
func AntigravityProviderFor(home hivehome.Home, model string) *llm.AntigravityProvider {
	return llm.NewAntigravityProvider(llm.AntigravityOptions{
		Model:        model,
		AccountsPath: filepath.Join(home.Root, "antigravity-accounts.json"),
		ClientID:     func() string { return AntigravityClientID(home) },
		ClientSecret: func() string { return AntigravityClientSecret(home) },
	})
}

// MainSlotAPIKeys ports get_api_keys: the configured key pool with empty
// entries dropped, or nil when none is configured.
//
// Parity: framework/config.py::get_api_keys
func MainSlotAPIKeys(home hivehome.Home) []string {
	raw, ok := MainSlotSection(home)["api_keys"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	keys := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			keys = append(keys, text)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// MainSlotProviderName is the section's provider, inferred from the model id
// when the section names none (the CLI resolved sessions the same way).
func MainSlotProviderName(section map[string]any) string {
	provider := strings.ToLower(strings.TrimSpace(pyStrOrEmpty(section["provider"])))
	if provider == "" || provider == "custom" {
		provider = providerFromModelName(strings.TrimSpace(pyStrOrEmpty(section["model"])))
	}
	return provider
}

// MainSlotKey resolves the main slot's key in Python's get_api_key order:
// the named endpoint's own key, the first pool key, the literal api_key, the
// subscription tokens (Claude Code, Codex, Kimi Code, Antigravity), the
// credential store, the configured api_key_env_var, then the provider's
// conventional environment variable (what litellm reads when api_key is None).
//
// Parity: framework/config.py::get_api_key
func MainSlotKey(home hivehome.Home) string {
	section := MainSlotSection(home)

	if name, ok := section["endpoint"].(string); ok && strings.TrimSpace(name) != "" {
		// Endpoint keys are never ambient: an endpoint without a stored key
		// resolves to its placeholder, exactly as _endpoint_key does.
		if key := llm.KeyFor(strings.TrimSpace(name)); key != "" {
			return key
		}
	}

	if pool := MainSlotAPIKeys(home); len(pool) > 0 {
		return pool[0]
	}

	if literal, ok := section["api_key"].(string); ok && literal != "" {
		return literal
	}

	if pyTruthy(section["use_claude_code_subscription"]) {
		if token := claudeCodeToken(); token != "" {
			return token
		}
	}
	if pyTruthy(section["use_codex_subscription"]) {
		if token := codexToken(); token != "" {
			return token
		}
	}
	if pyTruthy(section["use_kimi_code_subscription"]) {
		if token := kimiCodeToken(); token != "" {
			return token
		}
	}
	if pyTruthy(section["use_antigravity_subscription"]) {
		if token := antigravityToken(home); token != "" {
			return token
		}
	}

	provider := MainSlotProviderName(section)
	if stored, ok := storedProviderKey(provider); ok {
		return stored
	}

	if envName, ok := section["api_key_env_var"].(string); ok && strings.TrimSpace(envName) != "" {
		if value := os.Getenv(strings.TrimSpace(envName)); value != "" {
			return value
		}
	}
	if envName, ok := slotProviderEnvVars[provider]; ok {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	// The port's historical last resort (the CLI accepted it before the
	// session builder moved here); upstream returns None and lets litellm
	// read the provider's own env var, which the branch above already did.
	return strings.TrimSpace(os.Getenv("HIVE_API_KEY"))
}

// MainSlotAPIKey is MainSlotKey in the lazy shape the section resolvers take:
// the value is re-resolved on every call, so a configuration.json edit reaches
// already-built slot fallbacks without a restart.
func MainSlotAPIKey(home hivehome.Home) func() string {
	return func() string { return MainSlotKey(home) }
}

// MainSlotAPIBase ports get_api_base: the subscription endpoints come first
// (Codex's ChatGPT backend, Kimi Code's Anthropic-compatible base), Antigravity
// needs none, then the configured api_base, then OpenRouter's built-in base.
//
// Parity: framework/config.py::get_api_base
func MainSlotAPIBase(home hivehome.Home) string {
	section := MainSlotSection(home)
	if pyTruthy(section["use_codex_subscription"]) {
		return "https://chatgpt.com/backend-api/codex"
	}
	if pyTruthy(section["use_kimi_code_subscription"]) {
		return llm.KimiAPIBase
	}
	if pyTruthy(section["use_antigravity_subscription"]) {
		return ""
	}
	if base, ok := section["api_base"].(string); ok && strings.TrimSpace(base) != "" {
		return base
	}
	if provider, _ := section["provider"].(string); strings.EqualFold(strings.TrimSpace(provider), "openrouter") {
		return llm.OpenRouterAPIBase
	}
	return ""
}

// MainSlotExtraKwargs ports get_llm_extra_kwargs: subscription auth headers,
// ollama's num_ctx, or the section's extra_body — one shape per provider, in
// that order.
//
// Parity: framework/config.py::get_llm_extra_kwargs,
// framework/server/routes_config.py (same body shape it writes)
func MainSlotExtraKwargs(home hivehome.Home, apiKey string) map[string]any {
	extra := llm.ExtraLLMKWargs(MainSlotSection(home), apiKey, codexAccountID())
	if len(extra) == 0 {
		return nil
	}
	return extra
}

// MainSlotModel is the runtime model for the slot in Python's
// get_preferred_model shape: an explicit request wins as given, then the
// configured provider/model joined (an openrouter/<id> model id is unwrapped
// first), then Python's preferred-model fallback. Joining matters: the
// transport routes kimi/ and hive/ through the Anthropic Messages protocol, so
// a configured kimi model must keep its prefix until the wire name is built.
//
// Parity: framework/config.py::get_preferred_model
func MainSlotModel(home hivehome.Home, requested string) string {
	if model := strings.TrimSpace(requested); model != "" {
		return model
	}
	section := MainSlotSection(home)
	provider := strings.TrimSpace(pyStrOrEmpty(section["provider"]))
	model := strings.TrimSpace(pyStrOrEmpty(section["model"]))
	if provider != "" && !strings.EqualFold(provider, "custom") && model != "" {
		if strings.EqualFold(provider, "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
			model = model[len("openrouter/"):]
		}
		return provider + "/" + model
	}
	if model != "" {
		// A custom OpenAI-compatible endpoint (or a section that only names a
		// local model) keeps the model id as written: the Go transport has no
		// "custom/" provider prefix to route.
		return model
	}
	return DefaultPreferredModel
}

// MainSlotProvider builds the completion transport for a live session from the
// main llm slot: model, key, base, key pool and extra request fields all come
// from configuration.json, re-read at build time. A local model needs no key;
// every remote model must resolve one, with the same hint the CLI has always
// printed when none is configured. The Antigravity subscription swaps the whole
// transport for its Gemini-style provider, exactly as upstream's
// SessionManager.build_llm does.
func MainSlotProvider(home hivehome.Home, requestedModel string) (llm.Completer, error) {
	section := MainSlotSection(home)
	if pyTruthy(section["use_antigravity_subscription"]) {
		return AntigravityProviderFor(home, MainSlotModel(home, requestedModel)), nil
	}

	runtime := LoadRuntimeConfig(home, requestedModel)

	if llm.IsLocalModel(runtime.Model) {
		local := llm.NewHTTPProvider(llm.ProviderOptions{Model: runtime.Model, APIBase: runtime.APIBase})
		if runtime.ExtraKwargs != nil {
			local.ExtraKwargs = runtime.ExtraKwargs
		}
		return local, nil
	}

	provider := MainSlotProviderName(section)
	apiKey := runtime.APIKey
	if apiKey == "" {
		envVar := strings.TrimSpace(pyStrOrEmpty(section["api_key_env_var"]))
		if envVar == "" {
			envVar = slotProviderEnvVars[provider]
		}
		if envVar == "" {
			envVar = "OPENAI_API_KEY"
		}
		return nil, errors.New("no API key configured for model " + quoteErr(runtime.Model) +
			" (set " + envVar + " or configure the LLM in hive settings)")
	}

	built := llm.NewHTTPProvider(llm.ProviderOptions{Model: runtime.Model, APIKey: apiKey, APIBase: runtime.APIBase})
	if pool := MainSlotKeyPool(home); pool != nil {
		built.KeyPool = pool
	}
	if runtime.ExtraKwargs != nil {
		built.ExtraKwargs = runtime.ExtraKwargs
	}
	return built, nil
}

// MainSlotKeyPool exposes the configured rotation pool (nil unless 2+ keys).
func MainSlotKeyPool(home hivehome.Home) *llm.KeyPool {
	keys := MainSlotAPIKeys(home)
	if len(keys) < 2 {
		return nil
	}
	pool, err := llm.NewKeyPool(keys)
	if err != nil {
		return nil
	}
	return pool
}

// codexAccountID ports get_codex_account_id: tokens.account_id from
// ~/.codex/auth.json, else the chatgpt_account_id claim in the access token's
// JWT payload. Absent reads as "" (the header is simply not sent).
//
// Parity: framework/loader/agent_loader.py::get_codex_account_id
func codexAccountID() string {
	path := filepath.Join(homeDir(), ".codex", "auth.json")
	if homeDir() == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Tokens map[string]any `json:"tokens"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return ""
	}
	if account, ok := doc.Tokens["account_id"].(string); ok && account != "" {
		return account
	}
	access, _ := doc.Tokens["access_token"].(string)
	return accountIDFromJWT(access)
}

func accountIDFromJWT(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload := parts[1]
	if padding := 4 - len(payload)%4; padding != 4 {
		payload += strings.Repeat("=", padding)
	}
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var claims map[string]any
	if json.Unmarshal(decoded, &claims) != nil {
		return ""
	}
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	account, _ := auth["chatgpt_account_id"].(string)
	return account
}

// quoteErr renders a model id the way the old CLI hint did (%q).
func quoteErr(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "\"" + value + "\""
	}
	return string(encoded)
}
