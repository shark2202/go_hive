//go:build windows

package lsp

import "os/exec"

// configureProcessGroup is a no-op on Windows: there is no portable process
// group equivalent, so the client kills the direct child.
func configureProcessGroup(cmd *exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}

func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
