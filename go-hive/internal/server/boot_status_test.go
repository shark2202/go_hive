package server

import (
	"testing"
)

func resetBootStatus(t *testing.T) {
	t.Helper()
	reset := func() {
		bootMu.Lock()
		defer bootMu.Unlock()
		bootState = BootState{
			Ready:     false,
			Stage:     "Starting runtime",
			Detail:    "",
			UpdatedAt: unixSeconds(),
			History:   []BootStageEntry{},
		}
	}
	reset()
	t.Cleanup(reset)
}

// Parity-exempt: no upstream test exercises boot_status. Pins the initial
func TestBootStatusInitialState(t *testing.T) {
	resetBootStatus(t)

	state := BootStatus()
	if state.Ready {
		t.Fatal("ready = true, want false")
	}
	if state.Stage != "Starting runtime" {
		t.Fatalf("stage = %q, want %q", state.Stage, "Starting runtime")
	}
	if state.Detail != "" {
		t.Fatalf("detail = %q, want empty", state.Detail)
	}
	if len(state.History) != 0 {
		t.Fatalf("history length = %d, want 0", len(state.History))
	}
	if state.UpdatedAt <= 0 {
		t.Fatalf("updated_at = %v, want a positive Unix timestamp", state.UpdatedAt)
	}
}

// Parity-exempt: no upstream test exercises boot_status. Pins report() then
func TestBootStatusReportThenReady(t *testing.T) {
	resetBootStatus(t)

	ReportBootStage("Preparing queen session", "queen_a")
	state := BootStatus()
	if state.Ready {
		t.Fatal("ready = true after report, want false")
	}
	if state.Stage != "Preparing queen session" {
		t.Fatalf("stage = %q, want %q", state.Stage, "Preparing queen session")
	}
	if state.Detail != "queen_a" {
		t.Fatalf("detail = %q, want %q", state.Detail, "queen_a")
	}
	if len(state.History) != 1 {
		t.Fatalf("history length = %d, want 1", len(state.History))
	}
	if state.History[0].Stage != "Preparing queen session" || state.History[0].Detail != "queen_a" {
		t.Fatalf("history[0] = %#v", state.History[0])
	}
	if state.History[0].At != state.UpdatedAt {
		t.Fatalf("history at = %v, updated_at = %v, want equal", state.History[0].At, state.UpdatedAt)
	}

	ReportBootStage("Reading session history")
	state = BootStatus()
	if state.Detail != "" {
		t.Fatalf("detail = %q after report without detail, want empty", state.Detail)
	}

	MarkBootReady("Queen ready")
	state = BootStatus()
	if !state.Ready {
		t.Fatal("ready = false after mark_ready, want true")
	}
	if state.Stage != "Queen ready" {
		t.Fatalf("stage = %q, want %q", state.Stage, "Queen ready")
	}
	if state.Detail != "" {
		t.Fatalf("detail = %q after mark_ready, want empty", state.Detail)
	}
	if len(state.History) != 3 {
		t.Fatalf("history length = %d, want 3", len(state.History))
	}

	ReportBootStage("Loading tools")
	if BootStatus().Ready {
		t.Fatal("ready = true after a new report, want false")
	}
}

// Parity-exempt: no upstream test exercises boot_status. Pins the 32-entry
func TestBootStatusHistoryBounded(t *testing.T) {
	resetBootStatus(t)

	for i := 0; i < 40; i++ {
		ReportBootStage("stage")
	}
	state := BootStatus()
	if len(state.History) != MaxBootHistory {
		t.Fatalf("history length = %d, want %d", len(state.History), MaxBootHistory)
	}
	if state.Stage != "stage" {
		t.Fatalf("stage = %q", state.Stage)
	}

	state.History[0].Stage = "mutated"
	if BootStatus().History[0].Stage == "mutated" {
		t.Fatal("BootStatus returned a slice sharing the live history")
	}
}
