package collab

// Credential-resolution helpers shared by the collaboration modules.
// moduleToken / resolveAccountToken were root helpers (account_routing.go)
// reproduced here so the subpackage is self-contained.

import (
	"os"
	"strings"
)

// resolveAccountToken resolves a module credential for the current call: an
// account alias the action selected wins over the module's single token.
func resolveAccountToken(rt *Runtime, moduleID, provider string) string {
	if token, handled := rt.ResolveAccountToken(provider); handled {
		return token
	}
	return moduleToken(rt, moduleID)
}

// moduleToken resolves the module's own credential from WithCredential or the
// runtime environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	getenv := rt.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	token, _ := cfg.ResolvedToken(getenv)
	return strings.TrimSpace(token)
}
