package cli

// Colony→colony lean handoff: the accept path behind a colony-phase queen's
// task_create(new_colony=true) popup.
//
// The source queen stashed the queen-authored goal/handoff/task plan on
// session.pending_colony_pivot and parked her turn (chat_colony_dispatch.go).
// Confirming the popup spawns a SIBLING colony: nothing from the source
// conversation is copied or compacted. The new colony's queen opens on the
// handoff brief alone (seq 0), the goal + plan land in her task list before the
// session goes live, and the source colony stays alive and unlocked — its queen
// end-turns and waits for the user's next message while the user works in the
// new colony.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
// Parity: framework/server/routes_sessions.py::_inject_new_colony_confirmation

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	hivecolony "github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	hivesession "github.com/shark2202/go_hive/go-hive/internal/session"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// colonySiblingWorkerName is the single worker definition the sibling colony's
// worker.json declares, verbatim upstream.
const colonySiblingWorkerName = "worker"

// createSiblingColonyFromColony forks a colony-phase queen session into a new
// sibling colony without copying any of the source conversation.
//
// The caller (CreateColonyFromSource) already holds the lifecycle lock and has
// validated the slug and the live source session.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
func (l *colonyLifecycle) createSiblingColonyFromColony(
	source *hiveserver.Session,
	params hiveserver.CreateColonyFromSourceParams,
	pivot map[string]any,
) (*hiveserver.Session, error) {
	// The source queen must still be executing: her blocked
	// task_create(new_colony=true) call is what this fork unblocks, and a
	// torn-down executor cannot be woken.
	if err := validateSiblingSourceQueen(source); err != nil {
		return nil, err
	}

	// The pivot is queen-authored JSON; normalize and validate it before
	// touching the colony directory.
	goal, handoff, taskSpecs, err := parseSiblingPivot(pivot)
	if err != nil {
		return nil, err
	}

	colonyID := strings.TrimSpace(params.ColonyID)
	queenName := siblingColonyQueenName(source, pivot)

	colonyPath, err := reserveSiblingColonyDir(l.home, colonyID)
	if err != nil {
		return nil, err
	}
	// A failure before the session goes live tears the half-colony down again,
	// exactly like the DM fork path: a 5xx never leaves a colony on disk that
	// the user cannot open.
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(colonyPath)
		}
	}()

	// 1. tracker.db before worker.json, so its absolute path can be threaded
	//    into the worker's input_data binding.
	binding, bindingErr := tracker.BindingForColonyDir(colonyPath, colonyID)
	if bindingErr != nil {
		return nil, colonySiblingForkError(http.StatusInternalServerError, "failed to create colony: "+bindingErr.Error())
	}

	workerPath := filepath.Join(colonyPath, "worker.json")

	// 2. worker.json inherits the source queen's live surface.
	if err := writeSiblingWorkerDefinition(workerPath, source, binding); err != nil {
		return nil, err
	}

	// 3. A fresh queen session dir — no copytree — seeded with the handoff
	//    brief alone.
	colonySessionID, err := seedSiblingColonySession(l.home, source, queenName, colonyID, colonyPath, handoff)
	if err != nil {
		return nil, err
	}

	// 4. Seed the plan into the new queen's list before the session is live:
	//    the records are written straight to tasks.json, so no events fire yet.
	records, err := seedSiblingTaskPlan(l.home, colonySessionID, taskSpecs, goal)
	if err != nil {
		return nil, err
	}

	// 5. Colony metadata.json — provenance only. The new colony is a fresh
	//    root, not lineage-linked to the source.
	if err := writeSiblingColonyMetadata(colonyPath, colonyID, queenName, colonySessionID, source.ID); err != nil {
		return nil, err
	}

	// 6. Inherit the source queen's MCP allowlist, best-effort: an allow-all
	//    source (None) leaves the new colony on the default.
	colonySiblingInheritMCPAllowlist(l.home, source, colonyID, l.out)

	// 7. Bring the new colony live on an already-armed runtime, so a start
	//    failure is a clean 503 with no half-created session.
	created, err := l.startSiblingColonySession(colonyPath, colonyID, queenName, colonySessionID, params)
	if err != nil {
		return nil, err
	}
	committed = true

	// fork_kickoff_pending guards against an immediate re-pivot on the new
	// session's synthetic kickoff turn; the first genuine user message clears
	// it (upstream sets the flag on the returned session).
	created.ForkKickoffPending = true

	// 8. Emit task_created on the NEW session's bus so the Action Plan panel
	//    populates from the seeded plan the moment the user lands there. The
	//    records were written before the session went live, so this is the
	//    first event either side sees.
	emitSiblingTaskCreated(colonySessionID, records, created)

	// 9. Publish COLONY_CREATED on the SOURCE colony's bus: if the user ever
	//    returns to the source they see the system-message link to the new
	//    colony. The new session is the HTTP response, not this link.
	publishColonyCreated(source, colonyID, colonyPath, colonySessionID, "skipped", hiveserver.QueenPhaseColony)

	// 10. Wake the source queen: her task_create(new_colony=true) call parked
	//     the turn on user input, and the synthetic client-input message is
	//     what unblocks it. The message is informational — she end-turns and
	//     stays idle on this colony's existing plan.
	wakeSiblingSourceQueen(l.out, source, colonyID, len(records), colonySessionID)

	// 11. Clear the pivot stash so a later new_colony request opens a fresh
	//     popup instead of reading the now-stale payload. The source colony is
	//     deliberately NOT spawn-locked: it stays live for the user.
	source.ClearPendingColonyPivot()
	return created, nil
}

