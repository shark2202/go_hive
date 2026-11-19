package server

import (
	"net/http"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
)

// credentialSpecRouteRows fetches /api/credentials/specs and returns the rows
// keyed by credential id, failing on malformed or duplicate entries.
func credentialSpecRouteRows(t *testing.T, mux *http.ServeMux) map[string]map[string]any {
	t.Helper()
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/credentials/specs", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	rows := credentialRouteJSONList(t, body["specs"])
	byID := make(map[string]map[string]any, len(rows))
	for _, raw := range rows {
		row := credentialRouteJSONMap(t, raw)
		credentialID, _ := row["credential_id"].(string)
		if credentialID == "" {
			t.Fatalf("row without credential_id: %#v", row)
		}
		if _, duplicate := byID[credentialID]; duplicate {
			t.Fatalf("credential id %q emitted twice", credentialID)
		}
		byID[credentialID] = row
	}
	return byID
}

// specRouteMux builds the credential routes with the real registry and an
// optional specs store. Accounts are pinned empty so availability depends only
// on the store/env seam under test, never on the developer's ~/.hive.
func specRouteMux(specsStore func() credentialRouteStore) *http.ServeMux {
	return credentialRouteTestMux(CredentialRouteDeps{
		Specs:      credentialSpecsFromRegistry,
		SpecsStore: specsStore,
		Accounts:   func() map[string][]map[string]any { return map[string][]map[string]any{} },
	})
}

// TestCredentialSpecsRouteServesFullUpstreamRegistry is the regression guard for
// the old four-LLM-only endpoint: every credential id the Python registry
// declares must reach the JSON payload with the settings-row fields intact.
//
// Parity-exempt: the Python route serves CREDENTIAL_SPECS directly and has no
// route-level test that enumerates the catalogue; the table itself is covered by
// tools/tests/test_credential_registry.py.
func TestCredentialSpecsRouteServesFullUpstreamRegistry(t *testing.T) {
	byID := credentialSpecRouteRows(t, specRouteMux(func() credentialRouteStore {
		return credentials.ForTesting(nil)
	}))

	if len(byID) < len(credentials.GeneratedCredentialSpecs) {
		t.Fatalf("rows = %d, want at least %d", len(byID), len(credentials.GeneratedCredentialSpecs))
	}
	for _, spec := range credentials.GeneratedCredentialSpecs {
		if _, present := byID[spec.CredentialID]; !present {
			t.Fatalf("credential %q missing from /api/credentials/specs", spec.CredentialID)
		}
	}

	required := []string{
		"credential_name",
		"credential_id",
		"env_var",
		"description",
		"help_url",
		"api_key_instructions",
		"tools",
		"tool_modules",
		"aden_supported",
		"direct_api_key_supported",
		"credential_key",
		"credential_group",
		"available",
		"accounts",
	}
	for credentialID, row := range byID {
		for _, key := range required {
			if _, present := row[key]; !present {
				t.Fatalf("credential %q row missing %q: %#v", credentialID, key, row)
			}
		}
	}

	github := byID["github"]
	if github["env_var"] != "GITHUB_TOKEN" {
		t.Fatalf("github env_var = %v", github["env_var"])
	}
	if github["description"] != "GitHub Personal Access Token (classic)" {
		t.Fatalf("github description = %v", github["description"])
	}
	if github["credential_key"] != "access_token" {
		t.Fatalf("github credential_key = %v", github["credential_key"])
	}
	if github["aden_supported"] != true || github["direct_api_key_supported"] != true {
		t.Fatalf("github auth flags = %v/%v", github["aden_supported"], github["direct_api_key_supported"])
	}
	if github["available"] != false {
		t.Fatalf("github available = %v, want false without a store entry", github["available"])
	}
	modules := credentialRouteJSONList(t, github["tool_modules"])
	if len(modules) != 1 || modules[0] != "github_tool" {
		t.Fatalf("github tool_modules = %#v", modules)
	}
	tools := credentialRouteJSONList(t, github["tools"])
	if len(tools) != 19 {
		t.Fatalf("github tools = %d, want 19", len(tools))
	}

	// A credential declared only by an unmerged upstream module (x.py) must
	// still reach the route.
	if row, present := byID["x_api_key"]; !present || row["env_var"] != "X_API_KEY" {
		t.Fatalf("x_api_key row = %#v", row)
	}
}

