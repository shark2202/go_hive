package server

// Colony -> session bridges.
//
// A bound colony publishes worker lifecycle events on its own EventBus (the
// host owns it), while the dashboard follows the session's EventBus over SSE.
// Nothing connected the two, so worker started/completed/failed events were
// invisible to the UI. This file mirrors the colony lifecycle onto the session
// bus using the existing agent-event types, and forwards the same inputs
// (worker reports, framework triggers) to the colony's persistent overseer so
// it has a real input source.

import (
	"log"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
)

// colonyBridgeEventTypes are the colony event types worth mirroring onto the
// session bus. They are the existing agent-event lifecycle types; no new
// telemetry shape is introduced. The client-IO types carry the persistent
// overseer's replies and questions: the overseer is a queen-stage worker
// (stream "overseer"), which the dashboard's session pane would otherwise drop.
var colonyBridgeEventTypes = []eventbus.EventType{
	eventbus.EventExecutionStarted,
	eventbus.EventExecutionCompleted,
	eventbus.EventExecutionFailed,
	eventbus.EventWorkerCompleted,
	eventbus.EventWorkerFailed,
	eventbus.EventSubagentReport,
	eventbus.EventClientOutputDelta,
	eventbus.EventClientReasoning,
	eventbus.EventClientInputRequested,
	eventbus.EventLoopStateChanged,
	eventbus.EventToolCallStarted,
	eventbus.EventToolCallCompleted,
	eventbus.EventLLMTurnComplete,
}

// overseerStreamID is the stream the colony's persistent overseer publishes
// under (colony_runtime.start_overseer: stream_id="overseer").
const overseerStreamID = "overseer"

// colonyEventUnsubscriber is implemented by the concrete colony runtime; the
// server interface stays narrow enough for stubs in tests.
type colonyEventUnsubscriber interface {
	Unsubscribe(subscriptionID string) bool
}

// syncColonyEventBridge mirrors the bound runtime's lifecycle events onto the
// session bus. It is idempotent per runtime stream: repeated binds (every
// session lookup calls BindChatColony) do not stack subscriptions. A nil
// runtime tears the bridge down.
func (r *ChatRuntime) syncColonyEventBridge(runtime ColonyRuntime) {
	if r == nil {
		return
	}
	streamID := ""
	if runtime != nil {
		streamID = runtime.StreamID()
	}
	r.bridgeMu.Lock()
	defer r.bridgeMu.Unlock()
	if r.colonyBridgeUnsub != nil && r.colonyBridgeStream == streamID {
		return
	}
	if r.colonyBridgeUnsub != nil {
		r.colonyBridgeUnsub()
		r.colonyBridgeUnsub = nil
		r.colonyBridgeStream = ""
	}
	if runtime == nil || r.bus == nil {
		return
	}
	// A caller that already shares the session bus needs no mirror. It still
	// needs the park observer and the transcript recorder: an overseer
	// ask_user must reach the chat runtime's pending state (and the session
	// cursor) on either bus, and its reply must land in the transcript even
	// when its live events are already on the session bus.
	sharedBus := false
	if holder, ok := runtime.(interface{ EventBus() *eventbus.EventBus }); ok {
		sharedBus = holder.EventBus() == r.bus
	}
	eventTypes := colonyBridgeEventTypes
	if sharedBus {
		eventTypes = []eventbus.EventType{
			eventbus.EventClientInputRequested,
			eventbus.EventClientOutputDelta,
			eventbus.EventToolCallStarted,
			eventbus.EventExecutionCompleted,
			eventbus.EventExecutionFailed,
		}
	}
	var subscriptionID string
	subID, err := runtime.SubscribeToEvents(eventTypes, func(event *eventbus.AgentEvent) {
		// An overseer tool result can be the credential miss that has to park
		// the session. Consume it before mirroring, which deliberately drops
		// the overseer's own tool/token events.
		r.parkOverseerCredentialForm(event)
		// The overseer's reply is turned into one assistant record per turn
		// for the session transcript. A mirror is live-only; the transcript
		// is what a restarted session rebuilds its history from.
		r.observeOverseerTranscriptEvent(event)
		// The overseer asks in the same conversation the chat front end
		// renders, so its question is recorded on the session's pending_input
		// before the client sees the mirrored event: both halves then park on
		// one durable record, and a restart restores that wait instead of
		// silently continuing the last turn.
		r.applyOverseerAskUser(event)
		if sharedBus {
			return
		}
		mirrored := mirrorColonyEvent(event, runtime.StreamID())
		if mirrored == nil {
			return
		}
		r.bus.Publish(mirrored)
	})
	if err != nil {
		log.Printf("chat runtime %s: colony event bridge unavailable: %v", r.sessionID, err)
		return
	}
	subscriptionID = subID
	r.colonyBridgeStream = streamID
	r.colonyBridgeUnsub = func() {
		if unsubscriber, ok := runtime.(colonyEventUnsubscriber); ok && subscriptionID != "" {
			unsubscriber.Unsubscribe(subscriptionID)
		}
	}
}