// validateSiblingSourceQueen refuses to fork a source whose queen executor is
// gone or still initializing: the blocked task_create(new_colony=true) call
// this fork unblocks only exists on a live queen.
func validateSiblingSourceQueen(source *hiveserver.Session) error {
	if source.QueenExecutor == nil {
		return colonySiblingForkError(http.StatusServiceUnavailable,
			"source colony queen isn't running — cannot spawn a sibling colony right now.")
	}
	if source.QueenExecutor.QueenNode() == nil {
		return colonySiblingForkError(http.StatusServiceUnavailable,
			"source colony queen executor is initializing or tearing down — retry in a moment.")
	}
	return nil
}

// parseSiblingPivot normalizes the queen-authored pivot and rejects an
// incomplete plan before anything is written to disk.
func parseSiblingPivot(pivot map[string]any) (goal, handoff string, taskSpecs []tasks.TaskSpec, err error) {
	goal = strings.TrimSpace(colonySiblingString(pivot["goal"]))
	handoff = strings.TrimSpace(colonySiblingString(pivot["handoff"]))
	taskSpecs = colonySiblingTaskSpecs(pivot["tasks"])
	switch {
	case goal == "":
		return "", "", nil, colonySiblingForkError(http.StatusConflict, "pending pivot is missing `goal`")
	case handoff == "":
		return "", "", nil, colonySiblingForkError(http.StatusConflict, "pending pivot is missing `handoff`")
	case len(taskSpecs) == 0:
		return "", "", nil, colonySiblingForkError(http.StatusConflict, "pending pivot is missing `tasks`")
	}
	return goal, handoff, taskSpecs, nil
}

// siblingColonyQueenName resolves the new colony's queen scope. The Go loader
// has always written chat session trees under queens/<queen>/sessions and
// resolves the shared "default" queen onto its materialised profile id
// (resolveCreateIdentity). Resolve the scope here, once, and use it for the
// seeded tree, the create call and the metadata: a restart resolves the colony
// session from metadata's queen_name, so the two must name the same tree.
func siblingColonyQueenName(source *hiveserver.Session, pivot map[string]any) string {
	queenName := strings.TrimSpace(colonySiblingString(pivot["queen_name"]))
	if queenName == "" {
		queenName = strings.TrimSpace(source.QueenName)
	}
	if queenName == "" {
		queenName = "default"
	}
	queenName = chatSessionQueenScope(queenName)
	if queenName == "default" {
		queenName = hiveserver.DefaultQueenID()
	}
	return queenName
}

