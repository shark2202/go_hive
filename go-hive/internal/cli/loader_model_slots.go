package cli

// Providers for the model slots beside the main llm section.
//
// A session used to resolve one provider — the main llm section — and share it
// with every worker. configuration.json has three more slots the REST surface
// already read/wrote but the runtime never consumed:
//
//   - queen_llm.<queen_id>: a queen's own model, used when a chat session is
//     opened with her (upstream queen_orchestrator: "if queen_models.has_own(
//     session.queen_name): session.llm = queen_models.build(...)").
//   - worker_llm: a dedicated worker model; without it workers share the
//     queen's provider (upstream SessionManager.build_worker_llm returns None).
//   - vision_fallback: the caption model for a text-only main model (wired by
//     the chat runtime, see newVisionCaptioner).
//
// Parity: framework/server/queen_models.py::build,
// framework/server/session_manager.py::SessionManager.build_worker_llm,
// framework/config.py::get_worker_api_key / get_worker_api_base /
// get_worker_llm_extra_kwargs

import (
	"os"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/visionfallback"
)

// queenAwareChatProvider builds the provider one session's queen runs on: her
// own queen_llm section when she has one, otherwise the main model. A queen
// picked only after the first turn is switched in place by the queen routes'
// apply hook, so this covers the up-front cases (a chat opened with her, a
// channel, a resumed session).
func queenAwareChatProvider(home hivehome.Home, requestedModel, queenID string) (llm.Completer, error) {
	section := hiveserver.QueenLLMSection(home, queenID)
	if section == nil {
		return chatProviderFactory(home, requestedModel)
	}
	return slotProvider(section, mainSlotAPIKey(home)), nil
}

// workerChatProvider builds the dedicated worker provider, or (nil, nil) when
// no worker_llm section is configured: workers then share the queen's provider,
// exactly as upstream's build_worker_llm documents.
func workerChatProvider(home hivehome.Home) (llm.Completer, error) {
	section := hiveserver.WorkerLLMSection(home)
	if section == nil {
		return nil, nil
	}
	return slotProvider(section, mainSlotAPIKey(home)), nil
}

// slotProvider renders a model section into the HTTP transport. Key and base
// resolution live in the server package so the REST surface and the runtime
// cannot disagree about which key a slot uses.
func slotProvider(section map[string]any, mainKey func() string) *llm.Provider {
	provider := llm.NewHTTPProvider(llm.ProviderOptions{
		Model:   hiveserver.SectionModel(section),
		APIKey:  hiveserver.SectionAPIKey(section, mainKey),
		APIBase: hiveserver.SectionAPIBase(section),
	})
	if extra := slotExtraKwargs(section); extra != nil {
		provider.ExtraKwargs = extra
	}
	return provider
}

// slotExtraKwargs mirrors upstream's per-slot extra kwargs: num_ctx for an
// ollama model, otherwise the section's extra_body (a provider's thinking
// switch, reasoning_effort). The subscription header branches are absent: the
// Go port has no subscription-authenticated provider, and upstream's
// queen_models.build explicitly does not support them either.
func slotExtraKwargs(section map[string]any) map[string]any {
	if provider, _ := section["provider"].(string); strings.EqualFold(strings.TrimSpace(provider), "ollama") {
		return map[string]any{"num_ctx": hiveserver.SectionNumCtx(section)}
	}
	if extra := hiveserver.SectionExtraBody(section); extra != nil {
		return map[string]any{"extra_body": extra}
	}
	return nil
}

// mainSlotAPIKey is the main llm slot's key as a lazy resolver: the literal
// api_key, its api_key_env_var, the provider's environment variable, then
// HIVE_API_KEY. It exists so a slot without its own credentials falls back to
// the main model's key (upstream's get_api_key tail) without forcing the main
// provider to build first — a queen whose key lives only in her own section
// must work even when the main slot has none.
func mainSlotAPIKey(home hivehome.Home) func() string {
	llmSection, _ := hiveserver.HiveConfig(home)["llm"].(map[string]any)
	literal := configString(llmSection, "api_key")
	provider := strings.ToLower(configString(llmSection, "provider"))
	if provider == "" || provider == "custom" {
		provider = providerForModel(configString(llmSection, "model"))
	}
	envVar := configString(llmSection, "api_key_env_var")
	if envVar == "" {
		envVar = chatProviderEnvVars[provider]
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

// visionCaptionerFor builds the vision-fallback captioner for a session whose
// main model cannot see images: a text-only model's tool-result images are
// captioned (viewport coordinates included) by the configured vision_fallback
// model instead of being sent to a model that cannot read them.
//
// Nil in the three cases upstream also declines: no vision_fallback slot, a
// main model that already accepts image tool results, or a slot with no
// resolvable key (upstream logs "configured but no API key resolved; skipping").
//
// Parity: framework/agent_loop/internals/vision_fallback.py::caption_tool_image,
// framework/agent_loop/agent_loop.py::_vision_fallback_active
func visionCaptionerFor(home hivehome.Home, main llm.Completer) *visionfallback.Captioner {
	mainModel := ""
	if namer, ok := main.(llm.ModelNamer); ok {
		mainModel = namer.ModelName()
	}
	if mainModel == "" || llm.SupportsImageToolResults(mainModel) {
		return nil
	}
	section := hiveserver.VisionFallbackSection(home)
	if section == nil {
		return nil
	}
	mainKey := mainSlotAPIKey(home)
	if hiveserver.SectionAPIKey(section, mainKey) == "" {
		return nil
	}
	return visionfallback.New(
		slotProvider(section, mainKey),
		hiveserver.SectionModel(section),
		hiveserver.VisionFallbackMaxTokens(section),
	)
}
