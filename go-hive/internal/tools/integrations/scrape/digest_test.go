package scrape

// Catalog digest: this family moved out of internal/tools/integrations/ into
// its domain subpackage. The published surface ({names, actions, entries})
// must be byte-identical to the pre-move catalog; the digest below was computed
// from the former in-package batch3ScrapeTool before the move and hashes the
// same {names, actions, entries} JSON used for the before/after diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Actions []string `json:"actions"`
	Tool    string   `json:"tool"`
}

// surfaceDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for web_scrape before the move (1936 bytes,
// 8d80e3b17717e8990f9f113885cb4b87a1b94b3458a56f0c36d843dd52abbf20).
const surfaceDigestBaseline = "8d80e3b17717e8990f9f113885cb4b87a1b94b3458a56f0c36d843dd52abbf20"

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"web_scrape"}) {
		t.Fatalf("scrape registry tools = %v, want web_scrape", got)
	}
	if got := registry.ActionCount(); got != 1 {
		t.Fatalf("scrape action count = %d, want 1", got)
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
	if got := len(raw); got != 1936 {
		t.Fatalf("probe size = %d bytes, want 1936 (the pre-move probe size)", got)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != surfaceDigestBaseline {
		t.Fatalf("scrape catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/entries) must not change in a structural refactor.",
			got, surfaceDigestBaseline)
	}
}
