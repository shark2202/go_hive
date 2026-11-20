package credentials

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter

import (
	"fmt"
	"strings"
)

type CredentialStoreAdapter struct {
	store *CredentialStore
	specs []NamedSpec
	local LocalRegistry
}

func NewCredentialStoreAdapter(store *CredentialStore, specs []NamedSpec, local LocalRegistry) *CredentialStoreAdapter {
	return &CredentialStoreAdapter{store: store, specs: specs, local: local}
}

func (a *CredentialStoreAdapter) Store() *CredentialStore {
	if a == nil {
		return nil
	}
	return a.store
}

func (a *CredentialStoreAdapter) Specs() []NamedSpec {
	if a == nil {
		return nil
	}
	return a.specs
}

func (a *CredentialStoreAdapter) GetAllAccountInfo() []map[string]any {
	accounts := make([]map[string]any, 0)
	if a == nil {
		return accounts
	}
	seenSpecs := make(map[string]struct{}, len(a.specs))
	seenAccounts := make(map[[2]string]struct{})

	for _, named := range a.specs {
		credID := named.Spec.CredentialID
		if credID == "" {
			credID = named.Name
		}
		provider := named.Spec.AdenProviderName
		if provider == "" {
			provider = credID
		}
		if _, seen := seenSpecs[credID]; seen {
			continue
		}
		if a.store == nil {
			continue
		}
		value, err := a.store.Get(named.Name)
		if err != nil || value == nil || *value == "" {
			continue
		}
		providerAccounts, err := a.store.ListAccounts(provider)
		if err != nil {
			continue
		}
		seenSpecs[credID] = struct{}{}
		for _, acct := range providerAccounts {
			key := [2]string{mapString(acct, "provider"), mapString(acct, "alias")}
			if _, seen := seenAccounts[key]; seen {
				continue
			}
			seenAccounts[key] = struct{}{}
			copied := cloneAccount(acct)
			copied["credential_id"] = credID
			accounts = append(accounts, copied)
		}
	}

	for _, acct := range a.ListLocalAccounts("") {
		key := [2]string{mapString(acct, "provider"), mapString(acct, "alias")}
		if _, seen := seenAccounts[key]; seen {
			continue
		}
		seenAccounts[key] = struct{}{}
		copied := cloneAccount(acct)
		copied["credential_id"] = mapString(acct, "provider")
		accounts = append(accounts, copied)
	}
	return accounts
}

func (a *CredentialStoreAdapter) ListLocalAccounts(credentialID string) []map[string]any {
	if a == nil || a.local == nil {
		return []map[string]any{}
	}
	infos, err := a.local.ListAccounts(credentialID)
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.ToAccountDict())
	}
	return out
}

// Accounts lists the connected accounts for one provider, every account when
// provider is empty. It is the view behind get_account_info: upstream
// get_all_account_info() when unfiltered, and a provider-filtered slice of the
// same merged list when provider is set. Filtering the merged list (rather than
// the Aden store alone, as upstream list_accounts does) keeps locally-stored
// API-key accounts visible under the same filter.
func (a *CredentialStoreAdapter) Accounts(provider string) []map[string]any {
	accounts := a.GetAllAccountInfo()
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return accounts
	}
	out := make([]map[string]any, 0, len(accounts))
	for _, acct := range accounts {
		if AccountProvider(acct) == provider {
			out = append(out, acct)
		}
	}
	return out
}

// AccountToken resolves one named account's secret for a tool call. It mirrors
// upstream CredentialStoreAdapter.get_by_alias: the Aden store is asked for the
// (provider, alias) pair first — with refresh-on-access — and the local
// registry is the fallback for collected API-key accounts. ok is false when the
// alias does not resolve, so callers report their own auth error.
func (a *CredentialStoreAdapter) AccountToken(provider, alias string) (string, bool) {
	if a == nil {
		return "", false
	}
	provider = strings.TrimSpace(provider)
	alias = strings.TrimSpace(alias)
	if provider == "" || alias == "" {
		return "", false
	}
	if a.store != nil {
		cred, err := a.store.GetCredentialByAlias(provider, alias)
		if err == nil && cred != nil {
			// Re-fetch through GetCredentialEx so refresh-on-access fires, the
			// same reason upstream re-reads after the aliased lookup.
			if fresh, refreshErr := a.store.GetCredentialEx(cred.ID, true, true); refreshErr == nil && fresh != nil {
				cred = fresh
			}
			if value := cred.GetDefaultKey(); value != nil {
				if token := strings.TrimSpace(*value); token != "" {
					return token, true
				}
			}
		}
	}
	if a.local != nil {
		if value, found, err := a.local.GetKey(provider, alias, "api_key"); err == nil && found {
			if token := strings.TrimSpace(value); token != "" {
				return token, true
			}
		}
	}
	return "", false
}

func DefaultAdapter(specs []NamedSpec) (*CredentialStoreAdapter, error) {
	store, err := WithEncryptedStorage("")
	if err != nil {
		store = WithEnvStorage(map[string]string{})
	}
	return NewCredentialStoreAdapter(store, specs, DefaultLocalCredentialRegistry()), nil
}

func mapString(acct map[string]any, key string) string {
	value, ok := acct[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func cloneAccount(acct map[string]any) map[string]any {
	copied := make(map[string]any, len(acct)+1)
	for key, value := range acct {
		copied[key] = value
	}
	return copied
}
