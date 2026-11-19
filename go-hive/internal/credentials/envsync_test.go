package credentials

// Parity-exempt: Go-only bridge from the encrypted store into the process
// environment. Upstream resolves keys through CredentialStoreAdapter at call
// time, so there is no Python test to mirror.

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newEnvSyncStore(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate fernet key: %v", err)
	}
	t.Setenv(CredentialKeyEnvVar, key)
	return filepath.Join(home, "credentials")
}

func saveEnvSyncCredential(t *testing.T, basePath, credentialID, keyName, value string) {
	t.Helper()
	storage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	credential := NewCredentialObject(credentialID)
	credential.Keys.Set(NewCredentialKey(keyName, value))
	if err := storage.Save(credential); err != nil {
		t.Fatalf("save credential: %v", err)
	}
}

// Parity-exempt: pins the dashboard→tool contract for the Go port; no upstream
// equivalent because Python reads the store through the adapter instead.
func TestSyncEnvFromStoreInjectsSavedCredential(t *testing.T) {
	basePath := newEnvSyncStore(t)
	saveEnvSyncCredential(t, basePath, "brave_search", "api_key", "sk-brave-test")
	t.Setenv("HIVE_TEST_BRAVE_KEY", "")

	result := SyncEnvFromStore(basePath, []EnvBinding{{
		CredentialID: "brave_search",
		EnvVar:       "HIVE_TEST_BRAVE_KEY",
	}})

	if got := os.Getenv("HIVE_TEST_BRAVE_KEY"); got != "sk-brave-test" {
		t.Fatalf("env = %q, want the saved key", got)
	}
	if len(result.Injected) != 1 || result.Injected[0] != "HIVE_TEST_BRAVE_KEY" {
		t.Fatalf("injected = %#v", result.Injected)
	}
	if len(result.Skipped) != 0 || len(result.Missing) != 0 || result.Unreadable != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// Parity-exempt: protects operator-provided environment variables; upstream
// has the same precedence in CredentialSpec.env_var lookups but no Go test.
func TestSyncEnvFromStoreKeepsExistingEnvironment(t *testing.T) {
	basePath := newEnvSyncStore(t)
	saveEnvSyncCredential(t, basePath, "notion_token", "api_key", "sk-from-store")
	t.Setenv("HIVE_TEST_NOTION_KEY", "sk-from-shell")

	result := SyncEnvFromStore(basePath, []EnvBinding{{
		CredentialID: "notion_token",
		EnvVar:       "HIVE_TEST_NOTION_KEY",
	}})

	if got := os.Getenv("HIVE_TEST_NOTION_KEY"); got != "sk-from-shell" {
		t.Fatalf("env = %q, want the pre-existing value", got)
	}
	if len(result.Injected) != 0 || len(result.Skipped) != 1 || result.Skipped[0] != "HIVE_TEST_NOTION_KEY" {
		t.Fatalf("result = %#v", result)
	}
}

// Parity-exempt: secret redaction in the sync log is a Go-only requirement;
// the Python adapter never copies values into the environment at all.
func TestSyncEnvFromStoreDoesNotLogSecretValue(t *testing.T) {
	basePath := newEnvSyncStore(t)
	const secret = "sk-never-log-this-value"
	saveEnvSyncCredential(t, basePath, "exa_search", "api_key", secret)
	t.Setenv("HIVE_TEST_EXA_KEY", "")

	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)

	result := SyncEnvFromStore(basePath, []EnvBinding{{
		CredentialID: "exa_search",
		EnvVar:       "HIVE_TEST_EXA_KEY",
	}})

	if len(result.Injected) != 1 {
		t.Fatalf("injected = %#v", result.Injected)
	}
	if strings.Contains(logged.String(), secret) {
		t.Fatalf("log leaked the credential value: %q", logged.String())
	}
	if !strings.Contains(logged.String(), "env sync injected 1") {
		t.Fatalf("expected a sync summary log line, got %q", logged.String())
	}
}

// Parity-exempt: dashboard edits must un-stick a key the sync itself exported;
// upstream has no equivalent because it resolves the store on every call.
func TestSyncEnvFromStoreRefreshesPreviouslyInjectedValue(t *testing.T) {
	basePath := newEnvSyncStore(t)
	bindings := []EnvBinding{{
		CredentialID:  "linear_token",
		EnvVar:        "HIVE_TEST_LINEAR_KEY",
		CredentialKey: "api_key",
	}}
	t.Setenv("HIVE_TEST_LINEAR_KEY", "")
	saveEnvSyncCredential(t, basePath, "linear_token", "api_key", "sk-first")
	if result := SyncEnvFromStore(basePath, bindings); len(result.Injected) != 1 {
		t.Fatalf("first sync injected = %#v", result.Injected)
	}

	saveEnvSyncCredential(t, basePath, "linear_token", "api_key", "sk-second")
	result := SyncEnvFromStore(basePath, bindings)

	if got := os.Getenv("HIVE_TEST_LINEAR_KEY"); got != "sk-second" {
		t.Fatalf("env = %q, want the edited key", got)
	}
	if len(result.Refreshed) != 1 || result.Refreshed[0] != "HIVE_TEST_LINEAR_KEY" {
		t.Fatalf("refreshed = %#v", result.Refreshed)
	}
}
