package credentials

import (
	"testing"
)

// oauthAliasAccount builds an OAuth-managed account shaped like the ones the
// credential sync stores: an access token plus the provider/alias index keys.
func oauthAliasAccount(id, provider, alias, token string) *CredentialObject {
	cred := NewCredentialObject(id)
	cred.CredentialType = CredentialTypeOAuth2
	cred.Keys.Set(NewCredentialKey("access_token", token))
	cred.Keys.Set(NewCredentialKey("_alias", alias))
	cred.Keys.Set(NewCredentialKey("_integration_type", provider))
	return cred
}

// Parity-exempt: upstream AdenCachedStorage.load_by_alias /
// load_all_for_provider (core/framework/credentials/aden/storage.py) serve the
// provider + alias index; the Python suite never runs it against the encrypted
// file store, only against the in-memory fake.
func TestEncryptedFileStorageAliasIndex(t *testing.T) {
	storage := newEncryptedStorage(t)
	accounts := []*CredentialObject{
		oauthAliasAccount("google_work", "google", "work", "at-work"),
		oauthAliasAccount("google_personal", "google", "personal", "at-personal"),
		oauthAliasAccount("hubspot_default", "hubspot", "default", "hubspot-key"),
	}
	for _, cred := range accounts {
		if err := storage.Save(cred); err != nil {
			t.Fatalf("save %s: %v", cred.ID, err)
		}
	}

	if _, ok := any(storage).(ProviderAccountStorage); !ok {
		t.Fatal("EncryptedFileStorage does not implement ProviderAccountStorage")
	}
	if _, ok := any(storage).(AliasStorage); !ok {
		t.Fatal("EncryptedFileStorage does not implement AliasStorage")
	}

	found, err := storage.LoadByAlias("google", "work")
	if err != nil || found == nil || found.ID != "google_work" {
		t.Fatalf("load_by_alias(google, work) = %+v, %v", found, err)
	}
	if token, _ := found.GetKeyValue("access_token"); token != "at-work" {
		t.Fatalf("decrypted token = %q", token)
	}
	if other, err := storage.LoadByAlias("google", "personal"); err != nil || other == nil || other.ID != "google_personal" {
		t.Fatalf("load_by_alias(google, personal) = %+v, %v", other, err)
	}
	if wrongProvider, err := storage.LoadByAlias("hubspot", "work"); err != nil || wrongProvider != nil {
		t.Fatalf("cross-provider alias = %+v, %v", wrongProvider, err)
	}
	if missing, err := storage.LoadByAlias("google", "nope"); err != nil || missing != nil {
		t.Fatalf("missing alias = %+v, %v (must degrade to nil so callers fall back)", missing, err)
	}

	all, err := storage.LoadAllForProvider("google")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("google accounts = %d", len(all))
	}
	if all[0].ID != "google_personal" || all[1].ID != "google_work" {
		t.Fatalf("google account order = %s, %s", all[0].ID, all[1].ID)
	}
	if none, err := storage.LoadAllForProvider("nope"); err != nil || len(none) != 0 {
		t.Fatalf("unknown provider = %v, %v", none, err)
	}
}

// Parity-exempt: store.get_credential_by_alias / list_accounts against the
// encrypted store; the Python tests only cover the in-memory fake.
func TestCredentialStoreAliasIndexThroughEncryptedStorage(t *testing.T) {
	storage := newEncryptedStorage(t)
	for _, cred := range []*CredentialObject{
		oauthAliasAccount("google_work", "google", "work", "at-work"),
		oauthAliasAccount("google_personal", "google", "personal", "at-personal"),
	} {
		if err := storage.Save(cred); err != nil {
			t.Fatalf("save %s: %v", cred.ID, err)
		}
	}
	store := NewCredentialStore(WithStorage(storage), WithAutoRefresh(false))

	found, err := store.GetCredentialByAlias("google", "work")
	if err != nil || found == nil || found.ID != "google_work" {
		t.Fatalf("GetCredentialByAlias = %+v, %v", found, err)
	}
	prefixed, err := store.GetCredentialByAlias("google", "google/personal")
	if err != nil || prefixed == nil || prefixed.ID != "google_personal" {
		t.Fatalf("prefixed alias = %+v, %v", prefixed, err)
	}
	if missing, err := store.GetCredentialByAlias("google", "nope"); err != nil || missing != nil {
		t.Fatalf("missing alias = %+v, %v", missing, err)
	}

	accounts, err := store.ListAccounts("google")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 {
		t.Fatalf("accounts = %v", accounts)
	}
	if accounts[0]["credential_id"] != "google_personal" || accounts[0]["provider"] != "google" || accounts[0]["alias"] != "personal" {
		t.Fatalf("account[0] = %v", accounts[0])
	}
}

// Parity-exempt: CredentialStoreAdapter.get_by_alias ordering (store first,
// local registry as the fallback) has no Python test; this pins the fallback
// when the encrypted index carries no record for the pair.
func TestCredentialStoreAdapterFallsBackToLocalRegistry(t *testing.T) {
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)

	localDir := t.TempDir()
	registry, err := LocalCredentialRegistryAtPath(localDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SaveAccount("google", "legacy", "legacy-key", false, nil); err != nil {
		t.Fatal(err)
	}

	// No store records at all: the alias lookup must land on the local registry.
	emptyStore := NewCredentialStore(WithStorage(NewInMemoryStorage(nil)), WithAutoRefresh(false))
	adapter := NewCredentialStoreAdapter(emptyStore, nil, registry)
	token, ok := adapter.AccountToken("google", "legacy")
	if !ok || token != "legacy-key" {
		t.Fatalf("fallback token = %q, %v", token, ok)
	}
	if _, ok := adapter.AccountToken("google", "missing"); ok {
		t.Fatal("unknown alias resolved")
	}

	// A store record for the same pair wins over the local registry copy.
	storeDir := t.TempDir()
	storage, err := NewEncryptedFileStorage(storeDir, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(oauthAliasAccount("google_work", "google", "work", "store-token")); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.SaveAccount("google", "work", "local-token", false, nil); err != nil {
		t.Fatal(err)
	}
	store := NewCredentialStore(WithStorage(storage), WithAutoRefresh(false))
	adapter = NewCredentialStoreAdapter(store, nil, registry)
	token, ok = adapter.AccountToken("google", "work")
	if !ok || token != "store-token" {
		t.Fatalf("store-first token = %q, %v", token, ok)
	}
}
