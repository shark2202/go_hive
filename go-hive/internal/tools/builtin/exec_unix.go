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
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return processKill(cmd)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
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
