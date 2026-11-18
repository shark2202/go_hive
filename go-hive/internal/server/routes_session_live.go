package server

// Parity: framework/server/routes_sessions.py::handle_get_session_snapshot
// Parity: framework/server/routes_sessions.py::handle_live_sessions_stream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/runtimehealth"
)

const livePollInterval = 2500 * time.Millisecond

const liveHeartbeatInterval = 30 * time.Second

type activeWorkerCounter interface {
	ActiveWorkerCount() int
}

func handleGetSessionSnapshot(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	if deps.Manager == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	session := deps.Manager.GetSession(r.PathValue("session_id"))
	if session == nil || session.EventBus == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	writeJSON(w, http.StatusOK, eventbus.ComputeSessionSnapshot(session.EventBus))
}

func handleLiveSessionsStream(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll := deps.LivePollInterval
	if poll <= 0 {
		poll = livePollInterval
	}
	network := deps.RuntimeNetwork
	if network == nil {
		network = runtimehealth.Current
	}

	lastSignature := ""
	var lastEmit time.Time
	for {
		rows := liveSessionRows(deps)
		state := network()
		signature := liveSignature(rows, state)
		now := time.Now()
		if signature != lastSignature || now.Sub(lastEmit) > liveHeartbeatInterval {
			payload, err := json.Marshal(map[string]any{"sessions": rows, "network": state})
			if err == nil {
				fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", payload)
				flusher.Flush()
			}
			lastSignature = signature
			lastEmit = now
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(poll):
		}
	}
}

func liveSessionRows(deps SessionRouteDeps) []map[string]any {
	var sessions []*Session
	if deps.Manager != nil {
		sessions = deps.Manager.ListSessions()
	} else if deps.LiveSessions != nil {
		sessions = deps.LiveSessions()
	}
	rows := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		rows = append(rows, liveSessionSummary(session))
	}
	sort.Slice(rows, func(i, j int) bool {
		left, _ := rows[i]["session_id"].(string)
		right, _ := rows[j]["session_id"].(string)
		return left < right
	})
	return rows
}

func liveSessionSummary(session *Session) map[string]any {
	var snap map[string]any
	if session.EventBus != nil {
		snap = eventbus.ComputeSessionSnapshot(session.EventBus)
	}
	tools, _ := snap["current_tool_calls"].([]any)
	var primaryTool any
	if len(tools) > 0 {
		if entry, isMap := tools[0].(map[string]any); isMap {
			primaryTool = entry["name"]
		}
	}

	var queenID any
	var queenName any
	var phase any
	if phaseState, ok := session.PhaseState.(SessionPhaseState); ok && phaseState != nil {
		phase = phaseState.Phase()
		if id := phaseState.QueenID(); id != "" {
			queenID = id
		}
		if profile := phaseState.QueenProfile(); profile != nil {
			if name, present := profile["name"]; present {
				queenName = name
			}
		}
	}

	var colonyID any
	if session.ColonyID != "" {
		colonyID = session.ColonyID
	}

	activeWorkers := 0
	if counter, ok := session.Colony.(activeWorkerCounter); ok && counter != nil {
		activeWorkers = counter.ActiveWorkerCount()
	}

	return map[string]any{
		"session_id":          session.ID,
		"colony_id":           colonyID,
		"queen_id":            queenID,
		"queen_name":          queenName,
		"phase":               phase,
		"is_executing":        truthy(snap["is_executing"]),
		"awaiting_input":      truthy(snap["awaiting_input"]),
		"interrupted":         truthy(snap["interrupted"]),
		"interrupt_cause":     snap["interrupt_cause"],
		"queen_busy_reason":   snap["queen_busy_reason"],
		"park_reason":         snap["park_reason"],
		"current_tool_name":   primaryTool,
		"current_tool_count":  len(tools),
		"active_worker_count": activeWorkers,
		"last_event_at":       snap["last_event_at"],
		"snapshot_seq":        snapshotSeq(snap["snapshot_seq"]),
	}
}

func liveSignature(rows []map[string]any, network runtimehealth.Snapshot) string {
	tuples := make([]any, 0, len(rows)+1)
	for _, row := range rows {
		tuples = append(tuples, []any{
			row["session_id"],
			row["is_executing"],
			row["awaiting_input"],
			row["interrupted"],
			row["current_tool_name"],
			row["queen_busy_reason"],
			row["park_reason"],
			row["active_worker_count"],
		})
	}
	tuples = append(tuples, []any{"__network__", network.Degraded, derefString(network.Reason)})
	data, err := json.Marshal(tuples)
	if err != nil {
		return ""
	}
	return string(data)
}

func snapshotSeq(value any) any {
	if number, ok := value.(json.Number); ok {
		return number
	}
	if value == nil {
		return 0
	}
	return value
}

func derefString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && parsed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
