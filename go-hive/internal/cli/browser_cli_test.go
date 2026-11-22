package cli

// Parity-exempt: upstream has no test for tools/src/gcu/cli.py; these cases pin
// the ported CLI contract instead — every verb's bridge command name and
// parameters, the exit-code taxonomy from tools/src/gcu/errors.py, and the
// local session state that makes tab-less commands work between processes.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// browserBridgeCommands is the bridge's canonical command list
// (browserbridge/commands.go canonicalCommands). The CLI must reach every one.
var browserBridgeCommands = []string{
	"browser_setup", "browser_status", "browser_stop",
	"browser_tabs", "browser_open", "browser_close", "browser_activate_tab",
	"browser_navigate", "browser_reload",
	"browser_interact", "browser_select",
	"browser_get_text", "browser_resize", "browser_upload", "browser_dialog_respond",
	"browser_console", "browser_html", "browser_shadow_query", "browser_script",
	"browser_click", "browser_type", "browser_type_focused", "browser_press",
	"browser_scroll", "browser_snapshot", "browser_screenshot", "browser_evaluate",
}

type capturedCommand struct {
	Profile string
	Type    string
	Params  map[string]any
}

// fakeBridge records the last /command envelope and answers with `response`.
func fakeBridge(t *testing.T, response map[string]any) (*httptest.Server, func() capturedCommand) {
	t.Helper()
	var mu sync.Mutex
	last := capturedCommand{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			// The CLI's connectivity probe (requireTab, R07 family) reads this
			// before choosing between "not connected" (2) and "not started" (3).
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "status": "ok", "connected": true, "connections": 1})
			return
		}
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			// --browser-profile validation (nav.py::validate_browser_profile)
			// reads the connected labels from the bridge status.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"bridge": "running", "connected": true,
				"profiles":    []string{"Work"},
				"connections": []map[string]any{{"label": "Work"}},
			})
			return
		}
		if r.URL.Path != "/command" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var envelope capturedCommand
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		last = envelope
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		payload := response
		if payload == nil {
			payload = map[string]any{"ok": true, "result": map[string]any{"ok": true}}
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server, func() capturedCommand {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// runBrowser executes one CLI invocation with an isolated HIVE_HOME.
func runBrowser(t *testing.T, home string, argv ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := RunBrowserCLI(argv, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// TestBrowserCLICoverage drives every verb and pins the bridge command + the
// parameters it sends.
//
// Parity: tools/src/gcu/cli.py::_build_parser
func TestBrowserCLICoverage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	upload := filepath.Join(home, "upload.txt")
	if err := os.WriteFile(upload, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}

	cases := []struct {
		name   string
		argv   []string
		want   string
		checks map[string]string
	}{
		{name: "setup", argv: []string{"setup", "--json"}, want: "browser_setup"},
		{name: "status", argv: []string{"status", "--json", "--tab", "5"}, want: "browser_status",
			checks: map[string]string{"tab_id": "5"}},
		{name: "stop", argv: []string{"stop", "--json"}, want: "browser_stop"},
		{name: "open", argv: []string{"open", "https://example.com", "--json"}, want: "browser_open",
			checks: map[string]string{"url": "https://example.com", "background": "false"}},
		{name: "open background in profile", argv: []string{"open", "https://example.com", "--background", "--browser-profile", "Work", "--json"},
			want: "browser_open", checks: map[string]string{"background": "true", "browser_profile": "Work"}},
		{name: "navigate", argv: []string{"navigate", "https://example.com/a", "--tab", "7", "--wait-until", "networkidle", "--json"},
			want: "browser_navigate", checks: map[string]string{"url": "https://example.com/a", "wait_until": "networkidle", "tab_id": "7", "timeout_ms": "30000"}},
		{name: "reload", argv: []string{"reload", "--tab", "7", "--json"}, want: "browser_reload",
			checks: map[string]string{"tab_id": "7"}},
		{name: "interact click", argv: []string{"interact", "--action", "left_click", "--selector", "#go", "--tab", "7", "--json"},
			want: "browser_interact", checks: map[string]string{"action": "left_click", "selector": "#go", "tab_id": "7"}},
		{name: "interact screenshot", argv: []string{"interact", "--action", "screenshot", "--intent", "verify layout", "--tab", "7", "--json"},
			want: "browser_interact", checks: map[string]string{"action": "screenshot", "intent": "verify layout"}},
		{name: "interact drag coordinates", argv: []string{"interact", "--action", "drag", "--start-coordinate", "0.1,0.2", "--coordinate", "0.3,0.4", "--tab", "7", "--json"},
			want: "browser_interact", checks: map[string]string{"start_coordinate": "[0.1 0.2]", "coordinate": "[0.3 0.4]"}},
		{name: "select", argv: []string{"select", "#s", "--value", "a", "--value", "b", "--tab", "7", "--json"},
			want: "browser_select", checks: map[string]string{"selector": "#s", "values": "[a b]"}},
		{name: "upload", argv: []string{"upload", "#f", "--file", upload, "--tab", "7", "--json"},
			want: "browser_upload", checks: map[string]string{"selector": "#f", "file_paths": "[" + upload + "]"}},
		{name: "screenshot", argv: []string{"screenshot", "--intent", "verify layout", "--tab", "7", "--json"},
			want:   "browser_screenshot",
			checks: map[string]string{"full_page": "false", "timeout_ms": "5000", "intent": "verify layout"}},
		{name: "evaluate", argv: []string{"evaluate", "--js", "document.title", "--tab", "7", "--json"},
			want: "browser_evaluate", checks: map[string]string{"script": "document.title"}},
		{name: "script", argv: []string{"script", "--skill", "hive.demo", "--script", "run", "--args", `{"a":1}`, "--tab", "7", "--json"},
			want: "browser_script", checks: map[string]string{"skill": "hive.demo", "script": "run", "timeout_s": "60"}},
		{name: "click", argv: []string{"click", "#b", "--tab", "7", "--json"}, want: "browser_click",
			checks: map[string]string{"selector": "#b", "button": "left", "double_click": "false"}},
		{name: "type", argv: []string{"type", "#i", "--text", "hi", "--tab", "7", "--json"}, want: "browser_type",
			checks: map[string]string{"selector": "#i", "text": "hi"}},
		{name: "type-focused", argv: []string{"type-focused", "--text", "hi", "--tab", "7", "--json"},
			want: "browser_type_focused", checks: map[string]string{"text": "hi"}},
		{name: "press", argv: []string{"press", "Enter", "--tab", "7", "--json"}, want: "browser_press",
			checks: map[string]string{"key": "Enter"}},
		{name: "scroll", argv: []string{"scroll", "--direction", "up", "--amount", "100", "--tab", "7", "--json"},
			want: "browser_scroll", checks: map[string]string{"direction": "up", "amount": "100"}},
		{name: "tab list", argv: []string{"tab", "list", "--json"}, want: "browser_tabs"},
		{name: "tab close", argv: []string{"tab", "close", "9", "--json"}, want: "browser_close",
			checks: map[string]string{"tab_id": "9"}},
		{name: "tab activate", argv: []string{"tab", "activate", "9", "--json"}, want: "browser_activate_tab",
			checks: map[string]string{"tab_id": "9"}},
		{name: "page html", argv: []string{"page", "html", "#x", "--tab", "7", "--json"}, want: "browser_html",
			checks: map[string]string{"selector": "#x"}},
		{name: "page snapshot", argv: []string{"page", "snapshot", "--mode", "simple", "--tab", "7", "--json"},
			want: "browser_snapshot", checks: map[string]string{"mode": "simple"}},
		{name: "page text", argv: []string{"page", "text", "#t", "--tab", "7", "--json"}, want: "browser_get_text",
			checks: map[string]string{"selector": "#t", "timeout_ms": "5000"}},
		{name: "page shadow-query", argv: []string{"page", "shadow-query", "#q", "--tab", "7", "--json"},
			want: "browser_shadow_query", checks: map[string]string{"selector": "#q"}},
		{name: "page console", argv: []string{"page", "console", "--tab", "7", "--json"}, want: "browser_console"},
		{name: "page resize", argv: []string{"page", "resize", "--width", "1280", "--height", "800", "--tab", "7", "--json"},
			want: "browser_resize", checks: map[string]string{"width": "1280", "height": "800"}},
		{name: "dialog respond", argv: []string{"dialog", "respond", "accept", "--tab", "7", "--json"},
			want: "browser_dialog_respond", checks: map[string]string{"action": "accept"}},
	}

	seen := map[string]bool{}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, last := fakeBridge(t, nil)
			argv := append(append([]string{}, testCase.argv...), "--bridge", server.URL)
			_, stderr, code := runBrowser(t, home, argv...)
			if code != browserExitOK {
				t.Fatalf("exit = %d, want 0 (stderr: %s)", code, stderr)
			}
			got := last()
			if got.Type != testCase.want {
				t.Fatalf("bridge command = %q, want %q", got.Type, testCase.want)
			}
			seen[got.Type] = true
			for key, want := range testCase.checks {
				if rendered := fmt.Sprintf("%v", got.Params[key]); rendered != want {
					t.Fatalf("params[%s] = %s, want %s (all params: %v)", key, rendered, want, got.Params)
				}
			}
		})
	}

	missing := make([]string, 0, len(browserBridgeCommands))
	for _, name := range browserBridgeCommands {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("CLI does not reach %d bridge command(s): %v", len(missing), missing)
	}
}

