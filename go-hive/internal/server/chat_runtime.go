package server

// Parity: framework/server/routes_execution.py::deliver_to_queen (queen node runtime)

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hiveclock"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// chatStreamID and chatNodeID are the upstream queen identifiers: the
// dashboard's session SSE channel only treats a stream as client-facing when
// it is the queen stream.
const (
	chatStreamID = "queen"
	chatNodeID   = "queen"
)

// chatIdentityPrompt is the fallback identity for a chat session whose queen
// has no profile on disk (the shared "default" queen). A bound queen gets the
// formatted profile persona instead — see chatQueenIdentityPrompt.
const chatIdentityPrompt = "You are a queen agent in Hive, working directly with the" +
	" user in an interactive chat session. Answer the user, and use the available" +
	" tools when they help. Prefer doing the work over describing it. Be concise."

var (
	// ErrChatRuntimeNoLLM is returned when a runtime is built without a model.
	ErrChatRuntimeNoLLM = errors.New("server: no LLM configured for chat")
	// ErrChatRuntimeNoToolExecutor guards against advertising tools the
	// session cannot actually run.
	ErrChatRuntimeNoToolExecutor = errors.New("server: chat runtime has tools but no tool executor")
	// ErrChatRuntimeNoStore is returned when neither a conversation store nor
	// a root directory is supplied, which would silently drop the transcript.
	ErrChatRuntimeNoStore = errors.New("server: chat runtime needs a conversation store or root")
	// ErrChatRuntimeClosed is returned by Deliver after Close.
	ErrChatRuntimeClosed = errors.New("server: chat runtime is closed")
)

// ChatRuntimeDeps are injected so internal/server never imports a concrete tool
// package (nor builds providers): the loader owns those adapters.
type ChatRuntimeDeps struct {
	LLM   llm.Completer
	Tools []llm.ToolDef
	// Execute runs one tool call as text. It stays the compatibility path for
	// callers that never produce images.
	Execute func(ctx context.Context, name string, input map[string]any) (content string, isError bool, err error)
	// ExecuteBlocks supersedes Execute when set: it also returns the
	// provider-neutral content blocks (image_url for images, file for PDFs) a
	// tool returned, so attach_file attachments reach the model as real
	// multimodal parts instead of text.
	ExecuteBlocks func(ctx context.Context, name string, input map[string]any) (content string, isError bool, imageContent []map[string]any, err error)
	// ToolProber and ToolKiller are the tool executor's liveness handles for
	// the shared MCP connection pool. Execute/ExecuteBlocks are plain
	// closures and cannot carry methods, so the loader passes the registry
	// executor here: a tool that times out is probed and, when the server is
	// provably dead, force-disconnected instead of left unknown. Nil keeps
	// upstream's fastest path — probe reads "unknown" and nothing is
	// disconnected.
	//
	// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
	// executor.kill_for_tool wired onto the tool executor)
	ToolProber tools.LivenessProber
	ToolKiller tools.ToolKiller
	Store      agentloop.ConversationStore
	Root       string
	// Bus is the session's event bus. When set, the runtime mirrors the
	// upstream agent loop's emissions (execution / loop lifecycle, assistant
	// text, tool calls, per-LLM-call usage) so the dashboard's session SSE
	// channel and /events/history can follow the turn instead of hanging on
	// "Working…" until the next reload.
	Bus *eventbus.EventBus
	// SessionID scopes published execution ids and the session event log.
	SessionID string
	// QueenName scopes recalled memory and reflection to this queen. Empty
	// means the shared "default" queen.
	QueenName string
	// HomeRoot is HIVE_HOME. When set, the runtime recalls global + queen
	// memory each turn and schedules memory reflection after turns.
	HomeRoot string
	// Channel reports the chat channel this session belongs to: "queens" for
	// a queen-mail thread, "" for the dashboard (upstream Session.channel).
	// Read at each reminder render, so a channel that claims a live session
	// (queen mail does; upstream ChannelHost._claim) reaches the next turn.
	//
	// Parity: framework/server/queen_orchestrator.py (chat_app_provider)
	Channel func() string
	// IdleNudge overrides the idle-nudge budgets the session hub starts with.
	// Nil uses the loop's session_idle_nudge_* defaults.
	IdleNudge *agentloop.IdleNudgeConfig
	// InitialInputPending marks a create that delivers a prompt immediately
	// after construction (initial_prompt). The cold-resume mid-turn guard is
	// then suppressed: upstream only parks a restored mid-turn session when
	// the first iteration drained no user injection. False keeps the guard on.
	InitialInputPending bool
	// SkillsCatalog supplies the queen's rendered skills catalog for the
	// skills reminder. Nil (or an empty return) keeps that source silent.
	SkillsCatalog func() string
	// SuccessCriteria is retained as the generic chat-runtime criteria seam.
	// The production queen spec sets SkipJudge=true (upstream queen_orchestrator
	// and the static queen node both do), so a queen turn is never judged.
	SuccessCriteria *string
	// Colony is the session's live colony runtime. When set (at construction
	// or later through Session.BindChatColony), the queen gains run_worker and
	// every worker report arrives as a [WORKER_REPORT] user turn.
	Colony ColonyRuntime
	// OnTurnStart runs when a turn begins from a person, a timer or a worker
	// report (never for a letter another queen sent, and never for a
	// reminder-only wake). Queen mail uses it to start a new chain of letters,
	// as upstream does when someone else speaks in the conversation.
	OnTurnStart func()
	// VisionCaptioner captions tool-result images when the main model cannot
	// accept image blocks (vision_fallback). Nil leaves images to the media
	// gate, which tells the model an attachment was not delivered.
	VisionCaptioner agentloop.VisionCaptioner
	// AlwaysOnToolNames is the ordered always-on model surface of the flat
	// lazy layer (Python's first-request tools[] order). Empty keeps the
	// pre-lazy behavior: the whole catalogue is advertised, with only MCP
	// tools deferred.
	//
	// Parity: framework/agents/queen/queen_tools_defaults.py::always_enabled_tool_names
	AlwaysOnToolNames []string
	// DeferredTools are the flat definitions search_tools can load for this
	// session. They are announced by name only (searchable-tools reminder)
	// until loaded; grouped integration modules are not part of this layer.
	//
	// Parity: framework/tools/tool_tiers.py::ToolTierState.get_searchable_tools
	DeferredTools []llm.ToolDef
	// ManifestToolNames is the Python reference's per-queen allowlist, which
	// bounds what the searchable-tools manifest may announce (upstream
	// announces allowed − eager; for queen_growth that is chart_render and
	// web_scrape). It narrows the ANNOUNCEMENT only: every DeferredTool stays
	// loadable through search_tools, so the wider Go lazy layer keeps working.
	// ManifestFiltered marks the list authoritative — including an empty one,
	// which is a queen whose allowlist leaves nothing to announce. Nil with
	// ManifestFiltered false is the allow-all fall-through (announce the whole
	// deferred layer).
	//
	// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
	ManifestToolNames []string
	ManifestFiltered  bool
	// MCPToolNamesAll is the phase's MCP-origin tool-name universe: every tool
	// registered by the in-process harness groups plus any connected MCP
	// server. It is upstream's QueenPhaseState.mcp_tool_names_all — the set
	// the per-queen allowlist gates and the live-tools diagnostic counts.
	MCPToolNamesAll []string
	// Phase reports the queen's current phase ("independent" / "colony") for the
	// node_loop_iteration payload, mirroring the upstream loop's
	// iteration_metadata_provider. Nil leaves the iteration phase-less.
	Phase func() string
	// Session is the live session record this runtime serves. It is optional
	// (tests and embedded hosts build runtimes without one); when set, the
	// colony pivot intercept stashes its rich payload on it, mirroring
	// upstream's session.pending_colony_pivot, which the accept route
	// (POST /api/sessions with colony_id + source_session_id) and the dismiss
	// route read.
	Session *Session
	// Credentials reports the accounts connected in the session's home. It
	// renders into the static prompt's ambient "# Credentials" block (provider
	// names and counts, as upstream's summary does). Nil means "nothing
	// connected": the block still renders, so she knows the credentials tool
	// exists. The loader injects the same adapter the credential routes read.
	Credentials func() []map[string]any
	// PinnedCredentials renders the full per-account detail of the credentials
	// this session pinned with credentials(action=attach). It is re-read on
	// every prompt build, so an attach/detach shows up on the next model
	// request instead of waiting for a restart. Nil keeps the summary-only
	// block (embedded hosts and tests).
	//
	// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
	PinnedCredentials func() string
	// MCPCLI reports the command-line MCP servers configured in the registry
	// ("hive mcp run …" list). It renders into the static prompt right after
	// the credentials block. Nil (or an empty return) drops the segment, which
	// is what upstream sees when the registry has no CLI-mode server.
	MCPCLI func() string
}

