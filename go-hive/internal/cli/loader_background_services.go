package cli

// Serve-path background services. loader.go is frozen at 1936 lines by the
// size gate, so both lifecycles live here and the loader only defers one call.
//
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running
// Parity: framework/agents/queen/soul_reflection.py::run_due

import (
	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// startServeBackgroundServices starts the browser-bridge supervisor and the
// daily soul-reflection scheduler for one `hive serve` process. The returned
// stop function is safe to call when nothing started.
//
// The manager is what makes bridge notify frames land in agent sessions: the
// supervisor forwards each side-panel notify to DeliverBrowserNotify, which
// looks up the queen (session id) or worker (colony worker id) the profile
// names. Without a manager (nil) notify stays log-only.
func startServeBackgroundServices(r *Runner, home hivehome.Home, manager *hiveserver.SessionManager) func() {
	var notify func(profile, text string, extra map[string]any)
	if manager != nil {
		notify = func(profile, text string, extra map[string]any) {
			manager.DeliverBrowserNotify(profile, text, extra)
		}
	}
	stopBridge := startBrowserBridge(browserSupervisorConfig{Home: home, Out: r.Stdout, Notify: notify})
	stopSoul := r.startSoulDailyReflection(home)
	// Installed turn-context packages start once for the serve process, like
	// upstream's app.on_startup hook (framework/server/turn_context.py).
	hiveserver.StartTurnContexts(home)
	return func() {
		stopSoul()
		stopBridge()
		hiveserver.StopTurnContexts(home.Root)
	}
}
