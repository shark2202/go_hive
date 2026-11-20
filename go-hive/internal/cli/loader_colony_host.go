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
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/agents"
	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/queen"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
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
	provider, err := queenAwareChatProvider(home, model, colonyQueenName(home, colonyID))
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
	baseTools, declaredTools, executeTool := colonyWorkerToolSurface(colonyPath, toolbox, home)
	baseTools = withMCPWorkerTools(baseTools, declaredTools)
	return colonyhost.New(colonyhost.Config{
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
		DeclaredTools: declaredTools,
		ToolExecutor:  executeTool,
	})
}

// startColonyHost arms a colony host and starts its client-facing overseer.
// Every failure is returned to the caller: the colony lifecycle turns a nil
// return into an HTTP 500/503 instead of leaving a session that looks loaded
// but can never answer.
func startColonyHost(ctx context.Context, built *colonyhost.Host, home hivehome.Home, queenName, colonyID string) error {
	if err := built.Start(ctx); err != nil {
		return fmt.Errorf("start colony runtime for %s: %w", colonyID, err)
	}
	if _, err := built.StartOverseer(ctx, queenOverseerSpec(home, queenName), nil); err != nil {
		built.Stop()
		return fmt.Errorf("start colony overseer for %s: %w", colonyID, err)
	}
	return nil
}

// queenOverseerSpec is the identity the colony's persistent overseer runs
// under: the bound queen's persona when a profile is readable, otherwise a
// plain queen prompt so a colony without a materialised profile still has a
// client-facing overseer.
func queenOverseerSpec(home hivehome.Home, queenName string) agentloop.AgentSpec {
	prompt := "You are the queen of this colony. Coordinate its workers and report progress to the user."
	name := "Queen"
	if queenID := strings.TrimSpace(queenName); queenID != "" {
		name = queenID
		if profile, err := queen.LoadQueenProfile(home.QueensDir(), queenID); err == nil {
			if identity := strings.TrimSpace(queen.FormatQueenIdentityPrompt(profile, nil)); identity != "" {
				prompt = identity
			}
		}
	}
	return agentloop.AgentSpec{
		ID:               "queen",
		Name:             name,
		AgentType:        "event_loop",
		ToolAccessPolicy: "explicit",
		MaxRetries:       3,
		SystemPrompt:     &prompt,
	}
}
