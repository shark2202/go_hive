package webintel

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-webintel lane, base 63d64352). The published surface
// ({names, actions, entries}) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package registry
// (newBatch7Lane6Registry) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// surfaceDigestBaseline is the sha256 of the JSON object
// {"names": [...], "actions": {...}, "entries": [...]} for the three
// web-intelligence tools at 63d64352.
const surfaceDigestBaseline = "59b37c4c9b11c503510cfce9748f62f4729eb754cc2d458afdd375c1461dc132"

// Parity-exempt: structural-move guard; upstream has no equivalent hash,
// the Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	wantNames := []string{"similarweb", "news", "google_maps"}
	if got := registry.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("webintel registry tools = %v, want %v", got, wantNames)
	}
	if got := registry.ActionCount(); got != 42 {
		t.Fatalf("webintel action count = %d, want 42", got)
	}

	names := registry.Names()
	actions := make(map[string][]string, len(names))
	for _, name := range names {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("missing tool %q", name)
		}
		actions[name] = tool.ActionNames()
	}
	entries := make([]any, 0, len(names))
	for _, tool := range registry.Tools() {
		entries = append(entries, map[string]any{
			"name":        tool.Name,
			"description": tool.Description(),
			"schema":      tool.Schema(),
		})
	}
	payload := map[string]any{"names": names, "actions": actions, "entries": entries}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != surfaceDigestBaseline {
		t.Fatalf("webintel catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/entries) must not change in a structural refactor.",
			got, surfaceDigestBaseline)
	}
}
