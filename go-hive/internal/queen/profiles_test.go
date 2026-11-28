package queen

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_all_defaults_load_from_yaml
func TestAllDefaultsLoadFromYAML(t *testing.T) {
	profiles := DefaultQueens()
	if len(profiles) != 13 {
		t.Fatalf("DefaultQueens() = %d profiles, want 13", len(profiles))
	}
	for id, profile := range profiles {
		if name, _ := profile["name"].(string); name == "" {
			t.Errorf("%s: name is empty", id)
		}
		if title, _ := profile["title"].(string); title == "" {
			t.Errorf("%s: title is empty", id)
		}
		if _, ok := profile["default_tool_categories"].([]any); !ok {
			t.Errorf("%s: default_tool_categories is not a list (%T)", id, profile["default_tool_categories"])
		}
		if _, ok := profile["default_preset_skills"].([]any); !ok {
			t.Errorf("%s: default_preset_skills is not a list (%T)", id, profile["default_preset_skills"])
		}
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_no_queen_carries_preset_skill_defaults
func TestNoQueenCarriesPresetSkillDefaults(t *testing.T) {
	for id := range DefaultQueens() {
		if skills := DefaultSkillsFor(id); len(skills) != 0 {
			t.Errorf("%s: default_preset_skills = %v, want empty", id, skills)
		}
	}
	if skills := DefaultSkillsFor("queen_outbound"); len(skills) != 0 {
		t.Errorf("queen_outbound: %v, want empty", skills)
	}
	if skills := DefaultSkillsFor("queen_technology"); len(skills) != 0 {
		t.Errorf("queen_technology: %v, want empty", skills)
	}
}

// Parity: framework/agents/queen/queen_profiles.py::default_skills_for
//
// A custom queen's on-disk profile carries its own default_preset_skills list;
// a missing profile or key yields an empty (non-nil) set.
func TestPresetSkillsFromProfileReadsCustomList(t *testing.T) {
	got := PresetSkillsFromProfile(map[string]any{
		"default_preset_skills": []any{"hive.linkedin-automation", "hive.x-automation", ""},
	})
	want := map[string]struct{}{
		"hive.linkedin-automation": {},
		"hive.x-automation":        {},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PresetSkillsFromProfile = %#v, want %#v", got, want)
	}
	if empty := PresetSkillsFromProfile(nil); empty == nil || len(empty) != 0 {
		t.Fatalf("PresetSkillsFromProfile(nil) = %#v, want an empty non-nil set", empty)
	}
}

// Parity-exempt: no upstream test; a queen id from a URL/body must not escape
// the queens root when LoadQueenProfile builds profile.yaml's path.
func TestLoadQueenProfileRejectsTraversalID(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "profile.yaml"), []byte("name: leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	queensDir := filepath.Join(base, "queens")
	if _, err := LoadQueenProfile(queensDir, "../outside"); err == nil {
		t.Fatal("LoadQueenProfile accepted a traversal id")
	}
	for _, id := range []string{"", ".", "..", "a/b", `a\\b`, "a\x00b"} {
		if ValidQueenID(id) {
			t.Fatalf("ValidQueenID(%q) = true, want false", id)
		}
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_default_tools_for_known_and_unknown
func TestDefaultToolsForKnownAndUnknown(t *testing.T) {
	if tools := DefaultToolsFor("queen_outbound"); !containsString(tools, "charts") {
		t.Errorf("queen_outbound tools = %v, want charts", tools)
	}
	if tools := DefaultToolsFor("queen_operations"); containsString(tools, "research") {
		t.Errorf("queen_operations tools = %v, must omit research", tools)
	}
	if tools := DefaultToolsFor("queen_does_not_exist"); tools != nil {
		t.Errorf("unknown queen tools = %v, want nil", tools)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_materialize_writes_persona_only
func TestMaterializeWritesPersonaOnly(t *testing.T) {
	dir := t.TempDir()
	path, ok := materializeDefaultQueen(dir, "queen_outbound")
	if !ok || path == "" {
		t.Fatal("materializeDefaultQueen returned no path")
	}
	if path != filepath.Join(dir, "queen_outbound", "profile.yaml") {
		t.Fatalf("path = %q, want profile.yaml under the temp queens dir", path)
	}
	written := readYAMLMap(t, path)
	if _, exists := written["default_tool_categories"]; exists {
		t.Error("profile.yaml carries default_tool_categories")
	}
	if _, exists := written["default_preset_skills"]; exists {
		t.Error("profile.yaml carries default_preset_skills")
	}
	want := DefaultQueens()["queen_outbound"]["name"]
	if written["name"] != want {
		t.Errorf("name = %v, want %v", written["name"], want)
	}
}

// Parity: core/framework/agents/queen/queen_profiles.py::_atomic_write_text
//
// The durable guarantee is the shared atomicfs helper; this asserts the
// materialized result is complete and readable, not a concurrency race.
func TestMaterializeDefaultQueenWritesCompleteProfile(t *testing.T) {
	dir := t.TempDir()
	path, ok := materializeDefaultQueen(dir, "queen_outbound")
	if !ok {
		t.Fatal("materializeDefaultQueen(queen_outbound) = false, want true")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Fatalf("%s is empty", path)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("%s mode = %04o, want 0644", path, mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var written map[string]any
	if err := yaml.Unmarshal(data, &written); err != nil {
		t.Fatalf("parse %s as YAML: %v", path, err)
	}
	if want := DefaultProfilePersona("queen_outbound"); !reflect.DeepEqual(written, want) {
		t.Errorf("materialized profile\n got: %#v\nwant: %#v", written, want)
	}
	for _, key := range []string{"default_tool_categories", "default_preset_skills"} {
		if _, exists := written[key]; exists {
			t.Errorf("profile.yaml carries %s", key)
		}
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_self_heal_equality_holds_for_untouched_default
func TestSelfHealEqualityHoldsForUntouchedDefault(t *testing.T) {
	dir := t.TempDir()
	path, ok := materializeDefaultQueen(dir, "queen_sales")
	if !ok || path == "" {
		t.Fatal("materializeDefaultQueen returned no path")
	}
	onDisk := readYAMLMap(t, path)
	want := DefaultProfilePersona("queen_sales")
	if !reflect.DeepEqual(onDisk, want) {
		t.Errorf("materialized profile differs from DefaultProfilePersona\n got: %#v\nwant: %#v", onDisk, want)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_default_profile_persona_unknown_is_none
func TestDefaultProfilePersonaUnknownIsNone(t *testing.T) {
	if profile := DefaultProfilePersona("queen_nope"); profile != nil {
		t.Errorf("DefaultProfilePersona(unknown) = %#v, want nil", profile)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_a_retired_title_is_replaced_by_the_shipped_one
func TestRetiredTitleReplacedByShippedOne(t *testing.T) {
	if got := CurrentTitle("queen_sales", "Head of Sales"); got != "Head of RevOps" {
		t.Errorf("CurrentTitle(queen_sales, Head of Sales) = %v, want Head of RevOps", got)
	}
	if shipped := DefaultQueens()["queen_sales"]["title"]; shipped != "Head of RevOps" {
		t.Errorf("queen_sales shipped title = %v, want Head of RevOps", shipped)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_a_title_the_user_chose_is_never_touched
func TestUserChosenTitleNeverTouched(t *testing.T) {
	if got := CurrentTitle("queen_sales", "VP Revenue"); got != "VP Revenue" {
		t.Errorf("CurrentTitle(queen_sales, VP Revenue) = %v, want VP Revenue", got)
	}
	if got := CurrentTitle("queen_sales", ""); got != "" {
		t.Errorf("CurrentTitle(queen_sales, \"\") = %v, want empty", got)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_queens_with_no_retired_titles_pass_through
func TestQueensWithNoRetiredTitlesPassThrough(t *testing.T) {
	if got := CurrentTitle("queen_growth", "Head of Growth"); got != "Head of Growth" {
		t.Errorf("CurrentTitle(queen_growth, Head of Growth) = %v", got)
	}
	if got := CurrentTitle("queen_growth", "anything at all"); got != "anything at all" {
		t.Errorf("CurrentTitle(queen_growth, anything at all) = %v", got)
	}
}

// Parity: framework/agents/queen/tests/test_queen_profiles_defaults.py::test_every_retired_title_names_a_real_queen_with_a_current_one
func TestEveryRetiredTitleNamesRealQueenWithCurrentOne(t *testing.T) {
	for queenID, retired := range supersededTitles {
		if !IsDefaultQueen(queenID) {
			t.Errorf("%s: superseded title references an unknown queen", queenID)
			continue
		}
		current, _ := DefaultQueens()[queenID]["title"].(string)
		if current == "" {
			t.Errorf("%s: shipped title is empty", queenID)
			continue
		}
		if retired[current] {
			t.Errorf("%s: current title %q is also listed as retired", queenID, current)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}