// chatTurnRequest is one queued turn: a real client message, an external
// event (a worker report) the runtime delivers as a non-client user message,
// a temporal reminder the hub parked while the session was idle, or a
// framework trigger (timer tick / webhook hit).
type chatTurnRequest struct {
	message     string
	clientInput bool
	// recordOnly appends the user turn to the transcript without running the
	// model. It is how a message relayed to the colony overseer still lands
	// in the session history: the queue keeps the conversation single-writer.
	// external marks a record the front end did not author (the overseer's own
	// terminal report): it is stored as an "[External event]" turn instead of
	// a client input, and still runs no model turn. externalAssistant records
	// the message as the overseer's assistant reply instead of a user turn,
	// so a restarted session rebuilds the reply it saw live.
	recordOnly        bool
	external          bool
	externalAssistant bool
	// reminderWake is a message-less turn that only drains reminders the hub
	// parked (idle nudge). It never answers a pending ask_user question.
	reminderWake bool
	// triggers are the framework TriggerEvents that woke this turn. A trigger
	// turn carries no message: the events render into one batched user
	// message (upstream's drain_trigger_queue merges every queued trigger so
	// the model reasons about them atomically), and never count as client
	// input. A non-trigger turn leaves the slice empty.
	triggers []agentloop.TriggerEvent
	// fromQueenMail marks a letter or reply another queen sent. It enters as a
	// client message (so she sees who spoke) but does not start a new chain:
	// the depth of the letter it answers continues.
	fromQueenMail bool
	// opening is the upstream queen loop's first iteration: a session starts
	// its persistent loop immediately and runs one real model turn before any
	// user message, then parks on turn_done. It carries no user turn and no
	// execution/node-loop envelope — the session-start block already wrote the
	// loop's iteration 0 announcement.
	opening bool
	// correlationID is the id published on the CLIENT_INPUT_RECEIVED event for
	// a client message. The commit event upstream pairs with it is
	// client_input_committed, which carries the same id so listeners can tell
	// which message was committed where.
	correlationID string
	// images carries the user message's prepared content blocks (image_url /
	// file) into the transcript, mirroring upstream's
	// inject_event(..., image_content=...) payload.
	images []map[string]any
}

