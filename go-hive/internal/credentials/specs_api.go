package credentials

// UnmergedCredentialID reports whether id is declared only by a credential
// module that CREDENTIAL_SPECS does not merge (x.py today).
//
// /api/credentials/specs iterates CREDENTIAL_SPECS, so the route filters these
// ids out; GeneratedCredentialSpecs keeps their rows for the credential store's
// lookup surfaces. The list itself is generated — see specs_unmerged_gen.go.
func UnmergedCredentialID(id string) bool {
	for _, candidate := range GeneratedUnmergedCredentialIDs {
		if candidate == id {
			return true
		}
	}
	return false
}
