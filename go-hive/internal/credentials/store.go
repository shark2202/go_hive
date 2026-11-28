package credentials

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

const defaultCacheTTL = 300 * time.Second

type cacheEntry struct {
	credential *CredentialObject
	at         time.Time
}

type CredentialStore struct {
	mu            sync.Mutex
	storage       CredentialStorage
	providers     map[string]CredentialProvider
	providerOrder []string
	usageSpecs    map[string]*CredentialUsageSpec
	cache         map[string]cacheEntry
	cacheTTL      time.Duration
	autoRefresh   bool
	resolver      *TemplateResolver
}

type StoreOption func(*CredentialStore)

func WithStorage(storage CredentialStorage) StoreOption {
	return func(s *CredentialStore) { s.storage = storage }
}

func WithProviders(providers ...CredentialProvider) StoreOption {
	return func(s *CredentialStore) {
		for _, provider := range providers {
			s.registerProviderLocked(provider)
		}
	}
}

func WithCacheTTL(ttl time.Duration) StoreOption {
	return func(s *CredentialStore) { s.cacheTTL = ttl }
}

func WithAutoRefresh(enabled bool) StoreOption {
	return func(s *CredentialStore) { s.autoRefresh = enabled }
}

func NewCredentialStore(opts ...StoreOption) *CredentialStore {
	s := &CredentialStore{
		storage:     NewEnvVarStorage(nil),
		providers:   map[string]CredentialProvider{},
		usageSpecs:  map[string]*CredentialUsageSpec{},
		cache:       map[string]cacheEntry{},
		cacheTTL:    defaultCacheTTL,
		autoRefresh: true,
	}
	for _, opt := range opts {
		opt(s)
	}
	if len(s.providerOrder) == 0 {
		s.registerProviderLocked(StaticProvider{})
	}
	s.resolver = NewTemplateResolver(s)
	return s
}

func (s *CredentialStore) RegisterProvider(provider CredentialProvider) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerProviderLocked(provider)
}

func (s *CredentialStore) registerProviderLocked(provider CredentialProvider) {
	id := provider.ProviderID()
	if _, exists := s.providers[id]; !exists {
		s.providerOrder = append(s.providerOrder, id)
	}
	s.providers[id] = provider
}

func (s *CredentialStore) GetProvider(providerID string) CredentialProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providers[providerID]
}

func (s *CredentialStore) GetProviderForCredential(credential *CredentialObject) CredentialProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.providerForCredentialLocked(credential)
}

func (s *CredentialStore) providerForCredentialLocked(credential *CredentialObject) CredentialProvider {
	if credential.ProviderID != "" {
		if provider, ok := s.providers[credential.ProviderID]; ok {
			return provider
		}
	}
	for _, id := range s.providerOrder {
		provider := s.providers[id]
		if provider.CanHandle(credential) {
			return provider
		}
	}
	return nil
}

func (s *CredentialStore) RegisterUsage(spec *CredentialUsageSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageSpecs[spec.CredentialID] = spec
}

func (s *CredentialStore) GetUsageSpec(credentialID string) *CredentialUsageSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usageSpecs[credentialID]
}

func (s *CredentialStore) GetCredential(credentialID string, refreshIfNeeded bool) (*CredentialObject, error) {
	return s.GetCredentialEx(credentialID, refreshIfNeeded, false)
}

func (s *CredentialStore) GetCredentialEx(credentialID string, refreshIfNeeded, raiseOnRefreshFailure bool) (*CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cached := s.fromCacheLocked(credentialID); cached != nil {
		if refreshIfNeeded {
			if s.shouldRefreshLocked(cached) {
				return s.refreshCredentialLocked(cached, raiseOnRefreshFailure)
			}
			if raiseOnRefreshFailure && s.unrefreshableWithoutProviderLocked(cached) {
				return nil, s.credentialExpiredLocked(cached, "no credential provider is registered to refresh it")
			}
		}
		return cached, nil
	}

	credential, err := s.storage.Load(credentialID)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, nil
	}
	if refreshIfNeeded {
		if s.shouldRefreshLocked(credential) {
			credential, err = s.refreshCredentialLocked(credential, raiseOnRefreshFailure)
			if err != nil {
				return nil, err
			}
		} else if raiseOnRefreshFailure && s.unrefreshableWithoutProviderLocked(credential) {
			return nil, s.credentialExpiredLocked(credential, "no credential provider is registered to refresh it")
		}
	}
	s.addToCacheLocked(credential)
	return credential, nil
}