// ChatRuntime is the per-session queen: one AgentLoop, one conversation, one
// worker goroutine that drains accepted user messages in arrival order. A run
// of queued triggers is drained as one batched turn (see coalesceQueuedTurn).
type ChatRuntime struct {
	deps ChatRuntimeDeps

	loop     *agentloop.AgentLoop
	agentCtx *agentloop.AgentContext
	conv     *agentloop.NodeConversation
	store    agentloop.ConversationStore
	// tools is deps.Tools plus the ask_user synthetic the queen calls when she
	// needs a decision from the user.
	tools []llm.ToolDef

	bus       *eventbus.EventBus
	sessionID string
	// runtimeLogger is the three-level runtime log sink (L1 summary.json /
	// L2 details.jsonl / L3 tool_logs.jsonl). Non-nil only for session-shaped
	// roots (see newChatRuntimeLogger); nil keeps the pre-logs behavior for
	// tests and ad-hoc directories.
	runtimeLogger       *runtimelogs.RuntimeLogger
	runtimeLogStartedAt time.Time
	model               string
	// pinnedLLM is the session provider pinned for the current turn. A
	// model-slot PUT rebuilds the session's ProviderSlot in place; the pin
	// keeps every iteration of an in-flight turn on the provider the turn
	// started with, and the next turn resolves the replacement.
	pinnedLLM llm.Completer
	// streamLLM is non-nil when the configured provider can stream. It wraps
	// pinnedLLM for the agent loop and publishes per-frame client_output_delta
	// events; auxiliary callers keep using deps.LLM directly.
	streamLLM *chatStreamingCompleter

	queenName string
	// appliedQueenIdentity is the identity segment currently baked into the
	// system prompt. A persona edit that lands while a turn is running is
	// deferred (RefreshQueenIdentity refuses to write mid-turn); the turn
	// boundary compares the phase state's profile against this and rebases the
	// prompt, so the edit reaches the next model request instead of being
	// dropped.
	appliedQueenIdentity string
	globalMemoryDir      string
	queenMemoryDir       string
	reflection           *memory.ReflectionScheduler
	// shutdownGrace is the one budget Close gives the shutdown reflection and
	// the timeline flush (chatShutdownGrace); tests shrink it so the budget is
	// observable without waiting six seconds.
	shutdownGrace time.Duration
	// contextCompactedUnsub removes the session-bus subscription that turns a
	// completed compaction into the queen's long memory reflection.
	contextCompactedUnsub func()
	// memoryLLM resolves the memory-work model (reflection, dated timeline)
	// at each call: the worker_llm slot while memory_reflection.model is
	// "worker", else this session's provider.
	memoryLLM llm.Completer
	// lastRecall is the block injected last turn; recall is re-injected only
	// when its content changes (same guard as the upstream _last_injected_recall).
	lastRecall string
	// recallMu guards the two cached scope blocks left for the next turn's
	// delivery. They stay separate as upstream keeps them
	// (phase_state._cached_global_recall_block / _cached_queen_recall_block):
	// the session-start seed writes the global field only, so a seed landing
	// after the first refresh cannot drop the queen block. The pick runs off
	// the turn goroutine (upstream's _bg_refresh task) and only the turn
	// goroutine reads the cache, so this lock is the handoff.
	recallMu          sync.Mutex
	recallGlobalCache string
	recallQueenCache  string
	// timelineEnabled is set when the memory_timeline feature is on for this
	// home; timelineAfterTurn then extracts new user turns in the background.
	timelineEnabled bool
	timelineMu      sync.Mutex
	lastTimelineRun time.Time
	// timelineWG tracks the timeline writers (startup backfill and the
	// after-turn extractor). Close waits on it: a write that outlives the
	// runtime races the session directory's removal on fork/stop.
	timelineWG sync.WaitGroup
	// relayCatalogSent records that the skills catalog rode along with the
	// first client message relayed to the colony overseer. The overseer owns
	// execution there, so the chat loop's reminder hub never runs for it; the
	// catalog has to travel with the relayed input instead.
	relayCatalogMu   sync.Mutex
	relayCatalogSent bool
	lastRelayBlock   string
	// hub is the loop's reminder hub (one per session). Sources are registered
	// once at construction; the temporal idle ticker is started there and
	// stopped by Close.
	hub        *agentloop.ReminderHub
	idleSource *agentloop.IdleNudgeSource
	// toolTier is the session's MCP tool split (nil when the session has no
	// MCP tools). The queen tool-library routes rebuild it through
	// ApplyQueenMCPToolAllowlist, so the next turn advertises the new surface.
	toolTier *chatToolTier
	// unregisterReminders removes this runtime's hub from the REST reminder
	// mirror when the session closes.
	unregisterReminders func()
	// sentinelSource is the parked-queen ladder (nudge / done / escalate)
	// registered on the hub when a process-wide Sentinel manager exists.
	sentinelSource *sentinel.EscalationSource

	// Reminder signals: the ticker reads these through reminderSignals so the
	// idle nudge sees the same park/idle state the upstream loop publishes.
	lastProgress atomic.Int64
	turnActive   atomic.Bool
	firstEvent   atomic.Bool
	awaiting     atomic.Bool
	// userStopped is the explicit Stop flag. The cancel-queen route sets it
	// before cancelling the turn, the idle-nudge gate reads it through
	// reminderSignals, and the next real client input clears it.
	userStopped atomic.Bool

	// turnMu guards the in-flight turn's cancellation handle. The run
	// goroutine installs it for the duration of runTurn; the Stop route
	// cancels through it and waits on turnDone for the unwind. turnSeq keeps
	// a finishing turn from clearing a handle a newer turn already owns.
	turnMu     sync.Mutex
	turnSeq    uint64
	turnCtx    context.Context
	turnCancel context.CancelFunc
	turnDone   chan struct{}
	// openingStarted guards StartOpeningTurn so a session's first iteration is
	// queued at most once.
	openingStarted atomic.Bool
	parkMu         sync.Mutex
	parkReason     agentloop.ParkReason
	// parkStoreMu serialises the read-modify-write of the conversation
	// cursor's pending_input record. The queue goroutine and the colony event
	// bridge both persist park state, and an unguarded RMW would drop one
	// side's write.
	parkStoreMu sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	queue   []chatTurnRequest
	closed  bool
	lastErr error

	// colonyMu guards the session colony binding. The binding is late: the
	// chat runtime is built before a session's colony host is attached.
	colonyMu      sync.Mutex
	colonyRuntime *colony.ColonyRuntime
	colonyUnsub   func()
	// colonyBridgeUnsub mirrors the bound colony's lifecycle events onto the
	// session bus; colonyBridgeStream guards against re-subscribing the same
	// runtime on every session lookup.
	colonyBridgeUnsub  func()
	colonyBridgeStream string
	bridgeMu           sync.Mutex
	// pendingQuestions is the ask_user question the current turn parked on.
	// It survives until the next user message (the answer) starts a turn.
	pendingQuestions []map[string]any
	// pendingCredentialForm is the secure form the current turn parked on after
	// a tool reported a missing credential (upstream: credentials(collect)).
	// It survives until the next user message/trigger, like pendingQuestions.
	pendingCredentialForm map[string]any
	// pendingColonySuggestion is the suggest_colony payload the current turn
	// parked on (the DM "Create Colony" popup). It survives until the next
	// user message (the dismissal) starts a turn, and is persisted so a
	// restarted runtime re-announces the popup.
	//
	// Parity: framework/agent_loop/agent_loop.py:823-825 (_pending_colony_suggestion)
	pendingColonySuggestion map[string]any
	// pendingColonyPivot is the task_create(new_colony=true) payload the
	// current turn parked on. The same payload is stashed on Session
	// (session.pending_colony_pivot) for the accept/dismiss routes.
	//
	// Parity: framework/agent_loop/agent_loop.py:829-831 (_pending_colony_pivot)
	pendingColonyPivot map[string]any
	// Implicit judge ledger: upstream publishes turn N's verdict when the park
	// unblocks (the next message starts the turn that announces it). The chat
	// runtime remembers the finished turn and settles it at the next turn
	// start. verdictIteration is the session turn index (opening turn = 0).
	verdictMu          sync.Mutex
	verdictPending     bool
	verdictIteration   int
	verdictExecutionID string
	turnIndex          int
	// overseerTranscript folds the colony overseer's client-facing reply
	// frames into one assistant message per turn for the chat transcript.
	// The overseer runs on the colony bus, so this bridge is the only path
	// that can persist its reply in the session conversation.
	overseerTranscript overseerTranscriptRecorder

	wake chan struct{}
	done chan struct{}
}

func chatRuntimeStore(deps ChatRuntimeDeps) (agentloop.ConversationStore, error) {
	if deps.Store != nil {
		return deps.Store, nil
	}
	if deps.Root == "" {
		return nil, ErrChatRuntimeNoStore
	}
	return agentloop.NewFileConversationStore(filepath.Join(deps.Root, "conversations")), nil
}

// NewChatRuntime builds the session runtime and starts its worker. Its phases
// run in a fixed order — base state, agent context, loop collaborators, then
// session reminder/log wiring — because their side effects are order-sensitive.
func NewChatRuntime(deps ChatRuntimeDeps) (*ChatRuntime, error) {
	if deps.LLM == nil {
		return nil, ErrChatRuntimeNoLLM
	}
	if len(deps.Tools) > 0 && deps.Execute == nil && deps.ExecuteBlocks == nil {
		return nil, ErrChatRuntimeNoToolExecutor
	}
	store, err := chatRuntimeStore(deps)
	if err != nil {
		return nil, err
	}
	runtime, restoredPark := newChatRuntimeBase(deps, store)
	agentCtx := newChatAgentContext(runtime, deps)
	runtime.attachRuntimeLogger(deps, agentCtx)
	systemPrompt := chatQueenStaticPrompt(deps, runtime.queenName)

	conv, err := restoreChatConversation(store, systemPrompt)
	if err != nil {
		runtime.cancel()
		return nil, err
	}
	runtime.agentCtx = agentCtx
	runtime.conv = conv
	// The identity segment baked into this session's static prompt: a persona
	// edit rebases against it at the next turn boundary.
	runtime.appliedQueenIdentity = agentCtx.IdentityPrompt
	runtime.wireChatLoop(deps)
	runtime.wireChatSession(deps, restoredPark)

	go runtime.run()
	return runtime, nil
}

