package cli

// Runtime-layer lifecycle contract (docs/tool-contract-diff.md §18).
//
// Round 9 rebased the 12 queen-lifecycle references from the Tool Library
// layer (name + description only, empty schema) onto the Python runtime layer:
// the registered Tool objects projected through the request layer
// (framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format). The
// capture lives in docs/e2e-assets/schema-residuals/python-runtime-tools.json
// and was re-frozen into contracts/tool-definitions.json with
// scripts/refresh_lifecycle_runtime_reference.py.
//
// This gate pins the live Go definitions to that frozen reference, byte for
// byte. It covers all 12 names — not just the eight in the P1 parity manifest
// — so colony-phase tools (get_worker_status / inject_message / run_worker /
// stop_worker) cannot silently drift back to local rewrites.
//
// Parity-exempt: the reference is a frozen Python artifact, not a Python unit
// test.

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

const lifecycleRuntimeSource = "python:lifecycle_runtime"

// Parity-exempt: the reference is the frozen runtime-layer capture in
// contracts/tool-definitions.json, not a Python unit test.
func TestQueenLifecycleRuntimeSchemasMatchFrozenContract(t *testing.T) {
	raw, err := os.ReadFile(contractPath())
	if err != nil {
		t.Skipf("contract manifest not present (%v)", err)
	}
	var doc struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"input_schema"`
			Source      string         `json:"source"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("contract manifest unreadable: %v", err)
	}

	root := testTempDir(t)
	home := hivehome.Home{Root: root}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	live := map[string]llm.ToolDef{}
	for _, def := range catalogue.Defs() {
		if _, duplicate := live[def.Name]; !duplicate {
			live[def.Name] = def
		}
	}
	for _, def := range hiveserver.QueenLifecycleToolDefs() {
		live[def.Name] = def
	}

	checked := 0
	for _, tool := range doc.Tools {
		if tool.Source != lifecycleRuntimeSource {
			continue
		}
		checked++
		def, ok := live[tool.Name]
		if !ok {
			t.Errorf("%s: missing from the live session surface", tool.Name)
			continue
		}
		if def.Description != tool.Description {
			t.Errorf("%s: description is not the Python runtime text (got %d bytes, want %d)",
				tool.Name, len(def.Description), len(tool.Description))
		}
		if !flatJSONEqual(def.Parameters, tool.InputSchema) {
			t.Errorf("%s: schema is not the Python runtime schema\ngot:  %#v\nwant: %#v",
				tool.Name, def.Parameters, tool.InputSchema)
		}
	}
	if checked != 12 {
		t.Fatalf("checked %d runtime-layer lifecycle tools, want 12", checked)
	}
}
