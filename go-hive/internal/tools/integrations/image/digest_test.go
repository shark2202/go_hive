package image

// Catalog digest: this family moved out of internal/tools/integrations/
// (w210/integrations-image lane, base c1e0f2106). The published surface
// ({names, actions, entries}) must be byte-identical to the pre-move catalog;
// the digest below was computed from the former in-package probe
// (newBatch3MediaRegistry → image_generate) before the move and hashes the
// same {names, actions, entries} JSON used for the before/after diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Actions []string `json:"actions"`
	Tool    string   `json:"tool"`
}

// surfaceDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for image_generate at c1e0f2106 (640 bytes,
// 04c8c45d2626893780583619b23ad2a8c66cb5b261ef6532eb123c960bd406c9).
const surfaceDigestBaseline = "04c8c45d2626893780583619b23ad2a8c66cb5b261ef6532eb123c960bd406c9"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"image_generate"}) {
		t.Fatalf("image registry tools = %v, want image_generate", got)
	}
	if got := registry.ActionCount(); got != 1 {
		t.Fatalf("image action count = %d, want 1", got)
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
	if got := len(raw); got != 640 {
		t.Fatalf("probe size = %d bytes, want 640 (the pre-move probe size)", got)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != surfaceDigestBaseline {
		t.Fatalf("image catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/entries) must not change in a structural refactor.",
			got, surfaceDigestBaseline)
	}
}
