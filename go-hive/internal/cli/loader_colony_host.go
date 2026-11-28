package cli

// Colony-host assembly: the live colony runtime a session holds, its overseer
// and the model slots that back it. Split out of loader.go (which carries a
// frozen size budget) when the worker_llm / queen_llm slots were wired.
//
// Parity: framework/host/colony_runtime.py (ColonyRuntime.start_overseer),
// framework/server/session_manager.py::SessionManager.build_worker_llm

import (
	"context"
	"fmt"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	colonyhost "github.com/shark2202/go_hive/go-hive/internal/host"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// buildColonyHost constructs and starts the live colony runtime a
// `serve --colony` session holds (colony spawn/run/SSE entry). It returns nil
// when no provider is configured: a host without an LLM could never run a
// worker, so the session keeps its cold record and the runtime routes fall back
// to disk.
func (r *Runner) buildColonyHost(colonyPath, colonyID string, home hivehome.Home, model string) hiveserver.ColonyRuntime {
	built, err := r.newColonyHost(colonyPath, colonyID, home, model)
	if err != nil {
		fmt.Fprintf(r.Stdout, "Warning: colony runtime unavailable for %s: %v\n", colonyID, err)
		return nil
	}
	if err := built.Start(context.Background()); err != nil {
		fmt.Fprintf(r.Stdout, "Warning: colony runtime unavailable for %s: %v\n", colonyID, err)
		return nil
	}
	return built
}

// newColonyHost builds the colony runtime a serve session holds. Unlike
// buildColonyHost it returns the reason a runtime could not be built, so the
// colony lifecycle can report the failure instead of degrading to a cold
// session.
func (r *Runner) newColonyHost(colonyPath, colonyID string, home hivehome.Home, model string) (*colonyhost.Host, error) {
	// A colony belongs to a queen: when she has a model of her own
	// (queen_llm.<id>), her colony's overseer and workers run on it too.
	// Upstream shares session.llm with the colony runtime for the same reason.
	queenID := colonyQueenName(home, colonyID)
	provider, err := queenAwareChatProvider(home, model, queenID)
	if err != nil {
		return nil, err
	}
	// worker_llm gives workers a model of their own; nil (the default) keeps
	// them on the colony's provider, exactly as build_worker_llm's None does.
	workerProvider, err := workerChatProvider(home)
	if err != nil {
		return nil, err
	}
	// The worker the session runs is the one the colony defines on disk. A
	// colony with no worker config cannot be a real worker, so it keeps its
	// cold record instead of running a synthetic prompt.
	spec, err := agents.LoadAgentSpec(colonyPath)
	if err != nil {
		return nil, err
	}
	fields := spec.HostFields()
	toolbox := builtin.New(colonyPath)
	surface := newColonyWorkerSurface(colonyPath, toolbox, home)
	baseTools := withMCPWorkerTools(surface.baseTools, surface.declaredTools)
	built, err := colonyhost.New(colonyhost.Config{
		ColonyID:    colonyID,
		AgentSpec:   fields.AgentSpec,
		Goal:        fields.Goal,
		StoragePath: colonyPath,
		LLM:         provider,
		// nil shares LLM with the queen's provider.
		WorkerLLM: workerProvider,
		// worker_tools.always_enabled_categories overrides the worker tier
		// keep-set; nil keeps the hardcoded default and an empty list turns the
		// split off, as upstream documents.
		WorkerKeepSet: hiveserver.WorkerKeepSetFromConfig(home),
		Tools:         baseTools,
		// worker.json's "tools" list selects from this pool at worker start,
		// so a colony that declares an integration tool actually gets it.
		DeclaredTools: surface.declaredTools,
		ToolExecutor:  surface.execute,
		// A worker tool that times out probes and, when provably dead,
		// force-disconnects its shared MCP transport: the closure above
		// cannot carry methods, so the registry executor rides alongside.
		//
		// Parity: framework/loader/tool_registry.py (executor.probe_for_tool /
		// executor.kill_for_tool)
		ToolProber: surface.liveness,
		ToolKiller: surface.liveness,
		// The colony's workers see the owning queen's skill scope ahead of
		// the colony's own (colony overrides queen on collisions) — upstream
		// wires queen_ui into the colony runtime from the session's queen
		// (colony_runtime.py::_build_default_skills_config). A colony with no
		// recorded queen inherits the shared "default" queen, the same scope
		// its session tree uses.
		QueenDir: home.QueenDir(chatSessionQueenScope(queenID)),
		// Worker lineage: meta.json records the owning queen's id so a worker
		// directory found on disk can be attributed to her without a live
		// runtime (upstream writes ColonyRuntime._queen_id). A colony with no
		// recorded queen leaves it empty → meta.json queen_name = null.
		QueenName: queenID,
		// worker.json carries the source queen's operational protocols; the
		// worker context hands them to the generic prompt builder so they
		// reach the system prompt instead of staying an unread field.
		ProtocolsPrompt: fields.ProtocolsPrompt,
	})
	if err != nil {
		return nil, err
	}
	// Every worker's own tool calls run with its storage directory
	// (<colony>/workers/<id>) as the execution root: the builtin file sandbox
	// and the terminal default cwd resolve there, matching Python's Worker
	// (session_cwd = storage_path). The overseer keeps the colony root.
	//
	// Parity: framework/host/worker.py::Worker.run
	if runtime := built.Runtime(); runtime != nil {
		runtime.SetWorkerToolExecutorFactory(surface.executorForRoot)
	}
	return built, nil
}

// startColonyHost starts the colony runtime a session holds. Hive 1.0.15
// starts the runtime but attaches no overseer: the colony-bound queen chat is
// the session executor. Every failure is returned to the caller: the colony
// lifecycle turns a nil return into an HTTP 500/503 instead of leaving a
// session that looks loaded but can never answer.
func startColonyHost(ctx context.Context, built *colonyhost.Host, colonyID string) error {
	if err := built.Start(ctx); err != nil {
		return fmt.Errorf("start colony runtime for %s: %w", colonyID, err)
	}
	return nil
}
