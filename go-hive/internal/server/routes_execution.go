package server

// Parity: framework/server/routes_execution.py::handle_queen_context
// Parity: framework/server/routes_execution.py::handle_record_user_message
// Parity: framework/server/routes_execution.py::handle_cancel_queen
// Parity: framework/server/routes_execution.py::handle_session_presence
// Parity: framework/server/routes_execution.py::handle_mark_colony_spawned

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

type ExecutionRouteDeps struct {
	Manager     *SessionManager
	Home        hivehome.Home
	ReviveQueen func(*Session) error
	CancelWait  time.Duration
}

func RegisterExecutionRoutes(mux *http.ServeMux, deps ExecutionRouteDeps) {
	mux.HandleFunc("POST /api/sessions/{session_id}/queen-context", func(w http.ResponseWriter, r *http.Request) {
		handleQueenContext(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/record-message", func(w http.ResponseWriter, r *http.Request) {
		handleRecordUserMessage(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/cancel-queen", func(w http.ResponseWriter, r *http.Request) {
		handleCancelQueen(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/presence", func(w http.ResponseWriter, r *http.Request) {
		handleSessionPresence(w, r, deps)
	})
	mux.HandleFunc("POST /api/sessions/{session_id}/mark-colony-spawned", func(w http.ResponseWriter, r *http.Request) {
		handleMarkColonySpawned(w, r, deps)
	})
}

func resolveExecutionSession(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) (*Session, bool) {
	sessionID := r.PathValue("session_id")
	if deps.Manager != nil {
		if session := deps.Manager.GetSession(sessionID); session != nil {
			return session, true
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "Session '" + sessionID + "' not found"})
	return nil, false
}

func handleQueenContext(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	session, ok := resolveExecutionSession(w, r, deps)
	if !ok {
		return
	}
	decoded, err := decodeJSONValue(r)
	body, objectOK := decoded.(map[string]any)
	if err != nil || !objectOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	rawMessage, present := body["message"]
	if !present || !pyTruthy(rawMessage) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}
	message := pythonString(rawMessage)

	if found, injectErr := injectQueenEvent(session, message, false); found {
		if injectErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": injectErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "delivered": true})
		return
	}

	if deps.ReviveQueen != nil {
		if reviveErr := deps.ReviveQueen(session); reviveErr == nil {
			if found, injectErr := injectQueenEvent(session, message, false); found && injectErr == nil {
				writeJSON(w, http.StatusOK, map[string]any{"status": "queued_revived", "delivered": true})
				return
			}
		}
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "Queen not available"})
}

func injectQueenEvent(session *Session, message string, isClientInput bool) (bool, error) {
	if session == nil || session.QueenExecutor == nil {
		return false, nil
	}
	node := session.QueenExecutor.QueenNode()
	if node == nil {
		return false, nil
	}
	return true, node.InjectEvent(message, isClientInput)
}

func handleSessionPresence(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	if _, ok := resolveExecutionSession(w, r, deps); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resumed": false})
}

type queenTurnCanceller interface {
	CancelCurrentTurn() []<-chan struct{}
}

type queenUserStopper interface {
	MarkUserStopped()
}

type colonyWorkerStopper interface {
	StopWorkers(blockDispatch bool) (map[string]any, error)
}

func handleCancelQueen(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	session, ok := resolveExecutionSession(w, r, deps)
	if !ok {
		return
	}
	if session.QueenExecutor == nil {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": false, "error": "Queen not active"})
		return
	}
	node := session.QueenExecutor.QueenNode()
	if node == nil {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": false, "error": "Queen node not found"})
		return
	}
	canceller, canCancel := node.(queenTurnCanceller)
	if !canCancel {
		writeJSON(w, http.StatusOK, map[string]any{"cancelled": false, "error": "Queen node not found"})
		return
	}

	// Flag the stop before cancelling: the cancelled stream can park the loop
	// on its next tick, and the idle-nudge gate must already see the stop.
	if stopper, ok := node.(queenUserStopper); ok {
		stopper.MarkUserStopped()
	}
	tasks := canceller.CancelCurrentTurn()
	if len(tasks) > 0 {
		waitBudget := deps.CancelWait
		if waitBudget <= 0 {
			waitBudget = 500 * time.Millisecond
		}
		deadline := time.After(waitBudget)
		for _, task := range tasks {
			if task == nil {
				continue
			}
			select {
			case <-task:
			case <-deadline:
			}
		}
	}

	workers := map[string]any{}
	if session.Colony != nil {
		if stopper, ok := session.Colony.(colonyWorkerStopper); ok {
			result, err := stopper.StopWorkers(true)
			if err != nil {
				workers = map[string]any{"error": err.Error()}
			} else if result != nil {
				workers = result
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": true, "workers": workers})
}

func handleRecordUserMessage(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	sessionID := r.PathValue("session_id")
	decoded, err := decodeJSONValue(r)
	body, objectOK := decoded.(map[string]any)
	if err != nil || !objectOK {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	rawMessage, present := body["message"]
	if !present || !pyTruthy(rawMessage) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}
	message := pythonString(rawMessage)

	sessionDir, _ := FindQueenSessionDir(deps.Home, sessionID)
	if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "session not found"})
		return
	}
	conversations := filepath.Join(sessionDir, "conversations")
	if err := os.MkdirAll(conversations, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	store := agentloop.NewFileConversationStore(conversations)

	cursor, _ := store.ReadCursor()
	if cursor == nil {
		cursor = map[string]any{}
	}
	nextSeq := 0
	if existing := agentloop.GetCursorNextSeq(cursor); existing != nil {
		nextSeq = *existing
	} else {
		parts, _ := store.ReadParts()
		maxSeq := -1
		for _, part := range parts {
			if seq, ok := pythonIntLoose(part["seq"]); ok && int(seq) > maxSeq {
				maxSeq = int(seq)
			}
		}
		if len(parts) > 0 {
			nextSeq = maxSeq + 1
		}
	}

	msg := agentloop.Message{Seq: nextSeq, Role: agentloop.RoleUser, Content: message, IsClientInput: true}
	if err := store.WritePart(nextSeq, msg.ToStorageDict()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	updatedCursor := agentloop.UpdateCursorNextSeq(cursor, nextSeq+1)
	updatedCursor["pending_input"] = nil
	if err := store.WriteCursor(updatedCursor); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	maxEventSeq := maxEventSequence(filepath.Join(sessionDir, "events.jsonl"))
	event := eventbus.NewAgentEvent(eventbus.EventClientInputReceived, "queen")
	nodeID := "queen"
	executionID := sessionID
	event.NodeID = &nodeID
	event.ExecutionID = &executionID
	event.Data = map[string]any{"content": message, "image_count": 0}
	event.Seq = maxEventSeq + 1
	line, err := marshalJSONLine(event.ToDict())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	file, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		file.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := file.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"status": "recorded", "seq": nextSeq})
}

func maxEventSequence(eventsPath string) int {
	file, err := os.Open(eventsPath)
	if err != nil {
		return -1
	}
	defer file.Close()
	maxSeq := -1
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		trimmed := strings.Trim(line, " 	\n\r\v\f")
		if trimmed != "" {
			if decoded, decodeErr := decodeJSONBytes([]byte(trimmed)); decodeErr == nil {
				if object, ok := decoded.(map[string]any); ok {
					if seq, ok := eventSequenceValue(object["seq"]); ok && seq > maxSeq {
						maxSeq = seq
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	return maxSeq
}

// eventSequenceValue mirrors `isinstance(seq, int)`: JSON strings and floats
// are ignored, while json.Number integers and booleans count.
func eventSequenceValue(value any) (int, bool) {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := strconv.ParseInt(typed.String(), 10, 64); err == nil {
			return int(parsed), true
		}
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case float64:
		if typed == float64(int(typed)) {
			return int(typed), true
		}
	}
	return 0, false
}

func marshalJSONLine(value map[string]any) ([]byte, error) {
	buffer := &strings.Builder{}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(buffer.String(), "\n")), nil
}

func persistColonySpawnLock(session *Session, colonyID string) error {
	if session == nil {
		return errors.New("session is required")
	}
	queenDir := session.QueenDir
	if queenDir == "" {
		session.ColonySpawned = true
		session.SpawnedColonyID = colonyID
		return nil
	}
	metaPath := filepath.Join(queenDir, "meta.json")
	meta := map[string]any{}
	if data, err := os.ReadFile(metaPath); err == nil {
		if decoded, decodeErr := decodeJSONBytes(data); decodeErr == nil {
			if object, ok := decoded.(map[string]any); ok {
				meta = object
			}
		}
	}
	meta["colony_spawned"] = true
	meta["spawned_colony_id"] = colonyID
	meta["spawned_colony_at"] = pythonISOFormat(time.Now().UTC())
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return err
	}
	rendered, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(metaPath, rendered, 0o644); err != nil {
		return err
	}
	session.ColonySpawned = true
	session.SpawnedColonyID = colonyID
	return nil
}

func pythonISOFormat(t time.Time) string {
	base := t.Format("2006-01-02T15:04:05")
	if micros := t.Nanosecond() / 1000; micros != 0 {
		base += "." + strconv.Itoa(1000000 + micros)[1:]
	}
	return base + "+00:00"
}

func handleMarkColonySpawned(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	session, ok := resolveExecutionSession(w, r, deps)
	if !ok {
		return
	}
	body := map[string]any{}
	if r.ContentLength != 0 {
		if decoded, err := decodeJSONValue(r); err == nil {
			if object, ok := decoded.(map[string]any); ok {
				body = object
			}
		}
	}
	colonyID := strings.TrimSpace(pythonString(body["colony_id"]))
	if colonyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "colony_id is required"})
		return
	}
	if err := persistColonySpawnLock(session, colonyID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to persist: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":        session.ID,
		"colony_spawned":    true,
		"spawned_colony_id": colonyID,
	})
}