// reserveSiblingColonyDir claims the slug and creates the colony directory.
// The route vacates a soft-deleted colony before this hook runs; a live colony
// (worker.json or metadata.json on disk) still occupies the slug.
func reserveSiblingColonyDir(home hivehome.Home, colonyID string) (string, error) {
	colonyPath := home.ColonyDir(colonyID)
	if colonySiblingPathExists(filepath.Join(colonyPath, "worker.json")) ||
		colonySiblingPathExists(filepath.Join(colonyPath, "metadata.json")) {
		return "", colonySiblingForkError(http.StatusConflict,
			fmt.Sprintf("colony '%s' already exists — pick a different slug", colonyID))
	}
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError, "failed to create colony: "+err.Error())
	}
	return colonyPath, nil
}

// writeSiblingWorkerDefinition writes worker.json from the source queen's live
// surface: her tool names (queen-only lifecycle tools stripped), skills
// catalog, protocols, skill dirs and loop config, plus the colony binding the
// worker writes tracker rows through. A source queen with no built loop
// contributes nothing, which is upstream's queen_ctx None path.
func writeSiblingWorkerDefinition(workerPath string, source *hiveserver.Session, binding *tracker.Binding) error {
	surface := source.Chat.ForkSurfaceSnapshot()
	toolNames := colonySiblingInheritedToolNames(surface.ToolNames)
	var loopConfig *hivecolony.QueenLoopConfig
	if surface.HasLoop {
		loopConfig = &hivecolony.QueenLoopConfig{
			MaxIterations:      surface.MaxIterations,
			ToolCallBudget:     surface.ToolCallBudget,
			MaxContextTokens:   surface.MaxContextTokens,
			MaxToolResultChars: surface.MaxToolResultChars,
		}
	}
	skillsCatalog, protocolsPrompt, skillDirs := colonySiblingQueenPrompts(source)
	queenPhase := hiveserver.SessionPhaseLabel(source)
	if strings.TrimSpace(queenPhase) == "" {
		queenPhase = hiveserver.QueenPhaseColony
	}
	workerMeta := hivecolony.BuildMeta(hivecolony.WorkerMetaInput{
		WorkerName:          colonySiblingWorkerName,
		SourceSessionID:     source.ID,
		ToolNames:           toolNames,
		SkillsCatalogPrompt: skillsCatalog,
		ProtocolsPrompt:     protocolsPrompt,
		SkillDirs:           skillDirs,
		QueenLoopConfig:     loopConfig,
		QueenPhase:          queenPhase,
		QueenID:             colonySiblingQueenID(source),
		InputData: hivecolony.BuildInputData(hivehome.Colony{
			Name:      binding.Name,
			Dir:       binding.Dir,
			TrackerDB: binding.TrackerDB,
		}, ""),
	})
	if err := atomicfs.WriteJSON(workerPath, workerMeta, 0o644); err != nil {
		return colonySiblingForkError(http.StatusInternalServerError, "failed to create colony: "+err.Error())
	}
	return nil
}

// seedSiblingColonySession creates the fresh queen session dir (no copytree)
// seeded with the handoff brief alone, and records the fork provenance in its
// meta.json. The goal and plan go to the task list separately, so the new
// queen resumes onto an existing plan instead of creating one.
func seedSiblingColonySession(
	home hivehome.Home,
	source *hiveserver.Session,
	queenName, colonyID, colonyPath, handoff string,
) (string, error) {
	colonySessionID, idErr := hivesession.NewStore("").GenerateID(time.Now())
	if idErr != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError,
			"failed to generate colony session id: "+idErr.Error())
	}
	queenDir := chatSessionDir(home, queenName, colonySessionID)
	if err := os.MkdirAll(filepath.Dir(queenDir), 0o755); err != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError,
			"failed to create colony session dir: "+err.Error())
	}
	// exist_ok=False upstream: this fork owns the fresh leaf.
	if err := os.Mkdir(queenDir, 0o755); err != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError,
			"new colony session dir collision: "+err.Error())
	}
	if err := hiveserver.WriteHandoffSeedMessage(queenDir, handoff); err != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError,
			"failed to seed the colony handoff: "+err.Error())
	}
	destMeta := map[string]any{
		"agent_path":  colonyPath,
		"agent_name":  humanizeColonyName(colonyID),
		"queen_id":    queenName,
		"forked_from": source.ID,
		"colony_fork": true,
		"phase":       hiveserver.QueenPhaseColony,
		"created_at":  float64(time.Now().UnixNano()) / 1e9,
	}
	if err := hiveserver.MergeSessionMeta(filepath.Join(queenDir, "meta.json"), destMeta); err != nil {
		return "", colonySiblingForkError(http.StatusInternalServerError,
			"failed to seed the colony session meta: "+err.Error())
	}
	return colonySessionID, nil
}

