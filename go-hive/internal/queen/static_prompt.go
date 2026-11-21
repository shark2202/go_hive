package queen

// The queen's static system prompt: phase composition plus the ambient
// credentials block.
//
// Upstream assembles it in two places and this file mirrors both:
//
//	QueenPhaseState.get_static_prompt  -- identity, soul, phase base prompt,
//	                                      credentials, mcp_cli, protocols,
//	                                      joined with a blank line;
//	create_queen                       -- which segments compose each phase.
//
// Why it matters that this is not the generic agent prompt builder: the queen
// path never runs tool-gated skill pre-activation (upstream bypasses
// build_prompt_spec entirely by passing get_static_prompt as the loop's
// dynamic_prompt_provider). The browser-automation and terminal-tools
// foundation skills therefore stay catalog entries she reads on demand instead
// of ~60 KB pasted into her cached prefix. The `# Tools` section below is where
// upstream points her at them.

import (
	"fmt"
	"sort"
	"strings"
)

// Queen phases. Anything else is treated as independent, matching upstream's
// `if self.phase == "colony": base = prompt_colony else: base =
// prompt_independent`.
const (
	PhaseIndependent = "independent"
	PhaseColony      = "colony"
)

// IndependentPhasePrompt is the INDEPENDENT-phase base prompt.
//
// Parity: framework/server/queen_orchestrator.py::create_queen --
// character core + role + tool docs + shared rules + independent-only rules.
func IndependentPhasePrompt() string {
	return queenCharacterCore +
		queenRoleIndependent +
		queenToolsIndependent +
		queenBehaviorAlways +
		queenBehaviorIndependent
}

// ColonyPhasePrompt is the COLONY-phase base prompt.
//
// The shared rules sit last here on purpose (upstream keeps
// `_queen_behavior_colony` ahead of `_queen_behavior_always` so the
// colony-specific "first step is tracker_sql CREATE TABLE" anchor reads before
// the generic "task_create FIRST" default).
//
// Parity: framework/server/queen_orchestrator.py::create_queen
func ColonyPhasePrompt() string {
	return queenCharacterCore +
		queenRoleColony +
		queenToolsColony +
		queenBehaviorColony +
		queenBehaviorAlways
}

// PhasePrompt returns the base prompt for one phase.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func PhasePrompt(phase string) string {
	if phase == PhaseColony {
		return ColonyPhasePrompt()
	}
	return IndependentPhasePrompt()
}

// StaticPromptOptions are the inputs of the queen's cache-stable prefix.
type StaticPromptOptions struct {
	// Identity is the rendered queen persona (identity + soul, in the order
	// upstream appends them).
	Identity string
	// Soul is her own self-description, appended under the identity. Callers
	// that already folded it into Identity leave this empty.
	Soul string
	// Base is the phase prompt (PhasePrompt).
	Base string
	// Credentials is the ambient "# Credentials" block
	// (CredentialsSummaryBlock).
	Credentials string
	// MCPCLI is the `hive mcp run` server list, empty when no MCP CLI servers
	// are configured.
	MCPCLI string
	// Protocols is the default skill protocols block, empty under progressive
	// disclosure (the current upstream default).
	Protocols string
}

// StaticPrompt joins the queen's static segments the way upstream does: every
// optional segment is dropped when empty, the phase base prompt is always
// present, and segments are separated by one blank line.
//
// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState.get_static_prompt
func StaticPrompt(opts StaticPromptOptions) string {
	parts := make([]string, 0, 6)
	if opts.Identity != "" {
		parts = append(parts, opts.Identity)
	}
	if opts.Soul != "" {
		parts = append(parts, opts.Soul)
	}
	parts = append(parts, opts.Base)
	if opts.Credentials != "" {
		parts = append(parts, opts.Credentials)
	}
	if opts.MCPCLI != "" {
		parts = append(parts, opts.MCPCLI)
	}
	if opts.Protocols != "" {
		parts = append(parts, opts.Protocols)
	}
	return strings.Join(parts, "\n\n")
}

// credentialsNoAccountsText is the block a queen with nothing connected reads.
// Generated from upstream's build_credentials_summary([]).
const credentialsNoAccountsText = "# Credentials\n" +
	"No credentials are connected yet. Use the `credentials` tool " +
	"(action=collect) to add one, or run `hive-cred browse --json` in the " +
	"terminal to see what could be connected."

// credentialsConnectedTail follows the provider summary when at least one
// account is connected.
const credentialsConnectedTail = "In the terminal: `hive-cred browse --json` lists " +
	"aliases and identities, `hive-cred token <id> --account <alias>` gives a " +
	"script a FRESH value (call it at request time; never paste a token into a " +
	"file). Use the `credentials` tool to collect new ones (action=collect) and " +
	"to pin the ones you'll reuse this session (action=attach)."

// CredentialsSummaryBlock renders the compact connected-credentials section.
//
// Provider names plus counts only: enough for her to know what exists without
// dumping every alias into the cached prefix. Full per-account detail is
// re-injected only for credentials the user pins to the session.
//
// Parity: framework/orchestrator/prompting.py::build_credentials_summary
func CredentialsSummaryBlock(accounts []map[string]any) string {
	if len(accounts) == 0 {
		return credentialsNoAccountsText
	}
	counts := make(map[string]int, len(accounts))
	for _, account := range accounts {
		provider := accountText(account, "provider")
		if provider == "" {
			provider = accountText(account, "credential_id")
		}
		if provider == "" {
			provider = "unknown"
		}
		counts[provider]++
	}
	providers := make([]string, 0, len(counts))
	for provider := range counts {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	labels := make([]string, 0, len(providers))
	for _, provider := range providers {
		if counts[provider] > 1 {
			labels = append(labels, fmt.Sprintf("%s (%d)", provider, counts[provider]))
			continue
		}
		labels = append(labels, provider)
	}
	return "# Credentials\nConnected: " + strings.Join(labels, ", ") + ".\n" + credentialsConnectedTail
}

// accountText is Python's truthiness-aware str() of one account field: "" for
// a missing value, empty string or nil, and the value's string form otherwise.
func accountText(account map[string]any, key string) string {
	if account == nil {
		return ""
	}
	value, ok := account[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
