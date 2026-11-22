package cli

// The GCU viewport scale: configuration.json's `gcu_viewport_scale` reaches the
// bridge child through HIVE_BROWSER_VIEWPORT_SCALE.
//
// Parity: framework/config.py::get_gcu_viewport_scale

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func writeScaleConfig(t *testing.T, home hivehome.Home, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(home.ConfigurationFile()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// envValue reads key back out of a child environment, last entry winning (which
// is what the supervisor hands the child).
func envValue(env []string, key string) (string, bool) {
	value, found := "", false
	for _, entry := range env {
		if strings.HasPrefix(entry, key+"=") {
			value, found = strings.TrimPrefix(entry, key+"="), true
		}
	}
	return value, found
}

// TestBrowserBridgeViewportScaleResolution pins the resolution order: an
// explicit env value wins when it parses, the configuration supplies it
// otherwise, and the 0.8 default stands in for both. Every path is clamped.
//
// Parity-exempt: 上游 _viewport()（session.py，扩展化浏览器之前的版本）已删除，
// get_gcu_viewport_scale 侧无测试；本测试按当时的解析顺序固定 Go 侧语义。
func TestBrowserBridgeViewportScaleResolution(t *testing.T) {
	explicit := hivehome.Home{Root: t.TempDir()}
	writeScaleConfig(t, explicit, `{"gcu_viewport_scale": 0.65}`)
	plain := hivehome.Home{Root: t.TempDir()}

	envOf := func(pairs map[string]string) func(string) string {
		return func(key string) string { return pairs[key] }
	}

	cases := []struct {
		name string
		home hivehome.Home
		env  map[string]string
		want float64
	}{
		{"config value", explicit, nil, 0.65},
		{"missing key keeps 0.8", plain, nil, 0.8},
		{"env wins", explicit, map[string]string{"HIVE_BROWSER_VIEWPORT_SCALE": "0.3"}, 0.3},
		{"env junk falls back to config", explicit, map[string]string{"HIVE_BROWSER_VIEWPORT_SCALE": "wide"}, 0.65},
		{"env junk on a missing key falls back to 0.8", plain, map[string]string{"HIVE_BROWSER_VIEWPORT_SCALE": " "}, 0.8},
		{"env below range clamps up", explicit, map[string]string{"HIVE_BROWSER_VIEWPORT_SCALE": "0.01"}, 0.1},
		{"env above range clamps down", plain, map[string]string{"HIVE_BROWSER_VIEWPORT_SCALE": "2.0"}, 1.0},
		{"config below range keeps the getter default", plain, nil, 0.8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := browserBridgeViewportScale(tc.home, envOf(tc.env)); got != tc.want {
				t.Fatalf("browserBridgeViewportScale = %v, want %v", got, tc.want)
			}
		})
	}

	// A config value below the range is rejected by the getter (0.8), not
	// clamped to 0.1 — that is get_gcu_viewport_scale's own behavior.
	outOfRange := hivehome.Home{Root: t.TempDir()}
	writeScaleConfig(t, outOfRange, `{"gcu_viewport_scale": 0.01}`)
	if got := browserBridgeViewportScale(outOfRange, envOf(nil)); got != 0.8 {
		t.Fatalf("out-of-range config = %v, want 0.8", got)
	}
}

// TestBrowserBridgeChildEnvCarriesViewportScale pins the delivery: the child's
// environment carries HIVE_HOME and the resolved scale, and a pre-existing
// entry for the same key is replaced instead of shadowed.
//
// Parity: framework/config.py::get_gcu_viewport_scale
func TestBrowserBridgeChildEnvCarriesViewportScale(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeScaleConfig(t, home, `{"gcu_viewport_scale": 0.65}`)

	t.Setenv("HIVE_BROWSER_VIEWPORT_SCALE", "0.9")
	lookupEnv := func(key string) string { return os.Getenv(key) }

	env := browserBridgeChildEnv(home, lookupEnv)
	scale, ok := envValue(env, "HIVE_BROWSER_VIEWPORT_SCALE")
	if !ok {
		t.Fatal("child env has no HIVE_BROWSER_VIEWPORT_SCALE")
	}
	if scale != "0.9" {
		t.Fatalf("scale = %q, want the env override 0.9", scale)
	}
	if count := strings.Count(strings.Join(env, "\n"), "\nHIVE_BROWSER_VIEWPORT_SCALE="); count != 1 {
		t.Fatalf("expected exactly one scale entry, got %d: %v", count, env)
	}
	if got, ok := envValue(env, hivehome.EnvHome); !ok || got != home.Root {
		t.Fatalf("child HIVE_HOME = %q (ok=%v), want %q", got, ok, home.Root)
	}
}