// TestBrowserCLIExitCodes pins the taxonomy agents branch on.
//
// Parity: tools/src/gcu/errors.py::classify_result
func TestBrowserCLIExitCodes(t *testing.T) {
	cases := []struct {
		name     string
		response map[string]any
		status   int
		want     int
	}{
		{name: "ok", response: map[string]any{"ok": true, "result": map[string]any{"ok": true}}, want: browserExitOK},
		{name: "pending dialog", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "pending_dialog": map[string]any{"type": "alert"}}}, want: browserExitPendingDialog},
		{name: "rate limited", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "rate_limited"}}, want: browserExitRateLimited},
		{name: "not connected result", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "connected": false, "error": "The Hive browser extension isn't connected."}}, want: browserExitNotConnected},
		{name: "not started result", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "Browser not started. Run open first."}}, want: browserExitNotStarted},
		{name: "no active tab", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "no active tab is known for this profile — pass tab_id"}}, want: browserExitNotStarted},
		{name: "element not found", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "Element not found: #nope"}}, want: browserExitNotFound},
		{name: "ambiguous selector", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "selector matched multiple elements"}}, want: browserExitAmbiguous},
		{name: "domain failure", response: map[string]any{"ok": true, "result": map[string]any{"ok": false, "error": "tab.create returned no tabId"}}, want: browserExitDomain},
		{name: "http not found code", response: map[string]any{"ok": false, "code": "not_found", "message": "no such tab"}, status: http.StatusNotFound, want: browserExitNotFound},
		{name: "http not connected code", response: map[string]any{"ok": false, "code": "not_connected", "message": "no extension"}, status: http.StatusServiceUnavailable, want: browserExitNotConnected},
		// The bridge's bad_request family carries no upstream structured
		// counterpart: upstream classifies the error TEXT, so a message with
		// no fragment lands on 1 and one that names a missing thing on 4.
		// Exit 6 is only for the CLI's own prechecks (see TestBrowserCLIBadArgs).
		{name: "http bad request code", response: map[string]any{"ok": false, "code": "bad_request", "message": "missing selector"}, status: http.StatusBadRequest, want: browserExitDomain},
		{name: "http unknown command code", response: map[string]any{"ok": false, "code": "unknown_command", "message": "unknown command 'browser_frobnicate'"}, status: http.StatusBadRequest, want: browserExitDomain},
		{name: "http bad request with not-found text", response: map[string]any{"ok": false, "code": "bad_request", "message": "Element not found: #nope"}, status: http.StatusBadRequest, want: browserExitNotFound},
		{name: "http no-browser-profile code", response: map[string]any{"ok": false, "code": "no_browser_profile", "message": "The Hive browser extension isn't connected."}, status: http.StatusBadRequest, want: browserExitNotConnected},
		{name: "http rate limit code", response: map[string]any{"ok": false, "code": "rate_limited", "message": "view cap"}, status: http.StatusTooManyRequests, want: browserExitRateLimited},
	}
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status := testCase.status
			if status == 0 {
				status = http.StatusOK
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_ = json.NewEncoder(w).Encode(testCase.response)
			}))
			defer server.Close()
			_, stderr, code := runBrowser(t, home, "status", "--json", "--bridge", server.URL)
			if code != testCase.want {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, testCase.want, stderr)
			}
		})
	}
}