func cloneEventData(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	out := make(map[string]any, len(data))
	for key, value := range data {
		out[key] = value
	}
	return out
}

// isClientIOEvent reports whether an event type carries client-facing model
// output or a question for the user.
func isClientIOEvent(eventType eventbus.EventType) bool {
	switch eventType {
	case eventbus.EventClientOutputDelta, eventbus.EventClientReasoning, eventbus.EventClientInputRequested:
		return true
	}
	return false
}

// isWorkerProgressEvent reports whether an event type is live turn progress
// (text, tool activity, token usage) rather than a lifecycle boundary. Those
// are the events a worker publishes under its own stream and the session pane
// would otherwise filter out as worker noise.
func isWorkerProgressEvent(eventType eventbus.EventType) bool {
	switch eventType {
	case eventbus.EventToolCallStarted, eventbus.EventToolCallCompleted, eventbus.EventLLMTurnComplete:
		return true
	}
	return isClientIOEvent(eventType)
}

// isOverseerSessionEvent reports whether an overseer event belongs to the
// session's client-facing turn (text, questions, tool-independent turn
// boundaries the dashboard's stream state follows).
func isOverseerSessionEvent(eventType eventbus.EventType) bool {
	switch eventType {
	case eventbus.EventExecutionStarted, eventbus.EventExecutionCompleted, eventbus.EventExecutionFailed, eventbus.EventLoopStateChanged:
		return true
	}
	return isClientIOEvent(eventType)
}

// mirrorColonyEvent builds the session-bus copy of a colony event, or nil when
// the event must not be mirrored. The persistent overseer's client-facing events
// are re-stamped onto the session's queen stream: the overseer executes the work
// and the chat session is its front end, so its reply has to arrive where the
// dashboard renders the queen. A spawned worker's live progress is re-stamped
// the same way but keeps its identity in relayed_from=<worker id>; the session
// SSE filter drops a worker's text/tool/token events, so this relay is what
// makes a running worker visible. Lifecycle and report events keep their
// stream, and an event already carrying relayed_from is a mirror and is never
// mirrored again.
func mirrorColonyEvent(event *eventbus.AgentEvent, streamID string) *eventbus.AgentEvent {
	if event == nil {
		return nil
	}
	clone := event.Clone()
	clone.Data = cloneEventData(event.Data)
	if clone.ColonyID == nil && streamID != "" {
		colonyID := streamID
		clone.ColonyID = &colonyID
	}
	if clone.StreamID == overseerStreamID {
		if !isOverseerSessionEvent(clone.Type) {
			if isWorkerProgressEvent(clone.Type) {
				// The overseer's own tool/token events are already represented
				// by its client-facing turn; re-mirroring them would duplicate
				// the queen's stream.
				return nil
			}
			return clone
		}
		if clone.Data == nil {
			clone.Data = map[string]any{}
		}
		clone.Data["relayed_from"] = overseerStreamID
		clone.StreamID = chatStreamID
		clone.NodeID = eventbus.StrPtr(chatNodeID)
		return clone
	}
	if !isWorkerProgressEvent(clone.Type) {
		return clone
	}
	if clone.StreamID == chatStreamID || clone.StreamID == "" {
		return nil
	}
	if clone.Data != nil {
		if _, relayed := clone.Data["relayed_from"]; relayed {
			return nil
		}
	}
	if !eventbus.IsWorkerStream(clone.StreamID) {
		// Only the overseer and a colony worker feed the session pane; any
		// other stream's live progress has no session identity to relay.
		return nil
	}
	if clone.Data == nil {
		clone.Data = map[string]any{}
	}
	clone.Data["relayed_from"] = workerIDFromStream(clone.StreamID)
	clone.StreamID = chatStreamID
	clone.NodeID = eventbus.StrPtr(chatNodeID)
	return clone
}

