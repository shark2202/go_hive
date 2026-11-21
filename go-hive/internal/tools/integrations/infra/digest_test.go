package infra

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-infra lane, base 63d64352). The published surface (tool
// names, action names, name/description/JSON schema) must be byte-identical to
// the pre-move catalog; the digest below was computed from the former
// in-package registry (newBatch7Lane3Registry) before the move and hashes the
// same {names, actions, entries} JSON probe used for the before/after diff.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for the six infrastructure tools at 63d64352.
const catalogDigestBaseline = "aae7d29de914af0febe69a71d367335d529194e3955c42ebbfbfe2826934aace"

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Tool    string   `json:"tool"`
	Actions []string `json:"actions"`
}

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"terraform", "pagerduty", "prometheus", "kafka", "n8n", "tines"}) {
		t.Fatalf("infra registry tools = %v, want terraform/pagerduty/prometheus/kafka/n8n/tines", got)
	}
	if got := registry.ActionCount(); got != 32 {
		t.Fatalf("infra action count = %d, want 32", got)
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
		t.Fatalf("infra catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
