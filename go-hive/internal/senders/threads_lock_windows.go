//go:build windows

package senders

// Windows has no flock in the standard syscall package and this module takes
// no dependencies for one: cross-process appends on Windows rely on the
// in-process mutex plus atomic replace. The residual is confined to Windows;
// Unix hosts get the real file lock.

import "os"

func lockThreadFile(f *os.File) func() {
	return func() {}
}
