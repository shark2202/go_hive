package server

// Parity: framework/server/routes_execution.py::handle_queen_context
// Parity: framework/server/routes_execution.py::handle_record_user_message
// Parity: framework/server/routes_execution.py::handle_cancel_queen
// Parity: framework/server/routes_execution.py::handle_session_presence
// Parity: framework/server/routes_execution.py::handle_mark_colony_spawned
// Parity: framework/server/routes_execution.py::handle_compact_and_fork

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	sessionstore "github.com/aden-hive/hive-go/internal/session"
)

type ExecutionRouteDeps struct {
	Manager     *SessionManager
	Home        hivehome.Home
	ReviveQueen func(*Session) error
	CancelWait  time.Duration

	// CreateSession starts the live session a compact-and-fork lands in. It is
	// the Go stand-in for manager.create_session(queen_resume_from=...): the
	// server package has no session factory of its own. Nil means this build
	// has no factory wired, and the route answers 503 instead of reporting a
	// fork nobody can resume.
	CreateSession func(CreateSessionParams) (*Session, error)

	// GenerateSessionID mints the fork target's id; tests pin it.
	GenerateSessionID func(time.Time) (string, error)
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
	mux.HandleFunc("POST /api/sessions/{session_id}/compact-and-fork", func(w http.ResponseWriter, r *http.Request) {
		handleCompactAndFork(w, r, deps)
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

// forkContextTokens is the queen window upstream falls back to when the loop
// config carries no max_context_tokens (_queen_window_tokens). The Go chat
// runtime keeps that value in unexported conversation options, so the fallback
// is the window size reachable from here.
const forkContextTokens = 180_000

// ForkSessionError mirrors routes_execution.ForkSessionError: a fork failure
// carrying the HTTP status the route answers with.
type ForkSessionError struct {
	Message string
	Status  int
}

func (e *ForkSessionError) Error() string { return e.Message }

func handleCompactAndFork(w http.ResponseWriter, r *http.Request, deps ExecutionRouteDeps) {
	session, ok := resolveExecutionSession(w, r, deps)
	if !ok {
		return
	}
	result, forkErr := forkQueenSessionForSplit(session, deps)
	if forkErr != nil {
		writeJSON(w, forkErr.Status, map[string]any{"error": forkErr.Message})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// forkQueenSessionForSplit is the HTTP (no tasks) mode of upstream's
// fork_queen_session_for_split: carry the old conversation across as an
// LLM-compacted summary in a fresh session dir, then start a session on it.
// The old session stays alive; the response carries the successor id.
func forkQueenSessionForSplit(session *Session, deps ExecutionRouteDeps) (map[string]any, *ForkSessionError) {
	queenDir := session.QueenDir
	if queenDir == "" && deps.Home.Root != "" {
		if found, err := FindQueenSessionDir(deps.Home, session.ID); err == nil {
			queenDir = found
		}
	}
	if queenDir == "" || !entryIsDir(queenDir) {
		return nil, &ForkSessionError{Message: "queen session directory not found", Status: http.StatusNotFound}
	}
	if deps.Home.Root == "" {
		return nil, &ForkSessionError{Message: "hive home is not configured", Status: http.StatusInternalServerError}
	}

	// Compaction needs a stamped queen context with an LLM. Go keeps that on
	// Session.LLM; a session with no runtime has nothing to compact with.
	if session.Chat == nil && session.QueenExecutor == nil {
		return nil, &ForkSessionError{Message: "queen is not running", Status: http.StatusServiceUnavailable}
	}
	completer, _ := session.LLM.(llm.Completer)
	if completer == nil {
		return nil, &ForkSessionError{
			Message: "queen context not yet stamped (no LLM available for compaction). Send a message to the queen and retry.",
			Status:  http.StatusServiceUnavailable,
		}
	}

	queenName := session.QueenName
	if queenName == "" {
		queenName = "default"
	}
	generate := deps.GenerateSessionID
	if generate == nil {
		generate = sessionstore.NewStore("").GenerateID
	}
	newSessionID, err := generate(time.Now())
	if err != nil {
		return nil, &ForkSessionError{
			Message: "failed to generate forked session id: " + err.Error(),
			Status:  http.StatusInternalServerError,
		}
	}
	newDir := deps.Home.QueenSessionDir(queenName, newSessionID)
	if pathExists(newDir) {
		// Same-second collision would clobber another session.
		return nil, &ForkSessionError{
			Message: "new session dir collision: " + newDir,
			Status:  http.StatusInternalServerError,
		}
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return nil, &ForkSessionError{
			Message: "failed to create forked session dir: " + err.Error(),
			Status:  http.StatusInternalServerError,
		}
	}
	// exist_ok=False upstream: the leaf must be created by this request.
	if err := os.Mkdir(newDir, 0o755); err != nil {
		return nil, &ForkSessionError{
			Message: "failed to create forked session dir: " + err.Error(),
			Status:  http.StatusInternalServerError,
		}
	}

	summary, err := compactQueenConversationInto(queenDir, newDir, completer, forkContextTokens)
	if err != nil {
		return nil, &ForkSessionError{
			Message: "compaction failed: " + err.Error(),
			Status:  http.StatusInternalServerError,
		}
	}
	if summary == nil {
		return nil, &ForkSessionError{
			Message: "queen conversation is empty -- nothing to compact",
			Status:  http.StatusBadRequest,
		}
	}

	// Provenance only, no old-session state. A failed write is logged rather
	// than fatal: the summary is already on disk (upstream warns here too).
	newMeta := map[string]any{
		"queen_id":       queenName,
		"compacted_from": session.ID,
		"created_at":     float64(time.Now().UnixNano()) / 1e9,
		"compacted_at":   pythonISOFormat(time.Now().UTC()),
	}
	if rendered, marshalErr := json.Marshal(newMeta); marshalErr == nil {
		if writeErr := os.WriteFile(filepath.Join(newDir, "meta.json"), rendered, 0o644); writeErr != nil {
			log.Printf("fork_queen_session: failed to write new meta.json: %v", writeErr)
		}
	}

	// Upstream delegates this to manager.create_session. Without the factory
	// the fork would leave a compacted directory no session ever resumes, so
	// the route refuses instead of reporting a fork that cannot be used.
	if deps.CreateSession == nil {
		return nil, &ForkSessionError{
			Message: "forked session creation is not wired on this server: no session factory is configured for the new session",
			Status:  http.StatusServiceUnavailable,
		}
	}
	independentPhase := "independent"
	created, err := deps.CreateSession(CreateSessionParams{
		// Upstream passes session_id=None and resumes from the new dir; the Go
		// factory keys the session directory off SessionID, so pin the id the
		// summary was written under.
		SessionID:       &newSessionID,
		QueenResumeFrom: &newSessionID,
		QueenName:       &queenName,
		InitialPhase:    &independentPhase,
	})
	if err != nil {
		return nil, &ForkSessionError{
			Message: "failed to start forked session: " + err.Error(),
			Status:  http.StatusInternalServerError,
		}
	}
	if created == nil {
		return nil, &ForkSessionError{
			Message: "failed to start forked session: session factory returned no session",
			Status:  http.StatusInternalServerError,
		}
	}
	if created.ID != "" && created.ID != newSessionID {
		return nil, &ForkSessionError{
			Message: "failed to start forked session: session factory opened '" + created.ID +
				"', expected '" + newSessionID + "'; the compacted summary would be orphaned",
			Status: http.StatusInternalServerError,
		}
	}

	return map[string]any{
		"new_session_id":     newSessionID,
		"queen_id":           queenName,
		"compacted_from":     session.ID,
		"summary_chars":      summary.Chars,
		"messages_compacted": summary.Messages,
		"task_ids":           []string{},
	}, nil
}

type compactionSummary struct {
	Messages int
	Chars    int
}

// compactQueenConversationInto mirrors _compact_queen_conversation_in_place
// with dest_dir set: the source transcript is left untouched and the fresh
// session gets only the summary. nil, nil means "nothing to compact".
func compactQueenConversationInto(queenDir, destDir string, completer llm.Completer, maxContextTokens int) (*compactionSummary, error) {
	sourceConvs := filepath.Join(queenDir, "conversations")
	if !entryIsDir(sourceConvs) {
		return nil, nil
	}
	parts, err := agentloop.NewFileConversationStore(sourceConvs).ReadParts()
	if err != nil {
		return nil, err
	}
	messages := make([]agentloop.Message, 0, len(parts))
	for _, part := range parts {
		// Python's Message.from_storage_dict raises on malformed parts and the
		// caller skips them; the Go reader is lenient, so screen the required
		// fields here instead of compacting a half-built message.
		if _, ok := part["role"].(string); !ok {
			log.Printf("compact_in_place: skipping malformed part %v", part)
			continue
		}
		if _, ok := part["content"].(string); !ok {
			log.Printf("compact_in_place: skipping malformed part %v", part)
			continue
		}
		messages = append(messages, agentloop.MessageFromStorageDict(part))
	}
	if len(messages) == 0 {
		return nil, nil
	}

	summary, err := agentloop.LlmCompact(context.Background(), completer, messages, agentloop.LlmCompactOptions{
		MaxContextTokens:     maxContextTokens,
		PreserveUserMessages: true,
	})
	if err != nil {
		return nil, err
	}

	destConvs := filepath.Join(destDir, "conversations")
	if err := os.MkdirAll(destConvs, 0o755); err != nil {
		return nil, err
	}
	destStore := agentloop.NewFileConversationStore(destConvs)
	defer destStore.Close()

	summaryMsg := agentloop.Message{Seq: 0, Role: agentloop.RoleUser, Content: summary}
	if err := destStore.WritePart(0, summaryMsg.ToStorageDict()); err != nil {
		return nil, err
	}
	if err := destStore.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		return nil, err
	}
	if err := destStore.WriteMeta(map[string]any{
		"system_prompt":      "",
		"max_context_tokens": maxContextTokens,
	}); err != nil {
		return nil, err
	}
	return &compactionSummary{Messages: len(messages), Chars: utf8.RuneCountInString(summary)}, nil
}
