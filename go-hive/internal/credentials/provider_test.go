package credentials

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Parity: core/framework/credentials/tests/test_credential_store.py::TestStaticProvider (id, types, refresh unchanged, validate, no refresh).
func TestStaticProvider(t *testing.T) {
	provider := StaticProvider{}
	if provider.ProviderID() != "static" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}
	types := provider.SupportedTypes()
	if !supports(types, CredentialTypeAPIKey) || !supports(types, CredentialTypeCustom) {
		t.Fatalf("types = %v", types)
	}

	cred := &CredentialObject{ID: "test", Keys: NewKeyMap(NewCredentialKey("k", "v"))}
	refreshed, err := provider.Refresh(cred)
	if err != nil || refreshed.GetKey("k") == nil {
		t.Fatalf("refresh = %+v %v", refreshed, err)
	}
	if !provider.Validate(cred) {
		t.Fatal("credential with non-empty value rejected")
	}
	if provider.Validate(&CredentialObject{ID: "test"}) {
		t.Fatal("credential without keys accepted")
	}
	if provider.Validate(&CredentialObject{ID: "test", Keys: NewKeyMap(NewCredentialKey("k", "   "))}) {
		t.Fatal("blank key accepted")
	}
	if provider.ShouldRefresh(cred) {
		t.Fatal("static provider requested a refresh")
	}
	if provider.CanHandle(&CredentialObject{CredentialType: CredentialTypeBearerToken}) {
		t.Fatal("static provider handled a bearer token")
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::test_refresh_returns_unchanged,
func TestBearerTokenProvider(t *testing.T) {
	provider := BearerTokenProvider{}
	if provider.ProviderID() != "bearer_token" {
		t.Fatalf("provider id = %q", provider.ProviderID())
	}

	_, err := provider.Refresh(NewCredentialObject("bearer"))
	var refreshErr *CredentialRefreshError
	if !errors.As(err, &refreshErr) {
		t.Fatalf("refresh error = %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "cannot be refreshed") {
		t.Fatalf("message = %q", err)
	}

	future := time.Now().UTC().Add(time.Hour)
	valid := &CredentialObject{
		ID:   "bearer",
		Keys: NewKeyMap(&CredentialKey{Name: "access_token", Value: "t", ExpiresAt: &future}),
	}
	if !provider.Validate(valid) {
		t.Fatal("unexpired token rejected")
	}
	if provider.ShouldRefresh(valid) {
		t.Fatal("unexpired token flagged for refresh")
	}

	past := time.Now().UTC().Add(-time.Hour)
	expired := &CredentialObject{
		ID:   "bearer",
		Keys: NewKeyMap(&CredentialKey{Name: "token", Value: "t", ExpiresAt: &past}),
	}
	if provider.Validate(expired) {
		t.Fatal("expired token accepted")
	}
	if !provider.ShouldRefresh(expired) {
		t.Fatal("expired token not flagged for refresh")
	}

	if provider.Validate(NewCredentialObject("bearer")) {
		t.Fatal("token-less credential accepted")
	}
}
