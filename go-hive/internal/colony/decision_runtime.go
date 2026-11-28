package colony

// Per-worker decision ledgers.
//
// Every worker (and the persistent overseer) carries a stream-scoped decision
// runtime on its AgentContext, over one colony-wide storage root. Upstream
// wires the same seam in _build_worker/start_overseer through
// _make_runtime_adapter; a runtime with no storage root (DM host, tests) stays
// unwired rather than inventing a ledger path.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime._make_runtime_adapter

import (
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
)

// decisionRuntimes lazily owns the colony's shared stream ledger, so every
// worker stream writes runs/ under the colony directory through one storage.
// root is the storage path the caller configured (upstream ColonyRuntime's
// storage_path); empty means this runtime was built without a storage root and
// its workers stay unwired rather than writing into the process cwd.
type decisionRuntimes struct {
	mu     sync.Mutex
	root   string
	ledger *tracker.StreamLedger
}

func (d *decisionRuntimes) stream(streamID string) *tracker.StreamDecisionTracker {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ledger == nil {
		ledger, err := tracker.NewStreamLedger(d.root)
		if err != nil {
			return nil
		}
		d.ledger = ledger
	}
	stream, err := d.ledger.NewStream(streamID)
	if err != nil {
		return nil
	}
	return stream
}

// decisionRuntimeFor mirrors upstream's _make_runtime_adapter: a normal worker
// records as "worker:<worker_id>", while the persistent overseer is the
// colony's queen and records as "worker:overseer:<colony_id>" (upstream
// overseer_id = f"overseer:{stream_id}").
func (r *ColonyRuntime) decisionRuntimeFor(workerID string) *tracker.StreamDecisionTracker {
	if r == nil || r.decision.root == "" {
		return nil
	}
	return r.decision.stream("worker:" + workerID)
}

func (r *ColonyRuntime) buildContext(worker *Worker, spec agentloop.AgentSpec) *agentloop.AgentContext {
	colonyID := r.streamID
	// Workers run on the dedicated worker model when one is configured
	// (worker_llm); the persistent overseer is the colony's own Queen and
	// keeps the colony completer. The split mirrors upstream, where a worker
	// spawned through run_worker inherits the colony runtime's `_llm` — the
	// provider SessionManager built from worker_llm (falling back to
	// session.llm when the slot is unset).
	completer := r.llm
	if worker != nil && !worker.Persistent {
		completer = r.workerCompleter()
	}
	ctx := &agentloop.AgentContext{
		AgentID:        spec.ID,
		AgentSpec:      spec,
		LLM:            completer,
		StreamID:       worker.StreamID,
		ColonyID:       &colonyID,
		AccountsPrompt: r.accountsPrompt,
		// The operational protocols the forking queen carried ride every
		// worker/overseer context (upstream passes ColonyRuntime
		// .protocols_prompt into _build_worker and start_overseer).
		ProtocolsPrompt: r.protocolsPrompt,
	}
	// The decision runtime is the Go counterpart of upstream's
	// AgentContext(runtime=self._make_runtime_adapter(worker_id)). The
	// overseer's adapter is keyed by "overseer:<colony>", as upstream does.
	//
	// Wiring only: the agent loop never reads AgentContext.Runtime and no
	// production path calls Decide/RecordOutcome, so this seam records
	// nothing by itself (see tracker.DecisionTracker's writer-status note).
	// Keep it for parity; do not describe the ledger as a live audit chain.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
	workerID := worker.ID
	if worker.Persistent {
		workerID = "overseer:" + r.streamID
	}
	if decisionRuntime := r.decisionRuntimeFor(workerID); decisionRuntime != nil {
		ctx.Runtime = decisionRuntime
	}
	// Workers carry the colony goal into their prompt: criteria, MUST/SHOULD
	// constraints, and context. The persistent overseer keeps an empty goal
	// context (upstream StartOverseer sets goal_context=""), so the worker
	// goal never leaks into the queen's identity.
	//
	// Parity: framework/host/colony_runtime.py::ColonyRuntime._build_worker
	// (goal_context=self._goal.to_prompt_context()); framework/host/
	// colony_runtime.py::start_overseer (goal_context="").
	if !worker.Persistent && r.goal != nil {
		ctx.GoalContext = r.goal.ToPromptContext()
	}
	ctx.StopAfterToolBatch = func() bool { return worker.ExplicitReport() != nil }
	return ctx
}
