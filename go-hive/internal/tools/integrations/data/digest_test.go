package data

// Catalog digests: both lanes moved out of internal/tools/integrations/
// wholesale (int-split-p3 lane, base 00d51e22) into this shared family
// package. The published surface (tool name, description, JSON schema) must be
// byte-identical to the pre-move catalogs; the digests below were computed
// from the former in-package registries (newBatch5Lane2Registry,
// newBatch6Lane4Registry and their union) before the move.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"
)

// Lane baselines at 00d51e22.
const (
	databasesDigestBaseline = "bcb63e5398b6e5c61f2f188958bb8e8f6e8a3e0d645842712d7286f5d6c4a049"
	platformDigestBaseline  = "6814f1fc3eea0097cd9f50cb4d2c362ebbb654f4bb723669ce5cc38d29d9d9dc"
	familyDigestBaseline    = "4a8904300aac244b8e5cef3ea0a1514153eaac23ebb027da8fca81a5920ffcbd"
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
	databases := NewDatabasesRegistry()
	if got := databases.Names(); !reflect.DeepEqual(got, []string{"postgres", "redis", "mongodb", "supabase"}) {
		t.Fatalf("databases registry tools = %v", got)
	}
	if got := databases.ActionCount(); got != 32 {
		t.Fatalf("databases action count = %d, want 32", got)
	}
	assertDigest(t, databases, databasesDigestBaseline)

	platform := NewPlatformRegistry()
	if got := platform.Names(); !reflect.DeepEqual(got, []string{"snowflake", "bigquery", "databricks", "redshift", "azure_sql", "powerbi", "sap"}) {
		t.Fatalf("platform registry tools = %v", got)
	}
	if got := platform.ActionCount(); got != 33 {
		t.Fatalf("platform action count = %d, want 33", got)
	}
	assertDigest(t, platform, platformDigestBaseline)

	family := NewRegistry()
	if got := family.ActionCount(); got != 65 {
		t.Fatalf("family action count = %d, want 65", got)
	}
	assertDigest(t, family, familyDigestBaseline)
}
