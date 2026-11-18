//go:build windows

package builtin

import (
	"os"
	"os/exec"
)

// setProcessGroup is a no-op on Windows where /bin/bash is not available;
// terminal_exec reports the start failure to the model.
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup terminates the direct process. Windows has no POSIX
// process groups, so grandchildren are not covered.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if err != nil && !os.IsExist(err) && err != os.ErrProcessDone {
		return err
	}
	return nil
}
