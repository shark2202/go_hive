//go:build windows

package builtin

import "context"

const terminalPTYUnsupported = "terminal_pty_* tools are POSIX-only; not supported on Windows"

// terminalPTYOpen reports that PTY sessions need a POSIX host.
func (t *Toolbox) terminalPTYOpen(ctx context.Context, input map[string]any) (Result, error) {
	return terminalErr(terminalPTYUnsupported), nil
}

// terminalPTYRun reports that PTY sessions need a POSIX host.
func (t *Toolbox) terminalPTYRun(ctx context.Context, input map[string]any) (Result, error) {
	return terminalErr(terminalPTYUnsupported), nil
}

// terminalPTYClose reports that PTY sessions need a POSIX host.
func (t *Toolbox) terminalPTYClose(ctx context.Context, input map[string]any) (Result, error) {
	return terminalErr(terminalPTYUnsupported), nil
}
