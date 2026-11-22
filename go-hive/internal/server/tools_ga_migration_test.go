package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// gaTestWriteSidecar writes a raw tools.json payload (or malformed bytes when
// payload is a string) under an existing queen/colony directory.
func gaTestWriteSidecar(t *testing.T, path string, payload any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir sidecar dir: %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal sidecar: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

func gaTestWriteMalformedSidecar(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir sidecar dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write malformed sidecar: %v", err)
	}
}

// gaTestSorted returns a sorted copy of names, matching the grant helpers'
// sorted output.
func gaTestSorted(names []string) []string {
	out := append([]string{}, names...)
	sort.Strings(out)
	return out
}

// queenTechnologyGrantAdditions is the full role-default grant for a
// queen_technology sidecar saved before every tracked addition. browser_setup
// is absent: the additions table keys it under browser_core, which is
// always-enabled rather than a queen role category.
func queenTechnologyGrantAdditions() []string {
	return gaTestSorted([]string{
		"adjust_sender",
		"attach_file",
		"image_generate",
		"list_senders",
		"list_suppressed",
		"pick_sender",
		"search_timeline",
		"send_campaign",
		"send_from_sender",
		"sender_history",
		"setup_email_sender",
		"suppress_recipient",
	})
}

// Parity: framework/agents/queen/tools_ga_migration.py::migrate_queen_sidecars
func TestMigrateQueenSidecarsGrantsRoleAdditions(t *testing.T) {
	home := queenTestHome(t)
	t.Setenv("HIVE_APP_VERSION", "0.3.0")
	sidecarPath := filepath.Join(home.QueenDir("queen_technology"), "tools.json")
	gaTestWriteSidecar(t, sidecarPath, map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})

	changes := MigrateQueenSidecars(home, nil)
	want := queenTechnologyGrantAdditions()
	if !reflect.DeepEqual(changes["queen_technology"], want) {
		t.Fatalf("changes = %#v, want %#v", changes, want)
	}
	if len(changes) != 1 {
		t.Fatalf("changes covers %d queens, want 1: %#v", len(changes), changes)
	}

	sidecar := readQueenToolsSidecar(t, sidecarPath)
	wantEnabled := append(append([]string{}, want...), "pdf_read")
	sort.Strings(wantEnabled)
	if got := queenTestStringList(t, sidecar["enabled_mcp_tools"]); !reflect.DeepEqual(got, wantEnabled) {
		t.Fatalf("sidecar enabled = %#v, want %#v", got, wantEnabled)
	}
	if sidecar["saved_on_version"] != "0.3.0" {
		t.Fatalf("saved_on_version = %#v, want current app version", sidecar["saved_on_version"])
	}
	if updatedAt, _ := sidecar["updated_at"].(string); updatedAt == "" {
		t.Fatalf("updated_at missing: %#v", sidecar)
	}

	// Idempotent: a second pass finds nothing to add and leaves the file alone.
	before, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if again := MigrateQueenSidecars(home, nil); len(again) != 0 {
		t.Fatalf("second pass granted %#v, want no changes", again)
	}
	after, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("second pass rewrote the sidecar")
	}
}

// Parity: framework/agents/queen/tools_ga_migration.py::_apply_to_sidecar
func TestMigrateQueenSidecarsSkipsUntickAndMalformed(t *testing.T) {
	home := queenTestHome(t)

	// A sidecar saved on a release that already knew every addition keeps a
	// deliberate untick: no grants.
	unticked := filepath.Join(home.QueenDir("queen_technology"), "tools.json")
	gaTestWriteSidecar(t, unticked, map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.7.2",
	})

	// null / [] / non-string items / non-list all carry no allowlist to heal.
	for queenID, payload := range map[string]any{
		"queen_legal":           map[string]any{"enabled_mcp_tools": nil},
		"queen_talent":          map[string]any{"enabled_mcp_tools": []string{}},
		"queen_growth":          map[string]any{"enabled_mcp_tools": []any{"pdf_read", 42.0}},
		"queen_outbound":        map[string]any{"enabled_mcp_tools": "pdf_read"},
		"queen_market_research": map[string]any{"saved_on_version": "0.1.0"},
	} {
		gaTestWriteSidecar(t, filepath.Join(home.QueenDir(queenID), "tools.json"), payload)
	}

	// Custom queen IDs are skipped even with an old explicit allowlist.
	custom := filepath.Join(home.QueenDir("queen_custom_id"), "tools.json")
	gaTestWriteSidecar(t, custom, map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.0.1",
	})

	// A malformed sidecar warns and is skipped without blocking the pass.
	malformed := filepath.Join(home.QueenDir("queen_operations"), "tools.json")
	gaTestWriteMalformedSidecar(t, malformed)

	warnings := []string{}
	changes := MigrateQueenSidecars(home, func(message string) { warnings = append(warnings, message) })
	if len(changes) != 0 {
		t.Fatalf("changes = %#v, want none", changes)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "unreadable sidecar") {
		t.Fatalf("warnings = %#v, want one unreadable-sidecar warning", warnings)
	}
	if raw, err := os.ReadFile(malformed); err != nil || string(raw) != "{not json" {
		t.Fatalf("malformed sidecar was rewritten: %q err=%v", raw, err)
	}
	customSidecar := readQueenToolsSidecar(t, custom)
	if got := queenTestStringList(t, customSidecar["enabled_mcp_tools"]); !reflect.DeepEqual(got, []string{"pdf_read"}) {
		t.Fatalf("custom queen sidecar changed: %#v", customSidecar)
	}
}

