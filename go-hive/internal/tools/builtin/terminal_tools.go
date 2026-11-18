package builtin

import (
	"context"
	"fmt"
)

// Canonical terminal tool names.
const (
	ToolTerminalRG        = "terminal_rg"
	ToolTerminalGlob      = "terminal_glob"
	ToolTerminalOutputGet = "terminal_output_get"
	ToolTerminalJobStart  = "terminal_job_start"
	ToolTerminalJobLogs   = "terminal_job_logs"
	ToolTerminalJobManage = "terminal_job_manage"
	ToolTerminalPTYOpen   = "terminal_pty_open"
	ToolTerminalPTYRun    = "terminal_pty_run"
	ToolTerminalPTYClose  = "terminal_pty_close"
)

// terminalToolHandler runs one terminal tool. Every handler reports expected
// failures as a JSON envelope, so the error return is reserved for
// infrastructure faults.
type terminalToolHandler func(t *Toolbox, ctx context.Context, input map[string]any) (Result, error)

// terminalToolHandlers is the single registration point for the terminal tool
// surface.
var terminalToolHandlers = map[string]terminalToolHandler{
	ToolTerminalOutputGet: (*Toolbox).terminalOutputGet,
	ToolTerminalJobStart:  (*Toolbox).terminalJobStart,
	ToolTerminalJobLogs:   (*Toolbox).terminalJobLogs,
	ToolTerminalJobManage: (*Toolbox).terminalJobManage,
}

// executeTerminalTool dispatches a terminal tool, or reports the same
// unknown-tool error the builtin toolbox has always used.
func (t *Toolbox) executeTerminalTool(ctx context.Context, name string, input map[string]any) (Result, error) {
	if handler, ok := terminalToolHandlers[name]; ok {
		return handler(t, ctx, input)
	}
	return Result{}, fmt.Errorf("builtin: unknown tool %q", name)
}
