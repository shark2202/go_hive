package cli

// The stdio MCP server a Claude Code queen's runs are given: list_queens and
// message_queen, authenticated with the conversation token in the environment
// (HIVE_QUEEN_MAIL_URL/TOKEN/FROM). The runner writes its --mcp-config, which
// starts `hive queen-mail-mcp`.

import (
	"fmt"
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/queenmail"
)

// queenMailMCPCommands is the hidden plumbing command; it is not part of a
// user's workflow, but must stay on the binary a queen's runs start.
func (r *Runner) queenMailMCPCommands() []*commandSpec {
	return []*commandSpec{{
		Prog: "hive queen-mail-mcp",
		Help: "Serve the hive MCP server (list_queens, message_queen) on stdio",
		Description: "Runs the MCP server a Claude Code queen's runs are given, writing through " +
			"HIVE_QUEEN_MAIL_URL with HIVE_QUEEN_MAIL_TOKEN as the conversation HIVE_QUEEN_MAIL_FROM.",
		Dest: "queen_mail_mcp",
		// Internal extension with no Python CLI counterpart: hidden from
		// help/usage so `hive --help` matches core/framework/cli.py, while the
		// verb itself stays fully callable (the queen-mail runner needs it).
		Hidden: true,
		Run: func(*parsedArgs) int {
			if err := queenmail.ServeQueenMailMCP(r.Stdin, r.Stdout, os.Getenv); err != nil {
				fmt.Fprintf(r.Stderr, "queen-mail-mcp: %v\n", err)
				return 1
			}
			return 0
		},
	}}
}