// TestBrowserCLIUsageErrors pins CLI-level usage errors to exit 2 — argparse's
// code, which the upstream `hive-browser` runs on (measured: no args, unknown
// command/flag, missing required flag/positional, bad choice, bad float list).
//
// Parity: tools/src/gcu/cli.py::_build_parser
func TestBrowserCLIUsageErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, nil)
	cases := [][]string{
		{}, // no command
		{"bogus"},
		{"navigate"},                     // missing url positional
		{"open"},                         // missing url positional
		{"interact"},                     // --action required
		{"interact", "--action", "nope"}, // argparse choices
		{"interact", "--action", "screenshot", "--coordinate", "0.1"},       // _floats(2)
		{"interact", "--action", "zoom", "--region", "0.1,0.2"},             // _floats(4)
		{"interact", "--action", "wait", "--tab", "7", "--duration", "abc"}, // type=float
		{"interact", "--action", "left_click", "--tab", "7", "--auto-snapshot-mode", "nope"},
		{"interact", "--action", "left_click", "--tab", "7", "--scroll-direction", "sideways"},
		{"navigate", "https://example.com", "--tab", "7", "--wait-until", "whenever"},
		{"select", "#s"},                      // --value required
		{"upload", "#f"},                      // --file required
		{"page", "resize", "--width", "1280"}, // --height required
		{"page", "snapshot", "--mode", "nope"},
		{"tab", "close", "abc"},
		{"tab", "activate", "0"},
		{"dialog", "respond", "maybe"},
		{"script", "--skill", "x", "--script", "y", "--timeout-s", "soon"},
		{"status", "--unknown-flag"},
	}
	for _, argv := range cases {
		full := append(append([]string{}, argv...), "--bridge", server.URL)
		_, stderr, code := runBrowser(t, home, full...)
		if code != browserExitUsage {
			t.Fatalf("%v: exit = %d, want %d (stderr: %s)", argv, code, browserExitUsage, stderr)
		}
		if got := last(); got.Type != "" {
			t.Fatalf("%v: must not reach the bridge, but sent %q", argv, got.Type)
		}
	}
}

// TestBrowserCLIBadArgs pins the exact inputs that exit 6. Each case mirrors
// the upstream handler's own validation condition (not an argparse one):
//
//   - evaluate: cmd_evaluate rejects a missing/blank payload AFTER resolving
//     --js (read_arg_value), so @emptyfile and empty stdin are exit 6;
//   - script: cmd_script rejects a missing --skill/--script on the raw value
//     and a non-empty --args payload that is not a JSON object (whitespace
//     included — json.loads("   ") raises upstream too);
//   - type: a Go-only legacy verb, kept callable by name (upstream has no CLI
//     verb; its browser_type tool requires `text`, the condition mirrored).
//
// Parity: tools/src/gcu/cli_commands/evaluate.py::cmd_evaluate,
// tools/src/gcu/cli_commands/script.py::cmd_script,
// tools/src/gcu/errors.py::EXIT_VALIDATION
func TestBrowserCLIBadArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, nil)
	empty := filepath.Join(home, "empty.js")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	cases := [][]string{
		{"evaluate"},
		{"evaluate", "--js", ""},
		{"evaluate", "--js", "   "},
		{"evaluate", "--js", "@" + filepath.Join(home, "missing.js")},
		{"evaluate", "--js", "@" + empty},
		{"script", "--skill", "x"},
		{"script", "--skill", "x", "--script", "y", "--args", "not-json"},
		{"script", "--skill", "x", "--script", "y", "--args", "   "},
		{"script", "--skill", "x", "--script", "y", "--args", "[1]"},
		{"type", "#i"},
	}
	for _, argv := range cases {
		full := append(append([]string{}, argv...), "--bridge", server.URL)
		_, stderr, code := runBrowser(t, home, full...)
		if code != browserExitBadArgs {
			t.Fatalf("%v: exit = %d, want %d (stderr: %s)", argv, code, browserExitBadArgs, stderr)
		}
		if got := last(); got.Type != "" {
			t.Fatalf("%v: must not reach the bridge, but sent %q", argv, got.Type)
		}
	}
}

