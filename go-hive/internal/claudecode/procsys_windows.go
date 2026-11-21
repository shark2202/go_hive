//go:build windows

package claudecode

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup matches CREATE_NEW_PROCESS_GROUP, so a console Ctrl-C
// aimed at Hive does not reach the turn first.
const createNewProcessGroup = 0x00000200

// configureProcessGroup starts a turn in its own process group.
func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killProcessTree kills the turn. Windows has no killpg in the standard
// library; Process.Kill ends the turn itself, and its children are left to the
// job object Windows gives the console process group.
//
// Parity: framework/server/claude_code_queens.py::_kill_tree
func killProcessTree(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
