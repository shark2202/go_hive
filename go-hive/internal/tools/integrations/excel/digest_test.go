package excel

// Catalog digest: this package moved out of internal/tools/integrations/
// wholesale. The published surface (tool name, description, JSON schema) must
// be byte-identical to the pre-move catalog; the digest below was computed at
// main 98120156 from the former in-package registry.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON array of
// {name, description, schema} for the Excel tool at main 98120156.
const catalogDigestBaseline = "ed2bd007fac1995f837a7e11a4d1224315755ea79e3c40c6f1a9f7aa98aa6ff9"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	tools := registry.Tools()
	if len(tools) != 1 || tools[0].Name != "excel" {
		t.Fatalf("excel registry tools = %v, want the single excel tool", registry.Names())
	}
	entries := make([]any, 0, len(tools))
	for _, tool := range tools {
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
		t.Fatalf("excel catalog digest drifted:\n  got  %s\n  want %s", got, catalogDigestBaseline)
	}
}