// TestBrowserCLIEvaluateResolutionMatchesUpstream pins cmd_evaluate's argument
// resolution details that the exit code alone cannot show: "--script" aliases
// "--js" with LAST-one-wins (one argparse dest), the resolved payload is what
// gets the blank check (@emptyfile / empty stdin -> exit 6 with the upstream
// message), and a file-style value is only a file when "@" starts the RAW
// value (leading space stays inline source).
//
// Parity: tools/src/gcu/cli_commands/evaluate.py::cmd_evaluate;
// tools/src/gcu/cli_commands/script.py::read_arg_value
func TestBrowserCLIEvaluateResolutionMatchesUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, nil)

	// --script is the same dest as --js upstream: the last flag wins.
	_, stderr, code := runBrowser(t, home, "evaluate", "--script", "first", "--js", "second", "--tab", "7", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("--script then --js: exit = %d (stderr: %s)", code, stderr)
	}
	if got, _ := last().Params["script"].(string); got != "second" {
		t.Fatalf("--script then --js sent %q, want %q", got, "second")
	}
	_, stderr, code = runBrowser(t, home, "evaluate", "--js", "first", "--script", "second", "--tab", "7", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("--js then --script: exit = %d (stderr: %s)", code, stderr)
	}
	if got, _ := last().Params["script"].(string); got != "second" {
		t.Fatalf("--js then --script sent %q, want %q", got, "second")
	}

	// A leading space keeps "@path" inline (upstream matches on the raw value).
	inline := " @" + filepath.Join(home, "missing.js")
	_, stderr, code = runBrowser(t, home, "evaluate", "--js", inline, "--tab", "7", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("leading-space @path: exit = %d (stderr: %s)", code, stderr)
	}
	if got, _ := last().Params["script"].(string); got != inline {
		t.Fatalf("leading-space @path sent %q, want %q", got, inline)
	}

	// Empty resolved payloads are exit 6: an empty @file and empty stdin.
	empty := filepath.Join(home, "empty.js")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	var stdout, stderr2 bytes.Buffer
	code = runBrowserCLI([]string{"evaluate", "--js", "@" + empty, "--bridge", server.URL}, &stdout, &stderr2, strings.NewReader(""))
	if code != browserExitBadArgs || !strings.Contains(stderr2.String(), "error: evaluate requires JavaScript") {
		t.Fatalf("empty @file: exit = %d, stderr = %q", code, stderr2.String())
	}
	stdout.Reset()
	stderr2.Reset()
	code = runBrowserCLI([]string{"evaluate", "--js", "-", "--bridge", server.URL}, &stdout, &stderr2, strings.NewReader(""))
	if code != browserExitBadArgs || !strings.Contains(stderr2.String(), "error: evaluate requires JavaScript") {
		t.Fatalf("empty stdin: exit = %d, stderr = %q", code, stderr2.String())
	}

	// A missing @file keeps upstream's "could not read <path>" wording and 6.
	_, stderr, code = runBrowser(t, home, "evaluate", "--js", "@"+filepath.Join(home, "missing.js"), "--bridge", server.URL)
	if code != browserExitBadArgs || !strings.Contains(stderr, "error: could not read ") {
		t.Fatalf("missing @file: exit = %d, stderr = %q", code, stderr)
	}
}

// TestBrowserCLIScriptResolutionMatchesUpstream pins the script-side value
// semantics: --skill/--script are checked on the RAW value (whitespace-only
// passes and is forwarded), an EMPTY resolved --args payload is "no args" (so
// @emptyfile / empty stdin pass and omit the key), whitespace-only --args is a
// JSON error like json.loads("   "), and a bad --timeout-s is exit 2 even when
// --skill is missing (argparse converts before the handler runs).
//
// Parity: tools/src/gcu/cli_commands/script.py::cmd_script
func TestBrowserCLIScriptResolutionMatchesUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, nil)
	empty := filepath.Join(home, "empty.json")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}

	// Whitespace-only skill passes upstream's `if not args.skill` and is sent.
	_, stderr, code := runBrowser(t, home, "script", "--skill", "   ", "--script", "y", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("blank skill: exit = %d (stderr: %s)", code, stderr)
	}
	if got, _ := last().Params["skill"].(string); got != "   " {
		t.Fatalf("blank skill sent %q, want %q", got, "   ")
	}

	// Empty resolved --args is skipped exactly like upstream's `if args_raw:`.
	_, stderr, code = runBrowser(t, home, "script", "--skill", "x", "--script", "y", "--args", "@"+empty, "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("--args @empty: exit = %d (stderr: %s)", code, stderr)
	}
	if _, present := last().Params["args"]; present {
		t.Fatalf("--args @empty sent an args payload: %#v", last().Params)
	}
	var stdout, stderr2 bytes.Buffer
	code = runBrowserCLI([]string{"script", "--skill", "x", "--script", "y", "--args", "-", "--bridge", server.URL},
		&stdout, &stderr2, strings.NewReader(""))
	if code != browserExitOK {
		t.Fatalf("empty stdin --args: exit = %d (stderr: %s)", code, stderr2.String())
	}
	if _, present := last().Params["args"]; present {
		t.Fatalf("empty stdin --args sent a payload: %#v", last().Params)
	}

	// Whitespace-only --args is non-empty (upstream json.loads raises) -> 6.
	_, stderr, code = runBrowser(t, home, "script", "--skill", "x", "--script", "y", "--args", "   ", "--bridge", server.URL)
	if code != browserExitBadArgs || !strings.Contains(stderr, "error: --args must be valid JSON") {
		t.Fatalf("blank --args: exit = %d, stderr = %q", code, stderr)
	}

	// argparse converts --timeout-s before cmd_script, so this is exit 2
	// (usage) even though --skill is missing.
	_, stderr, code = runBrowser(t, home, "script", "--timeout-s", "soon", "--bridge", server.URL)
	if code != browserExitUsage {
		t.Fatalf("bad --timeout-s without --skill: exit = %d, want %d (stderr: %s)", code, browserExitUsage, stderr)
	}
}

