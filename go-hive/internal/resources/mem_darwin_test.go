//go:build darwin

package resources

import (
	"math"
	"testing"
)

// Parity: psutil/arch/osx/mem.c::psutil_virtual_mem — available is
// free + inactive and used is active + wired; speculative is excluded from
// both.
func TestParseDarwinVMStatMatchesPsutil(t *testing.T) {
	const pageSize = 16384
	text := "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n" +
		"Pages free:                          1000.\n" +
		"Pages active:                        3000.\n" +
		"Pages inactive:                      2000.\n" +
		"Pages speculative:                   9999.\n" +
		"Pages wired down:                     500.\n"
	avail, used, hasAvail, hasUsed := parseDarwinVMStat(text)
	if !hasAvail || !hasUsed {
		t.Fatalf("parse flags = avail:%v used:%v, want both true", hasAvail, hasUsed)
	}
	wantAvail := float64((1000+2000)*pageSize) / 1048576.0
	wantUsed := float64((3000+500)*pageSize) / 1048576.0
	if math.Abs(avail-wantAvail) > 1e-9 {
		t.Fatalf("available = %v MB, want %v MB (free+inactive, no speculative)", avail, wantAvail)
	}
	if math.Abs(used-wantUsed) > 1e-9 {
		t.Fatalf("used = %v MB, want %v MB (active+wired)", used, wantUsed)
	}
	if avail == wantAvail+float64(9999*pageSize)/1048576.0 {
		t.Fatal("speculative pages were counted in available memory")
	}
}

// A missing wired counter degrades used/pct but keeps available, mirroring the
// probe's per-field degradation.
// Parity-exempt: Go-only Darwin vm_stat parsing; upstream reads psutil.
func TestParseDarwinVMStatPartial(t *testing.T) {
	text := "page size of 4096 bytes\nPages free: 10.\nPages inactive: 20.\nPages active: 30.\n"
	avail, used, hasAvail, hasUsed := parseDarwinVMStat(text)
	if !hasAvail || hasUsed {
		t.Fatalf("flags = avail:%v used:%v, want avail only", hasAvail, hasUsed)
	}
	if want := float64(30*4096) / 1048576.0; math.Abs(avail-want) > 1e-9 {
		t.Fatalf("available = %v, want %v", avail, want)
	}
	if used != 0 {
		t.Fatalf("used = %v, want 0 when wired is absent", used)
	}
}
