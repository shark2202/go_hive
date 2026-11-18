package qmail

import (
	"os"
	"path/filepath"
	"testing"
)

// Parity-exempt: durable letter storage and queue replay are Go-side
// mechanisms; the Python server keeps letters in queen sessions instead.
func TestStoreRoundTripsAndQueueSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "queens"))
	letter := Letter{
		ID:          "hop_0123456789ab",
		At:          "2026-11-19T10:00:00+08:00",
		Kind:        KindMessage,
		From:        "queen_ava",
		To:          "queen_ivy",
		FromSession: "session_ava",
		Depth:       1,
		Chars:       5,
		Text:        "Hello",
		FromName:    "Ava",
		State:       StatePending,
	}
	queue := NewQueue(store)
	if err := queue.Enqueue(letter); err != nil {
		t.Fatal(err)
	}

	restarted := NewQueue(store)
	if restarted.Len() != 1 || restarted.IDs()[0] != letter.ID {
		t.Fatalf("restarted queue = %#v", restarted.IDs())
	}
	loaded, ok := store.Load(letter.ID)
	if !ok || loaded.Text != "Hello" || loaded.FromName != "Ava" {
		t.Fatalf("loaded = %#v, ok = %v", loaded, ok)
	}

	letter.State = StateDelivered
	letter.ToSession = "session_thread"
	if err := store.Save(letter); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAudit(letter); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.AuditFile())
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"id":"hop_0123456789ab","at":"2026-11-19T10:00:00+08:00","kind":"message","from":"queen_ava","to":"queen_ivy","from_session":"session_ava","to_session":"session_thread","caused_by":null,"depth":1,"chars":5}`+"\n" {
		t.Fatalf("audit line = %q", string(raw))
	}
	if counts := store.DailyCounts("2026-11-19"); counts["queen_ava"] != 1 {
		t.Fatalf("counts = %#v", counts)
	}
	if err := restarted.Ack(letter.ID); err != nil {
		t.Fatal(err)
	}
	if restarted.Len() != 0 {
		t.Fatalf("queue = %#v", restarted.IDs())
	}
	if NewQueue(store).Len() != 0 {
		t.Fatal("an acknowledged letter must not come back after a restart")
	}
}
