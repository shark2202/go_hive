//go:build unix

package nativecli

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the MCP server in its own process group so a
// shutdown can take its children with it.
func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup kills the server and anything it spawned.
func terminateProcessGroup(command *exec.Cmd) {
	if command.Process == nil || command.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
