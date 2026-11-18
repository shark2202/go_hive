package server

// Parity: framework/server/routes_execution.py::handle_chat
// Parity: framework/server/routes_events.py::handle_events

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

const sessionEventsHeartbeatInterval = 20 * time.Second

const sessionEventsQueueSize = 256

// ChatRouteDeps resolves the live session a chat message targets. Manager is
// the live store the server uses; Sessions covers app builds that expose a
// custom lookup.
type ChatRouteDeps struct {
	Manager  *SessionManager
	Sessions func(sessionID string) *Session
}

func RegisterChatRoutes(mux *http.ServeMux, deps ChatRouteDeps) {
	mux.HandleFunc("POST /api/sessions/{session_id}/chat", func(w http.ResponseWriter, r *http.Request) {
		handleSessionChat(w, r, deps)
	})
	mux.HandleFunc("GET /api/sessions/{session_id}/events", func(w http.ResponseWriter, r *http.Request) {
		handleSessionEventsStream(w, r, deps)
	})
}

func chatSession(deps ChatRouteDeps, sessionID string) *Session {
	if deps.Manager != nil {
		return deps.Manager.GetSession(sessionID)
	}
	if deps.Sessions != nil {
		return deps.Sessions(sessionID)
	}
	return nil
}

func decodeChatBody(r *http.Request, target any) error {
	if r.Body == nil {
		return io.EOF
	}
	return json.NewDecoder(r.Body).Decode(target)
}

func handleSessionChat(w http.ResponseWriter, r *http.Request, deps ChatRouteDeps) {
	sessionID := r.PathValue("session_id")
	session := chatSession(deps, sessionID)
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}

	// Upstream locks a session once it has spawned a colony: the user must
	// compact and fork into a fresh session before continuing.
	if session.ColonySpawned {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":             "session_locked",
			"reason":            "colony_spawned",
			"spawned_colony_id": session.SpawnedColonyID,
			"message": "This session is locked because a colony has been spawned " +
				"from it. Compact and start a new session with the same queen to continue.",
		})
		return
	}

	// Sessions forked away are retired: the conversation continues in the
	// successor session. Lock the old one so a stale tab cannot write to a
	// session that is being stopped.
	if session.SupersededBy != "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":         "session_locked",
			"reason":        "superseded",
			"superseded_by": session.SupersededBy,
			"message":       "This session has been forked into a fresh one. Continue in the successor session.",
		})
		return
	}

	// A genuine user message re-arms new_session on a freshly-forked session:
	// the kickoff turn is over, and any pivot the user makes from here is a
	// real one the queen may legitimately fork on.
	if session.ForkKickoffPending {
		session.ForkKickoffPending = false
	}

	var body struct {
		Message string `json:"message"`
	}
	if err := decodeChatBody(r, &body); err != nil {
		if errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}

	if session.Chat == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "No LLM configured for this session",
		})
		return
	}

	// Publish before the turn starts so listeners (UI echo, memory recall)
	// observe the message ahead of the model's first stream event.
	publishClientInputReceived(session, body.Message)

	if err := session.Chat.Deliver(body.Message); err != nil {
		if errors.Is(err, ErrChatRuntimeClosed) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": "Session chat runtime is closed",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "queen", "delivered": true})
}

// publishClientInputReceived mirrors the upstream CLIENT_INPUT_RECEIVED emit,
// which carries the echo the composer reconciles against.
func publishClientInputReceived(session *Session, message string) {
	if session == nil || session.EventBus == nil {
		return
	}
	executionID := session.ID
	nodeID := "queen"
	correlationID := newChatCorrelationID()
	event := eventbus.NewAgentEvent(eventbus.EventClientInputReceived, "queen")
	event.NodeID = &nodeID
	event.ExecutionID = &executionID
	event.CorrelationID = &correlationID
	event.Data = map[string]any{"content": message, "image_count": 0}
	session.EventBus.Publish(event)
}

func newChatCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("chat-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// handleSessionEventsStream is the minimal session SSE channel the CLI's
// transcript tail attaches to: every bus event for the session, optionally
// filtered by ?types=a,b. The subscription handler only enqueues (dropping on
// backpressure) so a slow client cannot stall the publisher.
func handleSessionEventsStream(w http.ResponseWriter, r *http.Request, deps ChatRouteDeps) {
	session := chatSession(deps, r.PathValue("session_id"))
	if session == nil || session.EventBus == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	types := parseEventTypeFilter(r.URL.Query().Get("types"))
	queue := make(chan map[string]any, sessionEventsQueueSize)
	subID := session.EventBus.Subscribe(eventbus.AllEventTypes, func(event *eventbus.AgentEvent) {
		if event == nil {
			return
		}
		if len(types) > 0 {
			if _, wanted := types[string(event.Type)]; !wanted {
				return
			}
		}
		select {
		case queue <- event.ToDict():
		default:
		}
	})
	defer session.EventBus.Unsubscribe(subID)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	heartbeat := time.NewTicker(sessionEventsHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-queue:
			encoded, err := json.Marshal(payload)
			if err != nil {
				continue
			}
			eventType, _ := payload["type"].(string)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func parseEventTypeFilter(raw string) map[string]struct{} {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name != "" {
			out[name] = struct{}{}
		}
	}
	return out
}
