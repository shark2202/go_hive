//go:build windows

package playbook

import "os/exec"

// configureProcessGroup has no process-group equivalent worth relying on here.
// Windows without a Python interpreter is an explicit unverified case for
// Python playbooks (see docs/playbook-python.md); Process.Kill covers the
// direct child.
func configureProcessGroup(cmd *exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func forceKillProcess(cmd *exec.Cmd) {
	terminateProcess(cmd)
}