// newChatRuntimeBase builds the runtime's zero-turn state: store/bus bindings,
// the pinned/streaming/memory model slots, and the restored parks.
// restoredPark reports a cold-interrupt park the caller must re-announce.
func newChatRuntimeBase(deps ChatRuntimeDeps, store agentloop.ConversationStore) (*ChatRuntime, bool) {
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &ChatRuntime{
		deps:      deps,
		store:     store,
		bus:       deps.Bus,
		sessionID: deps.SessionID,
		tools:     chatToolsWithAskUser(deps.Tools),
		ctx:       ctx,
		cancel:    cancel,
		wake:      make(chan struct{}, 1),
		done:      make(chan struct{}),
		// Upstream's server-wide shutdown grace is the cap on everything a
		// session starts as it stops (finish_background_tasks(6s)).
		shutdownGrace: chatShutdownGrace,
	}
	runtime.markProgress()
	// The session's transport may be a ProviderSlot (a model-slot PUT
	// rebuilds it). Pin the current provider for the turn machinery: the
	// agent loop reads agentCtx.LLM per iteration, so holding the slot there
	// would let a PUT tear a running turn.
	runtime.pinnedLLM = llm.ResolveCompleter(deps.LLM)
	if runtime.pinnedLLM == nil {
		runtime.pinnedLLM = deps.LLM
	}
	runtime.streamLLM = newChatStreamingCompleter(runtime.pinnedLLM)
	// Parity: framework/server/session_manager.py::_MemoryLLM.current
	runtime.memoryLLM = deps.LLM
	if root := strings.TrimSpace(deps.HomeRoot); root != "" {
		runtime.memoryLLM = newChatMemoryLLM(deps.LLM, hivehome.Home{Root: root},
			func() memory.ReflectionConfig { return chatMemoryReflectionConfig(root) })
	}
	// A session that has not run a turn is parked at turn_done: without this
	// the idle ticker would auto-start an untouched session after the
	// between-turns budget. A persisted pending_input (an unanswered ask_user
	// question, or a broken turn) restores the wait instead of forgetting it;
	// a restart over a mid-turn conversation restores the cold-interrupt park
	// (upstream §6b1½), so the in-flight work is never silently resumed.
	restoredPark := runtime.restoreParkForNewRuntime(deps.InitialInputPending)
	// The explicit-stop flag outlives the process: the idle-nudge gate must
	// stay closed on a restarted session until the user's next real message.
	//
	// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
	runtime.restoreUserStopped()
	runtime.queenName = strings.TrimSpace(deps.QueenName)
	if runtime.queenName == "" {
		runtime.queenName = "default"
	}
	// Memory scopes only exist under a HIVE_HOME; without one the runtime keeps
	// its pre-memory behaviour (no recall, no reflection).
	runtime.configureMemoryScopes()
	return runtime, restoredPark
}

// newChatAgentContext builds the queen's agent context. The loop completer is
// the streaming wrapper so every iteration passes the stream watchdog.
func newChatAgentContext(runtime *ChatRuntime, deps ChatRuntimeDeps) *agentloop.AgentContext {
	loopLLM := runtime.pinnedLLM
	if runtime.streamLLM != nil {
		loopLLM = runtime.streamLLM
	}
	// The queen's output budget follows the main model's configuration:
	// llm.max_tokens, else the model catalog's entry, else 8192 — upstream's
	// AgentContext(max_tokens=lc.get("max_tokens", get_max_tokens())).
	maxTokens := chatTurnMaxTokens(deps.HomeRoot)
	return &agentloop.AgentContext{
		AgentID: "queen",
		AgentSpec: agentloop.AgentSpec{
			ID:        "queen",
			Name:      "Queen",
			AgentType: "event_loop",
			// The queen is a forever-alive conversational agent. Upstream sets
			// skip_judge=True on both the runtime queen spec
			// (queen_orchestrator.py) and the static queen node, so no queen
			// turn is evaluated by the loop judge — including a colony-bound
			// queen. SuccessCriteria is carried for the generic AgentSpec
			// contract but does not arm the judge.
			SkipJudge:       true,
			SuccessCriteria: deps.SuccessCriteria,
		},
		LLM:            loopLLM,
		AvailableTools: runtime.tools,
		IdentityPrompt: chatQueenIdentityPrompt(deps.HomeRoot, runtime.queenName),
		InputData:      map[string]any{},
		StreamID:       "queen",
		MaxTokens:      maxTokens,
		// The chat-app gate: upstream wires ``chat_app_provider=lambda:
		// session.channel``, so a channel session (queen mail's "queens"
		// thread, or a chat-app channel once one runs) is known as such by
		// the tasks reminder source.
		ChatAppProvider: chatAppProviderFromChannel(deps.Channel),
		// The live phase gates the framework-level colony dispatch
		// (suggest_colony in independent, task_create(new_colony=true) in
		// colony). Reads through the same provider the emitter stamps on
		// node_loop_iteration.
		Phase: deps.Phase,
		// The colony pivot sink stashes the rich payload on the session while
		// the tool call is still being dispatched, so the accept route can
		// never race the popup event.
		PivotPayloadSink: runtime.sinkColonyPivot,
	}
}

// attachRuntimeLogger wires the three-level runtime logs (L1 summary.json /
// L2 details.jsonl / L3 tool_logs.jsonl): session-shaped roots
// (<storage>/sessions/<id>) get the store upstream's
// RuntimeLogStore(.../runtime_logs) feeds, keyed by the session id. The loop
// writes L3/L2 through AgentContext.RuntimeLogger as it runs; Close writes the
// L1 summary.
func (r *ChatRuntime) attachRuntimeLogger(deps ChatRuntimeDeps, agentCtx *agentloop.AgentContext) {
	if logger := newChatRuntimeLogger(deps.Root, deps.SessionID); logger != nil {
		r.runtimeLogger = logger
		r.runtimeLogStartedAt = time.Now()
		agentCtx.RuntimeLogger = logger
	}
}

// restoreChatConversation loads the transcript, seeding a new one with the
// static prompt; an existing transcript has its prompt rebased instead.
func restoreChatConversation(store agentloop.ConversationStore, systemPrompt string) (*agentloop.NodeConversation, error) {
	conv, err := agentloop.RestoreConversation(store, nil, nil)
	if err != nil {
		return nil, err
	}
	if conv == nil {
		conv = agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
			SystemPrompt: systemPrompt,
			Store:        store,
		})
	} else {
		conv.UpdateSystemPrompt(systemPrompt, nil)
	}
	return conv, nil
}

