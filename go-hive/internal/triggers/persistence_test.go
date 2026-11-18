package triggers

import (
	"os"
	"path/filepath"
	"testing"
)

type fakeSession struct {
	workerPath string
}

func (f fakeSession) WorkerPath() string { return f.workerPath }

// Parity: tests/test_trigger_persistence.py::test_trigger_definition_defaults_last_and_next_to_none
func TestTriggerDefinitionDefaultsLastAndNextToNone(t *testing.T) {
	tdef := TriggerDefinition{
		ID:            "t1",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "*/5 * * * *"},
	}
	if tdef.LastFiredAt != nil {
		t.Fatalf("last_fired_at = %v, want nil", *tdef.LastFiredAt)
	}
	if tdef.NextDueAt != nil {
		t.Fatalf("next_due_at = %v, want nil", *tdef.NextDueAt)
	}
}

// Parity: tests/test_trigger_persistence.py::test_save_trigger_round_trips_last_and_next
func TestSaveTriggerRoundTripsLastAndNext(t *testing.T) {
	agentPath := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	session := fakeSession{workerPath: agentPath}

	last := "2026-05-22T16:00:00Z"
	next := "2026-05-25T16:00:00Z"
	tdef := &TriggerDefinition{
		ID:            "daily_outreach",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"cron": "0 9 * * 1-5"},
		Description:   "Daily LinkedIn outreach",
		Task:          "Send today's 10 invites.",
		Enabled:       true,
		LastFiredAt:   &last,
		NextDueAt:     &next,
	}

	if err := SaveTriggerToAgent(session, "daily_outreach", tdef); err != nil {
		t.Fatalf("SaveTriggerToAgent: %v", err)
	}

	raw := ReadAgentTriggersJSON(agentPath)
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}
	entry := raw[0]
	if entry["last_fired_at"] != "2026-05-22T16:00:00Z" {
		t.Fatalf("last_fired_at = %v", entry["last_fired_at"])
	}
	if entry["next_due_at"] != "2026-05-25T16:00:00Z" {
		t.Fatalf("next_due_at = %v", entry["next_due_at"])
	}
	if entry["enabled"] != true {
		t.Fatalf("enabled = %v, want true", entry["enabled"])
	}
	if entry["task"] != "Send today's 10 invites." {
		t.Fatalf("task = %v", entry["task"])
	}
}

// Parity: tests/test_trigger_persistence.py::test_save_trigger_writes_none_for_unset_timestamps
func TestSaveTriggerWritesNoneForUnsetTimestamps(t *testing.T) {
	agentPath := filepath.Join(t.TempDir(), "agent")
	if err := os.MkdirAll(agentPath, 0o755); err != nil {
		t.Fatal(err)
	}
	session := fakeSession{workerPath: agentPath}

	tdef := &TriggerDefinition{
		ID:            "fresh",
		TriggerType:   "timer",
		TriggerConfig: map[string]any{"interval_minutes": 5},
	}
	if err := SaveTriggerToAgent(session, "fresh", tdef); err != nil {
		t.Fatalf("SaveTriggerToAgent: %v", err)
	}

	raw := ReadAgentTriggersJSON(agentPath)
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}
	if raw[0]["last_fired_at"] != nil {
		t.Fatalf("last_fired_at = %v, want nil", raw[0]["last_fired_at"])
	}
	if raw[0]["next_due_at"] != nil {
		t.Fatalf("next_due_at = %v, want nil", raw[0]["next_due_at"])
	}
}
