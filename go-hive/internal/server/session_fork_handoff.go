package server

// Parity: framework/server/routes_execution.py::fork_queen_session_for_split
// (the handoff/tasks mode used by task_create(new_session=true)).
//
// The HTTP compact-and-fork path lives in routes_execution.go and carries the
// old conversation across as an LLM summary. This file is the other mode: the
// new session inherits ONLY the queen-authored handoff brief, seeded together
// with the task plan she just wrote. No LLM compaction runs, so the fork stays
// inside one tool call's time budget.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	sessionstore "github.com/shark2202/go_hive/go-hive/internal/session"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
)

// HandoffForkDeps is everything the tool-driven fork needs from the process
// that owns the live sessions. It mirrors ExecutionRouteDeps' fork fields and
// is kept separate so the tasks tool layer never imports the server route
// types.
type HandoffForkDeps struct {
	Manager           *SessionManager
	Home              hivehome.Home
	CreateSession     func(CreateSessionParams) (*Session, error)
	GenerateSessionID func(time.Time) (string, error)
}

// handoffKickoffPrompt is the synthetic first turn of a forked session,
// verbatim upstream. The <hive-internal> tag marks it as a backend directive so
// the dashboard does not render it as a user bubble.
const handoffKickoffPrompt = "<hive-internal>\n" +
	"This is a fresh session created to carry out a task plan " +
	"that has ALREADY been set up for you — call task_list to " +
	"see it. Do NOT call task_create, and do NOT start another " +
	"new session: the plan already exists and this IS the new " +
	"session. Begin immediately — task_update the first task to " +
	"in_progress and work straight through the list."

// HandoffPayload is the tool-result body task_create(new_session=true) returns
// on success. The shape follows the Python tool result field for field.
func HandoffPayload(result map[string]any) map[string]any {
	taskIDs := result["task_ids"]
	if taskIDs == nil {
		taskIDs = []int{}
	}
	count := 0
	switch typed := taskIDs.(type) {
	case []int:
		count = len(typed)
	case []any:
		count = len(typed)
	}
	return map[string]any{
		"success":        true,
		"new_session":    true,
		"new_session_id": result["new_session_id"],
		"compacted_from": result["compacted_from"],
		"task_ids":       taskIDs,
		"message": "Forked into a fresh session (" + pythonString(result["new_session_id"]) +
			") and seeded " + strconv.Itoa(count) + " task(s). The user has been silently swapped there.",
		"instruction": "The user is now in the new session, where this task plan lives. " +
			"Do NOT produce any further response in this turn — end_turn now. " +
			"The new session's queen picks up the user's latest message and " +
			"works the plan there.",
	}
}

