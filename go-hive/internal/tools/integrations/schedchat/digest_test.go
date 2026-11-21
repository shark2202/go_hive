package schedchat

// Catalog digest: this family moved out of internal/tools/integrations/
// wholesale (int-p2-schedchat lane, base 63d64352). The published surface
// (tool names, action names, name/description/JSON schema) must be
// byte-identical to the pre-move catalog; the digest below was computed from
// the former in-package registry (newBatch7Lane8Registry) before the move and
// hashes the same {names, actions, entries} JSON probe used for the before/
// after diff (before == after, 9,802 bytes).
//
// Component digests at the captured baseline:
//   names   83a16caa42b8693f9c3570bfa65cb38842c977994cdfa39a22b882e1eb6044f3
//   actions 2db21b4233f94425a80e426c4149ede465fd37517b037c6d8ebe7a0a522b14ca
//   entries 4f031702f1d92f24d07e74d51488f7c9237600559c48348cd6ee901bad461743

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// catalogDigestBaseline is the sha256 of the JSON probe
// {names, actions, entries} for the five scheduling/chat tools at 63d64352.
const catalogDigestBaseline = "b4f85f8ce8485bb2cf8e94f879874b879413ebe388fcc5a7015547bf31b0ea9c"

// probeActions is one tool's ordered action names in the probe payload.
type probeActions struct {
	Tool    string   `json:"tool"`
	Actions []string `json:"actions"`
}

// Parity-exempt: structural-move guard; upstream has no equivalent hash, the
// Python tool surface is covered by the tool contract freeze.
func TestCatalogDigest(t *testing.T) {
	registry := NewRegistry()
	if got := registry.Names(); !reflect.DeepEqual(got, []string{"brevo", "calcom", "calendly", "discord", "mattermost"}) {
		t.Fatalf("schedchat registry tools = %v, want brevo/calcom/calendly/discord/mattermost", got)
	}
	if got := registry.ActionCount(); got != 40 {
		t.Fatalf("schedchat action count = %d, want 40", got)
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
	if len(raw) != 9802 {
		t.Fatalf("catalog probe bytes = %d, want 9802", len(raw))
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != catalogDigestBaseline {
		t.Fatalf("schedchat catalog digest drifted:\n  got  %s\n  want %s\n"+
			"The model-facing surface (names/actions/name/description/schema) must not change in a structural refactor.",
			got, catalogDigestBaseline)
	}
}
