package pyenv

import (
	"os"
	"testing"
)

// Parser tables for the Python-semantics env helpers: the accepted and
// rejected input shapes, the truthiness gate and the deny-list bool.

// Parity-exempt: CPython int() itself (whitespace, sign, single underscores);
// upstream wraps it in _env_int/_env_num, and the fallback rule of the latter
// is pinned by core/tests/test_runtime_resources.py::test_env_num_falls_back_on_garbage.
func TestIntMirrorsPythonInt(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  int
	}{
		{"unset", "", false, 20},
		{"empty", "", true, 20},
		{"spaces only", "   ", true, 20},
		{"plain", "7", true, 7},
		{"surrounding spaces", "  7  ", true, 7},
		{"plus sign", "+7", true, 7},
		{"negative", "-7", true, -7},
		{"underscore between digits", "1_0", true, 10},
		{"leading underscore rejected", "_10", true, 20},
		{"trailing underscore rejected", "10_", true, 20},
		{"double underscore rejected", "1__0", true, 20},
		{"float text rejected", "7.0", true, 20},
		{"garbage rejected", "abc", true, 20},
		{"hex rejected", "0x10", true, 20},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.set {
				t.Setenv("HIVE_TEST_INT", testCase.value)
			} else {
				t.Setenv("HIVE_TEST_INT", "20")
				if err := os.Unsetenv("HIVE_TEST_INT"); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
			}
			if got := Int("HIVE_TEST_INT", 20); got != testCase.want {
				t.Fatalf("Int = %d, want %d", got, testCase.want)
			}
		})
	}
}

// Parity-exempt: CPython float() itself (exponents, underscores, whitespace);
// upstream's _env_num wrapper is pinned by
// core/tests/test_runtime_resources.py::test_env_num_falls_back_on_garbage.
func TestFloatMirrorsPythonFloat(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
		want  float64
	}{
		{"unset", "", false, 1.5},
		{"empty", "", true, 1.5},
		{"plain", "2.5", true, 2.5},
		{"spaces", " 2.5 ", true, 2.5},
		{"exponent", "1e3", true, 1000},
		{"underscore", "1_0.5", true, 10.5},
		{"garbage", "fast", true, 1.5},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.set {
				t.Setenv("HIVE_TEST_FLOAT", testCase.value)
			} else {
				t.Setenv("HIVE_TEST_FLOAT", "1.5")
				if err := os.Unsetenv("HIVE_TEST_FLOAT"); err != nil {
					t.Fatalf("unsetenv: %v", err)
				}
			}
			if got := Float("HIVE_TEST_FLOAT", 1.5); got != testCase.want {
				t.Fatalf("Float = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Parity-exempt: mirrors inline upstream idioms rather than a helper —
// `if not os.environ.get(...)` (observability/logging.py, sentinel/manager.py)
// and `.strip().lower() in ("1", "true")` (tracker/llm_debug_logger.py);
// Python tests neither seam directly.
func TestGateHelpers(t *testing.T) {
	t.Setenv("HIVE_TEST_GATE", "")
	if NonEmpty("HIVE_TEST_GATE") {
		t.Fatal("NonEmpty(empty) = true, want false (os.environ.get is falsey)")
	}
	if Is1OrTrue("HIVE_TEST_GATE") {
		t.Fatal("Is1OrTrue(empty) = true")
	}
	for _, value := range []string{"1", " TRUE ", "true"} {
		t.Setenv("HIVE_TEST_GATE", value)
		if !Is1OrTrue("HIVE_TEST_GATE") {
			t.Fatalf("Is1OrTrue(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"0", "yes", "on", "false", "2"} {
		t.Setenv("HIVE_TEST_GATE", value)
		if Is1OrTrue("HIVE_TEST_GATE") {
			t.Fatalf("Is1OrTrue(%q) = true, want false", value)
		}
	}
	t.Setenv("HIVE_TEST_GATE", "0")
	if !NonEmpty("HIVE_TEST_GATE") {
		t.Fatal("NonEmpty(\"0\") = false: upstream's `if not os.getenv()` treats any non-empty string as set")
	}
}

// Parity: framework/config.py::get_adaptive_tool_budget_enabled
func TestDenyListBool(t *testing.T) {
	t.Setenv("HIVE_TEST_BOOL", "off")
	if DenyListBool("HIVE_TEST_BOOL", true) {
		t.Fatal("DenyListBool(off) = true")
	}
	t.Setenv("HIVE_TEST_BOOL", "disabled")
	if !DenyListBool("HIVE_TEST_BOOL", false) {
		t.Fatal("DenyListBool(disabled) = false; only the deny words are false")
	}
	t.Setenv("HIVE_TEST_BOOL", "  ")
	if !DenyListBool("HIVE_TEST_BOOL", true) {
		t.Fatal("DenyListBool(blank) should fall through to the default")
	}
}

// unsetEnv clears name for the test and restores it afterwards.
func unsetEnv(t *testing.T, name string) {
	t.Helper()
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unsetenv %s: %v", name, err)
	}
}

// Parity: core/tests/test_python_env.py::test_only_the_launchers_pythonhome_is_dropped
func TestDropsOnlyTheLaunchersPythonHome(t *testing.T) {
	const home = `C:\Python\cpython-3.11`
	cases := []struct {
		name      string
		setHome   bool
		home      string
		setMarker bool
		marker    string
		dropped   bool
	}{
		{name: "launcher pair dropped", setHome: true, home: home, setMarker: true, marker: home, dropped: true},
		{name: "user set without marker kept", setHome: true, home: home},
		{name: "marker for another value kept", setHome: true, home: home, setMarker: true, marker: `C:\other`},
		{name: "unset is a no-op"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.setHome {
				t.Setenv("PYTHONHOME", testCase.home)
			} else {
				unsetEnv(t, "PYTHONHOME")
			}
			if testCase.setMarker {
				t.Setenv("UV_INTERNAL__PYTHONHOME", testCase.marker)
			} else {
				unsetEnv(t, "UV_INTERNAL__PYTHONHOME")
			}

			if got := DropLauncherPythonHome(); got != testCase.dropped {
				t.Fatalf("DropLauncherPythonHome() = %v, want %v", got, testCase.dropped)
			}
			if testCase.dropped {
				if _, ok := os.LookupEnv("PYTHONHOME"); ok {
					t.Fatal("PYTHONHOME still set after the launcher pair was dropped")
				}
				if _, ok := os.LookupEnv("UV_INTERNAL__PYTHONHOME"); ok {
					t.Fatal("UV_INTERNAL__PYTHONHOME still set after the launcher pair was dropped")
				}
				// A second call has nothing left to drop: the cleanup is idempotent.
				if DropLauncherPythonHome() {
					t.Fatal("second DropLauncherPythonHome() = true, want false")
				}
				return
			}
			if testCase.setHome {
				if got := os.Getenv("PYTHONHOME"); got != testCase.home {
					t.Fatalf("PYTHONHOME = %q, want the user's %q", got, testCase.home)
				}
			}
			if testCase.setMarker {
				if got := os.Getenv("UV_INTERNAL__PYTHONHOME"); got != testCase.marker {
					t.Fatalf("UV_INTERNAL__PYTHONHOME = %q, want %q", got, testCase.marker)
				}
			}
		})
	}
}
