package server

// Parity: tools/src/aden_tools/credentials/__init__.py::CREDENTIAL_SPECS
// Parity: framework/server/routes_credentials.py::handle_list_specs

import "github.com/aden-hive/hive-go/internal/credentials"

// credentialSpecsFromRegistry is the /api/credentials/specs source. It serves
// one row per credential in the upstream CREDENTIAL_SPECS dict — the exact set
// the Python route iterates — plus the synthetic Aden Platform row that
// handleListSpecs prepends. Rows are keyed by credential id.
//
// Unmerged credential modules (x.py) and the model-catalog providers stay out:
// upstream's route never emits them, and the settings/models pages read their
// LLM keys from /api/models/overview (the frozen contract has 115 rows here).
// They remain in credentials.GeneratedCredentialSpecs for the credential store
// and CLI lookup paths.
func credentialSpecsFromRegistry() []CredentialSpecView {
	generated := credentials.GeneratedCredentialSpecs
	specs := make([]CredentialSpecView, 0, len(generated))
	seen := make(map[string]struct{}, len(generated))
	for _, spec := range generated {
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if credentials.UnmergedCredentialID(credentialID) {
			continue
		}
		if _, duplicate := seen[credentialID]; duplicate {
			continue
		}
		seen[credentialID] = struct{}{}
		specs = append(specs, credentialSpecViewFromRegistry(spec))
	}
	return specs
}

// credentialSpecViewFromRegistry projects one generated upstream spec onto the
// settings row shape. Defaults mirror base.py: a spec without an explicit
// credential_key reads "access_token", and every spec is directly configurable
// unless it opts out.
func credentialSpecViewFromRegistry(spec credentials.GeneratedCredentialSpec) CredentialSpecView {
	name := spec.Name
	if name == "" {
		name = spec.CredentialID
	}
	credentialKey := spec.CredentialKey
	if credentialKey == "" {
		credentialKey = "access_token"
	}
	return CredentialSpecView{
		Name:                  name,
		CredentialID:          spec.CredentialID,
		EnvVar:                spec.EnvVar,
		Description:           spec.Description,
		HelpURL:               spec.HelpURL,
		APIKeyInstructions:    spec.APIKeyInstructions,
		Tools:                 spec.Tools,
		AdenSupported:         spec.AdenSupported,
		DirectAPIKeySupported: spec.DirectAPIKeySupported,
		CredentialKey:         credentialKey,
		CredentialGroup:       spec.CredentialGroup,
		AdenProviderName:      spec.AdenProviderName,
	}
}
