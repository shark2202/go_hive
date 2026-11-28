package maintenance

// Parity: core/framework/config.py::get_retention_config
//
// The env override loop parses integer fields with the Python builtin int(),
// so " 1_0 " is 10 while "1__0" raises ValueError and leaves the default in
// place. strconv.Atoi accepts neither spelling.

import "testing"

// Parity-exempt: Python int() env parsing (whitespace + underscores) has no dedicated pytest; test_janitor_safety.py::test_retention_config_file_and_env_precedence covers only precedence.
func TestRetentionIntegerEnvMatchesPythonIntSemantics(t *testing.T) {
	home := testHome(t)

	t.Setenv("HIVE_RETENTION_EVENT_LOGS_DAYS", " 1_0 ")
	t.Setenv("HIVE_RETENTION_JUNK_MIN_BYTES", "5_000_000")
	cfg := LoadRetentionConfig(home)
	if cfg.EventLogsDays != 10 {
		t.Fatalf("EventLogsDays = %d, want 10", cfg.EventLogsDays)
	}
	if cfg.JunkMinBytes != 5_000_000 {
		t.Fatalf("JunkMinBytes = %d, want 5000000", cfg.JunkMinBytes)
	}

	flipped := DefaultRetentionConfig()
	t.Setenv("HIVE_RETENTION_EVENT_LOGS_DAYS", "1__0")
	cfg = LoadRetentionConfig(home)
	if cfg.EventLogsDays != flipped.EventLogsDays {
		t.Fatalf("invalid env should keep default %d, got %d", flipped.EventLogsDays, cfg.EventLogsDays)
	}
}
