package cli

// Parity: framework/server/routes_sessions.py::_create_colony_from_source
//
// The Create Colony popup's DM fork path lives here so loader.go stays inside
// its frozen size budget; the colony lifecycle type is shared across files in
// this package.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// CreateColonyFromSource backs the "Create Colony" popup: fork the source
// queen session into a new colony directory, lock the source so it cannot fork
// again, then resume the colony as a live session.
//
// Parity: framework/server/routes_sessions.py::_create_colony_from_source
func (l *colonyLifecycle) CreateColonyFromSource(params hiveserver.CreateColonyFromSourceParams) (*hiveserver.Session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	colonyID := strings.TrimSpace(params.ColonyID)
	if !colonySlugPattern.MatchString(colonyID) {
		return nil, &hiveserver.ColonyForkError{
			Message: "colony_id must be lowercase alphanumeric with underscores",
			Status:  http.StatusBadRequest,
		}
	}
	source := l.manager.GetSession(params.SourceSessionID)
	if source == nil {
		return nil, &hiveserver.ColonyForkError{
			Message: fmt.Sprintf("source_session_id '%s' is not a live session", params.SourceSessionID),
			Status:  http.StatusNotFound,
		}
	}
	// Phase branch, as upstream _create_colony_from_source: a colony-phase
	// queen's popup is the task_create(new_colony=true) lean-handoff pivot,
	// not the DM transcript fork. Its accept path spawns a sibling colony
	// seeded from the stashed goal/handoff/tasks — no compaction, no
	// transcript copy, no spawn lock on the source — and returns the new
	// colony session. The bulk of it lives in colony_pivot_lean.go.
	var pivot map[string]any
	phase := hiveserver.SessionPhaseLabel(source)
	if phase == hiveserver.QueenPhaseColony {
		pending := source.PendingColonyPivot()
		if pending == nil {
			return nil, &hiveserver.ColonyForkError{
				Message: "source colony has no pending new_colony pivot — the popup must be opened via task_create(new_colony=true) first.",
				Status:  http.StatusConflict,
			}
		}
		pivot, _ = pending.(map[string]any)
		return l.createSiblingColonyFromColony(source, params, pivot)
	}
	if phase != hiveserver.QueenPhaseIndependent {
		return nil, &hiveserver.ColonyForkError{
			Message: fmt.Sprintf("source session must be in 'independent' or 'colony' phase to fork into a colony (currently '%s').", phase),
			Status:  http.StatusConflict,
		}
	}
	if source.ColonySpawned {
		return nil, &hiveserver.ColonyForkError{
			Message: "source session already spawned a colony — cannot fork again.",
			Status:  http.StatusConflict,
		}
	}
	// Upstream fork_session_into_colony refuses a source whose queen loop is
	// gone or still initializing: the worker.json snapshot and the wake-up
	// injection both need the live executor/node. The colony directory is not
	// touched before this check passes.
	if err := colonyForkRequireLiveQueen(source); err != nil {
		return nil, err
	}
	colonyPath := l.home.ColonyDir(colonyID)
	if _, statErr := os.Stat(colonyPath); statErr == nil {
		return nil, &hiveserver.ColonyForkError{
			Message: fmt.Sprintf("colony '%s' already exists", colonyID),
			Status:  http.StatusConflict,
		}
	}

	queenName := strings.TrimSpace(source.QueenName)
	if params.QueenName != nil && strings.TrimSpace(*params.QueenName) != "" {
		queenName = strings.TrimSpace(*params.QueenName)
	}
	// The copied tree's queen segment is authoritative when the source session
	// dir exists (upstream derives queen_name from it). worker.json,
	// metadata.json, the copied tree and the resumed session must all name the
	// same queen, or the copy sits under a queen nothing resumes.
	sourceQueenDir := colonyForkSourceQueenDir(l.home, source)
	if derived := colonyForkSourceQueenName(sourceQueenDir); derived != "" {
		queenName = derived
	}
	queenScope := chatSessionQueenScope(queenName)
	model := l.modelFor(params.Model)

	// 1. The colony exists on disk before any runtime does: a failed start
	//    removes the directory again, so a 503 never leaves a half-colony.
	binding, err := writeColonyFromSession(colonyPath, colonyID, queenName, source, params)
	if err != nil {
		return nil, &hiveserver.ColonyForkError{Message: "failed to create colony: " + err.Error(), Status: http.StatusInternalServerError}
	}
	// 1a. Copy the source queen session into the colony's canonical session
	//     dir and mint the id the colony resumes. The inherited transcript is
	//     compacted once the copy exists, before the session opens (step 2).
	forkSeed, seedErr := prepareColonyForkSeed(l.home, colonyID, queenScope, sourceQueenDir, source.ID)
	if seedErr != nil {
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "failed to copy source queen session: " + seedErr.Error(), Status: http.StatusInternalServerError}
	}
	built, buildErr := l.build(colonyPath, colonyID, model)
	if buildErr != nil {
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "colony runtime unavailable: " + buildErr.Error(), Status: http.StatusServiceUnavailable}
	}
	if startErr := startColonyHost(context.Background(), built, colonyID); startErr != nil {
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: startErr.Error(), Status: http.StatusServiceUnavailable}
	}

	// 2. Compact the inherited transcript BEFORE the live colony session is
	//    opened. Upstream's fork_session_into_colony marks the copied queen
	//    dir in progress and schedules the background finalizer, and
	//    _create_session_for_existing_colony then blocks on that marker before
	//    _create_session_core. Opening the session first would let a supplied
	//    initial_prompt write into the transcript while the compactor rewrites
	//    the same files — and the compactor's stale-part sweep would drop the
	//    new message. A source with no session dir has nothing to compact and
	//    reports "skipped", as upstream does.
	compactionStatus := "skipped"
	if forkSeed.Copied {
		hiveserver.MarkInProgress(forkSeed.QueenDir)
		compactionStatus = "in_progress"
		l.scheduleColonyForkFinalize(
			forkSeed,
			colonyID,
			source.ID,
			colonyForkCompleter(source),
			hiveserver.QueenMaxContextTokens(l.home, queenScope, colonyForkContextTokens),
		)
		// 2a. Bounded wait for the compactor to settle (180s cap, 0.5s poll).
		//     A timeout or failed marker falls through to the raw transcript,
		//     fail-soft, exactly like upstream's await_completion.
		l.awaitColonyForkCompaction(forkSeed.QueenDir)
	}

	// 3. Resume the colony live on the runtime already armed above, under the
	//    copied session id. Upstream forks with `initial_phase or "colony"`;
	//    the explicit phase also keeps a copied meta.json["phase"] (the DM's
	//    "independent") from dragging the new session out of the colony.
	colonyPhase := hiveserver.QueenPhaseColony
	if params.InitialPhase != nil && strings.TrimSpace(*params.InitialPhase) != "" {
		colonyPhase = strings.TrimSpace(*params.InitialPhase)
	}
	createParams := hiveserver.CreateSessionParams{
		ColonyID:      &colonyID,
		SessionID:     &forkSeed.SessionID,
		QueenName:     stringPtrIfNotEmpty(queenName),
		Model:         params.Model,
		InitialPrompt: params.InitialPrompt,
		InitialPhase:  &colonyPhase,
	}
	if forkSeed.Copied {
		createParams.QueenResumeFrom = &forkSeed.SessionID
	}
	session, createErr := l.createSession(createParams, built, colonyPath)
	if createErr == nil && session == nil {
		createErr = fmt.Errorf("session factory returned no session")
	}
	if createErr == nil && session.ID != forkSeed.SessionID {
		// The factory opened some other id: the copied transcript would be
		// orphaned under an id nothing resumes.
		l.manager.StopSession(session.ID)
		createErr = fmt.Errorf("session factory opened '%s', expected '%s'", session.ID, forkSeed.SessionID)
	}
	if createErr != nil {
		built.Stop()
		_ = os.RemoveAll(colonyPath)
		return nil, &hiveserver.ColonyForkError{Message: "failed to start colony session: " + createErr.Error(), Status: http.StatusInternalServerError}
	}

	// 3a. Record the session the colony resumes under, exactly like
	// upstream's fork metadata (routes_execution.fork_session_into_colony
	// writes metadata["queen_session_id"] = colony_session_id). The loader's
	// `serve --colony` restart resolves that id and continues the same
	// transcript; without it a restart mints a fresh session and strands the
	// fork's history. Best-effort: a failed write only costs a fresh id on the
	// next boot.
	recordColonyQueenSessionID(colonyPath, session.ID, queenName)

	// 4. Lock the source session and link it back to the colony (steps 4/4a).
	if lockErr := l.colonyForkLockSource(source, session, built, colonyPath, colonyID, binding); lockErr != nil {
		return nil, lockErr
	}

	// 5. Wake the source queen and publish COLONY_CREATED (upstream's steps
	//    5/6) so the source UI renders the system-message link.
	l.colonyForkAnnounceSource(source, colonyID, colonyPath, session.ID, compactionStatus)
	return session, nil
}

