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
// the old four-LLM-only endpoint: every credential id the Python CREDENTIAL_SPECS
// dict serves must reach the JSON payload with the settings-row fields intact —
// and nothing else. Unmerged registry modules (x.py) and model-catalog providers
// are not part of the upstream route.
//
// Parity: framework/server/routes_credentials.py::handle_list_specs
func TestCredentialSpecsRouteServesFullUpstreamRegistry(t *testing.T) {
	byID := credentialSpecRouteRows(t, specRouteMux(func() credentialRouteStore {
		return credentials.ForTesting(nil)
	}))

	merged := 0
	for _, spec := range credentials.GeneratedCredentialSpecs {
		if credentials.UnmergedCredentialID(spec.CredentialID) {
			continue
		}
		merged++
		if _, present := byID[spec.CredentialID]; !present {
			t.Fatalf("credential %q missing from /api/credentials/specs", spec.CredentialID)
		}
	}
	if len(byID) != merged+1 { // + synthetic Aden Platform row
		t.Fatalf("rows = %d, want %d (merged registry + synthetic Aden row)", len(byID), merged+1)
	}

	required := []string{
		"credential_name",
		"credential_id",
		"env_var",
		"description",
		"help_url",
		"api_key_instructions",
		"tools",
		"aden_supported",
		"direct_api_key_supported",
		"credential_key",
		"credential_group",
		"available",
	}
	for credentialID, row := range byID {
		for _, key := range required {
			if _, present := row[key]; !present {
				t.Fatalf("credential %q row missing %q: %#v", credentialID, key, row)
			}
		}
		// Registry rows carry the per-provider account list. The synthetic
		// Aden Platform row does not: upstream inserts it as a literal dict
		// with no "accounts" key (handle_list_specs), so a key here would be
		// a Go-only field the strict shape comparison sees.
		_, hasAccounts := row["accounts"]
		if credentialID == credentials.AdenCredentialID {
			if hasAccounts {
				t.Fatalf("synthetic Aden row carries an accounts key: %#v", row)
			}
		} else if !hasAccounts {
			t.Fatalf("credential %q row missing %q: %#v", credentialID, "accounts", row)
		}
		if _, present := row["tool_modules"]; present {
			t.Fatalf("credential %q row carries the Go-only tool_modules field", credentialID)
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
	tools := credentialRouteJSONList(t, github["tools"])
	if len(tools) != 19 {
		t.Fatalf("github tools = %d, want 19", len(tools))
	}

	// An id declared only by an unmerged upstream module (x.py) is not part of
	// the upstream CREDENTIAL_SPECS dict, so the route must not invent a row:
	// the frozen contract has 115 rows and x_api_key is not one of them. The id
	// stays in GeneratedCredentialSpecs for the credential store's lookup
	// surfaces.
	if _, present := byID["x_api_key"]; present {
		t.Fatalf("x_api_key row must not be served: upstream CREDENTIAL_SPECS does not merge x.py")
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

// TestCredentialSpecsRouteExcludesModelCatalogAndUnmergedModules pins the row
// set to the upstream route: model-catalog providers (openai, minimax, kimi,
// hive) configure their keys through /api/models/overview, and unmerged
// credential modules (x.py) are not part of CREDENTIAL_SPECS — neither belongs
// in this response. The frozen API contract has 115 rows here.
//
// Parity: framework/server/routes_credentials.py::handle_list_specs
func TestCredentialSpecsRouteExcludesModelCatalogAndUnmergedModules(t *testing.T) {
	catalogSpecs := credentialSpecsFromCatalog()
	if len(catalogSpecs) == 0 {
		t.Fatal("model catalog returned no credential specs")
	}

	byID := credentialSpecRouteRows(t, specRouteMux(func() credentialRouteStore {
		return credentials.ForTesting(nil)
	}))

	for _, spec := range catalogSpecs {
		if _, present := byID[spec.CredentialID]; present {
			t.Fatalf("model catalog provider %q leaked into /api/credentials/specs", spec.CredentialID)
		}
	}
	for _, spec := range credentials.GeneratedCredentialSpecs {
		if !credentials.UnmergedCredentialID(spec.CredentialID) {
			continue
		}
		if _, present := byID[spec.CredentialID]; present {
			t.Fatalf("unmerged module credential %q leaked into /api/credentials/specs", spec.CredentialID)
		}
	}
}