// workerIDFromStream turns "worker:<id>" into the worker id the relay stamp
// names; the bare singular "worker" tag has no id and stays "worker".
func workerIDFromStream(streamID string) string {
	if id := strings.TrimPrefix(streamID, "worker:"); id != streamID && id != "" {
		return id
	}
	return "worker"
}

// parkOverseerCredentialForm is the overseer execution path's credential-form
// producer. The chat loop notices a missing-credential tool result inside its
// own turn; the overseer runs outside that loop, so its completed tool event is
// the only signal. The park, its persisted pending_input and the client event
// are the same ones the chat path produces, which is what
// POST /api/sessions/{id}/credential-form consumes.
//
// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
// Parity: framework/agent_loop/agent_loop.py::_await_user_input (credential_form branch)
func (r *ChatRuntime) parkOverseerCredentialForm(event *eventbus.AgentEvent) {
	if r == nil || event == nil || event.Type != eventbus.EventToolCallCompleted {
		return
	}
	if event.StreamID != overseerStreamID || event.Data == nil {
		return
	}
	toolName, _ := event.Data["tool_name"].(string)
	content, _ := event.Data["result"].(string)
	if toolName == "" || content == "" {
		return
	}
	_, _ = r.parkForMissingCredential(toolName, content)
}

// deliverToOverseer hands a client message to the colony's persistent overseer
// and reports whether it was accepted. A colony with no live overseer keeps the
// chat queen as the executor, so delivery never silently drops a message.
func (r *ChatRuntime) deliverToOverseer(message string) bool {
	if r == nil || strings.TrimSpace(message) == "" {
		return false
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil || runtime.OverseerID() == "" {
		return false
	}
	if err := runtime.InjectOverseer(message); err != nil {
		log.Printf("chat runtime %s: overseer relay unavailable: %v", r.sessionID, err)
		return false
	}
	return true
}

// deliverAnswerToOverseer is the client-message variant of deliverToOverseer:
// the injected message answers whatever the overseer was parked on, so the
// colony clears its half of the shared park in the same call.
func (r *ChatRuntime) deliverAnswerToOverseer(message string) bool {
	if r == nil || strings.TrimSpace(message) == "" {
		return false
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil || runtime.OverseerID() == "" {
		return false
	}
	if err := runtime.AnswerOverseer(message); err != nil {
		log.Printf("chat runtime %s: overseer answer relay unavailable: %v", r.sessionID, err)
		return false
	}
	return true
}

// relayClientInputToOverseer is the bound-session client path: the user message
// becomes an overseer turn and the chat queen's own loop stays out of it. One
// loop acts on the message — the overseer — while the chat runtime remains the
// session's front end (transcript, event bus, ask_user surface).
func (r *ChatRuntime) relayClientInputToOverseer(message string) bool {
	payload, block := r.relayPayloadWithCatalog(message)
	if !r.deliverAnswerToOverseer(payload) {
		return false
	}
	if block != "" {
		r.markRelayCatalogSent(block)
	}
	// The client message answers whatever either half was parked on: drop the
	// shared pending_input record (and the in-memory question) before the
	// turn's own checkpoint rewrites it as turn_done. One answer resolves both
	// conversations, so neither asks the same question again.
	r.clearPendingQuestions()
	r.clearPendingCredentialForm()
	// The session is busy even though the chat loop is not: re-arm the idle
	// nudge and close any open Sentinel escalation the way a client turn does.
	if r.idleSource != nil {
		r.idleSource.Reset()
	}
	r.notifySentinelLocalResume()
	r.markProgress()
	r.setParkState(agentloop.ParkReasonTurnDone, true)
	return true
}

// relaySkillsCatalogBlock renders the queen's skills catalog once, for the
// first client message relayed to the overseer. The reminder hub that normally
// injects it belongs to the chat loop; when the overseer executes, the catalog
// has to ride with the relayed input or it never reaches the model at all.
func (r *ChatRuntime) relaySkillsCatalogBlock() string {
	if r == nil || r.agentCtx == nil || r.agentCtx.QueenSkillsCatalogProvider == nil {
		return ""
	}
	provider, ok := r.agentCtx.QueenSkillsCatalogProvider.(func() string)
	if !ok || provider == nil {
		return ""
	}
	r.relayCatalogMu.Lock()
	sent := r.relayCatalogSent
	r.relayCatalogMu.Unlock()
	if sent {
		return ""
	}
	catalog := strings.TrimSpace(provider())
	if catalog == "" {
		return ""
	}
	return "<system-reminder>\nAvailable skills catalog:\n" + catalog + "\n</system-reminder>"
}

func (r *ChatRuntime) markRelayCatalogSent(block string) {
	r.relayCatalogMu.Lock()
	r.relayCatalogSent = true
	r.lastRelayBlock = block
	r.relayCatalogMu.Unlock()
}

// takeRelayCatalogBlock returns (once) the catalog that rode with the relayed
// message, so the transcript records what the overseer was actually given.
func (r *ChatRuntime) takeRelayCatalogBlock() string {
	r.relayCatalogMu.Lock()
	block := r.lastRelayBlock
	r.lastRelayBlock = ""
	r.relayCatalogMu.Unlock()
	return block
}

// recordRelayedClientTurn appends a relayed user turn to the transcript. It
// runs on the runtime's queue goroutine (the conversation's only writer), so a
// relayed message never races a running turn.
func (r *ChatRuntime) recordRelayedClientTurn(message string) {
	if r == nil || r.conv == nil {
		return
	}
	stamp := time.Now().Format("2006-01-02 15:04 MST")
	if _, err := r.conv.AddUserMessage("["+stamp+"] "+message, agentloop.UserMessageOptions{IsClientInput: true}); err != nil {
		log.Printf("chat runtime %s: relayed user turn not recorded: %v", r.sessionID, err)
	}
}

// recordOverseerTerminal archives the overseer's own terminal report in the
// session transcript without running the chat queen. The report is not an
// execution input — the overseer's reply already reached the front end as the
// mirrored client stream — so a model turn here would be a second answer to a
// report the user just saw.
//
// Parity-exempt: Go-side front-end record. Upstream's _on_worker_report turns
// every report into a queen turn; this port skips that turn because the
// overseer and the chat queen are two conversations over one client surface,
// so the second turn is a duplicate, not a response.
func (r *ChatRuntime) recordOverseerTerminal(report string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(report) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: report, recordOnly: true, external: true})
}

