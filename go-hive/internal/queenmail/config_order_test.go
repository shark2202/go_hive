package queenmail

import (
	"os"
	"slices"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: framework/config.py::get_queen_mail_config (claude_code is an
// ordered dict; correspondents/dashboard render its values in insertion order).
func TestClaudeQueensKeepConfigurationOrder(t *testing.T) {
	raw := []byte(`{"queen_mail": {"enabled": true, "queens": [], "claude_code": {
		"queen_zulu": {"name": "Zulu"},
		"queen_alpha": {"name": "Alpha"},
		"queen_mike": {"name": "Mike"}
	}}}`)

	config := ConfigFromJSON(raw)
	var got []string
	for _, queen := range config.ClaudeQueens() {
		got = append(got, queen.ID)
	}
	want := []string{"queen_zulu", "queen_alpha", "queen_mike"}
	if !slices.Equal(got, want) {
		t.Fatalf("ClaudeQueens() = %v, want configuration order %v", got, want)
	}

	// ConfigFromHive only has the decoded map; it cannot recover insertion
	// order and must still be deterministic.
	hive := decodeSection(t, string(raw))
	fallback := ConfigFromHive(hive)
	got = got[:0]
	for _, queen := range fallback.ClaudeQueens() {
		got = append(got, queen.ID)
	}
	if !slices.Equal(got, []string{"queen_alpha", "queen_mike", "queen_zulu"}) {
		t.Fatalf("map-only order = %v, want sorted ids", got)
	}

	// Correspondents appends the Claude Code queens in the same order.
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mail := New(home, config, nil)
	var correspondents []string
	for _, entry := range mail.Correspondents("") {
		correspondents = append(correspondents, entry["id"].(string))
	}
	if !slices.Equal(correspondents, want) {
		t.Fatalf("correspondents = %v, want configuration order %v", correspondents, want)
	}
}

// LoadConfig is the production reader: it must keep source order from
// configuration.json and apply the same defaults as ConfigFromHive.
// Parity-exempt: Go-only config-order preservation test.
func TestLoadConfigKeepsClaudeQueenOrder(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"queen_mail": {"enabled": true, "claude_code": {
		"queen_second": {"name": "Second"},
		"queen_first": {"name": "First"}
	}}}`
	if err := os.WriteFile(home.ConfigurationFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadConfig(home)
	if !config.Enabled || config.DailyLimit != DefaultDailyLimit {
		t.Fatalf("config = %+v", config)
	}
	queens := config.ClaudeQueens()
	if len(queens) != 2 || queens[0].ID != "queen_second" || queens[1].ID != "queen_first" {
		t.Fatalf("LoadConfig order = %+v", queens)
	}
}

// TestConfigFromJSONRejectsTrailingData mirrors hiveconf.Load's tolerance.
// Parity-exempt: Go-only trailing-data rejection test for the config reader.
func TestConfigFromJSONRejectsTrailingData(t *testing.T) {
	config := ConfigFromJSON([]byte(`{"queen_mail": {"enabled": true}} trailing`))
	if config.Enabled {
		t.Fatalf("trailing data must read as the default configuration: %+v", config)
	}
}
