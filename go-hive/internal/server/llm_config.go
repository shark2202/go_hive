package server

// Parity: framework/server/routes_config.py::handle_get_llm_config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/ratelimit"
)

var errProviderNotString = errors.New("provider is not a string")

var errLLMNotObject = errors.New("llm section is not an object")

var errUnhashable = errors.New("unhashable key")

var providerEnvVars = []struct{ Provider, EnvVar string }{
	{"anthropic", "ANTHROPIC_API_KEY"},
	{"openai", "OPENAI_API_KEY"},
	{"gemini", "GEMINI_API_KEY"},
	{"google", "GOOGLE_API_KEY"},
	{"minimax", "MINIMAX_API_KEY"},
	{"groq", "GROQ_API_KEY"},
	{"cerebras", "CEREBRAS_API_KEY"},
	{"openrouter", "OPENROUTER_API_KEY"},
	{"mistral", "MISTRAL_API_KEY"},
	{"together", "TOGETHER_API_KEY"},
	{"together_ai", "TOGETHER_API_KEY"},
	{"deepseek", "DEEPSEEK_API_KEY"},
	{"kimi", "KIMI_API_KEY"},
	{"hive", "HIVE_API_KEY"},
}

func providerEnvVarFor(provider string) (string, bool) {
	lower := strings.ToLower(provider)
	for _, entry := range providerEnvVars {
		if entry.Provider == lower {
			return entry.EnvVar, true
		}
	}
	return "", false
}

var providerCredMap = map[string]string{
	"anthropic":   "anthropic",
	"openai":      "openai",
	"gemini":      "gemini",
	"google":      "gemini",
	"minimax":     "minimax",
	"groq":        "groq",
	"cerebras":    "cerebras",
	"openrouter":  "openrouter",
	"mistral":     "mistral",
	"together":    "together",
	"together_ai": "together",
	"deepseek":    "deepseek",
	"kimi":        "kimi",
	"hive":        "hive",
}

type SubscriptionDefinition struct {
	ID          string
	Name        string
	Description string
	Flag        string
}

var subscriptionDefinitions = []SubscriptionDefinition{
	{"claude_code", "Claude Code Subscription", "Use your Claude Max/Pro plan", "use_claude_code_subscription"},
	{"zai_code", "ZAI Code Subscription", "Use your ZAI Code plan", "use_zai_code_subscription"},
	{"codex", "OpenAI Codex Subscription", "Use your Codex/ChatGPT Plus plan", "use_codex_subscription"},
	{"minimax_code", "MiniMax Coding Key", "Use your MiniMax coding key", "use_minimax_code_subscription"},
	{"kimi_code", "Kimi Code Subscription", "Use your Kimi Code plan", "use_kimi_code_subscription"},
	{"hive_llm", "Hive LLM", "Use your Hive API key", "use_hive_llm_subscription"},
	{"antigravity", "Antigravity Subscription", "Use your Google/Gemini plan", "use_antigravity_subscription"},
}

var allSubscriptionFlags = func() []string {
	flags := make([]string, 0, len(subscriptionDefinitions))
	for _, definition := range subscriptionDefinitions {
		flags = append(flags, definition.Flag)
	}
	return flags
}()

func subscriptionDefinition(id string) (SubscriptionDefinition, bool) {
	for _, definition := range subscriptionDefinitions {
		if definition.ID == id {
			return definition, true
		}
	}
	return SubscriptionDefinition{}, false
}

type Subscription struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Provider     string `json:"provider"`
	Flag         string `json:"flag"`
	DefaultModel string `json:"default_model"`
	APIBase      string `json:"api_base,omitempty"`
}

// Parity: framework/server/routes_config.py::_build_subscriptions
func BuildSubscriptions() ([]Subscription, error) {
	out := make([]Subscription, 0, len(subscriptionDefinitions))
	for _, definition := range subscriptionDefinitions {
		preset, ok := llm.GetPreset(definition.ID)
		if !ok {
			return nil, errors.New("Missing preset for subscription " + definition.ID)
		}
		row := Subscription{
			ID:           definition.ID,
			Name:         definition.Name,
			Description:  definition.Description,
			Provider:     preset.Provider,
			Flag:         definition.Flag,
			DefaultModel: preset.Model,
		}
		if preset.APIBase != "" {
			row.APIBase = preset.APIBase
		}
		out = append(out, row)
	}
	return out, nil
}

