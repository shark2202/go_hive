package integrations

// Account-aware credential resolution, shared by every module whose upstream
// tool exposes the `account` selector (hubspot, notion, discord, mattermost,
// google docs/gmail/calendar). Upstream, each of those tools calls
// `credentials.get_by_alias(provider, account)` when an alias is supplied and
// falls back to its single configured credential otherwise; these helpers
// reproduce that rule against the runtime's account view.
//
// The alias an action selected travels on the per-call runtime view (see
// integrationkit.Runtime.ForCall and Registry.execute), so the helpers stay
// parameter-free at every call site. Modules without upstream account support
// (obsidian; google sheets, whose `account` is a framework tracking parameter)
// never consult them and keep resolving their single token unchanged.
import (
	"os"
	"strings"
)

// accountToken resolves a module credential for the current call: an account
// alias the action selected wins over the module's single token. A selected
// alias that does not resolve yields "" so the caller reports its own auth
// error, matching upstream returning None from get_by_alias.
func accountToken(rt *Runtime, moduleID, provider string) string {
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

// batch7Lane8AccountProvider maps a lane-8 module to the upstream OAuth
// provider name its `account` parameter selects. Empty means the module has no
// account-aware credential (brevo/calcom/calendly keep their single token).
func batch7Lane8AccountProvider(moduleID string) string {
	switch moduleID {
	case "discord_tool":
		return "discord"
	case "mattermost_tool":
		return "mattermost"
	default:
		return ""
	}
}
