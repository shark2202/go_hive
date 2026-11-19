package credentials

// Parity: tools/tests/test_credential_registry.py::test_has_env_var

import (
	"encoding/hex"
	"strings"
	"testing"
)

// upstreamSpecCount is the number of distinct credential ids declared by
// tools/src/aden_tools/credentials. Bumping it requires regenerating
// specs_gen.go with go_hive/scripts/gen_credential_specs.py.
const upstreamSpecCount = 118

// upstreamModuleCount is the number of .py files in the upstream package
// (including __init__.py, base.py and the helpers that declare no specs).
const upstreamModuleCount = 85

func generatedSpecByID(t *testing.T, credentialID string) GeneratedCredentialSpec {
	t.Helper()
	for _, spec := range GeneratedCredentialSpecs {
		if spec.CredentialID == credentialID {
			return spec
		}
	}
	t.Fatalf("credential id %q missing from GeneratedCredentialSpecs", credentialID)
	return GeneratedCredentialSpec{}
}

// TestGeneratedCredentialSpecsHaveRequiredFields mirrors the upstream registry
// test: every spec carries an env var, a credential id and a description, and
// credential ids are unique.
//
// Parity: tools/tests/test_credential_registry.py::test_has_env_var
func TestGeneratedCredentialSpecsHaveRequiredFields(t *testing.T) {
	if len(GeneratedCredentialSpecs) != upstreamSpecCount {
		t.Fatalf("GeneratedCredentialSpecs = %d, want %d", len(GeneratedCredentialSpecs), upstreamSpecCount)
	}
	if GeneratedSpecsModuleCount != upstreamModuleCount {
		t.Fatalf("GeneratedSpecsModuleCount = %d, want %d", GeneratedSpecsModuleCount, upstreamModuleCount)
	}
	if _, err := hex.DecodeString(GeneratedSpecsDigest); err != nil || len(GeneratedSpecsDigest) != 64 {
		t.Fatalf("GeneratedSpecsDigest = %q, want 64 hex characters (err %v)", GeneratedSpecsDigest, err)
	}
	seen := make(map[string]struct{}, len(GeneratedCredentialSpecs))
	for _, spec := range GeneratedCredentialSpecs {
		if spec.CredentialID == "" {
			t.Fatalf("spec %q has an empty credential id", spec.Name)
		}
		if spec.EnvVar == "" {
			t.Fatalf("spec %q has an empty env var", spec.CredentialID)
		}
		if spec.Name == "" {
			t.Fatalf("spec %q has an empty name", spec.CredentialID)
		}
		if _, duplicate := seen[spec.CredentialID]; duplicate {
			t.Fatalf("credential id %q is declared more than once", spec.CredentialID)
		}
		seen[spec.CredentialID] = struct{}{}
	}
}

// TestGeneratedCredentialSpecsHaveDescriptions mirrors the upstream description
// invariant: a settings row without a description is unusable in the UI.
//
// Parity: tools/tests/test_credential_registry.py::test_has_description
func TestGeneratedCredentialSpecsHaveDescriptions(t *testing.T) {
	for _, spec := range GeneratedCredentialSpecs {
		if strings.TrimSpace(spec.Description) == "" {
			t.Fatalf("credential %q has no description", spec.CredentialID)
		}
	}
}

