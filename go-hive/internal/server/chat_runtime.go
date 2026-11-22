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

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hiveclock"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
	"github.com/aden-hive/hive-go/internal/runtimelogs"
	"github.com/aden-hive/hive-go/internal/sentinel"
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
	Store         agentloop.ConversationStore
	Root          string
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
	// SkillsCatalog supplies the queen's rendered skills catalog for the
	// skills reminder. Nil (or an empty return) keeps that source silent.
	SkillsCatalog func() string
	// SuccessCriteria is the optional success-criteria paragraph the loop
	// judge evaluates each turn against. Nil (or blank) keeps the judge off,
	// so a plain chat turn costs no extra model call. A colony-bound session
	// that arrives without it inherits the colony goal's criteria on bind.
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
	// Phase reports the queen's current phase ("independent" / "colony") for
	// the node_loop_iteration payload, mirroring the upstream loop's
	// iteration_metadata_provider. Nil leaves the iteration phase-less.
	Phase func() string
	// Credentials reports the accounts connected in the session's home. It
	// renders into the static prompt's ambient "# Credentials" block (provider
	// names and counts, as upstream's summary does). Nil means "nothing
	// connected": the block still renders, so she knows the credentials tool
	// exists. The loader injects the same adapter the credential routes read.
	Credentials func() []map[string]any
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
	// trigger is the framework TriggerEvent that woke this turn. Its payload
	// frames the user message; the event itself is never counted as client
	// input.
	trigger *agentloop.TriggerEvent
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
}

// ChatRuntime is the per-session queen: one AgentLoop, one conversation, one
// worker goroutine that drains accepted user messages in arrival order.
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

	queenName       string
	globalMemoryDir string
	queenMemoryDir  string
	reflection      *memory.ReflectionScheduler
	// memoryLLM resolves the memory-work model (reflection, dated timeline)
	// at each call: the worker_llm slot while memory_reflection.model is
	// "worker", else this session's provider.
	memoryLLM llm.Completer
	// lastRecall is the block injected last turn; recall is re-injected only
	// when its content changes (same guard as the upstream _last_injected_recall).
	lastRecall string
	// recallMu guards recallCache, the pick left for the next turn's delivery.
	// The pick runs off the turn goroutine (upstream's _bg_refresh task) and
	// only the turn goroutine reads the cache, so this lock is the handoff.
	recallMu    sync.Mutex
	recallCache string
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

