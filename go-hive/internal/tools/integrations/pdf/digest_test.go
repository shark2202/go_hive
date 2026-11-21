package pdf

// Catalog digest: this family moved out of internal/tools/integrations/
// (int-p2-pdf lane, base 45aa262e). The published surface
// ({names, actions, entries}) must be byte-identical to the pre-move catalog;
// the digest below was computed from the pre-move in-package probe
// (batch3PDFTool + pdfReadAction) before the move and hashes the same
// {names, actions, entries} JSON used for the before/after diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Tool    string   `json:"tool"`
	Actions []string `json:"actions"`
}

// surfaceDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for the pdf_read tool at 45aa262e (614 bytes,
// a973b88d8824c80ef1c16e91e415c86792715928c8bea55be377e988180a020e).
const surfaceDigestBaseline = "a973b88d8824c80ef1c16e91e415c86792715928c8bea55be377e988180a020e"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"pdf_read"}) {
		t.Fatalf("pdf registry tools = %v, want pdf_read", got)
	}
	if got := registry.ActionCount(); got != 1 {
		t.Fatalf("pdf action count = %d, want 1", got)
	}

	names := registry.Names()
	actions := make([]probeActions, 0, len(names))
	entries := make([]any, 0, len(names))
	for _, name := range names {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing from registry", name)
		}
		actions = append(actions, probeActions{Tool: name, Actions: tool.ActionNames()})
		entries = append(entries, map[string]any{
			"name":        tool.Name,
			"description": tool.Description(),
			"schema":      tool.Schema(),
		})
	}
	raw, err := json.Marshal(map[string]any{
		"names":   names,
		"actions": actions,
		"entries": entries,
	})
	if err != nil {
		t.Fatalf("marshal probe: %v", err)
	}
	if got := len(raw); got != 614 {
		t.Fatalf("probe size = %d bytes, want 614 (the pre-move probe size)", got)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != surfaceDigestBaseline {
		t.Fatalf("pdf catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/entries) must not change in a structural refactor.",
			got, surfaceDigestBaseline)
	}
}
