package tasks

// Parity: framework/tasks/tools/_context.py

import "sync"

type ExecContext struct {
	AgentID      string
	SessionID    string
	UsageAgentID string
}

var (
	execContextMu sync.RWMutex
	execContext   ExecContext
)

func SetExecContext(ctx ExecContext) func() {
	execContextMu.Lock()
	previous := execContext
	execContext = ctx
	execContextMu.Unlock()
	return func() {
		execContextMu.Lock()
		execContext = previous
		execContextMu.Unlock()
	}
}

func CurrentExecContext() ExecContext {
	execContextMu.RLock()
	defer execContextMu.RUnlock()
	return execContext
}

func CurrentAgentID() string { return CurrentExecContext().AgentID }

func CurrentSessionID() string { return CurrentExecContext().SessionID }

func CurrentUsageAgentID() string { return CurrentExecContext().UsageAgentID }