// seedSiblingTaskPlan writes the pivot plan straight to tasks.json before the
// session is live, so no events fire yet.
func seedSiblingTaskPlan(
	home hivehome.Home,
	colonySessionID string,
	taskSpecs []tasks.TaskSpec,
	goal string,
) ([]*tasks.TaskRecord, error) {
	store := tasks.NewTaskStore(home.Root)
	records, seedErr := store.CreateTasksBatch(colonySessionID, taskSpecs, &goal)
	if seedErr != nil {
		return nil, colonySiblingForkError(http.StatusInternalServerError,
			"failed to seed task plan in new colony: "+seedErr.Error())
	}
	return records, nil
}

// writeSiblingColonyMetadata records provenance for the new colony. It is a
// fresh root, not lineage-linked to the source.
func writeSiblingColonyMetadata(colonyPath, colonyID, queenName, colonySessionID, sourceSessionID string) error {
	now := time.Now().UTC()
	stamp := colonySiblingTimestamp(now)
	metadata := map[string]any{
		"colony_id":         colonyID,
		"queen_name":        queenName,
		"queen_session_id":  colonySessionID,
		"source_session_id": sourceSessionID,
		"created_at":        stamp,
		"updated_at":        stamp,
		"workers": map[string]any{
			colonySiblingWorkerName: map[string]any{
				"task":       "",
				"spawned_at": stamp,
			},
		},
	}
	if err := atomicfs.WriteJSON(filepath.Join(colonyPath, "metadata.json"), metadata, 0o644); err != nil {
		return colonySiblingForkError(http.StatusInternalServerError, "failed to create colony: "+err.Error())
	}
	return nil
}

// startSiblingColonySession brings the new colony live on an already-armed
// runtime, so a start failure is a clean 503 with no half-created session.
func (l *colonyLifecycle) startSiblingColonySession(
	colonyPath, colonyID, queenName, colonySessionID string,
	params hiveserver.CreateColonyFromSourceParams,
) (*hiveserver.Session, error) {
	built, buildErr := l.build(colonyPath, colonyID, l.modelFor(params.Model))
	if buildErr != nil {
		return nil, colonySiblingForkError(http.StatusServiceUnavailable, "colony runtime unavailable: "+buildErr.Error())
	}
	if startErr := startColonyHost(context.Background(), built, colonyID); startErr != nil {
		built.Stop()
		return nil, colonySiblingForkError(http.StatusServiceUnavailable, startErr.Error())
	}
	initialPhase := hiveserver.QueenPhaseColony
	if params.InitialPhase != nil && strings.TrimSpace(*params.InitialPhase) != "" {
		initialPhase = strings.TrimSpace(*params.InitialPhase)
	}
	created, createErr := l.createSession(hiveserver.CreateSessionParams{
		ColonyID:        &colonyID,
		SessionID:       &colonySessionID,
		Model:           params.Model,
		QueenResumeFrom: &colonySessionID,
		QueenName:       &queenName,
		InitialPhase:    &initialPhase,
	}, built, colonyPath)
	if createErr != nil {
		built.Stop()
		return nil, colonySiblingForkError(http.StatusInternalServerError,
			"failed to start colony session: "+createErr.Error())
	}
	if created == nil {
		built.Stop()
		return nil, colonySiblingForkError(http.StatusInternalServerError,
			"failed to start colony session: session factory returned no session")
	}
	if created.ID != "" && created.ID != colonySessionID {
		l.manager.StopSession(created.ID)
		built.Stop()
		return nil, colonySiblingForkError(http.StatusInternalServerError,
			"failed to start colony session: session factory opened '"+created.ID+
				"', expected '"+colonySessionID+"'")
	}
	return created, nil
}

