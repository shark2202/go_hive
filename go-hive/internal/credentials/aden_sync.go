package credentials

// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage
//
// The Aden server is the authoritative source for OAuth2 tokens. AdenSyncProvider
// fetches short-lived access tokens and delegates refresh to Aden;
// AdenCachedStorage serves reads from a local cache and falls back to Aden when
// the cache is missing or stale.
//
// Divergences (documented):
//   - report_usage is not implemented: the Go AdenCredentialClient has no
//     report endpoint, so AdenSyncProvider carries no reportUsage switch.
//   - Mutexes make the provider/storage safe for concurrent use; upstream's
//     Python objects are not synchronized.

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	adenDefaultProviderID      = "aden_sync"
	adenDefaultRefreshBuffer   = 5 * time.Minute
	adenDefaultStorageCacheTTL = 300 * time.Second
)

// AdenSyncClient is the slice of *AdenCredentialClient that the provider and
// cached storage use. Tests inject fakes through this interface.
type AdenSyncClient interface {
	ListIntegrations() ([]AdenIntegrationInfo, error)
	GetCredential(integrationID string) (*AdenCredentialResponse, error)
	RequestRefresh(integrationID string) (*AdenCredentialResponse, error)
	ValidateToken(integrationID string) (map[string]any, error)
	Close() error
}

// CredentialSaver is the store surface SyncAll populates.
type CredentialSaver interface {
	SaveCredential(credential *CredentialObject) error
}

// AdenSyncProvider synchronizes credentials with the Aden authentication
// server: tokens are fetched from Aden, refresh is delegated to Aden, and the
// local store holds a cache.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider
type AdenSyncProvider struct {
	client        AdenSyncClient
	providerID    string
	refreshBuffer time.Duration
}

var _ CredentialProvider = (*AdenSyncProvider)(nil)

// NewAdenSyncProvider builds a provider. An empty providerID defaults to
// "aden_sync"; refreshBufferMinutes is the window before expiry that triggers
// a refresh (upstream default: 5).
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.__init__
func NewAdenSyncProvider(client AdenSyncClient, providerID string, refreshBufferMinutes int) *AdenSyncProvider {
	if providerID == "" {
		providerID = adenDefaultProviderID
	}
	return &AdenSyncProvider{
		client:        client,
		providerID:    providerID,
		refreshBuffer: time.Duration(refreshBufferMinutes) * time.Minute,
	}
}

// ProviderID returns the provider's unique identifier.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.provider_id
func (p *AdenSyncProvider) ProviderID() string { return p.providerID }

// SupportedTypes returns the credential types this provider manages.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.supported_types
func (p *AdenSyncProvider) SupportedTypes() []CredentialType {
	return []CredentialType{CredentialTypeOAuth2, CredentialTypeBearerToken}
}

// CanHandle reports whether the credential is supported and either explicitly
// linked to this provider or marked _aden_managed.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.can_handle
func (p *AdenSyncProvider) CanHandle(credential *CredentialObject) bool {
	if credential == nil || !supports(p.SupportedTypes(), credential.CredentialType) {
		return false
	}
	if credential.ProviderID == p.providerID {
		return true
	}
	flag := credential.Keys.Get("_aden_managed")
	return flag != nil && flag.GetSecretValue() == "true"
}

// Refresh asks Aden to refresh the token, then projects the response onto the
// credential. A cached local token is still usable when Aden is unavailable;
// an expired token surfaces CredentialRefreshError.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.refresh
func (p *AdenSyncProvider) Refresh(credential *CredentialObject) (*CredentialObject, error) {
	if credential == nil {
		return nil, newCredentialRefreshError("Cannot refresh a nil credential")
	}
	if p.client == nil {
		return nil, newCredentialRefreshError("Aden server unavailable and token expired for '%s'", credential.ID)
	}

	response, err := p.client.RequestRefresh(credential.ID)
	if err != nil {
		var refreshErr *AdenRefreshError
		if errors.As(err, &refreshErr) {
			LogWarning("framework.credentials.aden.provider",
				"Aden refresh failed for '%s': %v", credential.ID, refreshErr)
			if refreshErr.RequiresReauthorization {
				url := refreshErr.ReauthorizationURL
				if url == "" {
					url = "your Aden dashboard"
				}
				return nil, newCredentialRefreshError(
					"Integration '%s' requires re-authorization. Visit: %s", credential.ID, url)
			}
			return nil, newCredentialRefreshError("Failed to refresh credential '%s': %v", credential.ID, refreshErr)
		}

		var clientErr *AdenClientError
		if errors.As(err, &clientErr) {
			LogWarning("framework.credentials.aden.provider",
				"Aden client error for '%s': %v", credential.ID, clientErr)
			accessKey := credential.Keys.Get("access_token")
			if accessKey != nil && accessKey.ExpiresAt != nil &&
				time.Now().UTC().Before(*accessKey.ExpiresAt) {
				LogWarning("framework.credentials.aden.provider",
					"Aden unavailable, using cached token for '%s'", credential.ID)
				return credential, nil
			}
			return nil, newCredentialRefreshError(
				"Aden server unavailable and token expired for '%s'", credential.ID)
		}

		return nil, newCredentialRefreshError("Failed to refresh credential '%s': %v", credential.ID, err)
	}
	if response == nil {
		return nil, newCredentialRefreshError("Failed to refresh credential '%s': Aden returned no response", credential.ID)
	}

	updated := p.updateCredentialFromAden(credential, response)
	LogInfo("framework.credentials.aden.provider", "Refreshed credential '%s' via Aden server", updated.ID)
	return updated, nil
}

