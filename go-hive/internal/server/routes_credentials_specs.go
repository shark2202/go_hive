package server

// Parity: tools/src/aden_tools/credentials/__init__.py::CREDENTIAL_SPECS
// Parity: framework/server/routes_credentials.py::handle_list_specs

import "github.com/aden-hive/hive-go/internal/credentials"

// credentialSpecsFromRegistry is the /api/credentials/specs source. It serves
// one row per credential declared by the upstream Python registry
// (tools/src/aden_tools/credentials, 118 credential ids), then appends the
// model-catalog providers that the registry does not cover so the settings page
// can still configure LLM API keys from the same endpoint. Rows are keyed by
// credential id, so a provider present in both sources is emitted once.
func credentialSpecsFromRegistry() []CredentialSpecView {
	generated := credentials.GeneratedCredentialSpecs
	specs := make([]CredentialSpecView, 0, len(generated)+8)
	seen := make(map[string]struct{}, len(generated))
	for _, spec := range generated {
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if _, duplicate := seen[credentialID]; duplicate {
			continue
		}
		seen[credentialID] = struct{}{}
		specs = append(specs, credentialSpecViewFromRegistry(spec))
	}
	for _, spec := range credentialSpecsFromCatalog() {
		if _, duplicate := seen[spec.CredentialID]; duplicate {
			continue
		}
		seen[spec.CredentialID] = struct{}{}
		specs = append(specs, spec)
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
		ToolModules:           spec.ToolModules,
		AdenSupported:         spec.AdenSupported,
		DirectAPIKeySupported: spec.DirectAPIKeySupported,
		CredentialKey:         credentialKey,
		CredentialGroup:       spec.CredentialGroup,
		AdenProviderName:      spec.AdenProviderName,
	}
}
