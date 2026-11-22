package credentials

// Parity-exempt: Go-only bridge from the encrypted credential store into the
// process environment. Upstream resolves keys through CredentialStoreAdapter
// at call time, while the ported tools read os.Getenv, so credentials saved
// from the dashboard are mirrored into the environment at serve startup and
// again after each write.

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// EnvBinding maps one saved credential onto the environment variable a tool
// reads. CredentialKey names the key inside the credential; empty falls back
// to the credential's primary key (api_key, access_token, ...). ExpiryKey
// names a key whose expiry timestamp is exported instead of its value.
type EnvBinding struct {
	CredentialID  string
	EnvVar        string
	CredentialKey string
	ExpiryKey     string
}

// EnvSyncResult summarizes one synchronization pass. It deliberately carries
// environment variable names and counts only, never key material.
type EnvSyncResult struct {
	Injected   []string // variables that were unset/empty and now carry a saved key
	Refreshed  []string // variables written by an earlier sync and re-exported
	Skipped    []string // variables already set outside the store (not touched)
	Missing    []string // bound variables with no usable key in the store
	Unreadable int      // saved credentials that could not be decrypted
}

// envSyncState remembers which variables this process exported from the store
// and the exact value written. A variable is overwritten only while its value
// is still the one a previous sync wrote: an operator-provided environment
// variable always wins, while a key edited in the dashboard takes effect
// without a restart.
type envSyncState struct {
	mu      sync.Mutex
	written map[string]string
}

var storeEnvSync = envSyncState{written: map[string]string{}}

// SyncEnvFromStore exports saved credentials from the encrypted store at
// basePath into the process environment. Existing non-empty variables are
// never overwritten (except for values a previous sync wrote itself); empty
// variables count as unset. Secret values never reach the log. An empty
// basePath resolves the default HIVE_HOME credentials directory.
func SyncEnvFromStore(basePath string, bindings []EnvBinding) EnvSyncResult {
	result := EnvSyncResult{}
	if len(bindings) == 0 {
		return result
	}
	// The encryption key normally arrives via HIVE_CREDENTIAL_KEY; recover it
	// from the secrets file or shell config when serve started without it.
	// Without a key nothing in the store is readable, and a "read" must not
	// mint a fresh encryption key.
	if key, _ := LoadCredentialKey(); key == "" {
		return result
	}
	if basePath == "" {
		home, err := hivehome.Resolve()
		if err != nil {
			LogWarning("framework.credentials.envsync", "credentials: env sync skipped: %v", err)
			return result
		}
		basePath = home.CredentialsDir()
	}
	storage, err := NewEncryptedFileStorage(basePath, "")
	if err != nil {
		LogWarning("framework.credentials.envsync", "credentials: env sync skipped: open store: %v", err)
		return result
	}

	storeEnvSync.mu.Lock()
	defer storeEnvSync.mu.Unlock()
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		envVar := strings.TrimSpace(binding.EnvVar)
		credentialID := strings.TrimSpace(binding.CredentialID)
		if envVar == "" || credentialID == "" || seen[envVar] {
			continue
		}
		seen[envVar] = true

		current, present := os.LookupEnv(envVar)
		previous, owned := storeEnvSync.written[envVar]
		hasExternalValue := present && strings.TrimSpace(current) != "" && !(owned && current == previous)
		if hasExternalValue {
			result.Skipped = append(result.Skipped, envVar)
			continue
		}

		credential, err := storage.Load(credentialID)
		if err != nil {
			// One undecryptable entry must not block the rest; count it rather
			// than log the error text, which can quote store internals.
			result.Unreadable++
			continue
		}
		if credential == nil {
			continue
		}
		value, ok := envValueFor(credential, binding.CredentialKey)
		if binding.ExpiryKey != "" {
			value, ok = envExpiryFor(credential, binding.ExpiryKey)
		}
		if !ok {
			result.Missing = append(result.Missing, envVar)
			continue
		}
		if err := os.Setenv(envVar, value); err != nil {
			result.Missing = append(result.Missing, envVar)
			continue
		}
		storeEnvSync.written[envVar] = value
		if owned && present && strings.TrimSpace(current) != "" {
			result.Refreshed = append(result.Refreshed, envVar)
		} else {
			result.Injected = append(result.Injected, envVar)
		}
	}

	if len(result.Injected)+len(result.Refreshed)+len(result.Skipped) > 0 {
		// Names and counts only: the values are credentials.
		LogInfo("framework.credentials.envsync",
			"credentials: env sync injected %d, refreshed %d, kept existing %d",
			len(result.Injected), len(result.Refreshed), len(result.Skipped),
		)
	}
	return result
}

// envValueFor picks the value a binding exports: the spec's credential_key
// first, then the conventional primary keys, then the first non-internal key.
func envValueFor(credential *CredentialObject, preferred string) (string, bool) {
	candidates := make([]string, 0, 4)
	if name := strings.TrimSpace(preferred); name != "" {
		candidates = append(candidates, name)
	}
	candidates = append(candidates, "api_key", "access_token", "value")
	for _, name := range candidates {
		if key := credential.Keys.Get(name); key != nil && key.GetSecretValue() != "" {
			return key.GetSecretValue(), true
		}
	}
	for _, name := range credential.Keys.Names() {
		if strings.HasPrefix(name, "_") {
			continue
		}
		if key := credential.Keys.Get(name); key != nil && key.GetSecretValue() != "" {
			return key.GetSecretValue(), true
		}
	}
	return "", false
}

// envExpiryFor exports the RFC3339 expiry of a key instead of its value. A key
// without an expiry exports nothing: an unset *_EXPIRES_AT correctly means
// "expiry unknown" to the OAuth2 refresher.
func envExpiryFor(credential *CredentialObject, keyName string) (string, bool) {
	key := credential.Keys.Get(keyName)
	if key == nil || key.ExpiresAt == nil {
		return "", false
	}
	return key.ExpiresAt.UTC().Format(time.RFC3339), true
}
