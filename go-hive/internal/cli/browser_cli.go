package cli

// Parity: tools/src/gcu/cli.py::main
//
// browser_cli.go is the `hive-browser` command surface: the CLI that replaces
// the retired `browser_*` MCP tools. It is a thin, stateless client of the
// standalone bridge's local HTTP API (POST /command on 14830) — the same
// surface `cmd/browserbridge` exposes — so the main module needs no dependency
// on the browserbridge module and no WebSocket/RPC client of its own.
//
// Layout:
//   browser_cli.go           dispatch, help, arg helpers
//   browser_cli_commands.go  the verb tree (setup … dialog respond)
//   browser_cli_client.go    HTTP transport, exit-code taxonomy, state file
//
// Exit codes are the stable agent contract ported from gcu/errors.py.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Exit codes — parity with tools/src/gcu/errors.py.
//
// browserExitUsage shares the value 2 with browserExitNotConnected: upstream's
// argparse exits 2 on a usage error (unknown flag/command, missing required
// argument, bad choice or type) and errors.py also uses 2 for not_connected, so
// both meanings live on the same code upstream too.
const (
	browserExitOK            = 0
	browserExitDomain        = 1
	browserExitUsage         = 2
	browserExitNotConnected  = 2
	browserExitNotStarted    = 3
	browserExitNotFound      = 4
	browserExitAmbiguous     = 5
	browserExitBadArgs       = 6
	browserExitPendingDialog = 7
	browserExitRateLimited   = 8
)

// browserCLI holds the process state one invocation needs.
type browserCLI struct {
	stdout io.Writer
	stderr io.Writer
	env    func(string) string
	stdin  io.Reader
	root   *commandSpec
}

// browserHealthTimeout bounds the /health connectivity probe.
const browserHealthTimeout = 3 * time.Second

// RunBrowserCLI is the `hive-browser` entry point; it returns the process exit
// code (see tools/src/gcu/errors.py for the taxonomy).
func RunBrowserCLI(argv []string, stdout, stderr io.Writer) int {
	return runBrowserCLI(argv, stdout, stderr, os.Stdin)
}

// runBrowserCLI is RunBrowserCLI with the stdin seam the `-` (stdin) argument
// form needs in tests.
func runBrowserCLI(argv []string, stdout, stderr io.Writer, stdin io.Reader) int {
	app := &browserCLI{stdout: stdout, stderr: stderr, env: os.Getenv, stdin: stdin}
	root := app.rootCommand()
	app.root = root
	args, err := parseCommand(root, argv)
	if err != nil {
		var help *helpRequest
		if errors.As(err, &help) {
			fmt.Fprint(app.stdout, help.spec.helpText())
			return browserExitOK
		}
		var usage *usageError
		if errors.As(err, &usage) {
			// argparse's shape: the responsible page's usage block, then
			// "<prog>: error: <message>" (exit 2, upstream's usage code).
			if block := usageErrorBlock(root, usage); block != "" {
				fmt.Fprintln(app.stderr, block)
			}
			fmt.Fprintf(app.stderr, "%s: error: %s\n", usage.prog, usage.msg)
			return browserExitUsage
		}
		fmt.Fprintf(app.stderr, "hive-browser: %v\n", err)
		return browserExitUsage
	}
	if args.cmd == nil || args.cmd.Run == nil {
		fmt.Fprintln(app.stderr, usageBlockFor(root, root.Prog))
		fmt.Fprintf(app.stderr, "%s: error: the following arguments are required: %s\n",
			root.Prog, root.Dest)
		return browserExitUsage
	}
	return args.cmd.Run(args)
}

func (c *browserCLI) rootCommand() *commandSpec {
	root := &commandSpec{
		Prog: "hive-browser",
		Description: "Drive the Hive browser (Beeline extension) from the terminal. " +
			"Replaces the browser_* MCP tools; agents invoke it via terminal_exec with --json.",
		// Upstream's argparse parent parser also accepts the shared flags
		// *before* the subcommand (`hive-browser --json status`). argparse lets
		// the subparser's own default win for a shared dest, so a leading value
		// parses but does not override the leaf's. The Go parser mirrors that:
		// group flags are accepted, leaf defaults stay in charge. --bridge is
		// Go-only and stays leaf-level (no upstream counterpart to align).
		Flags: browserGroupFlags(),
		// Upstream registers the subcommands with dest="cmd"; the dest names
		// the slot in argparse's "invalid choice" / "required" messages.
		Dest:        "cmd",
		Subcommands: c.subcommands(),
	}
	return root
}

