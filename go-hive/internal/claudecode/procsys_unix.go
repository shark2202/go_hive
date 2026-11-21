//go:build unix

package claudecode

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts a turn in its own process group, so stopping it
// takes the commands it started with it (upstream's start_new_session).
func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills the turn and everything it started.
//
// Parity: framework/server/claude_code_queens.py::_kill_tree
func killProcessTree(command *exec.Cmd) {
	if command == nil || command.Process == nil || command.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	_ = command.Process.Kill()
}