// ForkQueenSessionWithHandoff creates the successor session for a DM queen
// pivot: a brand-new session directory holding the handoff brief, the seeded
// task plan, provenance meta, and a live runtime resumed onto the plan. The old
// session is marked superseded so /chat locks it; its loop ends after the
// in-flight turn (upstream stops it once TOOL_CALL_COMPLETED fires — the Go
// runtime has no equivalent mid-turn cancellation seam, so the lock is what
// retires it).
//
// Returns {new_session_id, queen_id, compacted_from, task_ids}. Errors are
// queen-readable: the tool wraps them as {"success": false, "error": ...}.
func ForkQueenSessionWithHandoff(session *Session, deps HandoffForkDeps, handoff string, taskSpecs []map[string]any, goal string) (map[string]any, error) {
	if session == nil {
		return nil, &ForkSessionError{Message: "session is required", Status: 500}
	}
	handoff = strings.TrimSpace(handoff)
	if handoff == "" {
		return nil, &ForkSessionError{
			Message: "new_session=true requires a `handoff`, and you did not provide one. " +
				"The new session inherits NOTHING from this conversation — without a handoff it " +
				"starts blind. Call task_create again with a COMPLETE, objective handoff: the " +
				"user's goal in their terms, the concrete data (names, URLs, IDs, file paths, " +
				"the account to use, exact requirements), decisions made and options ruled " +
				"out, constraints, and what 'done' looks like.",
			Status: 400,
		}
	}
	if deps.CreateSession == nil {
		return nil, &ForkSessionError{
			Message: "forked session creation is not wired on this server: no session factory is configured",
			Status:  503,
		}
	}
	queenDir := session.QueenDir
	if queenDir == "" && deps.Home.Root != "" {
		if found, err := FindQueenSessionDir(deps.Home, session.ID); err == nil {
			queenDir = found
		}
	}
	if queenDir == "" || !entryIsDir(queenDir) {
		return nil, &ForkSessionError{Message: "queen session directory not found", Status: 404}
	}
	if deps.Home.Root == "" {
		return nil, &ForkSessionError{Message: "hive home is not configured", Status: 500}
	}

	// A chat app's thread (a QQ group, say) is one long conversation, and the
	// channel would follow the queen into a session carrying only her handoff:
	// everything said there so far would be lost. Her plan belongs in the
	// session the thread is in. Upstream discriminates handoff mode with
	// bool(tasks), so only the tool path (tasks provided) is gated; the gate
	// must run before any new session id, dir, task or session exists.
	channel := session.Channel()
	if len(taskSpecs) > 0 && channel != "" {
		return nil, &ForkSessionError{
			Message: fmt.Sprintf(
				"This conversation comes from the %s chat channel and stays in this session: "+
					"a new one would leave behind everything said here. Create the plan in this session "+
					"(task_create without new_session).",
				channel,
			),
			Status: 409,
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
		return nil, &ForkSessionError{Message: "failed to generate forked session id: " + err.Error(), Status: 500}
	}
	newDir := deps.Home.QueenSessionDir(queenName, newSessionID)
	if pathExists(newDir) {
		return nil, &ForkSessionError{Message: "new session dir collision: " + newDir, Status: 500}
	}
	if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
		return nil, &ForkSessionError{Message: "failed to create forked session dir: " + err.Error(), Status: 500}
	}
	// exist_ok=False upstream: the leaf is created by this fork.
	if err := os.Mkdir(newDir, 0o755); err != nil {
		return nil, &ForkSessionError{Message: "failed to create forked session dir: " + err.Error(), Status: 500}
	}

	// The handoff brief is the new session's ONLY inherited context.
	if err := writeHandoffSeedMessage(newDir, handoff); err != nil {
		return nil, &ForkSessionError{Message: "failed to seed the fork handoff: " + err.Error(), Status: 500}
	}

	newMeta := map[string]any{
		"queen_id":       queenName,
		"compacted_from": session.ID,
		"created_at":     float64(time.Now().UnixNano()) / 1e9,
	}
	if rendered, marshalErr := json.Marshal(newMeta); marshalErr == nil {
		if writeErr := os.WriteFile(filepath.Join(newDir, "meta.json"), rendered, 0o644); writeErr != nil {
			log.Printf("fork_queen_session(handoff): failed to write new meta.json: %v", writeErr)
		}
	}

	// Seed the plan into the fresh session before it goes live, so the new
	// queen resumes onto an existing list. The dir is fresh: ids start at #1.
	var goalPtr *string
	if trimmed := strings.TrimSpace(goal); trimmed != "" {
		goalPtr = &trimmed
	}
	specs := make([]tasks.TaskSpec, 0, len(taskSpecs))
	for _, spec := range taskSpecs {
		specs = append(specs, tasks.SpecFromMap(spec))
	}
	store := tasks.NewTaskStore(deps.Home.Root)
	records, err := store.CreateTasksBatch(newSessionID, specs, goalPtr)
	if err != nil {
		return nil, &ForkSessionError{Message: "failed to seed task plan: " + err.Error(), Status: 500}
	}
	taskIDs := make([]int, 0, len(records))
	for _, record := range records {
		taskIDs = append(taskIDs, record.ID)
	}

	independentPhase := "independent"
	kickoff := handoffKickoffPrompt
	created, err := deps.CreateSession(CreateSessionParams{
		SessionID:       &newSessionID,
		QueenResumeFrom: &newSessionID,
		QueenName:       &queenName,
		InitialPhase:    &independentPhase,
		InitialPrompt:   &kickoff,
	})
	if err != nil {
		return nil, &ForkSessionError{Message: "failed to start forked session: " + err.Error(), Status: 500}
	}
	if created == nil {
		return nil, &ForkSessionError{Message: "failed to start forked session: session factory returned no session", Status: 500}
	}
	if created.ID != "" && created.ID != newSessionID {
		return nil, &ForkSessionError{
			Message: "failed to start forked session: session factory opened '" + created.ID +
				"', expected '" + newSessionID + "'",
			Status: 500,
		}
	}

	// Disarm the pivot on the successor until its first genuine user message:
	// the synthetic kickoff turn must not fork again.
	created.ForkKickoffPending = true
	// Retire the old session: /chat locks it, and meta.json records the
	// successor so a restart-resolution pass skips it.
	session.SupersededBy = created.ID
	markSessionSuperseded(queenDir, created.ID)
	if deps.Manager != nil {
		publishSessionForked(session, created.ID, queenName, handoff)
	}

	return map[string]any{
		"new_session_id": created.ID,
		"queen_id":       queenName,
		"compacted_from": session.ID,
		"task_ids":       taskIDs,
	}, nil
}

