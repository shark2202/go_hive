package server

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/queen"
)

// chatQueenStaticPrompt assembles the session's cache-stable system prompt the
// way upstream's QueenPhaseState.get_static_prompt does: her identity and soul,
// the phase base prompt, then the ambient credentials block. The skills catalog
// and the searchable-tools manifest are NOT here — they ride the conversation
// as <system-reminder> blocks so they refresh near the latest turn without
// invalidating the cached prefix.
//
// The queen path deliberately does not go through
// agentloop.BuildSystemPromptForContext: that builder appends the tool-gated
// skill pre-activation (whole `browser-automation` / `terminal-tools-foundations`
// bodies) to the skills catalog, and upstream never runs it for a queen —
// get_static_prompt is handed to the loop as its dynamic_prompt_provider, which
// bypasses the generic spec entirely. Her tool guidance is the `# Tools`
// section of the base prompt plus the catalog entries she reads on demand.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
// Parity: framework/server/queen_orchestrator.py::create_queen
func chatQueenStaticPrompt(deps ChatRuntimeDeps, queenName string) string {
	return queen.StaticPrompt(queen.StaticPromptOptions{
		Identity:    chatQueenIdentityPrompt(deps.HomeRoot, queenName),
		Base:        queen.PhasePrompt(chatPhaseLabel(deps)),
		Credentials: chatCredentialsAmbientBlock(deps),
		MCPCLI:      chatMCPCLIPrompt(deps),
	})
}

// chatCredentialsAmbientBlock renders the "# Credentials" section: the compact
// provider summary, plus — when the session pinned credentials with the
// credentials tool — their full per-account detail under upstream's
// "Pinned to this session:" heading. The pinned read happens at every prompt
// build, so attach/detach is visible on the next model request.
//
// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
func chatCredentialsAmbientBlock(deps ChatRuntimeDeps) string {
	block := queen.CredentialsSummaryBlock(chatConnectedAccounts(deps))
	if deps.PinnedCredentials == nil {
		return block
	}
	pinned := strings.TrimSpace(deps.PinnedCredentials())
	if pinned == "" {
		return block
	}
	return block + "\n\nPinned to this session:\n" + pinned
}

// chatPhaseLabel reads the session's live phase, defaulting to independent when
// the deps carry no provider (an embedded host, a test).
func chatPhaseLabel(deps ChatRuntimeDeps) string {
	if deps.Phase != nil {
		if phase := strings.TrimSpace(deps.Phase()); phase != "" {
			return phase
		}
	}
	return queen.PhaseIndependent
}

// chatConnectedAccounts snapshots the connected accounts for the prompt's
// credentials block. Nil provider means nothing connected, which renders the
// "no credentials yet" text instead of dropping the section.
func chatConnectedAccounts(deps ChatRuntimeDeps) []map[string]any {
	if deps.Credentials == nil {
		return nil
	}
	return deps.Credentials()
}

// chatMCPCLIPrompt returns the command-line MCP server list, empty when the
// provider is absent or the registry has no CLI-mode server.
//
// Parity: framework/loader/mcp_cli.py::registry_cli_servers_prompt
func chatMCPCLIPrompt(deps ChatRuntimeDeps) string {
	if deps.MCPCLI == nil {
		return ""
	}
	return deps.MCPCLI()
}
