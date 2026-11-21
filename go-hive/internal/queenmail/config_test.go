package queenmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeQueenProfile(t *testing.T, homeRoot, queenID, name, title string) {
	t.Helper()
	dir := filepath.Join(homeRoot, "queens", queenID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name: " + name + "\ntitle: " + title + "\n"
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func decodeSection(t *testing.T, raw string) map[string]any {
	t.Helper()
	var hive map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&hive); err != nil {
		t.Fatal(err)
	}
	return hive
}

// Parity-exempt: the section parser has no Python pytest counterpart; its
// defaults are pinned from framework/config.py::get_queen_mail_config.
func TestQueenMailConfigReadsTheSection(t *testing.T) {
	hive := decodeSection(t, `{"queen_mail": {
		"enabled": true,
		"queens": ["queen_ava", "queen_ivy"],
		"daily_limit": 5,
		"max_depth": 3,
		"max_parallel_turns": 4,
		"claude_code": {"queen_claude": {"name": "Claude", "title": "Engineer"}, "queen_empty": {}}
	}}`)
	config := ConfigFromHive(hive)
	if !config.Enabled || config.DailyLimit != 5 || config.MaxDepth != 3 || config.MaxParallelTurns != 4 {
		t.Fatalf("config = %+v", config)
	}
	if len(config.Queens) != 2 || config.Queens[0] != "queen_ava" {
		t.Fatalf("queens = %#v", config.Queens)
	}
	if entry := config.ClaudeCode["queen_claude"]; entry.Name != "Claude" || entry.Title != "Engineer" {
		t.Fatalf("claude = %+v", entry)
	}
	if entry := config.ClaudeCode["queen_empty"]; entry.Name != "queen_empty" || entry.Title != DefaultClaudeTitle {
		t.Fatalf("default claude = %+v", entry)
	}
}

// Parity-exempt: invalid-value fallbacks have no Python pytest counterpart;
// pinned from get_queen_mail_config's `positive` helper and `is True` check.
func TestQueenMailConfigFallsBackOnInvalidValues(t *testing.T) {
	hive := decodeSection(t, `{"queen_mail": {
		"enabled": "yes",
		"queens": "queen_ava",
		"daily_limit": 0,
		"max_depth": -2,
		"max_parallel_turns": 2.5,
		"claude_code": {"": {"name": "Nobody"}, "queen_claude": "not-an-object"}
	}}`)
	config := ConfigFromHive(hive)
	if config.Enabled {
		t.Fatal("enabled must be strictly true")
	}
	if config.Queens != nil {
		t.Fatalf("queens = %#v, want nil", config.Queens)
	}
	if config.DailyLimit != DefaultDailyLimit || config.MaxDepth != DefaultMaxDepth || config.MaxParallelTurns != DefaultMaxParallelTurns {
		t.Fatalf("limits = %+v", config)
	}
	if len(config.ClaudeCode) != 0 {
		t.Fatalf("claude = %#v", config.ClaudeCode)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_a_claude_code_queen_takes_part_whatever_queens_says
func TestAClaudeCodeQueenTakesPartWhateverQueensSays(t *testing.T) {
	hive := decodeSection(t, `{"queen_mail": {
		"enabled": true,
		"queens": ["queen_ava", "queen_ivy"],
		"claude_code": {"queen_claude": {"name": "Claude", "title": "Engineer"}}
	}}`)
	config := ConfigFromHive(hive)
	if !config.Includes("queen_claude") {
		t.Fatal("a configured claude queen must take part")
	}
	if config.Includes("queen_zed") {
		t.Fatal("queen_zed is not in queen_mail.queens")
	}
	if !config.Includes("queen_ava") {
		t.Fatal("queen_ava is listed")
	}
}
