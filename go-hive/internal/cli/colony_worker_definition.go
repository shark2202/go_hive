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

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/atomicfs"
	hivecolony "github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/queen"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// writeColonyFromSession writes the colony directory the fork resumes from:
// metadata.json plus a worker.json carrying the worker runtime identity (the
// worker system prompt from the worker definition, never the queen's persona)
// and the user's handoff goal when the dialog supplied one.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony
func writeColonyFromSession(colonyPath, colonyID, queenName string, params hiveserver.CreateColonyFromSourceParams) error {
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		return err
	}
	meta := map[string]any{
		"name":              colonyID,
		"created_at":        float64(time.Now().UnixNano()) / 1e9,
		"source_session_id": params.SourceSessionID,
	}
	if queenName != "" {
		meta["queen_name"] = queenName
	}
	if err := atomicfs.WriteJSON(filepath.Join(colonyPath, "metadata.json"), meta, 0o644); err != nil {
		return err
	}

	goal := "Work on tasks for colony " + colonyID
	if params.InitialPrompt != nil && strings.TrimSpace(*params.InitialPrompt) != "" {
		goal = strings.TrimSpace(*params.InitialPrompt)
	}
	// The forked worker runs its own identity: the worker definition's prompt
	// (focus, no escalation, fail-fast) with the handoff task appended, as
	// upstream build_meta writes it. The queen's persona belongs to the
	// colony's overseer, which startColonyHost arms separately.
	worker := map[string]any{
		"name":          humanizeColonyName(colonyID),
		"description":   "Colony " + colonyID,
		"system_prompt": hivecolony.BuildSystemPrompt(&goal),
		"tools":         colonyToolsFor(queenName),
		"goal":          map[string]any{"description": goal},
	}
	if queenName != "" {
		worker["queen_name"] = queenName
	}
	return atomicfs.WriteJSON(filepath.Join(colonyPath, "worker.json"), worker, 0o644)
}

// colonyToolsFor is the declared tool set the forked worker config carries: the
// bound queen's default tool categories, or an empty list (never null) when the
// colony has no queen binding.
//
// The values are the queen profile's category labels (the Go fork path has no
// live queen tool snapshot, which is what upstream writes here); the colony
// runtime expands a category label to the tools it stands for when it applies
// the worker's declared set. Unknown labels resolve to nothing and are dropped.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony
func colonyToolsFor(queenName string) []string {
	if tools := queen.DefaultToolsFor(queenName); len(tools) > 0 {
		return tools
	}
	return []string{}
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

// colonyWorkerToolSurface assembles the colony worker's tool wiring: the
// builtin base surface every worker keeps, the declared-tool pool a
// worker.json "tools" list selects from (the session integration catalogue),
// and the executor that runs both under the colony workspace root. The
// executor carries the catalogue's content blocks, so a worker's attach_file
// reaches its model as image/PDF parts exactly like the queen's.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime (spawn_tools +
// _apply_tool_allowlist resolution for a colony worker)
func colonyWorkerToolSurface(colonyPath string, toolbox *builtin.Toolbox, home hivehome.Home) ([]llm.ToolDef, []llm.ToolDef, func(agentloop.ToolCallRequest) (agentloop.ToolResult, error)) {
	catalogue := newSessionToolCatalogue(colonyPath, toolbox, home)
	// The agent package this worker runs is the colony directory, so its base
	// name is the agent name upstream gives every MCP subprocess of the agent
	// (agent_loader uses agent_path.name). Injected into the worker catalogue's
	// MCP env and the subprocess-spawning tools' env input.
	if name := filepath.Base(filepath.Clean(colonyPath)); name != "" && name != "." && name != string(filepath.Separator) {
		catalogue.SetAgentName(name)
	}
	execute := func(request agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		content, isError, imageContent, execErr := catalogue.ExecuteBlocks(context.Background(), request.Name, request.Input)
		if execErr != nil {
			return agentloop.ToolResult{}, execErr
		}
		return agentloop.ToolResult{Content: content, IsError: isError, ImageContent: imageContent}, nil
	}
	return toolbox.Defs(), catalogue.Defs(), execute
}
