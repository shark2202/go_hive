package cli

// Pinned-credential prompt provider: the full per-account detail of the
// credentials this session pinned with credentials(action=attach).
//
// Upstream's queen_orchestrator._build_credentials_provider snapshots the
// connected accounts once and re-reads the session attachments on every call,
// so an attach/detach shows up on the very next turn. The Go port keeps the
// same split: the account snapshot comes from the shared credential adapter
// (sessionCredentialsProvider) and the refs come from the session directory.

import (
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/pipeline"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// sessionPinnedCredentialsProvider renders the "Pinned to this session:"
// detail, or "" when nothing is pinned or no pinned account is connected.
//
// Pins are read from upstream's global `$HIVE_HOME/sessions/<session_id>` with
// the chat tree's session directory as the legacy fallback.
//
// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
func sessionPinnedCredentialsProvider(home hivehome.Home, sessionID, sessionDir string) func() string {
	accounts := sessionCredentialsProvider(home)
	return func() string {
		refs := tools.ReadCredentialAttachmentsForSession(home.Root, sessionID, sessionDir)
		if len(refs) == 0 {
			return ""
		}
		attached := make([]map[string]any, 0, len(refs))
		for _, account := range accounts() {
			if tools.CredentialAttachmentMatches(account, refs) {
				attached = append(attached, account)
			}
		}
		if len(attached) == 0 {
			return ""
		}
		return pipeline.BuildAccountsPrompt(attached)
	}
}
