package server

// Model-slot and global-loop configuration readers.
//
// These are the configuration.json values the Go port used to expose through
// REST/CRUD only (worker_llm.*, vision_fallback.*, queen_llm.<id>.*,
// llm.aux_max_tokens, loop.max_tool_result_chars,
// worker_tools.always_enabled_categories, llm.antigravity_client_*). Reading
// them here gives the runtime a single, testable place to resolve a slot, so
// the Rest layer and the session/loop layer cannot drift apart.
//
// Parity: framework/config.py::get_queen_llm_section, section_model,
// section_extra_body, get_preferred_worker_model, get_vision_fallback_model,
// get_vision_fallback_api_key, get_vision_fallback_api_base, get_aux_max_tokens,
// get_max_tool_result_chars, get_antigravity_client_id,
// get_antigravity_client_secret;
// framework/agents/queen/queen_tools_defaults.py::configured_worker_always_enabled_categories

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/hiveconf"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tools"
)

// configSection returns one object-valued section of configuration.json. A
// missing or non-object section reads as nil (upstream reads it as {}).
func configSection(home hivehome.Home, name string) map[string]any {
	section, _ := HiveConfig(home)[name].(map[string]any)
	return section
}

// jsonIntValue reports the value as an int only when it is a JSON integer.
// Python's readers compare against int: 7 counts, 7.0, true and "7" do not.
func jsonIntValue(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		switch v := value.(type) {
		case int:
			return v, true
		case int64:
			return int(v), true
		}
		return 0, false
	}
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		return 0, false
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// SectionConfigured reports whether a model slot carries a usable
// provider/model pair. Python's readers (get_queen_llm_section,
// get_preferred_worker_model, get_vision_fallback_model) all require a truthy
// provider and a non-blank model.
func SectionConfigured(section map[string]any) bool {
	if section == nil {
		return false
	}
	provider, _ := section["provider"].(string)
	if strings.TrimSpace(provider) == "" {
		return false
	}
	model, _ := section["model"].(string)
	return strings.TrimSpace(model) != ""
}