// Validate checks the token with Aden and falls back to local expiry when Aden
// cannot answer.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.validate
func (p *AdenSyncProvider) Validate(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	if p.client != nil {
		if result, err := p.client.ValidateToken(credential.ID); err == nil {
			valid, _ := result["valid"].(bool)
			return valid
		}
	}
	accessKey := credential.Keys.Get("access_token")
	if accessKey == nil {
		return false
	}
	if accessKey.ExpiresAt == nil {
		return true
	}
	return time.Now().UTC().Before(*accessKey.ExpiresAt)
}

// ShouldRefresh reports whether access_token is expired or inside the refresh
// buffer.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.should_refresh
func (p *AdenSyncProvider) ShouldRefresh(credential *CredentialObject) bool {
	if credential == nil {
		return false
	}
	accessKey := credential.Keys.Get("access_token")
	if accessKey == nil || accessKey.ExpiresAt == nil {
		return false
	}
	return !time.Now().UTC().Before(accessKey.ExpiresAt.Add(-p.refreshBuffer))
}

// Revoke is unsupported by the Aden provider; it matches upstream's default
// CredentialProvider.revoke.
func (p *AdenSyncProvider) Revoke(*CredentialObject) bool { return false }

// ListIntegrations exposes the client listing used by SyncAll. Upstream walks
// self._client.list_integrations() directly.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.sync_all
func (p *AdenSyncProvider) ListIntegrations() ([]AdenIntegrationInfo, error) {
	if p == nil || p.client == nil {
		return nil, &AdenClientError{Message: "Aden client is not initialized"}
	}
	return p.client.ListIntegrations()
}

// FetchFromAden fetches one credential directly from Aden, or nil when the
// integration is gone.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.fetch_from_aden
func (p *AdenSyncProvider) FetchFromAden(integrationID string) (*CredentialObject, error) {
	if p == nil || p.client == nil {
		return nil, &AdenClientError{Message: "Aden client is not initialized"}
	}
	response, err := p.client.GetCredential(integrationID)
	if err != nil {
		return nil, err
	}
	if response == nil {
		return nil, nil
	}
	return p.adenResponseToCredential(response), nil
}

// SyncAll lists integrations, fetches tokens for the active ones, and saves
// each into the store. Listing failures and per-integration failures are
// diagnostics, never fatal; the number of saved credentials is returned.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider.sync_all
func (p *AdenSyncProvider) SyncAll(store CredentialSaver) int {
	synced := 0
	integrations, err := p.ListIntegrations()
	if err != nil {
		LogWarning("framework.credentials.aden.provider",
			"Failed to list integrations from Aden: %v", err)
		return synced
	}

	for _, info := range integrations {
		if info.Status != "active" {
			LogWarning("framework.credentials.aden.provider",
				"Skipping connection '%s': status=%s", info.Alias, info.Status)
			continue
		}
		credential, err := p.FetchFromAden(info.IntegrationID)
		if err != nil {
			LogWarning("framework.credentials.aden.provider",
				"Failed to sync '%s': %v", info.Alias, err)
			continue
		}
		if credential == nil {
			continue
		}
		if store == nil {
			continue
		}
		if err := store.SaveCredential(credential); err != nil {
			LogWarning("framework.credentials.aden.provider",
				"Failed to sync '%s': %v", info.Alias, err)
			continue
		}
		synced++
		LogInfo("framework.credentials.aden.provider", "Synced credential '%s' from Aden", info.Alias)
	}
	return synced
}

