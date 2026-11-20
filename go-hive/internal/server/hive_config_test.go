package server

// Parity: framework/config.py::get_gcu_viewport_scale

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// TestGcuViewportScaleFollowsTheGetter pins the getter's exact contract: a JSON
// number in [0.1, 1.0] is used, everything else falls back to 0.8 — including
// strings, out-of-range numbers and a missing key. `true` is an int in Python,
// so it resolves to 1.0.
//
// Parity-exempt: 上游 get_gcu_viewport_scale 是休眠 getter（无消费点、无测试）；
// 本测试按 getter 源码逐值固定语义，供 Go 侧读取点使用。
func TestGcuViewportScaleFollowsTheGetter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{"missing key", `{}`, 0.8},
		{"absent config file", "", 0.8},
		{"explicit number", `{"gcu_viewport_scale": 0.65}`, 0.65},
		{"integer one", `{"gcu_viewport_scale": 1}`, 1.0},
		{"lower bound", `{"gcu_viewport_scale": 0.1}`, 0.1},
		{"upper bound", `{"gcu_viewport_scale": 1.0}`, 1.0},
		{"below range", `{"gcu_viewport_scale": 0.05}`, 0.8},
		{"above range", `{"gcu_viewport_scale": 2.5}`, 0.8},
		{"zero", `{"gcu_viewport_scale": 0}`, 0.8},
		{"negative", `{"gcu_viewport_scale": -0.5}`, 0.8},
		{"string number", `{"gcu_viewport_scale": "0.5"}`, 0.8},
		{"null", `{"gcu_viewport_scale": null}`, 0.8},
		{"true is 1.0", `{"gcu_viewport_scale": true}`, 1.0},
		{"false is 0.8", `{"gcu_viewport_scale": false}`, 0.8},
		{"array", `{"gcu_viewport_scale": [0.5]}`, 0.8},
		{"object", `{"gcu_viewport_scale": {"v": 0.5}}`, 0.8},
		{"other keys coexist", `{"gcu_enabled": true, "gcu_viewport_scale": 0.9, "turn_context": {}}`, 0.9},
		{"broken json", `{"gcu_viewport_scale": `, 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := hivehome.Home{Root: t.TempDir()}
			if tc.body != "" {
				writeRawConfig(t, home, tc.body)
			}
			if got := GcuViewportScale(home); got != tc.want {
				t.Fatalf("GcuViewportScale = %v, want %v", got, tc.want)
			}
		})
	}
}