func NewChatRuntime(deps ChatRuntimeDeps) (*ChatRuntime, error) {
	if deps.LLM == nil {
		return nil, ErrChatRuntimeNoLLM
	}
	if len(deps.Tools) > 0 && deps.Execute == nil && deps.ExecuteBlocks == nil {
		return nil, ErrChatRuntimeNoToolExecutor
	}
	store := deps.Store
	if store == nil {
		if deps.Root == "" {
			return nil, ErrChatRuntimeNoStore
		}
		store = agentloop.NewFileConversationStore(filepath.Join(deps.Root, "conversations"))
	}

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
	// Memory work (reflection, the dated timeline) resolves its model at each
	// call: the worker_llm slot while memory_reflection.model is "worker" (the
	// default) and one is configured, else this session's provider. A Settings
	// edit therefore reaches a running session on its next reflection.
	//
	// Parity: framework/server/session_manager.py::_MemoryLLM.current
	runtime.memoryLLM = deps.LLM
	if root := strings.TrimSpace(deps.HomeRoot); root != "" {
		runtime.memoryLLM = newChatMemoryLLM(deps.LLM, hivehome.Home{Root: root},
			func() memory.ReflectionConfig { return chatMemoryReflectionConfig(root) })
	}
	// A session that has not run a turn is parked at turn_done: without this
	// the idle ticker would auto-start an untouched session after the
	// between-turns budget. A persisted pending_input (an unanswered ask_user
	// question, or a broken turn) restores the wait instead of forgetting it.
	if !runtime.restoreParkState() {
		runtime.setParkState(agentloop.ParkReasonTurnDone, true)
	}
	runtime.queenName = strings.TrimSpace(deps.QueenName)
	if runtime.queenName == "" {
		runtime.queenName = "default"
	}
	// Parity: core/framework/server/queen_orchestrator.py::initialize_memory_scopes
	// Memory scopes only exist under a HIVE_HOME; without one the runtime keeps
	// its pre-memory behaviour (no recall, no reflection) instead of guessing.
	if deps.HomeRoot != "" {
		runtime.globalMemoryDir = memory.GlobalMemoryDir(deps.HomeRoot)
		runtime.queenMemoryDir = memory.QueenMemoryDir(deps.HomeRoot, runtime.queenName)
		_ = memory.InitMemoryDir(runtime.globalMemoryDir)
		_ = memory.InitMemoryDir(runtime.queenMemoryDir)
		// Parity: core/framework/agents/queen/reflection_agent.py::subscribe_reflection_triggers
		if deps.Root != "" {
			runtime.reflection = &memory.ReflectionScheduler{
				SessionDir: deps.Root,
				LLM:        runtime.memoryLLM,
				GlobalDir:  runtime.globalMemoryDir,
				QueenDir:   runtime.queenMemoryDir,
				QueenID:    runtime.queenName,
				Config:     func() memory.ReflectionConfig { return chatMemoryReflectionConfig(deps.HomeRoot) },
				// A finished pass sediments its conclusions into her soul.
				OnReflect: runtime.sedimentSoul,
			}
			runtime.reflection.InitializeReflectionState()
		}
	}

	loopLLM := runtime.pinnedLLM
	if runtime.streamLLM != nil {
		loopLLM = runtime.streamLLM
	}
	// The queen's output budget follows the main model's configuration:
	// llm.max_tokens, else the model catalog's entry, else 8192 — upstream's
	// AgentContext(max_tokens=lc.get("max_tokens", get_max_tokens())).
	maxTokens := chatTurnMaxTokens(deps.HomeRoot)
	agentCtx := &agentloop.AgentContext{
		AgentID: "queen",
		AgentSpec: agentloop.AgentSpec{
			ID:        "queen",
			Name:      "Queen",
			AgentType: "event_loop",
			// The judge only evaluates a spec that carries criteria; a plain
			// chat session leaves this nil and its loop Judge stays nil.
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
	}
	// Three-level runtime logs (L1 summary.json / L2 details.jsonl / L3
	// tool_logs.jsonl): session-shaped roots (<storage>/sessions/<id>) get the
	// store upstream's RuntimeLogStore(.../runtime_logs) feeds, keyed by the
	// session id. The loop writes L3/L2 through AgentContext.RuntimeLogger as
	// it runs; Close writes the L1 summary.
	if logger := newChatRuntimeLogger(deps.Root, deps.SessionID); logger != nil {
		runtime.runtimeLogger = logger
		runtime.runtimeLogStartedAt = time.Now()
		agentCtx.RuntimeLogger = logger
	}
	systemPrompt := chatQueenStaticPrompt(deps, runtime.queenName)

	conv, err := agentloop.RestoreConversation(store, nil, nil)
	if err != nil {
		cancel()
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

	runtime.agentCtx = agentCtx
	runtime.conv = conv
	runtime.loop = agentloop.NewAgentLoop(chatLoopConfig(deps.Root, deps.HomeRoot, runtime.queenName), runtime.executeTool)
	// A text-only main model cannot see tool images: with vision_fallback
	// configured, each image is captioned (viewport coordinates included) and
	// dropped before the model sees the result.
	runtime.loop.VisionCaptioner = deps.VisionCaptioner
	// The streaming completer owns the LLM stream watchdog; its budgets come
	// from the loop config (upstream wires them from the same LoopConfig).
	if runtime.streamLLM != nil {
		ttft, interEvent := streamWatchdogBudgets(runtime.loop.Config)
		runtime.streamLLM.setStreamWatchdog(ttft, interEvent)
	}
	// A continue-nudge is a session event, not just a conversation write:
	// publish the watchdog verdict, the nudge and the reminder-injected event.
	runtime.loop.OnStreamStallNudge = runtime.emitStreamStallDiagnostics
	// A session with configured criteria evaluates its turns; without them the
	// judge stays nil and the turn costs exactly one model call.
	runtime.configureCriteriaJudge()
	if deps.Colony != nil {
		runtime.BindColony(deps.Colony)
	}
	// Register the per-session reminder sources and start the hub's idle
	// ticker; Close stops it with the rest of the session.
	runtime.configureReminderProviders()
	// Sentinel registers before the hub binds its agent context: binding
	// filters the consulted sources, so a source added afterwards would never
	// be polled. The autopilot gate it sets is read by that same binding.
	runtime.configureSentinel()
	runtime.configureReminders(deps.IdleNudge)
	runtime.startTimeline()
	// Publish the session's loop hub for the REST reminder mirror. The mirror
	// is read-only; the loop remains the only consumer that drains it.
	runtime.unregisterReminders = registerSessionReminderHub(runtime.sessionID, runtime.hub)

	if namer, ok := deps.LLM.(llm.ModelNamer); ok {
		runtime.model = namer.ModelName()
	}
	// The dashboard's /events/history reads <session dir>/events.jsonl — the
	// same file the bus appends every published event to. Wiring it here keeps
	// the live SSE channel and the reload history consistent.
	if runtime.bus != nil && deps.Root != "" {
		if err := runtime.bus.SetSessionLog(filepath.Join(deps.Root, "events.jsonl"), 0); err != nil {
			log.Printf("chat runtime %s: session event log unavailable: %v", deps.SessionID, err)
		}
	}

	go runtime.run()
	return runtime, nil
}

// Deliver queues a user message and wakes the worker. It never blocks on a
// running turn: turns are serialized by the queue, not by the caller. On a
// colony-bound session with a live overseer the message is relayed to it
// instead: the overseer is the session's executor and the chat queen its front
// end, so exactly one loop acts on the user's request.
func (r *ChatRuntime) Deliver(message string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if r.relayClientInputToOverseer(message) {
		// The overseer owns the turn; the session transcript still records the
		// ask. The record rides the queue so the conversation keeps a single
		// writer, and a closed runtime only loses the record, not the relay.
		recorded := message
		if block := r.takeRelayCatalogBlock(); block != "" {
			recorded = block + "\n\n" + message
		}
		_ = r.enqueueTurn(chatTurnRequest{message: recorded, clientInput: true, recordOnly: true})
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: message, clientInput: true})
}

// DeliverClientInput is Deliver for the HTTP chat route: the correlation id
// the handler published on CLIENT_INPUT_RECEIVED rides the turn so the
// committed boundary can carry the same id, as upstream's deliver_to_queen →
// _drain_injection_queue pair does.
func (r *ChatRuntime) DeliverClientInput(message, correlationID string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if r.relayClientInputToOverseer(message) {
		recorded := message
		if block := r.takeRelayCatalogBlock(); block != "" {
			recorded = block + "\n\n" + message
		}
		_ = r.enqueueTurn(chatTurnRequest{message: recorded, clientInput: true, recordOnly: true, correlationID: correlationID})
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: message, clientInput: true, correlationID: correlationID})
}