// updateCredentialFromAden projects a refresh response onto an existing
// credential.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider._update_credential_from_aden
func (p *AdenSyncProvider) updateCredentialFromAden(credential *CredentialObject, response *AdenCredentialResponse) *CredentialObject {
	credential.Keys.Set(&CredentialKey{
		Name:      "access_token",
		Value:     response.AccessToken,
		ExpiresAt: response.ExpiresAt,
	})
	if len(response.Scopes) > 0 {
		credential.Keys.Set(NewCredentialKey("scope", strings.Join(response.Scopes, " ")))
	}
	credential.Keys.Set(NewCredentialKey("_aden_managed", "true"))
	credential.Keys.Set(NewCredentialKey("_integration_type", response.IntegrationType()))
	if response.Alias != "" {
		credential.Keys.Set(NewCredentialKey("_alias", response.Alias))
	}
	for name, value := range response.Metadata {
		if text, ok := value.(string); ok && text != "" {
			credential.Keys.Set(NewCredentialKey("_identity_"+name, text))
		}
	}
	now := time.Now().UTC()
	credential.LastRefreshed = &now
	credential.ProviderID = p.providerID
	return credential
}

// adenResponseToCredential converts an Aden get-token response into a
// credential object.
// Parity: core/framework/credentials/aden/provider.py::AdenSyncProvider._aden_response_to_credential
func (p *AdenSyncProvider) adenResponseToCredential(response *AdenCredentialResponse) *CredentialObject {
	credential := NewCredentialObject(response.IntegrationID)
	credential.CredentialType = CredentialTypeOAuth2
	credential.Keys.Set(&CredentialKey{
		Name:      "access_token",
		Value:     response.AccessToken,
		ExpiresAt: response.ExpiresAt,
	})
	credential.Keys.Set(NewCredentialKey("_aden_managed", "true"))
	credential.Keys.Set(NewCredentialKey("_integration_type", response.IntegrationType()))
	if response.Alias != "" {
		credential.Keys.Set(NewCredentialKey("_alias", response.Alias))
	}
	if len(response.Scopes) > 0 {
		credential.Keys.Set(NewCredentialKey("scope", strings.Join(response.Scopes, " ")))
	}
	for name, value := range response.Metadata {
		if text, ok := value.(string); ok && text != "" {
			credential.Keys.Set(NewCredentialKey("_identity_"+name, text))
		}
	}
	credential.ProviderID = p.providerID
	credential.AutoRefresh = true
	return credential
}

// AdenCachedStorage combines a local cache with Aden fallback: reads prefer a
// fresh local entry, stale entries are refreshed from Aden, and Aden failures
// degrade to the cached copy.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage
type AdenCachedStorage struct {
	mu              sync.Mutex
	local           CredentialStorage
	provider        *AdenSyncProvider
	cacheTTL        time.Duration
	preferLocal     bool
	cacheTimestamps map[string]time.Time
	providerIndex   map[string][]string
	aliasIndex      map[string]string
}

var (
	_ CredentialStorage      = (*AdenCachedStorage)(nil)
	_ ProviderAccountStorage = (*AdenCachedStorage)(nil)
	_ AliasStorage           = (*AdenCachedStorage)(nil)
	_ CredentialSaver        = (*AdenCachedStorage)(nil)
)

// NewAdenCachedStorage builds cached storage. cacheTTLSeconds <= 0 uses
// upstream's 300-second default.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.__init__
func NewAdenCachedStorage(local CredentialStorage, provider *AdenSyncProvider, cacheTTLSeconds int, preferLocal bool) *AdenCachedStorage {
	ttl := time.Duration(cacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = adenDefaultStorageCacheTTL
	}
	return &AdenCachedStorage{
		local:           local,
		provider:        provider,
		cacheTTL:        ttl,
		preferLocal:     preferLocal,
		cacheTimestamps: map[string]time.Time{},
		providerIndex:   map[string][]string{},
		aliasIndex:      map[string]string{},
	}
}

// Save writes to the local cache and updates both indexes.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.save
func (s *AdenCachedStorage) Save(credential *CredentialObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(credential)
}

func (s *AdenCachedStorage) saveLocked(credential *CredentialObject) error {
	if credential == nil {
		return fmt.Errorf("cannot cache a nil credential")
	}
	if s.local != nil {
		if err := s.local.Save(credential); err != nil {
			return err
		}
	}
	s.cacheTimestamps[credential.ID] = time.Now().UTC()
	s.indexProviderLocked(credential)
	return nil
}