func (s *CredentialStore) GetKey(credentialID, keyName string) (*string, error) {
	return s.GetKeyEx(credentialID, keyName, false)
}

func (s *CredentialStore) GetKeyEx(credentialID, keyName string, raiseOnRefreshFailure bool) (*string, error) {
	credential, err := s.GetCredentialEx(credentialID, true, raiseOnRefreshFailure)
	if err != nil || credential == nil {
		return nil, err
	}
	return credential.GetKey(keyName), nil
}

func (s *CredentialStore) Get(credentialID string) (*string, error) {
	return s.GetEx(credentialID, false)
}

func (s *CredentialStore) GetEx(credentialID string, raiseOnRefreshFailure bool) (*string, error) {
	credential, err := s.GetCredentialEx(credentialID, true, raiseOnRefreshFailure)
	if err != nil || credential == nil {
		return nil, err
	}
	return credential.GetDefaultKey(), nil
}

func (s *CredentialStore) Resolve(template string) (string, error) {
	return s.resolver.Resolve(template)
}

func (s *CredentialStore) ResolveHeaders(headers map[string]string) (map[string]string, error) {
	return s.resolver.ResolveHeaders(headers)
}

func (s *CredentialStore) ResolveParams(params map[string]string) (map[string]string, error) {
	return s.resolver.ResolveParams(params)
}

func (s *CredentialStore) ResolveForUsage(credentialID string) (map[string]any, error) {
	s.mu.Lock()
	spec := s.usageSpecs[credentialID]
	s.mu.Unlock()
	if spec == nil {
		return nil, fmt.Errorf("No usage spec registered for '%s'", credentialID)
	}

	result := map[string]any{}
	if len(spec.Headers) > 0 {
		headers, err := s.resolveHeaders(spec.Headers)
		if err != nil {
			return nil, err
		}
		result["headers"] = headers
	}
	if len(spec.QueryParams) > 0 {
		params, err := s.resolveParams(spec.QueryParams)
		if err != nil {
			return nil, err
		}
		result["params"] = params
	}
	if len(spec.BodyFields) > 0 {
		data := make(map[string]string, len(spec.BodyFields))
		for key, value := range spec.BodyFields {
			resolved, err := s.resolver.Resolve(value)
			if err != nil {
				return nil, err
			}
			data[key] = resolved
		}
		result["data"] = data
	}
	return result, nil
}

func (s *CredentialStore) resolveHeaders(headers map[string]string) (map[string]string, error) {
	return s.resolver.ResolveHeaders(headers)
}

func (s *CredentialStore) resolveParams(params map[string]string) (map[string]string, error) {
	return s.resolver.ResolveParams(params)
}

func (s *CredentialStore) SaveCredential(credential *CredentialObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.storage.Save(credential); err != nil {
		return err
	}
	s.addToCacheLocked(credential)
	return nil
}

func (s *CredentialStore) DeleteCredential(credentialID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, credentialID)
	return s.storage.Delete(credentialID)
}

func (s *CredentialStore) ListCredentials() ([]string, error) {
	s.mu.Lock()
	storage := s.storage
	s.mu.Unlock()
	return storage.ListAll()
}

func (s *CredentialStore) ListAccounts(providerName string) ([]map[string]any, error) {
	var creds []*CredentialObject
	if storage, ok := s.currentStorage().(ProviderAccountStorage); ok {
		loaded, err := storage.LoadAllForProvider(providerName)
		if err != nil {
			return nil, err
		}
		creds = loaded
	} else {
		credential, err := s.GetCredential(providerName, true)
		if err != nil {
			return nil, err
		}
		if credential != nil {
			creds = []*CredentialObject{credential}
		}
	}

	out := make([]map[string]any, 0, len(creds))
	for _, credential := range creds {
		var alias any
		if a := credential.Alias(); a != nil {
			alias = *a
		}
		out = append(out, map[string]any{
			"credential_id": credential.ID,
			"provider":      providerName,
			"alias":         alias,
			"identity":      credential.Identity().ToDict(),
		})
	}
	return out, nil
}