// browserGroupFlags are the shared flags the root accepts before a subcommand
// (upstream's `common` parent parser: --json and --profile, the latter hidden
// with argparse.SUPPRESS upstream — mirrored here so the rendered help matches).
func browserGroupFlags() []argSpec {
	return []argSpec{
		{Dest: "json", Long: "json", Kind: kindBool, Help: "machine output (agent mode)"},
		{Dest: "profile", Long: "profile", Kind: kindString, Hidden: true,
			Help: "browser session profile (default: $HIVE_BROWSER_SESSION, else \"default\")"},
	}
}

// browserCommonFlags are the shared options every leaf accepts (upstream's
// argparse `common` parent plus the Go-only bridge override). They are declared
// per leaf because the shared parser does not propagate parent values into a
// subcommand that also declares the flag; `hive-browser status --json` (flag
// after the command) is the documented form.
func browserCommonFlags() []argSpec {
	return append(browserGroupFlags(), argSpec{Dest: "bridge", Long: "bridge", Kind: kindString, Hidden: true,
		Help: "bridge HTTP base URL (default: $HIVE_BROWSERBRIDGE_HTTP or http://127.0.0.1:14830)"})
}

// withCommon prepends the shared flags to a leaf's own flags. Upstream's
// argparse parent parser declares --json/--profile *before* the leaf's own
// arguments, so they render first in the usage line and the options block;
// flag order has no effect on parsing.
func withCommon(flags ...argSpec) []argSpec {
	return append(browserCommonFlags(), flags...)
}

// tabFlag is the `--tab` option shared by the tab-scoped commands. Upstream
// declares it bare (`sp.add_argument("--tab", type=int)`), so it renders
// without help text.
func tabFlag() argSpec {
	return argSpec{Dest: "tab", Long: "tab", Kind: kindInt}
}

func browserProfileFlag() argSpec {
	return argSpec{Dest: "browser_profile", Long: "browser-profile", Kind: kindString}
}

func timeoutFlag(def int, help string) argSpec {
	return argSpec{Dest: "timeout_ms", Long: "timeout-ms", Kind: kindInt, Default: def, Help: help}
}

// leaf builds one command: the spec, the parameter builder and an optional
// post-processor for the successful result.
func (c *browserCLI) leaf(prog, help string, flags []argSpec, positionals []positionalSpec, typ string, params browserParams, post browserPost) *commandSpec {
	return &commandSpec{
		Prog:        prog,
		Help:        help,
		Flags:       withCommon(flags...),
		Positionals: positionals,
		Run: func(args *parsedArgs) int {
			built, code := params(args)
			if code != browserExitOK {
				return code
			}
			return c.call(args, typ, built, post)
		},
	}
}

// call sends one bridge command and returns the classified exit code.
func (c *browserCLI) call(args *parsedArgs, typ string, params map[string]any, post browserPost) int {
	if params == nil {
		params = map[string]any{}
	}
	base := c.bridgeBase(args)
	profile := c.profile(args)
	// Upstream's errors.run wraps every command in _run_with_bridge, which
	// calls ensure_bridge_host_running() before the op. Spawn-on-demand is
	// what makes a cold `hive-browser open …` work without a prior
	// `hive serve`; an explicit --bridge opts out (see ensureBridge).
	_ = c.ensureBridge(args)
	result, err := c.post(base, profile, typ, params)
	if err != nil {
		var refusal *browserBridgeRefusal
		if errors.As(err, &refusal) {
			code := classifyBrowserError(stringOf(refusal.payload["code"]), err.Error())
			c.printResult(args, refusal.payload, code)
			return code
		}
		fmt.Fprintf(c.stderr, "hive-browser: %v\n", err)
		if isConnectError(err) {
			fmt.Fprintf(c.stderr, "start the bridge with `hive serve` (or `browserbridge`) and retry\n")
			return browserExitNotConnected
		}
		return browserExitDomain
	}
	code := classifyBrowserResult(result)
	if code == browserExitOK && post != nil {
		post(args, result)
		// Upstream classifies the op's FINAL result, after the spill layer has
		// run, so a failed artifact write still exits non-zero. Re-classify.
		code = classifyBrowserResult(result)
	}
	c.printResult(args, result, code)
	return code
}

