package credentials

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter

import (
	"fmt"
	"os"
	"strings"
	"time"
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

// DefaultAdapter builds the production adapter: the encrypted file store is
// the primary backend and the process env (or .env) is the fallback, so a
// credential that only exists as an env var still resolves. Upstream
// CredentialStoreAdapter.default() wraps EncryptedFileStorage in
// CompositeStorage with EnvVarStorage keyed by spec name; this mirrors that
// shape and additionally registers the credential id when it differs from the
// name (cloudflare / cloudflare_api_token).
//
// The local-only form deliberately registers no OAuth2 provider: upstream's
// default store is StaticProvider-only, and the HubSpot/Zoho providers need
// client credentials plus an unambiguous provider_id. Callers that own an
// OAuth2 provider register it with WithProviders; hive-cred uses
// DefaultAdapterWithAden so OAuth2 tokens refresh through the Aden platform
// when ADEN_API_KEY is configured.
//
// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default
func DefaultAdapter(specs []NamedSpec) (*CredentialStoreAdapter, error) {
	if specs == nil {
		specs = CatalogNamedSpecs()
	}
	return DefaultAdapterAt("", specs)
}

// DefaultAdapterAt is DefaultAdapter rooted at an explicit credentials
// directory, without the Aden upgrade. Session tools and credential routes
// resolve HIVE_HOME themselves, so they pass that directory instead of
// falling back to the process-wide environment; keeping the construction here
// ensures every caller gets the same encrypted-primary + env-fallback
// composite.
func DefaultAdapterAt(basePath string, specs []NamedSpec) (*CredentialStoreAdapter, error) {
	storage, local := localAdapterStorage(basePath, specs)
	return NewCredentialStoreAdapter(NewCredentialStore(WithStorage(storage)), specs, local), nil
}

// DefaultAdapterWithAden is the one-shot CLI flavour of DefaultAdapter. It
// mirrors upstream CredentialStoreAdapter.default(): when ADEN_API_KEY is set
// the local composite store is wrapped in AdenCachedStorage and an
// AdenSyncProvider is registered, so OAuth2/Bearer credentials refresh
// through the platform before hive-cred prints a value. The initial sync is
// best-effort; a missing key, an unbuildable client or a sync failure leaves
// the local store in place. This is the only default OAuth2-capable provider
// upstream wires into production.
//
// Serve and session paths keep using DefaultAdapterAt because their stores
// already share the runtime's Aden client; a fresh CLI process has no such
// store, so it needs the one-shot sync upstream performs.
//
// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default
func DefaultAdapterWithAden(basePath string, specs []NamedSpec) (*CredentialStoreAdapter, error) {
	if specs == nil {
		specs = CatalogNamedSpecs()
	}
	if strings.TrimSpace(os.Getenv(AdenEnvVar)) == "" {
		return DefaultAdapterAt(basePath, specs)
	}
	// Upstream's default adapter constructs the Aden client with a 5-second
	// timeout. One attempt keeps `hive-cred token` responsive when the
	// platform is unreachable; the token command is not a retry loop.
	client, err := NewAdenCredentialClient(AdenClientConfig{
		Timeout:       5 * time.Second,
		RetryAttempts: 1,
	})
	if err != nil {
		LogWarning("framework.credentials.store_adapter",
			"Aden backend unavailable, using local credential storage: %v", err)
		return DefaultAdapterAt(basePath, specs)
	}
	return defaultAdapterWithAdenClient(basePath, specs, client)
}

// defaultAdapterWithAdenClient is the injectable seam for tests and for
// callers that already own an Aden client. A nil client keeps the local
// composite store, matching the unconfigured branch of DefaultAdapterWithAden.
func defaultAdapterWithAdenClient(basePath string, specs []NamedSpec, client AdenSyncClient) (*CredentialStoreAdapter, error) {
	storage, local := localAdapterStorage(basePath, specs)
	if client == nil {
		return NewCredentialStoreAdapter(NewCredentialStore(WithStorage(storage)), specs, local), nil
	}
	provider := NewAdenSyncProvider(client, "", 5)
	cached := NewAdenCachedStorage(storage, provider, 0, true)
	store := NewCredentialStore(
		WithStorage(cached),
		WithProviders(provider),
		WithAutoRefresh(true),
	)
	// SyncAll treats listing and per-integration failures as diagnostics and
	// returns the number saved; upstream wraps the same call in try/except.
	provider.SyncAll(store)
	return NewCredentialStoreAdapter(store, specs, local), nil
}

// localAdapterStorage builds the encrypted-primary + env-fallback storage and
// the local account registry. If encrypted storage cannot be initialised it
// degrades to env-only storage, as upstream does.
func localAdapterStorage(basePath string, specs []NamedSpec) (CredentialStorage, LocalRegistry) {
	envMapping := envMappingForNamedSpecs(specs)
	envStorage := NewEnvVarStorage(envMapping)
	local := localRegistryAt(basePath)
	encrypted, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		return envStorage, local
	}
	return NewCompositeStorage(encrypted, envStorage), local
}

// localRegistryAt opens the local account registry beside the encrypted store
// so accounts saved through POST /api/credentials/local resolve through the
// same adapter as encrypted and env credentials.
func localRegistryAt(basePath string) LocalRegistry {
	if basePath == "" {
		return DefaultLocalCredentialRegistry()
	}
	if registry, err := LocalCredentialRegistryAtPath(basePath); err == nil && registry != nil {
		return registry
	}
	return DefaultLocalCredentialRegistry()
}

// envMappingForNamedSpecs builds the credential id -> env var table the env
// fallback storage reads. Upstream keys it by CREDENTIAL_SPECS name; Go CLI
// and tool callers also address credentials by CredentialID, so both keys map
// to the same variable.
func envMappingForNamedSpecs(specs []NamedSpec) map[string]string {
	mapping := make(map[string]string, len(specs)*2)
	for _, named := range specs {
		envVar := named.EnvVar
		if envVar == "" {
			continue
		}
		name := named.Name
		if name == "" {
			name = named.Spec.CredentialID
		}
		if name != "" {
			mapping[name] = envVar
		}
		if named.Spec.CredentialID != "" {
			mapping[named.Spec.CredentialID] = envVar
		}
	}
	return mapping
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