func (s *CredentialStore) currentStorage() CredentialStorage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storage
}

func (s *CredentialStore) GetCredentialByAlias(providerName, alias string) (*CredentialObject, error) {
	prefix := providerName + "/"
	if len(alias) >= len(prefix) && alias[:len(prefix)] == prefix {
		alias = alias[len(prefix):]
	}

	storage := s.currentStorage()
	if aliased, ok := storage.(AliasStorage); ok {
		return aliased.LoadByAlias(providerName, alias)
	}
	if byProvider, ok := storage.(ProviderAccountStorage); ok {
		creds, err := byProvider.LoadAllForProvider(providerName)
		if err != nil {
			return nil, err
		}
		for _, credential := range creds {
			if a := credential.Alias(); a != nil && *a == alias {
				return credential, nil
			}
		}
	}
	return nil, nil
}

func (s *CredentialStore) GetCredentialByIdentity(providerName, label string) (*CredentialObject, error) {
	return s.GetCredentialByAlias(providerName, label)
}

func (s *CredentialStore) IsAvailable(credentialID string) bool {
	credential, err := s.GetCredential(credentialID, false)
	return err == nil && credential != nil
}

func (s *CredentialStore) Exists(credentialID string) bool {
	storage := s.currentStorage()
	ok, err := storage.Exists(credentialID)
	return err == nil && ok
}

func (s *CredentialStore) ValidateForUsage(credentialID string) ([]string, error) {
	spec := s.GetUsageSpec(credentialID)
	if spec == nil {
		return []string{}, nil
	}
	credential, err := s.GetCredential(credentialID, true)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		return []string{fmt.Sprintf("Credential '%s' not found", credentialID)}, nil
	}
	errors := []string{}
	for _, keyName := range spec.RequiredKeys {
		if !credential.HasKey(keyName) {
			errors = append(errors, fmt.Sprintf("Missing required key '%s'", keyName))
		}
	}
	return errors, nil
}

