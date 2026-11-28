//go:build darwin

package resources

// System-memory probe for macOS without cgo/psutil. The numbers mirror
// psutil's Darwin backend (psutil/arch/osx/mem.c), which is what upstream's
// psutil.virtual_memory() reads:
//
//	available = (Pages free + Pages inactive) * page size
//	used      = (Pages active + Pages wired down) * page size
//	pct       = used / total * 100
//
// "Pages speculative" is deliberately absent: psutil only subtracts it from
// the separate "free" field, never from available/used, so including it here
// would overstate available memory.
//
// Parity: framework/host/runtime_resources.py::ResourceMonitor.sample (system
// memory branch) via psutil.virtual_memory().

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
	vmStatActive   = regexp.MustCompile(`Pages active:\s+(\d+)\.`)
	vmStatWired    = regexp.MustCompile(`Pages wired down:\s+(\d+)\.`)
)

func systemMemorySample() systemMemory {
	totalBytes, ok := darwinTotalBytes()
	if !ok || totalBytes == 0 {
		return systemMemory{}
	}
	totalMB := float64(totalBytes) / 1048576.0
	out := systemMemory{TotalMB: floatPtr(roundTo(totalMB, 0))}

	out2, err := exec.Command("vm_stat").Output()
	if err != nil {
		return out
	}
	availMB, usedMB, hasAvail, hasUsed := parseDarwinVMStat(string(out2))
	if hasAvail {
		out.AvailMB = floatPtr(roundTo(availMB, 0))
	}
	if hasUsed {
		out.UsedMB = floatPtr(roundTo(usedMB, 0))
		if totalMB > 0 {
			out.Pct = floatPtr(roundTo(usedMB/totalMB*100.0, 1))
		}
	}
	return out
}

// parseDarwinVMStat turns `vm_stat` text into psutil's available/used MB.
// A missing counter degrades only the value it feeds.
func parseDarwinVMStat(text string) (availMB, usedMB float64, hasAvail, hasUsed bool) {
	pageSize := int64(4096)
	if match := vmStatPageSize.FindStringSubmatch(text); match != nil {
		if parsed, err := strconv.ParseInt(match[1], 10, 64); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}
	freePages, freeOK := vmStatPages(vmStatFree, text)
	inactivePages, inactiveOK := vmStatPages(vmStatInactive, text)
	activePages, activeOK := vmStatPages(vmStatActive, text)
	wiredPages, wiredOK := vmStatPages(vmStatWired, text)

	if freeOK && inactiveOK {
		availMB = float64((freePages+inactivePages)*pageSize) / 1048576.0
		hasAvail = true
	}
	if activeOK && wiredOK {
		usedMB = float64((activePages+wiredPages)*pageSize) / 1048576.0
		hasUsed = true
	}
	return availMB, usedMB, hasAvail, hasUsed
}

func vmStatPages(re *regexp.Regexp, text string) (int64, bool) {
	match := re.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	parsed, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
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