// recordExternalTurn appends an "[External event]" turn to the transcript
// without running the model. It runs on the runtime's queue goroutine (the
// conversation's only writer), so archiving a report never races a turn.
func (r *ChatRuntime) recordExternalTurn(message string) {
	if r == nil || r.conv == nil {
		return
	}
	stamp := time.Now().Format("2006-01-02 15:04 MST")
	if _, err := r.conv.AddUserMessage("["+stamp+"] [External event] "+message, agentloop.UserMessageOptions{}); err != nil {
		log.Printf("chat runtime %s: external turn not recorded: %v", r.sessionID, err)
	}
}

// forwardToOverseer delivers a report or trigger to the colony's persistent
// overseer when one is running. It is the fallback path used when the overseer
// does not own the session's turns; it is deliberately best-effort so a
// stopping colony never turns a forwarded input into a failed report.
func (r *ChatRuntime) forwardToOverseer(content string) {
	if r == nil || strings.TrimSpace(content) == "" {
		return
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil || runtime.OverseerID() == "" {
		return
	}
	payload, block := r.relayPayloadWithCatalog(content)
	if err := runtime.InjectOverseer(payload); err != nil {
		log.Printf("chat runtime %s: overseer input dropped: %v", r.sessionID, err)
		return
	}
	if block != "" {
		r.markRelayCatalogSent(block)
	}
}

// overseerRelayActive reports whether a live overseer owns this session's
// turns. When it does, the chat runtime is the session's front end and every
// execution input (client message, worker report, framework trigger) goes to
// the overseer instead of a second chat-queen loop.
func (r *ChatRuntime) overseerRelayActive() bool {
	runtime := r.boundColonyRuntime()
	return runtime != nil && runtime.OverseerID() != ""
}

// relayWorkerReportToOverseer hands a worker report to the overseer when it owns
// the turns. False keeps the chat queen's own report turn.
func (r *ChatRuntime) relayWorkerReportToOverseer(report string) bool {
	if !r.overseerRelayActive() {
		return false
	}
	payload, block := r.relayPayloadWithCatalog(report)
	if !r.deliverToOverseer(payload) {
		return false
	}
	if block != "" {
		r.markRelayCatalogSent(block)
	}
	return true
}

// relayTriggerToOverseer hands a framework trigger to the overseer when it owns
// the turns. False keeps the chat queen's own trigger turn.
func (r *ChatRuntime) relayTriggerToOverseer(event agentloop.TriggerEvent) bool {
	if !r.overseerRelayActive() {
		return false
	}
	payload, block := r.relayPayloadWithCatalog(triggerBlockForOverseer(event))
	if !r.deliverToOverseer(payload) {
		return false
	}
	if block != "" {
		r.markRelayCatalogSent(block)
	}
	return true
}

// relayPayloadWithCatalog prepends the one-shot skills catalog block to the
// first execution input the overseer receives, whichever kind it is (client
// message, worker report or trigger).
func (r *ChatRuntime) relayPayloadWithCatalog(content string) (string, string) {
	block := r.relaySkillsCatalogBlock()
	if block == "" {
		return content, ""
	}
	return block + "\n\n" + content, block
}

// forwardTriggerToOverseer sends the same framework trigger the chat queen
// received. formatTriggerTurn renders the upstream shared block; its leading
// timestamp line is dropped because ColonyRuntime.InjectOverseer stamps the
// injected turn itself.
func (r *ChatRuntime) forwardTriggerToOverseer(event agentloop.TriggerEvent) {
	r.forwardToOverseer(triggerBlockForOverseer(event))
}

func triggerBlockForOverseer(event agentloop.TriggerEvent) string {
	stamp := time.Now().Format("2006-01-02 15:04 MST")
	block := formatTriggerTurn(event, stamp)
	if index := strings.Index(block, "\n"); index >= 0 {
		return block[index+1:]
	}
	return block
}

// submitWorkerReportToOverseer is the shared report path used when no overseer
// owns the session's turns: the chat queen still receives the [WORKER_REPORT]
// turn, and a different worker's report is also injected into the overseer. The
// overseer's own report is skipped to avoid an echo loop.
func (r *ChatRuntime) submitWorkerReportToOverseer(workerID, report string) {
	runtime := r.boundColonyRuntime()
	if runtime == nil || workerID == "" {
		return
	}
	if overseerID := runtime.OverseerID(); overseerID != "" && workerID == overseerID {
		return
	}
	r.forwardToOverseer(report)
}
