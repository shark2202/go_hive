//go:build !windows

package browserbridge

import (
	"errors"
	"syscall"
)

// pidAlive reports whether pid is still running. Signal 0 performs the
// permission and existence check without delivering anything; EPERM means the
// process exists but belongs to another user.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}
