package playbook

// Parity: framework/tools/playbook_tools.py::_fast_result_grace
//
// The playbook tool awaits a run for a short grace window: a fail-fast
// playbook (a NameError, a bad query — all before any worker is dispatched)
// returns its real outcome synchronously, while a genuinely long run falls
// through to "started" plus a completion notification.
//
// Upstream:
//
//	def _fast_result_grace() -> float:
//	    try:
//	        return max(0.0, float(os.environ.get("HIVE_PLAYBOOK_FAST_GRACE", "1.5")))
//	    except ValueError:
//	        return 1.5
//
// so an unset/empty/garbage value is 1.5, a negative value clamps to 0 (pure
// async), and 0 disables the wait entirely.

import (
	"math"

	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

const defaultFastGraceSeconds = 1.5

// envFloat keeps the scanner-visible `envFloat("HIVE_…", default)` shape.
func envFloat(name string, def float64) float64 { return pyenv.Float(name, def) }

// FastGraceSeconds resolves HIVE_PLAYBOOK_FAST_GRACE with upstream's clamp.
func FastGraceSeconds() float64 {
	value := envFloat("HIVE_PLAYBOOK_FAST_GRACE", 1.5)
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	return value
}