type LLMRouteDeps struct {
	CredentialKey       func(credentialID string) (string, bool)
	SubscriptionToken   func(subscriptionID string) (string, bool)
	DetectSubscriptions func() []string
	Validate            func(provider, apiKey string, apiBase, model *string) map[string]any
	HotSwap             func(fullModel string, apiKey, apiBase *string) int
}

func (deps LLMRouteDeps) withDefaults(home hivehome.Home) LLMRouteDeps {
	if deps.CredentialKey == nil {
		deps.CredentialKey = readCredentialKey
	}
	if deps.SubscriptionToken == nil {
		deps.SubscriptionToken = defaultSubscriptionToken(home)
	}
	if deps.DetectSubscriptions == nil {
		deps.DetectSubscriptions = defaultDetectSubscriptions(home)
	}
	if deps.Validate == nil {
		deps.Validate = func(provider, apiKey string, apiBase, model *string) map[string]any {
			return llm.ValidateProviderKey(provider, apiKey, apiBase, model)
		}
	}
	if deps.HotSwap == nil {
		deps.HotSwap = func(string, *string, *string) int { return 0 }
	}
	return deps
}

func readCredentialKey(credentialID string) (string, bool) {
	store, err := credentials.WithEncryptedStorage("")
	if err != nil {
		return "", false
	}
	key, err := store.Get(credentialID)
	if err != nil || key == nil {
		return "", false
	}
	return *key, true
}

func (deps LLMRouteDeps) subscriptionToken(id string) string {
	if deps.SubscriptionToken == nil {
		return ""
	}
	value, _ := deps.SubscriptionToken(id)
	return value
}

func (deps LLMRouteDeps) detectSubscriptions() []string {
	if deps.DetectSubscriptions == nil {
		return nil
	}
	return deps.DetectSubscriptions()
}

func defaultSubscriptionToken(home hivehome.Home) func(string) (string, bool) {
	return func(id string) (string, bool) {
		switch id {
		case "claude_code":
			return claudeCodeToken(), true
		case "zai_code":
			return os.Getenv("ZAI_API_KEY"), true
		case "codex":
			return codexToken(), true
		case "minimax_code":
			return os.Getenv("MINIMAX_API_KEY"), true
		case "kimi_code":
			if token := kimiCodeToken(); token != "" {
				return token, true
			}
			return os.Getenv("KIMI_API_KEY"), true
		case "hive_llm":
			return os.Getenv("HIVE_API_KEY"), true
		case "antigravity":
			return antigravityToken(home), true
		}
		return "", false
	}
}

// Parity: framework/server/routes_config.py::_detect_subscriptions
func defaultDetectSubscriptions(home hivehome.Home) func() []string {
	return func() []string {
		out := []string{}
		if claudeCodeToken() != "" {
			out = append(out, "claude_code")
		}
		if os.Getenv("ZAI_API_KEY") != "" {
			out = append(out, "zai_code")
		}
		if codexToken() != "" {
			out = append(out, "codex")
		}
		if os.Getenv("MINIMAX_API_KEY") != "" {
			out = append(out, "minimax_code")
		}
		kimi := kimiCodeToken()
		if kimi == "" {
			kimi = os.Getenv("KIMI_API_KEY")
		}
		if kimi != "" {
			out = append(out, "kimi_code")
		}
		if os.Getenv("HIVE_API_KEY") != "" {
			out = append(out, "hive_llm")
		}
		if antigravityToken(home) != "" {
			out = append(out, "antigravity")
		}
		return out
	}
}

// Subscription-token readers delegate to hiveconf so the pipeline's LLM stage
// and this route family share one resolution (A18).

func claudeCodeToken() string { return hiveconf.ClaudeCodeToken() }

