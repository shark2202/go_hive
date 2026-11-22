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
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
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
		// Upstream resolves the session through resolve_session, whose 404
		// envelope names the id; keep that wording for the chat route too.
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Session '" + sessionID + "' not found"})
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
		// DisplayMessage only affects the UI echo (and the CLIENT_INPUT_RECEIVED
		// event); the queen always receives the full prepared message.
		DisplayMessage *string          `json:"display_message"`
		Images         []map[string]any `json:"images"`
	}
	if err := decodeChatBody(r, &body); err != nil {
		if errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}

	// Save the attachments into the session and shape what the queen sees
	// before validating emptiness: a message may legitimately be just an
	// image, and upstream runs the same prepare first.
	message, images, savedImagePaths, err := prepareChatInput(session, body.Message, body.Images)
	if err != nil {
		log.Printf("chat route: prepare attachments failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	if strings.TrimSpace(message) == "" && len(images) == 0 {
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
	// observe the message ahead of the model's first stream event. The
	// correlation id rides into the runtime so the commit boundary carries the
	// same id, as upstream's deliver_to_queen → inject_event pair does.
	echo := message
	if body.DisplayMessage != nil {
		echo = *body.DisplayMessage
	}
	correlationID := publishClientInputReceived(session, echo, len(images), savedImagePaths)

	if err := session.Chat.DeliverClientInputWithImages(message, images, correlationID); err != nil {
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
// which carries the echo the composer reconciles against, the true attachment
// count and the saved attachment paths. It returns the correlation id so the
// runtime's CLIENT_INPUT_COMMITTED carries the same one.
func publishClientInputReceived(session *Session, message string, imageCount int, imagePaths []string) string {
	if session == nil || session.EventBus == nil {
		return ""
	}
	executionID := session.ID
	nodeID := "queen"
	correlationID := newChatCorrelationID()
	event := eventbus.NewAgentEvent(eventbus.EventClientInputReceived, "queen")
	event.NodeID = &nodeID
	event.ExecutionID = &executionID
	event.CorrelationID = &correlationID
	data := map[string]any{"content": message, "image_count": imageCount}
	if len(imagePaths) > 0 {
		// Paths to saved attachment files (relative to session dir) so the
		// frontend can reconstruct images on replay.
		data["image_paths"] = imagePaths
	}
	event.Data = data
	session.EventBus.Publish(event)
	return correlationID
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
//
// A fresh subscriber first gets the synthesized session_snapshot, then the
// buffered history upstream replays (the visible-chat subset — see
// sessionEventsReplayTypes), oldest first. Frames are data-only: upstream
// calls SSEResponse.send_event(data) without an event name, so the dashboard
// consumes them through EventSource.onmessage.
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
	if len(types) == 0 {
		// Upstream's handle_events falls back to DEFAULT_EVENT_TYPES when no
		// ?types= filter is given: the session feed is a curated chat feed,
		// not every bus event. Without this a Go subscriber saw events
		// (identity, reminders, stall diagnostics) a Python subscriber never
		// received live.
		types = defaultSessionEventTypes
	}
	// Worker chatter is opt-in per worker, as upstream's _parse_watch: every
	// worker's META events still pass (they render its bubble), but per-turn
	// detail only reaches a client that asked to watch that worker.
	watchAll, watched := eventbus.ParseWatch(r.URL.Query().Get("watch"))
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
		payload := event.ToDict()
		if eventbus.IsSuppressedForClient(payload, watchAll, watched) {
			return
		}
		select {
		case queue <- payload:
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

	// Parity: framework/server/routes_events.py::handle_events — a fresh
	// subscriber always gets a synthesized session_snapshot as the first
	// frame, so the dashboard rehydrates "queen busy / awaiting input" before
	// any live event and never starts from a stale spinner.
	snapshot := eventbus.ComputeSessionSnapshot(session.EventBus)
	snapshotEvent := eventbus.NewAgentEvent(eventbus.EventSessionSnapshot, chatStreamID)
	snapshotEvent.NodeID = eventbus.StrPtr(chatNodeID)
	if executionID, ok := snapshot["current_execution_id"].(string); ok && executionID != "" {
		snapshotEvent.ExecutionID = eventbus.StrPtr(executionID)
	}
	snapshotEvent.Data = snapshot
	queue <- snapshotEvent.ToDict()

	// Replay the buffered events that produce visible chat messages, oldest
	// first, so a reconnect that lands after the opening turn still sees its
	// output. Execution/node lifecycle is not replayed upstream either; the
	// exceptions are the visible-chat types mirrored in
	// sessionEventsReplayTypes. An already-answered client_input_requested is
	// suppressed so a resolved question never re-opens.
	replaySessionEvents(session.EventBus, queue, types, watchAll, watched)

	heartbeat := time.NewTicker(sessionEventsHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-queue:
			writeSSEData(w, flusher, payload)
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// sessionEventsReplayTypes mirrors routes_events.py::_REPLAY_TYPES: the subset
// of buffered history replayed to a (re)connecting client. Lifecycle events
// are excluded upstream to avoid duplicate state transitions; trigger/task
// events are included because they are idempotent state setters.
var sessionEventsReplayTypes = map[eventbus.EventType]struct{}{
	eventbus.EventClientOutputDelta:    {},
	eventbus.EventExecutionStarted:     {},
	eventbus.EventExecutionCompleted:   {},
	eventbus.EventLLMTurnComplete:      {},
	eventbus.EventToolCallStarted:      {},
	eventbus.EventToolCallCompleted:    {},
	eventbus.EventClientInputRequested: {},
	eventbus.EventClientInputReceived:  {},
	eventbus.EventClientInputCommitted: {},
	eventbus.EventTriggerAvailable:     {},
	eventbus.EventTriggerActivated:     {},
	eventbus.EventTriggerDeactivated:   {},
	eventbus.EventTriggerFired:         {},
	eventbus.EventTriggerRemoved:       {},
	eventbus.EventTriggerUpdated:       {},
	eventbus.EventTaskCreated:          {},
	eventbus.EventTaskUpdated:          {},
	eventbus.EventTaskDeleted:          {},
	eventbus.EventTaskListReset:        {},
	eventbus.EventSessionForked:        {},
}

// defaultSessionEventTypes mirrors routes_events.py::DEFAULT_EVENT_TYPES: the
// curated chat feed a session subscriber receives when it passes no ?types=
// filter. Keep this list in sync with the Python enum — the upstream set is
// the contract (49 entries).
var defaultSessionEventTypes = map[string]struct{}{
	"session_snapshot":                 {},
	"client_output_delta":              {},
	"client_input_requested":           {},
	"client_input_received":            {},
	"client_input_committed":           {},
	"llm_text_delta":                   {},
	"llm_reasoning_delta":              {},
	"client_reasoning":                 {},
	"tool_call_started":                {},
	"tool_call_completed":              {},
	"execution_started":                {},
	"execution_completed":              {},
	"execution_failed":                 {},
	"execution_paused":                 {},
	"payment_required":                 {},
	"node_loop_started":                {},
	"node_loop_iteration":              {},
	"node_loop_completed":              {},
	"llm_turn_complete":                {},
	"goal_progress":                    {},
	"node_internal_output":             {},
	"node_stalled":                     {},
	"node_retry":                       {},
	"node_tool_doom_loop":              {},
	"loop_state_changed":               {},
	"context_compaction_started":       {},
	"context_compacted":                {},
	"context_usage_updated":            {},
	"worker_colony_loaded":             {},
	"colony_created":                   {},
	"colony_suggestion_requested":      {},
	"credentials_required":             {},
	"client_credential_form_requested": {},
	"skill_install_requested":          {},
	"skill_install_decided":            {},
	"subagent_report":                  {},
	"queen_phase_changed":              {},
	"trigger_available":                {},
	"trigger_activated":                {},
	"trigger_deactivated":              {},
	"trigger_fired":                    {},
	"trigger_removed":                  {},
	"trigger_updated":                  {},
	"task_created":                     {},
	"task_updated":                     {},
	"task_deleted":                     {},
	"task_list_reset":                  {},
	"session_forked":                   {},
	"session_title_updated":            {},
}

// replaySessionEvents queues the replayable slice of the bus history onto the
// subscriber, oldest first. Worker per-turn chatter is suppressed for clients
// that never asked to watch that worker (upstream's is_suppressed_for_client),
// exactly as the live handler does.
func replaySessionEvents(bus *eventbus.EventBus, queue chan map[string]any, types map[string]struct{}, watchAll bool, watched map[string]struct{}) {
	if bus == nil {
		return
	}
	history := bus.GetHistory(eventbus.WithHistoryLimit(-1))
	if len(history) == 0 {
		return
	}
	resolved := eventbus.CollectResolvedRequestSeqs(bus)
	// GetHistory returns newest first; replay walks chronologically.
	for i := len(history) - 1; i >= 0; i-- {
		event := history[i]
		if event == nil {
			continue
		}
		if _, ok := sessionEventsReplayTypes[event.Type]; !ok {
			continue
		}
		// Upstream intersects the replay set with the subscriber's filter
		// (`_REPLAY_TYPES & event_type_values`): a client that asked for one
		// type must not receive the visible-chat subset it did not ask for.
		if len(types) > 0 {
			if _, wanted := types[string(event.Type)]; !wanted {
				continue
			}
		}
		payload := event.ToDict()
		if eventbus.IsSuppressedForClient(payload, watchAll, watched) {
			continue
		}
		if event.Type == eventbus.EventClientInputRequested {
			if _, answered := resolved[event.Seq]; answered {
				continue
			}
		}
		select {
		case queue <- payload:
		default:
			// Queue full: the client is behind; the snapshot already carried
			// the state, and the live stream picks up from here.
			return
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
		if name == "" {
			continue
		}
		// Upstream warns and drops unknown names; a filter that resolves to
		// nothing falls back to the default set at the call site, exactly like
		// _parse_event_types' `result or DEFAULT_EVENT_TYPES`.
		if _, known := eventbus.LookupEventType(name); !known {
			continue
		}
		out[name] = struct{}{}
	}
	return out
}
