package server

// Process-wide credential/catalog events.
//
// Upstream pairs every credential save/delete with two events on the global
// bus: CREDENTIAL_PROVIDER_CONNECTED / CREDENTIAL_PROVIDER_DISCONNECTED (so
// per-provider UI state flips) and TOOL_CATALOG_REFRESHED (so generic catalog
// subscribers only listen for one type). The Go port defined the event types
// and put them on the global SSE allowlist but never published them, so open
// tabs never refreshed after a credential write.
//
// Parity: framework/server/routes_credentials.py::_publish_credential_event
// Parity: framework/server/routes_credentials.py::_provider_for_credential

import (
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// globalCredentialEventStreamID is the cross-cutting stream upstream uses for
// events with no session scope ("global").
const globalCredentialEventStreamID = "global"

// publishCredentialCatalogEvent pairs one credential write with the connected/
// disconnected event and the tool-catalog refresh. It is the production
// PublishCredentialEvent hook wired by NewApp; PublishGlobal already contains
// its own failure guard, so a broken subscriber can never fail the route.
func publishCredentialCatalogEvent(connected bool, credentialID, provider string) {
	eventType := eventbus.EventCredentialProviderDisconnected
	trigger := "credential_delete"
	if connected {
		eventType = eventbus.EventCredentialProviderConnected
		trigger = "credential_save"
	}
	// Upstream passes None when the credential is not OAuth-bound; JSON null
	// is what subscribers compare against, so an unknown provider stays null
	// instead of becoming an empty string.
	providerValue := any(nil)
	if provider != "" {
		providerValue = provider
	}
	eventbus.PublishGlobal(&eventbus.AgentEvent{
		Type:     eventType,
		StreamID: globalCredentialEventStreamID,
		Data: map[string]any{
			"credential_id": credentialID,
			"provider":      providerValue,
		},
	})
	eventbus.PublishGlobal(&eventbus.AgentEvent{
		Type:     eventbus.EventToolCatalogRefreshed,
		StreamID: globalCredentialEventStreamID,
		Data: map[string]any{
			"trigger":       trigger,
			"credential_id": credentialID,
			"provider":      providerValue,
		},
	})
}

// credentialProviderForSpec resolves a credential id to its Aden provider name
// for the event payload. Unknown / non-OAuth credentials resolve to "" (null
// in the event), exactly like upstream's best-effort spec scan.
func credentialProviderForSpec(credentialID string) string {
	return specProvider(credentialSpecsFromRegistry(), credentialID)
}
