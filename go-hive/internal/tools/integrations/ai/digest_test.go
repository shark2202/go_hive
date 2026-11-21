package ai

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-ai lane, base 63aacb15). The published surface (tool
// name, description, JSON schema) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package registry
// before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON array of
// {name, description, schema} for the five AI-platform tools at 63aacb15.
const catalogDigestBaseline = "9a7b572bc04f7e1368fa327c8326923c241bd0cccdcbff131c419baac3b78571"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(
		got, []string{"pinecone", "wandb", "langfuse", "huggingface", "vision"},
	) {
		t.Fatalf("ai registry tools = %v, want pinecone/wandb/langfuse/huggingface/vision", got)
	}
	if got := registry.ActionCount(); got != 39 {
		t.Fatalf("ai action count = %d, want 39", got)
	}
	entries := make([]any, 0, 5)
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
		t.Fatalf("ai catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
