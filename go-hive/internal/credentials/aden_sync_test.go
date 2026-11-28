package credentials

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAdenClient is a deterministic AdenSyncClient: no HTTP, no credentials.
type fakeAdenClient struct {
	integrations   []AdenIntegrationInfo
	responses      map[string]*AdenCredentialResponse
	listErr        error
	getErr         error
	refreshResult  *AdenCredentialResponse
	refreshErr     error
	validateResult map[string]any
	validateErr    error

	listCalls    int
	getCalls     int
	refreshCalls int
	closed       bool
}

func (c *fakeAdenClient) ListIntegrations() ([]AdenIntegrationInfo, error) {
	c.listCalls++
	if c.listErr != nil {
		return nil, c.listErr
	}
	return append([]AdenIntegrationInfo{}, c.integrations...), nil
}

func (c *fakeAdenClient) GetCredential(integrationID string) (*AdenCredentialResponse, error) {
	c.getCalls++
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.responses[integrationID], nil
}

func (c *fakeAdenClient) RequestRefresh(integrationID string) (*AdenCredentialResponse, error) {
	c.refreshCalls++
	if c.refreshErr != nil {
		return nil, c.refreshErr
	}
	return c.refreshResult, nil
}

func (c *fakeAdenClient) ValidateToken(integrationID string) (map[string]any, error) {
	if c.validateErr != nil {
		return nil, c.validateErr
	}
	if c.validateResult == nil {
		return map[string]any{"valid": true}, nil
	}
	return c.validateResult, nil
}

func (c *fakeAdenClient) Close() error {
	c.closed = true
	return nil
}

func adenTestResponse(id, token string) *AdenCredentialResponse {
	return &AdenCredentialResponse{
		IntegrationID: id,
		AccessToken:   token,
		TokenType:     "Bearer",
		Provider:      "google",
		Alias:         "default",
	}
}

