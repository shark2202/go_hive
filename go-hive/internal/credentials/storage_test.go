package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

func newEncryptedStorage(t *testing.T) *EncryptedFileStorage {
	t.Helper()
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, key)
	storage, err := NewEncryptedFileStorage(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	return storage
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestInMemoryStorage (save/load, load missing, delete twice, list,
func TestInMemoryStorage(t *testing.T) {
	storage := NewInMemoryStorage(nil)
	cred := NewCredentialObject("test")
	cred.Keys.Set(NewCredentialKey("key", "value"))

	if err := storage.Save(cred); err != nil {
		t.Fatal(err)
	}
	loaded, err := storage.Load("test")
	if err != nil || loaded == nil {
		t.Fatalf("load = %+v %v", loaded, err)
	}
	if v, _ := loaded.GetKeyValue("key"); v != "value" {
		t.Fatalf("key = %q", v)
	}

	if missing, err := storage.Load("nonexistent"); err != nil || missing != nil {
		t.Fatalf("missing load = %+v %v", missing, err)
	}

	deleted, err := storage.Delete("test")
	if err != nil || !deleted {
		t.Fatalf("delete = %v %v", deleted, err)
	}
	if deleted, _ := storage.Delete("test"); deleted {
		t.Fatal("second delete returned true")
	}

	storage.Save(NewCredentialObject("a"))
	storage.Save(NewCredentialObject("b"))
	ids, err := storage.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v", ids)
	}
	if ok, _ := storage.Exists("a"); !ok {
		t.Fatal("exists(a) = false")
	}
	if ok, _ := storage.Exists("zzz"); ok {
		t.Fatal("exists(zzz) = true")
	}

	storage.Clear()
	if ids, _ := storage.ListAll(); len(ids) != 0 {
		t.Fatalf("after clear = %v", ids)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestEnvVarStorage::test_load_from_env, ::test_load_nonexistent,
func TestEnvVarStorage(t *testing.T) {
	t.Setenv("TEST_API_KEY", "test-value")
	storage := NewEnvVarStorage(map[string]string{"test": "TEST_API_KEY"})
	cred, err := storage.Load("test")
	if err != nil || cred == nil {
		t.Fatalf("load = %+v %v", cred, err)
	}
	if v, _ := cred.GetKeyValue("api_key"); v != "test-value" {
		t.Fatalf("api_key = %q", v)
	}
	if cred.Description != "Loaded from TEST_API_KEY" {
		t.Fatalf("description = %q", cred.Description)
	}

	missing := NewEnvVarStorage(map[string]string{"test": "CREDENTIALS_TEST_NONEXISTENT_VAR"})
	if cred, err := missing.Load("test"); err != nil || cred != nil {
		t.Fatalf("missing load = %+v %v", cred, err)
	}

	t.Setenv("MY_SERVICE_API_KEY", "value")
	defaultPattern := NewEnvVarStorage(nil)
	cred, err = defaultPattern.Load("my_service")
	if err != nil || cred == nil {
		t.Fatalf("default pattern load = %+v %v", cred, err)
	}
	if v, _ := cred.GetKeyValue("api_key"); v != "value" {
		t.Fatalf("default pattern value = %q", v)
	}

	saveErr := storage.Save(NewCredentialObject("test"))
	if !errors.Is(saveErr, ErrEnvStorageReadOnlySave) {
		t.Fatalf("save error = %v", saveErr)
	}
	if !strings.Contains(saveErr.Error(), "EnvVarStorage is read-only") || !strings.Contains(saveErr.Error(), "EncryptedFileStorage") {
		t.Fatalf("save message = %q", saveErr)
	}
	if _, deleteErr := storage.Delete("test"); !errors.Is(deleteErr, ErrEnvStorageReadOnlyDelete) {
		t.Fatalf("delete error = %v", deleteErr)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestEncryptedFileStorage::test_save_and_load, ::test_list_all,
func TestEncryptedFileStorage(t *testing.T) {
	storage := newEncryptedStorage(t)

	cred := NewCredentialObject("test")
	cred.Keys.Set(NewCredentialKey("api_key", "secret-value"))
	if err := storage.Save(cred); err != nil {
		t.Fatal(err)
	}
	loaded, err := storage.Load("test")
	if err != nil || loaded == nil {
		t.Fatalf("load = %+v %v", loaded, err)
	}
	if loaded.ID != "test" {
		t.Fatalf("id = %q", loaded.ID)
	}
	if v, _ := loaded.GetKeyValue("api_key"); v != "secret-value" {
		t.Fatalf("api_key = %q", v)
	}
	if _, err := os.Stat(filepath.Join(storage.BasePath(), "credentials", "test.enc")); err != nil {
		t.Fatalf("encrypted file missing: %v", err)
	}
	if missing, err := storage.Load("missing"); err != nil || missing != nil {
		t.Fatalf("missing load = %+v %v", missing, err)
	}

	storage.Save(NewCredentialObject("cred1"))
	storage.Save(NewCredentialObject("cred2"))
	ids, err := storage.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if !sliceContains(ids, "cred1") || !sliceContains(ids, "cred2") {
		t.Fatalf("list = %v", ids)
	}

	deleted, err := storage.Delete("test")
	if err != nil || !deleted {
		t.Fatalf("delete = %v %v", deleted, err)
	}
	if again, _ := storage.Delete("test"); again {
		t.Fatal("second delete returned true")
	}
	if loaded, _ := storage.Load("test"); loaded != nil {
		t.Fatal("deleted credential still loads")
	}

	second, err := NewEncryptedFileStorage(storage.BasePath(), "")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = second.Load("cred1")
	if err != nil || loaded == nil {
		t.Fatalf("second instance load = %+v %v", loaded, err)
	}
}

// Parity-exempt: the Python EncryptedFileStorage tests cover save/load, list_all,
func TestEncryptedFileStorageWritesIndex(t *testing.T) {
	storage := newEncryptedStorage(t)

	cred := NewCredentialObject("google_work")
	cred.CredentialType = CredentialTypeOAuth2
	cred.AutoRefresh = true
	first := time.Now().UTC().Add(time.Hour)
	second := time.Now().UTC().Add(2 * time.Hour)
	cred.Keys.Set(&CredentialKey{Name: "access_token", Value: "at", ExpiresAt: &second})
	cred.Keys.Set(&CredentialKey{Name: "refresh_token", Value: "rt", ExpiresAt: &first})
	cred.Keys.Set(NewCredentialKey("_alias", "work"))
	cred.Keys.Set(NewCredentialKey("_integration_type", "google"))
	cred.SetIdentity(map[string]string{"email": "dev@example.com"})

	if err := storage.Save(cred); err != nil {
		t.Fatal(err)
	}

	var index IndexEnvelope
	if err := atomicfs.ReadJSON(filepath.Join(storage.BasePath(), "metadata", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	if index.Version != IndexVersion {
		t.Fatalf("version = %q", index.Version)
	}
	if index.StorePath != storage.BasePath() || index.GeneratedAt == "" {
		t.Fatalf("envelope = %+v", index)
	}
	entry, ok := index.Credentials["google_work"]
	if !ok {
		t.Fatalf("entry missing: %+v", index.Credentials)
	}
	if entry.CredentialType != "oauth2" || !entry.AutoRefresh {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Provider == nil || *entry.Provider != "google" {
		t.Fatalf("provider = %v", entry.Provider)
	}
	if entry.Alias == nil || *entry.Alias != "work" {
		t.Fatalf("alias = %v", entry.Alias)
	}
	if entry.Identity["email"] != "dev@example.com" {
		t.Fatalf("identity = %v", entry.Identity)
	}
	if len(entry.KeyNames) != 2 || entry.KeyNames[0] != "access_token" || entry.KeyNames[1] != "refresh_token" {
		t.Fatalf("visible key names = %v", entry.KeyNames)
	}
	if entry.ExpiresAt == nil {
		t.Fatal("earliest expiry missing")
	}
	parsed, err := time.Parse(time.RFC3339Nano, *entry.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Equal(first) {
		t.Fatalf("expires_at = %v, want %v", parsed, first)
	}
	raw, _ := os.ReadFile(filepath.Join(storage.BasePath(), "metadata", "index.json"))
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"at"`) || strings.Contains(string(raw), `"rt"`) {
		t.Fatalf("index leaked a secret: %s", raw)
	}
}

// Parity-exempt: decryption-failure error typing has no Python test.
func TestEncryptedFileStorageReportsDecryptionFailure(t *testing.T) {
	storage := newEncryptedStorage(t)
	cred := NewCredentialObject("broken")
	if err := storage.Save(cred); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(storage.BasePath(), "credentials", "broken.enc")
	if err := os.WriteFile(path, []byte("not-a-fernet-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := storage.Load("broken")
	var decryption *CredentialDecryptionError
	if !errors.As(err, &decryption) {
		t.Fatalf("error = %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "Failed to decrypt credential 'broken'") {
		t.Fatalf("message = %q", err)
	}
}

// Parity-exempt: rebuilding a stale metadata index on open has no Python test.
func TestEncryptedFileStorageRebuildsStaleIndex(t *testing.T) {
	storage := newEncryptedStorage(t)
	storage.Save(NewCredentialObject("keep"))

	indexPath := filepath.Join(storage.BasePath(), "metadata", "index.json")
	if err := atomicfs.WriteJSON(indexPath, map[string]any{"version": "1.0", "credentials": map[string]any{}}, 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewEncryptedFileStorage(storage.BasePath(), "")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := reopened.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != "keep" {
		t.Fatalf("rebuilt index = %v", ids)
	}
}

// Parity: core/framework/credentials/tests/test_credential_store.py::TestCompositeStorage (primary wins, fallback, write primary only).
func TestCompositeStorage(t *testing.T) {
	primary := NewInMemoryStorage(nil)
	fallback := NewInMemoryStorage(nil)

	primaryCred := NewCredentialObject("test")
	primaryCred.Keys.Set(NewCredentialKey("k", "primary"))
	primary.Save(primaryCred)

	fallbackCred := NewCredentialObject("test")
	fallbackCred.Keys.Set(NewCredentialKey("k", "fallback"))
	fallback.Save(fallbackCred)

	storage := NewCompositeStorage(primary, fallback)
	cred, err := storage.Load("test")
	if err != nil || cred == nil {
		t.Fatalf("load = %+v %v", cred, err)
	}
	if v, _ := cred.GetKeyValue("k"); v != "primary" {
		t.Fatalf("primary read = %q", v)
	}

	fallbackOnly := NewInMemoryStorage(nil)
	fallbackCred2 := NewCredentialObject("only_fallback")
	fallbackCred2.Keys.Set(NewCredentialKey("k", "fallback"))
	fallbackOnly.Save(fallbackCred2)
	storage = NewCompositeStorage(NewInMemoryStorage(nil), fallbackOnly)
	cred, _ = storage.Load("only_fallback")
	if cred == nil || cred.GetKey("k") == nil {
		t.Fatal("fallback read failed")
	}

	writePrimary := NewInMemoryStorage(nil)
	writeFallback := NewInMemoryStorage(nil)
	storage = NewCompositeStorage(writePrimary, writeFallback)
	if err := storage.Save(NewCredentialObject("written")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := writePrimary.Exists("written"); !ok {
		t.Fatal("primary did not receive the write")
	}
	if ok, _ := writeFallback.Exists("written"); ok {
		t.Fatal("fallback received the write")
	}
}

func sliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
