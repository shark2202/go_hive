package cli

// Colony fork worker definition: the on-disk worker.json a forked colony
// resumes from, and the tool surface its workers run with. Kept out of
// loader.go so the serve assembly entry point stays under its frozen size
// budget.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	hivecolony "github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// colonyForkWorkerName is the single worker definition the DM fork's
// worker.json declares, verbatim upstream.
const colonyForkWorkerName = "worker"

// writeColonyFromSession writes the colony directory the fork resumes from:
// metadata.json plus a worker.json carrying the live queen surface the source
// session had at fork time (tools, skills/protocols prompts, skill dirs, loop
// config, queen phase/id) and the user's handoff goal when the dialog supplied
// one. It returns the colony binding it threaded into the worker's input_data
// so the caller can stamp the source session with it.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony
// Parity: framework/agents/queen/worker_definition.py::build_meta
func writeColonyFromSession(
	colonyPath, colonyID, queenName string,
	source *hiveserver.Session,
	params hiveserver.CreateColonyFromSourceParams,
) (*tracker.Binding, error) {
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		return nil, err
	}
	// tracker.db first, so its absolute path is threaded into the worker's
	// input_data binding before worker.json is written (upstream
	// ensure_tracker_db runs before build_meta).
	trackerDB, err := tracker.EnsureTrackerDB(colonyPath)
	if err != nil {
		return nil, err
	}
	dir := colonyPath
	if absolute, absErr := filepath.Abs(colonyPath); absErr == nil {
		dir = absolute
	}
	binding := &tracker.Binding{Name: colonyID, Dir: dir, TrackerDB: trackerDB}

	goal := "Work on tasks for colony " + colonyID
	if params.InitialPrompt != nil && strings.TrimSpace(*params.InitialPrompt) != "" {
		goal = strings.TrimSpace(*params.InitialPrompt)
	}
	// The forked worker runs its own identity (the worker definition's prompt):
	// the queen's persona belongs to the colony's overseer, which
	// startColonyHost arms separately. Everything else in worker.json is the
	// live queen surface, as upstream build_meta writes it.
	surface := colonyForkQueenSurface(source)
	var loopConfig *hivecolony.QueenLoopConfig
	if surface.HasLoop {
		loopConfig = &hivecolony.QueenLoopConfig{
			MaxIterations:      surface.MaxIterations,
			ToolCallBudget:     surface.ToolCallBudget,
			MaxContextTokens:   surface.MaxContextTokens,
			MaxToolResultChars: surface.MaxToolResultChars,
		}
	}
	skillsCatalog, protocolsPrompt, skillDirs := colonyForkQueenPrompts(source)
	worker := hivecolony.BuildMeta(hivecolony.WorkerMetaInput{
		WorkerName:          colonyForkWorkerName,
		SourceSessionID:     params.SourceSessionID,
		Task:                &goal,
		ToolNames:           colonySiblingInheritedToolNames(surface.ToolNames),
		SkillsCatalogPrompt: skillsCatalog,
		ProtocolsPrompt:     protocolsPrompt,
		SkillDirs:           skillDirs,
		QueenLoopConfig:     loopConfig,
		QueenPhase:          colonyForkQueenPhase(source),
		QueenID:             colonyForkQueenID(source),
		InputData: hivecolony.BuildInputData(hivehome.Colony{
			Name:      binding.Name,
			Dir:       binding.Dir,
			TrackerDB: binding.TrackerDB,
		}, ""),
	})
	if err := atomicfs.WriteJSON(filepath.Join(colonyPath, "worker.json"), worker, 0o644); err != nil {
		return nil, err
	}
	return binding, colonyForkWriteMetadata(colonyPath, colonyID, queenName, params.SourceSessionID, goal)
}

// colonyForkQueenSurface reads the forkable surface of the source queen. A
// session with no chat runtime contributes nothing, which is upstream's
// queen_ctx None path: the worker inherits no tools and keeps the default
// worker loop config.
func colonyForkQueenSurface(source *hiveserver.Session) hiveserver.QueenForkSurface {
	if source == nil || source.Chat == nil {
		return hiveserver.QueenForkSurface{}
	}
	return source.Chat.ForkSurfaceSnapshot()
}

// colonyForkQueenPhase is the source queen's phase label written to
// worker.json (upstream's phase_state.phase, empty when no state is stamped).
func colonyForkQueenPhase(source *hiveserver.Session) string {
	if source == nil {
		return ""
	}
	return hiveserver.SessionPhaseLabel(source)
}

