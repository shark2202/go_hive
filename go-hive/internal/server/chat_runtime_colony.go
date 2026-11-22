package server

// Colony binding for the chat runtime: attaching, inheriting and rebinding a
// colony runtime. Split out of chat_runtime.go to keep that file inside the
// repository's size budget (no behavior change).

import (
	"log"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	"github.com/shark2202/go_hive/go-hive/internal/soul"
)

func (r *ChatRuntime) BindColony(runtime ColonyRuntime) {
	if r == nil {
		return
	}
	changed := r.bindColony(colonyRuntimeOf(runtime))
	// Mirror worker lifecycle events onto the session bus even when the
	// concrete runtime is unchanged (a session lookup re-invokes BindColony).
	r.syncColonyEventBridge(runtime)
	if changed {
		// The hub filters sources by the bound colony when it binds, so a late
		// attach (create-with-colony, load_colony, fork) must re-bind or the
		// colony-gated sources — parallel nudge, active workers, Sentinel
		// autopilot — would never be consulted.
		r.rebindReminderContext()
	}
}

// configureCriteriaJudge arms the loop judge for a chat session whose
// AgentSpec carries success criteria. No criteria (or no completer) leaves the
// judge off, so an ordinary chat turn still costs exactly one model call.
//
// Parity: framework/agent_loop/internals/judge_pipeline.py::judge_turn
func (r *ChatRuntime) configureCriteriaJudge() bool {
	if r == nil || r.loop == nil || r.agentCtx == nil {
		return false
	}
	return r.loop.ConfigureJudge(r.agentCtx.AgentSpec, r.deps.LLM, r.bus)
}

// inheritColonyCriteria gives a colony-bound queen the colony goal's criteria
// when the session arrived without any of its own. A plain chat session has no
// goal and keeps its judge off; a session created with a colony inherits the
// criteria its workers are judged against, so the queen's turn is evaluated
// the same way.
//
// Parity-exempt: Go-only inheritance seam. Upstream's queen spec carries an
// empty success_criteria list; this port derives the paragraph from the
// colony goal for a session bound to one.
func (r *ChatRuntime) inheritColonyCriteria(concrete *colony.ColonyRuntime) {
	if r == nil || r.agentCtx == nil || concrete == nil {
		return
	}
	if agentloop.HasSuccessCriteria(r.agentCtx.AgentSpec) {
		return
	}
	criteria := agentloop.GoalCriteriaText(concrete.Goal())
	if criteria == nil {
		return
	}
	r.agentCtx.AgentSpec.SuccessCriteria = criteria
	r.configureCriteriaJudge()
}

// bindColony stores the concrete runtime and reports whether it changed.
func (r *ChatRuntime) bindColony(concrete *colony.ColonyRuntime) bool {
	r.colonyMu.Lock()
	defer r.colonyMu.Unlock()
	if r.colonyRuntime == concrete {
		return false
	}
	if r.colonyUnsub != nil {
		r.colonyUnsub()
		r.colonyUnsub = nil
	}
	r.colonyRuntime = concrete
	// The binding is the first point a create-with-colony session can see its
	// goal, so the judge is armed here rather than waiting for a load pass.
	r.inheritColonyCriteria(concrete)
	if concrete == nil {
		r.setBoundColonyID(nil)
		return true
	}
	unsubscribe, err := colony.SubscribeQueenReportsWithWorker(concrete, func(workerID, report string) {
		// Reports never fail a session: a closed runtime (queen session
		// torn down while a worker finished) simply drops the turn.
		if overseerID := concrete.OverseerID(); overseerID != "" && workerID == overseerID {
			// The overseer's own terminal report is front-end bookkeeping, not
			// an execution input: its reply already reached the session as the
			// mirrored client stream, and it speaks in the same conversation the
			// user is reading. Waking the chat queen here ran a second model
			// turn to answer a report the user had just seen — the "extra
			// turn" from the second conversation. Record it for scroll-back
			// only; no loop runs.
			_ = r.recordOverseerTerminal(report)
			return
		}
		// With a live overseer the report is its input: one loop acts on it,
		// and the chat queen stays the session's front end.
		if r.relayWorkerReportToOverseer(report) {
			return
		}
		_ = r.InjectWorkerReport(report)
		// The persistent overseer is the colony's own lead: give it the same
		// report (except its own) so it is not a tool surface without input.
		r.submitWorkerReportToOverseer(workerID, report)
	})
	if err != nil {
		log.Printf("chat runtime %s: worker report subscription unavailable: %v", r.sessionID, err)
		r.setBoundColonyID(concrete)
		return true
	}
	r.colonyUnsub = unsubscribe
	r.setBoundColonyID(concrete)
	return true
}