// emitSiblingTaskCreated emits task_created on the NEW session's bus so the
// Action Plan panel populates from the seeded plan the moment the user lands
// there. The records were written before the session went live, so this is the
// first event either side sees.
func emitSiblingTaskCreated(colonySessionID string, records []*tasks.TaskRecord, created *hiveserver.Session) {
	for _, record := range records {
		if record == nil {
			continue
		}
		tasks.EmitTaskCreated(colonySessionID, record, "", created.EventBus)
	}
}

// wakeSiblingSourceQueen injects the informational confirmation into the
// source queen: her task_create(new_colony=true) call parked the turn on user
// input, and the synthetic client-input message is what unblocks it. She
// end-turns and stays idle on this colony's existing plan.
func wakeSiblingSourceQueen(out io.Writer, source *hiveserver.Session, colonyID string, taskCount int, colonySessionID string) {
	confirmation := fmt.Sprintf(
		"[New colony '%s' created with %d task(s) (queen session %s). The user has been "+
			"navigated there; the off-goal plan now lives in the new colony, not here. "+
			"This colony's existing plan is untouched. End your turn now — the new "+
			"colony's queen takes over the pivoted work; you stay idle here for the "+
			"user's next message.]",
		colonyID, taskCount, colonySessionID)
	if queen := source.QueenExecutor.QueenNode(); queen != nil {
		if injectErr := queen.InjectEvent(confirmation, true); injectErr != nil {
			fmt.Fprintf(out, "Warning: failed to inject new-colony confirmation into %s: %v\n", source.ID, injectErr)
		}
	}
}

// colonySiblingForkError builds the HTTP-mapped error the accept route returns.
func colonySiblingForkError(status int, message string) *hiveserver.ColonyForkError {
	return &hiveserver.ColonyForkError{Message: message, Status: status}
}

// colonySiblingTaskSpecs normalizes the pivot's task list to the task store's
// spec shape. Non-object entries are dropped, as upstream's list() + create
// validation would reject them anyway.
func colonySiblingTaskSpecs(raw any) []tasks.TaskSpec {
	var entries []map[string]any
	switch typed := raw.(type) {
	case []map[string]any:
		entries = typed
	case []any:
		entries = make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if object, ok := entry.(map[string]any); ok {
				entries = append(entries, object)
			}
		}
	}
	specs := make([]tasks.TaskSpec, 0, len(entries))
	for _, entry := range entries {
		specs = append(specs, tasks.SpecFromMap(entry))
	}
	return specs
}

// colonySiblingQueenID is the queen identity the new colony's worker.json
// records (upstream's queen_ctx-derived queen_id, falling back to the phase
// state's identity).
func colonySiblingQueenID(source *hiveserver.Session) string {
	if state, ok := source.PhaseState.(hiveserver.SessionPhaseState); ok && state != nil {
		return strings.TrimSpace(state.QueenID())
	}
	return ""
}

// colonySiblingQueenPrompts reads the source queen's rendered skills catalog,
// protocols prompt and skill dirs from her live skills manager. A session with
// no manager (a cold record) contributes empties, matching upstream's
// queen_ctx None path.
func colonySiblingQueenPrompts(source *hiveserver.Session) (skillsCatalog, protocols string, dirs []string) {
	if source == nil || source.LiveSkills == nil {
		return "", "", nil
	}
	if catalog, ok := source.LiveSkills.(*queenSkillsCatalog); ok {
		skillsCatalog = catalog.CatalogForPhase(hiveserver.QueenPhaseColony)
		if manager := catalog.managerInstance(); manager != nil {
			protocols = manager.ProtocolsPrompt()
			dirs = manager.AllowlistedDirs()
		}
		return skillsCatalog, protocols, dirs
	}
	return source.LiveSkills.SkillsCatalogPrompt(), "", nil
}

