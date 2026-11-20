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
// The session transports are wrapped in *llm.ProviderSlot so the model-slot
// routes can rebuild them without restarting serve or duplicating the
// provider logic.
//
// Parity: framework/server/queen_models.py::build,
// framework/server/session_manager.py::SessionManager.build_worker_llm,
// framework/config.py::get_worker_api_key / get_worker_api_base /
// get_worker_llm_extra_kwargs

import (
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/visionfallback"
)

// newModelSlot wraps a live session transport so a model-slot write can
// rebuild it in place. The rebuild factory is the same NewHTTPProvider
// constructor the session started on.
func newModelSlot(initial llm.Completer) *llm.ProviderSlot {
	return llm.NewProviderSlot(initial, llm.HTTPProviderFactory)
}

// queenAwareChatProvider builds the provider one session's queen runs on: her
// own queen_llm section when she has one, otherwise the main model. A queen
// picked only after the first turn is switched in place by the queen routes'
// apply hook, so this covers the up-front cases (a chat opened with her, a
// channel, a resumed session).
func queenAwareChatProvider(home hivehome.Home, requestedModel, queenID string) (llm.Completer, error) {
	section := hiveserver.QueenLLMSection(home, queenID)
	if section == nil {
		provider, err := chatProviderFactory(home, requestedModel)
		if err != nil {
			return nil, err
		}
		return newModelSlot(provider), nil
	}
	return newModelSlot(hiveserver.SectionProvider(home, section)), nil
}

// workerChatProvider builds the dedicated worker provider, or (nil, nil) when
// no worker_llm section is configured: workers then share the queen's provider,
// exactly as upstream's build_worker_llm documents.
func workerChatProvider(home hivehome.Home) (llm.Completer, error) {
	section := hiveserver.WorkerLLMSection(home)
	if section == nil {
		return nil, nil
	}
	return newModelSlot(hiveserver.SectionProvider(home, section)), nil
}

// visionCaptionerFor builds the vision-fallback captioner for a session whose
// main model cannot see images: a text-only model's tool-result images are
// captioned (viewport coordinates included) by the configured vision_fallback
// model instead of being sent to a model that cannot read them.
//
// The slot is resolved before every caption, so a PUT for vision_fallback
// reaches the next tool image without a session rebuild (upstream reads the
// config per caption_tool_image call). A slot with no resolvable key reports
// no caption and the loop drops the images with the exhausted marker
// (upstream logs "configured but no API key resolved; skipping").
//
// Parity: framework/agent_loop/internals/vision_fallback.py::caption_tool_image,
// framework/agent_loop/agent_loop.py::_vision_fallback_active
func visionCaptionerFor(home hivehome.Home) *visionfallback.Captioner {
	return visionfallback.NewResolver(func() (llm.Completer, string, int, bool) {
		section := hiveserver.VisionFallbackSection(home)
		if section == nil {
			return nil, "", 0, false
		}
		if hiveserver.SectionAPIKey(section, hiveserver.MainSlotAPIKey(home)) == "" {
			return nil, "", 0, false
		}
		return hiveserver.SectionProvider(home, section),
			hiveserver.SectionModel(section),
			hiveserver.VisionFallbackMaxTokens(section),
			true
	})
}