// colonyForkQueenID is the source queen's identity written to worker.json
// (upstream's phase_state.queen_id, empty when no state is stamped).
func colonyForkQueenID(source *hiveserver.Session) string {
	if source == nil {
		return ""
	}
	return colonySiblingQueenID(source)
}

// colonyForkQueenPrompts reads the source queen's rendered skills catalog
// (filtered for her live phase, as queen_ctx carries it), protocols prompt and
// skill dirs from her live skills manager. A session with no manager (a cold
// record) contributes empties, matching upstream's queen_ctx None path.
func colonyForkQueenPrompts(source *hiveserver.Session) (skillsCatalog, protocols string, dirs []string) {
	if source == nil || source.LiveSkills == nil {
		return "", "", nil
	}
	if catalog, ok := source.LiveSkills.(*queenSkillsCatalog); ok {
		skillsCatalog = catalog.CatalogForPhase(colonyForkQueenPhase(source))
		if manager := catalog.managerInstance(); manager != nil {
			protocols = manager.ProtocolsPrompt()
			dirs = manager.AllowlistedDirs()
		}
		return skillsCatalog, protocols, dirs
	}
	return source.LiveSkills.SkillsCatalogPrompt(), "", nil
}

// colonyForkWriteMetadata records the fork's colony provenance: the source
// session it came from, the worker slot it spawned and the timestamps the UI
// and loader read back. queen_session_id is stamped later, once the session
// opens (recordColonyQueenSessionID).
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony (metadata.json)
func colonyForkWriteMetadata(colonyPath, colonyID, queenName, sourceSessionID, task string) error {
	now := time.Now().UTC()
	stamp := colonySiblingTimestamp(now)
	meta := map[string]any{
		"name":              colonyID,
		"colony_id":         colonyID,
		"created_at":        float64(now.UnixNano()) / 1e9,
		"updated_at":        stamp,
		"source_session_id": sourceSessionID,
		"workers": map[string]any{
			colonyForkWorkerName: map[string]any{
				"task":       colonyForkWorkerTask(task),
				"spawned_at": stamp,
			},
		},
	}
	if queenName != "" {
		meta["queen_name"] = queenName
	}
	return atomicfs.WriteJSON(filepath.Join(colonyPath, "metadata.json"), meta, 0o644)
}

// colonyForkWorkerTask carries the worker's committed task into metadata,
// truncated to 100 characters like upstream's “worker_task[:100]“.
func colonyForkWorkerTask(task string) string {
	runes := []rune(strings.TrimSpace(task))
	if len(runes) > 100 {
		return string(runes[:100])
	}
	return string(runes)
}

// recordColonyQueenSessionID stamps the session id a forked colony resumes
// under, mirroring upstream's fork metadata (routes_execution writes
// metadata["queen_session_id"] = colony_session_id). The loader's
// `serve --colony` restart adopts a recorded id when its session directory
// still exists, so the fork continues the same transcript instead of stranding
// it under an id nothing addresses. An existing non-empty id is preserved (the
// first fork owns the colony session); a failed write only costs a fresh id on
// the next boot.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony
func recordColonyQueenSessionID(colonyPath, sessionID, queenName string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	path := filepath.Join(colonyPath, "metadata.json")
	meta, ok := readJSONObject(path)
	if !ok || meta == nil {
		meta = map[string]any{}
	}
	if existing, present := meta["queen_session_id"].(string); present && strings.TrimSpace(existing) != "" {
		return
	}
	meta["queen_session_id"] = sessionID
	if _, present := meta["queen_name"]; !present && queenName != "" {
		meta["queen_name"] = queenName
	}
	_ = atomicfs.WriteJSON(path, meta, 0o644)
}

// colonyWorkerSurface is the assembled colony-worker tool wiring: the
// builtin base surface every worker keeps, the declared-tool pool a
// worker.json "tools" list selects from (the session integration catalogue),
// the executor that runs both under the colony workspace root, and the
// factory that rebuilds the same wiring under a worker's own storage
// directory. The executor carries the catalogue's content blocks, so a
// worker's attach_file reaches its model as image/PDF parts exactly like the
// queen's.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime (spawn_tools +
// _apply_tool_allowlist resolution for a colony worker)
type colonyWorkerSurface struct {
	catalogue     *sessionToolCatalogue
	baseTools     []llm.ToolDef
	declaredTools []llm.ToolDef
	liveness      *tools.RegistryExecutor
}

