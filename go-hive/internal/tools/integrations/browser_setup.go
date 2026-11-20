package integrations

// browser_setup: the in-process browser discovery tool.
//
// The browser itself is driven from the terminal through the `hive-browser`
// CLI (which replaced the `browser_*` MCP tools). The runtime can only observe
// tool NAMES, not opaque `terminal_exec` subprocess calls — so this one static
// tool keeps the browser capability discoverable and gate-able:
//
//   - its name starts with `browser_`, so the skill gating still pre-activates
//     the foundational browser-automation skill whenever it is present;
//   - its return teaches the agent the CLI surface and the golden rule: drive
//     the browser with `terminal_exec` running `hive-browser <command> ...
//     --json`.
//
// It is read-only and executes no network calls, so both queens and workers may
// hold it. Port of core/framework/tools/browser_tools.py.

import (
	"github.com/aden-hive/hive-go/internal/tools"
)

// BrowserSetupToolName is the upstream tool name (browser_tools.py).
const BrowserSetupToolName = "browser_setup"

// browserSetupCommands is the `hive-browser` command surface, kept in sync
// with the CLI. Static on purpose: the tool stays decoupled from the browser
// bridge package and never shells out.
var browserSetupCommands = map[string]string{
	"setup":                       "check the extension/bridge, list connected Chrome profiles, show install steps",
	"status":                      "connection + running state for your tab group",
	"open <url>":                  "open a tab (cold-start entry point); --browser-profile <label> targets a Chrome account",
	"navigate <url>":              "redirect an existing tab (--tab, --wait-until)",
	"reload":                      "reload the current page",
	"interact --action <a>":       "click/type/key/scroll/drag/screenshot/zoom/wait (--selector or --coordinate x,y as 0..1 fractions)",
	"select <sel> --value V":      "choose option(s) in a <select>",
	"upload <sel> --file P":       "set files on a file input (--trigger-selector for native pickers)",
	"screenshot --intent <p>":     "capture the tab; the JPEG is saved and auto-attached to your session",
	"evaluate --js <src|@file|->": "run JavaScript in the page (value of last expression)",
	"script --skill S --script F.py --args '{...}'":       "run a skill-bundled run(ctx) orchestration script",
	"tab list|close|activate":                             "manage tabs in your group",
	"page html|snapshot|text|shadow-query|console|resize": "page reads + viewport (large reads spill to a file path)",
	"dialog respond accept|dismiss":                       "resolve a pending native dialog",
}

// BrowserSetupDescription is the model-facing description (browser_tools.py
// _BROWSER_SETUP_DESC).
const BrowserSetupDescription = "Get everything you need to drive the web browser. The browser is controlled " +
	"from the terminal via the `hive-browser` CLI — run its commands with " +
	"terminal_exec, ALWAYS with `--json`. Call this first before any browser work: " +
	"it returns the command surface and how to check the extension connection. " +
	"Then run `hive-browser setup --json` in the terminal to confirm the Hive " +
	"browser extension is connected (it returns the Chrome Web Store install steps " +
	"if not) and to list the connected Chrome profiles."

// InstallBrowserSetup publishes browser_setup into a shared registry and returns
// the number of tools registered. The tool executes in-process from a static
// table: no flags are read and nothing touches the network.
func InstallBrowserSetup(target *tools.ToolRegistry) int {
	if target == nil {
		return 0
	}
	if target.HasTool(BrowserSetupToolName) {
		return 0
	}
	tool := browserSetupTool()
	target.Register(BrowserSetupToolName, tool, func(inputs map[string]any) (any, error) {
		return browserSetupPayload(), nil
	})
	// Gateable like the other built-in tools: the browser categories and
	// allowlists name browser_setup, which they only see when it is recorded
	// under a group. RecordBuiltinTools merges members and saves the harness
	// record, mirroring upstream record_builtin_tools (a credential resync
	// keeps it; an existing "browser" group is extended, not replaced).
	target.RecordBuiltinTools("browser", []string{BrowserSetupToolName})
	return 1
}

// browserSetupTool is the registered/catalog Tool definition. It takes no
// inputs.
func browserSetupTool() tools.Tool {
	return tools.Tool{
		Name:            BrowserSetupToolName,
		Description:     BrowserSetupDescription,
		Parameters:      map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		ConcurrencySafe: true,
	}
}

// browserSetupPayload is the static discovery envelope. Field names and text
// mirror browser_tools.py so the CLI surface stays one contract across ports.
func browserSetupPayload() map[string]any {
	commands := make(map[string]string, len(browserSetupCommands))
	for name, description := range browserSetupCommands {
		commands[name] = description
	}
	return map[string]any{
		"ok":  true,
		"cli": "hive-browser",
		"how": "Drive the browser by running `hive-browser <command> ... --json` in the " +
			"terminal via terminal_exec. Always pass --json. Fallback if the console " +
			"script isn't found: `uv run --project tools python -m gcu.cli <command> ... --json`.",
		"first_step":  "Run `hive-browser setup --json` to verify the extension is connected and list Chrome profiles.",
		"commands":    commands,
		"coordinates": "click/hover/key coordinates are FRACTIONS of the viewport (0..1), not pixels.",
		"screenshots": "`hive-browser screenshot` (and `interact --action screenshot`) save a JPEG and " +
			"return a `saved_to` file path. The framework attaches the image to your NEXT turn " +
			"automatically — so do NOT also attach_file it. If you don't see the image inline, " +
			"read the `saved_to` path (the Read/file tool renders images) as a fallback.",
		"exit_codes": "0 ok · 2 not_connected · 3 not_started · 4 not_found · 6 bad_args · 7 pending_dialog · 8 rate_limited.",
	}
}
