package cli

// Preloaded `serve --colony` sessions: the loader builds the colony host, the
// session's chat/trigger rung, and registers it with the session manager. The
// assembly lives here rather than in loader.go so the serve entry point stays
// inside the repository's frozen file-size budget.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/mcpregistry"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	hivesession "github.com/aden-hive/hive-go/internal/session"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// loadPreloadedColonySession builds and registers the live session behind one
// `serve --colony <path>` argument: the colony host (overseer included), the
// session's own chat runtime when a provider is configured, and its restored
// trigger runtime. It returns nil when the colony cannot be loaded; the caller
// keeps serving the other arguments.
//
// A preloaded colony used to be a worker-only record: its Chat stayed nil, so
// POST /chat answered 503 and no run_worker could ever be dispatched from the
// session. Attaching the chat runtime here makes the preloaded session a real
// Queen from boot.
func (r *Runner) loadPreloadedColonySession(
	colonyArg string,
	manager *hiveserver.SessionManager,
	liveMu *sync.Mutex,
	live *[]*hiveserver.Session,
	home hivehome.Home,
	model string,
) *hiveserver.Session {
	colonyPath, ok := r.resolveColonyPath(colonyArg)
	if !ok {
		fmt.Fprintf(r.Stdout, "Colony not found: %s\n", colonyArg)
		return nil
	}
	colonyID := filepath.Base(colonyPath)
	// The colony's queen comes from its metadata (queen_name), exactly like
	// upstream's _create_session_for_existing_colony, and scopes the session
	// tree, memory and skills catalog. A colony without a recorded queen keeps
	// the shared default queen.
	queenName := colonyQueenName(home, colonyID)
	// A colony restarted here resumes its own queen session (queen_resume_from)
	// instead of minting a new id on every boot: the previous transcript and
	// the triggers/judgement history stay attached to the same session.
	sessionID, sessionDir, resumed := resolvePreloadedColonySession(home, colonyID, queenName)
	if sessionID == "" {
		fmt.Fprintf(r.Stdout, "Error loading colony %s: could not resolve a session id\n", colonyArg)
		return nil
	}
	record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	record.ColonyID = colonyID
	record.WorkerPath = colonyPath
	record.QueenName = queenName
	// The resolved session root every meta.json writer uses.
	record.QueenDir = sessionDir
	// A preloaded colony is colony-bound on boot: the phase cascade restores
	// the persisted answer (or stamps "colony" for a fresh directory).
	hiveserver.AttachSessionPhaseState(record, nil, sessionDir)
	if resumed {
		record.QueenResumeFrom = sessionID
	}
	record.LoadedAt = float64(time.Now().UnixNano()) / 1e9
	// The live runtime behind the session: colony spawn/run/SSE entry.
	// Nil when no provider is configured, in which case the worker and
	// colony routes keep their on-disk fallbacks.
	record.Colony = r.buildColonyHost(colonyPath, colonyID, home, model)
	if built, ok := record.Colony.(*colonyhost.Host); ok {
		// A preloaded colony is live from boot: its overseer starts here
		// rather than waiting for a load_colony round trip.
		queenName := colonyQueenName(home, colonyID)
		if _, startErr := built.StartOverseer(context.Background(), queenOverseerSpec(home, queenName), nil); startErr != nil {
			fmt.Fprintf(r.Stdout, "Warning: colony overseer unavailable for %s: %v\n", colonyID, startErr)
		}
	}
	// Chat/trigger/dispatch runtime for the preloaded session. It must exist
	// before triggers are restored: a restored timer's first fire needs the
	// session's queen executor.
	r.attachPreloadedChatRuntime(record, manager, home, model, sessionDir)
	// Rebuild the colony's triggers.json runtime: enabled timers and
	// webhook routes survive a restart without the user re-activating them.
	if loaded, started := hiveserver.RestoreSessionTriggers(record); loaded > 0 {
		fmt.Fprintf(r.Stdout, "Restored %d trigger(s) (%d running) for %s\n", loaded, started, colonyID)
	}
	manager.AddSession(record)
	liveMu.Lock()
	*live = append(*live, record)
	liveMu.Unlock()
	fmt.Fprintf(r.Stdout, "Loaded colony: %s → session %s\n", colonyID, sessionID)
	return record
}

// resolvePreloadedColonySession picks the session a preloaded colony boots
// under. A colony records the id of its queen session in metadata.json
// (queen_session_id). When that session's directory still exists, the loader
// adopts the id — the same queen_resume_from path a created session takes — so
// a restart continues the conversation instead of stranding the old transcript
// under an id nothing addresses. The colony's own tree is checked first (the
// upstream layout); the queen-scoped tree this loader has always written is the
// fallback. Otherwise a fresh id is minted, its directory created, and the id
// saved for the next boot.
//
// Parity: framework/server/session_manager.py::_create_session_for_existing_colony
// Parity: framework/server/session_manager.py::_queen_session_dir
func resolvePreloadedColonySession(home hivehome.Home, colonyID, queenScope string) (sessionID, sessionDir string, resumed bool) {
	queenScope = chatSessionQueenScope(queenScope)
	if recorded := preloadedColonySessionID(home, colonyID); recorded != "" &&
		hivesession.ValidSegment(recorded) {
		candidates := []string{
			home.ColonyQueenSessionDir(colonyID, queenScope, recorded),
			chatSessionDir(home, queenScope, recorded),
		}
		for _, candidate := range candidates {
			if isSessionDir(candidate) && sessionDirQueenMatches(candidate, queenScope) {
				return recorded, candidate, true
			}
		}
	}
	generated, err := hivesession.NewStore("").GenerateID(time.Now())
	if err != nil {
		return "", "", false
	}
	dir := chatSessionDir(home, queenScope, generated)
	// Materialise the directory now: an empty-but-existing session is still the
	// colony's session, and the next boot must find it and reuse this id.
	_ = os.MkdirAll(dir, 0o755)
	persistPreloadedColonySessionID(home, colonyID, queenScope, generated)
	return generated, dir, false
}