// colonyForkLockSource performs the source-side lock and back-link of a
// completed fork (upstream's steps 4/4a): persist the spawn marker so the
// source cannot fork again, then stamp meta.json and the live record with the
// new colony. A failed persist tears the fork back down, as upstream does; the
// source transcript is untouched.
func (l *colonyLifecycle) colonyForkLockSource(
	source, session *hiveserver.Session,
	built *colonyhost.Host,
	colonyPath, colonyID string,
	binding *tracker.Binding,
) error {
	if lockErr := persistSessionColonySpawn(source, colonyID); lockErr != nil {
		l.manager.StopSession(session.ID)
		built.Stop()
		_ = os.RemoveAll(colonyPath)
		return &hiveserver.ColonyForkError{Message: "failed to persist colony spawn lock: " + lockErr.Error(), Status: http.StatusInternalServerError}
	}
	// meta.json gets agent_path/agent_name for cold discovery, and the live
	// record carries the colony binding so subsequent tracker calls target the
	// new DB.
	l.persistAgentMeta(source, colonyID, colonyPath)
	source.ColonyID = colonyID
	if binding != nil {
		source.Binding = binding
	}
	return nil
}

// colonyForkAnnounceSource unblocks the source queen whose suggest_colony call
// is parked on the popup (upstream's _inject_colony_confirmation) and publishes
// COLONY_CREATED on the source bus (upstream's step 6). The message is
// informational; the injection is the load-bearing part. A failed injection
// only warns, exactly as upstream logs it.
//
// Parity: framework/server/routes_sessions.py::_inject_colony_confirmation
// Parity: framework/server/routes_sessions.py::_create_colony_from_source
func (l *colonyLifecycle) colonyForkAnnounceSource(source *hiveserver.Session, colonyID, colonyPath, colonySessionID, compactionStatus string) {
	if queen := source.QueenExecutor.QueenNode(); queen != nil {
		confirmation := "[Colony '" + colonyID + "' created — this session is now locked. " +
			"The new colony queen takes over from here.]"
		if injectErr := queen.InjectEvent(confirmation, true); injectErr != nil {
			fmt.Fprintf(l.out, "Warning: failed to inject colony confirmation into %s: %v\n", source.ID, injectErr)
		}
	}
	publishColonyCreated(source, colonyID, colonyPath, colonySessionID, compactionStatus, "")
}

