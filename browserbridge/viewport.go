package browserbridge

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
)

// The GCU viewport scale: the fraction of the screen the automated browser is
// sized to. Upstream resolves it in tools/src/gcu/browser/session.py::_viewport
// (pre-extension) as an env override first, then configuration.json's
// `gcu_viewport_scale` (framework/config.py::get_gcu_viewport_scale, 0.1-1.0,
// default 0.8), warning on junk, and clamps the resolved value to [0.1, 1.0].
//
// The extension-based browser attaches to the user's own Chrome window, so
// upstream dropped the consumer and the getter is a dormant public API today.
// The Go port keeps the same resolution order and reports the resolved value
// (bridge status + the startup log) without changing capture geometry.
const (
	// ViewportScaleEnv is the override name the runtime's supervisor fills; it
	// is also read directly so a hand-started bridge honours it.
	ViewportScaleEnv = "HIVE_BROWSER_VIEWPORT_SCALE"

	// DefaultViewportScale / ViewportScaleMin / ViewportScaleMax mirror
	// get_gcu_viewport_scale's default and bounds.
	DefaultViewportScale = 0.8
	ViewportScaleMin     = 0.1
	ViewportScaleMax     = 1.0
)

// ResolveViewportScale is the env → configuration.json → default order above.
func ResolveViewportScale() float64 {
	return viewportScaleFrom(os.Getenv(ViewportScaleEnv), true, defaultConfigPath())
}

// viewportScaleFrom is ResolveViewportScale with its inputs injected, so the
// resolution is testable without touching the process environment.
func viewportScaleFrom(raw string, warn bool, configPath string) float64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return viewportScaleFromConfig(configPath)
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || math.IsNaN(parsed) {
		// Upstream warned on junk and fell through to the configuration. NaN is
		// treated as junk too: it cannot be serialized into the status payload,
		// and upstream's clamp only landed on 1.0 by accident of Python's
		// min() ordering.
		if warn {
			log.Printf("Invalid %s=%q, using default %s", ViewportScaleEnv, trimmed, formatScale(DefaultViewportScale))
		}
		return viewportScaleFromConfig(configPath)
	}
	// The env path is clamped, not range-rejected: an env 2.0 becomes 1.0 even
	// though a config 2.0 falls back to the default in the getter.
	return clampViewportScale(parsed)
}

// viewportScaleFromConfig ports get_gcu_viewport_scale: only a JSON number in
// [0.1, 1.0] counts; a string, null, an out-of-range number, a missing key or an
// unreadable file all fall back to the 0.8 default. Python's isinstance(scale,
// (int, float)) admits bool, so `true` yields 1.0 and `false` falls back.
func viewportScaleFromConfig(configPath string) float64 {
	scale, ok := configViewportScale(configPath)
	if ok && scale >= ViewportScaleMin && scale <= ViewportScaleMax {
		return scale
	}
	return DefaultViewportScale
}

// configViewportScale reads configuration.json's `gcu_viewport_scale` as a
// number (bools included, strings rejected).
func configViewportScale(configPath string) (float64, bool) {
	if strings.TrimSpace(configPath) == "" {
		return 0, false
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, false
	}
	value, ok := raw["gcu_viewport_scale"]
	if !ok {
		return 0, false
	}
	switch strings.TrimSpace(string(value)) {
	case "true":
		return 1, true
	case "false":
		return 0, true
	}
	var number float64
	if err := json.Unmarshal(value, &number); err != nil {
		return 0, false
	}
	return number, true
}

func clampViewportScale(scale float64) float64 {
	if scale < ViewportScaleMin {
		return ViewportScaleMin
	}
	if scale > ViewportScaleMax {
		return ViewportScaleMax
	}
	return scale
}

// formatScale renders a scale the way the status payload and logs report it
// ("0.8", "0.65").
func formatScale(scale float64) string {
	return strconv.FormatFloat(scale, 'f', -1, 64)
}