// InjectQueenMail queues a letter or reply from another queen: it enters as a
// client message (the way a chat app's message does, so she credits the
// writer) but is marked as queen mail, so the chain depth continues instead of
// starting over.
//
// Parity: framework/server/queen_mail.py::QueenMail._send_to_hive_queen
func (r *ChatRuntime) InjectQueenMail(content string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: content, clientInput: true, fromQueenMail: true})
}

// InjectWorkerReport queues a worker's [WORKER_REPORT] block as the queen's
// next turn. It is delivered as a non-client ("[External event]") user message,
// exactly like the upstream AgentLoop.inject_event(notification) the worker
// report handler calls, and never counted as a real user message.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (inject_event)
func (r *ChatRuntime) InjectWorkerReport(report string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(report) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: report, clientInput: false})
}

// InjectEvent queues content as the queen's next turn. A client input is a
// real user message; anything else is delivered as an "[External event]" user
// turn and never counted as a real user message. This is the QueenNode seam
// the session's trigger timers and the Sentinel manager resume path call.
//
// Parity: framework/agent_loop/agent_loop.py::inject_event
func (r *ChatRuntime) InjectEvent(content string, isClientInput bool) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if isClientInput {
		return r.Deliver(content)
	}
	return r.enqueueTurn(chatTurnRequest{message: content, clientInput: false})
}

