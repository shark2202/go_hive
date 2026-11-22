package cli

// sentinel_setup wiring: the flat-surface name executes through the Sentinel
// engine (no not_implemented envelope), and the Go tool definition is
// byte-identical to the frozen Python contract the flat surface advertises.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::handle
// Parity: framework/agent_loop/internals/sentinel_tool.py::build_sentinel_setup_tool

import (
	"context"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
)

// TestSentinelSetupToolDefMatchesFrozenSurface pins the definition chain: the
// Go tool def equals the embedded flat-surface manifest entry, and that entry
// equals the frozen contract file
// (contracts/tool-definitions.json ← gen_tool_surface_flat.py). Description is
// compared byte-for-byte; the schema by JSON value.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::build_sentinel_setup_tool
func TestSentinelSetupToolDefMatchesFrozenSurface(t *testing.T) {
	def := sentinel.SentinelSetupToolDef()
	if def.Name != "sentinel_setup" {
		t.Fatalf("tool name = %q", def.Name)
	}
	manifest := flatSurfaceManifest()
	var entry *flatSurfaceTool
	for index := range manifest.Tools {
		if manifest.Tools[index].Name == def.Name {
			entry = &manifest.Tools[index]
			break
		}
	}
	if entry == nil {
		t.Fatalf("flat surface manifest has no %q entry", def.Name)
	}
	if def.Description != entry.Description {
		t.Fatalf("%s description diverges from the embedded manifest:\n got %q\nwant %q",
			def.Name, def.Description, entry.Description)
	}
	if !flatJSONEqual(def.Parameters, entry.InputSchema) {
		t.Fatalf("%s schema diverges from the embedded manifest:\n got %v\nwant %v",
			def.Name, def.Parameters, entry.InputSchema)
	}

	contract := loadContractDefaultTools(t)
	reference, ok := contract[def.Name]
	if !ok {
		t.Fatalf("contract file has no %q entry", def.Name)
	}
	if def.Description != reference.Description {
		t.Fatalf("%s description diverges from the contract file:\n got %q\nwant %q",
			def.Name, def.Description, reference.Description)
	}
	if !flatJSONEqual(def.Parameters, reference.InputSchema) {
		t.Fatalf("%s schema diverges from the contract file:\n got %v\nwant %v",
			def.Name, def.Parameters, reference.InputSchema)
	}
}

// TestSentinelSetupExecutesThroughFlatSurface checks the execution route: the
// advertised name answers with the upstream usage text and actionable errors
// instead of the not_implemented shim envelope.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::handle
func TestSentinelSetupExecutesThroughFlatSurface(t *testing.T) {
	catalogue := queenFlatCatalogue(t)
	ctx := context.Background()

	content, isError, err := catalogue.Execute(ctx, "sentinel_setup", map[string]any{})
	if err != nil {
		t.Fatalf("sentinel_setup help: %v", err)
	}
	if isError {
		t.Fatalf("sentinel_setup help must not be an error: %s", content)
	}
	if strings.Contains(content, "not_implemented") {
		t.Fatalf("sentinel_setup still routes to the not_implemented shim: %s", content)
	}
	if content != sentinel.RenderSentinelSetupHelp() {
		t.Fatalf("sentinel_setup help diverges from the engine's usage text:\n got %q", content)
	}

	// An invalid token prefix is refused before any network or store access,
	// so this stays hermetic inside the unit suite.
	content, isError, err = catalogue.Execute(ctx, "sentinel_setup", map[string]any{
		"action": "store_token", "provider": "slack", "token": "xoxp-1",
	})
	if err != nil {
		t.Fatalf("sentinel_setup store_token: %v", err)
	}
	if !isError || !strings.Contains(content, "starts with 'xoxb-'") {
		t.Fatalf("store_token refusal = isError=%v content=%s", isError, content)
	}
}