// wireChatLoop builds the turn loop and attaches the collaborators it reads
// per iteration: the restored cold-resume cursor, the replay/stream events,
// the MCP probe pair, the vision captioner, the compaction bridge, the stream
// watchdog budgets, the stall-nudge sink and the criteria judge.
func (runtime *ChatRuntime) wireChatLoop(deps ChatRuntimeDeps) {
	runtime.loop = agentloop.NewAgentLoop(chatLoopConfig(deps.Root, deps.HomeRoot, runtime.queenName), runtime.executeTool)
	// Cold-resume state owned by the loop: the cumulative tool-call count (so
	// the lifetime budget caps across restarts instead of refilling) and the
	// turn-level doom fingerprint window. Restoring replaces the fresh loop's
	// zero state with the cursor's, once, before any turn runs.
	//
	// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
	// Parity: framework/agent_loop/agent_loop.py::execute (restored counters)
	if err := runtime.loop.RestoreCursorState(runtime.conv); err != nil {
		log.Printf("chat runtime %s: restore loop cursor state: %v", deps.SessionID, err)
	}
	// P0-14: the loop's soft replay tier publishes tool_call_replay_detected
	// for a cross-turn repeat. Hand it the session bus so the event reaches
	// SSE and events.jsonl instead of dying on a nil writer.
	//
	// Parity: framework/host/event_bus.py::emit_tool_call_replay_detected
	if runtime.bus != nil {
		runtime.loop.ReplayEvents = runtime.bus
	}
	// A timed-out tool call must probe the shared MCP connection before the
	// loop gives up on it: the executor closures above cannot carry methods,
	// so the loader's registry handles ride the loop directly (same pair
	// ExecuteTool uses on its own timeout path).
	//
	// Parity: framework/agent_loop/internals/tool_result_handler.py (timeout
	// handler: probe_for_tool / kill_for_tool)
	runtime.loop.ToolProber = deps.ToolProber
	runtime.loop.ToolKiller = deps.ToolKiller
	// A text-only main model cannot see tool images: with vision_fallback
	// configured, each image is captioned (viewport coordinates included) and
	// dropped before the model sees the result.
	runtime.loop.VisionCaptioner = deps.VisionCaptioner
	// Bridge the loop's completed compaction to the session bus (reflection).
	runtime.configureContextCompacted()
	// The streaming completer owns the LLM stream watchdog; its budgets come
	// from the loop config (upstream wires them from the same LoopConfig).
	if runtime.streamLLM != nil {
		ttft, interEvent := streamWatchdogBudgets(runtime.loop.Config)
		runtime.streamLLM.setStreamWatchdog(ttft, interEvent)
	}
	// A continue-nudge is a session event, not just a conversation write:
	// publish the watchdog verdict, the nudge and the reminder-injected event.
	runtime.loop.OnStreamStallNudge = runtime.emitStreamStallDiagnostics
}

// wireChatSession performs the post-loop wiring: late colony binding, the
// reminder sources and their hub (before the hub binds its agent context), the
// timeline, the REST mirror registration, the model label and session event
// log, then the re-announcement of a restored park once that log exists.
func (r *ChatRuntime) wireChatSession(deps ChatRuntimeDeps, restoredPark bool) {
	if deps.Colony != nil {
		r.BindColony(deps.Colony)
	}
	// Register the per-session reminder sources and start the hub's idle
	// ticker; Close stops it with the rest of the session. The Sentinel
	// escalation source is one of them: configureReminders owns the hub's
	// membership and registers it in upstream's slot, before the hub binds its
	// agent context (binding filters the consulted sources, so a source added
	// afterwards would never be polled).
	r.configureReminderProviders()
	r.configureReminders(deps.IdleNudge)
	r.startTimeline()
	// Publish the session's loop hub for the REST reminder mirror. The mirror
	// is read-only; the loop remains the only consumer that drains it.
	r.unregisterReminders = registerSessionReminderHub(r.sessionID, r.hub)

	if namer, ok := deps.LLM.(llm.ModelNamer); ok {
		r.model = namer.ModelName()
	}
	// The dashboard's /events/history reads <session dir>/events.jsonl — the
	// same file the bus appends every published event to. Wiring it here keeps
	// the live SSE channel and the reload history consistent.
	if r.bus != nil && deps.Root != "" {
		if err := r.bus.SetSessionLog(filepath.Join(deps.Root, "events.jsonl"), 0); err != nil {
			log.Printf("chat runtime %s: session event log unavailable: %v", deps.SessionID, err)
		}
	}
	if restoredPark {
		// A restarted bus carries no history: re-announce the restored wait
		// now that the session log is attached, so the snapshot endpoint and
		// the SSE first frame project the parked state (and /events/history
		// keeps the announcement).
		r.announceRestoredPark()
	}
}

// Close stops the worker, cancels the in-flight turn, drains any background
// memory reflection (with a final pass so the last turns are persisted), and
// releases the store. Everything stopping the session starts — the final short
// reflection and the timeline flush — shares one bounded grace
// (chatShutdownGrace, upstream's finish_background_tasks(6s) at server exit);
// past it the work is cancelled rather than waited on, and Close still blocks
// until both goroutines have exited so nothing writes after it returns.
func (r *ChatRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	r.colonyMu.Lock()
	if r.colonyUnsub != nil {
		r.colonyUnsub()
		r.colonyUnsub = nil
	}
	r.colonyRuntime = nil
	r.colonyMu.Unlock()
	r.syncColonyEventBridge(nil)

	r.cancel()
	<-r.done
	// Drop any uncollected background-tool handles: no later turn can collect
	// them. ToolExecutor has no cancel hook, so in-flight work runs to its own
	// budget and its result is discarded when it lands.
	//
	// Parity: framework/agent_loop/agent_loop.py::AgentLoop._background_calls
	if r.loop != nil {
		r.loop.ReleaseBackgroundTools()
	}
	// Upstream's stop_session flushes the timeline after the turn loop is
	// quiescent, so messages that arrived inside the live coalescing window are
	// not lost. It starts the flush fire-and-forget; the port runs it here so a
	// stopped session's directory is complete before teardown returns, sharing
	// one bounded budget with the final reflection (see below).
	//
	// Parity: core/framework/server/session_manager.py::SessionManager.stop_session
	grace := r.shutdownGrace
	if grace <= 0 {
		grace = chatShutdownGrace
	}
	// One budget for everything stopping a session starts, the way
	// finish_background_tasks gives one 6s grace to every task stop_session
	// spawned. Both paths honour the shared context, and Close still waits for
	// their goroutines to exit below, so its promise holds: once Close returns,
	// nothing writes into the session directory any more.
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), grace)
	var shutdownWG sync.WaitGroup
	if r.timelineEnabled && r.deps.LLM != nil && r.deps.Root != "" {
		shutdownWG.Add(1)
		go func() {
			defer shutdownWG.Done()
			_, _ = memory.UpdateSessionTimeline(shutdownCtx, r.deps.LLM, r.deps.Root)
		}()
	}
	// The timeline writers run on their own goroutines and write into the
	// session directory. Waiting here keeps Close's promise that nothing writes
	// after it returns (fork/stop then remove that directory).
	r.timelineWG.Wait()
	// Parity: framework/orchestrator/orchestrator.py::execute (end_run): the
	// session's run is over — read L2 back and write the L1 summary.
	r.finishRuntimeLogs()
	// The reminder ticker is owned by the session: stop it before tearing the
	// conversation down so it cannot park (or wake) anything afterwards.
	if r.hub != nil {
		r.hub.Stop()
	}
	if r.unregisterReminders != nil {
		r.unregisterReminders()
		r.unregisterReminders = nil
	}
	// Parity: core/framework/server/session_manager.py::SessionManager.stop_session
	// Teardown cancels the queen's reflection triggers and runs the final
	// reflection: reflections run on the scheduler's own context, so it waits
	// for one already in flight, cancels it when the shared budget is spent, and
	// then runs the final pass with whatever the budget still holds.
	if r.contextCompactedUnsub != nil {
		r.contextCompactedUnsub()
		r.contextCompactedUnsub = nil
	}
	if r.reflection != nil {
		shutdownWG.Add(1)
		go func() {
			defer shutdownWG.Done()
			r.reflection.Shutdown(shutdownCtx)
		}()
	}
	// Both shutdown paths exit on the shared context: after this wait nothing
	// writes into the session directory, and no goroutine is left behind.
	shutdownWG.Wait()
	stopShutdown()
	if r.bus != nil {
		// Flushes any pending delta snapshots and releases the session log fd.
		r.bus.CloseSessionLog()
	}
	if r.store == nil {
		return nil
	}
	return r.store.Close()
}