// colonySiblingInheritMCPAllowlist copies the source queen's enabled MCP tool
// list into the new colony's tools.json sidecar. Best-effort, as upstream: an
// allow-all source (Python None) leaves the new colony on the default, and a
// write failure only costs the new colony its curated surface.
func colonySiblingInheritMCPAllowlist(home hivehome.Home, source *hiveserver.Session, colonyID string, out io.Writer) {
	state, ok := source.PhaseState.(interface {
		EnabledMCPTools() ([]string, bool)
	})
	if !ok || state == nil {
		return
	}
	enabled, isNone := state.EnabledMCPTools()
	if isNone {
		return
	}
	if err := hiveserver.UpdateColonyToolsConfig(home, colonyID, enabled); err != nil {
		fmt.Fprintf(out, "Warning: failed to inherit MCP allowlist into '%s': %v\n", colonyID, err)
	}
}

// colonySiblingPathExists reports whether path names an entry on disk.
func colonySiblingPathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// colonySiblingTimestamp renders the ISO-8601 UTC stamp colony metadata carries
// (Python's datetime.isoformat()).
func colonySiblingTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.999999+00:00")
}

// queenWorkerInheritedTools are the "work-doing" primitives a worker keeps
// when a colony is forked; every other name in a queen phase tool list is
// queen-lifecycle and is stripped from worker.json.
//
// Parity: framework/server/routes_execution.py::_WORKER_INHERITED_TOOLS
var queenWorkerInheritedTools = map[string]struct{}{
	"terminal_exec":    {},
	"terminal_rg":      {},
	"terminal_glob":    {},
	"set_output":       {},
	"report_to_parent": {},
	"tracker_upsert":   {},
	"tracker_query":    {},
	"task_create":      {},
	"task_update":      {},
	"task_list":        {},
	"task_get":         {},
}

// queenIndependentPhaseTools is the independent-phase queen tool list.
//
// Parity: framework/agents/queen/nodes/__init__.py::_QUEEN_INDEPENDENT_TOOLS
var queenIndependentPhaseTools = []string{
	"suggest_colony",
	"task_create",
	"task_update",
	"task_list",
	"task_get",
	"search_tools",
	"write_skill",
	"list_queens",
	"message_queen",
}

// queenColonyPhaseTools is the colony-phase queen tool list.
//
// Parity: framework/agents/queen/nodes/__init__.py::_QUEEN_COLONY_TOOLS
var queenColonyPhaseTools = []string{
	"get_worker_status",
	"inject_message",
	"run_playbook",
	"get_playbook_status",
	"stop_playbook",
	"list_playbook_runs",
	"run_worker",
	"stop_worker",
	"write_skill",
	"set_trigger",
	"remove_trigger",
	"list_triggers",
	"tracker_sql",
	"tracker_register_writable",
	"tracker_upsert",
	"tracker_query",
	"task_create",
	"task_update",
	"task_list",
	"task_get",
	"search_tools",
	"list_queens",
	"message_queen",
}

// queenLifecycleExtraTools are queen-lifecycle tools registered outside the
// phase lists; they are stripped too.
//
// Parity: framework/server/routes_execution.py::_QUEEN_LIFECYCLE_EXTRAS
var queenLifecycleExtraTools = []string{
	"switch_to_independent",
	"get_worker_health_summary",
}

var queenOnlyLifecycleTools = sync.OnceValue(func() map[string]struct{} {
	queenOnly := map[string]struct{}{}
	for _, name := range queenIndependentPhaseTools {
		queenOnly[name] = struct{}{}
	}
	for _, name := range queenColonyPhaseTools {
		queenOnly[name] = struct{}{}
	}
	for name := range queenWorkerInheritedTools {
		delete(queenOnly, name)
	}
	for _, name := range queenLifecycleExtraTools {
		queenOnly[name] = struct{}{}
	}
	return queenOnly
})

// colonySiblingInheritedToolNames strips the queen-only lifecycle tools from
// the source queen's available-tool names, preserving her order.
//
// Parity: framework/server/routes_execution.py::_resolve_queen_only_tools
func colonySiblingInheritedToolNames(names []string) []string {
	queenOnly := queenOnlyLifecycleTools()
	out := make([]string, 0, len(names))
	for _, name := range names {
		if _, stripped := queenOnly[name]; stripped {
			continue
		}
		out = append(out, name)
	}
	return out
}

// colonySiblingString reads a payload value as a string, treating a missing or
// non-string field as empty (the pivot payload is queen-authored JSON).
func colonySiblingString(value any) string {
	text, _ := value.(string)
	return text
}
