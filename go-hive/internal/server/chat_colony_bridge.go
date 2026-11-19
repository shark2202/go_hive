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
// telemetry shape is introduced.
var colonyBridgeEventTypes = []eventbus.EventType{
	eventbus.EventExecutionStarted,
	eventbus.EventExecutionCompleted,
	eventbus.EventExecutionFailed,
	eventbus.EventWorkerCompleted,
	eventbus.EventWorkerFailed,
	eventbus.EventSubagentReport,
}

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
	// A caller that already shares the session bus needs no mirror.
	if holder, ok := runtime.(interface{ EventBus() *eventbus.EventBus }); ok {
		if holder.EventBus() == r.bus {
			return
		}
	}
	var subscriptionID string
	subID, err := runtime.SubscribeToEvents(colonyBridgeEventTypes, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		clone := event.Clone()
		clone.Data = cloneEventData(event.Data)
		if clone.ColonyID == nil {
			colonyID := runtime.StreamID()
			clone.ColonyID = &colonyID
		}
		r.bus.Publish(clone)
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

// forwardToOverseer delivers a report or trigger to the colony's persistent
// overseer when one is running. It is deliberately best-effort: the chat queen
// still owns the user-facing turn, and a stopping colony must not turn a
// forwarded input into a failed report.
func (r *ChatRuntime) forwardToOverseer(content string) {
	if r == nil || strings.TrimSpace(content) == "" {
		return
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil || runtime.OverseerID() == "" {
		return
	}
	if err := runtime.InjectOverseer(content); err != nil {
		log.Printf("chat runtime %s: overseer input dropped: %v", r.sessionID, err)
	}
}

// forwardTriggerToOverseer sends the same framework trigger the chat queen
// received. formatTriggerTurn renders the upstream shared block; its leading
// timestamp line is dropped because ColonyRuntime.InjectOverseer stamps the
// injected turn itself.
func (r *ChatRuntime) forwardTriggerToOverseer(event agentloop.TriggerEvent) {
	stamp := time.Now().Format("2006-01-02 15:04 MST")
	block := formatTriggerTurn(event, stamp)
	if index := strings.Index(block, "\n"); index >= 0 {
		block = block[index+1:]
	}
	r.forwardToOverseer(block)
}

// submitToOverseer is the shared report path: the chat queen still receives
// the [WORKER_REPORT] turn, and a different worker's report is also injected
// into the overseer. The overseer's own report is skipped to avoid an echo
// loop.
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
