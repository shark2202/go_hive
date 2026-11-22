package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func soulDailyTestHome(t *testing.T, configuration map[string]any) hivehome.Home {
	t.Helper()
	root := testTempDir(t)
	if configuration != nil {
		data, err := json.Marshal(configuration)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "configuration.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hivehome.Home{Root: root}
}

// Parity-exempt: no upstream test; the daily reflection's answer budget follows
// upstream's max(llm.max_tokens, aux default) rule.
func TestSoulDailyMaxTokens(t *testing.T) {
	plain := soulDailyTestHome(t, map[string]any{"llm": map[string]any{"model": "m"}})
	if got := soulDailyMaxTokens(plain); got != 8192 {
		t.Fatalf("default max tokens = %d, want 8192", got)
	}
	raised := soulDailyTestHome(t, map[string]any{"llm": map[string]any{"max_tokens": 12000}})
	if got := soulDailyMaxTokens(raised); got != 12000 {
		t.Fatalf("configured max tokens = %d, want 12000", got)
	}
	lowered := soulDailyTestHome(t, map[string]any{"llm": map[string]any{"max_tokens": 512}})
	if got := soulDailyMaxTokens(lowered); got != 8192 {
		t.Fatalf("small configured budget = %d, want the auxiliary default", got)
	}
}

// Parity-exempt: no upstream test; the serve loop must start the daily
// reflection for a queen-less home without blocking and stop it on shutdown.
func TestStartSoulDailyReflectionStartsAndStops(t *testing.T) {
	home := soulDailyTestHome(t, map[string]any{"soul": map[string]any{"queens": map[string]any{}}})
	runner := &Runner{Stdout: os.Stdout}

	stop := runner.startSoulDailyReflection(home)

	done := make(chan struct{})
	go func() {
		time.Sleep(20 * time.Millisecond)
		stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the daily reflection blocked")
	}
	stop()
}