func (r *ChatRuntime) run() {
	defer close(r.done)
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			closed := r.closed
			r.mu.Unlock()
			if closed {
				return
			}
			select {
			case <-r.wake:
				// A temporal reminder (idle nudge) may have parked while the
				// worker slept. Run it as an energized, message-less turn so
				// the model acts on it instead of leaving it unread.
				if r.hub != nil && r.hub.PendingLen() > 0 {
					r.runTurn(chatTurnRequest{reminderWake: true})
				}
				continue
			case <-r.ctx.Done():
				return
			}
		}
		message, rest := coalesceQueuedTurn(r.queue)
		r.queue = rest
		r.mu.Unlock()
		r.runTurn(message)
	}
}

// coalesceQueuedTurn removes the queue's leading turn. A leading trigger takes
// every trigger that follows it contiguously: upstream's drain_trigger_queue
// reads the whole queue as one batch, so N triggers queued at the same wake
// become a single user message. A client message ahead of them keeps its own
// turn — the drain order is the arrival order, so a trigger run never absorbs
// (or reorders past) a client input.
//
// The caller holds r.mu; the returned queue is the remainder.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func coalesceQueuedTurn(queue []chatTurnRequest) (chatTurnRequest, []chatTurnRequest) {
	if len(queue) == 0 {
		return chatTurnRequest{}, queue
	}
	head := queue[0]
	if len(head.triggers) == 0 {
		return head, queue[1:]
	}
	end := 1
	for end < len(queue) && len(queue[end].triggers) > 0 {
		end++
	}
	if end == 1 {
		return head, queue[1:]
	}
	batch := chatTurnRequest{triggers: make([]agentloop.TriggerEvent, 0, end)}
	for _, request := range queue[:end] {
		batch.triggers = append(batch.triggers, request.triggers...)
	}
	return batch, queue[end:]
}

// runTurn mirrors the upstream drain: the user message joins the conversation
// stamped with its arrival time, per-queen recall rides along as a
// <system-reminder>, then the loop runs until the model stops calling tools.
// Each step is mirrored onto the session event bus with the upstream
// agent-loop event names so the dashboard follows the turn live. A
// reminderWake turn has no user message: it drains a reminder the idle ticker
// parked and runs the model so the agent acts on it.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
func (r *ChatRuntime) runTurn(request chatTurnRequest) {
	// Every turn runs under its own context so the Stop route can abort just
	// this turn's stream. The handle lives until runTurn returns: the deferred
	// endTurn closes the done channel the route waits on after the emitter and
	// park cleanup below have finished.
	turnCtx, turnSeq := r.beginTurn()
	defer r.endTurn(turnSeq)
	if request.opening {
		r.runOpeningTurn(turnCtx)
		return
	}
	// A persona edit that landed while the previous turn was running reaches
	// this turn's system prompt here, before anything reads it (upstream reads
	// the phase identity per turn).
	r.rebaseQueenIdentityFromPhaseState()
	r.pinTurnProvider()
	// A real client message lifts an explicit Stop: the loop is about to run a
	// turn on this message, so it is no longer "stopped". Upstream clears
	// _user_stopped as the event arrives, before it is queued.
	//
	// Parity: framework/agent_loop/agent_loop.py::AgentLoop.inject_event
	if request.clientInput {
		r.clearUserStopped()
	}
	if request.recordOnly {
		r.recordOnlyTurn(request)
		return
	}
	message := request.message
	// The session-start recall seed fires on the first message of a fresh
	// session (the create-session initial prompt) — the string the upstream
	// session_start hook receives as its trigger. The check must precede the
	// SESSION_START reminder write below, which already makes the
	// conversation non-empty.
	firstSessionMessage := request.clientInput && r.conv.MessageCount() == 0
	if request.reminderWake {
		message = "Reminder"
	}
	// Someone other than a correspondent spoke: the chain of letters starts
	// again here. A letter or reply she is answering keeps its depth.
	if r.deps.OnTurnStart != nil && !request.fromQueenMail && !request.reminderWake {
		r.deps.OnTurnStart()
	}
	executionID := newChatExecutionID()
	emitter := r.newTurnEmitter(executionID)
	if emitter != nil {
		emitter.executionStarted(message)
		r.agentCtx.ExecutionID = executionID
		r.agentCtx.Observer = emitter
		defer func() { r.agentCtx.Observer = nil }()
	}
	// A streaming provider publishes its text as frames while the loop's LLM
	// call is in flight. Without a live bus there is no consumer, so the
	// non-streaming path stays in charge.
	if r.streamLLM != nil && emitter != nil {
		r.streamLLM.beginTurn(r.conv.SystemPrompt(), emitter.StreamTextDelta)
		// A 402 on this turn's stream publishes payment_required on the
		// session feed under the turn's execution, so the client can reopen
		// the upgrade popup instead of parsing execution_failed.
		r.streamLLM.setPaymentRequiredSink(r.bus, chatStreamID, executionID)
		defer r.streamLLM.endTurn()
	}

	stamp := hiveclock.Now().Format("2006-01-02 15:04 MST")
	if len(request.triggers) > 0 {
		// A trigger turn is delivered as upstream's drained trigger batch: one
		// shared timestamp header over every event queued at this wake, so
		// the block carries its own stamp and is inserted verbatim rather than
		// taking the "[stamp] [External event]" prefix.
		message = formatTriggerBatch(request.triggers, stamp)
	}
	r.markTurnStart()
	defer r.markTurnEnd()
	// A real user message or a trigger answers any outstanding ask_user park:
	// both unblock the pending-input wait upstream (`_await_user_input` clears
	// its stash when the wait returns), so clear it before this turn can
	// record a new question. An external report is not an answer, so it
	// leaves the parked question in place.
	if request.clientInput || len(request.triggers) > 0 {
		r.clearPendingQuestions()
		r.clearPendingCredentialForm()
		r.clearPendingColony()
	}
	if request.clientInput {
		// Parity: framework/agent_loop/agent_loop.py::inject_event — a real
		// user message re-arms the idle nudge's per-variant caps and closes an
		// open Sentinel escalation for this session.
		if r.idleSource != nil {
			r.idleSource.Reset()
		}
		r.notifySentinelLocalResume()
	}
	if request.reminderWake {
		if !r.injectHubReminders() {
			emitter.executionCompleted(0, string(agentloop.ParkReasonTurnDone), nil)
			r.setParkState(agentloop.ParkReasonTurnDone, true)
			return
		}
		r.emitPendingImplicitJudgeVerdict()
	} else {
		// Upstream publishes the previous turn's judge verdict as its park
		// unblocks — before this message's reminder frame and commit.
		r.emitPendingImplicitJudgeVerdict()
		// Parity: framework/agent_loop/agent_loop.py::execute — SESSION_START
		// fires before the first user message so the situational frame
		// precedes the request.
		sessionStart := r.conv.MessageCount() == 0
		if sessionStart {
			r.fireReminderPoint(agentloop.SESSION_START, message, nil)
		}
		// Parity: framework/agent_loop/agent_loop.py::_drain_injection_queue
		// — USER_PROMPT_SUBMIT fires before the queued user message is added.
		// A trigger is drained separately upstream, so it never fires this
		// point (there is no user prompt to frame).
		//
		// The first turn's text already reached SESSION_START; upstream's
		// pending-user-text queue only carries queued client messages, so the
		// same text must not be replayed here (that would render the
		// past-conversations source twice).
		userText := message
		if sessionStart {
			userText = ""
		}
		if len(request.triggers) == 0 {
			r.fireReminderPoint(agentloop.USER_PROMPT_SUBMIT, userText, nil)
		}
		if !r.commitTurnUserMessage(message, stamp, request, emitter, executionID) {
			return
		}
		// The ticker may have parked a reminder while the session waited for
		// this message; drain it into the turn before the model reads it.
		r.injectHubReminders()
	}
	// Recall is a user-input step upstream; an external worker report must not
	// consume (or re-query) the queen's memory. Reminders still ride along.
	if request.clientInput {
		r.injectRecall(message, stamp)
		if firstSessionMessage {
			// The session's first message also runs the seed, the way the
			// upstream session_start hook does before the first model call.
			// It lands after the delivery above, so message 1 itself stays
			// unseeded.
			r.seedRecall(message)
		}
	}
	result, err := r.loop.ExecuteTurn(turnCtx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		r.handleTurnError(turnCtx, err, result, emitter)
		return
	}
	if !result.Success {
		err := errors.New("server: chat turn did not complete")
		parkReason := turnFailureParkReason(result, agentloop.ParkReasonUnknown)
		emitter.executionFailed(err, parkReason)
		r.setParkState(parkReason, true)
		r.setLastError(err)
		return
	}
	// Parity: framework/agent_loop/agent_loop.py::_await_user_input — a turn
	// that asked the user a question parks on ask_user, not turn_done, so the
	// snapshot and idle nudge treat it as an external wait. The framework
	// colony dispatch outranks both: a suggest_colony or
	// task_create(new_colony=true) call parks on the popup, whose event is the
	// rich colony_suggestion_requested rather than client_input_requested.
	parkReason := r.turnParkReason(result, emitter)
	emitter.executionCompleted(result.InnerTurns, string(parkReason), r.parkQuestionsFor(parkReason))
	r.noteImplicitJudgeVerdictDue(executionID)
	// Parity: framework/agent_loop/agent_loop.py::execute — STOP fires after a
	// text-only turn (no tool result can carry a tail).
	if len(result.ToolResults) == 0 {
		r.fireReminderPoint(agentloop.STOP, "", nil)
	}
	r.setParkState(parkReason, true)
	// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
	if r.reflection != nil {
		r.reflection.OnTurnDone(r.ctx, "queen", "turn_done")
	}
	r.timelineAfterTurn()
	r.setLastError(nil)
}