// TestCredentialSpecsRouteAvailabilityReadsStore proves the "available" flag
// still comes from the credential store once the table is generated.
//
// Parity: core/framework/server/tests/test_api.py::TestCredentials::test_specs_availability_treats_decryption_error_as_unavailable
func TestCredentialSpecsRouteAvailabilityReadsStore(t *testing.T) {
	byID := credentialSpecRouteRows(t, specRouteMux(func() credentialRouteStore {
		return credentials.ForTesting(map[string]map[string]string{
			"github": {"access_token": "ghp_test"},
		})
	}))

	if byID["github"]["available"] != true {
		t.Fatalf("github available = %v, want true for a stored credential", byID["github"]["available"])
	}
	if byID["notion"]["available"] != false {
		t.Fatalf("notion available = %v, want false", byID["notion"]["available"])
	}
}

// TestCredentialSpecsRouteAvailabilityReadsEnv proves the env-var fallback uses
// the generated env var for each spec.
//
// Parity-exempt: env-backed availability lives in the credential store, covered
// by internal/credentials/store_test.go; this asserts the row projection.
func TestCredentialSpecsRouteAvailabilityReadsEnv(t *testing.T) {
	t.Setenv(credentials.CredentialKeyEnvVar, "")
	t.Setenv("NOTION_API_TOKEN", "notion-test-token")

	byID := credentialSpecRouteRows(t, specRouteMux(nil))

	if byID["notion"]["available"] != true {
		t.Fatalf("notion available = %v, want true from NOTION_API_TOKEN", byID["notion"]["available"])
	}
	if byID["github"]["available"] != false {
		t.Fatalf("github available = %v, want false", byID["github"]["available"])
	}
}

// TestCredentialSpecsRouteKeepsModelCatalogProviders pins the additive half of
// the union: model-catalog providers (openai, minimax, kimi, hive) are not in
// the upstream credential registry, and dropping them would remove LLM API-key
// entry from the settings page.
//
// Parity-exempt: the model catalog is a Go-port surface (/api/models/overview)
// with no Python credential-spec counterpart.
func TestCredentialSpecsRouteKeepsModelCatalogProviders(t *testing.T) {
	catalogSpecs := credentialSpecsFromCatalog()
	if len(catalogSpecs) == 0 {
		t.Fatal("model catalog returned no credential specs")
	}
	generated := make(map[string]struct{}, len(credentials.GeneratedCredentialSpecs))
	for _, spec := range credentials.GeneratedCredentialSpecs {
		generated[spec.CredentialID] = struct{}{}
	}
	wantRows := len(credentials.GeneratedCredentialSpecs) + 1 // + synthetic Aden Platform row
	for _, spec := range catalogSpecs {
		if _, covered := generated[spec.CredentialID]; !covered {
			wantRows++
		}
	}

	byID := credentialSpecRouteRows(t, specRouteMux(func() credentialRouteStore {
		return credentials.ForTesting(nil)
	}))
	if len(byID) != wantRows {
		t.Fatalf("rows = %d, want %d (registry + model catalog + synthetic Aden row)", len(byID), wantRows)
	}
	if _, present := byID[credentials.AdenCredentialID]; !present {
		t.Fatalf("synthetic %q row missing", credentials.AdenCredentialID)
	}
	for _, spec := range catalogSpecs {
		row, present := byID[spec.CredentialID]
		if !present {
			t.Fatalf("model catalog provider %q missing from /api/credentials/specs", spec.CredentialID)
		}
		if row["env_var"] != spec.EnvVar {
			t.Fatalf("%s env_var = %v, want %v", spec.CredentialID, row["env_var"], spec.EnvVar)
		}
		if row["direct_api_key_supported"] != true {
			t.Fatalf("%s direct_api_key_supported = %v", spec.CredentialID, row["direct_api_key_supported"])
		}
	}
}
