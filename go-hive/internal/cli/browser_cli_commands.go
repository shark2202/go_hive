package cli

// Parity: tools/src/gcu/cli.py::_build_parser
//
// browser_cli_commands.go is the `hive-browser` verb tree. Every leaf maps onto
// one bridge command (`browser_*`) or a bridge control command; the bridge
// names, parameter names and defaults follow tools/src/gcu/browser/tools/*.py
// and the Go port's command table (browserbridge/commands*.go).

import (
	"fmt"
	"os"
	"strings"
)

// browserParams builds one command's parameter object; a non-zero return is the
// exit code for a client-side validation failure: browserExitUsage (2) for the
// checks argparse itself makes upstream (required flags, choices, typed
// values), browserExitBadArgs (6) for the Go-only validations upstream has no
// argparse equivalent for.
type browserParams func(*parsedArgs) (map[string]any, int)

// browserPost post-processes a successful result before it is printed.
type browserPost func(*parsedArgs, map[string]any)

// subcommands is the top-level verb list, in roughly the upstream order.
func (c *browserCLI) subcommands() []*commandSpec {
	specs := []*commandSpec{
		c.helpCommand(),
		c.setupCommand(),
		c.statusCommand(),
		c.stopCommand(),
		c.openCommand(),
		c.navigateCommand(),
		c.reloadCommand(),
		c.interactCommand(),
		c.selectCommand(),
		c.uploadCommand(),
		c.screenshotCommand(),
		c.evaluateCommand(),
		c.scriptCommand(),
		c.clickCommand(),
		c.typeCommand(),
		c.typeFocusedCommand(),
		c.pressCommand(),
		c.scrollCommand(),
		c.tabCommand(),
		c.pageCommand(),
		c.dialogCommand(),
	}
	return specs
}

// helpCommand is `hive-browser help [topic]` — pure local text, no bridge.
func (c *browserCLI) helpCommand() *commandSpec {
	return &commandSpec{
		Prog:  "hive-browser help",
		Help:  "usage for the CLI or one command: `hive-browser help [cmd]`",
		Flags: withCommon(),
		Positionals: []positionalSpec{
			{Dest: "topic", Help: "a command to show help for"},
		},
		Run: func(args *parsedArgs) int {
			topic := strings.TrimSpace(args.str("topic"))
			text := c.root.helpText()
			if topic != "" {
				found := findSubcommand(c.root, topic)
				if found == nil {
					fmt.Fprintf(c.stderr, "hive-browser: no such command %q — run `hive-browser help` for the full list\n", topic)
					// Upstream `_help` raises BrowserError outside `errors.run`
					// (cli.py:main calls _help directly), so an unknown topic
					// surfaces as an uncaught error: exit 1, not the argparse
					// usage code.
					return browserExitDomain
				}
				text = found.helpText()
			}
			if args.boolean("json") {
				writeJSONOut(c.stdout, map[string]any{"topic": orDefault(topic, "hive-browser"), "help": text}, true)
				return browserExitOK
			}
			fmt.Fprint(c.stdout, text)
			return browserExitOK
		},
	}
}

func orDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// --- lifecycle --------------------------------------------------------------

func (c *browserCLI) setupCommand() *commandSpec {
	return c.leaf("hive-browser setup", "check the extension/bridge and show install steps",
		nil, nil, "browser_setup", func(*parsedArgs) (map[string]any, int) {
			return map[string]any{}, browserExitOK
		}, nil)
}