// recordOnlyTurn appends a message the overseer already executed so the
// transcript keeps the ask, without running the chat queen again. The
// overseer's own reply is an assistant record, not a second user turn.
func (r *ChatRuntime) recordOnlyTurn(request chatTurnRequest) {
	if request.externalAssistant {
		r.recordExternalAssistant(request.message)
		return
	}
	if request.external {
		r.recordExternalTurn(request.message)
		return
	}
	r.recordRelayedClientTurn(request.message, request.images)
}

// handleTurnError classifies a failed turn execution. A runtime teardown, a
// Stop cancellation and a loop-raised park each end the turn without a
// failure frame; only a genuine failure publishes execution_failed and
// records the error.
func (r *ChatRuntime) handleTurnError(turnCtx context.Context, err error, result agentloop.TurnResult, emitter *chatTurnEmitter) {
	// A cancelled turn (session teardown) is not a failure worth
	// broadcasting: the runtime is going away, not the model failing.
	if r.ctx.Err() != nil {
		r.setLastError(err)
		return
	}
	// The Stop route cancelled only this turn. Upstream parks the loop on
	// user_stopped and waits for the next message instead of failing the
	// execution, so announce that park and leave lastErr alone.
	//
	// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (_turn_cancelled)
	if turnCtx.Err() != nil {
		emitter.executionParked(agentloop.ParkReasonUserStopped, nil)
		r.setParkState(agentloop.ParkReasonUserStopped, true)
		return
	}
	// A loop-raised park is a designed stop, not a failure: the empty
	// response guard and the deterministic doom loop end the turn with
	// a reason-tagged error, and upstream announces that wait on the
	// bus (client_input_requested) instead of failing the execution.
	//
	// Parity: framework/agent_loop/agent_loop.py::_await_user_input
	if parkReason, parked := loopParkReason(err); parked {
		emitter.executionParked(parkReason, nil)
		r.setParkState(parkReason, true)
		return
	}
	// A loop-raised park (the deterministic doom loop) outranks the
	// generic failure reason: the event and the runtime park state must
	// name the same reason the TurnResult carries.
	parkReason := turnFailureParkReason(result, agentloop.ParkReasonLLMError)
	emitter.executionFailed(err, parkReason)
	r.setParkState(parkReason, true)
	r.setLastError(err)
}

// commitTurnUserMessage is runTurn's user-message commit step: it stamps the
// message for this turn, inserts it into the conversation and, for a real
// client message, announces the exact seq it landed at. It reports false when
// the commit failed after publishing the failure (execution_failed frame, park
// state and last error), so runTurn aborts the turn.
func (r *ChatRuntime) commitTurnUserMessage(message, stamp string, request chatTurnRequest, emitter *chatTurnEmitter, executionID string) bool {
	stamped := "[" + stamp + "] " + message
	switch {
	case len(request.triggers) > 0:
		// formatTriggerBatch already stamped the block, matching the
		// upstream drain which inserts the batch verbatim.
		stamped = message
	case !request.clientInput:
		stamped = "[" + stamp + "] [External event] " + message
	}
	committedMessage, err := r.conv.AddUserMessage(stamped, agentloop.UserMessageOptions{
		IsClientInput: request.clientInput,
		IsTrigger:     len(request.triggers) > 0,
		ImageContent:  request.images,
	})
	if err != nil {
		emitter.executionFailed(err, agentloop.ParkReasonUnknown)
		r.setParkState(agentloop.ParkReasonUnknown, true)
		r.setLastError(err)
		return false
	}
	// A real client message announces the exact conversation seq it landed
	// at, so the UI places the bubble where the conversation has it (the
	// upstream CLIENT_INPUT_COMMITTED boundary; see emit docstring). An
	// external report or trigger is not a client input and never emits it.
	if request.clientInput && emitter != nil {
		var correlation *string
		if request.correlationID != "" {
			correlation = &request.correlationID
		}
		r.bus.EmitClientInputCommitted(chatStreamID, chatNodeID, &executionID, committedMessage.Seq, correlation)
	}
	return true
}

// StartOpeningTurn runs the upstream queen loop's first iteration for a
// freshly created session: before any user message, the loop announces itself,
// runs one real model turn and parks on turn_done. The session-start block
// (emitSessionStartEvents) supplies the loop's opening announcements; this
// adds the LLM frames and the park boundary a subscriber sees as
// session_snapshot -> client_output_delta -> llm_turn_complete ->
// client_input_requested, exactly as the Python queen does on creation.
//
// It is idempotent and only ever queues once per runtime; resumed sessions
// (logs already carrying events) never call it.
func (r *ChatRuntime) StartOpeningTurn() {
	if r == nil {
		return
	}
	if r.openingStarted.Swap(true) {
		return
	}
	_ = r.enqueueTurn(chatTurnRequest{opening: true})
}