func (s *CredentialStore) ValidateAll() (map[string][]string, error) {
	s.mu.Lock()
	ids := make([]string, 0, len(s.usageSpecs))
	for id := range s.usageSpecs {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	errors := map[string][]string{}
	for _, id := range ids {
		credErrors, err := s.ValidateForUsage(id)
		if err != nil {
			return nil, err
		}
		if len(credErrors) > 0 {
			errors[id] = credErrors
		}
	}
	return errors, nil
}

func (s *CredentialStore) ValidateCredential(credentialID string) (bool, error) {
	credential, err := s.GetCredential(credentialID, false)
	if err != nil || credential == nil {
		return false, err
	}
	provider := s.GetProviderForCredential(credential)
	if provider == nil {
		return credential.Keys.Len() > 0, nil
	}
	return provider.Validate(credential), nil
}

func (s *CredentialStore) shouldRefreshLocked(credential *CredentialObject) bool {
	if !s.autoRefresh {
		return false
	}
	if !credential.AutoRefresh {
		return false
	}
	provider := s.providerForCredentialLocked(credential)
	if provider == nil {
		return false
	}
	return provider.ShouldRefresh(credential)
}

// unrefreshableWithoutProviderLocked reports whether the credential holds an
// OAuth2/bearer access token that has actually expired while no provider is
// registered to refresh it. A token still inside the provider refresh buffer
// stays usable, so only a genuinely expired token is escalated. The
// best-effort read path still returns the stale credential, matching upstream,
// but a caller that asked for a reauth signal must not mistake that for a
// successful refresh.
func (s *CredentialStore) unrefreshableWithoutProviderLocked(credential *CredentialObject) bool {
	if !s.autoRefresh || !credential.AutoRefresh {
		return false
	}
	if credential.CredentialType != CredentialTypeOAuth2 && credential.CredentialType != CredentialTypeBearerToken {
		return false
	}
	if s.providerForCredentialLocked(credential) != nil {
		return false
	}
	key := credential.Keys.Get("access_token")
	if key == nil || key.ExpiresAt == nil {
		return false
	}
	return key.IsExpired()
}

// credentialExpiredLocked builds the structured reauth error shared by a
// failed refresh and the no-provider case, carrying the same provider/alias
// metadata so the CLI can report which account needs reconnecting.
func (s *CredentialStore) credentialExpiredLocked(credential *CredentialObject, reason string) *CredentialExpiredError {
	expired := &CredentialExpiredError{
		CredentialID: credential.ID,
		msg: fmt.Sprintf(
			"OAuth token for '%s' is expired and %s. Reauthorization required.",
			credential.ID, reason,
		),
	}
	if provider := credential.ProviderType(); provider != nil {
		expired.Provider = *provider
	}
	if alias := credential.Alias(); alias != nil {
		expired.Alias = *alias
	}
	return expired
}

func (s *CredentialStore) refreshCredentialLocked(credential *CredentialObject, raiseOnFailure bool) (*CredentialObject, error) {
	provider := s.providerForCredentialLocked(credential)
	if provider == nil {
		// Upstream logs and returns the stale credential here. Keep that
		// best-effort behaviour, but make the unsupported OAuth2 case visible:
		// without a provider the token can never be refreshed, so callers that
		// asked for a reauth signal get CredentialExpiredError instead of a
		// value that looks refreshed.
		LogWarning("framework.credentials.store",
			"No provider found for credential '%s'", credential.ID)
		if raiseOnFailure && s.unrefreshableWithoutProviderLocked(credential) {
			return nil, s.credentialExpiredLocked(credential, "no credential provider is registered to refresh it")
		}
		return credential, nil
	}

	refreshed, err := provider.Refresh(credential)
	if err != nil {
		var refreshErr *CredentialRefreshError
		if errors.As(err, &refreshErr) {
			if raiseOnFailure {
				return nil, s.credentialExpiredLocked(credential, fmt.Sprintf("refresh failed: %v", refreshErr))
			}
			return credential, nil
		}
		return nil, err
	}

	now := time.Now().UTC()
	refreshed.LastRefreshed = &now
	if err := s.storage.Save(refreshed); err != nil {
		return nil, err
	}
	s.addToCacheLocked(refreshed)
	return refreshed, nil
}

func (s *CredentialStore) RefreshCredential(credentialID string) (*CredentialObject, error) {
	credential, err := s.GetCredential(credentialID, false)
	if err != nil || credential == nil {
		return credential, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshCredentialLocked(credential, false)
}

// NewLifecycleManager returns a TokenLifecycleManager bound to this store for
// credentialID, giving callers refresh-on-access instead of raw token reads.
// provider supplies the token endpoint and request formatting; a non-positive
// refreshBuffer keeps the five-minute default. Each call returns a fresh
// manager with its own token cache, so keep one per credential rather than
// building one per request.
//
// Parity: core/framework/credentials/oauth2/lifecycle.py::TokenLifecycleManager
func (s *CredentialStore) NewLifecycleManager(provider *BaseOAuth2Provider, credentialID string, refreshBuffer time.Duration) *TokenLifecycleManager {
	return NewTokenLifecycleManager(provider, credentialID, s, refreshBuffer)
}

func (s *CredentialStore) fromCacheLocked(credentialID string) *CredentialObject {
	entry, ok := s.cache[credentialID]
	if !ok {
		return nil
	}
	if time.Since(entry.at) > s.cacheTTL {
		delete(s.cache, credentialID)
		return nil
	}
	return entry.credential
}

func (s *CredentialStore) addToCacheLocked(credential *CredentialObject) {
	s.cache[credential.ID] = cacheEntry{credential: credential, at: time.Now().UTC()}
}

func (s *CredentialStore) ClearCache() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = map[string]cacheEntry{}
}

func ForTesting(credentials map[string]map[string]string) *CredentialStore {
	data := map[string]*CredentialObject{}
	for credentialID, keys := range credentials {
		credential := NewCredentialObject(credentialID)
		names := make([]string, 0, len(keys))
		for name := range keys {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			credential.Keys.Set(NewCredentialKey(name, keys[name]))
		}
		data[credentialID] = credential
	}
	return NewCredentialStore(
		WithStorage(NewInMemoryStorage(data)),
		WithAutoRefresh(false),
	)
}

func WithEncryptedStorage(basePath string, opts ...StoreOption) (*CredentialStore, error) {
	storage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		return nil, err
	}
	return NewCredentialStore(append(opts, WithStorage(storage))...), nil
}