func adenTestCredential(id, token string, expiresAt *time.Time) *CredentialObject {
	credential := NewCredentialObject(id)
	credential.CredentialType = CredentialTypeOAuth2
	credential.Keys.Set(&CredentialKey{Name: "access_token", Value: token, ExpiresAt: expiresAt})
	credential.Keys.Set(NewCredentialKey("_aden_managed", "true"))
	return credential
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_provider_id
func TestAdenSyncProviderIdentityAndTypes(t *testing.T) {
	provider := NewAdenSyncProvider(&fakeAdenClient{}, "", 5)
	if provider.ProviderID() != "aden_sync" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}
	types := provider.SupportedTypes()
	if len(types) != 2 || types[0] != CredentialTypeOAuth2 || types[1] != CredentialTypeBearerToken {
		t.Fatalf("supported types = %#v", types)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_can_handle_oauth2
func TestAdenSyncProviderCanHandle(t *testing.T) {
	provider := NewAdenSyncProvider(&fakeAdenClient{}, "", 5)
	oauth := adenTestCredential("google", "token", nil)
	if !provider.CanHandle(oauth) {
		t.Fatal("Aden-managed OAuth2 credential must be handled")
	}
	linked := NewCredentialObject("linked")
	linked.CredentialType = CredentialTypeOAuth2
	linked.ProviderID = "aden_sync"
	if !provider.CanHandle(linked) {
		t.Fatal("credential linked by provider_id must be handled")
	}
	apiKey := NewCredentialObject("api")
	apiKey.CredentialType = CredentialTypeAPIKey
	if provider.CanHandle(apiKey) {
		t.Fatal("API-key credential must not be Aden-handled")
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_sync_all
func TestAdenSyncProviderSyncAllSkipsInactive(t *testing.T) {
	client := &fakeAdenClient{
		integrations: []AdenIntegrationInfo{
			{IntegrationID: "hash-active", Provider: "google", Alias: "work", Status: "active"},
			{IntegrationID: "hash-inactive", Provider: "slack", Alias: "old", Status: "requires_reauth"},
		},
		responses: map[string]*AdenCredentialResponse{
			"hash-active": adenTestResponse("hash-active", "active-token"),
		},
	}
	provider := NewAdenSyncProvider(client, "", 5)
	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)), WithAutoRefresh(false))

	if synced := provider.SyncAll(store); synced != 1 {
		t.Fatalf("synced = %d, want 1", synced)
	}
	credential, err := store.GetCredential("hash-active", false)
	if err != nil || credential == nil {
		t.Fatalf("synced credential = %#v, err = %v", credential, err)
	}
	if token, _ := credential.GetKeyValue("access_token"); token != "active-token" {
		t.Fatalf("token = %q", token)
	}
	if providerKey, _ := credential.GetKeyValue("_integration_type"); providerKey != "google" {
		t.Fatalf("_integration_type = %q", providerKey)
	}
	if absent, _ := store.GetCredential("hash-inactive", false); absent != nil {
		t.Fatalf("inactive integration was synced: %#v", absent)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_refresh_success
func TestAdenSyncProviderRefreshSuccess(t *testing.T) {
	client := &fakeAdenClient{refreshResult: adenTestResponse("hash-google", "fresh-token")}
	provider := NewAdenSyncProvider(client, "", 5)
	credential := adenTestCredential("hash-google", "stale-token", nil)

	updated, err := provider.Refresh(credential)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token, _ := updated.GetKeyValue("access_token"); token != "fresh-token" {
		t.Fatalf("token = %q", token)
	}
	if updated.ProviderID != "aden_sync" || updated.LastRefreshed == nil {
		t.Fatalf("updated = %#v", updated)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_refresh_requires_reauth
func TestAdenSyncProviderRefreshRequiresReauth(t *testing.T) {
	client := &fakeAdenClient{refreshErr: &AdenRefreshError{
		AdenClientError:         &AdenClientError{Message: "refresh failed", StatusCode: 400},
		RequiresReauthorization: true,
		ReauthorizationURL:      "https://aden.example/reconnect",
	}}
	provider := NewAdenSyncProvider(client, "", 5)
	_, err := provider.Refresh(adenTestCredential("hash-google", "stale", nil))
	if err == nil {
		t.Fatal("expected CredentialRefreshError")
	}
	var refreshErr *CredentialRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("error type = %T", err)
	}
	if !strings.Contains(err.Error(), "requires re-authorization") ||
		!strings.Contains(err.Error(), "https://aden.example/reconnect") {
		t.Fatalf("error = %v", err)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_refresh_aden_unavailable_cached_valid
func TestAdenSyncProviderRefreshUsesCachedTokenWhenAdenUnavailable(t *testing.T) {
	client := &fakeAdenClient{refreshErr: &AdenClientError{Message: "connection failed"}}
	provider := NewAdenSyncProvider(client, "", 5)
	future := time.Now().UTC().Add(time.Hour)
	credential := adenTestCredential("hash-google", "cached-token", &future)

	updated, err := provider.Refresh(credential)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token, _ := updated.GetKeyValue("access_token"); token != "cached-token" {
		t.Fatalf("token = %q", token)
	}

	past := time.Now().UTC().Add(-time.Hour)
	_, err = provider.Refresh(adenTestCredential("hash-google", "expired", &past))
	if err == nil || !strings.Contains(err.Error(), "unavailable and token expired") {
		t.Fatalf("expired refresh error = %v", err)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_validate_fallback_to_local
func TestAdenSyncProviderValidateFallsBackToLocal(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	credential := adenTestCredential("hash-google", "token", &future)

	client := &fakeAdenClient{validateErr: &AdenClientError{Message: "unavailable"}}
	provider := NewAdenSyncProvider(client, "", 5)
	if !provider.Validate(credential) {
		t.Fatal("unexpired local token must validate when Aden is unavailable")
	}

	missing := NewCredentialObject("missing")
	missing.CredentialType = CredentialTypeOAuth2
	if provider.Validate(missing) {
		t.Fatal("credential without access_token must not validate")
	}

	noExpiry := adenTestCredential("no-expiry", "token", nil)
	if !provider.Validate(noExpiry) {
		t.Fatal("token without expiry is assumed valid")
	}

	client.validateResult = map[string]any{"valid": false}
	client.validateErr = nil
	if provider.Validate(credential) {
		t.Fatal("Aden validation result must win when reachable")
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_should_refresh_within_buffer
func TestAdenSyncProviderShouldRefresh(t *testing.T) {
	provider := NewAdenSyncProvider(&fakeAdenClient{}, "", 5)
	soon := time.Now().UTC().Add(time.Minute)
	if !provider.ShouldRefresh(adenTestCredential("soon", "token", &soon)) {
		t.Fatal("token inside the refresh buffer must refresh")
	}
	later := time.Now().UTC().Add(time.Hour)
	if provider.ShouldRefresh(adenTestCredential("later", "token", &later)) {
		t.Fatal("token outside the refresh buffer must not refresh")
	}
	if provider.ShouldRefresh(adenTestCredential("no-expiry", "token", nil)) {
		t.Fatal("token without expiry must not refresh")
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenSyncProvider::test_sync_all
func TestAdenSyncProviderSyncAllOverHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/credentials":
			_, _ = w.Write([]byte(`{"integrations":[` +
				`{"integration_id":"hash-github","provider":"github","alias":"default","status":"active"},` +
				`{"integration_id":"hash-old","provider":"github","alias":"old","status":"revoked"}]}`))
		case "/v1/credentials/hash-github":
			_, _ = w.Write([]byte(`{"integration_id":"hash-github","access_token":"http-token",` +
				`"provider":"github","alias":"default","metadata":{"email":"dev@example.com"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewAdenCredentialClient(AdenClientConfig{
		BaseURL:       server.URL,
		APIKey:        "test-key",
		RetryAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewAdenCredentialClient: %v", err)
	}
	defer client.Close()
	provider := NewAdenSyncProvider(client, "", 5)
	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)), WithAutoRefresh(false))

	if synced := provider.SyncAll(store); synced != 1 {
		t.Fatalf("synced = %d, want 1", synced)
	}
	credential, err := store.GetCredential("hash-github", false)
	if err != nil || credential == nil {
		t.Fatalf("credential = %#v, %v", credential, err)
	}
	if token, _ := credential.GetKeyValue("access_token"); token != "http-token" {
		t.Fatalf("token = %q", token)
	}
	if email := credential.Identity().Email; email == nil || *email != "dev@example.com" {
		t.Fatalf("identity = %#v", credential.Identity())
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCachedStorage::test_load_from_fresh_cache
func TestAdenCachedStorageFreshCacheSkipsAden(t *testing.T) {
	client := &fakeAdenClient{responses: map[string]*AdenCredentialResponse{
		"hash-google": adenTestResponse("hash-google", "fresh-token"),
	}}
	provider := NewAdenSyncProvider(client, "", 5)
	local := NewInMemoryStorage(nil)
	storage := NewAdenCachedStorage(local, provider, 300, true)

	credential := adenTestCredential("hash-google", "cached-token", nil)
	credential.Keys.Set(NewCredentialKey("_integration_type", "google"))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := storage.Load("hash-google")
	if err != nil || loaded == nil {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
	if token, _ := loaded.GetKeyValue("access_token"); token != "cached-token" {
		t.Fatalf("token = %q", token)
	}
	if client.getCalls != 0 {
		t.Fatalf("fresh cache consulted Aden %d times", client.getCalls)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCachedStorage::test_load_from_aden_when_stale
func TestAdenCachedStorageStaleCacheRefreshesFromAden(t *testing.T) {
	client := &fakeAdenClient{responses: map[string]*AdenCredentialResponse{
		"hash-google": adenTestResponse("hash-google", "fresh-token"),
	}}
	provider := NewAdenSyncProvider(client, "", 5)
	storage := NewAdenCachedStorage(NewInMemoryStorage(nil), provider, 1, true)

	credential := adenTestCredential("hash-google", "stale-token", nil)
	credential.Keys.Set(NewCredentialKey("_integration_type", "google"))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	storage.mu.Lock()
	storage.cacheTimestamps["hash-google"] = time.Now().UTC().Add(-2 * time.Second)
	storage.mu.Unlock()

	loaded, err := storage.Load("hash-google")
	if err != nil || loaded == nil {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
	if token, _ := loaded.GetKeyValue("access_token"); token != "fresh-token" {
		t.Fatalf("token = %q, want fresh-token", token)
	}
	if client.getCalls != 1 {
		t.Fatalf("Aden fetches = %d, want 1", client.getCalls)
	}
	// The Aden response is cached for the next read.
	storage.Load("hash-google")
	if client.getCalls != 1 {
		t.Fatalf("refreshed credential not cached: fetches = %d", client.getCalls)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCachedStorage::test_load_falls_back_to_stale_when_aden_fails
func TestAdenCachedStorageFallsBackToStaleOnAdenFailure(t *testing.T) {
	client := &fakeAdenClient{getErr: &AdenClientError{Message: "connection failed"}}
	provider := NewAdenSyncProvider(client, "", 5)
	storage := NewAdenCachedStorage(NewInMemoryStorage(nil), provider, 300, true)

	credential := adenTestCredential("hash-google", "stale-token", nil)
	credential.Keys.Set(NewCredentialKey("_integration_type", "google"))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("Save: %v", err)
	}
	storage.InvalidateCache("hash-google")

	loaded, err := storage.Load("hash-google")
	if err != nil || loaded == nil {
		t.Fatalf("Load = %#v, %v", loaded, err)
	}
	if token, _ := loaded.GetKeyValue("access_token"); token != "stale-token" {
		t.Fatalf("token = %q, want stale-token", token)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCachedStorage::test_load_by_provider_name
func TestAdenCachedStorageProviderAndAliasLookup(t *testing.T) {
	provider := NewAdenSyncProvider(&fakeAdenClient{}, "", 5)
	storage := NewAdenCachedStorage(NewInMemoryStorage(nil), provider, 300, true)

	work := adenTestCredential("google_work", "work-token", nil)
	work.Keys.Set(NewCredentialKey("_integration_type", "google"))
	work.Keys.Set(NewCredentialKey("_alias", "work"))
	personal := adenTestCredential("google_personal", "personal-token", nil)
	personal.Keys.Set(NewCredentialKey("_integration_type", "google"))
	personal.Keys.Set(NewCredentialKey("_alias", "personal"))
	for _, credential := range []*CredentialObject{work, personal} {
		if err := storage.Save(credential); err != nil {
			t.Fatalf("Save %s: %v", credential.ID, err)
		}
	}

	byProvider, err := storage.Load("google")
	if err != nil || byProvider == nil {
		t.Fatalf("Load(provider) = %#v, %v", byProvider, err)
	}
	if byProvider.ID != "google_work" {
		t.Fatalf("provider lookup id = %q", byProvider.ID)
	}
	byAlias, err := storage.LoadByAlias("google", "work")
	if err != nil || byAlias == nil || byAlias.ID != "google_work" {
		t.Fatalf("LoadByAlias = %#v, %v", byAlias, err)
	}
	if missing, err := storage.LoadByAlias("google", "nope"); err != nil || missing != nil {
		t.Fatalf("missing alias = %#v, %v", missing, err)
	}
	all, err := storage.LoadAllForProvider("google")
	if err != nil || len(all) != 2 {
		t.Fatalf("LoadAllForProvider = %d, %v", len(all), err)
	}
	if exists, err := storage.Exists("google"); err != nil || !exists {
		t.Fatalf("Exists(provider) = %v, %v", exists, err)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenCachedStorage::test_rebuild_provider_index
func TestAdenCachedStorageRebuildProviderIndex(t *testing.T) {
	provider := NewAdenSyncProvider(&fakeAdenClient{}, "", 5)
	local := NewInMemoryStorage(nil)
	storage := NewAdenCachedStorage(local, provider, 300, true)

	accounts := []*CredentialObject{
		adenTestCredential("google_work", "work", nil),
		adenTestCredential("google_personal", "personal", nil),
		adenTestCredential("hubspot_default", "hubspot", nil),
	}
	for i, credential := range accounts {
		providers := []string{"google", "google", "hubspot"}
		credential.Keys.Set(NewCredentialKey("_integration_type", providers[i]))
		if err := local.Save(credential); err != nil {
			t.Fatalf("local save: %v", err)
		}
	}

	indexed, err := storage.RebuildProviderIndex()
	if err != nil {
		t.Fatalf("RebuildProviderIndex: %v", err)
	}
	if indexed != 2 {
		t.Fatalf("indexed = %d, want 2 provider mappings", indexed)
	}
	all, err := storage.LoadAllForProvider("google")
	if err != nil || len(all) != 2 {
		t.Fatalf("google accounts = %d, %v", len(all), err)
	}
}

// Parity: core/framework/credentials/aden/tests/test_aden_sync.py::TestAdenIntegration::test_cached_storage_with_store
func TestAdenCachedStorageSyncAllFromAden(t *testing.T) {
	client := &fakeAdenClient{
		integrations: []AdenIntegrationInfo{
			{IntegrationID: "hash-google", Provider: "google", Alias: "default", Status: "active"},
		},
		responses: map[string]*AdenCredentialResponse{
			"hash-google": adenTestResponse("hash-google", "synced-token"),
		},
	}
	provider := NewAdenSyncProvider(client, "", 5)
	storage := NewAdenCachedStorage(NewInMemoryStorage(nil), provider, 300, true)

	if synced := storage.SyncAllFromAden(); synced != 1 {
		t.Fatalf("synced = %d", synced)
	}
	loaded, err := storage.LoadByAlias("google", "default")
	if err != nil || loaded == nil {
		t.Fatalf("synced credential = %#v, %v", loaded, err)
	}
	if token, _ := loaded.GetKeyValue("access_token"); token != "synced-token" {
		t.Fatalf("token = %q", token)
	}
}

// Parity: core/framework/credentials/store.py::CredentialStore.with_aden_sync
func TestWithAdenSyncConfiguredAndUnconfigured(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)

	local, err := WithAdenSync(t.TempDir(), nil, 0, false)
	if err != nil {
		t.Fatalf("WithAdenSync unconfigured: %v", err)
	}
	if _, ok := local.storage.(*EncryptedFileStorage); !ok {
		t.Fatalf("unconfigured storage = %T, want encrypted local", local.storage)
	}
	if local.GetProvider("aden_sync") != nil {
		t.Fatal("unconfigured store registered an Aden provider")
	}
	if local.GetProvider("static") == nil {
		t.Fatal("unconfigured store lost the default StaticProvider")
	}

	// An explicit ADEN_API_KEY alone is enough to build the Aden backend; no
	// network happens because autoSync is false.
	t.Setenv(AdenEnvVar, "aden-key")
	envConfigured, err := WithAdenSync(t.TempDir(), nil, 0, false)
	if err != nil {
		t.Fatalf("WithAdenSync env-configured: %v", err)
	}
	if _, ok := envConfigured.storage.(*AdenCachedStorage); !ok {
		t.Fatalf("env-configured storage = %T, want AdenCachedStorage", envConfigured.storage)
	}
	if envConfigured.GetProvider("aden_sync") == nil {
		t.Fatal("env-configured store has no Aden provider")
	}

	client := &fakeAdenClient{
		integrations: []AdenIntegrationInfo{
			{IntegrationID: "hash-google", Provider: "google", Alias: "default", Status: "active"},
		},
		responses: map[string]*AdenCredentialResponse{
			"hash-google": adenTestResponse("hash-google", "configured-token"),
		},
	}
	configured, err := WithAdenSync(t.TempDir(), client, 120, true)
	if err != nil {
		t.Fatalf("WithAdenSync configured: %v", err)
	}
	if _, ok := configured.storage.(*AdenCachedStorage); !ok {
		t.Fatalf("configured storage = %T, want AdenCachedStorage", configured.storage)
	}
	if provider := configured.GetProvider("aden_sync"); provider == nil {
		t.Fatal("configured store has no Aden provider")
	} else if _, ok := provider.(*AdenSyncProvider); !ok {
		t.Fatalf("provider = %T", provider)
	}
	credential, err := configured.GetCredential("hash-google", false)
	if err != nil || credential == nil {
		t.Fatalf("initial sync credential = %#v, %v", credential, err)
	}
	if token, _ := credential.GetKeyValue("access_token"); token != "configured-token" {
		t.Fatalf("token = %q", token)
	}
}

// Parity: core/framework/credentials/store.py::CredentialStore.enable_aden_sync
func TestEnableAdenSyncUpgradesStoreInPlace(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	client := &fakeAdenClient{
		integrations: []AdenIntegrationInfo{
			{IntegrationID: "hash-google", Provider: "google", Alias: "default", Status: "active"},
		},
		responses: map[string]*AdenCredentialResponse{
			"hash-google": adenTestResponse("hash-google", "upgraded-token"),
		},
	}
	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))

	synced, err := store.EnableAdenSync(client, 0)
	if err != nil || synced != 1 {
		t.Fatalf("EnableAdenSync = %d, %v", synced, err)
	}
	if _, ok := store.storage.(*AdenCachedStorage); !ok {
		t.Fatalf("storage = %T, want AdenCachedStorage", store.storage)
	}
	if store.GetProvider("aden_sync") == nil {
		t.Fatal("Aden provider not registered")
	}
	if credential, err := store.GetCredential("hash-google", false); err != nil || credential == nil {
		t.Fatalf("credential = %#v, %v", credential, err)
	}

	// Safe to call repeatedly; it converges rather than nesting caches.
	if synced, err := store.EnableAdenSync(client, 0); err != nil || synced != 1 {
		t.Fatalf("second EnableAdenSync = %d, %v", synced, err)
	}
	if cached, ok := store.storage.(*AdenCachedStorage); !ok {
		t.Fatalf("storage = %T", store.storage)
	} else if _, nested := cached.local.(*AdenCachedStorage); nested {
		t.Fatal("EnableAdenSync nested two cached storages")
	}
}

// Parity: core/framework/credentials/store.py::CredentialStore.enable_aden_sync
func TestEnableAdenSyncWithoutKeyIsNoop(t *testing.T) {
	t.Setenv(AdenEnvVar, "")
	store := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)))
	before := store.storage

	synced, err := store.EnableAdenSync(nil, 0)
	if err != nil || synced != 0 {
		t.Fatalf("EnableAdenSync = %d, %v", synced, err)
	}
	if store.storage != before {
		t.Fatalf("storage changed to %T without a key", store.storage)
	}
}