// colonyForkRequireLiveQueen enforces upstream's live-queen precondition
// before anything is written: the source must carry a queen executor and a
// registered queen node. Both absences are upstream RuntimeErrors that
// _create_colony_from_source maps to a 503.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony (queen_executor / node_registry guard)
func colonyForkRequireLiveQueen(source *hiveserver.Session) error {
	if source == nil || source.QueenExecutor == nil {
		return &hiveserver.ColonyForkError{
			Message: fmt.Sprintf(
				"queen_executor is None for session %q — the queen loop isn't running right now. "+
					"Wait for the queen to come back (or send her a chat message to revive her) and retry the colony fork.",
				colonyForkSourceID(source),
			),
			Status: http.StatusServiceUnavailable,
		}
	}
	if source.QueenExecutor.QueenNode() == nil {
		return &hiveserver.ColonyForkError{
			Message: fmt.Sprintf(
				"queen node is missing from the executor's registry for session %q. "+
					"The queen loop is in an initialization or teardown window; retry after a moment.",
				source.ID,
			),
			Status: http.StatusServiceUnavailable,
		}
	}
	return nil
}

// colonyForkSourceID is the session id a precondition message names, empty
// when the caller had no live record.
func colonyForkSourceID(source *hiveserver.Session) string {
	if source == nil {
		return ""
	}
	return source.ID
}
