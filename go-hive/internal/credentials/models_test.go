package credentials

import (
	"encoding/json"
	"testing"
	"time"
)

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCredentialKey
func TestCredentialKeyBasics(t *testing.T) {
	key := NewCredentialKey("api_key", "test-value")
	if key.Name != "api_key" || key.GetSecretValue() != "test-value" {
		t.Fatalf("key = %+v", key)
	}
	if key.ExpiresAt != nil || key.IsExpired() {
		t.Fatalf("fresh key should not expire: %+v", key)
	}

	future := time.Now().UTC().Add(time.Hour)
	key = &CredentialKey{Name: "token", Value: "xxx", ExpiresAt: &future}
	if key.IsExpired() {
		t.Fatal("future key reported expired")
	}

	past := time.Now().UTC().Add(-time.Hour)
	key = &CredentialKey{Name: "token", Value: "xxx", ExpiresAt: &past}
	if !key.IsExpired() {
		t.Fatal("past key not reported expired")
	}

	key = &CredentialKey{Name: "token", Value: "xxx", Metadata: map[string]any{"client_id": "abc", "scope": "read"}}
	if key.Metadata["client_id"] != "abc" {
		t.Fatalf("metadata = %v", key.Metadata)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCredentialObject::test_create_simple_credential,
func TestCredentialObjectKeys(t *testing.T) {
	cred := &CredentialObject{
		ID:             "brave_search",
		CredentialType: CredentialTypeAPIKey,
		Keys:           NewKeyMap(NewCredentialKey("api_key", "test-key")),
	}
	if got := cred.GetKey("api_key"); got == nil || *got != "test-key" {
		t.Fatalf("api_key = %v", got)
	}

	multi := &CredentialObject{
		ID:             "github_oauth",
		CredentialType: CredentialTypeOAuth2,
		Keys: NewKeyMap(
			NewCredentialKey("access_token", "ghp_xxx"),
			NewCredentialKey("refresh_token", "ghr_xxx"),
		),
	}
	if v, _ := multi.GetKeyValue("access_token"); v != "ghp_xxx" {
		t.Fatalf("access_token = %q", v)
	}
	if v, _ := multi.GetKeyValue("refresh_token"); v != "ghr_xxx" {
		t.Fatalf("refresh_token = %q", v)
	}
	if multi.GetKey("nonexistent") != nil {
		t.Fatal("missing key returned a value")
	}

	fresh := NewCredentialObject("test")
	fresh.SetKey("new_key", "new_value", nil, nil)
	if v, _ := fresh.GetKeyValue("new_key"); v != "new_value" {
		t.Fatalf("new_key = %q", v)
	}
	if fresh.UpdatedAt == nil {
		t.Fatal("set_key did not refresh updated_at")
	}

	expires := time.Now().UTC().Add(time.Hour)
	fresh.SetKey("token", "xxx", &expires, nil)
	if got := fresh.Keys.Get("token"); got == nil || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("token key = %+v", got)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCredentialObject::test_needs_refresh, ::test_get_default_key,
func TestCredentialObjectLifecycle(t *testing.T) {
	past := time.Now().UTC().Add(-time.Hour)
	cred := &CredentialObject{
		ID:   "test",
		Keys: NewKeyMap(&CredentialKey{Name: "token", Value: "xxx", ExpiresAt: &past}),
	}
	if !cred.NeedsRefresh() {
		t.Fatal("expired key not reported by needs_refresh")
	}
	if cred.IsValid() {
		t.Fatal("all-expired credential reported valid")
	}

	byAPIKey := &CredentialObject{ID: "test", Keys: NewKeyMap(NewCredentialKey("api_key", "key-value"))}
	if got := byAPIKey.GetDefaultKey(); got == nil || *got != "key-value" {
		t.Fatalf("default(api_key) = %v", got)
	}
	byAccess := &CredentialObject{ID: "test", Keys: NewKeyMap(NewCredentialKey("access_token", "token-value"))}
	if got := byAccess.GetDefaultKey(); got == nil || *got != "token-value" {
		t.Fatalf("default(access_token) = %v", got)
	}
	byValue := &CredentialObject{ID: "test", Keys: NewKeyMap(
		NewCredentialKey("api_key", "second"), NewCredentialKey("value", "first"),
	)}
	if got := byValue.GetDefaultKey(); got == nil || *got != "first" {
		t.Fatalf("default(value) = %v", got)
	}
	byFirst := &CredentialObject{ID: "test", Keys: NewKeyMap(
		NewCredentialKey("custom_a", "a"), NewCredentialKey("custom_b", "b"),
	)}
	if got := byFirst.GetDefaultKey(); got == nil || *got != "a" {
		t.Fatalf("default(first) = %v", got)
	}

	empty := &CredentialObject{ID: "test"}
	if cred := empty.GetDefaultKey(); cred != nil {
		t.Fatalf("empty default = %v", cred)
	}

	usage := NewCredentialObject("test")
	if usage.UseCount != 0 || usage.LastUsed != nil {
		t.Fatalf("fresh usage = %d %v", usage.UseCount, usage.LastUsed)
	}
	usage.RecordUsage()
	if usage.UseCount != 1 || usage.LastUsed == nil {
		t.Fatalf("recorded usage = %d %v", usage.UseCount, usage.LastUsed)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCredentialUsageSpec::test_create_usage_spec
func TestCredentialUsageSpecCreate(t *testing.T) {
	spec := &CredentialUsageSpec{
		CredentialID: "brave_search",
		RequiredKeys: []string{"api_key"},
		Headers:      map[string]string{"X-Subscription-Token": "{{api_key}}"},
	}
	if spec.CredentialID != "brave_search" || len(spec.RequiredKeys) != 1 || spec.Headers["X-Subscription-Token"] != "{{api_key}}" {
		t.Fatalf("spec = %+v", spec)
	}
}

// Parity-exempt: Python's models.py exposes identity/alias/set_identity but no
func TestCredentialIdentityMarkers(t *testing.T) {
	cred := NewCredentialObject("google")
	cred.Keys.Set(NewCredentialKey("_integration_type", "google"))
	cred.Keys.Set(NewCredentialKey("_alias", "work"))
	cred.SetIdentity(map[string]string{"email": "dev@example.com", "username": ""})

	if got := cred.ProviderType(); got == nil || *got != "google" {
		t.Fatalf("provider type = %v", got)
	}
	if got := cred.Alias(); got == nil || *got != "work" {
		t.Fatalf("alias = %v", got)
	}
	identity := cred.Identity()
	if identity.Label() != "dev@example.com" || !identity.IsKnown() {
		t.Fatalf("identity = %+v label=%q", identity, identity.Label())
	}
	dict := identity.ToDict()
	if dict["email"] != "dev@example.com" || len(dict) != 1 {
		t.Fatalf("identity dict = %v", dict)
	}
	if cred.HasKey("_identity_username") {
		t.Fatal("empty identity field was persisted")
	}
}

// Parity-exempt: Go-only JSON ordering guarantee; Python relies on dict insertion
func TestKeyMapJSONPreservesOrder(t *testing.T) {
	original := &CredentialObject{
		ID:             "ordered",
		CredentialType: CredentialTypeCustom,
		Keys: NewKeyMap(
			NewCredentialKey("first", "1"),
			NewCredentialKey("second", "2"),
			NewCredentialKey("third", "3"),
		),
	}
	original.Keys.Set(NewCredentialKey("second", "2b"))

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded CredentialObject
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	names := decoded.Keys.Names()
	if len(names) != 3 || names[0] != "first" || names[1] != "second" || names[2] != "third" {
		t.Fatalf("order drifted: %v", names)
	}
	if got := decoded.GetDefaultKey(); got == nil || *got != "1" {
		t.Fatalf("default after round-trip = %v", got)
	}
	if v, _ := decoded.GetKeyValue("second"); v != "2b" {
		t.Fatalf("replaced value = %q", v)
	}
}
