package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
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

// Parity: framework/agents/queen/soul_reflection.py::llm_for
func TestSoulDailyCompleterPrefersTheWorkerSlot(t *testing.T) {
	home := slotTestHome(t, map[string]any{
		"llm":        map[string]any{"provider": "openai", "model": "main-model", "api_key": "main-key"},
		"worker_llm": map[string]any{"provider": "openai", "model": "worker-model", "api_key": "worker-key"},
		"queen_llm": map[string]any{
			"queen_technology": map[string]any{"provider": "anthropic", "model": "queen-model", "api_key": "queen-key"},
		},
	})

	completer, err := soulDailyCompleter(home)("queen_technology", &soulstore.SoulConfig{Model: "worker"})
	if err != nil {
		t.Fatalf("build worker reflection model: %v", err)
	}
	provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
	if !ok || provider.Model != "openai/worker-model" {
		t.Fatalf("worker-configured reflection model = %#v, want the worker_llm slot even when her queen_llm exists", provider)
	}
}

// Parity: framework/agents/queen/soul_reflection.py::llm_for
//
// The worker slot is only for a soul configured with model: worker. With both
// slots configured, a queen-model soul must keep her queen_llm section; the
// daily reflection must not drift onto the worker's model.
func TestSoulDailyCompleterOnlyUsesTheWorkerSlotWhenAsked(t *testing.T) {
	home := slotTestHome(t, map[string]any{
		"llm":        map[string]any{"provider": "openai", "model": "main-model", "api_key": "main-key"},
		"worker_llm": map[string]any{"provider": "openai", "model": "worker-model", "api_key": "worker-key"},
		"queen_llm": map[string]any{
			"queen_technology": map[string]any{"provider": "anthropic", "model": "queen-model", "api_key": "queen-key"},
		},
	})

	for _, model := range []string{"queen", ""} {
		completer, err := soulDailyCompleter(home)("queen_technology", &soulstore.SoulConfig{Model: model})
		if err != nil {
			t.Fatalf("build %q reflection model: %v", model, err)
		}
		provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
		if !ok || provider.Model != "anthropic/queen-model" {
			t.Fatalf("%q reflection model = %#v, want her queen_llm section, not the worker slot", model, provider)
		}
	}
}

// Parity: framework/agents/queen/soul_reflection.py::llm_for
func TestSoulDailyCompleterFallsBackToHerQueenModel(t *testing.T) {
	home := slotTestHome(t, map[string]any{
		"llm": map[string]any{"provider": "openai", "model": "main-model", "api_key": "main-key"},
		"queen_llm": map[string]any{
			"queen_technology": map[string]any{"provider": "anthropic", "model": "queen-model", "api_key": "queen-key"},
		},
	})

	// "worker" with no worker_llm section, and the default "queen", both run
	// on her own queen_llm section — upstream's worker or queen_models.build.
	for _, model := range []string{"worker", "queen"} {
		completer, err := soulDailyCompleter(home)("queen_technology", &soulstore.SoulConfig{Model: model})
		if err != nil {
			t.Fatalf("build %s reflection model: %v", model, err)
		}
		provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
		if !ok || provider.Model != "anthropic/queen-model" {
			t.Fatalf("%s reflection model = %#v, want her queen_llm slot", model, provider)
		}
	}

	// A queen without her own section keeps the main model.
	completer, err := soulDailyCompleter(home)("queen_growth", &soulstore.SoulConfig{Model: "queen"})
	if err != nil {
		t.Fatalf("build main reflection model: %v", err)
	}
	provider, ok := llm.ResolveCompleter(completer).(*llm.Provider)
	if !ok || provider.Model != "openai/main-model" {
		t.Fatalf("queen without a section = %#v, want the main slot", provider)
	}
}

// Parity: framework/agents/queen/soul_reflection.py::scheduler
func TestSoulDailyScheduleMatchesUpstreamTiming(t *testing.T) {
	// scheduler(first_after_s=120.0, every_s=600.0): the first pass waits two
	// minutes and every pass after it ten.
	if soulDailyFirstDelay != 120*time.Second {
		t.Fatalf("first daily-check delay = %s, want 2m", soulDailyFirstDelay)
	}
	if soulDailyInterval != 600*time.Second {
		t.Fatalf("daily-check interval = %s, want 10m", soulDailyInterval)
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
