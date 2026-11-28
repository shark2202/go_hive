package credentials

import (
	"os"
	"strings"
)

// providerCredentialIDs mirrors framework/config.py::_PROVIDER_CRED_MAP: the
// credential id a provider's BYOK key is stored under. A provider outside the
// table has no credential-store step, exactly as upstream's map lookup yields
// no credential id.
var providerCredentialIDs = map[string]string{
	"anthropic":   "anthropic",
	"openai":      "openai",
	"gemini":      "gemini",
	"google":      "gemini",
	"minimax":     "minimax",
	"groq":        "groq",
	"cerebras":    "cerebras",
	"openrouter":  "openrouter",
	"mistral":     "mistral",
	"together":    "together",
	"together_ai": "together",
	"deepseek":    "deepseek",
	"kimi":        "kimi",
	"hive":        "hive",
}

// ProviderCredentialID returns the credential id that backs a provider's
// credential-store key. Case and surrounding space are ignored, as upstream's
// provider.lower() ignores them.
//
// Parity: framework/config.py::_PROVIDER_CRED_MAP
func ProviderCredentialID(provider string) (string, bool) {
	id, ok := providerCredentialIDs[strings.ToLower(strings.TrimSpace(provider))]
	return id, ok
}

// StoredProviderKey reads a provider's BYOK key from the encrypted credential
// store. Without a HIVE_CREDENTIAL_KEY there is no store to read, so the
// lookup reports absent rather than guessing; a storage, decryption or
// missing-key failure reads as absent too.
//
// Parity: framework/config.py::_get_api_key_from_credential_store
func StoredProviderKey(provider string) (string, bool) {
	if os.Getenv(CredentialKeyEnvVar) == "" {
		return "", false
	}
	credentialID, ok := ProviderCredentialID(provider)
	if !ok {
		return "", false
	}
	store, err := WithEncryptedStorage("")
	if err != nil {
		return "", false
	}
	key, err := store.Get(credentialID)
	if err != nil || key == nil || *key == "" {
		return "", false
	}
	return *key, true
}