// writeHandoffSeedMessage writes the seq-0 handoff message, cursor and store
// meta a fresh session needs before NodeConversation.restore accepts it.
func writeHandoffSeedMessage(sessionDir, handoff string) error {
	conversations := filepath.Join(sessionDir, "conversations")
	if err := os.MkdirAll(conversations, 0o755); err != nil {
		return err
	}
	store := agentloop.NewFileConversationStore(conversations)
	defer store.Close()
	message := agentloop.Message{Seq: 0, Role: agentloop.RoleUser, Content: "[Session handoff]\n" + handoff}
	if err := store.WritePart(0, message.ToStorageDict()); err != nil {
		return err
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 1}); err != nil {
		return err
	}
	return store.WriteMeta(map[string]any{"system_prompt": "", "max_context_tokens": 180_000})
}

// WriteHandoffSeedMessage exposes the fork's seq-0 seed writer to the colony
// loader's lean-handoff accept path: the sibling colony's fresh queen session
// opens on the same `[Session handoff]\n<handoff>` message the DM fork writes,
// with no transcript copy and no LLM involved.
//
// Parity: framework/server/routes_execution.py::_write_seed_message
func WriteHandoffSeedMessage(sessionDir, handoff string) error {
	return writeHandoffSeedMessage(sessionDir, handoff)
}

// markSessionSuperseded stamps the old session's meta.json with its successor.
func markSessionSuperseded(queenDir, newSessionID string) {
	metaPath := filepath.Join(queenDir, "meta.json")
	meta := map[string]any{}
	if data, err := os.ReadFile(metaPath); err == nil {
		if decoded, decodeErr := decodeJSONBytes(data); decodeErr == nil {
			if object, ok := decoded.(map[string]any); ok {
				meta = object
			}
		}
	}
	meta["superseded_by"] = newSessionID
	meta["superseded_at"] = pythonISOFormat(time.Now().UTC())
	rendered, err := json.Marshal(meta)
	if err != nil {
		log.Printf("fork_queen_session(handoff): failed to marshal superseded meta: %v", err)
		return
	}
	if err := os.WriteFile(metaPath, rendered, 0o644); err != nil {
		log.Printf("fork_queen_session(handoff): failed to mark old session superseded: %v", err)
	}
}

// publishSessionForked tells the dashboard to swap silently to the successor.
// Best-effort, like upstream: the fork already succeeded.
func publishSessionForked(session *Session, newSessionID, queenName, handoff string) {
	if session.EventBus == nil {
		return
	}
	event := eventbus.NewAgentEvent(eventbus.EventSessionForked, "queen")
	event.Data = map[string]any{
		"new_session_id":  newSessionID,
		"queen_id":        queenName,
		"from_session_id": session.ID,
		"handoff":         handoff,
	}
	session.EventBus.Publish(event)
}