// SaveCredential lets SyncAll populate this storage directly (upstream
// storage.sync_all_from_aden calls self.save).
func (s *AdenCachedStorage) SaveCredential(credential *CredentialObject) error {
	return s.Save(credential)
}

// Load resolves a provider name through the provider index first, then by
// exact credential id.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.load
func (s *AdenCachedStorage) Load(credentialID string) (*CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, resolvedID := range s.providerIndex[credentialID] {
		if resolvedID == credentialID {
			continue
		}
		credential, err := s.loadByIDLocked(resolvedID)
		if err != nil {
			return nil, err
		}
		if credential != nil {
			return credential, nil
		}
	}
	return s.loadByIDLocked(credentialID)
}

// loadByIDLocked serves a fresh cache entry, otherwise refreshes an
// Aden-managed credential from Aden and falls back to the stale copy on
// failure.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage._load_by_id
func (s *AdenCachedStorage) loadByIDLocked(credentialID string) (*CredentialObject, error) {
	var local *CredentialObject
	if s.local != nil {
		loaded, err := s.local.Load(credentialID)
		if err != nil {
			return nil, err
		}
		local = loaded
	}
	if s.preferLocal && local != nil && s.isCacheFreshLocked(credentialID) {
		return local, nil
	}
	if local == nil {
		return nil, nil
	}
	if !s.isAdenManagedLocked(credentialID, local) {
		return local, nil
	}
	if s.provider == nil {
		return local, nil
	}

	credential, err := s.provider.FetchFromAden(credentialID)
	if err != nil {
		LogWarning("framework.credentials.aden.storage",
			"Failed to fetch '%s' from Aden: %v", credentialID, err)
		LogInfo("framework.credentials.aden.storage", "Using stale cached credential '%s'", credentialID)
		return local, nil
	}
	if credential == nil {
		return local, nil
	}
	if err := s.saveLocked(credential); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *AdenCachedStorage) isAdenManagedLocked(credentialID string, credential *CredentialObject) bool {
	if _, indexed := s.providerIndex[credentialID]; indexed {
		return true
	}
	for _, ids := range s.providerIndex {
		for _, id := range ids {
			if id == credentialID {
				return true
			}
		}
	}
	return credential != nil && credential.Keys.Get("_aden_managed") != nil
}

// LoadAllForProvider returns every indexed credential for a provider name.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.load_all_for_provider
func (s *AdenCachedStorage) LoadAllForProvider(providerName string) ([]*CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := append([]string{}, s.providerIndex[providerName]...)
	results := make([]*CredentialObject, 0, len(ids))
	for _, id := range ids {
		credential, err := s.loadByIDLocked(id)
		if err != nil {
			return nil, err
		}
		if credential != nil {
			results = append(results, credential)
		}
	}
	return results, nil
}

// LoadByAlias resolves provider:alias through the in-memory alias index.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.load_by_alias
func (s *AdenCachedStorage) LoadByAlias(providerName, alias string) (*CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.aliasIndex[providerName+":"+alias]
	if !ok || id == "" {
		return nil, nil
	}
	return s.loadByIDLocked(id)
}

// Delete removes the local entry and its cache timestamp. It never deletes
// from the Aden server.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.delete
func (s *AdenCachedStorage) Delete(credentialID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cacheTimestamps, credentialID)
	if s.local == nil {
		return false, nil
	}
	return s.local.Delete(credentialID)
}

// ListAll lists credential ids from the local cache only.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.list_all
func (s *AdenCachedStorage) ListAll() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local == nil {
		return nil, nil
	}
	return s.local.ListAll()
}

// Exists checks the local cache by exact id and through the provider index.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.exists
func (s *AdenCachedStorage) Exists(credentialID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.local == nil {
		return false, nil
	}
	exists, err := s.local.Exists(credentialID)
	if err != nil || exists {
		return exists, err
	}
	for _, resolvedID := range s.providerIndex[credentialID] {
		if resolvedID == credentialID {
			continue
		}
		if found, err := s.local.Exists(resolvedID); err != nil {
			return false, err
		} else if found {
			return true, nil
		}
	}
	return false, nil
}

// IsCacheFresh reports whether the local entry is inside the cache TTL.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage._is_cache_fresh
func (s *AdenCachedStorage) IsCacheFresh(credentialID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isCacheFreshLocked(credentialID)
}

