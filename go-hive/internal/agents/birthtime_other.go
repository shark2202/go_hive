//go:build !darwin && !windows

package agents

// Parity: framework/agents/discovery.py::discover_agents

import (
	"os"
	"time"
)

func birthTime(info os.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