// TestBrowserCLIScriptProfileValidationMatchesUpstream pins
// nav.py::validate_browser_profile as invoked by cmd_script: "default" is
// never validated, an unknown label exits 6 with upstream's message, and an
// empty profile enumeration (dead bridge / older bridge) must NOT reject.
//
// Parity: tools/src/gcu/cli_commands/nav.py::validate_browser_profile
func TestBrowserCLIScriptProfileValidationMatchesUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, nil)

	_, stderr, code := runBrowser(t, home, "script", "--skill", "x", "--script", "y", "--browser-profile", "Nope", "--bridge", server.URL)
	if code != browserExitBadArgs || !strings.Contains(stderr, "error: unknown browser profile 'Nope'. connected: ['Work']") {
		t.Fatalf("unknown profile: exit = %d, stderr = %q", code, stderr)
	}
	if got := last(); got.Type != "" {
		t.Fatalf("unknown profile must not reach the bridge, sent %q", got.Type)
	}

	_, stderr, code = runBrowser(t, home, "script", "--skill", "x", "--script", "y", "--browser-profile", "Work", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("known profile: exit = %d (stderr: %s)", code, stderr)
	}
	if got, _ := last().Params["browser_profile"].(string); got != "Work" {
		t.Fatalf("known profile sent %q", got)
	}

	_, stderr, code = runBrowser(t, home, "script", "--skill", "x", "--script", "y", "--browser-profile", "default", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("default profile: exit = %d (stderr: %s)", code, stderr)
	}

	// No labels to validate against (dead bridge): the check must not guess.
	_, stderr, code = runBrowser(t, home, "script", "--skill", "x", "--script", "y",
		"--browser-profile", "Nope", "--bridge", closedBridgeURL(t))
	if code != browserExitNotConnected {
		t.Fatalf("dead bridge + unknown profile: exit = %d, want %d (stderr: %s)", code, browserExitNotConnected, stderr)
	}
}

// TestBrowserCLINotConnectedBeforeNotStarted pins the connectivity-first exit
// code of the R07 family: with no local tab AND no reachable bridge at all,
// upstream answers "not connected" (2), not "not started" (3). The tab
// pre-check must not mask a dead bridge.
//
// Parity-exempt: upstream has no test for tools/src/gcu/cli.py; this case pins
// the ported exit-code contract (gcu/errors.py) instead.
func TestBrowserCLINotConnectedBeforeNotStarted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	dead := closedBridgeURL(t)
	missing := filepath.Join(home, "missing.txt")
	for _, argv := range [][]string{
		{"upload", "#f", "--file", missing},
		{"reload"},
		{"navigate", "https://example.com"},
		{"select", "#f", "--value", "v"},
		{"page", "text", "h1"},
	} {
		_, stderr, code := runBrowser(t, home, append(argv, "--json", "--bridge", dead)...)
		if code != browserExitNotConnected {
			t.Fatalf("%v exit = %d, want %d (stderr: %s)", argv, code, browserExitNotConnected, stderr)
		}
	}
}

// closedBridgeURL returns a bridge URL whose port has nothing listening.
func closedBridgeURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return "http://" + address
}

// TestBrowserCLIUploadMissingFileMatchesUpstream pins R07: a missing upload
// file is NOT a local validation. The CLI forwards browser_upload and the
// bridge answers `File not found: <p>` — classify_result maps that to exit 4
// ("not found"), exactly like upstream's tool handler; without a known tab the
// CLI answers exit 3 ("Browser not started") like upstream's context check.
//
// Parity: tools/src/gcu/cli_commands/interact.py::cmd_upload
func TestBrowserCLIUploadMissingFileMatchesUpstream(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	missing := filepath.Join(home, "missing.txt")
	server, last := fakeBridge(t, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": false, "error": "File not found: " + missing,
		},
	})

	_, stderr, code := runBrowser(t, home, "upload", "#f", "--file", missing, "--tab", "7",
		"--json", "--bridge", server.URL)
	if code != browserExitNotFound {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, browserExitNotFound, stderr)
	}
	if got := last(); got.Type != "browser_upload" {
		t.Fatalf("bridge command = %q, want browser_upload (no local pre-check)", got.Type)
	}

	// No tab known for the profile: the CLI stops before the round trip with
	// upstream's not-started code.
	_, _, code = runBrowser(t, home, "upload", "#f", "--file", missing, "--json", "--bridge", server.URL)
	if code != browserExitNotStarted {
		t.Fatalf("tab-less exit = %d, want %d", code, browserExitNotStarted)
	}
}

