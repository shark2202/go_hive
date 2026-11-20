package server

// Parity: framework/server/app.py::handle_resources
// Parity: framework/server/app.py::handle_browser_status
// Parity: framework/server/app.py::handle_browser_status_stream
// Parity: framework/server/app.py::handle_browser_tab_reveal
//
// app.py registers seven direct routes: /api/health, /api/health/resources,
// /api/startup-status, /api/browser/status, /api/browser/status/stream,
// /api/browser/tab/reveal and the SPA catch-all. /api/health and
// /api/startup-status are already implemented in app.go, and the SPA
// catch-all lives there too; this file adds the four routes that have no
// other owner.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultBridgeWSBasePort is where the Go browser bridge dials its
	// extension WebSocket (browserbridge.DefaultWSAddr); the status HTTP
	// endpoint is one above it.
	defaultBridgeWSBasePort = 14829
	// legacyBridgeStatusPort is the status endpoint of the pre-port-move
	// Python bridge (and of the Go bridge's legacy listener generation).
	// Still probed as a fallback so a runtime on the legacy pair keeps
	// reporting its real state.
	legacyBridgeStatusPort = 9230
	browserPongStaleMS     = 15000.0
	browserDefaultPoll     = 3 * time.Second
	browserDefaultQuiet    = 5
	browserProbeTimeout    = 500 * time.Millisecond
	browserContextsTime    = 1 * time.Second
	browserRevealTime      = 2 * time.Second

	resourceDefaultHistory = 120
	resourceIntervalS      = 15.0
	resourceWarnAvailFrac  = 0.25
	resourceCritAvailFrac  = 0.15
	resourceWarnRenderers  = 45
	resourceCritRenderers  = 60
)

// AppExtraRouteDeps carries the seams the four app.py extras need. Every
// function is optional; the defaults probe the local GCU bridge over the
// same HTTP-over-TCP contract app.py uses and serve an empty resource
// monitor snapshot until a sampler is wired.
type AppExtraRouteDeps struct {
	// Resources returns the resource-monitor snapshot. historyN nil means the
	// whole buffer (upstream ?full=1); a pointer to 0 means snapshot only.
	Resources func(historyN *int) map[string]any
	// StatusAddr / StatusAddrs pin the bridge status endpoint. StatusAddrs is
	// the probe order; StatusAddr (single address) is the test seam kept for
	// the older cases. Both optional: nil resolves the environment's bridge
	// address list.
	StatusAddr   func() string
	StatusAddrs  func() []string
	ProbeStatus  func(addr string) map[string]any
	ProbeContext func(addr string) []map[string]any
	RevealTab    func(addr string, tabID int) map[string]any

	PollInterval   time.Duration
	KeepaliveTicks int
}

// statusAddrs is the bridge-status probe order: the injected seam first, then
// the environment (HIVE_BROWSERBRIDGE_HTTP, HIVE_BRIDGE_PORT base + 1, default
// 14830), then the legacy 9230 endpoint.
func (deps AppExtraRouteDeps) statusAddrs() []string {
	if deps.StatusAddrs != nil {
		if addrs := deps.StatusAddrs(); len(addrs) > 0 {
			return addrs
		}
	}
	if deps.StatusAddr != nil {
		return []string{deps.StatusAddr()}
	}
	return defaultBridgeStatusAddrs()
}

// resolveBridgeStatus probes the candidate addresses in order and returns the
// first one that answers with a live bridge plus that probe's payload. When no
// candidate answers with a bridge, it returns the primary address and the
// primary probe's payload (the classic {bridge:false} envelope).
func (deps AppExtraRouteDeps) resolveBridgeStatus() (string, map[string]any) {
	addrs := deps.statusAddrs()
	var primary map[string]any
	for _, addr := range addrs {
		probe := deps.probeStatus(addr)
		if pyTruthy(probe["bridge"]) {
			return addr, probe
		}
		if primary == nil {
			primary = probe
		}
	}
	if primary == nil {
		primary = map[string]any{"bridge": false, "connected": false, "connections": []any{}}
	}
	return addrs[0], primary
}

func (deps AppExtraRouteDeps) probeStatus(addr string) map[string]any {
	if deps.ProbeStatus != nil {
		return deps.ProbeStatus(addr)
	}
	return probeBridgeStatus(addr)
}

func (deps AppExtraRouteDeps) probeContexts(addr string) []map[string]any {
	if deps.ProbeContext != nil {
		return deps.ProbeContext(addr)
	}
	return probeBridgeContexts(addr)
}

