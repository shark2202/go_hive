// Package routesapp serves the four app.py "extra" routes that have no other
// owner: the resource-monitor snapshot, the browser-bridge status probe, its
// SSE stream, and the tab-reveal proxy. It depends on routeutil and the Deps
// function seams, never on the parent server package.
package routesapp

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
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/resources"
	"github.com/shark2202/go_hive/go-hive/internal/server/routeutil"
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
	browserContextsTime    = 1 * time.Second
	browserRevealTime      = 2 * time.Second

	resourceDefaultHistory = resources.DefaultSnapshotLength
)

// Deps carries the seams the four app.py extras need. Every
// function is optional; the defaults probe the local GCU bridge over the
// same HTTP-over-TCP contract app.py uses and serve an empty resource
// monitor snapshot until a sampler is wired.
type Deps struct {
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
func (deps Deps) statusAddrs() []string {
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
func (deps Deps) resolveBridgeStatus() (string, map[string]any) {
	addrs := deps.statusAddrs()
	var primary map[string]any
	for _, addr := range addrs {
		probe := deps.probeStatus(addr)
		if routeutil.PyTruthy(probe["bridge"]) {
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

func (deps Deps) probeStatus(addr string) map[string]any {
	if deps.ProbeStatus != nil {
		return deps.ProbeStatus(addr)
	}
	return probeBridgeStatus(addr)
}

func (deps Deps) probeContexts(addr string) []map[string]any {
	if deps.ProbeContext != nil {
		return deps.ProbeContext(addr)
	}
	return probeBridgeContexts(addr)
}

func (deps Deps) revealTab(addr string, tabID int) map[string]any {
	if deps.RevealTab != nil {
		return deps.RevealTab(addr, tabID)
	}
	return revealBrowserTab(addr, tabID)
}

func (deps Deps) pollInterval() time.Duration {
	if deps.PollInterval > 0 {
		return deps.PollInterval
	}
	return browserDefaultPoll
}

func (deps Deps) keepaliveTicks() int {
	if deps.KeepaliveTicks > 0 {
		return deps.KeepaliveTicks
	}
	return browserDefaultQuiet
}

func RegisterRoutes(mux *http.ServeMux, deps Deps) {
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
func handleHealthResources(w http.ResponseWriter, r *http.Request, deps Deps) {
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
		routeutil.WriteJSON(w, http.StatusOK, emptyResourceSnapshot(historyN))
		return
	}
	routeutil.WriteJSON(w, http.StatusOK, deps.Resources(historyN))
}

// emptyResourceSnapshot mirrors ResourceMonitor.snapshot before the first
// sample: thresholds, cadence, no history, verdict "unknown".
func emptyResourceSnapshot(historyN *int) map[string]any {
	history := []any{}
	if historyN != nil && *historyN > 0 {
		history = []any{}
	}
	thresholds := resources.ThresholdsFromEnv()
	return map[string]any{
		"available":  false,
		"interval_s": resources.IntervalSeconds(),
		"thresholds": map[string]any{
			"warn_avail_mb":   thresholds.WarnAvailFrac,
			"crit_avail_mb":   thresholds.CritAvailFrac,
			"warn_avail_frac": thresholds.WarnAvailFrac,
			"crit_avail_frac": thresholds.CritAvailFrac,
			"warn_renderers":  thresholds.WarnRenderers,
			"crit_renderers":  thresholds.CritRenderers,
		},
		"history": history,
		"verdict": "unknown",
	}
}

// Parity: framework/server/app.py::handle_browser_status
func handleBrowserStatus(w http.ResponseWriter, r *http.Request, deps Deps) {
	routeutil.WriteJSON(w, http.StatusOK, browserStatusPayload(deps, r.URL.Query().Get("session_id")))
}

// Parity: framework/server/app.py::_browser_status_payload
func browserStatusPayload(deps Deps, sessionID string) map[string]any {
	addr, probe := deps.resolveBridgeStatus()
	status := copyStringAny(probe)
	pong, hasPong := numberValue(status["last_pong_age_ms"])
	connected := routeutil.PyTruthy(status["connected"])
	bridge := routeutil.PyTruthy(status["bridge"])

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
			if routeutil.PyTruthy(tab["active"]) {
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
	if !routeutil.PyTruthy(title) && !routeutil.PyTruthy(url) {
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
		bridge:    routeutil.PyTruthy(payload["bridge"]),
		connected: routeutil.PyTruthy(payload["connected"]),
		health:    routeutil.PyStr(payload["health"]),
	}
	if tab, ok := payload["active_tab"].(map[string]any); ok {
		signature.title, signature.titleSet = tab["title"].(string)
		signature.url, signature.urlSet = tab["url"].(string)
	}
	return signature
}

// Parity: framework/server/app.py::handle_browser_status_stream
func handleBrowserStatusStream(w http.ResponseWriter, r *http.Request, deps Deps) {
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
			routeutil.WriteNamedSSEEvent(w, flusher, "status", payload)
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
func handleBrowserTabReveal(w http.ResponseWriter, r *http.Request, deps Deps) {
	decoded, err := routeutil.DecodeJSONValue(r)
	body, ok := decoded.(map[string]any)
	if err != nil || !ok {
		body = map[string]any{}
	}
	tabID, ok := routeutil.PyIntValue(body["tab_id"])
	if !ok {
		routeutil.WriteJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "tab_id (int) is required",
		})
		return
	}
	routeutil.WriteJSON(w, http.StatusOK, deps.revealTab(revealAddr(deps), tabID))
}

// revealAddr picks the same bridge endpoint the status route reports, so the
// jump-to-tab button targets the runtime that is actually listening.
func revealAddr(deps Deps) string {
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
	raw, err := routeutil.BridgeHTTPRequest(addr, "GET /status HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n", routeutil.BridgeProbeTimeout)
	if err != nil {
		return unavailable
	}
	body, ok := routeutil.BridgeResponseBody(raw)
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
		"connected":   routeutil.PyTruthy(data["connected"]),
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
	raw, err := routeutil.BridgeHTTPRequest(addr, "GET /contexts HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n", browserContextsTime)
	if err != nil {
		return nil
	}
	body, ok := routeutil.BridgeResponseBody(raw)
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
	raw, err := routeutil.BridgeHTTPRequest(addr,
		fmt.Sprintf("POST /tabs/%d/reveal HTTP/1.0\r\nHost: 127.0.0.1\r\nContent-Length: 0\r\n\r\n", tabID),
		browserRevealTime)
	if err != nil {
		return map[string]any{"ok": false, "error": "bridge unreachable"}
	}
	body, ok := routeutil.BridgeResponseBody(raw)
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
	return routeutil.PyFloat(value)
}

func pythonSameNumber(left, right any) bool {
	if left == nil || right == nil {
		return false
	}
	leftNumber, leftOK := routeutil.PyFloat(left)
	rightNumber, rightOK := routeutil.PyFloat(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	return leftIsText && rightIsText && leftText == rightText
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
