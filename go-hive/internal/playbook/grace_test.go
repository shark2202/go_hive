package playbook

import (
	"os"
	"testing"
)

// Parity-exempt: Go-side table for the ported grace resolver; upstream's
// _fast_result_grace has no test module.

func TestFastGraceSecondsMatchesUpstream(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  float64
	}{
		{"unset default", "", false, 1.5},
		{"empty falls back", "", true, 1.5},
		{"override", "2.5", true, 2.5},
		{"truthy int text", "3", true, 3},
		{"zero disables the wait", "0", true, 0},
		{"negative clamps to zero", "-4", true, 0},
		{"garbage keeps the default", "quickly", true, 1.5},
		{"python underscore form", "1_0", true, 10},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.set {
				t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", testCase.value)
			} else {
				t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", "x")
				if err := os.Unsetenv("HIVE_PLAYBOOK_FAST_GRACE"); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
			}
			if got := FastGraceSeconds(); got != testCase.want {
				t.Fatalf("FastGraceSeconds = %v, want %v", got, testCase.want)
			}
		})
	}
}
