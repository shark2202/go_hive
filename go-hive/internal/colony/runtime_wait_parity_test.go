package colony

import (
	"testing"
	"time"
)

// The upstream default (`ColonyRuntime.wait_for_worker_reports(timeout: float =
// 600.0)`) is ten minutes: a worker batch the queen waits on must not be cut
// short at the port's old 60s, or she pays another model turn to re-ask.
//
// Parity: core/tests/test_colony_runtime_overseer.py::test_wait_for_worker_reports_collects_all
func TestDefaultReportWaitIsTenMinutes(t *testing.T) {
	if DefaultReportWait != 10*time.Minute {
		t.Fatalf("DefaultReportWait = %s, want 10m (upstream default 600.0s)", DefaultReportWait)
	}
}