// newColonyWorkerSurface builds the worker tool surface for a colony
// workspace root. The catalogue is built once; per-worker executors are
// derived from it with WithRoot, so definitions and installed integrations
// stay shared.
func newColonyWorkerSurface(colonyPath string, toolbox *builtin.Toolbox, home hivehome.Home) *colonyWorkerSurface {
	catalogue := newSessionToolCatalogue(colonyPath, toolbox, home)
	// The agent package this worker runs is the colony directory, so its base
	// name is the agent name upstream gives every MCP subprocess of the agent
	// (agent_loader uses agent_path.name). Injected into the worker catalogue's
	// MCP env and the subprocess-spawning tools' env input.
	if name := filepath.Base(filepath.Clean(colonyPath)); name != "" && name != "." && name != string(filepath.Separator) {
		catalogue.SetAgentName(name)
	}
	return &colonyWorkerSurface{
		catalogue:     catalogue,
		baseTools:     toolbox.Defs(),
		declaredTools: catalogue.Defs(),
		liveness:      catalogue.Registry().GetExecutor(),
	}
}

// execute runs one worker tool call under the colony workspace root. It is
// the executor the colony runtime holds for the overseer and for any worker
// without its own storage root.
func (s *colonyWorkerSurface) execute(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	return s.executorAt(s.catalogue)(request)
}

// executorForRoot returns the executor a single worker's own tool calls run
// through: the same definitions and integrations, with the builtin file-tool
// sandbox and the injected session_cwd moved to the worker's storage
// directory (<colony>/workers/<id>). Upstream scopes a Worker's execution
// context to its storage path, so a worker's relative file paths and its
// terminal's default cwd are the worker directory, not the colony's.
//
// Only the builtin surface is re-rooted: every other tool (integrations, MCP,
// a trigger pipeline's registry) delegates to base, which is the executor the
// runtime would have used for the worker anyway. That keeps the worker tool
// surface and any host-injected executor intact while the file/terminal root
// follows the worker's storage.
//
// Parity: framework/host/worker.py::Worker.run (session_cwd=str(storage_path))
func (s *colonyWorkerSurface) executorForRoot(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor {
	root = strings.TrimSpace(root)
	if root == "" || s.catalogue == nil {
		return base
	}
	rooted := s.executorAt(s.catalogue.WithRoot(root))
	return func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		if _, builtin := s.catalogue.builtinNames[request.Name]; builtin {
			return rooted(request)
		}
		if base != nil {
			return base(request)
		}
		return rooted(request)
	}
}

// executorAt wraps one catalogue's ExecuteBlocks in the agent-loop executor
// signature.
func (s *colonyWorkerSurface) executorAt(catalogue *sessionToolCatalogue) func(agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
	return func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		content, isError, imageContent, execErr := catalogue.ExecuteBlocks(context.Background(), request.Name, request.Input)
		if execErr != nil {
			return agentloop.ToolResult{}, execErr
		}
		return agentloop.ToolResult{Content: content, IsError: isError, ImageContent: imageContent}, nil
	}
}

// colonyWorkerToolSurface assembles the colony worker's tool wiring rooted at
// the colony workspace.
func colonyWorkerToolSurface(colonyPath string, toolbox *builtin.Toolbox, home hivehome.Home) ([]llm.ToolDef, []llm.ToolDef, func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)) {
	surface := newColonyWorkerSurface(colonyPath, toolbox, home)
	return surface.baseTools, surface.declaredTools, surface.execute
}

// colonyWorkerToolSurfaceWithLiveness is colonyWorkerToolSurface plus the
// catalogue's registry executor. The tool-timeout recovery needs a
// probe_for_tool / kill_for_tool pair on the loop, and the executor closure
// above cannot carry methods, so the registry executor travels separately and
// the colony runtime hands it to every worker/overseer loop.
//
// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
// executor.kill_for_tool)
func colonyWorkerToolSurfaceWithLiveness(colonyPath string, toolbox *builtin.Toolbox, home hivehome.Home) ([]llm.ToolDef, []llm.ToolDef, func(agentloop.ToolCallRequest) (agentloop.ToolResult, error), *tools.RegistryExecutor) {
	surface := newColonyWorkerSurface(colonyPath, toolbox, home)
	return surface.baseTools, surface.declaredTools, surface.execute, surface.liveness
}
