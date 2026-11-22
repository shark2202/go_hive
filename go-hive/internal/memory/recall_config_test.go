package memory

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRecallConfigFile(t *testing.T, home, document string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), []byte(document), 0o644); err != nil {
		t.Fatalf("WriteFile configuration.json: %v", err)
	}
}

func unsetMemoryRecallEnv(t *testing.T) {
	t.Helper()
	previous, ok := os.LookupEnv("HIVE_MEMORY_RECALL")
	if err := os.Unsetenv("HIVE_MEMORY_RECALL"); err != nil {
		t.Fatalf("Unsetenv: %v", err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv("HIVE_MEMORY_RECALL", previous)
		} else {
			_ = os.Unsetenv("HIVE_MEMORY_RECALL")
		}
	})
}

// Parity: core/tests/test_memory_reminders.py::test_recall_mode_defaults_to_index
func TestMemoryRecallModeDefaultsToIndex(t *testing.T) {
	unsetMemoryRecallEnv(t)
	home := t.TempDir()
	writeRecallConfigFile(t, home, `{"memory_recall": {"mode": "someday", "wait_seconds": -1}}`)

	got := GetMemoryRecallConfig(home)
	want := DefaultMemoryRecallConfig()
	if got != want {
		t.Fatalf("config = %+v, want %+v", got, want)
	}
	if want.Mode != "index" || want.WaitSeconds != 10.0 {
		t.Fatalf("defaults = %+v, want index/10", want)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_recall_mode_comes_from_the_config_and_the_env_overrides_it
func TestMemoryRecallModeFromConfigAndEnvOverride(t *testing.T) {
	unsetMemoryRecallEnv(t)
	home := t.TempDir()
	writeRecallConfigFile(t, home, `{"memory_recall": {"mode": "wait", "wait_seconds": 4}}`)

	if got := GetMemoryRecallConfig(home); got.Mode != "wait" || got.WaitSeconds != 4.0 {
		t.Fatalf("config = %+v, want wait/4", got)
	}

	t.Setenv("HIVE_MEMORY_RECALL", "index")
	if got := GetMemoryRecallConfig(home); got.Mode != "index" || got.WaitSeconds != 4.0 {
		t.Fatalf("env override = %+v, want index/4", got)
	}
}

// Parity: core/tests/test_memory_reminders.py::test_recall_mode_comes_from_the_config_and_the_env_overrides_it
// (the probe's 9-row matrix: defaults, invalid values, empty env, bool wait).
func TestMemoryRecallConfigMatrix(t *testing.T) {
	cases := []struct {
		name     string
		document string
		env      *string // nil means unset
		mode     string
		wait     float64
	}{
		{"default when nothing is set", `{}`, nil, "index", 10},
		{"invalid env falls through to config", `{"memory_recall": {"mode": "wait"}}`, ptr("nope"), "wait", 10},
		{"env is stripped and lowercased", `{"memory_recall": {"mode": "background", "wait_seconds": 2.5}}`, ptr(" WAIT "), "wait", 2.5},
		{"empty env falls through to config", `{"memory_recall": {"mode": "background"}}`, ptr(""), "background", 10},
		{"non-object section reads as default", `{"memory_recall": "index"}`, nil, "index", 10},
		{"bool wait_seconds is rejected", `{"memory_recall": {"mode": "wait", "wait_seconds": true}}`, nil, "wait", 10},
		{"zero wait_seconds falls back", `{"memory_recall": {"mode": "wait", "wait_seconds": 0}}`, nil, "wait", 10},
		{"float wait_seconds is accepted", `{"memory_recall": {"mode": "wait", "wait_seconds": 0.25}}`, nil, "wait", 0.25},
		{"string mode in env is invalid", `{"memory_recall": {"mode": "wait"}}`, ptr("  "), "wait", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeRecallConfigFile(t, home, tc.document)
			if tc.env == nil {
				unsetMemoryRecallEnv(t)
			} else {
				t.Setenv("HIVE_MEMORY_RECALL", *tc.env)
			}
			got := GetMemoryRecallConfig(home)
			if got.Mode != tc.mode || got.WaitSeconds != tc.wait {
				t.Fatalf("config = %+v, want %s/%v", got, tc.mode, tc.wait)
			}
		})
	}
}

// Parity: core/framework/config.py::get_memory_recall_env_mode
func TestMemoryRecallEnvModeOnlyAcceptsKnownModes(t *testing.T) {
	t.Setenv("HIVE_MEMORY_RECALL", " Index ")
	if mode, ok := MemoryRecallEnvMode(); !ok || mode != "index" {
		t.Fatalf("env mode = %q ok=%v, want index", mode, ok)
	}
	t.Setenv("HIVE_MEMORY_RECALL", "someday")
	if _, ok := MemoryRecallEnvMode(); ok {
		t.Fatal("an unknown mode must not count")
	}
}

func ptr[T any](value T) *T { return &value }
