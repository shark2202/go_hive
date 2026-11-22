package cli

// sentinel_setup execution route.
//
// The queen-facing Sentinel setup tool (store_token / configure / test /
// status / detect_chat / help) is implemented in internal/sentinel; this file
// provides the production seams it reads: the per-session hive home (store +
// credentials), the process-global Sentinel manager, the encrypted credential
// save with its store → environment bridge, and the tool_catalog_refreshed
// event the desktop UI refetches on.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::handle

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/sentinel"
)

// executeSentinelSetup routes one sentinel_setup call through the engine.
// default_colony_id resolves the colony bound to the session, as upstream
// resolves it from colony_binding_provider.
func (c *sessionToolCatalogue) executeSentinelSetup(ctx context.Context, input map[string]any) (string, bool, bool) {
	content := sentinel.HandleSentinelSetup(ctx, input, colonyIDForExecuteContext(ctx), newSentinelSetupDeps(c.home))
	return content, strings.HasPrefix(content, "ERROR:"), true
}

// colonyIDForExecuteContext returns the on-disk colony directory name bound to
// this tool call. The chat runtime binds its colony runtime on the execution
// context; a catalogue used outside a bound session resolves "" (configure and
// test then require an explicit colony_id inside the tool).
func colonyIDForExecuteContext(ctx context.Context) string {
	spawner := colony.RunWorkerSpawnerFromContext(ctx)
	if carrier, ok := spawner.(interface{ StreamID() string }); ok {
		return carrier.StreamID()
	}
	return ""
}

// newSentinelSetupDeps assembles the tool's runtime seams from a hive home.
func newSentinelSetupDeps(home hivehome.Home) sentinel.SentinelSetupDeps {
	notifier := sentinel.NewHTTPNotifier(nil)
	notifier.ResolveToken = sentinelCredentialResolver(home)
	deps := sentinel.SentinelSetupDeps{
		Store:          sentinel.NewStore(home.Root),
		Notifier:       notifier,
		SaveToken:      func(credentialID, token string) error { return saveSentinelSetupToken(home, credentialID, token) },
		PublishRefresh: publishSentinelSetupRefresh,
		HTTP:           &http.Client{Timeout: 15 * time.Second},
	}
	if manager := sentinel.GetSentinelManager(); manager != nil {
		deps.Manager = manager
	}
	return deps
}

// sentinelCredentialResolver reads a stored channel token from the encrypted
// credential store, mirroring notifier._credential (store first; the notifier
// applies the environment fallback itself). A store with no readable key
// resolves nothing rather than minting a fresh encryption key.
func sentinelCredentialResolver(home hivehome.Home) func(credentialID, envVar string) (string, bool) {
	return func(credentialID, _ string) (string, bool) {
		if home.Root == "" {
			return "", false
		}
		if key, _ := credentials.LoadCredentialKey(); key == "" {
			return "", false
		}
		store, err := credentials.WithEncryptedStorage(home.CredentialsDir())
		if err != nil {
			return "", false
		}
		value, err := store.Get(credentialID)
		if err != nil || value == nil || *value == "" {
			return "", false
		}
		return *value, true
	}
}

// saveSentinelSetupToken stores one channel token exactly like the desktop
// "Connect" step: credential id slack / slack_app / telegram, value under the
// access_token key. The store → environment bridge then re-exports it so the
// in-process notifier and the Sentinel manager pick the token up this session —
// the Go equivalent of upstream's adapter-cache reset.
func saveSentinelSetupToken(home hivehome.Home, credentialID, token string) error {
	_, _ = credentials.LoadCredentialKey()
	store, err := credentials.WithEncryptedStorage(home.CredentialsDir())
	if err != nil {
		return err
	}
	credential := credentials.NewCredentialObject(credentialID)
	credential.Keys.Set(credentials.NewCredentialKey("access_token", token))
	if err := store.SaveCredential(credential); err != nil {
		return err
	}
	credentials.SyncEnvFromStore(home.CredentialsDir(), sentinelSetupEnvBindings())
	return nil
}

// sentinelSetupEnvBindings maps the three Sentinel tokens onto the environment
// variables the notifier reads, matching the settings catalog's env names.
func sentinelSetupEnvBindings() []credentials.EnvBinding {
	return []credentials.EnvBinding{
		{CredentialID: "telegram", EnvVar: "TELEGRAM_BOT_TOKEN", CredentialKey: "access_token"},
		{CredentialID: "slack", EnvVar: "SLACK_BOT_TOKEN", CredentialKey: "access_token"},
		{CredentialID: "slack_app", EnvVar: "SLACK_APP_TOKEN", CredentialKey: "access_token"},
	}
}

// publishSentinelSetupRefresh emits the process-wide tool_catalog_refreshed
// event the Sentinel tab (and credential surfaces) refetch on. Best-effort:
// PublishGlobal already contains its own failure guard.
func publishSentinelSetupRefresh() {
	eventbus.PublishGlobal(&eventbus.AgentEvent{
		Type:     eventbus.EventToolCatalogRefreshed,
		StreamID: "global",
		Data:     map[string]any{"trigger": "sentinel_setup"},
	})
}