func (c *browserCLI) statusCommand() *commandSpec {
	return c.leaf("hive-browser status", "connection + running state + connected Chrome profiles",
		[]argSpec{tabFlag()}, nil, "browser_status", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{}
			c.optTab(args, params)
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) stopCommand() *commandSpec {
	return c.leaf("hive-browser stop", "close this session's tab group (all its tabs)",
		nil, nil, "browser_stop", func(*parsedArgs) (map[string]any, int) {
			return map[string]any{}, browserExitOK
		}, func(args *parsedArgs, _ map[string]any) {
			clearBrowserCLIState(c.statePath(), c.profile(args))
		})
}

// --- navigation -------------------------------------------------------------

func (c *browserCLI) openCommand() *commandSpec {
	return c.leaf("hive-browser open", "open a tab at <url> (cold-start entry point)",
		[]argSpec{
			{Dest: "background", Long: "background", Kind: kindBool, Help: "open in the background"},
			browserProfileFlag(),
		},
		[]positionalSpec{{Dest: "url", Required: true, Help: "URL to open"}},
		"browser_open", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{
				"url":        args.str("url"),
				"background": args.boolean("background"),
			}
			if label := strings.TrimSpace(args.str("browser_profile")); label != "" {
				params["browser_profile"] = label
			}
			return params, browserExitOK
		}, func(args *parsedArgs, result map[string]any) {
			if tabID := intOf(result["tabId"]); tabID > 0 {
				saveBrowserCLIState(c.statePath(), c.profile(args), tabID, stringOf(result["browser_profile"]))
			}
		})
}

