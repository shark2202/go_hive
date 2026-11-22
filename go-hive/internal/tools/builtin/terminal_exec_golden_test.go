package builtin

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/toolgolden"
)

// Parity-exempt: Go-side golden harness; replays the captured scenarios in
// docs/tool-output-contract.md against the live tool.
//
// Parity: tools/src/terminal_tools/exec.py::terminal_exec.
//
// TestTerminalExecGoldenAgainstCapture replays the captured terminal_exec
// scenarios against the live builtin and compares the canonical model-visible
// envelope with the committed golden. This is the pin for the envelope shape:
// the body is a JSON document whose key set, null-vs-value semantics, wording
// and is_error marker must match the capture; only the volatile values (pid,
// runtime_ms, job ids) and the disposable workspace path are masked.
func TestTerminalExecGoldenAgainstCapture(t *testing.T) {
	scenarios, err := toolgolden.LoadScenarios("../../../../docs/e2e-assets/tool-output/results.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	raw, err := os.ReadFile("../../../../docs/e2e-assets/tool-output/scenarios.json")
	if err != nil {
		t.Fatalf("load scenarios: %v", err)
	}
	var doc struct {
		Scenarios []struct {
			ID        string          `json:"id"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse scenarios: %v", err)
	}

	cases := map[string]bool{
		"terminal_exec_ok":              true,
		"terminal_exec_nonzero":         true,
		"terminal_exec_timeout":         true,
		"terminal_exec_auto_background": true,
		"terminal_exec_truncate":        true,
		"terminal_exec_warning":         true,
		"terminal_exec_blocked":         true,
		"terminal_exec_bad_wait":        true,
	}

	options := toolgolden.DefaultOptions()
	tb := New(t.TempDir())
	ran := 0
	for _, scenario := range doc.Scenarios {
		if !cases[scenario.ID] {
			continue
		}
		golden, ok := scenarios[scenario.ID]
		if !ok {
			t.Fatalf("scenario %s missing from the capture", scenario.ID)
		}
		var args map[string]any
		if err := json.Unmarshal(scenario.Arguments, &args); err != nil {
			t.Fatalf("scenario %s arguments: %v", scenario.ID, err)
		}
		t.Run(scenario.ID, func(t *testing.T) {
			result, err := tb.Execute(context.Background(), ToolTerminalExec, args)
			if err != nil {
				t.Fatalf("terminal_exec %v: %v", args, err)
			}
			content := result.Content
			if result.IsError {
				content = "ERROR: " + content
			}
			got := toolgolden.Canonicalize(content, options)
			if !got.Equal(golden.Go) {
				t.Fatalf("canonical envelope drifted from the capture.\n got: %#v\nwant: %#v", got, golden.Go)
			}
		})
		ran++
	}
	if ran != len(cases) {
		t.Fatalf("replayed %d/%d terminal_exec scenarios", ran, len(cases))
	}
}
