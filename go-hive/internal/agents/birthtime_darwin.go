//go:build darwin

package agents

// Parity: framework/agents/discovery.py::discover_agents

import (
	"os"
	"syscall"
	"time"
)

func birthTime(info os.FileInfo) (time.Time, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec), true
}