// TestGeneratedCredentialSpecsTrackToolConsumers proves the table records both
// halves of "what needs this credential": the tool names declared on the spec
// and the Go-side source modules that read it.
//
// Parity: tools/tests/test_credential_registry.py::test_has_tools_or_node_types
func TestGeneratedCredentialSpecsTrackToolConsumers(t *testing.T) {
	// slack_app is the one credential upstream documents as infrastructure-only
	// (_INFRA_ONLY_CREDENTIAL_SPECS in tools/tests/test_credential_registry.py).
	infraOnly := map[string]bool{"slack_app": true}
	withConsumers := 0
	for _, spec := range GeneratedCredentialSpecs {
		if len(spec.Tools) > 0 || len(spec.NodeTypes) > 0 {
			withConsumers++
			continue
		}
		if !infraOnly[spec.CredentialID] {
			t.Fatalf("credential %q declares neither tools nor node_types", spec.CredentialID)
		}
	}
	if withConsumers != upstreamSpecCount-len(infraOnly) {
		t.Fatalf("specs with tool consumers = %d, want %d", withConsumers, upstreamSpecCount-len(infraOnly))
	}
	github := generatedSpecByID(t, "github")
	if len(github.Tools) != 19 {
		t.Fatalf("github tools = %d, want 19", len(github.Tools))
	}
	if len(github.ToolModules) != 1 || github.ToolModules[0] != "github_tool" {
		t.Fatalf("github tool modules = %#v, want [github_tool]", github.ToolModules)
	}
	airtable := generatedSpecByID(t, "airtable_pat")
	if len(airtable.ToolModules) != 1 || airtable.ToolModules[0] != "airtable_tool" {
		t.Fatalf("airtable_pat tool modules = %#v, want [airtable_tool]", airtable.ToolModules)
	}
	modules := 0
	for _, spec := range GeneratedCredentialSpecs {
		if len(spec.ToolModules) > 0 {
			modules++
		}
	}
	if modules < 100 {
		t.Fatalf("only %d specs name a tool module, want at least 100", modules)
	}
}

// TestGeneratedCredentialSpecsCarryHealthCheckMetadata keeps the health-check
// endpoint and credential key projection honest: the settings UI offers a
// "test connection" action driven by these fields.
//
// Parity: tools/tests/test_credential_registry.py::test_checkers_have_corresponding_specs
func TestGeneratedCredentialSpecsCarryHealthCheckMetadata(t *testing.T) {
	withEndpoint := 0
	for _, spec := range GeneratedCredentialSpecs {
		if spec.CredentialKey == "" {
			t.Fatalf("credential %q has no credential key", spec.CredentialID)
		}
		if spec.HealthCheckEndpoint == "" {
			continue
		}
		withEndpoint++
		switch spec.HealthCheckMethod {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			t.Fatalf("credential %q has unsupported health check method %q", spec.CredentialID, spec.HealthCheckMethod)
		}
		if !strings.HasPrefix(spec.HealthCheckEndpoint, "http") && !strings.HasPrefix(spec.HealthCheckEndpoint, "/") {
			t.Fatalf("credential %q health check endpoint = %q", spec.CredentialID, spec.HealthCheckEndpoint)
		}
	}
	if withEndpoint < 40 {
		t.Fatalf("only %d specs have a health check endpoint, want at least 40", withEndpoint)
	}
	if key := generatedSpecByID(t, "notion").CredentialKey; key != "api_key" {
		t.Fatalf("notion credential key = %q, want api_key", key)
	}
	if key := generatedSpecByID(t, "github").CredentialKey; key != "access_token" {
		t.Fatalf("github credential key = %q, want access_token", key)
	}
}

// TestGeneratedCredentialSpecsPreferCanonicalModules pins the dedupe rule: five
// credential ids are declared twice upstream, and the copy that belongs to the
// module merged into __init__.py must win over integrations.py/x.py/zoho.py.
//
// Parity-exempt: duplicate credential ids (github, hubspot, linear,
// x_bearer_token, zoho_crm) are a Python-side packaging detail; the merge
// preference has no upstream test.
func TestGeneratedCredentialSpecsPreferCanonicalModules(t *testing.T) {
	cases := map[string]string{
		"github":         "github.py",
		"hubspot":        "hubspot.py",
		"linear":         "linear.py",
		"x_bearer_token": "twitter.py",
		"zoho_crm":       "zoho_crm.py",
		"x_api_key":      "x.py",
	}
	for credentialID, module := range cases {
		if got := generatedSpecByID(t, credentialID).SourceModule; got != module {
			t.Fatalf("credential %q source module = %q, want %q", credentialID, got, module)
		}
	}
}
