// Package hiveclock is the seam a benchmark harness uses to freeze the
// clock the user-facing surfaces report.
//
// The LongMemEval harness asks each question on the question's own date, so
// the queen's dated context (message stamps, get_current_time) must report
// that date rather than today. Upstream gets this by monkeypatching
// datetime.now in the modules that compute it; Go has no such hook, so the
// same three surfaces read this package and honor HIVE_FAKE_NOW (RFC3339)
// when it is set. With the variable unset, Now is exactly time.Now().
package hiveclock

import (
	"os"
	"strings"
	"time"
)

// FrozenEnv names the environment variable holding the frozen instant.
const FrozenEnv = "HIVE_FAKE_NOW"

// Now returns the frozen instant when FrozenEnv holds a parseable RFC3339
// timestamp, and time.Now() otherwise. A malformed value is ignored so a
// stray environment entry cannot wedge dated output.
func Now() time.Time {
	raw := strings.TrimSpace(os.Getenv(FrozenEnv))
	if raw == "" {
		return time.Now()
	}
	frozen, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Now()
	}
	return frozen
}
