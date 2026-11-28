package cli

// Parity: tests/test_hive_cred_cli.py — hive-cred resolves a credential that
// is present only in the environment when using the production default
// adapter (encrypted store + env fallback).

import (
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.default — env-only key through the real hive-cred path.
func TestCredTokenResolvesEnvOnlyCredential(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	t.Setenv("BRAVE_SEARCH_API_KEY", "env-only-key")
	useCredAdapterFactory(t, func() (credAdapter, error) {
		return credentials.DefaultAdapter(credentials.CatalogNamedSpecs())
	})

	code, stdout, stderr := runCred(t, "token", "brave_search")
	if code != 0 || stdout != "env-only-key\n" {
		t.Fatalf("Run(token brave_search) = %d, %q, want 0, %q (stderr %q)",
			code, stdout, "env-only-key\n", stderr)
	}
}
