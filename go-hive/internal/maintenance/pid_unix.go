//go:build !windows

package maintenance

import (
	"errors"
	"syscall"
)

// Parity: framework/maintenance/janitor.py::_pid_alive
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
