package browserbridge

// The GCU viewport scale as the bridge sees it: HIVE_BROWSER_VIEWPORT_SCALE from
// the runtime, else configuration.json, clamped, reported by GET /status.
//
// Parity: framework/config.py::get_gcu_viewport_scale

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeViewportConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "configuration.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}
	return path
}

// TestViewportScaleResolutionPinsOrder pins the bridge-side resolution: a parsed
// env value is clamped into [0.1, 1.0]; an unset, blank or unparseable value
// falls through to configuration.json's getter, then the 0.8 default.
//
// Parity-exempt: 上游该键的消费点已在扩展化浏览器中移除（getter 为休眠 API，
// session.py 的 HIVE_BROWSER_VIEWPORT_SCALE 覆盖也随之删除）；本测试固定 Go 侧
// 的解析/夹取语义。
func TestViewportScaleResolutionPinsOrder(t *testing.T) {
	empty := writeViewportConfig(t, `{}`)
	configured := writeViewportConfig(t, `{"gcu_viewport_scale": 0.55, "gcu_enabled": true}`)
	rejected := writeViewportConfig(t, `{"gcu_viewport_scale": 0.01}`)

	cases := []struct {
		name   string
		raw    string
		config string
		want   float64
	}{
		{"unset env, no config", "", empty, 0.8},
		{"blank env, no config", "   ", empty, 0.8},
		{"explicit env", "0.65", configured, 0.65},
		{"explicit env overrides config", "0.65", configured, 0.65},
		{"integer one", "1", empty, 1.0},
		{"lower bound", "0.1", empty, 0.1},
		{"env below range clamps up", "0.01", configured, 0.1},
		{"env above range clamps down", "3", empty, 1.0},
		{"env negative clamps up", "-1", empty, 0.1},
		{"junk env falls back to config", "wide", configured, 0.55},
		{"blank env falls back to config", "  ", configured, 0.55},
		{"NaN env falls back to config", "NaN", configured, 0.55},
		{"unset env reads config", "", configured, 0.55},
		{"out-of-range config keeps the getter default", "", rejected, 0.8},
		{"missing config file", "", filepath.Join(t.TempDir(), "absent.json"), 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := viewportScaleFrom(tc.raw, false, tc.config); got != tc.want {
				t.Fatalf("viewportScaleFrom(%q, config=%s) = %v, want %v", tc.raw, tc.config, got, tc.want)
			}
		})
	}
}

// TestViewportScaleFromConfigMirrorsTheGetter pins the configuration.json half:
// numbers only (bools are ints in Python), strings/null/missing keys rejected.
//
// Parity: framework/config.py::get_gcu_viewport_scale
func TestViewportScaleFromConfigMirrorsTheGetter(t *testing.T) {
	cases := []struct {
		name string
		body string
		want float64
	}{
		{"explicit number", `{"gcu_viewport_scale": 0.65}`, 0.65},
		{"integer", `{"gcu_viewport_scale": 1}`, 1.0},
		{"string is rejected", `{"gcu_viewport_scale": "0.5"}`, 0.8},
		{"null is rejected", `{"gcu_viewport_scale": null}`, 0.8},
		{"true is 1.0", `{"gcu_viewport_scale": true}`, 1.0},
		{"false is 0.8", `{"gcu_viewport_scale": false}`, 0.8},
		{"above range", `{"gcu_viewport_scale": 2.0}`, 0.8},
		{"missing key", `{"other": 1}`, 0.8},
		{"broken json", `{"gcu_viewport_scale": `, 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := viewportScaleFromConfig(writeViewportConfig(t, tc.body)); got != tc.want {
				t.Fatalf("viewportScaleFromConfig = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestViewportScaleReadsTheProcessEnv pins the env name the supervisor fills,
// including the configuration.json fallback when the var is absent.
//
// Parity: framework/config.py::get_gcu_viewport_scale
func TestViewportScaleReadsTheProcessEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), []byte(`{"gcu_viewport_scale": 0.45}`), 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}

	t.Setenv(ViewportScaleEnv, "0.35")
	if got := ResolveViewportScale(); got != 0.35 {
		t.Fatalf("ResolveViewportScale = %v, want the env override 0.35", got)
	}
	t.Setenv(ViewportScaleEnv, "")
	if got := ResolveViewportScale(); got != 0.45 {
		t.Fatalf("ResolveViewportScale without the var = %v, want the configuration 0.45", got)
	}
}

// TestStatusReportsViewportScale pins the observable delivery point: the bridge
// status payload carries the resolved scale, and a Config that sets it wins over
// the environment.
//
// Parity-exempt: 上游 Python bridge 的 /status 没有该字段（getter 无消费点）；
// Go 侧把它作为可观测的落地位置，形状是 status 的超集。
func TestStatusReportsViewportScale(t *testing.T) {
	t.Setenv(ViewportScaleEnv, "0.4")
	bridge := New(Config{ControlAddr: "127.0.0.1:0"})
	if got := bridge.cfg.ViewportScale; got != 0.4 {
		t.Fatalf("resolved scale = %v, want 0.4", got)
	}
	if got := bridge.Status(context.Background()).ViewportScale; got != 0.4 {
		t.Fatalf("status scale = %v, want 0.4", got)
	}

	// The payload key the side panel / parity probes read.
	payload, err := json.Marshal(bridge.Status(context.Background()))
	if err != nil {
		t.Fatalf("marshal status: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if got, ok := decoded["viewport_scale"].(float64); !ok || got != 0.4 {
		t.Fatalf("status JSON viewport_scale = %#v, want 0.4", decoded["viewport_scale"])
	}

	explicit := New(Config{ControlAddr: "127.0.0.1:0", ViewportScale: 0.75})
	if got := explicit.Status(context.Background()).ViewportScale; got != 0.75 {
		t.Fatalf("explicit config scale = %v, want 0.75", got)
	}
}
