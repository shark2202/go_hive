//go:build !windows

package builtin

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// terminalSignalGroup delivers sig to the command's whole process group so
// children spawned by a shell job are covered, not just the direct child.
func terminalSignalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return cmd.Process.Signal(sig)
	}
	if err := syscall.Kill(-pgid, sig); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}

// terminalTerminateTree asks the group to stop, then force-kills it when the
// grace period expires.
func terminalTerminateTree(cmd *exec.Cmd, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pgid, 0) == syscall.ESRCH {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// terminalExitCode reports the wait status the way subprocess did: a negative
// signal number when the child was killed, otherwise the exit status.
func terminalExitCode(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -int(ws.Signal())
	}
	return state.ExitCode()
}

// terminalSignalForAction maps a manage action onto its POSIX signal.
func terminalSignalForAction(action string) (syscall.Signal, bool) {
	switch action {
	case "signal_int":
		return syscall.SIGINT, true
	case "signal_term":
		return syscall.SIGTERM, true
	case "signal_kill":
		return syscall.SIGKILL, true
	case "signal_hup":
		return syscall.SIGHUP, true
	case "signal_usr1":
		return syscall.SIGUSR1, true
	case "signal_usr2":
		return syscall.SIGUSR2, true
	}
	return 0, false
}

func terminalIsSignaledExit(code int) bool {
	switch -code {
	case int(syscall.SIGINT), int(syscall.SIGTERM), int(syscall.SIGKILL), int(syscall.SIGHUP), int(syscall.SIGUSR1), int(syscall.SIGUSR2):
		return code < 0
	}
	return false
}
