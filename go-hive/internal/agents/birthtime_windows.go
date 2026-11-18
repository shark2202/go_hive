//go:build windows

package agents

// Parity: framework/agents/discovery.py::discover_agents

import (
	"os"
	"syscall"
	"time"
)

func birthTime(info os.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, stat.CreationTime.Nanoseconds()), true
}
