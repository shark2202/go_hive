package server

// Turn-boundary plumbing: the provider pin and persona rebase a turn starts
// with, and the error -> park-reason mapping its outcome publishes. Split out
// of chat_runtime.go to keep the runtime file inside the repository's frozen
// size budget; the code is unchanged.

import (
	"errors"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// pinTurnProvider resolves the session's provider slot at the start of a turn.
// A model-slot PUT rebuilds the slot in place; resolving here is what makes
// the change "session-level": the turn that was already running kept its
// pinned provider, and the first turn started afterwards runs on the rebuilt
// one. The run goroutine is the only writer of agentCtx.LLM, so no lock is
// needed beyond the slot's own.
//
// Parity: framework/server/queen_models.py::apply,
// framework/server/routes_config.py::_hot_swap_sessions (the reconfigure half;
// the pin keeps an in-flight turn on the provider it started with).
func (r *ChatRuntime) pinTurnProvider() {
	pinned := llm.ResolveCompleter(r.deps.LLM)
	if pinned == nil || pinned == r.pinnedLLM {
		return
	}
	r.pinnedLLM = pinned
	r.streamLLM = newChatStreamingCompleter(pinned)
	if r.streamLLM != nil && r.loop != nil {
		ttft, interEvent := streamWatchdogBudgets(r.loop.Config)
		r.streamLLM.setStreamWatchdog(ttft, interEvent)
	}
	var loopLLM llm.Completer = pinned
	if r.streamLLM != nil {
		loopLLM = r.streamLLM
	}
	r.agentCtx.LLM = loopLLM
	if namer, ok := pinned.(llm.ModelNamer); ok {
		r.model = namer.ModelName()
	} else {
		r.model = ""
	}
}

// rebaseQueenIdentityFromPhaseState applies a persona edit that landed while a
// turn was running. A profile save re-materializes the phase state's queen
// profile and re-publishes QUEEN_IDENTITY_SELECTED, but the conversation's
// system prompt is a baked static block: RefreshQueenIdentity rebases it only
// on an idle session, because the turn goroutine is the conversation's only
// writer. Upstream reads the identity from the phase state on every turn
// (`get_static_prompt`), so a mid-turn edit reaches the next AgentLoop turn —
// this is that boundary. The turn goroutine calls it, so the write can never
// race the running turn; an unchanged identity is a no-op.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
// Parity: framework/server/routes_queens.py::_refresh_live_queen_identity
func (r *ChatRuntime) rebaseQueenIdentityFromPhaseState() {
	if r == nil || r.conv == nil || r.agentCtx == nil || r.deps.Session == nil {
		return
	}
	phase, ok := r.deps.Session.PhaseState.(interface{ QueenProfile() map[string]any })
	if !ok || phase == nil {
		return
	}
	profile := phase.QueenProfile()
	if profile == nil {
		// A session that never materialized an identity keeps the one it was
		// built with; there is no profile to rebase onto.
		return
	}
	identity := chatQueenIdentityFromProfile(r.deps.HomeRoot, r.queenName, profile)
	if identity == r.appliedQueenIdentity {
		return
	}
	r.agentCtx.IdentityPrompt = identity
	r.conv.UpdateSystemPrompt(chatQueenStaticPromptWithIdentity(r.deps, identity), nil)
	r.appliedQueenIdentity = identity
}

// loopParkReason reports the park an error names, when the loop raised that
// park itself instead of failing. Both loop guards that end a turn by design
// (the empty-response ghost stream and the deterministic doom loop) return a
// typed error carrying the reason; upstream's _await_user_input publishes the
// wait off exactly that reason. Ordinary errors (LLM failures, persistence
// errors) carry none and keep the failure path.
//
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (reason)
func loopParkReason(err error) (agentloop.ParkReason, bool) {
	var carrier interface{ ParkReason() agentloop.ParkReason }
	if !errors.As(err, &carrier) {
		return "", false
	}
	if reason := carrier.ParkReason(); reason != "" {
		return reason, true
	}
	return "", false
}

// turnFailureParkReason keeps a loop-raised park on a failed turn. The loop's
// TurnResult.ParkReason is set when the guard itself stopped the turn (the
// deterministic doom loop); a turn that merely errored leaves it nil and the
// caller's generic reason applies. Returning the carried reason keeps the
// published loop_state_changed, the persisted pending_input, and the
// TurnResult naming the same park.
func turnFailureParkReason(result agentloop.TurnResult, fallback agentloop.ParkReason) agentloop.ParkReason {
	if result.ParkReason != nil && *result.ParkReason != "" {
		return *result.ParkReason
	}
	return fallback
}
