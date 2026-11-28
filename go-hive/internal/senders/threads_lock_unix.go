//go:build !windows

package senders

// OS-level lock for the threads store. Windows has no syscall.Flock; the
// windows counterpart is a documented no-op, matching internal/maintenance.

import (
	"os"
	"syscall"
)

// lockThreadFile takes an exclusive advisory lock. A failure degrades to the
// in-process mutex alone rather than refusing the append.
func lockThreadFile(f *os.File) func() {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}
