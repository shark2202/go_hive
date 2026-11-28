package colony

// Per-worker tool execution root.
//
// Parity: framework/host/worker.py::Worker.run — a worker scopes its tool
// execution context to its own storage directory (ToolRegistry
// .set_execution_context(session_cwd=str(self._storage_path))), so its
// built-in file tools operate inside <colony>/workers/<id> and its terminal
// tools default to that directory. The colony overseer keeps the colony
// workspace (start_overseer passes storage_path=<colony>), and nothing here
// touches that path.

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// SetWorkerToolExecutorFactory installs the factory that builds a worker's own
// tool executor for its storage directory. The host's single ToolExecutor is
// rooted at the colony workspace, so the factory receives that colony-root
// executor as base and returns a worker-scoped executor: built-in tools (file
// ops, terminal) resolve under the worker root, and anything the worker scope
// does not own falls back to base, so a host-provided executor (a trigger
// pipeline's registry, for example) keeps working.
//
// The factory is consulted once per worker run (and again on resume). The
// persistent overseer never uses it: it stays on the colony-root executor.
// Call before Start; a nil factory (the default) keeps every worker on the
// colony-root ToolExecutor.
func (r *ColonyRuntime) SetWorkerToolExecutorFactory(factory func(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor) {
	if r == nil {
		return
	}
	r.workerExecMu.Lock()
	r.workerExecFactory = factory
	r.workerExecMu.Unlock()
}

// workerExecutorFactory returns the installed factory, or nil when none was
// set (the colony-root executor is then used for every worker).
func (r *ColonyRuntime) workerExecutorFactory() func(root string, base agentloop.ToolExecutor) agentloop.ToolExecutor {
	if r == nil {
		return nil
	}
	r.workerExecMu.RLock()
	defer r.workerExecMu.RUnlock()
	return r.workerExecFactory
}

// workerExecutionRoot is the on-disk root a worker's own tool calls resolve
// against: the worker's storage directory (<colony>/workers/<id>). An empty
// result means the worker has no own storage (a hand-built worker in tests,
// or the persistent overseer, whose StoragePath is the colony root): those
// keep the runtime's colony-root ToolExecutor.
func workerExecutionRoot(worker *Worker) string {
	if worker == nil || worker.Persistent {
		return ""
	}
	return strings.TrimSpace(worker.StoragePath)
}
