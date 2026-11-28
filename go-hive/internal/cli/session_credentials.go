package cli

// Session credential view: the read-only projection of the host's credential
// store that a session attaches to its tool registry
// (tools.ToolRegistry.SetAccountSource). It backs the two agent-facing paths
// that need to know which accounts exist:
//
//   - get_account_info — the account list (upstream
//     tools/src/aden_tools/tools/account_info_tool/account_info_tool.py), and
//   - the `account` alias selector on account-aware actions (hubspot, notion,
//     discord, mattermost, google docs/gmail/calendar), resolved with the same
//     store the credential routes write (upstream
//     CredentialStoreAdapter.get_by_alias).
//
// The view is lazy: opening the encrypted store creates its directories, so a
// session that never asks about accounts pays nothing. Every failure degrades to
// an empty view instead of an error — get_account_info answers
// {"accounts":[],"count":0} and an unresolvable alias reports the tool's own
// auth error, never a panic.

import (
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type sessionAccountSource struct {
	home hivehome.Home

	once    sync.Once
	adapter *credentials.CredentialStoreAdapter
}

// newSessionAccountSource builds the view for one session's home. The store is
// opened on first use, not here.
func newSessionAccountSource(home hivehome.Home) *sessionAccountSource {
	return &sessionAccountSource{home: home}
}

// view opens the credential store once. It never returns nil: when the
// encrypted store cannot be opened the view falls back to an environment-only
// store, which still answers (with an empty account list).
func (v *sessionAccountSource) view() *credentials.CredentialStoreAdapter {
	v.once.Do(func() {
		v.adapter = buildSessionCredentialAdapter(v.home)
	})
	return v.adapter
}

// Accounts lists the connected accounts, all of them when provider is "".
func (v *sessionAccountSource) Accounts(provider string) []map[string]any {
	if v == nil {
		return []map[string]any{}
	}
	adapter := v.view()
	if adapter == nil {
		return []map[string]any{}
	}
	accounts := adapter.Accounts(provider)
	if accounts == nil {
		return []map[string]any{}
	}
	return accounts
}

// AccountToken resolves one named account's secret.
func (v *sessionAccountSource) AccountToken(provider, alias string) (string, bool) {
	if v == nil {
		return "", false
	}
	adapter := v.view()
	if adapter == nil {
		return "", false
	}
	return adapter.AccountToken(provider, alias)
}

// buildSessionCredentialAdapter opens the session's credential store the same
// way the credential routes do: pick up the persisted key file first (otherwise
// a fresh process generates a throwaway key and cannot read what an earlier run
// wrote), then build the production adapter under the session home. The
// adapter keeps the encrypted store primary and the spec env vars as fallback,
// so get_account_info and account aliases also see env-only credentials.
func buildSessionCredentialAdapter(home hivehome.Home) *credentials.CredentialStoreAdapter {
	_, _ = credentials.LoadCredentialKey()

	baseDir := ""
	if home.Root != "" {
		baseDir = home.CredentialsDir()
	}
	adapter, err := credentials.DefaultAdapterAt(baseDir, sessionCredentialSpecs())
	if err == nil && adapter != nil {
		return adapter
	}
	// Defensive fallback: an env-backed store still answers (with an empty
	// account list) rather than failing the tool call.
	store := credentials.WithEnvStorage(map[string]string{})
	return credentials.NewCredentialStoreAdapter(store, sessionCredentialSpecs(), nil)
}

// sessionCredentialSpecs projects the generated credential table onto the
// NamedSpec rows the adapter indexes accounts by. The credential routes build
// the same shape from the settings catalog; the table is the static superset
// that also covers providers without a settings row.
func sessionCredentialSpecs() []credentials.NamedSpec {
	generated := credentials.GeneratedCredentialSpecs
	named := make([]credentials.NamedSpec, 0, len(generated))
	seen := make(map[string]struct{}, len(generated))
	for _, spec := range generated {
		if spec.Name == "" {
			continue
		}
		if _, dup := seen[spec.Name]; dup {
			continue
		}
		seen[spec.Name] = struct{}{}
		named = append(named, credentials.NamedSpec{
			Name: spec.Name,
			Spec: credentials.CredentialSpec{
				CredentialID:     spec.CredentialID,
				AdenProviderName: spec.AdenProviderName,
				Description:      spec.Description,
			},
			EnvVar: spec.EnvVar,
		})
	}
	return named
}
