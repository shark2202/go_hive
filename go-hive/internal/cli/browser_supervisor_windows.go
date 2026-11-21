//go:build windows

package cli

// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
//
// Windows side of the browser-bridge process plumbing. There is no process
// group to signal: the child is signalled directly and killed after the grace
// period, which is enough to stop one browserbridge process.

import (
	"os"
	"syscall"
)

func browserBridgeBinaryNames() []string { return []string{"browserbridge.exe", "browserbridge"} }

func browserBridgeExecutableName() string { return "browserbridge" }

func browserBridgeSysProcAttr() *syscall.SysProcAttr { return nil }

// browserBridgeDetachedSysProcAttr mirrors upstream's start_new_session=True,
// which on Windows is a no-op (CPython ignores the flag there): the detached
// child already survives this process because nothing signals it.
func browserBridgeDetachedSysProcAttr() *syscall.SysProcAttr { return nil }

// browserProcessAlive reports whether pid still exists. Windows has no
// signal-0 probe; os.FindProcess opens a handle and fails for a PID that no
// longer exists. The supervisor uses it to refuse a respawn into a dead
// runtime, the guard upstream's _run_supervisor parent watchdog enforces.
//
// Parity: tools/src/gcu/bridge_host.py::_run_supervisor
func browserProcessAlive(pid int) bool {
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

func browserBridgeSignal(process *os.Process, sig syscall.Signal) error {
	if process == nil {
		return nil
	}
	return process.Signal(sig)
}