func codexToken() string { return hiveconf.CodexToken() }

func kimiCodeToken() string { return hiveconf.KimiCodeToken() }

func antigravityToken(home hivehome.Home) string { return hiveconf.AntigravityToken(home) }

func homeDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}

// Parity: framework/server/routes_config.py::handle_get_llm_config
func LLMConfigResponse(home hivehome.Home, deps LLMRouteDeps) (int, map[string]any) {
	deps = deps.withDefaults(home)
	config := HiveConfig(home)
	llmSection, err := llmSectionOf(config)
	if err != nil {
		return 500, errorBody()
	}

	provider, hasProvider := llmSection["provider"]
	if !hasProvider {
		provider = ""
	}
	model, hasModel := llmSection["model"]
	if !hasModel {
		model = ""
	}

	hasKey, err := deps.configuredAPIKeyPresent(config)
	if err != nil {
		return 500, errorBody()
	}
	if !hasKey {
		resolved, err := deps.resolveAPIKey(provider)
		if err != nil {
			return 500, errorBody()
		}
		hasKey = resolved != nil
	}

	connected := make([]string, 0, len(providerEnvVars))
	for _, entry := range providerEnvVars {
		if entry.Provider == "google" || entry.Provider == "together_ai" {
			continue
		}
		key, err := deps.resolveAPIKey(entry.Provider)
		if err != nil {
			return 500, errorBody()
		}
		if key != nil {
			connected = append(connected, entry.Provider)
		}
	}

	subscriptions, err := BuildSubscriptions()
	if err != nil {
		return 500, errorBody()
	}

	var activeSubscription any
	for _, sub := range subscriptions {
		if pyTruthy(llmSection[sub.Flag]) {
			activeSubscription = sub.ID
			break
		}
	}

	detected := make([]string, 0)
	for _, id := range deps.detectSubscriptions() {
		if deps.subscriptionToken(id) != "" {
			detected = append(detected, id)
		}
	}

	return 200, map[string]any{
		"provider":               provider,
		"model":                  model,
		"has_api_key":            hasKey,
		"max_tokens":             llmSection["max_tokens"],
		"max_context_tokens":     llmSection["max_context_tokens"],
		"connected_providers":    connected,
		"active_subscription":    activeSubscription,
		"detected_subscriptions": detected,
		"subscriptions":          subscriptions,
		"api_base":               llmSection["api_base"],
		"api_key_env_var":        llmSection["api_key_env_var"],
	}
}

// Parity: framework/config.py::get_api_key
func (deps LLMRouteDeps) configuredAPIKeyPresent(config map[string]any) (bool, error) {
	llmSection, err := llmSectionOf(config)
	if err != nil {
		return false, err
	}

	if name, ok := llmSection["endpoint"].(string); ok && name != "" {
		if llm.KeyFor(name) != "" {
			return true, nil
		}
	}

	if pool, ok := llmSection["api_keys"].([]any); ok && len(pool) > 0 {
		for _, item := range pool {
			if pyTruthy(item) {
				return true, nil
			}
		}
	}

	if pyTruthy(llmSection["api_key"]) {
		return true, nil
	}

	for _, id := range []string{"claude_code", "codex", "kimi_code", "antigravity"} {
		definition, _ := subscriptionDefinition(id)
		if pyTruthy(llmSection[definition.Flag]) && deps.subscriptionToken(id) != "" {
			return true, nil
		}
	}

	if os.Getenv("HIVE_CREDENTIAL_KEY") != "" {
		provider, hasProvider := llmSection["provider"]
		if !hasProvider {
			provider = ""
		}
		name, ok := provider.(string)
		if !ok {
			return false, errProviderNotString
		}
		if credID, found := providerCredMap[strings.ToLower(name)]; found {
			if key, ok := deps.CredentialKey(credID); ok && key != "" {
				return true, nil
			}
		}
	}

	if envValue := llmSection["api_key_env_var"]; pyTruthy(envValue) {
		name, ok := envValue.(string)
		if !ok {
			return false, errProviderNotString
		}
		if value, present := os.LookupEnv(name); present && value != "" {
			return true, nil
		}
	}
	return false, nil
}