func (deps AppExtraRouteDeps) revealTab(addr string, tabID int) map[string]any {
	if deps.RevealTab != nil {
		return deps.RevealTab(addr, tabID)
	}
	return revealBrowserTab(addr, tabID)
}

func (deps AppExtraRouteDeps) pollInterval() time.Duration {
	if deps.PollInterval > 0 {
		return deps.PollInterval
	}
	return browserDefaultPoll
}

func (deps AppExtraRouteDeps) keepaliveTicks() int {
	if deps.KeepaliveTicks > 0 {
		return deps.KeepaliveTicks
	}
	return browserDefaultQuiet
}

func RegisterAppExtraRoutes(mux *http.ServeMux, deps AppExtraRouteDeps) {
	mux.HandleFunc("GET /api/health/resources", func(w http.ResponseWriter, r *http.Request) {
		handleHealthResources(w, r, deps)
	})
	mux.HandleFunc("GET /api/browser/status", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserStatus(w, r, deps)
	})
	mux.HandleFunc("GET /api/browser/status/stream", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserStatusStream(w, r, deps)
	})
	mux.HandleFunc("POST /api/browser/tab/reveal", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserTabReveal(w, r, deps)
	})
}

// Parity: framework/server/app.py::handle_resources
func handleHealthResources(w http.ResponseWriter, r *http.Request, deps AppExtraRouteDeps) {
	query := r.URL.Query()
	full := false
	switch strings.ToLower(query.Get("full")) {
	case "1", "true", "yes":
		full = true
	}

	var historyN *int
	if !full {
		count := resourceDefaultHistory
		if raw, present := query["history"]; present {
			value := ""
			if len(raw) > 0 {
				value = raw[0]
			}
			count = pythonIntOr(value, resourceDefaultHistory)
		}
		historyN = &count
	}

	if deps.Resources == nil {
		writeJSON(w, http.StatusOK, emptyResourceSnapshot(historyN))
		return
	}
	writeJSON(w, http.StatusOK, deps.Resources(historyN))
}

// emptyResourceSnapshot mirrors ResourceMonitor.snapshot before the first
// sample: thresholds, cadence, no history, verdict "unknown".
func emptyResourceSnapshot(historyN *int) map[string]any {
	history := []any{}
	if historyN != nil && *historyN > 0 {
		history = []any{}
	}
	return map[string]any{
		"available":  false,
		"interval_s": resourceIntervalS,
		"thresholds": map[string]any{
			"warn_avail_mb":   resourceWarnAvailFrac,
			"crit_avail_mb":   resourceCritAvailFrac,
			"warn_avail_frac": resourceWarnAvailFrac,
			"crit_avail_frac": resourceCritAvailFrac,
			"warn_renderers":  resourceWarnRenderers,
			"crit_renderers":  resourceCritRenderers,
		},
		"history": history,
		"verdict": "unknown",
	}
}

// Parity: framework/server/app.py::handle_browser_status
func handleBrowserStatus(w http.ResponseWriter, r *http.Request, deps AppExtraRouteDeps) {
	writeJSON(w, http.StatusOK, browserStatusPayload(deps, r.URL.Query().Get("session_id")))
}

// Parity: framework/server/app.py::_browser_status_payload
func browserStatusPayload(deps AppExtraRouteDeps, sessionID string) map[string]any {
	addr, probe := deps.resolveBridgeStatus()
	status := copyStringAny(probe)
	pong, hasPong := numberValue(status["last_pong_age_ms"])
	connected := pyTruthy(status["connected"])
	bridge := pyTruthy(status["bridge"])

	health := "offline"
	switch {
	case connected && (!hasPong || pong < browserPongStaleMS):
		health = "healthy"
	case connected:
		health = "stale"
	case bridge:
		health = "disconnected"
	}

	payload := make(map[string]any, len(status)+2)
	for key, value := range status {
		payload[key] = value
	}
	payload["health"] = health

	if sessionID != "" {
		var active any
		if connected {
			active = activeTabForProfile(deps.probeContexts(addr), sessionID)
		}
		payload["active_tab"] = active
	}
	return payload
}