// SectionModel renders a model section as "provider/model" the way LiteLLM
// takes it: openrouter keeps raw model ids, so an "openrouter/<id>" model is
// unwrapped first.
//
// Parity: framework/config.py::section_model
func SectionModel(section map[string]any) string {
	provider, _ := section["provider"].(string)
	provider = strings.TrimSpace(provider)
	model, _ := section["model"].(string)
	model = strings.TrimSpace(model)
	if strings.EqualFold(provider, "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
		model = model[len("openrouter/"):]
	}
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

// SectionExtraBody returns the section's extra_body object, or nil when it is
// absent/empty. The fields are copied into every request body verbatim.
//
// Parity: framework/config.py::section_extra_body
func SectionExtraBody(section map[string]any) map[string]any {
	extra, ok := section["extra_body"].(map[string]any)
	if !ok || len(extra) == 0 {
		return nil
	}
	copied := make(map[string]any, len(extra))
	for key, value := range extra {
		copied[key] = value
	}
	return copied
}

// SectionNumCtx is the section's num_ctx (ollama only), defaulting to 16384 —
// the same default the llm slot carries.
func SectionNumCtx(section map[string]any) int {
	if value, ok := jsonIntValue(section["num_ctx"]); ok {
		return value
	}
	return 16384
}

// SectionAPIKey resolves a slot's key in the upstream order: the named
// endpoint's own key, the literal api_key, the provider's credential-store
// entry, the api_key_env_var, and finally mainKey (the main llm slot's key, as
// resolved by the caller). subscription tokens are deliberately absent: the Go
// port has no subscription-authenticated provider to hand them to.
//
// Parity: framework/config.py::get_queen_api_key, get_vision_fallback_api_key
func SectionAPIKey(section map[string]any, mainKey func() string) string {
	if section == nil {
		return callKey(mainKey)
	}
	if name, ok := section["endpoint"].(string); ok && strings.TrimSpace(name) != "" {
		if key := llm.KeyFor(strings.TrimSpace(name)); key != "" {
			return key
		}
	}
	if literal, ok := section["api_key"].(string); ok && literal != "" {
		return literal
	}
	provider, _ := section["provider"].(string)
	if stored, ok := storedProviderKey(provider); ok {
		return stored
	}
	if envName, ok := section["api_key_env_var"].(string); ok && envName != "" {
		if value := os.Getenv(envName); value != "" {
			return value
		}
	}
	if envName, ok := providerEnvVarFor(provider); ok {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	return callKey(mainKey)
}

func callKey(mainKey func() string) string {
	if mainKey == nil {
		return ""
	}
	return mainKey()
}

// storedProviderKey looks a provider's key up in the encrypted credential
// store. Without HIVE_CREDENTIAL_KEY there is no store (upstream's read-only
// fallback), so the lookup reports absent rather than guessing.
func storedProviderKey(provider string) (string, bool) {
	if strings.TrimSpace(provider) == "" || os.Getenv("HIVE_CREDENTIAL_KEY") == "" {
		return "", false
	}
	credentialID, ok := providerCredMap[strings.ToLower(strings.TrimSpace(provider))]
	if !ok {
		return "", false
	}
	return readCredentialKey(credentialID)
}

// SectionAPIBase resolves a slot's base URL: the literal api_base, then the
// provider's built-in base for openrouter.
//
// Parity: framework/config.py::get_queen_api_base, get_vision_fallback_api_base
func SectionAPIBase(section map[string]any) string {
	if section == nil {
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

// MainSlotAPIKey resolves the main llm slot's key lazily: the literal api_key,
// the api_key_env_var (or the provider's environment variable), then
// HIVE_API_KEY. Slot sections without a key of their own fall back here, as
// upstream's get_api_key tail does.
//
// Parity: framework/config.py::get_api_key
func MainSlotAPIKey(home hivehome.Home) func() string {
	llmSection := configSection(home, "llm")
	literal := strings.TrimSpace(pyStrOrEmpty(llmSection["api_key"]))
	provider := strings.ToLower(strings.TrimSpace(pyStrOrEmpty(llmSection["provider"])))
	if provider == "" || provider == "custom" {
		provider = providerFromModelName(strings.TrimSpace(pyStrOrEmpty(llmSection["model"])))
	}
	envVar := strings.TrimSpace(pyStrOrEmpty(llmSection["api_key_env_var"]))
	if envVar == "" {
		envVar = slotProviderEnvVars[provider]
	}
	return func() string {
		if literal != "" {
			return literal
		}
		if envVar != "" {
			if value := strings.TrimSpace(os.Getenv(envVar)); value != "" {
				return value
			}
		}
		return strings.TrimSpace(os.Getenv("HIVE_API_KEY"))
	}
}

// slotProviderEnvVars is the slot-resolution provider table. It is separate
// from providerEnvVars (which drives GET /api/config/llm's connected_providers
// list and must stay upstream-shaped): it also carries the providers the CLI's
// session builder knows but the connected list does not.
var slotProviderEnvVars = map[string]string{
	"anthropic":   "ANTHROPIC_API_KEY",
	"openai":      "OPENAI_API_KEY",
	"gemini":      "GEMINI_API_KEY",
	"google":      "GOOGLE_API_KEY",
	"minimax":     "MINIMAX_API_KEY",
	"groq":        "GROQ_API_KEY",
	"cerebras":    "CEREBRAS_API_KEY",
	"openrouter":  "OPENROUTER_API_KEY",
	"mistral":     "MISTRAL_API_KEY",
	"together":    "TOGETHER_API_KEY",
	"together_ai": "TOGETHER_API_KEY",
	"deepseek":    "DEEPSEEK_API_KEY",
	"kimi":        "KIMI_API_KEY",
	"hive":        "HIVE_API_KEY",
	"zai":         "ZAI_API_KEY",
	"xai":         "XAI_API_KEY",
}

// providerFromModelName infers a provider from a model id when the section
// names none (the CLI resolves sessions the same way).
func providerFromModelName(model string) string {
	lower := strings.ToLower(model)
	if index := strings.Index(lower, "/"); index > 0 {
		if _, known := slotProviderEnvVars[lower[:index]]; known {
			return lower[:index]
		}
	}
	switch {
	case llm.IsAnthropicModel(lower):
		return "anthropic"
	case strings.HasPrefix(lower, "gemini-"):
		return "gemini"
	}
	return "openai"
}

// SectionProvider builds the HTTP transport for one configured model section.
// Key, base and extra-body resolution live here so the runtime rebuild path
// and the session builder cannot drift.
//
// Parity: framework/server/queen_models.py::build
func SectionProvider(home hivehome.Home, section map[string]any) *llm.Provider {
	provider := llm.NewHTTPProvider(llm.ProviderOptions{
		Model:   SectionModel(section),
		APIKey:  SectionAPIKey(section, MainSlotAPIKey(home)),
		APIBase: SectionAPIBase(section),
	})
	if extra := sectionExtraKwargs(section); extra != nil {
		provider.ExtraKwargs = extra
	}
	return provider
}

// sectionExtraKwargs mirrors upstream's per-slot extra kwargs: num_ctx for an
// ollama model, otherwise the section's extra_body (a provider's thinking
// switch, reasoning_effort).
func sectionExtraKwargs(section map[string]any) map[string]any {
	if provider, _ := section["provider"].(string); strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return map[string]any{"num_ctx": SectionNumCtx(section)}
	}
	if extra := SectionExtraBody(section); extra != nil {
		return map[string]any{"extra_body": extra}
	}
	return nil
}

// ReconfigureSection rebuilds the provider behind a live session (or colony
// worker slot) from a written model section: the model, key, base and
// extra_body upstream's provider.reconfigure replaces. A holder without a
// reconfigure seam reports false, so callers count only real switches.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.reconfigure
func ReconfigureSection(holder any, home hivehome.Home, section map[string]any) bool {
	reconfigurable, ok := holder.(interface {
		Reconfigure(fullModel string, apiKey, apiBase *string, extraBody any)
	})
	if !ok || section == nil || !SectionConfigured(section) {
		return false
	}
	key := SectionAPIKey(section, MainSlotAPIKey(home))
	if key == "" {
		// Nothing resolvable in the section: keep the live provider's key
		// instead of stripping the Authorization header upstream's
		// resolver-based provider would have kept.
		if completer, ok := holder.(llm.Completer); ok {
			if live, ok := llm.ResolveCompleter(completer).(*llm.Provider); ok {
				key = live.APIKey
			}
		}
	}
	var apiKey *string
	if key != "" {
		apiKey = &key
	}
	var apiBase *string
	if base := SectionAPIBase(section); base != "" {
		apiBase = &base
	}
	reconfigurable.Reconfigure(SectionModel(section), apiKey, apiBase, section["extra_body"])
	return true
}

// QueenLLMSection is a queen's own model section, or nil when she has none (or
// it lacks a provider/model pair).
//
// Parity: framework/config.py::get_queen_llm_section
func QueenLLMSection(home hivehome.Home, queenID string) map[string]any {
	queenID = strings.TrimSpace(queenID)
	if queenID == "" {
		return nil
	}
	sections, _ := HiveConfig(home)["queen_llm"].(map[string]any)
	section, _ := sections[queenID].(map[string]any)
	if !SectionConfigured(section) {
		return nil
	}
	return section
}

// WorkerLLMSection is the configured worker model section, or nil when the
// worker shares the main model (upstream's get_preferred_worker_model returns
// None in that case).
//
// Parity: framework/config.py::get_preferred_worker_model
func WorkerLLMSection(home hivehome.Home) map[string]any {
	section := configSection(home, "worker_llm")
	if !SectionConfigured(section) {
		return nil
	}
	return section
}

// VisionFallbackSection is the configured vision-fallback slot, or nil.
//
// Parity: framework/config.py::get_vision_fallback_model
func VisionFallbackSection(home hivehome.Home) map[string]any {
	section := configSection(home, "vision_fallback")
	if !SectionConfigured(section) {
		return nil
	}
	return section
}

// VisionFallbackModel is "provider/model" for the configured fallback, or "".
//
// Parity: framework/config.py::get_vision_fallback_model
func VisionFallbackModel(home hivehome.Home) string {
	section := VisionFallbackSection(home)
	if section == nil {
		return ""
	}
	return SectionModel(section)
}

// VisionFallbackMaxTokens is the configured caption budget, or 0 when absent so
// the caller falls back to the aux budget (upstream: `max_tokens or
// get_aux_max_tokens()`).
//
// Parity: framework/agent_loop/internals/vision_fallback.py::caption_tool_image
func VisionFallbackMaxTokens(section map[string]any) int {
	if value, ok := jsonIntValue(section["max_tokens"]); ok && value > 0 {
		return value
	}
	return 0
}

// LLMMaxTokens resolves the main llm slot's output budget: the configured
// max_tokens, else the model catalog entry, else 8192.
//
// Parity: framework/config.py::get_max_tokens
func LLMMaxTokens(home hivehome.Home) int {
	llmSection := configSection(home, "llm")
	if value, ok := jsonIntValue(llmSection["max_tokens"]); ok {
		return value
	}
	if limits, ok := catalogLimits(llmSection); ok {
		return limits[0]
	}
	return llm.DefaultMaxTokens
}

// catalogLimits looks the section's provider/model pair up in the curated
// model catalog.
func catalogLimits(section map[string]any) ([2]int, bool) {
	if section == nil {
		return [2]int{}, false
	}
	provider, _ := section["provider"].(string)
	model, _ := section["model"].(string)
	model = strings.TrimSpace(model)
	if strings.EqualFold(strings.TrimSpace(provider), "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
		model = model[len("openrouter/"):]
	}
	if strings.TrimSpace(provider) == "" || model == "" {
		return [2]int{}, false
	}
	maxTokens, maxContext, ok := llm.GetModelLimits(strings.TrimSpace(provider), model)
	if !ok {
		return [2]int{}, false
	}
	return [2]int{maxTokens, maxContext}, true
}

// QueenMaxContextTokens is the context window a queen's own loop budgets by:
// her section's max_context_tokens, else her model's catalog window, else the
// caller's fallback (the queen loop's 180k). Without a section of her own the
// main llm slot governs, which the caller already applied.
//
// Parity: framework/config.py::get_queen_max_context_tokens
func QueenMaxContextTokens(home hivehome.Home, queenID string, fallback int) int {
	section := QueenLLMSection(home, queenID)
	if section == nil {
		return fallback
	}
	if value, ok := jsonIntValue(section["max_context_tokens"]); ok {
		return value
	}
	if limits, ok := catalogLimits(section); ok {
		return limits[1]
	}
	return fallback
}

// AuxMaxTokens is the output budget for small utility LLM calls:
// llm.aux_max_tokens, else min(8192, the main model's max_tokens).
//
// Parity: framework/config.py::get_aux_max_tokens
func AuxMaxTokens(home hivehome.Home) int {
	llmSection := configSection(home, "llm")
	if value, ok := jsonIntValue(llmSection["aux_max_tokens"]); ok {
		return value
	}
	maxTokens := LLMMaxTokens(home)
	if maxTokens < llm.DefaultAuxMaxTokens {
		return maxTokens
	}
	return llm.DefaultAuxMaxTokens
}

// LoopMaxToolResultChars is the spillover threshold for tool results:
// loop.max_tool_result_chars, else the caller's fallback (30_000 for a global
// loop, the profile's value for a queen/worker loop).
//
// Parity: framework/config.py::get_max_tool_result_chars
func LoopMaxToolResultChars(home hivehome.Home, fallback int) int {
	return hiveconf.MaxToolResultChars(home, fallback)
}

// WorkerToolsAlwaysEnabledCategories is the worker keep-set override: the
// configured categories when worker_tools.always_enabled_categories is a list
// of strings, else nil (the hardcoded default applies).
//
// Parity: framework/agents/queen/queen_tools_defaults.py::configured_worker_always_enabled_categories
func WorkerToolsAlwaysEnabledCategories(home hivehome.Home) ([]string, bool) {
	section := configSection(home, "worker_tools")
	if section == nil {
		return nil, false
	}
	raw, ok := section["always_enabled_categories"].([]any)
	if !ok {
		return nil, false
	}
	categories := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			categories = append(categories, text)
		}
	}
	if len(categories) != len(raw) {
		// Upstream requires every entry to be a string; a mixed list is not an
		// override at all, it keeps the default.
		return nil, false
	}
	return categories, true
}

// WorkerKeepSetFromConfig turns the worker_tools override into the keep-set
// closure the colony runtime takes. A nil func means "no override" so the
// runtime keeps its hardcoded default; a configured empty list returns an empty
// set, which turns worker tiering off exactly as upstream documents.
//
// Parity: framework/agents/queen/queen_tools_defaults.py::worker_always_enabled_tool_names
func WorkerKeepSetFromConfig(home hivehome.Home) func() map[string]struct{} {
	categories, ok := WorkerToolsAlwaysEnabledCategories(home)
	if !ok {
		return nil
	}
	toolNames := map[string]struct{}{}
	for _, category := range categories {
		for _, name := range tools.ResolveCategoryTools(category, nil) {
			toolNames[name] = struct{}{}
		}
	}
	return func() map[string]struct{} { return toolNames }
}

// AntigravityClientID is the Antigravity OAuth client id: ANTIGRAVITY_CLIENT_ID,
// then llm.antigravity_client_id. The public-source fetch upstream falls back to
// is not ported (the Go login path reads the account file instead), so an
// unconfigured id reads as "".
//
// Parity: framework/config.py::get_antigravity_client_id
func AntigravityClientID(home hivehome.Home) string {
	return antigravitySetting(home, "ANTIGRAVITY_CLIENT_ID", "antigravity_client_id")
}

// AntigravityClientSecret mirrors AntigravityClientID for the secret.
//
// Parity: framework/config.py::get_antigravity_client_secret
func AntigravityClientSecret(home hivehome.Home) string {
	return antigravitySetting(home, "ANTIGRAVITY_CLIENT_SECRET", "antigravity_client_secret")
}

func antigravitySetting(home hivehome.Home, envName, key string) string {
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		return value
	}
	llmSection := configSection(home, "llm")
	if value, ok := llmSection[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}
