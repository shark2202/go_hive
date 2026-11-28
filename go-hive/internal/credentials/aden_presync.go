package credentials

// Parity: framework/credentials/validation.py::_presync_aden_tokens
//
// When ADEN_API_KEY is set, fetch OAuth tokens from Aden and export them to
// the environment before presence validation runs. Only credentials marked
// aden_supported are touched, only integrations Aden actually lists are
// fetched, and an explicit user environment value wins unless force is set.
// Aden being unreachable is a diagnostic, never a validation failure.

import (
	"os"
	"strings"
)

// AdenTokenClient is the slice of *AdenCredentialClient the pre-sync needs.
// Tests can inject a client; production builds one from ADEN_API_KEY and
// ADEN_API_URL via NewAdenClientFromEnv.
type AdenTokenClient interface {
	ListIntegrations() ([]AdenIntegrationInfo, error)
	GetCredential(integrationID string) (*AdenCredentialResponse, error)
	Close() error
}

// PresyncAdenTokens is the exported entry point for the resync route: force
// re-exports Aden tokens for aden_supported specs before validation. It never
// returns an error; failures are logged and validation continues.
// Parity: core/framework/credentials/validation.py::_presync_aden_tokens
func PresyncAdenTokens(specs []GeneratedCredentialSpec, force bool, client AdenTokenClient) {
	presyncAdenTokens(specs, force, client)
}

// presyncAdenTokens mirrors validation.py::_presync_aden_tokens.
// It never returns an error: every failure is logged and validation continues.
func presyncAdenTokens(specs []GeneratedCredentialSpec, force bool, injected AdenTokenClient) {
	if os.Getenv(AdenEnvVar) == "" {
		return
	}

	client := injected
	if client == nil {
		created, err := NewAdenClientFromEnv()
		if err != nil {
			LogWarning("framework.credentials.validation", "Aden pre-sync unavailable: %v", err)
			return
		}
		client = created
	}
	defer client.Close()

	integrations, err := client.ListIntegrations()
	if err != nil {
		LogWarning("framework.credentials.validation", "Aden pre-sync: failed to list integrations: %v", err)
		return
	}

	// Aden integration IDs are hashes; the provider name is the stable key a
	// credential spec can match. Sync_all() upstream skips non-active rows,
	// and only the first active account per provider is addressable by name.
	activeByProvider := make(map[string]AdenIntegrationInfo, len(integrations))
	for _, integration := range integrations {
		if integration.Status != "active" || integration.Provider == "" || integration.IntegrationID == "" {
			continue
		}
		if _, exists := activeByProvider[integration.Provider]; !exists {
			activeByProvider[integration.Provider] = integration
		}
	}

	synced := []string{}
	skippedEnvSet := []string{}
	missingInAden := []string{}
	for _, spec := range specs {
		if !spec.AdenSupported || spec.EnvVar == "" {
			continue
		}
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}

		// An explicit user export always wins unless the caller asked for a
		// refresh (the credentials modal after reauthorization).
		if !force && os.Getenv(spec.EnvVar) != "" {
			skippedEnvSet = append(skippedEnvSet, credentialID)
			continue
		}

		integration, present := findAdenIntegration(activeByProvider, spec, credentialID)
		if !present {
			missingInAden = append(missingInAden, credentialID)
			continue
		}

		response, err := client.GetCredential(integration.IntegrationID)
		if err != nil {
			LogWarning("framework.credentials.validation",
				"Aden pre-sync failed for %s (id=%s): %v", spec.EnvVar, credentialID, err)
			continue
		}
		if response == nil {
			// 404: the integration vanished between list and fetch.
			missingInAden = append(missingInAden, credentialID)
			continue
		}

		value := adenCredentialKeyValue(response, spec.CredentialKey)
		if value == "" {
			LogWarning("framework.credentials.validation",
				"Aden pre-sync: %s (id=%s) available but key '%s' returned none",
				spec.EnvVar, credentialID, spec.CredentialKey)
			continue
		}
		if err := os.Setenv(spec.EnvVar, value); err != nil {
			LogWarning("framework.credentials.validation",
				"Aden pre-sync failed for %s (id=%s): %v", spec.EnvVar, credentialID, err)
			continue
		}
		synced = append(synced, credentialID)
		LogDebug("framework.credentials.validation", "Pre-synced %s from Aden", spec.EnvVar)
	}

	LogInfo("framework.credentials.validation",
		"Aden pre-sync: synced=%v skipped(env-set)=%v missing(not-in-Aden)=%v",
		synced, skippedEnvSet, missingInAden)
}

// findAdenIntegration matches the spec against Aden's provider namespace.
// AdenProviderName is authoritative; the credential id and display name are
// fallbacks because older registries omit the provider name.
func findAdenIntegration(activeByProvider map[string]AdenIntegrationInfo, spec GeneratedCredentialSpec, credentialID string) (AdenIntegrationInfo, bool) {
	for _, candidate := range []string{spec.AdenProviderName, credentialID, spec.Name} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if integration, present := activeByProvider[candidate]; present {
			return integration, true
		}
	}
	return AdenIntegrationInfo{}, false
}

// adenCredentialKeyValue projects the fetched credential onto the key the spec
// exports. Aden's synced credential object stores the OAuth token under
// access_token (upstream _aden_response_to_credential); other keys can only be
// satisfied when the response carries them as metadata.
func adenCredentialKeyValue(response *AdenCredentialResponse, credentialKey string) string {
	if response == nil {
		return ""
	}
	key := strings.TrimSpace(credentialKey)
	if key == "" || key == "access_token" {
		return response.AccessToken
	}
	if raw, present := response.Metadata[key]; present {
		if value, ok := raw.(string); ok {
			return value
		}
	}
	return ""
}
