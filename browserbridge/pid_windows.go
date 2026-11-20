//go:build windows

package browserbridge

import "os"

// pidAlive reports whether pid is still running. Windows has no signal-0
// probe; os.FindProcess opens a handle and fails for a PID that no longer
// exists, which is enough for the reaper's owner check. The handle is released
// immediately so a long-lived bridge cannot leak kernel handles per check.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = process.Release()
	return true
}
