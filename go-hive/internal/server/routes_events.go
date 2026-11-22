package server

// Parity: framework/server/routes_events.py::handle_global_events
// Parity: framework/server/routes_events.py::handle_crm_changed
// Parity: framework/server/sse.py::SSEResponse.send_event

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// The session-primary SSE route (GET /api/sessions/{session_id}/events) is
// already owned by RegisterChatRoutes in chat_routes.go. It is intentionally
// NOT registered here: Go's ServeMux panics on a duplicate pattern, and the
// upstream comment on that route ("Session-primary route") is satisfied by
// the chat module. This file only carries the two routes that have no other
// owner: the global cross-cutting channel and the CRM write ping.

const globalEventsQueueSize = 200

const defaultGlobalKeepalive = 15 * time.Second

type EventRouteDeps struct {
	// GlobalBus resolves the process-global bus. Nil uses eventbus.GetGlobalEventBus.
	GlobalBus func() *eventbus.EventBus
	// Keepalive is the idle keepalive cadence. Zero uses 15s.
	Keepalive time.Duration
}

func (deps EventRouteDeps) globalBus() *eventbus.EventBus {
	if deps.GlobalBus != nil {
		return deps.GlobalBus()
	}
	return eventbus.GetGlobalEventBus()
}

func (deps EventRouteDeps) keepalive() time.Duration {
	if deps.Keepalive > 0 {
		return deps.Keepalive
	}
	return defaultGlobalKeepalive
}

// globalEventTypes mirrors routes_events.py::_GLOBAL_EVENT_TYPES: credential
// connect/disconnect, tool catalog refreshes, tools-config edits and CRM
// writes. Every published event of these types reaches every subscriber.
var globalEventTypes = []eventbus.EventType{
	eventbus.EventCredentialProviderConnected,
	eventbus.EventCredentialProviderDisconnected,
	eventbus.EventToolCatalogRefreshed,
	eventbus.EventToolsConfigChanged,
	eventbus.EventCRMChanged,
}

// crmEntities mirrors routes_events.py::_CRM_ENTITIES. The renderer keys its
// tabs by these slugs; anything else is dropped rather than forwarded.
var crmEntities = map[string]struct{}{
	"people":        {},
	"organizations": {},
	"opportunities": {},
	"interactions":  {},
}

func RegisterEventRoutes(mux *http.ServeMux, deps EventRouteDeps) {
	mux.HandleFunc("GET /api/events/global", func(w http.ResponseWriter, r *http.Request) {
		handleGlobalEvents(w, r, deps)
	})
	mux.HandleFunc("POST /api/crm/changed", func(w http.ResponseWriter, r *http.Request) {
		handleCRMChanged(w, r)
	})
}

// Parity: framework/server/routes_events.py::handle_global_events
func handleGlobalEvents(w http.ResponseWriter, r *http.Request, deps EventRouteDeps) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	bus := deps.globalBus()
	queue := make(chan map[string]any, globalEventsQueueSize)
	disconnected := make(chan struct{})
	var once sync.Once
	markDisconnected := func() { once.Do(func() { close(disconnected) }) }

	subID := bus.Subscribe(globalEventTypes, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		select {
		case queue <- event.ToDict():
		default:
			// Global events are infrequent; a full queue means the client is
			// wedged. Drop and let it reconnect, as upstream does.
			markDisconnected()
		}
	})
	defer bus.Unsubscribe(subID)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(deps.keepalive())
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-disconnected:
			return
		case payload := <-queue:
			// SSEResponse.send_event(data) is called without event= upstream,
			// so global frames are data-only and land on EventSource.onmessage.
			writeSSEData(w, flusher, payload)
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// Parity: framework/server/routes_events.py::handle_crm_changed
func handleCRMChanged(w http.ResponseWriter, r *http.Request) {
	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		body = map[string]any{}
	}

	entities := []string{}
	if raw, ok := body["entities"].([]any); ok {
		seen := map[string]struct{}{}
		for _, item := range raw {
			name, ok := item.(string)
			if !ok {
				continue
			}
			if _, allowed := crmEntities[name]; !allowed {
				continue
			}
			seen[name] = struct{}{}
		}
		for name := range seen {
			entities = append(entities, name)
		}
		sort.Strings(entities)
	}

	event := eventbus.NewAgentEvent(eventbus.EventCRMChanged, "global")
	event.Data = map[string]any{
		"entities": entities,
		"schema":   pyTruthy(body["schema"]),
	}
	eventbus.PublishGlobal(event)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// writeSSEData renders a data-only SSE frame, as SSEResponse.send_event does
// when no event name is given (the global channel).
func writeSSEData(w http.ResponseWriter, flusher http.Flusher, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", encoded)
	flusher.Flush()
}

// writeNamedSSEEvent renders one SSE frame with an explicit event name, as
// SSEResponse.send_event(event=...) does.
func writeNamedSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, payload map[string]any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
	flusher.Flush()
}
