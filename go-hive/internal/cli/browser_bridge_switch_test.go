package cli

// The bridge enablement switch gates both the serve supervisor and the browser
// tool catalogue, so it stays in the composition root.

import (
	"os"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// TestBrowserBridgeRequestedSwitch pins the switch precedence: env > gcu_enabled
// > default on.
//
// Parity-exempt: 上游没有开关单测；gcu_enabled 由 quickstart.sh 直接写 configuration.json。
func TestBrowserBridgeRequestedSwitch(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeConfig := func(body string) {
		if err := os.WriteFile(home.ConfigurationFile(), []byte(body), 0o644); err != nil {
			t.Fatalf("write configuration.json: %v", err)
		}
	}

	cases := []struct {
		name string
		body string
		env  string
		want bool
	}{
		{name: "default on without a config file", want: true},
		{name: "gcu_enabled true", body: `{"gcu_enabled": true}`, want: true},
		{name: "gcu_enabled false opts out", body: `{"gcu_enabled": false}`, want: false},
		{name: "env forces on over config", body: `{"gcu_enabled": false}`, env: "1", want: true},
		{name: "env forces off over config", body: `{"gcu_enabled": true}`, env: "0", want: false},
		{name: "env off spelled out", body: `{"gcu_enabled": true}`, env: "off", want: false},
		{name: "garbage env falls through to config", body: `{"gcu_enabled": false}`, env: "maybe", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.body == "" {
				_ = os.Remove(home.ConfigurationFile())
			} else {
				writeConfig(testCase.body)
			}
			lookup := func(key string) string {
				if key == browserBridgeSwitchEnv {
					return testCase.env
				}
				return ""
			}
			if got := browserBridgeRequested(home, lookup); got != testCase.want {
				t.Fatalf("browserBridgeRequested = %v, want %v (env=%q body=%q)", got, testCase.want, testCase.env, testCase.body)
			}
		})
	}
}
