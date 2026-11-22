package crm

// Catalog digests: the three lanes moved out of internal/tools/integrations/
// wholesale (int-split-p3 lane, base 00d51e22) into this shared family
// package. The published surface (tool name, description, JSON schema) must be
// byte-identical to the pre-move catalogs; the digests below were computed
// from the former in-package registries (newBatch6Lane1Registry,
// newBatch6Lane2Registry, newBatch5Lane4Registry and their union) before the
// move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// Lane baselines at 00d51e22.
const (
	crmDigestBaseline     = "9e97e7ae954d5d47887c2f63dac7f1999a10e2d6c16ca2afc536addcca538572"
	supportDigestBaseline = "8d90a93d41e2f3a54aaeb01f22ff133516370845028a69298b335c185ebf0e4f"
	hubObsDigestBaseline  = "64a178d0e9f0600b08561981c53f64c32abad00ca08e0954ecac560613e00921"
	familyDigestBaseline  = "fff012ee4064141a861c30f5c8d0d8f3a44b09af9992a5bf89dfe93ee07cef94"
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
	crmLane := NewCRMRegistry()
	if got := crmLane.Names(); !reflect.DeepEqual(got, []string{"salesforce", "pipedrive", "zoho_crm", "attio"}) {
		t.Fatalf("crm registry tools = %v", got)
	}
	if got := crmLane.ActionCount(); got != 43 {
		t.Fatalf("crm action count = %d, want 43", got)
	}
	assertDigest(t, crmLane, crmDigestBaseline)

	support := NewSupportRegistry()
	if got := support.Names(); !reflect.DeepEqual(got, []string{"intercom", "zendesk", "freshdesk", "greenhouse"}) {
		t.Fatalf("support registry tools = %v", got)
	}
	if got := support.ActionCount(); got != 45 {
		t.Fatalf("support action count = %d, want 45", got)
	}
	assertDigest(t, support, supportDigestBaseline)

	hubObs := NewHubSpotObsidianRegistry()
	if got := hubObs.Names(); !reflect.DeepEqual(got, []string{"hubspot", "obsidian"}) {
		t.Fatalf("hubspot/obsidian registry tools = %v", got)
	}
	if got := hubObs.ActionCount(); got != 21 {
		t.Fatalf("hubspot/obsidian action count = %d, want 21", got)
	}
	assertDigest(t, hubObs, hubObsDigestBaseline)

	family := NewRegistry()
	if got := family.ActionCount(); got != 109 {
		t.Fatalf("family action count = %d, want 109", got)
	}
	assertDigest(t, family, familyDigestBaseline)
}
