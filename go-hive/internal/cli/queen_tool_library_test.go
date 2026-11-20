package cli

// Tool Library projection tests: the /api/queen/{id}/tools catalog must expose
// the same harness groups the upstream bare registry serves, with the upstream
// group membership, and the lifecycle list with its 16 entries.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/skills"
)

type fakeToolRegistryCatalog struct {
	catalog map[string][]map[string]any
}

func (f fakeToolRegistryCatalog) GetFullMCPCatalog() map[string][]map[string]any { return f.catalog }

func newTestLibrary(t *testing.T) *QueenToolLibrary {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	return NewQueenToolLibrary(home)
}

func catalogNames(t *testing.T, library *QueenToolLibrary, group string) []string {
	t.Helper()
	entries := library.Catalog("queen_growth", nil)[group]
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name, _ := entry["name"].(string)
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Parity: framework/server/routes_queen_tools.py::_build_tools_snapshot
func TestQueenToolLibraryProjectsHarnessGroups(t *testing.T) {
	library := newTestLibrary(t)
	catalog := library.Catalog("queen_growth", nil)

	groups := make([]string, 0, len(catalog))
	for group := range catalog {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	wantGroups := []string{"chart-tools", "files-tools", "hive_tools", "terminal-tools"}
	if len(groups) != len(wantGroups) {
		t.Fatalf("groups = %v, want %v", groups, wantGroups)
	}
	for i := range wantGroups {
		if groups[i] != wantGroups[i] {
			t.Fatalf("groups = %v, want %v", groups, wantGroups)
		}
	}

	wantHive := []string{
		"attach_file", "csv_append", "csv_info", "csv_read", "csv_sql", "csv_write",
		"get_account_info", "get_current_time", "image_generate", "pdf_read", "web_scrape",
	}
	if got := catalogNames(t, library, "hive_tools"); !equalStrings(got, wantHive) {
		t.Fatalf("hive_tools = %v, want %v", got, wantHive)
	}
	if got := catalogNames(t, library, "files-tools"); !equalStrings(got, []string{"edit_file", "read_file", "search_files", "write_file"}) {
		t.Fatalf("files-tools = %v", got)
	}
	if got := catalogNames(t, library, "chart-tools"); !equalStrings(got, []string{"chart_render"}) {
		t.Fatalf("chart-tools = %v", got)
	}
	if got := catalogNames(t, library, "terminal-tools"); len(got) != 10 {
		t.Fatalf("terminal-tools = %v, want 10 tools", got)
	}
}

// Parity-exempt: csv_sql is not ported (DuckDB); pins the explicitly marked
// unavailable row instead of a silent omission or a fake implementation.
func TestQueenToolLibraryMarksUnportedCSVSQL(t *testing.T) {
	library := newTestLibrary(t)
	var entry map[string]any
	for _, candidate := range library.Catalog("queen_growth", nil)["hive_tools"] {
		if candidate["name"] == "csv_sql" {
			entry = candidate
		}
	}
	if entry == nil {
		t.Fatal("csv_sql row missing from hive_tools")
	}
	description, _ := entry["description"].(string)
	if !contains(description, "NOT AVAILABLE IN THIS BUILD") {
		t.Fatalf("csv_sql description does not mark the tool unavailable: %q", description)
	}
	schema, _ := entry["input_schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if properties == nil || properties["path"] == nil || properties["query"] == nil {
		t.Fatalf("csv_sql schema = %#v", schema)
	}
}

// Parity: framework/server/routes_queen_tools.py::_render_mcp_servers
func TestQueenToolLibrarySchemaKeepsContextParamAndFloatDefaults(t *testing.T) {
	library := newTestLibrary(t)
	byName := map[string]map[string]any{}
	for _, entry := range library.Catalog("queen_growth", nil)["terminal-tools"] {
		name, _ := entry["name"].(string)
		byName[name] = entry
	}
	exec := byName["terminal_exec"]
	if exec == nil {
		t.Fatal("terminal_exec missing")
	}
	schema, _ := exec["input_schema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	if _, present := properties["session_cwd"]; !present {
		t.Fatalf("terminal_exec schema is missing the session_cwd context param: %#v", properties)
	}

	var attach map[string]any
	for _, entry := range library.Catalog("queen_growth", nil)["hive_tools"] {
		if entry["name"] == "attach_file" {
			attach = entry
		}
	}
	if attach == nil {
		t.Fatal("attach_file missing")
	}
	attachSchema, _ := attach["input_schema"].(map[string]any)
	attachProps, _ := attachSchema["properties"].(map[string]any)
	maxBytes, _ := attachProps["max_bytes_mb"].(map[string]any)
	// The upstream contract carries 25.0 (a JSON float); the lexical form must
	// survive so the wire JSON stays 25.0, not 25.
	if _, ok := maxBytes["default"].(json.Number); !ok {
		t.Fatalf("max_bytes_mb default type = %T, want json.Number", maxBytes["default"])
	}
	if got := string(maxBytes["default"].(json.Number)); got != "25.0" {
		t.Fatalf("max_bytes_mb default = %s, want 25.0", got)
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::register_queen_lifecycle_tools
func TestQueenToolLibraryLifecycleHasSixteenEntries(t *testing.T) {
	library := newTestLibrary(t)
	lifecycle := library.Lifecycle("queen_growth", nil)
	names := make([]string, 0, len(lifecycle))
	for _, entry := range lifecycle {
		names = append(names, entry["name"].(string))
	}
	want := []string{
		"get_playbook_status", "get_worker_status", "inject_message", "list_browser_profiles",
		"list_playbook_runs", "list_triggers", "list_worker_questions", "remove_trigger",
		"reply_to_worker", "run_playbook", "run_worker", "set_trigger", "stop_playbook",
		"stop_worker", "update_worker_profile", "write_skill",
	}
	if !equalStrings(names, want) {
		t.Fatalf("lifecycle = %v, want %v", names, want)
	}
	for _, name := range []string{"get_playbook_status", "list_playbook_runs", "stop_playbook"} {
		entry := findLifecycle(lifecycle, name)
		if entry == nil {
			t.Fatalf("%s missing", name)
		}
		description, _ := entry["description"].(string)
		if !contains(description, "Unavailable") {
			t.Fatalf("%s is not marked unavailable: %q", name, description)
		}
	}
	for _, entry := range lifecycle {
		if entry["editable"] != false {
			t.Fatalf("%s editable = %#v", entry["name"], entry["editable"])
		}
	}
	// write_skill's entry is the real registered def, not a placeholder.
	if entry := findLifecycle(lifecycle, skills.WriteSkillToolName); entry == nil ||
		!contains(entry["description"].(string), "skill") {
		t.Fatalf("write_skill entry = %#v", entry)
	}
}

// Parity: framework/server/routes_queen_tools.py::_catalog_from_live_session
func TestQueenToolLibraryPrefersLiveRegistryCatalog(t *testing.T) {
	library := newTestLibrary(t)
	live := map[string][]map[string]any{"live-tools": {{"name": "live_tool"}}}
	session := &hiveserver.Session{}
	session.QueenToolRegistry = fakeToolRegistryCatalog{catalog: live}
	catalog := library.Catalog("queen_growth", session)
	if _, ok := catalog["live-tools"]; !ok {
		t.Fatalf("live catalog not preferred: %#v", catalog)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func findLifecycle(entries []map[string]any, name string) map[string]any {
	for _, entry := range entries {
		if entry["name"] == name {
			return entry
		}
	}
	return nil
}