func (c *browserCLI) navigateCommand() *commandSpec {
	return c.leaf("hive-browser navigate", "redirect an existing tab to <url>",
		[]argSpec{
			tabFlag(),
			{Dest: "wait_until", Long: "wait-until", Kind: kindString, Default: "load",
				Help: "commit | domcontentloaded | load | networkidle"},
			timeoutFlag(30000, "how long to wait for the --wait-until condition (ms)"),
			browserProfileFlag(),
		},
		[]positionalSpec{{Dest: "url", Required: true, Help: "URL to navigate to"}},
		"browser_navigate", func(args *parsedArgs) (map[string]any, int) {
			waitUntil := strings.TrimSpace(args.str("wait_until"))
			if !containsStringValue(browserWaitUntil, waitUntil) {
				fmt.Fprintf(c.stderr, "hive-browser: unknown --wait-until %q (choose from %s)\n",
					waitUntil, strings.Join(browserWaitUntil, ", "))
				return nil, browserExitUsage
			}
			params := map[string]any{
				"url":        args.str("url"),
				"wait_until": waitUntil,
				"timeout_ms": args.intValue("timeout_ms"),
			}
			if label := strings.TrimSpace(args.str("browser_profile")); label != "" {
				params["browser_profile"] = label
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) reloadCommand() *commandSpec {
	return c.leaf("hive-browser reload", "reload the current page",
		[]argSpec{tabFlag()}, nil, "browser_reload", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

// --- interaction ------------------------------------------------------------

var browserInteractActions = []string{
	"left_click", "right_click", "middle_click", "double_click", "triple_click",
	"hover", "type", "key", "scroll", "drag", "screenshot", "zoom", "wait",
}

var browserSnapshotModes = []string{"default", "simple", "interactive"}

var browserAutoSnapshotModes = []string{"simple", "default", "interactive", "off"}

// browserScrollDirections / browserWaitUntil are the upstream argparse choices
// for `interact --scroll-direction` and `navigate --wait-until`.
var browserScrollDirections = []string{"up", "down", "left", "right"}

var browserWaitUntil = []string{"commit", "domcontentloaded", "load", "networkidle"}

func (c *browserCLI) interactCommand() *commandSpec {
	flags := []argSpec{
		{Dest: "action", Long: "action", Kind: kindString, Help: "one of: " + strings.Join(browserInteractActions, ", ")},
		tabFlag(),
		{Dest: "selector", Long: "selector", Kind: kindString, Help: "CSS selector target"},
		{Dest: "coordinate", Long: "coordinate", Kind: kindString, Help: "[x,y] viewport fractions 0..1"},
		{Dest: "start_selector", Long: "start-selector", Kind: kindString, Help: "drag start selector"},
		{Dest: "start_coordinate", Long: "start-coordinate", Kind: kindString, Help: "[x,y] drag start fractions"},
		{Dest: "text", Long: "text", Kind: kindString, Help: "text to type"},
		{Dest: "no_clear_first", Long: "no-clear-first", Kind: kindBool, Help: "type without clearing first"},
		{Dest: "no_insert_text", Long: "no-insert-text", Kind: kindBool, Help: "type by key events instead of insertText"},
		{Dest: "modifiers", Long: "modifiers", Kind: kindString, Help: "e.g. Meta+A"},
		{Dest: "repeat", Long: "repeat", Kind: kindInt, Default: 1, Help: "key repeat count"},
		{Dest: "scroll_direction", Long: "scroll-direction", Kind: kindString, Default: "down",
			Help: "up | down | left | right"},
		{Dest: "scroll_amount", Long: "scroll-amount", Kind: kindInt, Default: 500, Help: "scroll distance in px"},
		{Dest: "intent", Long: "intent", Kind: kindString, Help: "why this action (screenshot/zoom)"},
		{Dest: "full_page", Long: "full-page", Kind: kindBool, Help: "screenshot the document, not the viewport"},
		{Dest: "no_annotate", Long: "no-annotate", Kind: kindBool, Help: "skip screenshot annotations"},
		{Dest: "region", Long: "region", Kind: kindString, Help: "[x0,y0,x1,y1] fractions (zoom)"},
		{Dest: "duration", Long: "duration", Kind: kindString, Help: "seconds (wait)"},
		{Dest: "wait_for_selector", Long: "wait-for-selector", Kind: kindString, Help: "wait until a selector exists"},
		{Dest: "wait_for_text", Long: "wait-for-text", Kind: kindString, Help: "wait until text appears"},
		timeoutFlag(0, "how long the action may take (ms)"),
		{Dest: "auto_snapshot_mode", Long: "auto-snapshot-mode", Kind: kindString, Default: "simple",
			Help: "simple | default | interactive | off"},
		{Dest: "wait_after_ms", Long: "wait-after-ms", Kind: kindInt, Help: "settle time after the action (ms)"},
	}
	return c.leaf("hive-browser interact", "click / type / key / scroll / drag / screenshot / zoom / wait",
		flags, nil, "browser_interact", func(args *parsedArgs) (map[string]any, int) {
			action := strings.TrimSpace(args.str("action"))
			if action == "" {
				fmt.Fprintln(c.stderr, "hive-browser: --action is required")
				return nil, browserExitUsage
			}
			if !containsStringValue(browserInteractActions, action) {
				fmt.Fprintf(c.stderr, "hive-browser: unknown action %q (choose from %s)\n",
					action, strings.Join(browserInteractActions, ", "))
				return nil, browserExitUsage
			}
			// --intent is not validated here: upstream's CLI passes it through
			// and the tool layer answers `screenshot requires intent` with
			// ok:false, which the taxonomy maps to exit 1 — same path, same
			// code. (zoom does not take an intent upstream at all.)
			if direction := strings.TrimSpace(args.str("scroll_direction")); !containsStringValue(browserScrollDirections, direction) {
				fmt.Fprintf(c.stderr, "hive-browser: unknown --scroll-direction %q (choose from %s)\n",
					direction, strings.Join(browserScrollDirections, ", "))
				return nil, browserExitUsage
			}
			if mode := strings.TrimSpace(args.str("auto_snapshot_mode")); !containsStringValue(browserAutoSnapshotModes, mode) {
				fmt.Fprintf(c.stderr, "hive-browser: unknown --auto-snapshot-mode %q (choose from %s)\n",
					mode, strings.Join(browserAutoSnapshotModes, ", "))
				return nil, browserExitUsage
			}
			params := map[string]any{
				"action":             action,
				"repeat":             args.intValue("repeat"),
				"scroll_direction":   args.str("scroll_direction"),
				"scroll_amount":      args.intValue("scroll_amount"),
				"auto_snapshot_mode": args.str("auto_snapshot_mode"),
				"clear_first":        !args.boolean("no_clear_first"),
				"use_insert_text":    !args.boolean("no_insert_text"),
			}
			if args.wasProvided("modifiers") {
				params["modifiers"] = args.str("modifiers")
			}
			if args.wasProvided("timeout_ms") {
				params["timeout_ms"] = args.intValue("timeout_ms")
			}
			if args.wasProvided("wait_after_ms") {
				params["wait_after_ms"] = args.intValue("wait_after_ms")
			}
			for cliKey, bridgeKey := range map[string]string{
				"selector": "selector", "text": "text", "intent": "intent",
				"wait_for_selector": "wait_for_selector", "wait_for_text": "wait_for_text",
				"start_selector": "start_selector",
			} {
				if args.wasProvided(cliKey) {
					params[bridgeKey] = args.str(cliKey)
				}
			}
			for cliKey, bridgeKey := range map[string]string{
				"coordinate": "coordinate", "start_coordinate": "start_coordinate",
			} {
				if !args.wasProvided(cliKey) {
					continue
				}
				list, err := parseFloatList(args.str(cliKey), 2, "--"+strings.ReplaceAll(cliKey, "_", "-"))
				if err != nil {
					fmt.Fprintf(c.stderr, "hive-browser: %v\n", err)
					return nil, browserExitUsage
				}
				params[bridgeKey] = list
			}
			if args.wasProvided("region") {
				list, err := parseFloatList(args.str("region"), 4, "--region")
				if err != nil {
					fmt.Fprintf(c.stderr, "hive-browser: %v\n", err)
					return nil, browserExitUsage
				}
				params["region"] = list
			}
			if args.wasProvided("duration") {
				seconds, err := parseFloatValue(args.str("duration"))
				if err != nil {
					fmt.Fprintf(c.stderr, "hive-browser: --duration: %v\n", err)
					return nil, browserExitUsage
				}
				params["duration"] = seconds
			}
			if args.boolean("full_page") {
				params["full_page"] = true
			}
			params["annotate"] = !args.boolean("no_annotate")
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) selectCommand() *commandSpec {
	return c.leaf("hive-browser select", "select option(s) in a <select> dropdown",
		[]argSpec{
			{Dest: "value", Long: "value", Kind: kindAppend, Default: []string{}, Help: "option value (repeatable)"},
			tabFlag(),
		},
		[]positionalSpec{{Dest: "selector", Required: true, Help: "selector of the <select>"}},
		"browser_select", func(args *parsedArgs) (map[string]any, int) {
			values := args.list("value")
			if len(values) == 0 {
				fmt.Fprintln(c.stderr, "hive-browser: --value is required (repeatable)")
				return nil, browserExitUsage
			}
			params := map[string]any{"selector": args.str("selector"), "values": values}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) uploadCommand() *commandSpec {
	return c.leaf("hive-browser upload", "set files on a file input (direct or via trigger)",
		[]argSpec{
			{Dest: "file", Long: "file", Kind: kindAppend, Default: []string{}, Help: "file path (repeatable)"},
			{Dest: "trigger_selector", Long: "trigger-selector", Kind: kindString, Help: "element to click first"},
			timeoutFlag(30000, "how long to wait for the file input (ms)"),
			tabFlag(),
		},
		[]positionalSpec{{Dest: "selector", Required: true, Help: "selector of the file input"}},
		"browser_upload", func(args *parsedArgs) (map[string]any, int) {
			files := args.list("file")
			if len(files) == 0 {
				fmt.Fprintln(c.stderr, "hive-browser: --file is required (repeatable)")
				return nil, browserExitUsage
			}
			for _, path := range files {
				// Upstream reads the files in the handler and answers
				// ok:false → exit 1; the Go CLI checks locally and keeps its
				// own bad-args code (6), documented in browser-host.md.
				if info, err := os.Stat(expandUser(path)); err != nil || info.IsDir() {
					fmt.Fprintf(c.stderr, "hive-browser: file not found: %s\n", path)
					return nil, browserExitBadArgs
				}
			}
			params := map[string]any{
				"selector":   args.str("selector"),
				"file_paths": files,
				"timeout_ms": args.intValue("timeout_ms"),
			}
			if trigger := strings.TrimSpace(args.str("trigger_selector")); trigger != "" {
				params["trigger_selector"] = trigger
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

// --- capture / evaluate / script -------------------------------------------

func (c *browserCLI) screenshotCommand() *commandSpec {
	return c.leaf("hive-browser screenshot", "capture the tab (JPEG spilled to a file, path returned)",
		[]argSpec{
			{Dest: "intent", Long: "intent", Kind: kindString, Help: "why this screenshot"},
			{Dest: "full_page", Long: "full-page", Kind: kindBool, Help: "capture the document region"},
			{Dest: "selector", Long: "selector", Kind: kindString, Help: "element to wait for and capture"},
			{Dest: "no_annotate", Long: "no-annotate", Kind: kindBool, Help: "skip annotations"},
			timeoutFlag(5000, "selector wait (ms) when --selector is given"),
			tabFlag(),
		}, nil, "browser_screenshot", func(args *parsedArgs) (map[string]any, int) {
			// Upstream's standalone `screenshot` verb passes --intent straight
			// to render_screenshot(intent=None), so omitting it still captures
			// (exit 0 when connected); only the retired MCP tool schema made it
			// mandatory. Match the CLI path here instead of pre-checking.
			params := map[string]any{
				"full_page":  args.boolean("full_page"),
				"annotate":   !args.boolean("no_annotate"),
				"timeout_ms": args.intValue("timeout_ms"),
			}
			if args.wasProvided("intent") {
				params["intent"] = args.str("intent")
			}
			if args.wasProvided("selector") {
				params["selector"] = args.str("selector")
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) evaluateCommand() *commandSpec {
	return c.leaf("hive-browser evaluate", "run JavaScript in the page (value of last expression)",
		[]argSpec{
			{Dest: "js", Long: "js", Kind: kindString, Help: "JS source: inline, @FILE, or - for stdin"},
			{Dest: "script", Long: "script", Kind: kindString, Help: "alias for --js"},
			tabFlag(),
		}, nil, "browser_evaluate", func(args *parsedArgs) (map[string]any, int) {
			source := args.str("js")
			if !args.wasProvided("js") {
				source = args.str("script")
			}
			js, err := c.readScriptSource(source)
			if err != nil {
				fmt.Fprintf(c.stderr, "hive-browser: %v\n", err)
				return nil, browserExitBadArgs
			}
			params := map[string]any{"script": js}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) scriptCommand() *commandSpec {
	return c.leaf("hive-browser script", "run a skill-bundled run(ctx) orchestration script",
		[]argSpec{
			{Dest: "skill", Long: "skill", Kind: kindString, Help: "skill name or dir (e.g. hive.linkedin-core)"},
			{Dest: "script", Long: "script", Kind: kindString, Help: "a script under <skill>/scripts/"},
			{Dest: "args", Long: "args", Kind: kindString, Help: "ctx.args as JSON (inline, @file, or -)"},
			{Dest: "timeout_s", Long: "timeout-s", Kind: kindString, Default: "60", Help: "script timeout (s)"},
			browserProfileFlag(),
			tabFlag(),
		}, nil, "browser_script", func(args *parsedArgs) (map[string]any, int) {
			skill := strings.TrimSpace(args.str("skill"))
			script := strings.TrimSpace(args.str("script"))
			if skill == "" || script == "" {
				fmt.Fprintln(c.stderr, "hive-browser: --skill and --script are required "+
					"(ad-hoc JS is `hive-browser evaluate --js`)")
				return nil, browserExitBadArgs
			}
			timeout, err := parseFloatValue(args.str("timeout_s"))
			if err != nil {
				fmt.Fprintf(c.stderr, "hive-browser: --timeout-s: %v\n", err)
				return nil, browserExitUsage
			}
			params := map[string]any{"skill": skill, "script": script, "timeout_s": timeout}
			scriptArgs, err := c.decodeJSONObject(args.str("args"), "--args")
			if err != nil {
				fmt.Fprintf(c.stderr, "hive-browser: %v\n", err)
				return nil, browserExitBadArgs
			}
			if scriptArgs != nil {
				params["args"] = scriptArgs
			}
			if label := strings.TrimSpace(args.str("browser_profile")); label != "" {
				params["browser_profile"] = label
			}
			c.optTab(args, params)
			return params, browserExitOK
		}, nil)
}

// --- bridge-level verbs (beyond the 22 MCP tools) ---------------------------

func (c *browserCLI) clickCommand() *commandSpec {
	return c.leaf("hive-browser click", "click an element via CDP (browser_click)",
		[]argSpec{
			{Dest: "button", Long: "button", Kind: kindString, Default: "left", Help: "left | right | middle"},
			{Dest: "double", Long: "double", Kind: kindBool, Help: "double click"},
			{Dest: "auto_snapshot_mode", Long: "auto-snapshot-mode", Kind: kindString, Default: "simple",
				Help: "simple | default | interactive | off"},
			tabFlag(),
		},
		[]positionalSpec{{Dest: "selector", Required: true, Help: "CSS selector"}},
		"browser_click", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{
				"selector":           args.str("selector"),
				"button":             args.str("button"),
				"double_click":       args.boolean("double"),
				"auto_snapshot_mode": args.str("auto_snapshot_mode"),
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) typeCommand() *commandSpec {
	return c.leaf("hive-browser type", "type into an element via CDP (browser_type)",
		append(typeFlags(), tabFlag()), []positionalSpec{{Dest: "selector", Required: true, Help: "CSS selector"}},
		"browser_type", func(args *parsedArgs) (map[string]any, int) {
			params, code := typeParams(args)
			if code != browserExitOK {
				return nil, code
			}
			params["selector"] = args.str("selector")
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) typeFocusedCommand() *commandSpec {
	return c.leaf("hive-browser type-focused", "type into the focused element (browser_type_focused)",
		append(typeFlags(), tabFlag()), nil, "browser_type_focused", func(args *parsedArgs) (map[string]any, int) {
			params, code := typeParams(args)
			if code != browserExitOK {
				return nil, code
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func typeFlags() []argSpec {
	return []argSpec{
		{Dest: "text", Long: "text", Kind: kindString, Help: "text to type"},
		{Dest: "no_clear_first", Long: "no-clear-first", Kind: kindBool, Help: "type without clearing first"},
		{Dest: "no_insert_text", Long: "no-insert-text", Kind: kindBool, Help: "use key events instead of insertText"},
		{Dest: "delay_ms", Long: "delay-ms", Kind: kindInt, Default: 1, Help: "per-character delay (ms)"},
		timeoutFlag(30000, "how long typing may take (ms)"),
	}
}

func typeParams(args *parsedArgs) (map[string]any, int) {
	if !args.wasProvided("text") {
		return nil, browserExitBadArgs
	}
	return map[string]any{
		"text":            args.str("text"),
		"clear_first":     !args.boolean("no_clear_first"),
		"use_insert_text": !args.boolean("no_insert_text"),
		"delay_ms":        args.intValue("delay_ms"),
		"timeout_ms":      args.intValue("timeout_ms"),
	}, browserExitOK
}

func (c *browserCLI) pressCommand() *commandSpec {
	return c.leaf("hive-browser press", "press a key, optionally on an element (browser_press)",
		[]argSpec{
			{Dest: "selector", Long: "selector", Kind: kindString, Help: "element to focus first"},
			{Dest: "modifiers", Long: "modifiers", Kind: kindString, Help: "e.g. Meta+A"},
			tabFlag(),
		},
		[]positionalSpec{{Dest: "key", Required: true, Help: "key name, e.g. Enter"}},
		"browser_press", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{"key": args.str("key")}
			if args.wasProvided("selector") {
				params["selector"] = args.str("selector")
			}
			if args.wasProvided("modifiers") {
				params["modifiers"] = args.str("modifiers")
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) scrollCommand() *commandSpec {
	return c.leaf("hive-browser scroll", "scroll the page or an element (browser_scroll)",
		[]argSpec{
			{Dest: "direction", Long: "direction", Kind: kindString, Default: "down", Help: "up | down | left | right"},
			{Dest: "amount", Long: "amount", Kind: kindInt, Default: 500, Help: "distance in px"},
			{Dest: "selector", Long: "selector", Kind: kindString, Help: "element to scroll"},
			{Dest: "auto_snapshot_mode", Long: "auto-snapshot-mode", Kind: kindString, Default: "simple",
				Help: "simple | default | interactive | off"},
			tabFlag(),
		}, nil, "browser_scroll", func(args *parsedArgs) (map[string]any, int) {
			params := map[string]any{
				"direction":          args.str("direction"),
				"amount":             args.intValue("amount"),
				"auto_snapshot_mode": args.str("auto_snapshot_mode"),
			}
			if args.wasProvided("selector") {
				params["selector"] = args.str("selector")
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

// --- tab (noun) -------------------------------------------------------------

func (c *browserCLI) tabCommand() *commandSpec {
	return &commandSpec{
		Prog:        "hive-browser tab",
		Help:        "tab-group management: list / close / activate",
		Dest:        "tab",
		Description: "Manage the tabs this browser session opened.",
		Subcommands: []*commandSpec{
			c.leaf("hive-browser tab list", "list tabs in this session's group",
				[]argSpec{tabFlag()}, nil, "browser_tabs", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{}
					c.optTab(args, params)
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser tab close", "close a tab (default: the active tab)",
				[]argSpec{tabFlag()}, []positionalSpec{{Dest: "tab_id", Help: "tab id (default: active tab)"}},
				"browser_close", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{}
					if args.wasProvided("tab_id") && strings.TrimSpace(args.str("tab_id")) != "" {
						tab, err := parsePositiveInt(args.str("tab_id"))
						if err != nil {
							fmt.Fprintf(c.stderr, "hive-browser: tab_id: %v\n", err)
							return nil, browserExitUsage
						}
						params["tab_id"] = tab
						return params, browserExitOK
					}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, func(args *parsedArgs, result map[string]any) {
					// The bridge reports the closed tab when it can; otherwise
					// forget the tab we asked it to close.
					closed := intOf(result["tabId"])
					if closed <= 0 {
						closed = intOf(result["closedTabId"])
					}
					forgetBrowserCLITab(c.statePath(), c.profile(args), closed)
				}),
			c.leaf("hive-browser tab activate", "bring a tab to the foreground",
				[]argSpec{tabFlag()}, []positionalSpec{{Dest: "tab_id", Required: true, Help: "tab id"}},
				"browser_activate_tab", func(args *parsedArgs) (map[string]any, int) {
					tab, err := parsePositiveInt(args.str("tab_id"))
					if err != nil {
						fmt.Fprintf(c.stderr, "hive-browser: tab_id: %v\n", err)
						return nil, browserExitUsage
					}
					return map[string]any{"tab_id": tab}, browserExitOK
				}, func(args *parsedArgs, result map[string]any) {
					if tabID := intOf(result["tabId"]); tabID > 0 {
						saveBrowserCLIState(c.statePath(), c.profile(args), tabID, stringOf(result["browser_profile"]))
					}
				}),
		},
	}
}

// --- page (noun) ------------------------------------------------------------

func (c *browserCLI) pageCommand() *commandSpec {
	tab := tabFlag()
	head := argSpec{Dest: "head", Long: "head", Kind: kindInt, Help: "also return the first N chars inline"}
	return &commandSpec{
		Prog:        "hive-browser page",
		Help:        "page reads + viewport: html / snapshot / text / shadow-query / console / resize",
		Dest:        "page",
		Description: "Read the page or change the viewport.",
		Subcommands: []*commandSpec{
			c.leaf("hive-browser page html", "page or element outerHTML (spilled to a file)",
				[]argSpec{head, tab}, []positionalSpec{{Dest: "selector", Help: "CSS selector (omit for the whole page)"}},
				"browser_html", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{}
					if strings.TrimSpace(args.str("selector")) != "" {
						params["selector"] = args.str("selector")
					}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, c.spillHead),
			c.leaf("hive-browser page snapshot", "accessibility tree (spilled to a file)",
				[]argSpec{
					{Dest: "mode", Long: "mode", Kind: kindString, Default: "default",
						Help: "default | simple | interactive"},
					head, tab,
				}, nil, "browser_snapshot", func(args *parsedArgs) (map[string]any, int) {
					mode := strings.TrimSpace(args.str("mode"))
					if !containsStringValue(browserSnapshotModes, mode) {
						fmt.Fprintf(c.stderr, "hive-browser: unknown --mode %q (choose from %s)\n",
							mode, strings.Join(browserSnapshotModes, ", "))
						return nil, browserExitUsage
					}
					params := map[string]any{"mode": mode}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, c.spillHead),
			c.leaf("hive-browser page text", "text content of an element",
				[]argSpec{timeoutFlag(5000, "selector wait (ms)"), tab},
				[]positionalSpec{{Dest: "selector", Required: true, Help: "CSS selector"}},
				"browser_get_text", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{
						"selector":   args.str("selector"),
						"timeout_ms": args.intValue("timeout_ms"),
					}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser page shadow-query", "element rect as viewport fractions (pierces shadow DOM with ' >>> ')",
				[]argSpec{tab}, []positionalSpec{{Dest: "selector", Required: true, Help: "CSS selector"}},
				"browser_shadow_query", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{"selector": args.str("selector")}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser page console", "console messages (stub — use `evaluate`)",
				[]argSpec{
					{Dest: "level", Long: "level", Kind: kindString, Help: "error | warn | log | all"},
					tab,
				}, nil, "browser_console", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{}
					if args.wasProvided("level") {
						params["level"] = args.str("level")
					}
					c.optTab(args, params)
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser page resize", "resize the viewport",
				[]argSpec{
					{Dest: "width", Long: "width", Kind: kindInt, Help: "viewport width in CSS px"},
					{Dest: "height", Long: "height", Kind: kindInt, Help: "viewport height in CSS px"},
					tab,
				}, nil, "browser_resize", func(args *parsedArgs) (map[string]any, int) {
					width, height := args.intValue("width"), args.intValue("height")
					if !args.wasProvided("width") || !args.wasProvided("height") || width <= 0 || height <= 0 {
						fmt.Fprintln(c.stderr, "hive-browser: --width and --height are required (positive integers)")
						return nil, browserExitUsage
					}
					params := map[string]any{"width": width, "height": height}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, nil),
		},
	}
}

// spillHead copies the first --head characters of a spilled payload into the
// result so an agent does not need a second read.
func (c *browserCLI) spillHead(args *parsedArgs, result map[string]any) {
	if result == nil || !args.wasProvided("head") {
		return
	}
	head := args.intValue("head")
	if head <= 0 {
		return
	}
	for _, key := range []string{"saved_to", "path"} {
		path, ok := result[key].(string)
		if !ok || strings.TrimSpace(path) == "" {
			continue
		}
		data, err := os.ReadFile(expandUser(path))
		if err != nil {
			return
		}
		if len(data) > head {
			data = data[:head]
		}
		result["head"] = string(data)
		return
	}
}

// --- dialog (noun) ----------------------------------------------------------

func (c *browserCLI) dialogCommand() *commandSpec {
	return &commandSpec{
		Prog:        "hive-browser dialog",
		Help:        "native dialogs: respond",
		Dest:        "dialog",
		Description: "Answer a native dialog that is blocking the page.",
		Subcommands: []*commandSpec{
			c.leaf("hive-browser dialog respond", "accept/dismiss a pending native dialog",
				[]argSpec{
					{Dest: "prompt_text", Long: "prompt-text", Kind: kindString, Help: "text for a prompt()"},
					tabFlag(),
				},
				[]positionalSpec{{Dest: "action", Required: true, Help: "accept | dismiss"}},
				"browser_dialog_respond", func(args *parsedArgs) (map[string]any, int) {
					action := strings.ToLower(strings.TrimSpace(args.str("action")))
					if action != "accept" && action != "dismiss" {
						fmt.Fprintf(c.stderr, "hive-browser: action must be accept or dismiss, got %q\n", action)
						return nil, browserExitUsage
					}
					params := map[string]any{"action": action}
					if args.wasProvided("prompt_text") {
						params["prompt_text"] = args.str("prompt_text")
					}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, nil),
		},
	}
}
