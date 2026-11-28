package credentials

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/store.py::CredentialStore.get_provider_for_credential
// An explicit provider_id wins over registration order; without one the first
// registered provider that supports the credential type is used.
func TestOAuth2ProviderSelection(t *testing.T) {
	hubSpot := NewHubSpotOAuth2Provider("client-1", "secret-1", nil)
	zoho := NewZohoOAuth2Provider("client-1", "secret-1", ZohoOAuth2Options{})
	store := NewCredentialStore(
		WithStorage(NewInMemoryStorage(nil)),
		WithProviders(hubSpot, zoho),
	)

	explicit := &CredentialObject{ID: "zoho-cred", CredentialType: CredentialTypeOAuth2, ProviderID: "zoho_crm_oauth2"}
	if got := store.GetProviderForCredential(explicit); got != zoho {
		t.Fatalf("explicit provider_id selected %T, want Zoho", got)
	}

	generic := &CredentialObject{ID: "generic-cred", CredentialType: CredentialTypeOAuth2}
	if got := store.GetProviderForCredential(generic); got != hubSpot {
		t.Fatalf("type fallback selected %T, want the first registered OAuth2 provider", got)
	}

	unknownID := &CredentialObject{ID: "unknown-cred", CredentialType: CredentialTypeOAuth2, ProviderID: "missing"}
	if got := store.GetProviderForCredential(unknownID); got != hubSpot {
		t.Fatalf("unknown provider_id fallback selected %T, want the type fallback", got)
	}

	apiKey := &CredentialObject{ID: "api-key-cred", CredentialType: CredentialTypeAPIKey}
	if got := store.GetProviderForCredential(apiKey); got != nil {
		t.Fatalf("api_key credential selected %T, want no OAuth2 provider", got)
	}
}

// Parity-exempt: Go-only production wiring for one-shot CLI processes; the
// upstream default adapter registers AdenSyncProvider and syncs before use.
func TestOAuth2SuccessfulRefreshThroughProductionAdapter(t *testing.T) {
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	client := &fakeAdenClient{
		integrations: []AdenIntegrationInfo{
			{IntegrationID: "hash-google", Provider: "google", Alias: "work", Status: "active"},
		},
		responses: map[string]*AdenCredentialResponse{
			"hash-google": {
				IntegrationID: "hash-google",
				AccessToken:   "stale-aden",
				TokenType:     "Bearer",
				ExpiresAt:     &past,
				Provider:      "google",
				Alias:         "work",
			},
		},
		refreshResult: &AdenCredentialResponse{
			IntegrationID: "hash-google",
			AccessToken:   "fresh-aden",
			TokenType:     "Bearer",
			ExpiresAt:     &future,
			Provider:      "google",
			Alias:         "work",
		},
	}

	adapter, err := defaultAdapterWithAdenClient(t.TempDir(), CatalogNamedSpecs(), client)
	if err != nil {
		t.Fatalf("defaultAdapterWithAdenClient: %v", err)
	}
	store := adapter.Store()
	if store.GetProvider("aden_sync") == nil {
		t.Fatal("production adapter did not register the Aden OAuth2 provider")
	}
	if got := store.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2, ProviderID: "aden_sync"}); got == nil {
		t.Fatal("explicit aden_sync credential did not select a provider")
	}
	// AdenSyncProvider claims only credentials explicitly linked to it (or
	// carrying _aden_managed); a bare OAuth2 credential stays unclaimed.
	if got := store.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2}); got != nil {
		t.Fatalf("bare oauth2 credential claimed by %T, want no provider", got)
	}

	value, err := store.GetKeyEx("google", "access_token", true)
	if err != nil || value == nil || *value != "fresh-aden" {
		t.Fatalf("access_token = %v, err = %v, want fresh-aden", value, err)
	}
	if client.refreshCalls != 1 {
		t.Fatalf("Aden refresh calls = %d, want 1", client.refreshCalls)
	}
}