// Parity: framework/server/routes_config.py::_resolve_api_key
func (deps LLMRouteDeps) resolveAPIKey(provider any) (*string, error) {
	name, ok := provider.(string)
	if !ok {
		return nil, errProviderNotString
	}
	lower := strings.ToLower(name)
	if credID, found := providerCredMap[lower]; found {
		if key, ok := deps.CredentialKey(credID); ok && key != "" {
			return &key, nil
		}
	}
	if envVar, found := providerEnvVarFor(lower); found {
		if value, present := os.LookupEnv(envVar); present {
			return &value, nil
		}
	}
	return nil, nil
}

// Parity: framework/server/routes_config.py::handle_update_llm_config
func UpdateLLMConfig(home hivehome.Home, body map[string]any, deps LLMRouteDeps) (int, map[string]any) {
	deps = deps.withDefaults(home)
	if pyTruthy(body["custom"]) {
		return updateLLMCustom(home, body, deps)
	}
	subscriptionID, hasSubscription := body["subscription"]
	if hasSubscription && pyTruthy(subscriptionID) {
		return updateLLMSubscription(home, subscriptionID, deps)
	}
	return updateLLMAPIKey(home, body, deps)
}

// Parity: framework/server/routes_config.py::handle_update_llm_config (custom)
func updateLLMCustom(home hivehome.Home, body map[string]any, deps LLMRouteDeps) (int, map[string]any) {
	model := strings.TrimSpace(pyStrValue(firstTruthyValue(body["model"], "")))
	if model == "" {
		return 400, map[string]any{"error": "'model' is required"}
	}

	var (
		status   int
		response map[string]any
		saved    map[string]any
		apiBase  string
		envVar   string
		apiKey   *string
	)
	err := MutateHiveConfig(home, func(config map[string]any) bool {
		llmSection, err := llmSectionForWrite(config)
		if err != nil {
			status, response = 500, errorBody()
			return false
		}

		provider := pyStrValue(firstTruthyValue(body["provider"], llmSection["provider"], "openai"))
		apiBase = strings.TrimSpace(pyStrValue(firstTruthyValue(body["api_base"], llmSection["api_base"], "")))
		envVar = strings.TrimSpace(pyStrValue(firstTruthyValue(body["api_key_env_var"], llmSection["api_key_env_var"], "")))

		if envVar != "" {
			if value, present := os.LookupEnv(envVar); present {
				apiKey = &value
			}
		}
		if apiKey == nil {
			resolved, err := deps.resolveAPIKey(provider)
			if err != nil {
				status, response = 500, errorBody()
				return false
			}
			apiKey = resolved
		}

		llmSection["provider"] = provider
		llmSection["model"] = model
		if pyTruthy(body["max_tokens"]) {
			value, ok := ratelimit.CoerceInt(body["max_tokens"])
			if !ok {
				status, response = 500, errorBody()
				return false
			}
			llmSection["max_tokens"] = value
		}
		if pyTruthy(body["max_context_tokens"]) {
			value, ok := ratelimit.CoerceInt(body["max_context_tokens"])
			if !ok {
				status, response = 500, errorBody()
				return false
			}
			llmSection["max_context_tokens"] = value
		}
		if envVar != "" {
			llmSection["api_key_env_var"] = envVar
		}
		if apiBase != "" {
			llmSection["api_base"] = apiBase
		}
		for _, flag := range allSubscriptionFlags {
			delete(llmSection, flag)
		}
		saved = llmSection
		return true
	})
	if err != nil {
		return 500, errorBody()
	}
	if response != nil {
		return status, response
	}

	swapped := deps.HotSwap(pyStrValue(saved["provider"])+"/"+model, apiKey, optionalString(apiBase))

	return 200, map[string]any{
		"provider":            saved["provider"],
		"model":               model,
		"has_api_key":         apiKey != nil,
		"max_tokens":          saved["max_tokens"],
		"max_context_tokens":  saved["max_context_tokens"],
		"sessions_swapped":    swapped,
		"active_subscription": nil,
		"api_base":            strOrNil(apiBase),
		"api_key_env_var":     strOrNil(envVar),
	}
}

