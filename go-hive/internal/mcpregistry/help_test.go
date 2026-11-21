package mcpregistry

import (
	"strings"
	"testing"
)

// collapseHelp flattens wrapped lines so per-choice summaries can be matched
// regardless of the terminal width the page was rendered at.
func collapseHelp(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// TestMCPVerbHelpTopics pins the per-verb help pages: each verb renders its own
// usage/positionals/options page (upstream registers all 14 verbs as argparse
// subparsers), and the group page lists the full choice set.
//
// Parity: tests/test_mcp_registry_cli.py::test_register_mcp_commands_creates_all_subcommands
func TestMCPVerbHelpTopics(t *testing.T) {
	group := MCPHelpText()
	if !strings.Contains(group, "usage: hive mcp [-h]") {
		t.Fatalf("group usage = %q", group)
	}
	if !strings.Contains(group, "{install,add,mode,run,remove,enable,disable,list,info,config,search,health,init,update}") {
		t.Fatalf("group page lacks the argparse choice set:\n%s", group)
	}
	for _, verb := range mcpVerbOrder {
		page := MCPVerbHelpText(verb)
		if !strings.Contains(page, "usage: hive mcp "+verb) {
			t.Fatalf("%s page lacks its own usage:\n%s", verb, page)
		}
		if !strings.Contains(page, "options:\n  -h, --help") {
			t.Fatalf("%s page lacks the options block:\n%s", verb, page)
		}
		if !strings.Contains(collapseHelp(group), mcpVerbSummary[verb]) {
			t.Fatalf("group page lacks the %s summary", verb)
		}
	}
	if !strings.Contains(MCPVerbHelpText("run"), "Run an installed MCP server's tools as commands") {
		t.Fatalf("run page lacks its description:\n%s", MCPVerbHelpText("run"))
	}
	if !strings.Contains(MCPVerbHelpText("mode"), "{cli,tools}") {
		t.Fatalf("mode page lacks the positional choice set:\n%s", MCPVerbHelpText("mode"))
	}
	if got := MCPVerbHelpText("nosuchtopic"); got != group {
		t.Fatalf("unknown topic should fall back to the group page")
	}
}
