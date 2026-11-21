package social

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-social lane, base 63aacb15). The published surface
// ({names, actions, entries}) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package
// registry (newBatch7Lane7Registry) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// surfaceDigestBaseline is the sha256 of the JSON object
// {"names": [...], "actions": {...}, "entries": [...]} for the five
// social-media tools at 63aacb15.
const surfaceDigestBaseline = "595c1719268b3027c4e2b8e3545e4a5857ed847dc68221ec8388a0819b674cf0"

// Parity-exempt: structural-move guard; upstream has no equivalent hash,
// the Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	wantNames := []string{"youtube", "reddit", "twitter", "twilio", "pushover"}
	if got := registry.Names(); !reflect.DeepEqual(got, wantNames) {
		t.Fatalf("social registry tools = %v, want %v", got, wantNames)
	}
	if got := registry.ActionCount(); got != 36 {
		t.Fatalf("social action count = %d, want 36", got)
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
		t.Fatalf("social catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/entries) must not change in a structural refactor.",
			got, surfaceDigestBaseline)
	}
}