// printResult writes the result as JSON (--json) or a terse key/value list.
func (c *browserCLI) printResult(args *parsedArgs, result map[string]any, code int) {
	if args != nil && args.boolean("json") {
		writeJSONOut(c.stdout, result, true)
		return
	}
	if code != browserExitOK {
		message := "command failed"
		switch value := result["error"].(type) {
		case string:
			if value != "" {
				message = value
			}
		case nil:
			if text, ok := result["message"].(string); ok && text != "" {
				message = text
			}
		default:
			if encoded, err := json.Marshal(value); err == nil {
				message = string(encoded)
			}
		}
		fmt.Fprintf(c.stderr, "hive-browser: %s\n", message)
		return
	}
	renderBrowserResult(c.stdout, result)
}

// bridgeBase resolves --bridge, then HIVE_BROWSERBRIDGE_HTTP, then the
// documented 14830 endpoint.
func (c *browserCLI) bridgeBase(args *parsedArgs) string {
	if args != nil {
		if raw := strings.TrimSpace(args.str("bridge")); raw != "" {
			if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
				raw = "http://" + raw
			}
			return strings.TrimRight(raw, "/")
		}
	}
	return browserBridgeBaseURL(c.env)
}

// profile resolves --profile, then the runtime-stamped HIVE_BROWSER_SESSION,
// then "default" (the bridge's own fallback).
func (c *browserCLI) profile(args *parsedArgs) string {
	if args != nil {
		if raw := strings.TrimSpace(args.str("profile")); raw != "" {
			return raw
		}
	}
	if raw := strings.TrimSpace(c.env("HIVE_BROWSER_SESSION")); raw != "" {
		return raw
	}
	return "default"
}

// tabOrState returns --tab when given, else the active tab this profile opened
// through the CLI (the local mirror of upstream's persisted contexts).
func (c *browserCLI) tabOrState(args *parsedArgs) (int, bool) {
	if args != nil && args.wasProvided("tab") && args.intValue("tab") > 0 {
		return args.intValue("tab"), true
	}
	state := loadBrowserCLIState(c.statePath())
	entry, ok := state[c.profile(args)]
	if !ok || entry.TabID <= 0 {
		return 0, false
	}
	return entry.TabID, true
}

// requireTab injects the resolved tab id; commands that cannot work without one
// fail with exit 3 (not started) before any round-trip, matching the upstream
// CLI's "Browser not started. Run `hive-browser open <url>` first." message.
//
// The connectivity check still comes first, as it does upstream: with no
// bridge/extension reachable at all, upstream answers "not connected" (2), not
// "not started", so the local pre-check must not short-circuit that state
// (R07 family: `upload`/`reload`/`navigate`/`select` with nothing running).
func (c *browserCLI) requireTab(args *parsedArgs, params map[string]any) int {
	tab, ok := c.tabOrState(args)
	if !ok {
		if !c.bridgeConnected(args) {
			fmt.Fprintln(c.stderr, "hive-browser: the Hive browser extension isn't connected")
			fmt.Fprintln(c.stderr, "start the bridge with `hive serve` (or `browserbridge`) and retry")
			return browserExitNotConnected
		}
		fmt.Fprintln(c.stderr, "hive-browser: Browser not started. Run `hive-browser open <url>` first.")
		return browserExitNotStarted
	}
	params["tab_id"] = tab
	return browserExitOK
}

// bridgeConnected reports whether the bridge answers /health with at least one
// extension connection — the HTTP-proxy equivalent of upstream's in-process
// `bridge.is_connected`. The probe only runs on an already-failing path, and
// uses a short timeout so a dead bridge is reported promptly.
func (c *browserCLI) bridgeConnected(args *parsedArgs) bool {
	base := c.bridgeBase(args)
	ctx, cancel := context.WithTimeout(context.Background(), browserHealthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: browserHealthTimeout}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Connected bool `json:"connected"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&payload); err != nil {
		return false
	}
	return payload.Connected
}

// connectedProfileLabels fetches the bridge's connected Chrome profile labels
// (GET /status, the Go bridge's `profiles` list; falls back to
// connections[].label). Upstream's validate_browser_profile reads the same
// list via bridge.connected_profiles() and treats an empty enumeration as
// "cannot tell" — never a rejection.
func (c *browserCLI) connectedProfileLabels(args *parsedArgs) []string {
	base := c.bridgeBase(args)
	ctx, cancel := context.WithTimeout(context.Background(), browserHealthTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/status", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{Timeout: browserHealthTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var payload struct {
		Profiles    []string `json:"profiles"`
		Connections []struct {
			Label string `json:"label"`
		} `json:"connections"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil
	}
	labels := make([]string, 0, len(payload.Profiles))
	for _, label := range payload.Profiles {
		if strings.TrimSpace(label) != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		for _, connection := range payload.Connections {
			if strings.TrimSpace(connection.Label) != "" {
				labels = append(labels, connection.Label)
			}
		}
	}
	return labels
}