// Parity-exempt: the no-provider negative is the boundary upstream leaves
// silent; Go must never present a stale OAuth2 token as refreshed when the
// caller asked for a reauth signal.
func TestOAuth2NilProviderNeverPassesAsRefreshed(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	newCredential := func() *CredentialObject {
		credential := &CredentialObject{
			ID:             "github_oauth",
			CredentialType: CredentialTypeOAuth2,
			AutoRefresh:    true,
			Keys: NewKeyMap(
				&CredentialKey{Name: "access_token", Value: "stale-access", ExpiresAt: &past},
				NewCredentialKey("refresh_token", "refresh-1"),
				NewCredentialKey("_alias", "work"),
				NewCredentialKey("_integration_type", "github"),
			),
		}
		return credential
	}
	storage := NewInMemoryStorage(map[string]*CredentialObject{"github_oauth": newCredential()})
	store := NewCredentialStore(WithStorage(storage))

	// Best-effort reads keep upstream's stale fallback, but nothing may be
	// marked refreshed: last_refreshed stays unset and the token is unchanged.
	credential, err := store.GetCredential("github_oauth", true)
	if err != nil || credential == nil {
		t.Fatalf("best-effort read = %+v, err = %v", credential, err)
	}
	if credential.LastRefreshed != nil {
		t.Fatal("stale OAuth2 credential was marked refreshed without a provider")
	}
	if value, _ := credential.GetKeyValue("access_token"); value != "stale-access" {
		t.Fatalf("stale access_token = %q, want stale-access", value)
	}

	// The reauth path (hive-cred token / tool execution) must raise instead of
	// handing back a value that looks refreshed.
	store.ClearCache()
	credential, err = store.GetCredentialEx("github_oauth", true, true)
	if credential != nil {
		t.Fatalf("reauth read returned credential %+v, want nil", credential)
	}
	var expired *CredentialExpiredError
	if !errors.As(err, &expired) {
		t.Fatalf("error = %T %v, want CredentialExpiredError", err, err)
	}
	if expired.CredentialID != "github_oauth" || expired.Provider != "github" || expired.Alias != "work" {
		t.Fatalf("expired metadata = %+v", expired)
	}
	if !strings.Contains(err.Error(), "no credential provider is registered") ||
		!strings.Contains(err.Error(), "Reauthorization required.") {
		t.Fatalf("message = %q", err.Error())
	}
}

// Parity-exempt: DefaultAdapterAt is the Go-only explicit-home constructor.
// It must keep the local store OAuth2-provider-free: upstream's default store
// is StaticProvider-only and OAuth2 providers are explicit registrations.
func TestDefaultAdapterAtLeavesOAuth2OptIn(t *testing.T) {
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)
	t.Setenv(AdenEnvVar, "")

	adapter, err := DefaultAdapterAt(t.TempDir(), CatalogNamedSpecs())
	if err != nil {
		t.Fatalf("DefaultAdapterAt: %v", err)
	}
	store := adapter.Store()
	if store == nil {
		t.Fatal("DefaultAdapterAt returned no store")
	}
	if got := store.GetProvider("static"); got == nil {
		t.Fatal("default store lost StaticProvider")
	}
	if got := store.GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2}); got != nil {
		t.Fatalf("default store auto-registered %T for oauth2", got)
	}

	// The CLI constructor must take the same local path when Aden is not
	// configured: no key means no default OAuth2 provider.
	cliAdapter, err := DefaultAdapterWithAden(t.TempDir(), CatalogNamedSpecs())
	if err != nil {
		t.Fatalf("DefaultAdapterWithAden: %v", err)
	}
	if got := cliAdapter.Store().GetProviderForCredential(&CredentialObject{CredentialType: CredentialTypeOAuth2}); got != nil {
		t.Fatalf("unconfigured CLI adapter auto-registered %T for oauth2", got)
	}
}
