//go:build windows

package maintenance

import "syscall"

// Parity: framework/maintenance/janitor.py::_pid_alive
func PidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	openProcess := kernel32.NewProc("OpenProcess")
	closeHandle := kernel32.NewProc("CloseHandle")
	const processQueryLimitedInformation = 0x1000
	handle, _, _ := openProcess.Call(processQueryLimitedInformation, 0, uintptr(uint32(pid)))
	if handle == 0 {
		return false
	}
	_, _, _ = closeHandle.Call(handle)
	return true
}
