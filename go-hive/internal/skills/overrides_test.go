package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boolPtr(v bool) *bool { return &v }

// Parity: tests/test_skill_overrides.py::TestSkillOverrideStore::test_load_missing_returns_empty
func TestOverrideLoadMissingEmpty(t *testing.T) {
	store := LoadSkillOverrideStore(filepath.Join(t.TempDir(), "skills_overrides.json"), "queen:x")
	if len(store.Overrides) != 0 {
		t.Fatalf("overrides = %+v", store.Overrides)
	}
	if store.AllDefaultsDisabled {
		t.Fatal("all_defaults_disabled should be false")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillOverrideStore::test_upsert_and_save_roundtrip
func TestOverrideUpsertSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills_overrides.json")
	store := LoadSkillOverrideStore(path, "queen:x")
	created := time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)
	createdBy := "user"
	store.Upsert("foo", &OverrideEntry{
		Enabled:    boolPtr(false),
		Provenance: ProvenanceFramework,
		CreatedAt:  &created,
		CreatedBy:  &createdBy,
	})
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["version"] != float64(1) {
		t.Fatalf("version = %v", raw["version"])
	}
	overrides, _ := raw["overrides"].(map[string]any)
	foo, _ := overrides["foo"].(map[string]any)
	if foo["enabled"] != false {
		t.Fatalf("enabled = %v", foo["enabled"])
	}
	if foo["provenance"] != "framework" {
		t.Fatalf("provenance = %v", foo["provenance"])
	}

	again := LoadSkillOverrideStore(path, "queen:x")
	entry := again.Get("foo")
	if entry == nil {
		t.Fatal("foo missing after reload")
	}
	if entry.Enabled == nil || *entry.Enabled {
		t.Fatalf("enabled = %v", entry.Enabled)
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillOverrideStore::test_tombstone_survives_reload
func TestOverrideTombstoneSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills_overrides.json")
	store := LoadSkillOverrideStore(path, "queen:x")
	store.Upsert("foo", &OverrideEntry{Enabled: boolPtr(true), Provenance: ProvenanceUserUICreated})
	store.Remove("foo", true)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}
	again := LoadSkillOverrideStore(path, "queen:x")
	if _, ok := again.DeletedUISkills["foo"]; !ok {
		t.Fatalf("deleted_ui_skills = %+v", again.DeletedUISkills)
	}
	if again.Get("foo") != nil {
		t.Fatal("entry should be gone")
	}
}

// Parity: tests/test_skill_overrides.py::TestSkillOverrideStore::test_corrupt_file_loads_empty
func TestOverrideCorruptLoadsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills_overrides.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := LoadSkillOverrideStore(path, "queen:x")
	if len(store.Overrides) != 0 {
		t.Fatalf("overrides = %+v", store.Overrides)
	}
}

// Parity-exempt: the is_disabled master-switch precedence has no pytest in
func TestOverrideIsDisabledSemantics(t *testing.T) {
	store := LoadSkillOverrideStore(filepath.Join(t.TempDir(), "o.json"), "queen:x")

	if !store.IsDisabled("missing", false) {
		t.Fatal("missing skill with default disabled should be disabled")
	}
	if store.IsDisabled("missing", true) {
		t.Fatal("missing skill with default enabled should be enabled")
	}

	store.SetEnabled("off", false, nil)
	if !store.IsDisabled("off", true) {
		t.Fatal("explicit disabled should override default enabled")
	}
	store.SetEnabled("on", true, nil)
	if store.IsDisabled("on", false) {
		t.Fatal("explicit enabled should override default disabled")
	}

	store.AllDefaultsDisabled = true
	if !store.IsDisabled("another", true) {
		t.Fatal("master switch should disable default-enabled skill")
	}
	if store.IsDisabled("on", true) {
		t.Fatal("explicit enabled=True should win over master switch")
	}
	if store.EffectiveEnabled("another", true) {
		t.Fatal("effective_enabled should invert is_disabled")
	}
}

// Parity-exempt: tombstone lifting on upsert has no Python pytest; the Python
func TestOverrideUpsertLiftsTombstone(t *testing.T) {
	store := LoadSkillOverrideStore(filepath.Join(t.TempDir(), "o.json"), "queen:x")
	store.Remove("foo", true)
	store.Upsert("foo", &OverrideEntry{Enabled: boolPtr(true)})
	if _, ok := store.DeletedUISkills["foo"]; ok {
		t.Fatal("upsert should lift tombstone")
	}
}

// Parity-exempt: EntryFromDict type coercion has no direct pytest; the Python
func TestOverrideEntryFromDict(t *testing.T) {
	entry := EntryFromDict(map[string]any{
		"enabled":         "yes",
		"provenance":      "nonsense",
		"param_overrides": map[string]any{"depth": float64(3)},
		"created_at":      "2026-04-21T00:00:00+00:00",
	})
	if entry.Enabled != nil {
		t.Fatal("non-bool enabled should be nil")
	}
	if entry.Provenance != ProvenanceFramework {
		t.Fatalf("provenance = %q", entry.Provenance)
	}
	if entry.ParamOverrides["depth"] != float64(3) {
		t.Fatalf("param_overrides = %+v", entry.ParamOverrides)
	}
	if entry.CreatedAt == nil || !entry.CreatedAt.Equal(time.Date(2026, 4, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("created_at = %v", entry.CreatedAt)
	}
}
