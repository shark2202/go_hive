package credentials

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter

import "fmt"

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