// unknownBrowserProfile mirrors nav.py::validate_browser_profile (audit B4):
// "" and "default" are always acceptable; an unknown label is rejected ONLY
// when the bridge reported at least one connected profile. The message matches
// upstream's `unknown browser profile {label!r}. connected: {labels}` shape
// (exit 6); no message means the label is fine.
func (c *browserCLI) unknownBrowserProfile(args *parsedArgs, label string) string {
	if label == "" || label == "default" {
		return ""
	}
	labels := c.connectedProfileLabels(args)
	if len(labels) == 0 {
		return ""
	}
	for _, known := range labels {
		if known == label {
			return ""
		}
	}
	quoted := make([]string, 0, len(labels))
	for _, known := range labels {
		quoted = append(quoted, "'"+known+"'")
	}
	return fmt.Sprintf("unknown browser profile '%s'. connected: [%s]", label, strings.Join(quoted, ", "))
}

// optTab injects the resolved tab id when there is one; the bridge then falls
// back to its own context registry for the profile.
func (c *browserCLI) optTab(args *parsedArgs, params map[string]any) {
	if tab, ok := c.tabOrState(args); ok {
		params["tab_id"] = tab
	}
}

// statePath is <HIVE_HOME>/browser_cli_state.json (upstream keeps the
// equivalent profile→group index in ~/.hive/browser_contexts.json).
func (c *browserCLI) statePath() string {
	if root := strings.TrimSpace(c.env("HIVE_HOME")); root != "" {
		return filepath.Join(root, "browser_cli_state.json")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".hive", "browser_cli_state.json")
	}
	return ""
}

// --- argument helpers -------------------------------------------------------

// readScriptSource implements upstream's `--js SRC|@FILE|-` resolution
// (cli_commands/script.py::read_arg_value): the raw value is used verbatim —
// only an exact "-" reads stdin and only a leading "@" reads a file. No
// trimming, so " @file" is inline source with a leading space, exactly like
// upstream's `raw == "-"` / `raw.startswith("@")` tests. A failed file read
// is upstream's validation("could not read <path>: <e>") (exit 6).
func (c *browserCLI) readScriptSource(value string) (string, error) {
	switch {
	case value == "-":
		data, err := io.ReadAll(io.LimitReader(c.stdin, 8<<20))
		if err != nil {
			return "", fmt.Errorf("could not read input: %w", err)
		}
		return string(data), nil
	case strings.HasPrefix(value, "@"):
		path := expandUser(value[1:])
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("could not read %s: %v", path, err)
		}
		return string(data), nil
	}
	return value, nil
}

// parseFloatList parses "0.3,0.6" (or "x0,y0,x1,y1") into a JSON-ready list.
func parseFloatList(raw string, want int, label string) ([]any, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != want {
		return nil, fmt.Errorf("%s expects %d comma-separated numbers, got %d", label, want, len(parts))
	}
	out := make([]any, 0, want)
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a number", label, strings.TrimSpace(part))
		}
		out = append(out, value)
	}
	return out, nil
}

// decodeJSONObject implements upstream's `--args JSON|@FILE|-` contract
// (cli_commands/script.py): the value is resolved verbatim like --js (exact
// "-" = stdin, leading "@" = file, no trimming), an EMPTY resolved payload
// means "no args" (upstream's `if args_raw:` truthiness), and any non-empty
// payload must parse as a JSON object (dict) — anything else is upstream's
// validation error, printed as `error: <message>` and exit 6. Parsing runs on
// the raw resolved text, so whitespace-only input fails like upstream's
// json.loads("   ") instead of being skipped.
func (c *browserCLI) decodeJSONObject(raw string, label string) (map[string]any, error) {
	resolved := raw
	switch {
	case raw == "-":
		data, err := io.ReadAll(io.LimitReader(c.stdin, 8<<20))
		if err != nil {
			return nil, fmt.Errorf("could not read input: %w", err)
		}
		resolved = string(data)
	case strings.HasPrefix(raw, "@"):
		path := expandUser(raw[1:])
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read %s: %v", path, err)
		}
		resolved = string(data)
	}
	if resolved == "" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(resolved), &decoded); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON: %v", label, err)
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a JSON object (dict)", label)
	}
	return object, nil
}
