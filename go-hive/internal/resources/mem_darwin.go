//go:build darwin

package resources

// System-memory probe for macOS without cgo/psutil: total from sysctl
// hw.memsize, available from vm_stat's free + inactive + speculative pages
// (the same approximation psutil's macOS backend uses for "available").
//
// Parity: framework/host/runtime_resources.py::ResourceMonitor.sample (system
// memory branch).

import (
	"encoding/binary"
	"os/exec"
	"regexp"
	"strconv"
	"syscall"
)

var (
	vmStatPageSize = regexp.MustCompile(`page size of (\d+) bytes`)
	vmStatFree     = regexp.MustCompile(`Pages free:\s+(\d+)\.`)
	vmStatInactive = regexp.MustCompile(`Pages inactive:\s+(\d+)\.`)
	vmStatSpec     = regexp.MustCompile(`Pages speculative:\s+(\d+)\.`)
)

func systemMemorySample() systemMemory {
	totalBytes, ok := darwinTotalBytes()
	if !ok || totalBytes == 0 {
		return systemMemory{}
	}
	totalMB := float64(totalBytes) / 1048576.0
	out := systemMemory{TotalMB: floatPtr(roundTo(totalMB, 0))}

	avail, ok := darwinAvailableMB()
	if !ok {
		return out
	}
	used := totalMB - avail
	pct := 0.0
	if totalMB > 0 {
		pct = used / totalMB * 100.0
	}
	out.AvailMB = floatPtr(roundTo(avail, 0))
	out.UsedMB = floatPtr(roundTo(used, 0))
	out.Pct = floatPtr(roundTo(pct, 1))
	return out
}

func darwinAvailableMB() (float64, bool) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, false
	}
	text := string(out)
	pageSize := int64(4096)
	if match := vmStatPageSize.FindStringSubmatch(text); match != nil {
		if parsed, err := strconv.ParseInt(match[1], 10, 64); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	pages := int64(0)
	found := false
	for _, re := range []*regexp.Regexp{vmStatFree, vmStatInactive, vmStatSpec} {
		if match := re.FindStringSubmatch(text); match != nil {
			if parsed, err := strconv.ParseInt(match[1], 10, 64); err == nil {
				pages += parsed
				found = true
			}
		}
	}
	if !found {
		return 0, false
	}
	return float64(pages*pageSize) / 1048576.0, true
}

// darwinTotalBytes reads hw.memsize through syscall.Sysctl, which returns the
// raw 8-byte integer as a string (SysctlUint64 was removed from the package).
func darwinTotalBytes() (uint64, bool) {
	raw, err := syscall.Sysctl("hw.memsize")
	if err != nil || len(raw) < 8 {
		return 0, false
	}
	return binary.LittleEndian.Uint64([]byte(raw)[:8]), true
}
