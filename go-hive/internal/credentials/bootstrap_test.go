package credentials

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func bootstrapHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("HIVE_HOME", filepath.Join(home, ".hive"))
	t.Setenv("HIVE_DESKTOP_MODE", "")
	t.Setenv(CredentialKeyEnvVar, "")
	t.Setenv(AdenEnvVar, "")
	return home
}

// Parity: core/tests/test_credential_bootstrap.py::test_bootstrap_loads_configured_llm_env_var_from_shell_config
func TestBootstrapLoadsConfiguredLLMEnvVar(t *testing.T) {
	home := bootstrapHome(t)
	if err := os.WriteFile(
		filepath.Join(home, ".zshrc"),
		[]byte("export OPENROUTER_API_KEY=\"or-key-123\"\nexport ANTHROPIC_API_KEY='an-key-456'\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	EnsureCredentialKeyEnv(
		WithExtraEnvVars("ANTHROPIC_API_KEY"),
		WithLLMEnvVar("OPENROUTER_API_KEY"),
	)

	if got := os.Getenv("OPENROUTER_API_KEY"); got != "or-key-123" {
		t.Fatalf("OPENROUTER_API_KEY = %q", got)
	}
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "an-key-456" {
		t.Fatalf("ANTHROPIC_API_KEY = %q", got)
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_bootstrap_does_not_override_existing_configured_llm_env_var
func TestBootstrapDoesNotOverrideExistingEnvVar(t *testing.T) {
	home := bootstrapHome(t)
	if err := os.WriteFile(
		filepath.Join(home, ".zshrc"),
		[]byte("export OPENROUTER_API_KEY=\"new-value-should-not-apply\"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "already-set")

	EnsureCredentialKeyEnv(WithLLMEnvVar("OPENROUTER_API_KEY"))

	if got := os.Getenv("OPENROUTER_API_KEY"); got != "already-set" {
		t.Fatalf("existing value overwritten: %q", got)
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_load_credential_key_persists_env_value_to_disk
func TestLoadCredentialKeyPersistsEnvValue(t *testing.T) {
	dir := bootstrapHome(t)
	t.Setenv(CredentialKeyEnvVar, "env-key-abc")

	loaded, err := LoadCredentialKey()
	if err != nil || loaded != "env-key-abc" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	path := filepath.Join(dir, ".hive", "secrets", "credential_key")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("key file missing: %v", err)
	}
	if string(data) != "env-key-abc" {
		t.Fatalf("key file = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %o", info.Mode().Perm())
	}
	if dirInfo, err := os.Stat(filepath.Dir(path)); err == nil && dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("secrets dir mode = %o", dirInfo.Mode().Perm())
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_load_credential_key_overwrites_stale_disk_value_with_env
func TestLoadCredentialKeyOverwritesStaleDiskValue(t *testing.T) {
	dir := bootstrapHome(t)
	path := filepath.Join(dir, ".hive", "secrets", "credential_key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("disk-old-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(CredentialKeyEnvVar, "env-new-key")

	loaded, err := LoadCredentialKey()
	if err != nil || loaded != "env-new-key" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "env-new-key" {
		t.Fatalf("stale key not overwritten: %q", data)
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_load_credential_key_skips_write_when_env_and_file_match
func TestLoadCredentialKeySkipsIdenticalWrite(t *testing.T) {
	dir := bootstrapHome(t)
	path := filepath.Join(dir, ".hive", "secrets", "credential_key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	t.Setenv(CredentialKeyEnvVar, "same-key")

	loaded, err := LoadCredentialKey()
	if err != nil || loaded != "same-key" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("identical key rewritten: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_load_credential_key_from_disk_does_not_rewrite_itself
func TestLoadCredentialKeyFromDiskDoesNotRewrite(t *testing.T) {
	dir := bootstrapHome(t)
	path := filepath.Join(dir, ".hive", "secrets", "credential_key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("from-disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	t.Setenv(CredentialKeyEnvVar, "")

	loaded, err := LoadCredentialKey()
	if err != nil || loaded != "from-disk" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	if got := os.Getenv(CredentialKeyEnvVar); got != "from-disk" {
		t.Fatalf("env not populated from disk: %q", got)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("disk load rewrote the file: %v -> %v", before.ModTime(), after.ModTime())
	}
}

// Parity: core/tests/test_credential_bootstrap.py::test_load_credential_key_swallows_persistence_failure
func TestLoadCredentialKeySwallowsPersistenceFailure(t *testing.T) {
	dir := bootstrapHome(t)
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HIVE_HOME", blocked)
	t.Setenv(CredentialKeyEnvVar, "env-key")

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)

	loaded, err := LoadCredentialKey()
	if err != nil || loaded != "env-key" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	if !strings.Contains(logs.String(), "failed to persist HIVE_CREDENTIAL_KEY") {
		t.Fatalf("warning missing: %q", logs.String())
	}
}

// Parity-exempt: no Python test exercises the real SaveAdenAPIKey/LoadAdenAPIKey
func TestAdenAPIKeyRoundTrip(t *testing.T) {
	dir := bootstrapHome(t)
	key, err := GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveCredentialKey(key); err != nil {
		t.Fatal(err)
	}

	if err := SaveAdenAPIKey("aden-123"); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(AdenEnvVar); got != "aden-123" {
		t.Fatalf("env after save = %q", got)
	}

	t.Setenv(AdenEnvVar, "")
	loaded, err := LoadAdenAPIKey()
	if err != nil || loaded != "aden-123" {
		t.Fatalf("load = %q %v", loaded, err)
	}
	if got := os.Getenv(AdenEnvVar); got != "aden-123" {
		t.Fatalf("env after load = %q", got)
	}

	credentialFile := filepath.Join(dir, ".hive", "credentials", "credentials", "aden_api_key.enc")
	if _, err := os.Stat(credentialFile); err != nil {
		t.Fatalf("credential file missing: %v", err)
	}

	if !DeleteAdenAPIKey() {
		t.Fatal("delete did not report a removal")
	}
	if got := os.Getenv(AdenEnvVar); got != "" {
		t.Fatalf("env after delete = %q", got)
	}
	if loaded, _ := LoadAdenAPIKey(); loaded != "" {
		t.Fatalf("deleted aden key reloaded: %q", loaded)
	}
	if _, err := os.Stat(credentialFile); !os.IsNotExist(err) {
		t.Fatalf("credential file survived delete: %v", err)
	}
}
