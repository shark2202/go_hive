// Command hive-browser drives the Hive browser through the standalone bridge.
//
// It is the CLI replacement for the retired `browser_*` MCP tools: every verb
// posts one command to the bridge's local agent API (127.0.0.1:14830) and exits
// with the taxonomy in internal/cli/browser_cli_client.go (0 ok, 2 not
// connected, 3 not started, 4 not found, 6 bad arguments, 7 pending dialog,
// 8 rate limited). Agents invoke it with --json.
//
// The bridge itself is owned by `hive serve` (or by running `browserbridge`
// directly); this binary is a pure client and never spawns one.
package main

import (
	"os"

	"github.com/aden-hive/hive-go/internal/cli"
)

func main() {
	os.Exit(cli.GuardMain(func() int { return cli.RunBrowserCLI(os.Args[1:], os.Stdout, os.Stderr) }))
}
