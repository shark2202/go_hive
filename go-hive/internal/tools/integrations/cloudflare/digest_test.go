package cloudflare

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-cloudflare lane, base 63d64352). The published surface
// (tool name, description, JSON schema) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package registry
// (batch7Lane2NewRegistry) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON array of
// {name, description, schema} for the Cloudflare tool at 63d64352.
const catalogDigestBaseline = "253a24839d0505257a8b2f5cc608558ac92b1e40f011bdee06c1f84ff024d8fa"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"cloudflare"}) {
		t.Fatalf("cloudflare registry tools = %v, want [cloudflare]", got)
	}
	if got := registry.ActionCount(); got != 54 {
		t.Fatalf("cloudflare action count = %d, want 54", got)
	}
	entries := make([]any, 0, 1)
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
		t.Fatalf("cloudflare catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
