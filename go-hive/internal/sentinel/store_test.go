package sentinel

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func stringPtr(value string) *string { return &value }

func floatPtr(value float64) *float64 { return &value }

func testRecord(colony, escalation string) *EscalationRecord {
	return &EscalationRecord{
		EscalationID:     escalation,
		ColonyID:         colony,
		SessionID:        "s1",
		CorrelationToken: "tok",
		ParkReason:       "ask_user",
		QuestionText:     "shall I continue?",
		Channel:          "telegram",
		ThreadRef:        map[string]any{"message_id": 5},
		Status:           StatusOpen,
		CreatedAt:        "2026-01-02T03:04:05+00:00",
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_classify_after_seconds_default_and_override
func TestClassifyAfterSecondsDefaultAndOverride(t *testing.T) {
	store := newTestStore(t, nil)
	if got := store.ClassifyAfterSeconds(); got != 300.0 {
		t.Fatalf("default classify_after_seconds = %v, want 300", got)
	}
	store.GlobalConfig = func() map[string]any {
		return map[string]any{"sentinel": map[string]any{"classify_after_seconds": 60}}
	}
	if got := store.ClassifyAfterSeconds(); got != 60.0 {
		t.Fatalf("override classify_after_seconds = %v, want 60", got)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_notifications_enabled_by_default_via_hive
func TestNotificationsEnabledByDefaultViaHive(t *testing.T) {
	store := newTestStore(t, nil)
	config := store.LoadNotificationsConfig("c1")
	if !config.SentinelEnabled {
		t.Fatal("sentinel should be enabled by default")
	}
	if config.Channel != "hive" {
		t.Fatalf("channel = %q, want hive", config.Channel)
	}

	// An existing but empty file is honored as-is; a missing sentinel_enabled
	// key still defaults to enabled.
	writeColonyConfig(t, store, "c2", map[string]any{})
	empty := store.LoadNotificationsConfig("c2")
	if !empty.SentinelEnabled || empty.Channel != "hive" {
		t.Fatalf("empty config = %#v", empty)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_notifications_enabled_reads_per_colony_opt_in
func TestNotificationsEnabledReadsPerColonyOptIn(t *testing.T) {
	store := newTestStore(t, nil)
	writeColonyConfig(t, store, "c1", map[string]any{
		"sentinel_enabled": true,
		"channel":          "telegram",
		"target":           map[string]any{"chat_id": "9"},
		"allowlist":        []any{"42"},
	})
	config := store.LoadNotificationsConfig("c1")
	if !config.SentinelEnabled {
		t.Fatal("sentinel_enabled should be true")
	}
	if config.Channel != "telegram" {
		t.Fatalf("channel = %q, want telegram", config.Channel)
	}
	if got := PyStr(config.Target["chat_id"]); got != "9" {
		t.Fatalf("target chat_id = %q", got)
	}
	if len(config.Allowlist) != 1 || config.Allowlist[0] != "42" {
		t.Fatalf("allowlist = %#v", config.Allowlist)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_update_notifications_config_writes_and_merges
func TestUpdateNotificationsConfigWritesAndMerges(t *testing.T) {
	store := newTestStore(t, nil)
	if err := os.MkdirAll(filepath.Join(store.ColoniesDir, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateNotificationsConfig("c1", NotificationsUpdate{
		SentinelEnabled: true,
		Channel:         stringPtr("telegram"),
		Target:          map[string]any{"chat_id": "9"},
		Allowlist:       []string{"42", "7"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	config := store.LoadNotificationsConfig("c1")
	if !config.SentinelEnabled || config.Channel != "telegram" {
		t.Fatalf("config = %#v", config)
	}
	if got := PyStr(config.Target["chat_id"]); got != "9" {
		t.Fatalf("target chat_id = %q", got)
	}
	if len(config.Allowlist) != 2 || config.Allowlist[0] != "42" || config.Allowlist[1] != "7" {
		t.Fatalf("allowlist = %#v", config.Allowlist)
	}

	// A later thread update must preserve the saved settings.
	if err := store.UpdateNotificationsThread("c1", map[string]any{"message_id": 5}); err != nil {
		t.Fatalf("thread update: %v", err)
	}
	config = store.LoadNotificationsConfig("c1")
	if got := PyStr(config.Thread["message_id"]); got != "5" {
		t.Fatalf("thread = %#v", config.Thread)
	}
	if !config.SentinelEnabled || config.Channel != "telegram" {
		t.Fatalf("settings not preserved: %#v", config)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_per_colony_classify_after_seconds_round_trip
func TestPerColonyClassifyAfterSecondsRoundTrip(t *testing.T) {
	store := newTestStore(t, nil)
	if err := os.MkdirAll(filepath.Join(store.ColoniesDir, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	update := func(budget *float64) {
		t.Helper()
		if err := store.UpdateNotificationsConfig("c1", NotificationsUpdate{
			SentinelEnabled:      true,
			Channel:              stringPtr("slack"),
			Target:               map[string]any{"channel": "C1"},
			Allowlist:            []string{"U1"},
			ClassifyAfterSeconds: budget,
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
	}

	update(nil)
	if got := store.LoadNotificationsConfig("c1").ClassifyAfterSeconds; got != nil {
		t.Fatalf("unset budget = %v, want nil", *got)
	}
	update(floatPtr(300.0))
	if got := store.LoadNotificationsConfig("c1").ClassifyAfterSeconds; got == nil || *got != 300.0 {
		t.Fatalf("set budget = %v", got)
	}
	update(nil)
	if got := store.LoadNotificationsConfig("c1").ClassifyAfterSeconds; got != nil {
		t.Fatalf("cleared budget = %v, want nil", *got)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_update_notifications_config_missing_colony
func TestUpdateNotificationsConfigMissingColony(t *testing.T) {
	store := newTestStore(t, nil)
	err := store.UpdateNotificationsConfig("ghost", NotificationsUpdate{
		SentinelEnabled: true,
		Channel:         stringPtr("telegram"),
		Target:          map[string]any{},
		Allowlist:       []string{},
	})
	if !errors.Is(err, ErrColonyNotFound) {
		t.Fatalf("error = %v, want ErrColonyNotFound", err)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_update_thread_preserves_settings
func TestUpdateThreadPreservesSettings(t *testing.T) {
	store := newTestStore(t, nil)
	writeColonyConfig(t, store, "c1", map[string]any{
		"sentinel_enabled": true,
		"channel":          "slack",
		"target":           map[string]any{"channel": "C1"},
		"allowlist":        []any{"U1"},
	})
	if err := store.UpdateNotificationsThread("c1", map[string]any{"ts": "123.456"}); err != nil {
		t.Fatalf("thread update: %v", err)
	}
	config := store.LoadNotificationsConfig("c1")
	if got := PyStr(config.Thread["ts"]); got != "123.456" {
		t.Fatalf("thread ts = %q", got)
	}
	if !config.SentinelEnabled || config.Channel != "slack" || len(config.Allowlist) != 1 || config.Allowlist[0] != "U1" {
		t.Fatalf("settings not preserved: %#v", config)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_write_load_round_trip
func TestWriteLoadRoundTrip(t *testing.T) {
	store := newTestStore(t, nil)
	record := testRecord("c1", "esc_1")
	if err := store.WriteEscalation(record); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, ok := store.LoadEscalation("c1", "esc_1")
	if !ok || loaded == nil {
		t.Fatal("record should load")
	}
	if loaded.EscalationID != "esc_1" || loaded.QuestionText != "shall I continue?" {
		t.Fatalf("loaded = %#v", loaded)
	}
	if got := PyStr(loaded.ThreadRef["message_id"]); got != "5" {
		t.Fatalf("thread_ref = %#v", loaded.ThreadRef)
	}
	if loaded.Status != StatusOpen {
		t.Fatalf("status = %q", loaded.Status)
	}
}

// Parity: core/framework/sentinel/store.py::EscalationRecord
func TestEscalationRecordPersistsExactJSONFields(t *testing.T) {
	store := newTestStore(t, nil)
	if err := store.WriteEscalation(testRecord("c1", "esc_1")); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.EscalationsDir("c1"), "esc_1.json"))
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	expected := []string{
		"escalation_id", "colony_id", "session_id", "correlation_token",
		"park_reason", "question_text", "channel", "thread_ref", "status",
		"created_at", "resolved_at", "resolved_by",
	}
	for _, key := range expected {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("persisted record missing %q: %s", key, raw)
		}
	}
	if len(decoded) != len(expected) {
		t.Fatalf("persisted record has extra fields: %s", raw)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_list_open_only_returns_open
func TestListOpenOnlyReturnsOpen(t *testing.T) {
	store := newTestStore(t, nil)
	if err := store.WriteEscalation(testRecord("c1", "esc_1")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteEscalation(testRecord("c1", "esc_2")); err != nil {
		t.Fatal(err)
	}
	if !store.ResolveEscalation("c1", "esc_2", "") {
		t.Fatal("resolve esc_2")
	}
	open := store.ListOpen("c1")
	if len(open) != 1 || open[0].EscalationID != "esc_1" {
		t.Fatalf("open ids = %#v", open)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_list_all_open_across_colonies
func TestListAllOpenAcrossColonies(t *testing.T) {
	store := newTestStore(t, nil)
	if err := store.WriteEscalation(testRecord("c1", "esc_1")); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteEscalation(testRecord("c2", "esc_2")); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, record := range store.ListAllOpen() {
		ids[record.EscalationID] = true
	}
	if len(ids) != 2 || !ids["esc_1"] || !ids["esc_2"] {
		t.Fatalf("ids = %#v", ids)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_resolve_is_idempotent
func TestResolveIsIdempotent(t *testing.T) {
	store := newTestStore(t, nil)
	if err := store.WriteEscalation(testRecord("c1", "esc_1")); err != nil {
		t.Fatal(err)
	}
	if !store.ResolveEscalation("c1", "esc_1", "42") {
		t.Fatal("first resolve should win")
	}
	if store.ResolveEscalation("c1", "esc_1", "") {
		t.Fatal("second resolve should report already done")
	}
	record, ok := store.LoadEscalation("c1", "esc_1")
	if !ok || record.Status != StatusResolved {
		t.Fatalf("record = %#v", record)
	}
	if record.ResolvedBy == nil || *record.ResolvedBy != "42" {
		t.Fatalf("resolved_by = %v", record.ResolvedBy)
	}
}

// Parity: core/framework/sentinel/store.py, core/tests/test_sentinel_store.py::test_resolve_missing_returns_false
func TestResolveMissingReturnsFalse(t *testing.T) {
	store := newTestStore(t, nil)
	if store.ResolveEscalation("c1", "nope", "") {
		t.Fatal("resolving a missing record should return false")
	}
}