// Parity: framework/agents/queen/tools_ga_migration.py::migrate_colony_sidecars
func TestMigrateColonySidecarsUsesMetadataQueen(t *testing.T) {
	home := queenTestHome(t)
	t.Setenv("HIVE_APP_VERSION", "0.3.0")
	if err := os.MkdirAll(home.ColoniesDir(), 0o755); err != nil {
		t.Fatalf("mkdir colonies: %v", err)
	}
	seedToolColony(t, home, "colony_one", map[string]any{"colony_id": "colony_one", "queen_name": "queen_technology"})
	gaTestWriteSidecar(t, filepath.Join(home.ColoniesDir(), "colony_one", "tools.json"), map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})

	// A colony forked from a custom queen is skipped (no role default).
	seedToolColony(t, home, "colony_custom", map[string]any{"colony_id": "colony_custom", "queen_name": "queen_custom_id"})
	gaTestWriteSidecar(t, filepath.Join(home.ColoniesDir(), "colony_custom", "tools.json"), map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})

	// A directory without metadata.json is not a colony.
	gaTestWriteSidecar(t, filepath.Join(home.ColoniesDir(), "colony_no_meta", "tools.json"), map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})

	changes := MigrateColonySidecars(home, nil)
	want := queenTechnologyGrantAdditions()
	if !reflect.DeepEqual(changes["colony_one"], want) {
		t.Fatalf("changes = %#v, want colony_one -> %#v", changes, want)
	}
	if len(changes) != 1 {
		t.Fatalf("changes covers %d colonies, want 1: %#v", len(changes), changes)
	}
	sidecar := readQueenToolsSidecar(t, filepath.Join(home.ColoniesDir(), "colony_one", "tools.json"))
	if sidecar["saved_on_version"] != "0.3.0" {
		t.Fatalf("colony saved_on_version = %#v", sidecar["saved_on_version"])
	}
}

// Parity: framework/agents/queen/tools_ga_migration.py::run_ga_tool_migration
func TestRunGAToolMigrationQueensThenColonies(t *testing.T) {
	home := queenTestHome(t)
	t.Setenv("HIVE_APP_VERSION", "0.3.0")
	if err := os.MkdirAll(home.ColoniesDir(), 0o755); err != nil {
		t.Fatalf("mkdir colonies: %v", err)
	}
	gaTestWriteSidecar(t, filepath.Join(home.QueenDir("queen_technology"), "tools.json"), map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})
	seedToolColony(t, home, "colony_one", map[string]any{"colony_id": "colony_one", "queen_name": "queen_technology"})
	gaTestWriteSidecar(t, filepath.Join(home.ColoniesDir(), "colony_one", "tools.json"), map[string]any{
		"enabled_mcp_tools": []string{"pdf_read"},
		"saved_on_version":  "0.2.18",
	})

	queens, colonies := RunGAToolMigration(home, nil)
	if len(queens["queen_technology"]) == 0 {
		t.Fatalf("queen pass did not grant: %#v", queens)
	}
	if len(colonies["colony_one"]) == 0 {
		t.Fatalf("colony pass did not grant: %#v", colonies)
	}

	// Missing queens/colonies dirs are non-fatal no-ops.
	empty := hivehome.Home{Root: t.TempDir()}
	emptyQueens, emptyColonies := RunGAToolMigration(empty, nil)
	if len(emptyQueens) != 0 || len(emptyColonies) != 0 {
		t.Fatalf("empty home changed: %#v %#v", emptyQueens, emptyColonies)
	}
}
