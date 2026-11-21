package agentloop

// Prompt parity: the <system-reminder> wrapper every reminder (including
// turn-context packages and the skills catalog) is delivered in. The frozen
// golden is produced by the Python reference's wrap_reminder with the same
// bodies — see go_hive/scripts/prompt_catalog_parity.py.
//
// Regenerate:
//
//	HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1 go test ./internal/agentloop -run TestReminderBlockParityGolden

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const promptParityGoldenDir = "testdata/prompt-parity"

// Parity-exempt: golden fixtures are captured from the Python side by
// scripts/prompt_catalog_parity.py; this test replays them against Go
// rendering and has no single upstream function to name.
func TestReminderBlockParityGolden(t *testing.T) {
	bodies := []string{
		"Turn context example body for the parity probe.",
		"Searchable tools manifest example.\nbeta-tool",
	}
	block := WrapReminder(bodies)

	if captureDir := strings.TrimSpace(os.Getenv("HIVE_PROMPT_CATALOG_CAPTURE_DIR")); captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o755); err != nil {
			t.Fatalf("capture dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(captureDir, "reminder.block.txt"), []byte(block), 0o644); err != nil {
			t.Fatalf("capture reminder block: %v", err)
		}
	}

	goldenPath := filepath.Join(promptParityGoldenDir, "reminder.block.txt")
	if os.Getenv("HIVE_PROMPT_CATALOG_WRITE_GOLDEN") == "1" {
		if err := os.MkdirAll(promptParityGoldenDir, 0o755); err != nil {
			t.Fatalf("golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(block), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1)", err)
	}
	if block != string(golden) {
		t.Fatalf("reminder block diverged from golden:\n  want: %s\n  got:  %s", strconv.Quote(string(golden)), strconv.Quote(block))
	}
}
