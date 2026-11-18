//go:build windows

package builtin

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// terminalSignalGroup terminates the direct child. Windows has no POSIX
// process groups; the whole tree is not covered.
func terminalSignalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

// terminalTerminateTree terminates the direct child.
func terminalTerminateTree(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

// terminalExitCode reports the exit status; Windows has no signal exit codes.
func terminalExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	return state.ExitCode()
}

func terminalIsSignaledExit(code int) bool { return false }