// Parity: framework/server/routes_config.py::handle_update_llm_config (subscription)
func updateLLMSubscription(home hivehome.Home, subscriptionID any, deps LLMRouteDeps) (int, map[string]any) {
	subscriptions, err := BuildSubscriptions()
	if err != nil {
		return 500, errorBody()
	}
	sub, status, response := findSubscription(subscriptions, subscriptionID)
	if response != nil {
		return status, response
	}

	var apiBase *string
	if sub.APIBase != "" {
		apiBase = &sub.APIBase
	}

	token := deps.subscriptionToken(sub.ID)
	if token == "" {
		return 400, map[string]any{"error": "No credential found for " + sub.Name +
			". Please check your subscription or API key."}
	}

	check := deps.Validate(sub.Provider, token, apiBase, optionalString(sub.DefaultModel))
	if isFalse, ok := check["valid"].(bool); ok && !isFalse {
		return 400, map[string]any{"error": sub.Name + " key validation failed: " + validationMessage(check)}
	}

	maxTokens, maxContext := 8192, 120000
	if preset, ok := llm.GetPreset(sub.ID); ok {
		maxTokens = preset.MaxTokens
		maxContext = preset.MaxContextTokens
	}

	wrote := false
	err = MutateHiveConfig(home, func(config map[string]any) bool {
		llmSection, err := llmSectionForWrite(config)
		if err != nil {
			return false
		}
		llmSection["provider"] = sub.Provider
		llmSection["model"] = sub.DefaultModel
		llmSection["max_tokens"] = maxTokens
		llmSection["max_context_tokens"] = maxContext
		for _, flag := range allSubscriptionFlags {
			delete(llmSection, flag)
		}
		llmSection[sub.Flag] = true
		delete(llmSection, "api_key_env_var")
		if sub.APIBase != "" {
			llmSection["api_base"] = sub.APIBase
		} else {
			delete(llmSection, "api_base")
		}
		wrote = true
		return true
	})
	if err != nil || !wrote {
		return 500, errorBody()
	}

	swapped := deps.HotSwap(sub.Provider+"/"+sub.DefaultModel, &token, apiBase)

	return 200, map[string]any{
		"provider":            sub.Provider,
		"model":               sub.DefaultModel,
		"has_api_key":         true,
		"max_tokens":          maxTokens,
		"max_context_tokens":  maxContext,
		"sessions_swapped":    swapped,
		"active_subscription": sub.ID,
	}
}