func preloadedColonySessionID(home hivehome.Home, colonyID string) string {
	meta, ok := readJSONObject(filepath.Join(home.ColoniesDir(), colonyID, "metadata.json"))
	if !ok {
		return ""
	}
	raw, _ := meta["queen_session_id"].(string)
	return strings.TrimSpace(raw)
}

// persistPreloadedColonySessionID records the colony's queen session id (and
// the queen scope it lives under) without disturbing the rest of metadata.json.
// Best effort: a failed write only costs a fresh id on the next boot.
func persistPreloadedColonySessionID(home hivehome.Home, colonyID, queenScope, sessionID string) {
	path := filepath.Join(home.ColoniesDir(), colonyID, "metadata.json")
	meta, ok := readJSONObject(path)
	if !ok || meta == nil {
		meta = map[string]any{}
	}
	meta["queen_session_id"] = sessionID
	if _, present := meta["queen_name"]; !present && queenScope != "default" {
		meta["queen_name"] = queenScope
	}
	_ = atomicfs.WriteJSON(path, meta, 0o644)
}

// attachPreloadedChatRuntime arms a preloaded colony session with the same
// Queen runtime a POST /api/sessions create builds: builtin + integration +
// task + tracker + run_worker tools over a per-session transcript, bound to the
// session's colony so run_worker dispatches and worker reports arrive. Without
// a usable provider the session keeps Chat nil and POST /chat keeps its 503,
// mirroring createSessionWithRuntime's degradation.
//
// sessionDir is the colony-scoped session directory the loader resolved; a
// resumed session continues the transcript already in it.
func (r *Runner) attachPreloadedChatRuntime(record *hiveserver.Session, manager *hiveserver.SessionManager, home hivehome.Home, model, sessionDir string) {
	if record == nil || record.Colony == nil {
		return
	}
	provider, err := queenAwareChatProvider(home, model, record.QueenName)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", record.ID, err)
		return
	}
	if sessionDir == "" {
		sessionDir = chatSessionDir(home, chatSessionQueenScope(record.QueenName), record.ID)
	}
	toolbox := builtin.New("")
	tools := newSessionToolCatalogue(sessionDir, toolbox, home)
	skillsCatalog := liveSkillsCatalog(record, home, chatSessionQueenScope(record.QueenName))
	// A preloaded colony session is still a queen chat session: it gets the
	// same queen-mail and session-bound lifecycle tools a created one does.
	// A session whose queen was not picked yet registers them and is refused
	// when it writes.
	onTurnStart := enableQueenSessionTools(tools, manager, record.ID, record, home)
	// Same flat lazy split a created session gets (24 always-on + deferred).
	alwaysOnTools, deferredTools := tools.LazySurfaceDeps()
	built, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM:               provider,
		Tools:             tools.ModelDefs(),
		AlwaysOnToolNames: alwaysOnTools,
		DeferredTools:     deferredTools,
		Execute:           tools.Execute,
		ExecuteBlocks:     tools.ExecuteBlocks,
		Root:              sessionDir,
		Bus:               record.EventBus,
		SessionID:         record.ID,
		QueenName:         record.QueenName,
		HomeRoot:          home.Root,
		// The preloaded path must announce the same queen skills catalog a
		// created session does: without it the skills_catalog reminder source
		// stays silent for the whole `serve --colony` session.
		//
		// Parity: core/framework/server/queen_orchestrator.py::create_queen
		// The catalog is phase-filtered at render time, like upstream's
		// QueenPhaseState.render_skills_catalog.
		SkillsCatalog: func() string {
			return skillsCatalog.CatalogForPhase(hiveserver.SessionPhaseLabel(record))
		},
		Colony:      record.Colony,
		OnTurnStart: onTurnStart,
		// Same vision-fallback captioning a created session gets.
		VisionCaptioner: visionCaptionerFor(home),
		// The queen's phase rides node_loop_iteration, as upstream's
		// iteration_metadata_provider reports it.
		Phase: func() string { return hiveserver.SessionPhaseLabel(record) },
		// The ambient "# Credentials" block of her static prompt: the same
		// store the credential routes read, snapshotted when the preloaded
		// session's prompt is built.
		//
		// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
		Credentials: func() []map[string]any {
			adapter := buildSessionCredentialAdapter(home)
			if adapter == nil {
				return nil
			}
			return adapter.GetAllAccountInfo()
		},
		// The command-line MCP servers she runs with terminal_exec, right after
		// the credentials block.
		//
		// Parity: framework/loader/mcp_cli.py::registry_cli_servers_prompt
		MCPCLI: mcpregistry.RegistryCLIServersPrompt,
	})
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", record.ID, err)
		return
	}
	record.Chat = built
	record.LLM = provider
	// Session title: written by the worker model after the first finished
	// turn, exactly as upstream's subscribe_title_trigger registers it on the
	// session bus.
	if record.EventBus != nil && sessionDir != "" {
		record.TitleTrigger = hiveserver.SubscribeTitleTrigger(record.EventBus, sessionDir, provider)
	}
	// The preloaded session's queen executor is its chat runtime, so trigger
	// timers and webhooks reach the Queen exactly like a created session.
	record.QueenExecutor = &hiveserver.QueenExecutor{
		NodeRegistry: map[string]hiveserver.QueenNode{"queen": built},
	}
}
