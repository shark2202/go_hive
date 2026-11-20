package cli

// Parity: tools/src/gcu/errors.py::classify_result
//
// browser_cli_client.go is the transport + exit-code half of `hive-browser`:
// one POST /command per invocation against the bridge's local HTTP API, plus
// the taxonomy that turns a result into the stable exit code agents branch on.
// The upstream CLI stands up an in-process `RemoteBridge` client; the Go CLI
// speaks the same command envelope over HTTP because the main module must not
// import the browserbridge module.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const browserCLIHTTPTimeout = 120 * time.Second

// browserCommandRequest is the POST /command envelope.
type browserCommandRequest struct {
	Profile string         `json:"profile,omitempty"`
	Type    string         `json:"type"`
	Params  map[string]any `json:"params"`
}

// post runs one bridge command. A non-nil error is either a transport failure
// (bridge unreachable, so the caller can exit 2) or an HTTP-level refusal whose
// exit code the caller has no better information for than "domain".
func (c *browserCLI) post(base, profile, typ string, params map[string]any) (map[string]any, error) {
	body, err := json.Marshal(browserCommandRequest{Profile: profile, Type: typ, Params: params})
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", typ, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserCLIHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/command", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := browserHTTPClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("browser bridge not reachable at %s: %w", base, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, fmt.Errorf("read bridge response: %w", readErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("bridge returned invalid JSON (HTTP %d): %s", resp.StatusCode, truncateForError(string(payload)))
	}
	if resp.StatusCode != http.StatusOK {
		return decoded, &browserBridgeRefusal{status: resp.StatusCode, payload: decoded}
	}
	result, _ := decoded["result"].(map[string]any)
	if result == nil {
		result = map[string]any{}
	}
	return result, nil
}

// browserBridgeRefusal is an HTTP-level failure that still carries a structured
// bridge error (code + message), so the CLI can classify it precisely.
type browserBridgeRefusal struct {
	status  int
	payload map[string]any
}

func (e *browserBridgeRefusal) Error() string {
	message := stringOf(e.payload["message"])
	if message == "" {
		if errValue, present := e.payload["error"]; present {
			message = fmt.Sprintf("%v", errValue)
		}
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", e.status)
	}
	return message
}

func browserHTTPClient() *http.Client {
	return &http.Client{Timeout: browserCLIHTTPTimeout}
}

func truncateForError(text string) string {
	if len(text) > 300 {
		return text[:300] + "…"
	}
	return text
}

// isConnectError reports whether the failure was "nothing is listening".
func isConnectError(err error) bool {
	if err == nil {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		var opErr *net.OpError
		if errors.As(urlErr.Err, &opErr) {
			return true
		}
		if errors.Is(urlErr.Err, context.DeadlineExceeded) {
			return false
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return strings.Contains(err.Error(), "not reachable")
}

// classifyBrowserResult ports errors.py::classify_result: a result dict without
// `ok: false` is success; failures are matched to the most specific exit code
// first (dialog → rate limit → not connected → not started → not found →
// ambiguous → domain).
func classifyBrowserResult(result map[string]any) int {
	if result == nil {
		return browserExitOK
	}
	ok, hasOK := result["ok"].(bool)
	if !hasOK || ok {
		return browserExitOK
	}
	if _, pending := result["pending_dialog"]; pending {
		return browserExitPendingDialog
	}
	errorText := stringOf(result["error"])
	low := strings.ToLower(errorText)
	if errorText == "rate_limited" {
		return browserExitRateLimited
	}
	if connected, present := result["connected"].(bool); present && !connected {
		return browserExitNotConnected
	}
	if strings.Contains(low, "browser bridge") || (strings.Contains(low, "extension") && strings.Contains(low, "connect")) {
		return browserExitNotConnected
	}
	if strings.Contains(low, "not started") {
		return browserExitNotStarted
	}
	// The Go bridge phrases this as "no active tab is known for this profile".
	// Upstream's CLI answers "Browser not started" for the same state, so the
	// agent contract stays "open a tab first".
	if strings.Contains(low, "no active tab") {
		return browserExitNotStarted
	}
	for _, fragment := range []string{"no tab to close", "no tabs", "no such tab", "not found"} {
		if strings.Contains(low, fragment) {
			return browserExitNotFound
		}
	}
	if strings.Contains(low, "ambiguous") || (strings.Contains(low, "multiple") && strings.Contains(low, "match")) {
		return browserExitAmbiguous
	}
	return browserExitDomain
}

// classifyBrowserError maps the bridge's structured error code (the HTTP
// failure path) onto the same taxonomy.
func classifyBrowserError(code, message string) int {
	low := strings.ToLower(message)
	switch code {
	case "not_connected":
		return browserExitNotConnected
	case "bad_request", "unknown_command", "no_browser_profile", "no_such_profile", "unsupported_extension":
		return browserExitBadArgs
	case "not_found":
		return browserExitNotFound
	case "ambiguous_profile":
		return browserExitAmbiguous
	case "rate_limited":
		return browserExitRateLimited
	case "invalid_state":
		if strings.Contains(low, "not started") || strings.Contains(low, "no active tab") {
			return browserExitNotStarted
		}
		return browserExitNotFound
	case "timeout", "connection_lost", "extension_error", "cdp_error", "conflict", "create_failed", "ungroup_no_op":
		return browserExitDomain
	}
	if strings.Contains(low, "pending dialog") {
		return browserExitPendingDialog
	}
	return browserExitDomain
}

// renderBrowserResult prints the terse human form (upstream `_render`).
func renderBrowserResult(out io.Writer, result map[string]any) {
	if items, ok := result["items"].([]any); ok {
		if len(items) == 0 {
			fmt.Fprintln(out, "(no results)")
			return
		}
		rows := make([]map[string]any, 0, len(items))
		columns := make([]string, 0, 4)
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			rows = append(rows, row)
			for _, key := range sortedKeys(row) {
				if !containsStringValue(columns, key) {
					columns = append(columns, key)
				}
			}
		}
		fmt.Fprintln(out, strings.Join(columns, " | "))
		for _, row := range rows {
			cells := make([]string, 0, len(columns))
			for _, column := range columns {
				value, present := row[column]
				if !present || value == nil {
					cells = append(cells, "")
					continue
				}
				cells = append(cells, fmt.Sprintf("%v", value))
			}
			fmt.Fprintln(out, strings.Join(cells, " | "))
		}
		return
	}
	for _, key := range sortedKeys(result) {
		value := result[key]
		if nested, ok := value.(map[string]any); ok {
			encoded, err := json.Marshal(nested)
			if err == nil {
				value = string(encoded)
			}
		}
		fmt.Fprintf(out, "%s: %v\n", key, value)
	}
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// --- local session state ----------------------------------------------------

// browserCLIStateEntry mirrors the part of upstream's persisted contexts the
// CLI needs: which tab this profile is acting on.
type browserCLIStateEntry struct {
	TabID          int    `json:"tabId"`
	BrowserProfile string `json:"browser_profile,omitempty"`
	UpdatedAtMS    int64  `json:"updated_at_ms"`
}

// loadBrowserCLIState reads <HIVE_HOME>/browser_cli_state.json; a missing or
// unreadable file is an empty state, never an error (the bridge is the source
// of truth for everything else).
func loadBrowserCLIState(path string) map[string]browserCLIStateEntry {
	if strings.TrimSpace(path) == "" {
		return map[string]browserCLIStateEntry{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]browserCLIStateEntry{}
	}
	out := map[string]browserCLIStateEntry{}
	if json.Unmarshal(data, &out) != nil {
		return map[string]browserCLIStateEntry{}
	}
	return out
}

func saveBrowserCLIState(path, profile string, tabID int, browserProfile string) {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(profile) == "" || tabID <= 0 {
		return
	}
	state := loadBrowserCLIState(path)
	state[profile] = browserCLIStateEntry{
		TabID:          tabID,
		BrowserProfile: browserProfile,
		UpdatedAtMS:    time.Now().UnixMilli(),
	}
	writeBrowserCLIState(path, state)
}

// forgetBrowserCLITab drops a tab; when it was the remembered active tab the
// profile entry goes away, so the next tab-less command says "open first".
func forgetBrowserCLITab(path, profile string, tabID int) {
	if strings.TrimSpace(path) == "" {
		return
	}
	state := loadBrowserCLIState(path)
	if entry, ok := state[profile]; ok && (tabID <= 0 || entry.TabID == tabID) {
		delete(state, profile)
		writeBrowserCLIState(path, state)
	}
}

func clearBrowserCLIState(path, profile string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	state := loadBrowserCLIState(path)
	if _, ok := state[profile]; !ok {
		return
	}
	delete(state, profile)
	writeBrowserCLIState(path, state)
}

// writeBrowserCLIState writes atomically (temp file + rename) and ignores
// failures: bookkeeping must never fail an otherwise successful command.
func writeBrowserCLIState(path string, state map[string]browserCLIStateEntry) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return
	}
	_ = os.Rename(temp, path)
}

// --- small helpers ----------------------------------------------------------

func intOf(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := strconv.Atoi(typed.String())
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return 0
}

func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func parseFloatValue(raw string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", strings.TrimSpace(raw))
	}
	return value, nil
}

func parsePositiveInt(raw string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%q is not a positive integer", strings.TrimSpace(raw))
	}
	return value, nil
}
