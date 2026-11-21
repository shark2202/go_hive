package enrichment

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-enrichment lane, base 63aacb15). The published surface
// (tool names, action names, name/description/JSON schema) must be
// byte-identical to the pre-move catalog; the digest below was computed from
// the former in-package registry (batch7Lane5NewRegistry) before the move and
// hashes the same {names, actions, entries} JSON probe used for the before/
// after diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for the five enrichment tools at 63aacb15.
const catalogDigestBaseline = "8bd792d97cda598dc3902b9579ccec63e4292b0db4a9307a0ba42bd1c6f9e758"

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Tool    string   `json:"tool"`
	Actions []string `json:"actions"`
}

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"apify", "apollo", "lusha", "serpapi", "exa_search"}) {
		t.Fatalf("enrichment registry tools = %v, want apify/apollo/lusha/serpapi/exa_search", got)
	}
	if got := registry.ActionCount(); got != 36 {
		t.Fatalf("enrichment action count = %d, want 36", got)
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
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != catalogDigestBaseline {
		t.Fatalf("enrichment catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
