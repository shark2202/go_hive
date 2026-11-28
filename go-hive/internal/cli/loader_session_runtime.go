package cli

// Created-session chat runtime assembly: the flat lazy tool surface, the
// per-session runtime deps, and the title/trigger wiring a POST /api/sessions
// create builds. It lives here rather than in loader.go so
// createSessionWithRuntime stays inside the repository's frozen function-size
// budget; the code is unchanged.

import (
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// attachCreatedChatRuntime builds the queen runtime for a session created
// through POST /api/sessions. A runtime construction failure is reported on
// stdout and returns nil, so the session stays listable with Chat nil and
// POST /chat answers 503 instead of faking delivery against an endpoint that
// would 401.
func (r *Runner) attachCreatedChatRuntime(
	record *hiveserver.Session,
	sessionID string,
	sessionDir string,
	queenScope string,
	home hivehome.Home,
	tools *sessionToolCatalogue,
	provider llm.Completer,
	onTurnStart func(),
	initialInputPending bool,
) *hiveserver.ChatRuntime {
	skillsCatalog := liveSkillsCatalog(record, home, queenScope)
	// Flat lazy layer: first request advertises the Python reference's 24
	// always-on names; the rest of the default surface and the flat
	// directory tools load on demand via search_tools. Grouped mode
	// returns nils and keeps the full catalogue.
	alwaysOnTools, deferredTools := tools.LazySurfaceDeps()
	manifestNames, manifestFiltered := sessionManifestNames(tools, queenScope)
	// One registry executor carries both liveness handles: ProbeForTool
	// resolves the bare MCP alias and reads the shared connection, while
	// KillForTool force-disconnects a provably dead server.
	//
	// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
	// executor.kill_for_tool)
	toolLiveness := tools.Registry().GetExecutor()
	built, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM:               provider,
		Tools:             tools.ModelDefs(),
		AlwaysOnToolNames: alwaysOnTools,
		DeferredTools:     deferredTools,
		ManifestToolNames: manifestNames,
		ManifestFiltered:  manifestFiltered,
		MCPToolNamesAll:   tools.CatalogueMCPToolNamesAll(),
		Execute:           tools.Execute,
		ExecuteBlocks:     tools.ExecuteBlocks,
		// A tool that times out probes its shared MCP transport before the
		// loop reclaims it; the closures above cannot carry the methods,
		// so the catalogue's registry executor rides alongside.
		ToolProber: toolLiveness,
		ToolKiller: toolLiveness,
		Root:       sessionDir,
		Bus:        record.EventBus,
		SessionID:  sessionID,
		// The live session record: the colony pivot intercept stashes
		// session.pending_colony_pivot on it for the accept/dismiss routes.
		Session:   record,
		QueenName: record.QueenName,
		HomeRoot:  home.Root,
		// A prompt this create will deliver right after construction is
		// upstream's drained injection: the cold-resume mid-turn guard
		// must not park the session when fresh input is already headed in.
		InitialInputPending: initialInputPending,
		// The session's chat channel ("" on the dashboard): the queen
		// mail thread claims "queens", and a resumed session restored
		// it from meta.json. Read at each reminder render.
		Channel: func() string { return record.Channel() },
		// The catalog is phase-filtered at render time: an independent queen
		// drops worker/colony-only skills, a colony-bound queen announces the
		// colony view. Reads the live phase so a mid-session fork re-announces.
		//
		// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.render_skills_catalog
		SkillsCatalog: func() string {
			return skillsCatalog.CatalogForPhase(hiveserver.SessionPhaseLabel(record))
		},
		OnTurnStart: onTurnStart,
		// A text-only main model gets its tool images captioned by the
		// vision_fallback slot; nil when there is none to use.
		VisionCaptioner: visionCaptionerFor(home),
		// The queen's phase rides node_loop_iteration, as upstream's
		// iteration_metadata_provider reports it.
		Phase: func() string { return hiveserver.SessionPhaseLabel(record) },
		// The ambient "# Credentials" block of her static prompt: the
		// same store the credential routes read, snapshotted when the
		// session's prompt is built.
		//
		// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
		Credentials: sessionCredentialsProvider(home),
		// Full per-account detail for credentials pinned with
		// credentials(action=attach); re-read on every prompt build.
		//
		// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
		PinnedCredentials: sessionPinnedCredentialsProvider(home, record.ID, sessionDir),
		// The command-line MCP servers she runs with terminal_exec, right
		// after the credentials block.
		//
		// Parity: framework/loader/mcp_cli.py::registry_cli_servers_prompt
		MCPCLI: mcpregistry.RegistryCLIServersPrompt,
	})
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: chat runtime unavailable for %s: %v\n", sessionID, err)
	} else {
		record.Chat = built
		record.LLM = provider
		// Session title: written by the worker model after the first
		// finished turn, exactly as upstream's subscribe_title_trigger
		// registers it on the session bus — on the worker_llm slot when
		// configured, else the session's own model (queen_orchestrator's
		// _title_llm, resolved once at subscribe time).
		if record.EventBus != nil && record.QueenDir != "" {
			record.TitleTrigger = hiveserver.SubscribeTitleTrigger(record.EventBus, record.QueenDir, hiveserver.TitleTriggerLLM(home, provider))
		}
		// The session's queen executor is the chat runtime: timer and
		// webhook triggers reach the queen through it (queenNode →
		// InjectTrigger), which is the assignment the audit found missing.
		record.QueenExecutor = &hiveserver.QueenExecutor{
			NodeRegistry: map[string]hiveserver.QueenNode{"queen": built},
		}
	}
	return built
}
