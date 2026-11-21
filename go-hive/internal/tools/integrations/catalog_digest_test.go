package integrations

// Tool-catalog digest: the refactor lanes may move files, split packages and
// rename identifiers, but the model-facing tool surface (name, description,
// JSON schema) must stay byte-identical. This freezes the digest of every tool
// New() publishes so a structural change that alters the surface fails here.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// catalogDigest hashes the ordered (name, description, schema) triples of the
// given tools. Byte-for-byte reproducible: json.Marshal sorts map keys.
func catalogDigest(tools []*Tool) string {
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
		panic(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// catalogDigestBaseline is the digest computed at main 98120156, before the
// integrationkit extraction and the excel subpackage pilot. It must not change
// without an explicit parity review.
const catalogDigestBaseline = "0b3e3852dca6eb9544887810b4992e2798d15598c2d7b84552ab5d686423c1dc"

// Parity-exempt: structural-refactor guard for the Go tool catalog; upstream
// has no equivalent hash and the Python surface is covered by the tool
// contract freeze.
func TestBuiltinCatalogDigest(t *testing.T) {
	registry := New()
	if len(registry.Tools()) == 0 {
		t.Fatal("New() published no tools")
	}
	if got := catalogDigest(registry.Tools()); got != catalogDigestBaseline {
		t.Fatalf("tool catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
