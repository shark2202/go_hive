package server

// Turn-context parity: the body a started package contributes to the reminder
// ahead of a queen's turn. The Go build has no linked `hive.turn_context`
// factories (entry-point discovery is compile-time here), so this test
// registers one and drives the real runtime path the serve boot uses:
//
//	configuration.json enables turn_context.probe -> StartTurnContexts ->
//	runtime.ForSession / Context -> the package's text.
//
// The frozen golden is the Python reference's context_for output for the same
// package body — see go_hive/scripts/prompt_catalog_parity.py.
//
// Regenerate:
//
//	HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1 go test ./internal/server -run TestTurnContextBodyParityGolden

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const turnContextProbeBody = "Turn-context probe body for the parity probe."

type parityTurnContext struct {
	body string
}

func (p parityTurnContext) Start() error { return nil }

func (p parityTurnContext) Stop() error { return nil }

func (p parityTurnContext) Context(queenID, message string) (string, error) {
	return p.body, nil
}

// Parity-exempt: golden fixtures are captured from the Python runtime by
// scripts/prompt_catalog_parity.py; this test replays the body/wrapper against
// Go rendering rather than naming one upstream function.
func TestTurnContextBodyParityGolden(t *testing.T) {
	home := t.TempDir()
	config := []byte("{\n  \"turn_context\": {\"probe\": {\"enabled\": true}}\n}\n")
	if err := os.WriteFile(filepath.Join(home, "configuration.json"), config, 0o644); err != nil {
		t.Fatalf("write configuration.json: %v", err)
	}

	RegisterTurnContextProvider("probe", func(host TurnContextHost) (TurnContext, error) {
		return parityTurnContext{body: turnContextProbeBody}, nil
	})
	defer UnregisterTurnContextProvider("probe")

	runtime := StartTurnContexts(hivehome.Home{Root: home})
	defer StopTurnContexts(home)
	if runtime == nil || runtime.Empty() {
		t.Fatal("turn-context runtime started nothing for an enabled package")
	}

	body := runtime.Context("queen_growth", "PARITY_PROBE_MESSAGE")
	if body != turnContextProbeBody {
		t.Fatalf("turn context body = %q, want %q", body, turnContextProbeBody)
	}
	if block := wrapTurnContextBlockForTest(body); !strings.Contains(block, turnContextProbeBody) {
		t.Fatalf("reminder block lost the package body: %q", block)
	}

	if captureDir := strings.TrimSpace(os.Getenv("HIVE_PROMPT_CATALOG_CAPTURE_DIR")); captureDir != "" {
		if err := os.MkdirAll(captureDir, 0o755); err != nil {
			t.Fatalf("capture dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(captureDir, "turn-context.txt"), []byte(body), 0o644); err != nil {
			t.Fatalf("capture turn-context: %v", err)
		}
	}

	goldenPath := filepath.Join("testdata", "prompt-parity", "turn-context.body.txt")
	if os.Getenv("HIVE_PROMPT_CATALOG_WRITE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("golden dir: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(body), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (regenerate with HIVE_PROMPT_CATALOG_WRITE_GOLDEN=1)", err)
	}
	if body != string(golden) {
		t.Fatalf("turn-context body diverged from golden:\n  want: %s\n  got:  %s", strconv.Quote(string(golden)), strconv.Quote(body))
	}
}

// wrapTurnContextBlockForTest documents the wrapper the hub applies to the
// body (the byte-level wrapper parity lives in agentloop's reminder golden).
func wrapTurnContextBlockForTest(body string) string {
	return "<system-reminder>\n" + body + "\n</system-reminder>"
}