// InjectTrigger queues a framework trigger (timer tick or webhook hit) as the
// queen's next turn. Triggers are delivered as their own user message, tagged
// is_trigger so the dashboard renders a banner instead of the raw block; they
// are never counted as client input.
//
// Parity: framework/agent_loop/agent_loop.py::inject_trigger
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func (r *ChatRuntime) InjectTrigger(event agentloop.TriggerEvent) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	pending := event
	// A live overseer owns the session's turns: the trigger goes to it alone,
	// so the chat queen does not answer the same tick a second time.
	if r.relayTriggerToOverseer(pending) {
		return nil
	}
	if err := r.enqueueTurn(chatTurnRequest{trigger: &pending}); err != nil {
		return err
	}
	// The colony's persistent overseer is the second lead for a bound colony;
	// give it the same trigger so it is not a Queen with no input source.
	r.forwardTriggerToOverseer(pending)
	return nil
}

func (r *ChatRuntime) enqueueTurn(request chatTurnRequest) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrChatRuntimeClosed
	}
	r.queue = append(r.queue, request)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// LastError reports the outcome of the most recent turn (nil after a
// successful turn).
func (r *ChatRuntime) LastError() error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Transcript reads the persisted conversation parts from the store.
func (r *ChatRuntime) Transcript() ([]map[string]any, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.ReadParts()
}

