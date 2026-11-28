package pipeline

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// BuildAccountsPrompt renders the connected-accounts detail block for callers
// outside the pipeline (the chat prompt's pinned-credentials section). It is
// the same simple provider-grouped shape upstream passes to
// build_accounts_prompt(accounts) with no tool map.
//
// Parity: core/framework/orchestrator/prompting.py::build_accounts_prompt
func BuildAccountsPrompt(accounts []map[string]any) string {
	return buildAccountsPrompt(accounts, nil, nil)
}

// buildAccountsPrompt renders the connected-accounts system-prompt block.
//
// Format: a `# Connected integrations` heading, then one block per provider.
// Each provider header names the tools that accept an `account=` argument;
// each account is listed alias-first with the alias wrapped in double quotes so
// the model treats it as a literal identifier. A multi-account provider appends
// a disambiguation note. Pure data — behavioral guidance lives in the node's
// planning knowledge, not here.
//
// A nil toolProviderMap keeps the simple provider-grouped shape (no tool list).
// A non-nil nodeToolNames filters each provider's tools to the ones the node
// actually has.
//
// Parity: core/framework/orchestrator/prompting.py::build_accounts_prompt
func buildAccountsPrompt(accounts []map[string]any, toolProviderMap map[string]string, nodeToolNames []string) string {
	if len(accounts) == 0 {
		return ""
	}

	// Python's dict preserves account order, so a provider's position in the
	// block list is the first account's position.
	type providerGroup struct {
		provider string
		accounts []map[string]any
	}
	groups := make([]providerGroup, 0, len(accounts))
	index := map[string]int{}
	for _, account := range accounts {
		provider := "unknown"
		if value, ok := account["provider"]; ok {
			provider = fmt.Sprint(value)
		}
		if position, ok := index[provider]; ok {
			groups[position].accounts = append(groups[position].accounts, account)
			continue
		}
		index[provider] = len(groups)
		groups = append(groups, providerGroup{provider: provider, accounts: []map[string]any{account}})
	}

	multiAccountNote := "\nWhen a provider below has multiple accounts, ask the user which one to use and list the options — do not guess."

	// Simple path: no tool map — just group accounts by provider.
	if toolProviderMap == nil {
		sections := []string{"# Connected integrations"}
		for _, group := range groups {
			sections = append(sections, "\n"+group.provider)
			for _, account := range group.accounts {
				sections = append(sections, credentials.FormatAccountLineWith(account, false, false, "unknown"))
			}
		}
		for _, group := range groups {
			if len(group.accounts) > 1 {
				sections = append(sections, multiAccountNote)
				break
			}
		}
		return strings.Join(sections, "\n")
	}

	providerTools := map[string][]string{}
	for toolName, provider := range toolProviderMap {
		providerTools[provider] = append(providerTools[provider], toolName)
	}
	for provider := range providerTools {
		sort.Strings(providerTools[provider])
	}

	var nodeToolSet map[string]struct{}
	if len(nodeToolNames) > 0 {
		nodeToolSet = make(map[string]struct{}, len(nodeToolNames))
		for _, name := range nodeToolNames {
			nodeToolSet[name] = struct{}{}
		}
	}

	sections := []string{"# Connected integrations"}
	hasMultiAccount := false
	for _, group := range groups {
		toolsForProvider := append([]string(nil), providerTools[group.provider]...)
		if nodeToolSet != nil {
			filtered := toolsForProvider[:0]
			for _, name := range toolsForProvider {
				if _, ok := nodeToolSet[name]; ok {
					filtered = append(filtered, name)
				}
			}
			toolsForProvider = filtered
			if len(toolsForProvider) == 0 {
				continue
			}
		}

		allLocal := true
		for _, account := range group.accounts {
			if source, ok := account["source"]; !ok || source != "local" {
				allLocal = false
				break
			}
		}
		toolsText := strings.Join(toolsForProvider, ", ")
		headerSuffix := ""
		if len(toolsForProvider) > 0 && !allLocal {
			headerSuffix = ` (use account="<alias>" with: ` + toolsText + ")"
		} else if len(toolsForProvider) > 0 && allLocal {
			headerSuffix = " (tools: " + toolsText + ")"
		}

		sections = append(sections, "\n"+group.provider+headerSuffix)
		for _, account := range group.accounts {
			sections = append(sections, credentials.FormatAccountLineWith(account, false, false, "unknown"))
		}
		if len(group.accounts) > 1 {
			hasMultiAccount = true
		}
	}
	if len(sections) <= 1 {
		return ""
	}
	if hasMultiAccount {
		sections = append(sections, multiAccountNote)
	}
	return strings.Join(sections, "\n")
}
