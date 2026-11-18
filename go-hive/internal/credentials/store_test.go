package credentials

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCredentialStore::test_for_testing_factory, ::test_get_credential,
func TestCredentialStoreBasics(t *testing.T) {
	store := ForTesting(map[string]map[string]string{"test": {"api_key": "value"}})

	if value, err := store.Get("test"); err != nil || value == nil || *value != "value" {
		t.Fatalf("get = %v %v", value, err)
	}
	if value, err := store.GetKey("test", "api_key"); err != nil || value == nil || *value != "value" {
		t.Fatalf("get_key = %v %v", value, err)
	}
	credential, err := store.GetCredential("test", true)
	if err != nil || credential == nil {
		t.Fatalf("get_credential = %+v %v", credential, err)
	}
	if v, _ := credential.GetKeyValue("api_key"); v != "value" {
		t.Fatalf("credential key = %q", v)
	}

	empty := ForTesting(nil)
	if credential, err := empty.GetCredential("nonexistent", true); err != nil || credential != nil {
		t.Fatalf("nonexistent credential = %+v %v", credential, err)
	}
	if value, err := empty.Get("nonexistent"); err != nil || value != nil {
		t.Fatalf("nonexistent get = %v %v", value, err)
	}

	cred := NewCredentialObject("new")
	cred.Keys.Set(NewCredentialKey("k", "v"))
	if err := empty.SaveCredential(cred); err != nil {
		t.Fatal(err)
	}
	loaded, err := empty.GetCredential("new", true)
	if err != nil || loaded == nil {
		t.Fatalf("save/load = %+v %v", loaded, err)
	}
	if v, _ := loaded.GetKeyValue("k"); v != "v" {
		t.Fatalf("loaded key = %q", v)
	}

	store = ForTesting(map[string]map[string]string{"test": {"k": "v"}})
	if deleted, err := store.DeleteCredential("test"); err != nil || !deleted {
		t.Fatalf("delete = %v %v", deleted, err)
	}
	if credential, _ := store.GetCredential("test", true); credential != nil {
		t.Fatal("deleted credential still loads")
	}

	store = ForTesting(map[string]map[string]string{"a": {"k": "v"}, "b": {"k": "v"}})
	ids, err := store.ListCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if !sliceContains(ids, "a") || !sliceContains(ids, "b") {
		t.Fatalf("ids = %v", ids)
	}

	store = ForTesting(map[string]map[string]string{"test": {"k": "v"}})
	if !store.IsAvailable("test") || store.IsAvailable("nonexistent") {
		t.Fatal("is_available mismatch")
	}
	if !store.Exists("test") || store.Exists("nonexistent") {
		t.Fatal("exists mismatch")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_resolve_templates, ::test_resolve_headers,
func TestCredentialStoreUsageAndTemplates(t *testing.T) {
	store := ForTesting(map[string]map[string]string{"test": {"api_key": "value"}})
	resolved, err := store.Resolve("Key: {{test.api_key}}")
	if err != nil || resolved != "Key: value" {
		t.Fatalf("resolve = %q %v", resolved, err)
	}

	headerStore := ForTesting(map[string]map[string]string{"test": {"token": "xxx"}})
	headers, err := headerStore.ResolveHeaders(map[string]string{"Authorization": "Bearer {{test.token}}"})
	if err != nil {
		t.Fatal(err)
	}
	if headers["Authorization"] != "Bearer xxx" {
		t.Fatalf("headers = %v", headers)
	}

	provider := &StaticProvider{}
	store.RegisterProvider(provider)
	if got := store.GetProvider("static"); got != provider {
		t.Fatalf("provider identity drifted: %v", got)
	}

	spec := &CredentialUsageSpec{
		CredentialID: "test",
		RequiredKeys: []string{"api_key"},
		Headers:      map[string]string{"X-Key": "{{test.api_key}}"},
	}
	store.RegisterUsage(spec)
	if got := store.GetUsageSpec("test"); got != spec {
		t.Fatalf("usage spec identity drifted: %v", got)
	}

	if problems, err := store.ValidateForUsage("test"); err != nil || len(problems) != 0 {
		t.Fatalf("validate = %v %v", problems, err)
	}

	missing := ForTesting(map[string]map[string]string{"test": {"other_key": "value"}})
	missing.RegisterUsage(&CredentialUsageSpec{CredentialID: "test", RequiredKeys: []string{"api_key"}})
	problems, err := missing.ValidateForUsage("test")
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "api_key") {
		t.Fatalf("problems = %v", problems)
	}
	if problems, _ := missing.ValidateForUsage("unknown"); len(problems) != 0 {
		t.Fatalf("unregistered spec problems = %v", problems)
	}

	all, err := missing.ValidateAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all["test"]) != 1 {
		t.Fatalf("validate_all = %v", all)
	}

	if ok, err := store.ValidateCredential("test"); err != nil || !ok {
		t.Fatalf("validate_credential = %v %v", ok, err)
	}
	if ok, _ := store.ValidateCredential("missing"); ok {
		t.Fatal("missing credential validated")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_caching, ::test_clear_cache.
func TestCredentialStoreCache(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	store := NewCredentialStore(WithStorage(storage), WithCacheTTL(60*time.Second))

	cred := NewCredentialObject("test")
	cred.Keys.Set(NewCredentialKey("k", "v"))
	storage.Save(cred)

	if _, err := store.GetCredential("test", true); err != nil {
		t.Fatal(err)
	}
	storage.Delete("test")
	cached, err := store.GetCredential("test", true)
	if err != nil || cached == nil {
		t.Fatalf("cache miss after storage delete: %+v %v", cached, err)
	}

	storage2 := NewInMemoryStorage(nil)
	store2 := NewCredentialStore(WithStorage(storage2))
	cred2 := NewCredentialObject("test")
	storage2.Save(cred2)
	if _, err := store2.GetCredential("test", true); err != nil {
		t.Fatal(err)
	}
	storage2.Delete("test")
	store2.ClearCache()
	if credential, err := store2.GetCredential("test", true); err != nil || credential != nil {
		t.Fatalf("clear_cache left a stale entry: %+v %v", credential, err)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_caching
func TestCredentialStoreCacheTTLExpiry(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	store := NewCredentialStore(WithStorage(storage), WithCacheTTL(time.Nanosecond))
	storage.Save(NewCredentialObject("test"))
	if _, err := store.GetCredential("test", true); err != nil {
		t.Fatal(err)
	}
	storage.Delete("test")
	time.Sleep(2 * time.Millisecond)
	if credential, err := store.GetCredential("test", true); err != nil || credential != nil {
		t.Fatalf("expired cache entry served: %+v %v", credential, err)
	}
}

// Parity-exempt: resolve_for_usage has no Python test in test_credential_store.py.
func TestCredentialStoreResolveForUsage(t *testing.T) {
	store := ForTesting(map[string]map[string]string{"test": {"api_key": "value"}})
	store.RegisterUsage(&CredentialUsageSpec{
		CredentialID: "test",
		RequiredKeys: []string{"api_key"},
		Headers:      map[string]string{"X-Key": "{{test.api_key}}"},
		QueryParams:  map[string]string{"key": "{{test.api_key}}"},
		BodyFields:   map[string]string{"token": "{{test.api_key}}"},
	})

	kwargs, err := store.ResolveForUsage("test")
	if err != nil {
		t.Fatal(err)
	}
	if kwargs["headers"].(map[string]string)["X-Key"] != "value" {
		t.Fatalf("headers = %v", kwargs["headers"])
	}
	if kwargs["params"].(map[string]string)["key"] != "value" {
		t.Fatalf("params = %v", kwargs["params"])
	}
	if kwargs["data"].(map[string]string)["token"] != "value" {
		t.Fatalf("data = %v", kwargs["data"])
	}

	if _, err := store.ResolveForUsage("unknown"); err == nil || !strings.Contains(err.Error(), "No usage spec registered for 'unknown'") {
		t.Fatalf("missing spec error = %v", err)
	}
}

// Parity-exempt: reauth-ready CredentialExpiredError on refresh failure has no
func TestCredentialStoreRefreshFailure(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	newExpired := func() *CredentialObject {
		cred := NewCredentialObject("bearer")
		cred.CredentialType = CredentialTypeBearerToken
		cred.AutoRefresh = true
		cred.Keys.Set(&CredentialKey{Name: "access_token", Value: "old", ExpiresAt: &past})
		cred.Keys.Set(NewCredentialKey("_alias", "work"))
		cred.Keys.Set(NewCredentialKey("_integration_type", "auth0"))
		return cred
	}

	storage := NewInMemoryStorage(map[string]*CredentialObject{"bearer": newExpired()})
	store := NewCredentialStore(WithStorage(storage), WithProviders(BearerTokenProvider{}))

	_, err := store.GetCredentialEx("bearer", true, true)
	var expired *CredentialExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("error = %T %v", err, err)
	}
	if expired.CredentialID != "bearer" || expired.Provider != "auth0" || expired.Alias != "work" {
		t.Fatalf("expired metadata = %+v", expired)
	}
	if !strings.Contains(err.Error(), "Reauthorization required.") {
		t.Fatalf("message = %q", err)
	}

	storage = NewInMemoryStorage(map[string]*CredentialObject{"bearer": newExpired()})
	store = NewCredentialStore(WithStorage(storage), WithProviders(BearerTokenProvider{}))
	credential, err := store.GetCredentialEx("bearer", true, false)
	if err != nil || credential == nil {
		t.Fatalf("best-effort refresh = %+v %v", credential, err)
	}
	if v, _ := credential.GetKeyValue("access_token"); v != "old" {
		t.Fatalf("stale token = %q", v)
	}
}

// Parity-exempt: persisting refreshed credentials has no Python test.
func TestCredentialStoreRefreshPersistsNewToken(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	store := NewCredentialStore(WithStorage(storage), WithProviders(StaticProvider{}))

	past := time.Now().UTC().Add(-time.Hour)
	cred := NewCredentialObject("refreshable")
	cred.AutoRefresh = true
	cred.CredentialType = CredentialTypeAPIKey
	cred.Keys.Set(&CredentialKey{Name: "api_key", Value: "old", ExpiresAt: &past})
	storage.Save(cred)

	refreshed, err := store.RefreshCredential("refreshable")
	if err != nil || refreshed == nil {
		t.Fatalf("refresh = %+v %v", refreshed, err)
	}
	if refreshed.LastRefreshed == nil {
		t.Fatal("last_refreshed not set")
	}
	stored, _ := storage.Load("refreshable")
	if stored == nil || stored.LastRefreshed == nil {
		t.Fatalf("refresh not persisted: %+v", stored)
	}
}

type providerStorage struct {
	*InMemoryStorage
	creds []*CredentialObject
}

func (s *providerStorage) LoadAllForProvider(string) ([]*CredentialObject, error) {
	return s.creds, nil
}

func (s *providerStorage) LoadByAlias(provider, alias string) (*CredentialObject, error) {
	for _, cred := range s.creds {
		if p := cred.ProviderType(); p != nil && *p == provider {
			if a := cred.Alias(); a != nil && *a == alias {
				return cred, nil
			}
		}
	}
	return nil, nil
}

// Parity: core/tests/test_hive_cred_cli.py::test_browse_lists_accounts_but_never_a_value
func TestCredentialStoreAccounts(t *testing.T) {
	work := NewCredentialObject("google_work")
	work.Keys.Set(NewCredentialKey("_integration_type", "google"))
	work.Keys.Set(NewCredentialKey("_alias", "work"))
	work.SetIdentity(map[string]string{"email": "work@example.com"})

	personal := NewCredentialObject("google_personal")
	personal.Keys.Set(NewCredentialKey("_integration_type", "google"))
	personal.Keys.Set(NewCredentialKey("_alias", "personal"))
	personal.SetIdentity(map[string]string{"email": "me@example.com"})

	storage := &providerStorage{
		InMemoryStorage: NewInMemoryStorage(map[string]*CredentialObject{
			"google_work":     work,
			"google_personal": personal,
		}),
		creds: []*CredentialObject{work, personal},
	}
	store := NewCredentialStore(WithStorage(storage), WithAutoRefresh(false))

	accounts, err := store.ListAccounts("google")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %v", accounts)
	}
	if accounts[0]["credential_id"] != "google_work" || accounts[0]["provider"] != "google" || accounts[0]["alias"] != "work" {
		t.Fatalf("account[0] = %v", accounts[0])
	}
	identity := accounts[0]["identity"].(map[string]any)
	if identity["email"] != "work@example.com" {
		t.Fatalf("identity = %v", identity)
	}

	found, err := store.GetCredentialByAlias("google", "work")
	if err != nil || found == nil || found.ID != "google_work" {
		t.Fatalf("alias lookup = %+v %v", found, err)
	}
	prefixed, err := store.GetCredentialByAlias("google", "google/personal")
	if err != nil || prefixed == nil || prefixed.ID != "google_personal" {
		t.Fatalf("prefixed alias lookup = %+v %v", prefixed, err)
	}
	if missing, err := store.GetCredentialByAlias("google", "nope"); err != nil || missing != nil {
		t.Fatalf("missing alias = %+v %v", missing, err)
	}
	if byIdentity, err := store.GetCredentialByIdentity("google", "personal"); err != nil || byIdentity == nil {
		t.Fatalf("identity alias = %+v %v", byIdentity, err)
	}
}

// Parity-exempt: default provider registration has no Python pytest counterpart.
func TestCredentialStoreDefaultProviders(t *testing.T) {
	store := NewCredentialStore()
	if store.GetProvider("static") == nil {
		t.Fatal("default StaticProvider missing")
	}
}