// Close stops the worker, cancels the in-flight turn, drains any background
// memory reflection (with a final pass so the last turns are persisted), and
// releases the store.
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
	// Upstream's session_manager.stop_session flushes the timeline after the
	// turn loop is quiescent, so messages that arrived inside the live
	// coalescing window are not lost. The runtime context is already
	// cancelled here, so the flush gets its own bounded context; timelineWG
	// keeps Close's promise that nothing writes after it returns.
	//
	// Parity: core/framework/server/session_manager.py::SessionManager.stop_session
	if r.timelineEnabled && r.deps.LLM != nil && r.deps.Root != "" {
		ctx, cancel := context.WithTimeout(context.Background(), memoryTimelineStopFlushTimeout)
		r.timelineWG.Add(1)
		go func() {
			defer r.timelineWG.Done()
			defer cancel()
			_, _ = memory.UpdateSessionTimeline(ctx, r.deps.LLM, r.deps.Root)
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
	// reflection: reflections run on the scheduler's own context, so this
	// waits for one already in flight instead of aborting it mid-write.
	if r.reflection != nil {
		r.reflection.Close(memory.ReflectionCloseGrace)
	}
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
		message := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		r.runTurn(message)
	}
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
	if request.opening {
		r.runOpeningTurn()
		return
	}
	r.pinTurnProvider()
	if request.recordOnly {
		// A message the overseer executed: append the turn so the transcript
		// keeps the ask, without running the chat queen again. The overseer's
		// own reply is an assistant record, not a second user turn.
		if request.externalAssistant {
			r.recordExternalAssistant(request.message)
			return
		}
		if request.external {
			r.recordExternalTurn(request.message)
			return
		}
		r.recordRelayedClientTurn(request.message)
		return
	}
	message := request.message
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
		defer r.streamLLM.endTurn()
	}

	stamp := hiveclock.Now().Format("2006-01-02 15:04 MST")
	if request.trigger != nil {
		// A trigger is delivered as upstream's drained trigger batch: the
		// block carries its own timestamp, so it is inserted verbatim rather
		// than taking the "[stamp] [External event]" prefix.
		message = formatTriggerTurn(*request.trigger, stamp)
	}
	r.markTurnStart()
	defer r.markTurnEnd()
	// A real user message or a trigger answers any outstanding ask_user park:
	// both unblock the pending-input wait upstream (`_await_user_input` clears
	// its stash when the wait returns), so clear it before this turn can
	// record a new question. An external report is not an answer, so it
	// leaves the parked question in place.
	if request.clientInput || request.trigger != nil {
		r.clearPendingQuestions()
		r.clearPendingCredentialForm()
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
		if r.conv.MessageCount() == 0 {
			r.fireReminderPoint(agentloop.SESSION_START, message, nil)
		}
		// Parity: framework/agent_loop/agent_loop.py::_drain_injection_queue
		// — USER_PROMPT_SUBMIT fires before the queued user message is added.
		// A trigger is drained separately upstream, so it never fires this
		// point (there is no user prompt to frame).
		if request.trigger == nil {
			r.fireReminderPoint(agentloop.USER_PROMPT_SUBMIT, message, nil)
		}
		// The past-conversations source fires at that same point upstream, so
		// its excerpts land ahead of the user's message rather than after it.
		if request.clientInput {
			r.injectPastConversationRecall(message)
		}
		stamped := "[" + stamp + "] " + message
		switch {
		case request.trigger != nil:
			// formatTriggerTurn already stamped the block, matching the
			// upstream drain which inserts the batch verbatim.
			stamped = message
		case !request.clientInput:
			stamped = "[" + stamp + "] [External event] " + message
		}
		committedMessage, err := r.conv.AddUserMessage(stamped, agentloop.UserMessageOptions{
			IsClientInput: request.clientInput,
			IsTrigger:     request.trigger != nil,
		})
		if err != nil {
			emitter.executionFailed(err, agentloop.ParkReasonUnknown)
			r.setParkState(agentloop.ParkReasonUnknown, true)
			r.setLastError(err)
			return
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
		// The ticker may have parked a reminder while the session waited for
		// this message; drain it into the turn before the model reads it.
		r.injectHubReminders()
	}
	// Recall is a user-input step upstream; an external worker report must not
	// consume (or re-query) the queen's memory. Reminders still ride along.
	if request.clientInput {
		r.injectRecall(message, stamp)
	}
	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		// A cancelled turn (session teardown) is not a failure worth
		// broadcasting: the runtime is going away, not the model failing.
		if r.ctx.Err() == nil {
			// A loop-raised park (the deterministic doom loop) outranks the
			// generic failure reason: the event and the runtime park state must
			// name the same reason the TurnResult carries.
			parkReason := turnFailureParkReason(result, agentloop.ParkReasonLLMError)
			emitter.executionFailed(err, parkReason)
			r.setParkState(parkReason, true)
		}
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
	// Parity: framework/agent_loop/agent_loop.py::_await_user_input — a turn
	// that asked the user a question parks on ask_user, not turn_done, so the
	// snapshot and idle nudge treat it as an external wait.
	parkReason := agentloop.ParkReasonTurnDone
	if pending := r.pendingParkReason(); pending != "" {
		parkReason = pending
	}
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
func (r *ChatRuntime) runOpeningTurn() {
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

	result, err := r.loop.ExecuteTurn(r.ctx, r.agentCtx, r.conv, r.toolsForTurn())
	if err != nil {
		if r.ctx.Err() == nil {
			parkReason := turnFailureParkReason(result, agentloop.ParkReasonLLMError)
			emitter.executionFailed(err, parkReason)
			r.setParkState(parkReason, true)
		}
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
	parkReason := agentloop.ParkReasonTurnDone
	if pending := r.pendingParkReason(); pending != "" {
		parkReason = pending
	}
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

// injectRecall gives the turn the block picked for an earlier message and
// starts the pick for this message in the background; it is the upstream
// "background" mode (_recall_on_user_input). The EventBus drops a handler at
// 15s, so upstream's handler must return fast: it delivers only what is
// already cached and fires the pick as a task that joins the queen's next
// drain — in practice her next message. This method therefore never calls the
// model on the turn's critical path. "wait" and "index" are reminder sources,
// so it is silent while either is set.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input
// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
// (cached delivery, deduped against _last_injected_recall)
func (r *ChatRuntime) injectRecall(query, stamp string) {
	if r.globalMemoryDir == "" {
		return
	}
	if r.memoryRecallSettings().Mode != memory.MemoryRecallBackground {
		return
	}
	// Upstream: await _inject_recall_if_changed() — cached block only, no
	// model call; then asyncio.create_task(_bg_refresh()).
	r.deliverCachedRecall(stamp)
	r.pickRecallInBackground(query)
}

// deliverCachedRecall appends the cached pick as a <system-reminder> when it
// differs from the last one injected. Upstream's drain queues passive
// injections after anything already queued, so the reminder lands after the
// user's message. It runs on the turn goroutine; conversation writes stay
// single-writer.
//
// Parity: core/framework/server/queen_orchestrator.py::_inject_recall_if_changed
func (r *ChatRuntime) deliverCachedRecall(stamp string) {
	r.recallMu.Lock()
	block := r.recallCache
	r.recallMu.Unlock()
	if block == "" || block == r.lastRecall {
		return
	}
	r.lastRecall = block
	reminder := "<system-reminder>\n" + agentloop.MemoryRecallHeader + "\n\n" + block + "\n</system-reminder>"
	// Recall is context, never a reason to fail a turn: a write error leaves
	// the conversation as it was and the turn runs without the reminder.
	_, _ = r.conv.AddUserMessage("["+stamp+"] [External event] "+reminder, agentloop.UserMessageOptions{})
}

// pickRecallInBackground runs the pick off the turn goroutine, mirroring
// upstream's _bg_refresh task: the result is cached, and the next drain (the
// next message) delivers it if it differs. The pick swallows its own failures
// the way select_memories does — an empty result overwrites the cache, as
// upstream's _refresh_recall_cache assigns whatever the pick returned.
//
// Parity: core/framework/server/queen_orchestrator.py::_recall_on_user_input (_bg_refresh)
func (r *ChatRuntime) pickRecallInBackground(query string) {
	if r == nil {
		return
	}
	go r.refreshRecallCache(query)
}

// refreshRecallCache picks the scoped recall blocks for query and stores the
// joined result as the deliverable cache. The empty-query guard mirrors
// upstream _refresh_recall_cache (`if not query`): an empty message makes no
// pick call, and any cached block is still delivered at the next drain.
//
// Parity: core/framework/server/queen_orchestrator.py::_refresh_recall_cache
func (r *ChatRuntime) refreshRecallCache(query string) {
	if r == nil || query == "" {
		return
	}
	block := r.recallBlockForQuery(query)
	r.recallMu.Lock()
	r.recallCache = block
	r.recallMu.Unlock()
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

// toolsForTurn advertises the session tools for one turn. The colony-runtime
// lifecycle tools (run_worker, get_worker_status, inject_message,
// stop_worker) are colony-gated: without a bound colony the queen does not see
// the defs at all (a direct call still returns an actionable no-colony error
// from the executor), matching upstream's colony-phase tool list.
//
// Parity: framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools
// (the colony-runtime tools only reach a colony-phase registry)
func (r *ChatRuntime) toolsForTurn() []llm.ToolDef {
	runtime := r.boundColonyRuntime()
	if runtime != nil {
		return filterToolsByColonyAllowlist(runtime, r.tools)
	}
	out := make([]llm.ToolDef, 0, len(r.tools))
	for _, def := range r.tools {
		if def.Name == colony.RunWorkerToolName || colonyBoundQueenTool(def.Name) {
			continue
		}
		out = append(out, def)
	}
	return out
}

// turnContextUsage builds the context readout the upstream loop publishes on
// every context tick (iteration start, after each tool result, after the tool
// batch), using the same char-based projection as the session-start block.
func (r *ChatRuntime) turnContextUsage(trigger string) map[string]any {
	if r == nil || r.conv == nil {
		return nil
	}
	maxContextTokens := 0
	if r.loop != nil {
		maxContextTokens = r.loop.Config.MaxContextTokens
	}
	return composeContextUsage(r.conv, maxContextTokens, r.toolsForTurn(), trigger)
}

// BindColony attaches (or clears) this session's colony runtime. Attaching
// subscribes the queen to the colony's SUBAGENT_REPORT events, which arrive as
// [WORKER_REPORT] turns; detaching unsubscribes. It is idempotent and safe to
// call from session lookup paths.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (subscribe)
