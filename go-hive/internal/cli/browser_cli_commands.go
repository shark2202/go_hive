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
		hiddenVerb(c.clickCommand()),
		hiddenVerb(c.typeCommand()),
		hiddenVerb(c.typeFocusedCommand()),
		hiddenVerb(c.pressCommand()),
		hiddenVerb(c.scrollCommand()),
		c.tabCommand(),
		c.pageCommand(),
		c.dialogCommand(),
	}
	return specs
}

// hiddenVerb marks a Go-only verb: still callable by name (scripts and agents
// wrap the old bridge commands through it), but kept out of every help surface
// so the `hive-browser --help` tree matches tools/src/gcu/cli.py, which has no
// click / type / type-focused / press / scroll verbs.
func hiddenVerb(spec *commandSpec) *commandSpec {
	spec.Hidden = true
	return spec
}

// helpCommand is `hive-browser help [topic]` — pure local text, no bridge.
func (c *browserCLI) helpCommand() *commandSpec {
	return &commandSpec{
		Prog:  "hive-browser help",
		Help:  "usage for the CLI or a command: `hive-browser help [cmd]`",
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
			// Upstream `_help` returns the text and `_render` prints it, so the
			// page gains one trailing newline on this path (unlike `--help`,
			// where argparse writes format_help() verbatim).
			fmt.Fprintln(c.stdout, text)
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
			{Dest: "background", Long: "background", Kind: kindBool},
			{Dest: "browser_profile", Long: "browser-profile", Kind: kindString,
				Help: "which connected Chrome profile to open in"},
		},
		[]positionalSpec{{Dest: "url", Required: true}},
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
				Choices: browserWaitUntil},
			timeoutFlag(30000, "how long to wait for the --wait-until condition (ms)"),
			browserProfileFlag(),
		},
		[]positionalSpec{{Dest: "url", Required: true}},
		"browser_navigate", func(args *parsedArgs) (map[string]any, int) {
			// --wait-until is validated by the parser: upstream declares it
			// with choices=, so argparse rejects an unknown value itself.
			params := map[string]any{
				"url":        args.str("url"),
				"wait_until": args.str("wait_until"),
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
		{Dest: "action", Long: "action", Kind: kindString, Required: true, Choices: browserInteractActions},
		tabFlag(),
		{Dest: "selector", Long: "selector", Kind: kindString},
		{Dest: "coordinate", Long: "coordinate", Kind: kindString, Metavar: "x,y",
			Help: "[x,y] viewport fractions 0..1"},
		{Dest: "start_selector", Long: "start-selector", Kind: kindString},
		{Dest: "start_coordinate", Long: "start-coordinate", Kind: kindString, Metavar: "x,y"},
		{Dest: "text", Long: "text", Kind: kindString},
		{Dest: "no_clear_first", Long: "no-clear-first", Kind: kindBool},
		{Dest: "no_insert_text", Long: "no-insert-text", Kind: kindBool},
		{Dest: "modifiers", Long: "modifiers", Kind: kindString},
		{Dest: "repeat", Long: "repeat", Kind: kindInt, Default: 1},
		{Dest: "scroll_direction", Long: "scroll-direction", Kind: kindString, Default: "down",
			Choices: browserScrollDirections},
		{Dest: "scroll_amount", Long: "scroll-amount", Kind: kindInt, Default: 500},
		{Dest: "intent", Long: "intent", Kind: kindString},
		{Dest: "full_page", Long: "full-page", Kind: kindBool},
		{Dest: "no_annotate", Long: "no-annotate", Kind: kindBool},
		{Dest: "region", Long: "region", Kind: kindString, Metavar: "x0,y0,x1,y1"},
		{Dest: "duration", Long: "duration", Kind: kindString},
		{Dest: "wait_for_selector", Long: "wait-for-selector", Kind: kindString},
		{Dest: "wait_for_text", Long: "wait-for-text", Kind: kindString},
		timeoutFlag(0, ""),
		{Dest: "auto_snapshot_mode", Long: "auto-snapshot-mode", Kind: kindString, Default: "simple",
			Choices: browserAutoSnapshotModes},
		{Dest: "wait_after_ms", Long: "wait-after-ms", Kind: kindInt},
	}
	return c.leaf("hive-browser interact", "click / type / key / scroll / drag / screenshot / zoom / wait",
		flags, nil, "browser_interact", func(args *parsedArgs) (map[string]any, int) {
			// --action (required), --scroll-direction and --auto-snapshot-mode
			// (choices) are validated by the parser, exactly as upstream's
			// argparse declares them.
			//
			// --intent is not validated here: upstream's CLI passes it through
			// and the tool layer answers `screenshot requires intent` with
			// ok:false, which the taxonomy maps to exit 1 — same path, same
			// code. (zoom does not take an intent upstream at all.)
			params := map[string]any{
				"action":             args.str("action"),
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
			// The interact screenshot action spills the same 800px JPEG the
			// standalone verb does (upstream _blocks_to_cli_dict); ask the
			// bridge for that payload when this action captures an image.
			if args.str("action") == "screenshot" {
				params["format"] = "jpeg"
				params["quality"] = 75
				params["max_width"] = 800
			}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, func(args *parsedArgs, result map[string]any) {
			intent, hasIntent := "", false
			if args.wasProvided("intent") {
				intent, hasIntent = args.str("intent"), true
			}
			action := strings.TrimSpace(args.str("action"))
			if action == "screenshot" || action == "zoom" {
				c.spillImageResult(result, "browser_interact", intent, hasIntent)
				return
			}
			// Coordinate click / hover / key return the interaction dict plus
			// one annotated screenshot; upstream collapses that image block
			// into saved_to + _image only (no screenshot metadata).
			c.spillActionImage(result, intent, hasIntent)
		})
}

func (c *browserCLI) selectCommand() *commandSpec {
	return c.leaf("hive-browser select", "select option(s) in a <select> dropdown",
		[]argSpec{
			{Dest: "value", Long: "value", Kind: kindAppend, Default: []string{}, Required: true,
				Help: "option value (repeatable)"},
			tabFlag(),
		},
		[]positionalSpec{{Dest: "selector", Required: true}},
		"browser_select", func(args *parsedArgs) (map[string]any, int) {
			// --value is required=True upstream: the parser reports it.
			params := map[string]any{"selector": args.str("selector"), "values": args.list("value")}
			if code := c.requireTab(args, params); code != browserExitOK {
				return nil, code
			}
			return params, browserExitOK
		}, nil)
}

func (c *browserCLI) uploadCommand() *commandSpec {
	return c.leaf("hive-browser upload", "set files on a file input (direct or via trigger)",
		[]argSpec{
			{Dest: "file", Long: "file", Kind: kindAppend, Default: []string{}, Required: true,
				Help: "file path (repeatable)"},
			{Dest: "trigger_selector", Long: "trigger-selector", Kind: kindString},
			tabFlag(),
			timeoutFlag(30000, ""),
		},
		[]positionalSpec{{Dest: "selector", Required: true}},
		"browser_upload", func(args *parsedArgs) (map[string]any, int) {
			// --file is required=True upstream: the parser reports it.
			files := args.list("file")
			// No local existence check: upstream reads the files in the tool
			// handler AFTER the bridge/context/tab checks and answers
			// ok:false "File not found: <p>", which classify_result maps to 4
			// (or 3 for "Browser not started"). The bridge performs the same
			// check in the same order, so pass straight through (R07).
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
			{Dest: "intent", Long: "intent", Kind: kindString},
			{Dest: "full_page", Long: "full-page", Kind: kindBool,
				Help: "capture the document region (bounded; downscaled to 800px wide like the default)"},
			{Dest: "selector", Long: "selector", Kind: kindString},
			{Dest: "no_annotate", Long: "no-annotate", Kind: kindBool},
			timeoutFlag(5000, "selector wait (ms) when --selector is given (polls like interact)"),
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
				// Upstream's CLI always spills the 800px JPEG (spill=True);
				// ask the bridge for exactly that payload.
				"format":    "jpeg",
				"quality":   75,
				"max_width": 800,
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
		}, func(args *parsedArgs, result map[string]any) {
			intent, hasIntent := "", false
			if args.wasProvided("intent") {
				intent, hasIntent = args.str("intent"), true
			}
			c.spillImageResult(result, "browser_screenshot", intent, hasIntent)
		})
}

func (c *browserCLI) evaluateCommand() *commandSpec {
	spec := c.leaf("hive-browser evaluate", "run JavaScript in the page (value of last expression)",
		[]argSpec{
			{Dest: "js", Long: "js", Kind: kindString, Metavar: "SRC|@FILE|-",
				Help: "JS source: inline, @FILE (read from path), or - (stdin)"},
			{Dest: "script", Long: "script", Kind: kindString, Metavar: "JS", Help: "alias for --js"},
			tabFlag(),
		}, nil, "browser_evaluate", func(args *parsedArgs) (map[string]any, int) {
			source := args.str("js")
			if !args.wasProvided("js") {
				source = args.str("script")
			}
			if strings.TrimSpace(source) == "" {
				// Upstream raises this as a command-level validation error
				// (exit 6), printed as "error: <message>".
				fmt.Fprintln(c.stderr, "error: evaluate requires JavaScript via --js (inline, @file, or -)")
				return nil, browserExitBadArgs
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
	// Upstream mounts RawDescriptionHelpFormatter here; the epilog keeps its
	// line breaks and follows the options block (cli.py:218-230).
	spec.RawDescription = true
	spec.Epilog = "Return semantics: a SINGLE expression is returned automatically (e.g.\n" +
		"'document.title'). For MULTIPLE statements, wrap in an IIFE and `return`\n" +
		"the value: '(()=>{ const n=[...document.links].length; return n; })()'.\n" +
		"Bare multi-statement code without a return yields null; a top-level `throw`\n" +
		"is a parse error (throw inside the IIFE instead).\n" +
		"\n" +
		"examples:\n" +
		"  hive-browser evaluate --js 'document.title' --json\n" +
		"  hive-browser evaluate --js @/tmp/probe.js --json   # quote-heavy JS: read from a file\n" +
		"  echo 'location.href' | hive-browser evaluate --js - --json"
	return spec
}

func (c *browserCLI) scriptCommand() *commandSpec {
	spec := c.leaf("hive-browser script", "run a skill-bundled run(ctx) orchestration script",
		[]argSpec{
			{Dest: "skill", Long: "skill", Kind: kindString, Help: "skill name or dir (e.g. hive.linkedin-core)"},
			{Dest: "script", Long: "script", Kind: kindString, Help: "a script under <skill>/scripts/ (the .py suffix is optional)"},
			{Dest: "args", Long: "args", Kind: kindString, Metavar: "JSON|@file|-",
				Help: "ctx.args as JSON (inline, @file, or - for stdin)"},
			{Dest: "timeout_s", Long: "timeout-s", Kind: kindString, Default: "60"},
			tabFlag(),
			browserProfileFlag(),
		}, nil, "browser_script", func(args *parsedArgs) (map[string]any, int) {
			skill := strings.TrimSpace(args.str("skill"))
			script := strings.TrimSpace(args.str("script"))
			// Upstream validates these in cmd_script (not argparse), with its
			// own wording and exit 6 — reproduced verbatim.
			if skill == "" {
				fmt.Fprintln(c.stderr, "error: --skill is required (scripts are scoped to a skill dir). "+
					"For ad-hoc JavaScript, use: hive-browser evaluate --js '<code>'")
				return nil, browserExitBadArgs
			}
			if script == "" {
				fmt.Fprintln(c.stderr, "error: --script is required (a script file under <skill>/scripts/)")
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
	// Upstream's epilog renders under the default formatter (collapsed and
	// wrapped); the contract capture glues it to the last option.
	spec.Epilog = "example: hive-browser script --skill hive.linkedin-core --script lk_get_identity --args '{}' --json"
	return spec
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

// hiddenTabFlag is the per-command tab selector Go accepts on the `tab` noun's
// subcommands. Upstream's tab subparsers do not define --tab, so it is a Go
// extension: still parsed and forwarded, but kept out of help.
func hiddenTabFlag() argSpec {
	flag := tabFlag()
	flag.Hidden = true
	return flag
}

func (c *browserCLI) tabCommand() *commandSpec {
	return &commandSpec{
		Prog: "hive-browser tab",
		Help: "tab-group management: list / close / activate",
		// Upstream registers the tab verbs with dest="verb".
		Dest:  "verb",
		Flags: browserGroupFlags(),
		Subcommands: []*commandSpec{
			c.leaf("hive-browser tab list", "list tabs in this session's group",
				[]argSpec{hiddenTabFlag()}, nil, "browser_tabs", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{}
					c.optTab(args, params)
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser tab close", "close a tab (default: active)",
				[]argSpec{hiddenTabFlag()}, []positionalSpec{{Dest: "tab_id"}},
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
				[]argSpec{hiddenTabFlag()}, []positionalSpec{{Dest: "tab_id", Required: true}},
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
	return &commandSpec{
		Prog:  "hive-browser page",
		Help:  "page reads + viewport: html / snapshot / text / shadow-query / console / resize",
		Dest:  "page",
		Flags: browserGroupFlags(),
		Subcommands: []*commandSpec{
			c.leaf("hive-browser page html", "page or element outerHTML (spilled to a file)",
				[]argSpec{{Dest: "head", Long: "head", Kind: kindInt, Metavar: "N",
					Help: "also return the first N chars of the payload inline"}, tab},
				[]positionalSpec{{Dest: "selector", Help: "CSS selector (omit for the whole page)"}},
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
						Choices: browserSnapshotModes},
					{Dest: "head", Long: "head", Kind: kindInt, Metavar: "N",
						Help: "also return the first N chars of the tree inline"}, tab,
				}, nil, "browser_snapshot", func(args *parsedArgs) (map[string]any, int) {
					// --mode carries choices= upstream: the parser rejects an
					// unknown value before the command runs.
					params := map[string]any{"mode": args.str("mode")}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, c.snapshotResultPost),
			c.leaf("hive-browser page text", "text content of an element",
				[]argSpec{tab, timeoutFlag(5000, "")},
				[]positionalSpec{{Dest: "selector", Required: true}},
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
				[]argSpec{tab}, []positionalSpec{{Dest: "selector", Required: true}},
				"browser_shadow_query", func(args *parsedArgs) (map[string]any, int) {
					params := map[string]any{"selector": args.str("selector")}
					if code := c.requireTab(args, params); code != browserExitOK {
						return nil, code
					}
					return params, browserExitOK
				}, nil),
			c.leaf("hive-browser page console", "console messages (stub — use `evaluate`)",
				[]argSpec{
					{Dest: "level", Long: "level", Kind: kindString},
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
					{Dest: "width", Long: "width", Kind: kindInt, Required: true},
					{Dest: "height", Long: "height", Kind: kindInt, Required: true},
					tab,
				}, nil, "browser_resize", func(args *parsedArgs) (map[string]any, int) {
					// --width/--height are required=True upstream; the parser
					// reports a missing or non-integer value itself.
					width, height := args.intValue("width"), args.intValue("height")
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
		Prog:  "hive-browser dialog",
		Help:  "native dialogs: respond",
		Dest:  "dialog",
		Flags: browserGroupFlags(),
		Subcommands: []*commandSpec{
			c.leaf("hive-browser dialog respond", "accept/dismiss a pending native dialog",
				[]argSpec{
					{Dest: "prompt_text", Long: "prompt-text", Kind: kindString},
					tabFlag(),
				},
				[]positionalSpec{{Dest: "action", Required: true, Choices: []string{"accept", "dismiss"}}},
				"browser_dialog_respond", func(args *parsedArgs) (map[string]any, int) {
					// The {accept,dismiss} positional is a parser-level choice
					// upstream; the parser has already rejected anything else.
					params := map[string]any{"action": args.str("action")}
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