// WithAdenSync builds a credential store that syncs OAuth2 tokens from the
// Aden server. An explicitly supplied client counts as configured even when
// ADEN_API_KEY is unset (tests inject fakes this way). With no client and no
// ADEN_API_KEY, it degrades to the encrypted local-only store with the default
// StaticProvider. With a client (or a key in the environment) the store uses an
// AdenCachedStorage backend and an AdenSyncProvider sharing that same client.
//
// autoSync mirrors upstream's auto_sync: serve builds the store with false so
// startup never blocks on the network; tests and one-shot callers can opt in.
//
// Parity: core/framework/credentials/store.py::CredentialStore.with_aden_sync
func WithAdenSync(basePath string, client AdenSyncClient, cacheTTLSeconds int, autoSync bool, opts ...StoreOption) (*CredentialStore, error) {
	localStorage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		return nil, err
	}

	if client == nil {
		if os.Getenv(AdenEnvVar) == "" {
			LogInfo("framework.credentials.store",
				"ADEN_API_KEY not set, using local-only credential storage")
			return NewCredentialStore(append(opts, WithStorage(localStorage))...), nil
		}
		created, err := NewAdenClientFromEnv()
		if err != nil {
			LogWarning("framework.credentials.store", "Failed to build Aden backend: %v", err)
			return NewCredentialStore(append(opts, WithStorage(localStorage))...), nil
		}
		client = created
	}

	provider := NewAdenSyncProvider(client, "", 5)
	cachedStorage := NewAdenCachedStorage(localStorage, provider, cacheTTLSeconds, true)
	store := NewCredentialStore(append(
		opts,
		WithStorage(cachedStorage),
		WithProviders(provider),
		WithAutoRefresh(true),
	)...)
	if autoSync {
		provider.SyncAll(store)
	}
	return store, nil
}

// EnableAdenSync upgrades this store in place to sync credentials from Aden,
// so components already holding the store gain Aden awareness without a
// restart. It is a no-op returning 0 when ADEN_API_KEY is unset or the client
// cannot be built, and is safe to call repeatedly.
//
// Parity: core/framework/credentials/store.py::CredentialStore.enable_aden_sync
func (s *CredentialStore) EnableAdenSync(client AdenSyncClient, cacheTTLSeconds int) (int, error) {
	if s == nil {
		return 0, nil
	}
	if client == nil {
		if os.Getenv(AdenEnvVar) == "" {
			LogInfo("framework.credentials.store",
				"enable_aden_sync: ADEN_API_KEY not set; staying local-only")
			return 0, nil
		}
		created, err := NewAdenClientFromEnv()
		if err != nil {
			LogWarning("framework.credentials.store", "Failed to build Aden backend: %v", err)
			return 0, nil
		}
		client = created
	}

	provider := NewAdenSyncProvider(client, "", 5)
	s.mu.Lock()
	localStorage := s.storage
	if cached, ok := localStorage.(*AdenCachedStorage); ok {
		// Reuse the cache's local backend rather than nesting two caches.
		localStorage = cached.local
	}
	s.storage = NewAdenCachedStorage(localStorage, provider, cacheTTLSeconds, true)
	s.registerProviderLocked(provider)
	s.autoRefresh = true
	s.cache = map[string]cacheEntry{}
	s.mu.Unlock()

	synced := provider.SyncAll(s)
	LogInfo("framework.credentials.store",
		"enable_aden_sync: Aden sync activated, synced %d credential(s)", synced)
	return synced, nil
}

func WithEnvStorage(envMapping map[string]string, opts ...StoreOption) *CredentialStore {
	return NewCredentialStore(append(opts, WithStorage(NewEnvVarStorage(envMapping)))...)
}
