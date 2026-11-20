//go:build windows

package nativecli

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup matches CREATE_NEW_PROCESS_GROUP, so a console
// Ctrl-C aimed at the CLI does not reach the MCP server first.
const createNewProcessGroup = 0x00000200

// configureProcessGroup starts the MCP server in its own process group.
func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminateProcessGroup kills the server process.
func terminateProcessGroup(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	_ = command.Process.Kill()
}
