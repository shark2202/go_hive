package devops

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-devops lane, base a955c3b6). The published surface (tool
// name, description, JSON schema) must be byte-identical to the pre-move
// catalog; the digest below was computed from the former in-package registry
// (newBatch7Lane1Registry) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON array of
// {name, description, schema} for the four DevOps hosting tools at a955c3b6.
const catalogDigestBaseline = "490e4e2d2d3ccb952f053e3106e8bed435b2f7cbfe4c2b74123fb7b4894529c4"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"gitlab", "aws_s3", "vercel", "docker_hub"}) {
		t.Fatalf("devops registry tools = %v, want gitlab/aws_s3/vercel/docker_hub", got)
	}
	if got := registry.ActionCount(); got != 31 {
		t.Fatalf("devops action count = %d, want 31", got)
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
		t.Fatalf("devops catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
