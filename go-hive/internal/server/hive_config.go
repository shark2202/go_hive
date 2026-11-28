package server

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: framework/config.py::get_hive_config
func HiveConfig(home hivehome.Home) map[string]any {
	return hiveconf.Load(home)
}

// Parity: framework/config.py::get_stall_check_enabled
func StallCheckEnabled(home hivehome.Home) bool {
	if raw, ok := os.LookupEnv("HIVE_STALL_CHECK"); ok {
		trimmed := strings.TrimSpace(raw)
		if trimmed != "" {
			switch strings.ToLower(trimmed) {
			case "0", "false", "no", "off":
				return false
			}
			return true
		}
	}
	return pyTruthy(HiveConfig(home)["stall_check"])
}

// GcuViewportScaleDefault / GcuViewportScaleMin / GcuViewportScaleMax are the
// bounds get_gcu_viewport_scale enforces.
const (
	GcuViewportScaleDefault = 0.8
	GcuViewportScaleMin     = 0.1
	GcuViewportScaleMax     = 1.0
)

// GcuViewportScale ports get_gcu_viewport_scale: the GCU viewport scale factor
// (0.1-1.0), default 0.8. Only a JSON number inside the range counts. Python's
// isinstance(scale, (int, float)) admits bool — True is 1.0, False is 0.0 — so
// `true` yields 1.0 while `false`, strings ("0.5"), null and out-of-range
// numbers fall back to 0.8.
//
// Parity: framework/config.py::get_gcu_viewport_scale
func GcuViewportScale(home hivehome.Home) float64 {
	scale, ok := pythonNumber(HiveConfig(home)["gcu_viewport_scale"])
	if ok && scale >= GcuViewportScaleMin && scale <= GcuViewportScaleMax {
		return scale
	}
	return GcuViewportScaleDefault
}

// pythonNumber is isinstance(value, (int, float)) over a decoded JSON value:
// json.Number (what the UseNumber decoder produces for every number), floats,
// ints and bools (a bool is an int in Python). Strings are rejected here, as
// upstream rejects "0.5".
func pythonNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}
