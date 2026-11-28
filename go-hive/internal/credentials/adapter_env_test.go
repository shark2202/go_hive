package credentials

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default
//
// The default adapter must use CompositeStorage(encrypted primary, env fallback)
// so a credential that exists only in the environment resolves instead of
// returning not_found.

import (
	"testing"
)

func newDefaultAdapterHome(t *testing.T) {
	t.Helper()
	t.Setenv("HIVE_HOME", t.TempDir())
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)
}

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default — encrypted primary + EnvVarStorage fallback.
func TestDefaultAdapterResolvesEnvOnlyCredential(t *testing.T) {
	newDefaultAdapterHome(t)
	t.Setenv("BRAVE_SEARCH_API_KEY", "env-only-key")

	adapter, err := DefaultAdapter(nil)
	if err != nil {
		t.Fatalf("DefaultAdapter: %v", err)
	}
	value, err := adapter.Store().Get("brave_search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value == nil || *value != "env-only-key" {
		t.Fatalf("env-only value = %v, want env-only-key", value)
	}
}

// Parity: core/framework/credentials/storage.py::CompositeStorage.load — the encrypted store wins when both backends hold the credential.
func TestDefaultAdapterEncryptedStoreBeatsEnvironment(t *testing.T) {
	newDefaultAdapterHome(t)
	t.Setenv("BRAVE_SEARCH_API_KEY", "env-only-key")

	adapter, err := DefaultAdapter(nil)
	if err != nil {
		t.Fatalf("DefaultAdapter: %v", err)
	}
	credential := NewCredentialObject("brave_search")
	credential.Keys.Set(NewCredentialKey("api_key", "encrypted-key"))
	if err := adapter.Store().SaveCredential(credential); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}

	value, err := adapter.Store().Get("brave_search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value == nil || *value != "encrypted-key" {
		t.Fatalf("value = %v, want the encrypted primary", value)
	}
}

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default — env mapping is keyed by credential name; the Go CLI also addresses credentials by CredentialID (cloudflare -> cloudflare_api_token).
func TestDefaultAdapterEnvMappingCoversCredentialID(t *testing.T) {
	newDefaultAdapterHome(t)
	t.Setenv("CLOUDFLARE_API_TOKEN", "cf-env-token")

	adapter, err := DefaultAdapter(nil)
	if err != nil {
		t.Fatalf("DefaultAdapter: %v", err)
	}
	for _, credentialID := range []string{"cloudflare", "cloudflare_api_token"} {
		value, err := adapter.Store().Get(credentialID)
		if err != nil {
			t.Fatalf("Get(%s): %v", credentialID, err)
		}
		if value == nil || *value != "cf-env-token" {
			t.Fatalf("Get(%s) = %v, want cf-env-token", credentialID, value)
		}
	}
}

// TestDefaultAdapterAtResolvesEnvOnlyCredential pins the explicit-home form
// used by session tools and the credential routes: an adapter rooted at a
// caller-supplied credentials directory must still fall back to the env var.
// Parity-exempt: DefaultAdapterAt is the Go-only explicit-home constructor used
// by session tools and credential routes; upstream resolves the single ambient
// home instead.
func TestDefaultAdapterAtResolvesEnvOnlyCredential(t *testing.T) {
	basePath := t.TempDir()
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)
	t.Setenv("BRAVE_SEARCH_API_KEY", "env-only-key")

	adapter, err := DefaultAdapterAt(basePath, CatalogNamedSpecs())
	if err != nil {
		t.Fatalf("DefaultAdapterAt: %v", err)
	}
	value, err := adapter.Store().Get("brave_search")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value == nil || *value != "env-only-key" {
		t.Fatalf("env-only value = %v, want env-only-key", value)
	}
}