// TestBrowserCLIScreenshotSpillsArtifact pins R01 on the CLI layer: the
// standalone screenshot writes the bridge's JPEG to
// <artifactDir>/browser_screenshot_<ms>_tab<id>.jpg and returns the upstream
// pointer contract (`saved_to`, `_image`, `size`, `imageType`) with the raw
// base64 `data` dropped.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::render_screenshot
func TestBrowserCLIScreenshotSpillsArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9}
	server, last := fakeBridge(t, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": true, "tabId": 7, "url": "https://example.com/",
			"data":     base64.StdEncoding.EncodeToString(jpegBytes),
			"mimeType": "image/jpeg", "format": "jpeg",
			"imageWidth": 800, "imageHeight": 500,
			"cssWidth": 1600, "cssHeight": 1000,
			"capturedWidth": 1600, "capturedHeight": 1000,
			"fullPage": false, "devicePixelRatio": 1, "physicalScale": 2,
			"annotated": false,
		},
	})

	stdout, stderr, code := runBrowser(t, home, "screenshot", "--tab", "7", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	got := last()
	if got.Type != "browser_screenshot" {
		t.Fatalf("bridge command = %q", got.Type)
	}
	// The CLI asks the bridge for the 800px JPEG payload upstream spills.
	for key, want := range map[string]string{"format": "jpeg", "quality": "75", "max_width": "800"} {
		if rendered := fmt.Sprintf("%v", got.Params[key]); rendered != want {
			t.Fatalf("params[%s] = %s, want %s", key, rendered, want)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, stdout)
	}
	if _, present := payload["data"]; present {
		t.Fatalf("inline base64 data must not be printed: %v", payload)
	}
	path, _ := payload["saved_to"].(string)
	if path == "" {
		t.Fatalf("saved_to missing: %v", payload)
	}
	if !strings.Contains(filepath.Base(path), "browser_screenshot_") || !strings.HasSuffix(path, "_tab7.jpg") {
		t.Fatalf("artifact name = %s", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("artifact unreadable: %v", err)
	}
	if !bytes.Equal(written, jpegBytes) {
		t.Fatalf("artifact bytes = %v", written)
	}
	if payload["size"] != float64(len(jpegBytes)) || payload["imageType"] != "jpeg" {
		t.Fatalf("size/imageType = %v/%v", payload["size"], payload["imageType"])
	}
	image, ok := payload["_image"].(map[string]any)
	if !ok {
		t.Fatalf("_image missing: %v", payload)
	}
	if image["path"] != path || image["mime"] != "image/jpeg" {
		t.Fatalf("_image = %v", image)
	}
	if intent, present := image["intent"]; !present || intent != nil {
		t.Fatalf("_image.intent = %v (present=%v), want null", intent, present)
	}
	if hint, _ := payload["scaleHint"].(string); !strings.Contains(hint, "fractions 0..1") {
		t.Fatalf("scaleHint = %q", hint)
	}
}

// TestBrowserCLIInteractCoordinateSpillsImagePointer pins R15 on the CLI layer:
// a coordinate click/hover/key result keeps its interaction dict and gains only
// `saved_to` + `_image` (upstream `_blocks_to_cli_dict`), with the JPEG written
// as <artifactDir>/browser_interact_<ms>_tab<id>.jpg — no screenshot metadata
// keys and no inline base64.
//
// Parity: tools/src/gcu/cli_commands/interact.py::_blocks_to_cli_dict
func TestBrowserCLIInteractCoordinateSpillsImagePointer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	jpegBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0xFF, 0xD9}
	server, last := fakeBridge(t, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": true, "action": "click", "tabId": 7, "x": 400.0, "y": 300.0,
			"button": "left", "clickCount": 1, "method": "cdp",
			"data": base64.StdEncoding.EncodeToString(jpegBytes), "mimeType": "image/jpeg",
		},
	})

	stdout, stderr, code := runBrowser(t, home, "interact", "--action", "left_click",
		"--coordinate", "0.5,0.5", "--tab", "7", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	got := last()
	if got.Type != "browser_interact" || got.Params["action"] != "left_click" {
		t.Fatalf("bridge command = %q params=%v", got.Type, got.Params)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, stdout)
	}
	if payload["action"] != "click" || payload["x"] != float64(400) {
		t.Fatalf("interaction dict not preserved: %v", payload)
	}
	for _, key := range []string{"data", "mimeType", "imageWidth", "imageHeight", "cssWidth", "cssHeight", "capturedWidth", "capturedHeight", "physicalScale", "devicePixelRatio", "annotated", "imageType", "size", "scaleHint", "crop_box", "fullPage"} {
		if _, present := payload[key]; present {
			t.Fatalf("unexpected %q in coordinate-action CLI result: %v", key, payload)
		}
	}
	path, _ := payload["saved_to"].(string)
	if !strings.Contains(filepath.Base(path), "browser_interact_") || !strings.HasSuffix(path, "_tab7.jpg") {
		t.Fatalf("artifact name = %s", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("artifact unreadable: %v", err)
	}
	if !bytes.Equal(written, jpegBytes) {
		t.Fatalf("artifact bytes = %v", written)
	}
	image, ok := payload["_image"].(map[string]any)
	if !ok {
		t.Fatalf("_image missing: %v", payload)
	}
	if image["path"] != path || image["mime"] != "image/jpeg" {
		t.Fatalf("_image = %v", image)
	}
	if intent, present := image["intent"]; !present || intent != nil {
		t.Fatalf("_image.intent = %v (present=%v), want null", intent, present)
	}
	want := []string{"_image", "action", "button", "clickCount", "method", "ok", "saved_to", "tabId", "x", "y"}
	var keys []string
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("key set = %v, want %v", keys, want)
	}
}

// TestBrowserCLISnapshotSpillsArtifact pins R02 on the CLI layer: `page
// snapshot` writes the AX tree to <artifactDir>/browser_snapshot_<ms>_tab<id>.txt
// and returns {ok,tabId,url,length,saved_to} — no inline tree — so `--head`
// reads real content instead of being a no-op.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::browser_snapshot
func TestBrowserCLISnapshotSpillsArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	tree := "- RootWebArea [ref=e1] \"Doc\"\n  - button [ref=e2] \"Go\" [href=\"https://example.com/go\"]\n"
	server, _ := fakeBridge(t, map[string]any{
		"ok": true,
		"result": map[string]any{
			"ok": true, "tabId": 7, "url": "https://example.com/",
			"tree": tree, "nodes": 2,
		},
	})

	stdout, stderr, code := runBrowser(t, home, "page", "snapshot", "--tab", "7", "--head", "24", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v (%s)", err, stdout)
	}
	if _, present := payload["tree"]; present {
		t.Fatalf("inline tree must not be printed: %v", payload)
	}
	if _, present := payload["nodes"]; present {
		t.Fatalf("nodes key must be replaced by length: %v", payload)
	}
	path, _ := payload["saved_to"].(string)
	if !strings.Contains(filepath.Base(path), "browser_snapshot_") || !strings.HasSuffix(path, "_tab7.txt") {
		t.Fatalf("saved_to = %s", path)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("artifact unreadable: %v", err)
	}
	if string(written) != tree {
		t.Fatalf("artifact = %q, want %q", written, tree)
	}
	if payload["length"] != float64(len(tree)) {
		t.Fatalf("length = %v, want %d", payload["length"], len(tree))
	}
	if head, _ := payload["head"].(string); head != tree[:24] {
		t.Fatalf("head = %q, want %q (--head must read the artifact)", head, tree[:24])
	}
}