func (s *AdenCachedStorage) isCacheFreshLocked(credentialID string) bool {
	cachedAt, ok := s.cacheTimestamps[credentialID]
	if !ok {
		return false
	}
	return time.Since(cachedAt) < s.cacheTTL
}

// InvalidateCache forces the next load to consult Aden.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.invalidate_cache
func (s *AdenCachedStorage) InvalidateCache(credentialID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cacheTimestamps, credentialID)
}

// InvalidateAll drops every cache timestamp.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.invalidate_all
func (s *AdenCachedStorage) InvalidateAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cacheTimestamps = map[string]time.Time{}
}

// indexProviderLocked maps provider name -> credential id and provider:alias ->
// credential id.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage._index_provider
func (s *AdenCachedStorage) indexProviderLocked(credential *CredentialObject) {
	integrationKey := credential.Keys.Get("_integration_type")
	if integrationKey == nil {
		return
	}
	providerName := integrationKey.GetSecretValue()
	if providerName == "" {
		return
	}
	ids := s.providerIndex[providerName]
	present := false
	for _, id := range ids {
		if id == credential.ID {
			present = true
			break
		}
	}
	if !present {
		s.providerIndex[providerName] = append(ids, credential.ID)
	}
	if aliasKey := credential.Keys.Get("_alias"); aliasKey != nil {
		if alias := aliasKey.GetSecretValue(); alias != "" {
			s.aliasIndex[providerName+":"+alias] = credential.ID
		}
	}
}

// RebuildProviderIndex re-indexes every locally cached credential. The return
// value counts credentials that introduced a new provider mapping, matching
// upstream's counter.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.rebuild_provider_index
func (s *AdenCachedStorage) RebuildProviderIndex() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providerIndex = map[string][]string{}
	s.aliasIndex = map[string]string{}
	indexed := 0
	if s.local == nil {
		return indexed, nil
	}
	ids, err := s.local.ListAll()
	if err != nil {
		return indexed, err
	}
	for _, id := range ids {
		credential, err := s.local.Load(id)
		if err != nil {
			return indexed, err
		}
		if credential == nil {
			continue
		}
		before := len(s.providerIndex)
		s.indexProviderLocked(credential)
		if len(s.providerIndex) > before {
			indexed++
		}
	}
	return indexed, nil
}

// SyncAllFromAden lists active integrations and caches each token. Listing
// failures are logged, never returned (upstream catches every exception).
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.sync_all_from_aden
func (s *AdenCachedStorage) SyncAllFromAden() int {
	synced := 0
	if s.provider == nil {
		return synced
	}
	integrations, err := s.provider.ListIntegrations()
	if err != nil {
		LogWarning("framework.credentials.aden.storage",
			"Failed to list integrations from Aden: %v", err)
		return synced
	}
	for _, info := range integrations {
		if info.Status != "active" {
			LogWarning("framework.credentials.aden.storage",
				"Skipping integration '%s': status=%s", info.Alias, info.Status)
			continue
		}
		credential, err := s.provider.FetchFromAden(info.IntegrationID)
		if err != nil {
			LogWarning("framework.credentials.aden.storage",
				"Failed to sync '%s': %v", info.Alias, err)
			continue
		}
		if credential == nil {
			continue
		}
		if err := s.Save(credential); err != nil {
			LogWarning("framework.credentials.aden.storage",
				"Failed to sync '%s': %v", info.Alias, err)
			continue
		}
		synced++
	}
	return synced
}

// GetCacheInfo reports cache freshness per locally cached credential.
// Parity: core/framework/credentials/aden/storage.py::AdenCachedStorage.get_cache_info
func (s *AdenCachedStorage) GetCacheInfo() (map[string]map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := map[string]map[string]any{}
	if s.local == nil {
		return info, nil
	}
	ids, err := s.local.ListAll()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, id := range ids {
		cachedAt, ok := s.cacheTimestamps[id]
		if !ok {
			info[id] = map[string]any{
				"cached_at":             nil,
				"is_fresh":              false,
				"ttl_remaining_seconds": 0.0,
			}
			continue
		}
		remaining := (cachedAt.Add(s.cacheTTL).Sub(now)).Seconds()
		if remaining < 0 {
			remaining = 0
		}
		info[id] = map[string]any{
			"cached_at":             cachedAt.Format(time.RFC3339),
			"is_fresh":              remaining > 0,
			"ttl_remaining_seconds": remaining,
		}
	}
	return info, nil
}
