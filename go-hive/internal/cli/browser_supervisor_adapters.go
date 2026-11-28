package cli

// Thin aliases for the browser supervisor. The lifecycle lives in
// internal/cli/browsersupervisor and the control-plane client in
// internal/cli/browsercli; the composition root keeps these names so serve and
// the tool gate keep their historical call sites.
//
// Parity: tools/src/gcu/bridge_host.py::ensure_bridge_host_running

import (
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/cli/browsercli"
	"github.com/shark2202/go_hive/go-hive/internal/cli/browsersupervisor"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	// browserBridgeSwitchEnv overrides the configuration.json switch.
	// "0"/"off"/"false"/"no" disables the bridge, "1"/"on"/"true"/"yes"
	// forces it on regardless of configuration.json.
	browserBridgeSwitchEnv = "HIVE_BROWSER_BRIDGE"
)

// browserBridgeRequested applies the switch: an explicit env value wins, then
// configuration.json's `gcu_enabled` (upstream always writes it true when the
// browser feature is installed), then the default — on. The bridge is inert
// without an extension connection, so "on by default" only costs a listener.
func browserBridgeRequested(home hivehome.Home, lookupEnv func(string) string) bool {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	switch strings.ToLower(strings.TrimSpace(lookupEnv(browserBridgeSwitchEnv))) {
	case "0", "off", "false", "no", "disable", "disabled":
		return false
	case "1", "on", "true", "yes", "enable", "enabled":
		return true
	}
	if strings.TrimSpace(home.Root) == "" {
		return true
	}
	config, ok := readJSONObject(home.ConfigurationFile())
	if !ok {
		return true
	}
	// `gcu_enabled: false` is the upstream opt-out (quickstart.sh writes true).
	if enabled, present := config["gcu_enabled"].(bool); present {
		return enabled
	}
	return true
}

type browserSupervisorConfig = browsersupervisor.Config

// startBrowserBridge wires the control-plane keepalive the supervisor package
// deliberately does not own: the WebSocket control client belongs to the
// browser CLI surface, while the supervisor only calls back through the seam.
func startBrowserBridge(cfg browserSupervisorConfig) func() {
	lookupEnv := cfg.LookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	if !browserBridgeRequested(cfg.Home, lookupEnv) {
		return func() {}
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = browsercli.ControlAddr(lookupEnv)
	}
	if cfg.StartControlKeepalive == nil {
		cfg.StartControlKeepalive = func(control browsersupervisor.ControlKeepaliveConfig) func() {
			return browsercli.StartControlKeepalive(browsercli.ControlKeepalive{
				ControlAddr: control.ControlAddr,
				OwnerPID:    control.OwnerPID,
				Out:         control.Out,
				OnNotify:    control.OnNotify,
				Wake:        control.Wake,
			})
		}
	}
	return browsersupervisor.Start(cfg)
}
