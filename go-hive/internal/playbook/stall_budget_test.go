package playbook

import (
	"testing"
	"time"
)

// Parity-exempt: upstream runs a playbook in-process, so it has no parent-side
// silence guard to pin; the port keeps one (Execute's stall budget) and this
// test fixes its default so it cannot drift below a legitimate worker
// dispatch (upstream PlaybookRun(default_timeout=600), pythonStallGrace=300).
func TestDefaultStallTimeoutOutlastsAWorkerDispatch(t *testing.T) {
	if got := DefaultStallTimeout(); got != 900*time.Second {
		t.Fatalf("DefaultStallTimeout = %s, want 900s (600s dispatch + 300s grace)", got)
	}
	if DefaultStallTimeout() <= pythonDefaultTimeout {
		t.Fatalf("the silence guard (%s) must not cut a full-length worker dispatch (%s) short",
			DefaultStallTimeout(), pythonDefaultTimeout)
	}
	if pythonDefaultTimeout != 600*time.Second {
		t.Fatalf("pythonDefaultTimeout = %s, want upstream's 600s dispatch default", pythonDefaultTimeout)
	}
}