// TestBrowserCLIIntentErrorIsDomain pins the upstream intent path: the CLI does
// not pre-check `--intent` for interact screenshot/zoom; the bridge answers
// ok:false and the taxonomy maps that to exit 1 (domain), exactly like
// gcu.browser.tools.interact._dispatch → classify_result upstream.
//
// Parity: tools/src/gcu/browser/tools/interact.py::_dispatch
func TestBrowserCLIIntentErrorIsDomain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	cases := []struct {
		name     string
		argv     []string
		response map[string]any
		action   string
	}{
		{
			name: "screenshot without intent",
			argv: []string{"interact", "--action", "screenshot", "--tab", "7"},
			response: map[string]any{"ok": true, "result": map[string]any{
				"ok": false,
				"error": "screenshot requires `intent` — one phrase naming the entity, " +
					"the target element, and the action you plan to take.",
			}},
			action: "screenshot",
		},
		{
			name: "zoom without region",
			argv: []string{"interact", "--action", "zoom", "--tab", "7"},
			response: map[string]any{"ok": true, "result": map[string]any{
				"ok": false, "error": "zoom requires region [x0, y0, x1, y1]",
			}},
			action: "zoom",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			server, last := fakeBridge(t, testCase.response)
			argv := append(append([]string{}, testCase.argv...), "--bridge", server.URL)
			_, stderr, code := runBrowser(t, home, argv...)
			if code != browserExitDomain {
				t.Fatalf("exit = %d, want %d (stderr: %s)", code, browserExitDomain, stderr)
			}
			got := last()
			if got.Type != "browser_interact" || fmt.Sprintf("%v", got.Params["action"]) != testCase.action {
				t.Fatalf("command = %q params = %v, want browser_interact/%s", got.Type, got.Params, testCase.action)
			}
			if _, present := got.Params["intent"]; present {
				t.Fatalf("CLI must not invent an intent: %v", got.Params)
			}
		})
	}
}

// TestBrowserCLINotConnected covers the cold case: nothing listens, so the CLI
// says so and exits 2 (agents then run `hive serve` / `hive-browser setup`).
//
// Parity-exempt: 上游 CLI 在 errors.run 里自带 ensure_bridge_host_running()，
// 先拉起 bridge_host 再退 2；Go CLI 不拉桥（桥归 serve 管），这条断言的「不拉桥」
// 分支上游没有等价测试。
func TestBrowserCLINotConnected(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	addr, _ := freeTCPAddr(t)
	stdout, stderr, code := runBrowser(t, "", "status", "--json", "--bridge", "http://"+addr)
	if code != browserExitNotConnected {
		t.Fatalf("exit = %d, want %d (stdout: %s stderr: %s)", code, browserExitNotConnected, stdout, stderr)
	}
	if !strings.Contains(stderr, "not reachable") {
		t.Fatalf("expected a reachability error, got %q", stderr)
	}
}

// TestBrowserCLIJSONOutput pins the --json envelope and the human form.
//
// Parity: tools/src/gcu/errors.py::run
func TestBrowserCLIJSONOutput(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	server, _ := fakeBridge(t, map[string]any{"ok": true, "result": map[string]any{"ok": true, "tabId": 42, "url": "https://example.com"}})
	defer server.Close()

	stdout, stderr, code := runBrowser(t, "", "open", "https://example.com", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if intOf(decoded["tabId"]) != 42 {
		t.Fatalf("tabId = %v, want 42", decoded["tabId"])
	}

	stdout, _, code = runBrowser(t, "", "open", "https://example.com", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("human form exit = %d", code)
	}
	if !strings.Contains(stdout, "tabId: 42") || !strings.Contains(stdout, "url: https://example.com") {
		t.Fatalf("human output = %q", stdout)
	}
}

// TestBrowserCLIStateFile pins the client-side session memory: `open` remembers
// the tab for the profile, tab-less commands reuse it, and closing it forgets.
//
// Parity-exempt: 上游把 profile→group 索引用 browser_contexts.json + rehydrate_contexts
// 重建（tools/src/gcu/browser/tools/lifecycle.py），没有对应单测；Go 侧只记 tabId。
func TestBrowserCLIStateFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	t.Setenv("HIVE_BROWSER_SESSION", "worker-1")
	server, last := fakeBridge(t, nil)
	defer server.Close()

	if _, stderr, code := runBrowser(t, home, "open", "https://example.com", "--json", "--bridge", server.URL); code != browserExitOK {
		t.Fatalf("open exit = %d (stderr: %s)", code, stderr)
	}
	// The fake bridge returns no tabId, so seed the state the way a real open
	// would (the supervisor test covers the real bridge).
	saveBrowserCLIState(filepath.Join(home, "browser_cli_state.json"), "worker-1", 77, "Default")

	if _, stderr, code := runBrowser(t, home, "page", "text", "h1", "--json", "--bridge", server.URL); code != browserExitOK {
		t.Fatalf("page text exit = %d (stderr: %s)", code, stderr)
	}
	if got := fmt.Sprintf("%v", last().Params["tab_id"]); got != "77" {
		t.Fatalf("tab_id = %s, want the remembered 77", got)
	}
	if got := last().Profile; got != "worker-1" {
		t.Fatalf("profile = %q, want worker-1", got)
	}

	// A different profile has no tab: the CLI refuses with exit 3.
	t.Setenv("HIVE_BROWSER_SESSION", "worker-2")
	_, stderr, code := runBrowser(t, home, "page", "text", "h1", "--json", "--bridge", server.URL)
	if code != browserExitNotStarted {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, browserExitNotStarted, stderr)
	}
	if !strings.Contains(stderr, "Browser not started") {
		t.Fatalf("stderr = %q", stderr)
	}

	// Closing the remembered tab clears the profile entry.
	saveBrowserCLIState(filepath.Join(home, "browser_cli_state.json"), "worker-2", 88, "Default")
	runBrowser(t, home, "tab", "close", "88", "--json", "--bridge", server.URL)
	state := loadBrowserCLIState(filepath.Join(home, "browser_cli_state.json"))
	if _, still := state["worker-2"]; still {
		t.Fatalf("closed tab left state behind: %v", state)
	}
}

