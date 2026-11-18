//go:build !windows

package builtin

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so a timeout can
// take down the shell and every child it spawned.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the command's process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	// Setpgid makes the child the leader of its own group, so its pgid is the
	// pid at start. Kill that group directly: syscall.Getpgid(pid) fails once
	// the leader has exited, which is exactly the case where a background
	// child still holds the stdout/stderr pipes and keeps Wait blocked.
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == nil || err == syscall.ESRCH {
		return nil
	}
	pgid, getErr := syscall.Getpgid(pid)
	if getErr != nil {
		return processKill(cmd)
	}
	if killErr := syscall.Kill(-pgid, syscall.SIGKILL); killErr != nil {
		return processKill(cmd)
	}
	return nil
}

func processKill(cmd *exec.Cmd) error {
	err := cmd.Process.Kill()
	if err != nil && !os.IsExist(err) && err != os.ErrProcessDone {
		return err
	}
	return nil
}
