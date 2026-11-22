package collab

// Catalog digests: both lanes moved out of internal/tools/integrations/
// wholesale (int-split-p3 lane, base 00d51e22) into this shared family
// package. The published surface (tool name, description, JSON schema) must be
// byte-identical to the pre-move catalogs; the digests below were computed
// from the former in-package registries (newBatch5Lane1Registry,
// newBatch4Registry and their union) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// Lane baselines at 00d51e22.
const (
	atlassianDigestBaseline = "e2334c4dc188db886dd4905436ce7340a0519cb244ed571d48c97281df931191"
	notionDigestBaseline    = "38f807aa2d93db2b4cc55c10f5b76e7f04b4ff078172343a862c3ef3821a3fce"
	familyDigestBaseline    = "74f76688cb10defaa9aa4f9f5dcd8b9da9dda6e0d64c5c0cf382a8bfea351890"
)

func digestOf(t *testing.T, registry *Registry) string {
	t.Helper()
	entries := make([]any, 0, len(registry.Tools()))
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
	return hex.EncodeToString(sum[:])
}

func assertDigest(t *testing.T, registry *Registry, want string) {
	t.Helper()
	if got := digestOf(t, registry); got != want {
		t.Fatalf("catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (name/description/schema) must not change in a structural refactor.",
			got, want)
	}
}

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	atlassian := NewAtlassianRegistry()
	if got := atlassian.Names(); !reflect.DeepEqual(got, []string{"jira", "confluence", "asana"}) {
		t.Fatalf("atlassian registry tools = %v", got)
	}
	if got := atlassian.ActionCount(); got != 26 {
		t.Fatalf("atlassian action count = %d, want 26", got)
	}
	assertDigest(t, atlassian, atlassianDigestBaseline)

	workspace := NewNotionRegistry()
	if got := workspace.Names(); !reflect.DeepEqual(got, []string{"notion", "linear"}) {
		t.Fatalf("notion registry tools = %v", got)
	}
	if got := workspace.ActionCount(); got != 34 {
		t.Fatalf("notion action count = %d, want 34", got)
	}
	assertDigest(t, workspace, notionDigestBaseline)

	family := NewRegistry()
	if got := family.ActionCount(); got != 60 {
		t.Fatalf("family action count = %d, want 60", got)
	}
	assertDigest(t, family, familyDigestBaseline)
}