// Parity: framework/server/app.py::_active_tab_for_profile
func activeTabForProfile(contexts []map[string]any, profile string) any {
	var entry map[string]any
	for _, candidate := range contexts {
		if candidate == nil {
			continue
		}
		if name, ok := candidate["profile"].(string); ok && name == profile {
			entry = candidate
			break
		}
	}
	if entry == nil {
		return nil
	}
	tabs, _ := entry["tabs"].([]any)
	activeID := entry["activeTab"]
	var chosen map[string]any
	for _, item := range tabs {
		tab, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if pythonSameNumber(tab["id"], activeID) {
			chosen = tab
			break
		}
	}
	if chosen == nil {
		for _, item := range tabs {
			tab, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if pyTruthy(tab["active"]) {
				chosen = tab
				break
			}
		}
	}
	if chosen == nil {
		return nil
	}
	title := chosen["title"]
	url := chosen["url"]
	if !pyTruthy(title) && !pyTruthy(url) {
		return nil
	}
	return map[string]any{"id": chosen["id"], "title": title, "url": url}
}

// browserSignature mirrors the tuple the upstream stream compares: bridge,
// connected, health and the active tab's title/url.
type browserSignature struct {
	bridge    bool
	connected bool
	health    string
	titleSet  bool
	title     string
	urlSet    bool
	url       string
}

func signatureOfPayload(payload map[string]any) browserSignature {
	signature := browserSignature{
		bridge:    pyTruthy(payload["bridge"]),
		connected: pyTruthy(payload["connected"]),
		health:    pyStr(payload["health"]),
	}
	if tab, ok := payload["active_tab"].(map[string]any); ok {
		signature.title, signature.titleSet = tab["title"].(string)
		signature.url, signature.urlSet = tab["url"].(string)
	}
	return signature
}

// Parity: framework/server/app.py::handle_browser_status_stream
func handleBrowserStatusStream(w http.ResponseWriter, r *http.Request, deps AppExtraRouteDeps) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sessionID := r.URL.Query().Get("session_id")

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	var last browserSignature
	hasLast := false
	quietTicks := 0
	for {
		payload := browserStatusPayload(deps, sessionID)
		signature := signatureOfPayload(payload)
		if !hasLast || signature != last {
			writeNamedSSEEvent(w, flusher, "status", payload)
			last = signature
			hasLast = true
			quietTicks = 0
		} else {
			quietTicks++
			if quietTicks >= deps.keepaliveTicks() {
				fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
				quietTicks = 0
			}
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(deps.pollInterval()):
		}
	}
}

// Parity: framework/server/app.py::handle_browser_tab_reveal
func handleBrowserTabReveal(w http.ResponseWriter, r *http.Request, deps AppExtraRouteDeps) {
	decoded, err := decodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		body = map[string]any{}
	}
	tabID, ok := pythonIntValue(body["tab_id"])
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "tab_id (int) is required",
		})
		return
	}
	writeJSON(w, http.StatusOK, deps.revealTab(revealAddr(deps), tabID))
}

// revealAddr picks the same bridge endpoint the status route reports, so the
// jump-to-tab button targets the runtime that is actually listening.
func revealAddr(deps AppExtraRouteDeps) string {
	addr, _ := deps.resolveBridgeStatus()
	return addr
}

