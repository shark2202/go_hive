//go:build !windows

package playbook

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the interpreter in its own process group so a
// cancelled run can signal the whole tree (script-spawned children included).
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcess asks the process group to stop (SIGTERM).
func terminateProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// forceKillProcess is the escalation after the grace period (SIGKILL).
func forceKillProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