// Parity: framework/server/routes_config.py::handle_update_llm_config (api key)
func updateLLMAPIKey(home hivehome.Home, body map[string]any, deps LLMRouteDeps) (int, map[string]any) {
	providerRaw, modelRaw := body["provider"], body["model"]
	if !pyTruthy(providerRaw) || !pyTruthy(modelRaw) {
		return 400, map[string]any{"error": "Both 'provider' and 'model' are required"}
	}

	entry, found, err := findModelInfo(providerRaw, modelRaw)
	if err != nil {
		return 500, errorBody()
	}
	if !found {
		return 400, map[string]any{"error": "Model '" + pyStrValue(modelRaw) +
			"' is not available for provider '" + pyStrValue(providerRaw) + "'."}
	}

	provider := providerRaw.(string)
	model := modelRaw.(string)
	envVar, _ := providerEnvVarFor(provider)

	var apiBase any
	if strings.ToLower(provider) == "openrouter" {
		apiBase = llm.OpenRouterAPIBase
	}
	if apiBase == nil && strings.ToLower(provider) == "hive" {
		llmSection, err := llmSectionOf(HiveConfig(home))
		if err != nil {
			return 500, errorBody()
		}
		if base, present := llmSection["api_base"]; present {
			apiBase = base
		}
	}

	apiKey, err := deps.resolveAPIKey(provider)
	if err != nil {
		return 500, errorBody()
	}
	if apiKey == nil || *apiKey == "" {
		return 400, map[string]any{"error": "No API key found for " + provider +
			". Please add one in Manage Keys."}
	}

	check := deps.Validate(provider, *apiKey, stringPointer(apiBase), &model)
	if isFalse, ok := check["valid"].(bool); ok && !isFalse {
		return 400, map[string]any{"error": "API key validation failed for " + provider + ": " +
			validationMessage(check)}
	}

	wrote := false
	err = MutateHiveConfig(home, func(config map[string]any) bool {
		llmSection, err := llmSectionForWrite(config)
		if err != nil {
			return false
		}
		llmSection["provider"] = provider
		llmSection["model"] = modelRaw
		llmSection["max_tokens"] = entry.MaxTokens
		llmSection["max_context_tokens"] = entry.MaxContextTokens
		if envVar != "" {
			llmSection["api_key_env_var"] = envVar
		}
		if pyTruthy(apiBase) {
			llmSection["api_base"] = apiBase
		} else {
			delete(llmSection, "api_base")
		}
		for _, flag := range allSubscriptionFlags {
			delete(llmSection, flag)
		}
		wrote = true
		return true
	})
	if err != nil || !wrote {
		return 500, errorBody()
	}

	swapped := deps.HotSwap(provider+"/"+model, apiKey, stringPointer(apiBase))

	return 200, map[string]any{
		"provider":            provider,
		"model":               modelRaw,
		"has_api_key":         true,
		"max_tokens":          entry.MaxTokens,
		"max_context_tokens":  entry.MaxContextTokens,
		"sessions_swapped":    swapped,
		"active_subscription": nil,
	}
}

func findSubscription(subscriptions []Subscription, id any) (*Subscription, int, map[string]any) {
	name, ok := id.(string)
	if !ok {
		switch id.(type) {
		case []any, map[string]any:
			return nil, 500, errorBody()
		}
		return nil, 400, map[string]any{"error": "Unknown subscription: " + pyStrValue(id)}
	}
	for i := range subscriptions {
		if subscriptions[i].ID == name {
			return &subscriptions[i], 200, nil
		}
	}
	return nil, 400, map[string]any{"error": "Unknown subscription: " + name}
}

func findModelInfo(provider, model any) (llm.ModelEntry, bool, error) {
	providerID, ok := provider.(string)
	if !ok {
		switch provider.(type) {
		case []any, map[string]any:
			return llm.ModelEntry{}, false, errUnhashable
		}
		return llm.ModelEntry{}, false, nil
	}
	modelID, ok := model.(string)
	if !ok {
		return llm.ModelEntry{}, false, nil
	}
	entry, found := llm.FindModel(providerID, modelID)
	return entry, found, nil
}

func llmSectionOf(config map[string]any) (map[string]any, error) {
	raw, present := config["llm"]
	if !present {
		return map[string]any{}, nil
	}
	section, ok := raw.(map[string]any)
	if !ok {
		return nil, errLLMNotObject
	}
	return section, nil
}

func llmSectionForWrite(config map[string]any) (map[string]any, error) {
	section, err := llmSectionOf(config)
	if err != nil {
		return nil, err
	}
	if _, present := config["llm"]; !present {
		config["llm"] = section
	}
	return section, nil
}

func firstTruthyValue(values ...any) any {
	for _, value := range values {
		if pyTruthy(value) {
			return value
		}
	}
	return values[len(values)-1]
}

func validationMessage(check map[string]any) string {
	message, present := check["message"]
	if !present {
		return "unknown error"
	}
	return pyStrValue(message)
}

func optionalString(value any) *string {
	if s, ok := value.(string); ok {
		return &s
	}
	return nil
}

func stringPointer(value any) *string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return &v
	case json.Number:
		s := v.String()
		return &s
	case float64:
		s := pyStr(v)
		return &s
	case bool:
		s := pyStr(v)
		return &s
	}
	return nil
}

func strOrNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func errorBody() map[string]any {
	return map[string]any{"error": internalServerError}
}
