//go:build !windows

package maintenance

import (
	"os"
	"syscall"
)

func lockFile(f *os.File) func() {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return func() {}
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
}