// runOpeningTurn is runTurn for the message-less first iteration. It has no
// user turn to add: the loop's SESSION_START reminder is the first message the
// model reads, which is what gives a fresh session an opening reply upstream.
// ctx is the runTurn per-turn context, so the Stop route aborts this iteration
// the same way it aborts a message turn.
func (r *ChatRuntime) runOpeningTurn(ctx context.Context) {
	// Same turn-boundary rebase as runTurn: the opening iteration is the first
	// model request of the session, so a profile materialized after
	// construction must reach it.
	r.rebaseQueenIdentityFromPhaseState()
	r.pinTurnProvider()
	// Upstream runs the queen loop under the session id as the execution id;
	// the turn carries no per-execution envelope, but the park events keep it.
	executionID := r.sessionID
	if executionID == "" {
		executionID = newChatExecutionID()
	}
	emitter := r.newTurnEmitter(executionID)
	if emitter != nil {
		emitter.opening = true
		r.agentCtx.ExecutionID = executionID
		r.agentCtx.Observer = emitter
		defer func() { r.agentCtx.Observer = nil }()
	}
	if r.streamLLM != nil && emitter != nil {
		r.streamLLM.beginTurn(r.conv.SystemPrompt(), emitter.StreamTextDelta)
		r.streamLLM.setPaymentRequiredSink(r.bus, chatStreamID, executionID)
		defer r.streamLLM.endTurn()
	}

	r.markTurnStart()
	defer r.markTurnEnd()

	// The loop's first iteration announces the session frame before reading
	// the model; the event itself was written with the session-start block, so
	// only the conversation reminder is injected here.
	if r.conv.MessageCount() == 0 {
		r.fireReminderPoint(agentloop.SESSION_START, "", nil)
	}

	result, err := r.loop.ExecuteTurn(ctx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		if r.ctx.Err() != nil {
			r.setLastError(err)
			return
		}
		// The Stop route cancelled this iteration: park on user_stopped, the
		// same way runTurn's counterpart does, without a failure frame.
		//
		// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop (_turn_cancelled)
		if ctx.Err() != nil {
			emitter.executionParked(agentloop.ParkReasonUserStopped, nil)
			r.setParkState(agentloop.ParkReasonUserStopped, true)
			return
		}
		// A loop-raised park announces the wait, not a failure (upstream's
		// _await_user_input); see the runTurn counterpart.
		if parkReason, parked := loopParkReason(err); parked {
			emitter.executionParked(parkReason, nil)
			r.setParkState(parkReason, true)
			return
		}
		parkReason := turnFailureParkReason(result, agentloop.ParkReasonLLMError)
		emitter.executionFailed(err, parkReason)
		r.setParkState(parkReason, true)
		r.setLastError(err)
		return
	}
	if !result.Success {
		err := errors.New("server: chat turn did not complete")
		parkReason := turnFailureParkReason(result, agentloop.ParkReasonUnknown)
		emitter.executionFailed(err, parkReason)
		r.setParkState(parkReason, true)
		r.setLastError(err)
		return
	}
	parkReason := r.turnParkReason(result, emitter)
	emitter.executionCompleted(result.InnerTurns, string(parkReason), r.parkQuestionsFor(parkReason))
	r.noteImplicitJudgeVerdictDue(executionID)
	if len(result.ToolResults) == 0 {
		r.fireReminderPoint(agentloop.STOP, "", nil)
	}
	r.setParkState(parkReason, true)
	if r.reflection != nil {
		r.reflection.OnTurnDone(r.ctx, "queen", "turn_done")
	}
	r.timelineAfterTurn()
	r.setLastError(nil)
}

// parkQuestionsFor returns the questions the park boundary must announce: only
// an ask_user park carries them (upstream passes `questions` to
// _await_user_input only on that branch).
func (r *ChatRuntime) parkQuestionsFor(parkReason agentloop.ParkReason) []map[string]any {
	if r == nil || parkReason != agentloop.ParkReasonAskUser {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneParkQuestions(r.pendingQuestions)
}

// newChatExecutionID returns the per-turn execution id the loop and the
// published events carry. Parity: framework/host/execution_manager.py uses a
// fresh uuid per queen execution.
func newChatExecutionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("exec-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// injectHubReminders drains reminders the hub parked (the idle ticker) into the
// conversation as <system-reminder> blocks. It runs on the turn goroutine, so
// conversation writes never race the loop.
//
// Parity: framework/agent_loop/agent_loop.py::_drain_reminder_hub
func (r *ChatRuntime) injectHubReminders() bool {
	if r == nil || r.hub == nil || r.conv == nil {
		return false
	}
	items := r.hub.TakePending()
	if len(items) == 0 {
		return false
	}
	injected := false
	for _, item := range items {
		block := agentloop.WrapReminder([]string{item.Body})
		if block == "" {
			continue
		}
		if _, err := r.conv.AddUserMessage(block, agentloop.UserMessageOptions{IsSystemReminder: true}); err != nil {
			continue
		}
		injected = true
	}
	return injected
}

func (r *ChatRuntime) setLastError(err error) {
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
}

func (r *ChatRuntime) executeTool(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	// A completed tool call is forward progress: feed the idle clock so the
	// ticker does not nudge a session that is actively working.
	r.markProgress()
	if req.Name == "ask_user" {
		return r.executeAskUser(req.Input), nil
	}
	// The single CLI-style credentials tool (help/collect/attach/detach). It
	// runs here because collect parks the loop and attach/detach rebase the
	// session's credentials block — both need runtime state the tool registry
	// does not see.
	//
	// Parity: framework/agent_loop/agent_loop.py (credentials branch)
	if req.Name == tools.CredentialsToolName {
		return r.executeCredentialsTool(req.Input), nil
	}
	if r.mcpToolBlocked(req.Name) {
		return mcpToolRefusal(req.Name), nil
	}
	if r.deps.Execute == nil && r.deps.ExecuteBlocks == nil {
		return agentloop.ToolResult{
			Content: fmt.Sprintf("No executor registered for tool '%s'", req.Name),
			IsError: true,
		}, nil
	}
	var (
		content      string
		isError      bool
		imageContent []map[string]any
		err          error
	)
	if r.deps.ExecuteBlocks != nil {
		content, isError, imageContent, err = r.deps.ExecuteBlocks(r.toolExecuteContext(), req.Name, req.Input)
	} else {
		content, isError, err = r.deps.Execute(r.toolExecuteContext(), req.Name, req.Input)
	}
	if err != nil {
		return agentloop.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	// A missing-credential failure is not a dead end: park the turn on a secure
	// credential form so the user can supply the key (upstream's
	// credentials(collect) park), rather than only reporting an error.
	if annotated, parked := r.parkForMissingCredential(req.Name, content); parked {
		content = annotated
	}
	return agentloop.ToolResult{Content: content, IsError: isError, ImageContent: imageContent}, nil
}

// BindColony attaches (or clears) this session's colony runtime. Attaching
// subscribes the queen to the colony's SUBAGENT_REPORT events, which arrive as
// [WORKER_REPORT] turns; detaching unsubscribes. It is idempotent and safe to
// call from session lookup paths.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (subscribe)
