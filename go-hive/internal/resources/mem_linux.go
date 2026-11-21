//go:build linux

package resources

// System-memory probe for Linux: /proc/meminfo MemTotal/MemAvailable, the same
// two values psutil.virtual_memory() resolves on that platform.
//
// Parity: framework/host/runtime_resources.py::ResourceMonitor.sample (system
// memory branch).

import (
	"os"
	"strconv"
	"strings"
)

func systemMemorySample() systemMemory {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return systemMemory{}
	}
	totalKB, availKB := int64(0), int64(-1)
	for _, line := range strings.Split(string(raw), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availKB = value
		}
	}
	if totalKB <= 0 {
		return systemMemory{}
	}
	totalMB := float64(totalKB) / 1024.0
	out := systemMemory{TotalMB: floatPtr(roundTo(totalMB, 0))}
	if availKB < 0 {
		return out
	}
	availMB := float64(availKB) / 1024.0
	usedMB := totalMB - availMB
	pct := usedMB / totalMB * 100.0
	out.AvailMB = floatPtr(roundTo(availMB, 0))
	out.UsedMB = floatPtr(roundTo(usedMB, 0))
	out.Pct = floatPtr(roundTo(pct, 1))
	return out
}