// rebindReminderContext re-filters the hub's active sources against the live
// agent context. It runs after the colony binding changes; without it a source
// whose AppliesTo is colony-gated would stay excluded from the bound list.
func (r *ChatRuntime) rebindReminderContext() {
	if r == nil || r.hub == nil || r.agentCtx == nil {
		return
	}
	r.hub.Bind(agentloop.NewReminderAgentContext(r.agentCtx))
}

// setBoundColonyID refreshes the reminder context's colony id. The caller
// holds colonyMu, so this must not call boundColonyRuntime.
func (r *ChatRuntime) setBoundColonyID(runtime *colony.ColonyRuntime) {
	if r == nil || r.agentCtx == nil {
		return
	}
	if runtime == nil || runtime.StreamID() == "" {
		r.agentCtx.ColonyID = nil
		return
	}
	colonyID := runtime.StreamID()
	r.agentCtx.ColonyID = &colonyID
}

// boundColonyRuntime returns the concrete colony runtime whose workers this
// queen can dispatch, or nil when no colony is attached.
func (r *ChatRuntime) boundColonyRuntime() *colony.ColonyRuntime {
	if r == nil {
		return nil
	}
	r.colonyMu.Lock()
	defer r.colonyMu.Unlock()
	return r.colonyRuntime
}

// colonyRuntimeOf unwraps the session ColonyRuntime seam: the CLI's colony
// host exposes Runtime(), and tests may pass the concrete runtime directly.
func colonyRuntimeOf(runtime ColonyRuntime) *colony.ColonyRuntime {
	switch typed := runtime.(type) {
	case nil:
		return nil
	case *colony.ColonyRuntime:
		return typed
	case interface{ Runtime() *colony.ColonyRuntime }:
		return typed.Runtime()
	}
	return nil
}

// chatToolsWithAskUser returns the session's tool list plus the ask_user
// synthetic. The queen's loop is the only client-facing stream, so she always
// gets the question tool — even a session with no integration tools.
//
// Parity: framework/agent_loop/internals/synthetic_tools.py::build_ask_user_tool
func chatToolsWithAskUser(tools []llm.ToolDef) []llm.ToolDef {
	for _, tool := range tools {
		if tool.Name == "ask_user" {
			return tools
		}
	}
	out := make([]llm.ToolDef, 0, len(tools)+1)
	out = append(out, tools...)
	out = append(out, agentloop.BuildAskUserTool())
	return out
}

// chatQueenIdentityPrompt resolves the session queen's persona from her
// profile. Upstream formats the live identity with max_examples=0 so the
// roleplay examples stay out of the system prompt; a queen with no profile on
// disk (the shared "default" queen) keeps the generic chat identity.
//
// Parity: framework/server/queen_orchestrator.py::_queen_identity_hook
// Parity: framework/agents/queen/queen_profiles.py::format_queen_identity_prompt
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func chatQueenIdentityPrompt(homeRoot, queenName string) string {
	identity := chatIdentityPrompt
	if homeRoot != "" {
		profile, err := queen.LoadQueenProfile(hivehome.Home{Root: homeRoot}.QueensDir(), queenName)
		if err != nil {
			// The shared "default" queen ships no profile; that is expected. A
			// named queen that cannot load is worth one diagnostic line.
			if queenName != "default" {
				log.Printf("chat queen %s: profile unavailable, using the generic identity: %v", queenName, err)
			}
		} else {
			maxExamples := 0
			if prompt := strings.TrimSpace(queen.FormatQueenIdentityPrompt(profile, &maxExamples)); prompt != "" {
				identity = prompt
			}
		}
	}
	// Her soul rides the static prompt right under her identity, as upstream:
	// identity + soul + base prompt. It is not a per-turn reminder because it
	// changes at most once a day and belongs in the cached prefix.
	//
	// Parity: core/framework/agents/queen/soul.py::prompt_for
	if homeRoot != "" {
		home := hivehome.Home{Root: homeRoot}
		if soulPrompt := soul.PromptFor(HiveConfig(home), home.QueensDir(), queenName); soulPrompt != "" {
			identity = identity + "\n\n" + soulPrompt
		}
	}
	return identity
}

// sedimentSoul writes a finished reflection's durable conclusions into this
// queen's soul. Only the queen scope is read: global memories are shared by
// every queen, and a soul is one queen's own record. Nothing is written when
// the pass changed no memory, when she has no soul configured, or when her
// soul already carries every conclusion.
//
