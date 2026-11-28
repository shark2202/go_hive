package credentials

// Parity: framework/credentials/catalog.py::credential_specs (+ browse/inspect consumers)

import "sort"

// CatalogNamedSpecs is the NamedSpec projection of the upstream CREDENTIAL_SPECS
// dict — the merged credential modules only. It is what `hive-cred browse`
// iterates for the connectable list and what the default adapter indexes
// accounts by, mirroring catalog.credential_specs() / CredentialStoreAdapter.default().
//
// Unmerged modules (x.py) and the model-catalog-only providers stay out, the
// same ids routes_credentials_specs filters; they remain reachable through
// GeneratedCredentialSpecs for the credential store's lookup paths.
func CatalogNamedSpecs() []NamedSpec {
	generated := GeneratedCredentialSpecs
	named := make([]NamedSpec, 0, len(generated))
	seen := make(map[string]struct{}, len(generated))
	for _, spec := range generated {
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if credentialID == "" || UnmergedCredentialID(credentialID) {
			continue
		}
		if _, duplicate := seen[credentialID]; duplicate {
			continue
		}
		seen[credentialID] = struct{}{}
		name := spec.Name
		if name == "" {
			name = credentialID
		}
		named = append(named, NamedSpec{
			Name: name,
			Spec: CredentialSpec{
				CredentialID:     spec.CredentialID,
				AdenProviderName: spec.AdenProviderName,
				Description:      spec.Description,
			},
			EnvVar: spec.EnvVar,
		})
	}
	sort.SliceStable(named, func(i, j int) bool {
		return credentialIDOf(named[i]) < credentialIDOf(named[j])
	})
	return named
}

func credentialIDOf(named NamedSpec) string {
	if named.Spec.CredentialID != "" {
		return named.Spec.CredentialID
	}
	return named.Name
}
