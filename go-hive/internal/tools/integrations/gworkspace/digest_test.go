package gworkspace

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-split-p3 lane, base 00d51e22). The published surface (tool
// name, description, JSON schema) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package registry
// (newBatch5Lane3Registry) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON array of
// {name, description, schema} for the four Google Workspace tools at 00d51e22.
const catalogDigestBaseline = "5bb8117a8289ea20dfe94cbf890bee1ff19f6cb83b14396afee1b754b09b68cc"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"google_sheets", "google_docs", "gmail", "calendar"}) {
		t.Fatalf("gworkspace registry tools = %v, want google_sheets/google_docs/gmail/calendar", got)
	}
	if got := registry.ActionCount(); got != 39 {
		t.Fatalf("gworkspace action count = %d, want 39", got)
	}
	entries := make([]any, 0, 4)
	for _, tool := range registry.Tools() {
		entries = append(entries, map[string]any{
			"name":        tool.Name,
			"description": tool.Description(),
			"schema":      tool.Schema(),
		})
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != catalogDigestBaseline {
		t.Fatalf("gworkspace catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