// Parity: framework/server/app.py::_probe_browser_bridge
func probeBridgeStatus(addr string) map[string]any {
	unavailable := map[string]any{
		"bridge":      false,
		"connected":   false,
		"connections": []any{},
	}
	raw, err := bridgeHTTPRequest(addr, "GET /status HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n", browserProbeTimeout)
	if err != nil {
		return unavailable
	}
	body, ok := bridgeResponseBody(raw)
	if !ok {
		return unavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil || data == nil {
		return unavailable
	}
	connections, _ := data["connections"].([]any)
	if connections == nil {
		connections = []any{}
	}
	primary, _ := firstMap(connections)
	out := map[string]any{
		"bridge":      true,
		"connected":   pyTruthy(data["connected"]),
		"connections": connections,
	}
	if value, present := primary["last_pong_age_ms"]; present {
		out["last_pong_age_ms"] = value
	}
	if value, present := primary["version"]; present {
		out["extension_version"] = value
	}
	if value, present := data["uptime_ms"]; present {
		out["uptime_ms"] = value
	}
	return out
}

// Parity: framework/server/app.py::_probe_browser_contexts
func probeBridgeContexts(addr string) []map[string]any {
	raw, err := bridgeHTTPRequest(addr, "GET /contexts HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n", browserContextsTime)
	if err != nil {
		return nil
	}
	body, ok := bridgeResponseBody(raw)
	if !ok {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var data map[string]any
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil
	}
	rows, ok := data["contexts"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if entry, ok := row.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

// Parity: framework/server/app.py::_reveal_browser_tab
func revealBrowserTab(addr string, tabID int) map[string]any {
	raw, err := bridgeHTTPRequest(addr,
		fmt.Sprintf("POST /tabs/%d/reveal HTTP/1.0\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\n\r\n", tabID),
		browserRevealTime)
	if err != nil {
		return map[string]any{"ok": false, "error": "bridge unreachable"}
	}
	body, ok := bridgeResponseBody(raw)
	if !ok {
		return map[string]any{"ok": false, "error": "no response"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil || payload == nil {
		return map[string]any{"ok": true}
	}
	return payload
}

// defaultBridgeStatusAddrs resolves the bridge status probe order. The bridge
// moved from the legacy 9229/9230 pair to 14829/14830, so probing 9230 alone
// reported a running bridge as offline; the primary endpoint is the
// supervisor's explicit override (HIVE_BROWSERBRIDGE_HTTP) or
// HIVE_BRIDGE_PORT + 1 (default 14830, the port the Go bridge binds), and the
// legacy 9230 endpoint stays as a fallback for a pre-port-move runtime.
func defaultBridgeStatusAddrs() []string {
	primary := ""
	if raw := strings.TrimSpace(os.Getenv("HIVE_BROWSERBRIDGE_HTTP")); raw != "" {
		primary = bridgeStatusHostPort(raw)
	}
	if primary == "" {
		port := defaultBridgeWSBasePort
		if raw := strings.TrimSpace(os.Getenv("HIVE_BRIDGE_PORT")); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				port = parsed
			}
		}
		primary = net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1))
	}
	addrs := []string{primary}
	if legacy := net.JoinHostPort("127.0.0.1", strconv.Itoa(legacyBridgeStatusPort)); legacy != primary {
		addrs = append(addrs, legacy)
	}
	return addrs
}

// bridgeStatusHostPort normalises HIVE_BROWSERBRIDGE_HTTP ("host:port",
// "http://host:port" or a URL) to a dialable host:port, or "" when there is no
// host to dial.
func bridgeStatusHostPort(raw string) string {
	text := strings.TrimSpace(raw)
	if strings.Contains(text, "://") {
		parsed, err := url.Parse(text)
		if err != nil || parsed.Host == "" {
			return ""
		}
		return parsed.Host
	}
	if index := strings.IndexByte(text, '/'); index >= 0 {
		text = text[:index]
	}
	if _, _, err := net.SplitHostPort(text); err != nil {
		return ""
	}
	return text
}

// bridgeHTTPRequest speaks the bridge's HTTP/1.0-over-TCP contract and reads
// the response to EOF (the bridge closes after replying).
func bridgeHTTPRequest(addr, request string, timeout time.Duration) ([]byte, error) {
	connection, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(connection, request); err != nil {
		return nil, err
	}
	return io.ReadAll(connection)
}

func bridgeResponseBody(raw []byte) ([]byte, bool) {
	marker := []byte("\r\n\r\n")
	index := bytes.Index(raw, marker)
	if index < 0 {
		return nil, false
	}
	return raw[index+len(marker):], true
}

func firstMap(items []any) (map[string]any, bool) {
	if len(items) == 0 {
		return map[string]any{}, false
	}
	entry, ok := items[0].(map[string]any)
	if !ok {
		return map[string]any{}, false
	}
	return entry, true
}

func copyStringAny(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func numberValue(value any) (float64, bool) {
	return toFloat(value)
}

func pythonSameNumber(left, right any) bool {
	if left == nil || right == nil {
		return false
	}
	leftNumber, leftOK := toFloat(left)
	rightNumber, rightOK := toFloat(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	return leftIsText && rightIsText && leftText == rightText
}

// pythonIntValue mirrors isinstance(value, int) for a JSON-decoded value.
// Python's bool is a subclass of int, so true counts as 1.
func pythonIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		text := typed.String()
		if strings.ContainsAny(text, ".eE") {
			return 0, false
		}
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int(typed), true
	}
	return 0, false
}

// pythonIntOr mirrors int(raw) with a fallback: surrounding whitespace and
// underscores between digits are accepted, everything else is the fallback.
func pythonIntOr(raw string, fallback int) int {
	text := strings.TrimSpace(raw)
	if text == "" {
		return fallback
	}
	sign := ""
	if text[0] == '+' || text[0] == '-' {
		sign, text = text[:1], text[1:]
	}
	if text == "" {
		return fallback
	}
	cleaned := strings.ReplaceAll(text, "_", "")
	if cleaned == "" || strings.HasPrefix(cleaned, "_") || strings.HasSuffix(cleaned, "_") {
		return fallback
	}
	for _, char := range cleaned {
		if char < '0' || char > '9' {
			return fallback
		}
	}
	parsed, err := strconv.Atoi(sign + cleaned)
	if err != nil {
		return fallback
	}
	return parsed
}