// TestBrowserCLIHelp pins the local help paths (no bridge, exit 0).
//
// Parity: tools/src/gcu/cli.py::_help
func TestBrowserCLIHelp(t *testing.T) {
	stdout, _, code := runBrowser(t, "", "help")
	if code != browserExitOK || !strings.Contains(stdout, "hive-browser") {
		t.Fatalf("help exit = %d, output = %q", code, stdout)
	}
	stdout, _, code = runBrowser(t, "", "help", "tab")
	// argparse renders a subparser group page as usage + the choice set; the
	// group's own one-line summary only appears in the parent listing, so the
	// page carries the per-choice descriptions instead.
	if code != browserExitOK || !strings.Contains(stdout, "{list,close,activate}") ||
		!strings.Contains(stdout, "list tabs in this session's group") {
		t.Fatalf("help tab exit = %d, output = %q", code, stdout)
	}
	_, stderr, code := runBrowser(t, "", "help", "nope")
	// Upstream `_help` raises outside errors.run: an unknown topic exits 1
	// (domain), not the argparse usage code and not the Go-internal 6.
	if code != browserExitDomain || !strings.Contains(stderr, "no such command") {
		t.Fatalf("help nope exit = %d, stderr = %q", code, stderr)
	}
}

// TestBrowserCLIScreenshotWithoutIntent pins R05: the standalone `screenshot`
// verb must not require --intent. Upstream passes intent=None through to
// render_screenshot, so the command reaches the bridge and exits 0 when the
// capture succeeds; only interact --action screenshot answers ok:false.
//
// Parity: tools/src/gcu/cli_commands/capture.py::cmd_screenshot
func TestBrowserCLIScreenshotWithoutIntent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, last := fakeBridge(t, map[string]any{"ok": true, "result": map[string]any{
		"ok": true, "data": "aGVsbG8=", "mimeType": "image/png", "format": "png",
	}})

	stdout, stderr, code := runBrowser(t, home, "screenshot", "--tab", "7", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("exit = %d, want 0 (stdout: %s stderr: %s)", code, stdout, stderr)
	}
	got := last()
	if got.Type != "browser_screenshot" {
		t.Fatalf("command = %q, want browser_screenshot", got.Type)
	}
	if _, present := got.Params["intent"]; present {
		t.Fatalf("CLI must not invent an intent: %v", got.Params)
	}
	if got.Params["full_page"] != false || got.Params["timeout_ms"] != float64(5000) {
		t.Fatalf("params = %v", got.Params)
	}

	// --intent still flow through when given.
	server, last = fakeBridge(t, map[string]any{"ok": true, "result": map[string]any{"ok": true}})
	_, stderr, code = runBrowser(t, home, "screenshot", "--intent", "verify layout", "--tab", "7", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("intent exit = %d (stderr: %s)", code, stderr)
	}
	if got := last(); got.Params["intent"] != "verify layout" {
		t.Fatalf("intent not forwarded: %v", got.Params)
	}
}

// TestBrowserCLILeadingCommonFlags pins the upstream parent-parser placement:
// `hive-browser --json status` parses (no usage error). argparse lets the
// subparser's own default win for a shared dest, and the Go parser mirrors
// that — the leading value does not override the leaf's, so the documented
// agent form keeps --json after the command.
//
// Parity: tools/src/gcu/cli.py::_build_parser
func TestBrowserCLILeadingCommonFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HIVE_HOME", home)
	server, _ := fakeBridge(t, map[string]any{"ok": true, "result": map[string]any{"ok": true, "connected": false}})

	stdout, stderr, code := runBrowser(t, home, "--json", "status", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("--json status exit = %d, want 0 (stderr: %s)", code, stderr)
	}
	if strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Fatalf("leading --json must not override the leaf default (upstream prints the human form): %s", stdout)
	}

	stdout, stderr, code = runBrowser(t, home, "status", "--json", "--bridge", server.URL)
	if code != browserExitOK {
		t.Fatalf("status --json exit = %d (stderr: %s)", code, stderr)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("trailing --json must still print JSON: %v (%s)", err, stdout)
	}

	// No subcommand after a leading flag is still the argparse usage error.
	_, _, code = runBrowser(t, home, "--json")
	if code != browserExitUsage {
		t.Fatalf("bare --json exit = %d, want %d", code, browserExitUsage)
	}
}
