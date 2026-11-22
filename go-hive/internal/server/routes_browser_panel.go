package server

// Parity: framework/server/routes_workers.py::handle_browser_contexts
// Parity: framework/server/routes_workers.py::handle_browser_tabs
// Parity: framework/server/routes_workers.py::handle_close_browser_context
//
// The desktop's browser panel reads three process-global routes upstream:
// GET /api/browser/contexts (tab-group registry), GET /api/browser/tabs (every
// tab the extension sees) and POST /api/browser/contexts/close. In this port
// that state lives in the separate Browser Bridge process, so the routes read
// the bridge's local agent API and reshape its rows into the upstream bodies:
//
//	contexts → {available, count, contexts:[{profile, group_id, name,
//	            tab_count, active_tab_id}]}
//	tabs     → {available, count, live_count, discarded_count,
//	            hive_group_count, tabs:[{id, windowId, groupId, isHiveGroup,
//	            url, title, discarded, active, audible, pinned}]}
//	close    → {closed:[…]} (400 {"error": …} for a missing profile)
//
// Upstream answers 200 with available:false (and an empty collection) whenever
// the browser tools / bridge are not usable, so an unreachable bridge maps to
// the same envelope instead of a 503: the desktop panel distinguishes states by
// the body, not by the status code.

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	browserPanelTimeout    = 5 * time.Second
	browserPanelBodyLimit  = 1 << 20
	browserPanelReplyLimit = 4 << 20
	// browserPanelBridgePort is the documented upstream bridge port base
	// (HIVE_BRIDGE_PORT); the agent HTTP API listens one above it.
	browserPanelBridgePort = 14829
)

// BrowserPanelDeps carries the seams the three panel routes need. Both are
// optional: nil resolves the environment's bridge address and uses a short
// local-host timeout.
type BrowserPanelDeps struct {
	// BaseURL returns the bridge origin, e.g. "http://127.0.0.1:14830".
	BaseURL func() string
	Client  *http.Client
}

func (deps BrowserPanelDeps) baseURL() string {
	if deps.BaseURL != nil {
		return strings.TrimRight(strings.TrimSpace(deps.BaseURL()), "/")
	}
	return defaultBrowserPanelBase()
}

func (deps BrowserPanelDeps) client() *http.Client {
	if deps.Client != nil {
		return deps.Client
	}
	return &http.Client{Timeout: browserPanelTimeout}
}

// defaultBrowserPanelBase resolves the bridge's agent HTTP API origin: the
// supervisor's explicit override (HIVE_BROWSERBRIDGE_HTTP) first, then the
// legacy HIVE_BRIDGE_PORT base + 1, then the documented default 14830.
func defaultBrowserPanelBase() string {
	if raw := strings.TrimSpace(os.Getenv("HIVE_BROWSERBRIDGE_HTTP")); raw != "" {
		return "http://" + raw
	}
	port := browserPanelBridgePort
	if raw := strings.TrimSpace(os.Getenv("HIVE_BRIDGE_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			port = parsed
		}
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1))
}

func RegisterBrowserPanelRoutes(mux *http.ServeMux, deps BrowserPanelDeps) {
	mux.HandleFunc("GET /api/browser/contexts", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserPanelContexts(w, r, deps)
	})
	mux.HandleFunc("GET /api/browser/tabs", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserPanelTabs(w, r, deps)
	})
	mux.HandleFunc("POST /api/browser/contexts/close", func(w http.ResponseWriter, r *http.Request) {
		handleBrowserPanelClose(w, r, deps)
	})
}

// panelBridgeReply is one bridge response: its status code and decoded body.
type panelBridgeReply struct {
	status int
	body   map[string]any
}

// getBridgeJSON issues one GET against the bridge and decodes the JSON body.
// A transport failure (or a body that is not a JSON object) yields a reason
// string instead; callers turn that into the upstream available:false body.
func getBridgeJSON(r *http.Request, deps BrowserPanelDeps, base, path string) (panelBridgeReply, string) {
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+path, nil)
	if err != nil {
		return panelBridgeReply{}, "browser bridge not reachable at " + base
	}
	response, err := deps.client().Do(request)
	if err != nil {
		return panelBridgeReply{}, "browser bridge not reachable at " + base
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, browserPanelReplyLimit))
	if err != nil {
		return panelBridgeReply{}, "browser bridge response could not be read"
	}
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil || body == nil {
		return panelBridgeReply{}, "browser bridge returned invalid JSON"
	}
	return panelBridgeReply{status: response.StatusCode, body: body}, ""
}

// bridgeReason maps a failed bridge reply to the upstream reason strings: the
// "no extension connected" state upstream calls "bridge not connected".
func bridgeReason(reply panelBridgeReply, fallback string) string {
	if code := stringOfJSON(reply.body["code"]); code == "not_connected" {
		return "bridge not connected"
	}
	if message := stringOfJSON(reply.body["error"]); message != "" {
		return message
	}
	if message := stringOfJSON(reply.body["message"]); message != "" {
		return message
	}
	return fallback
}

func stringOfJSON(value any) string {
	text, _ := value.(string)
	return text
}

// handleBrowserPanelContexts is the upstream handle_browser_contexts shape: a
// registry snapshot with available/count and one row per profile→tab group.
func handleBrowserPanelContexts(w http.ResponseWriter, r *http.Request, deps BrowserPanelDeps) {
	base := deps.baseURL()
	reply, reason := getBridgeJSON(r, deps, base, "/contexts")
	if reason != "" {
		writeBrowserPanelUnavailable(w, reason, "contexts")
		return
	}
	if reply.status != http.StatusOK {
		writeBrowserPanelUnavailable(w, bridgeReason(reply, "browser bridge answered "+strconv.Itoa(reply.status)), "contexts")
		return
	}
	rows, _ := reply.body["contexts"].([]any)
	contexts := make([]map[string]any, 0, len(rows))
	for _, raw := range rows {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		contexts = append(contexts, upstreamContextRow(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"count":     len(contexts),
		"contexts":  contexts,
	})
}

// upstreamContextRow reshapes one bridge /contexts row into the upstream
// list_active_contexts row (routes_workers.py:127-135).
func upstreamContextRow(entry map[string]any) map[string]any {
	tabCount := 0
	if tabs, ok := entry["tabs"].([]any); ok {
		tabCount = len(tabs)
	}
	row := map[string]any{
		"profile":       entry["profile"],
		"group_id":      entry["groupId"],
		"name":          entry["name"],
		"tab_count":     tabCount,
		"active_tab_id": entry["activeTab"],
	}
	if groupID, ok := pythonIntValue(entry["groupId"]); ok && groupID <= 0 {
		row["group_id"] = nil
	}
	return row
}

// handleBrowserPanelTabs is the upstream handle_browser_tabs shape: every tab
// the extension sees, enriched with the Hive-group verdict and counters.
func handleBrowserPanelTabs(w http.ResponseWriter, r *http.Request, deps BrowserPanelDeps) {
	base := deps.baseURL()
	reply, reason := getBridgeJSON(r, deps, base, "/tabs")
	if reason != "" {
		writeBrowserPanelUnavailable(w, reason, "tabs")
		return
	}
	if reply.status != http.StatusOK {
		writeBrowserPanelUnavailable(w, bridgeReason(reply, "browser bridge answered "+strconv.Itoa(reply.status)), "tabs")
		return
	}

	// Which groupIds carry the Hive marker? Same source as the contexts route:
	// the bridge's tab-group registry. A registry read failure only drops the
	// enrichment (isHiveGroup false), never the tab list.
	//
	// Decision (2026-11-22, user): keep the real enrichment instead of
	// mirroring upstream's key bug (routes_workers.py reads ctx["groupId"]
	// while lifecycle.py rows key "group_id", so upstream is always false).
	// Registered intentional improvement; review when upstream fixes the key
	// (then re-evaluate parity and possibly drop this hunk).
	hiveGroupIDs := map[int]struct{}{}
	if contextsReply, contextsReason := getBridgeJSON(r, deps, base, "/contexts"); contextsReason == "" && contextsReply.status == http.StatusOK {
		if rows, ok := contextsReply.body["contexts"].([]any); ok {
			for _, raw := range rows {
				entry, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if groupID, ok := pythonIntValue(entry["groupId"]); ok && groupID > 0 {
					hiveGroupIDs[groupID] = struct{}{}
				}
			}
		}
	}

	rows, _ := reply.body["tabs"].([]any)
	tabs := make([]map[string]any, 0, len(rows))
	liveCount := 0
	for _, raw := range rows {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		row := upstreamTabRow(entry, hiveGroupIDs)
		if !row["discarded"].(bool) {
			liveCount++
		}
		tabs = append(tabs, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available":        true,
		"count":            len(tabs),
		"live_count":       liveCount,
		"discarded_count":  len(tabs) - liveCount,
		"hive_group_count": len(hiveGroupIDs),
		"tabs":             tabs,
	})
}

// upstreamTabRow reshapes one bridge tab row into the upstream enriched row
// (routes_workers.py:352-368). Fields the extension does not send (windowId,
// discarded, active, audible, pinned) become null/false exactly like
// dict.get() upstream.
func upstreamTabRow(entry map[string]any, hiveGroupIDs map[int]struct{}) map[string]any {
	groupID, hasGroupID := pythonIntValue(entry["groupId"])
	var groupOut any
	isHiveGroup := false
	if hasGroupID {
		if groupID != -1 {
			groupOut = groupID
		}
		if _, ok := hiveGroupIDs[groupID]; ok {
			isHiveGroup = true
		}
	}
	return map[string]any{
		"id":          entry["id"],
		"windowId":    entry["windowId"],
		"groupId":     groupOut,
		"isHiveGroup": isHiveGroup,
		"url":         entry["url"],
		"title":       entry["title"],
		"discarded":   pyTruthy(entry["discarded"]),
		"active":      pyTruthy(entry["active"]),
		"audible":     pyTruthy(entry["audible"]),
		"pinned":      pyTruthy(entry["pinned"]),
	}
}

// handleBrowserPanelClose is the upstream handle_close_browser_context shape:
// {"closed": results} with the per-profile outcomes, and the bridge's 400
// {"error": "profile (or all=true) is required"} passed through unchanged.
func handleBrowserPanelClose(w http.ResponseWriter, r *http.Request, deps BrowserPanelDeps) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, browserPanelBodyLimit))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "could not read request body"})
		return
	}
	base := deps.baseURL()
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/contexts/close", bytes.NewReader(payload))
	if err != nil {
		writeBrowserPanelUnavailable(w, "browser bridge not reachable at "+base, "")
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := deps.client().Do(request)
	if err != nil {
		writeBrowserPanelUnavailable(w, "browser bridge not reachable at "+base, "")
		return
	}
	defer func() { _ = response.Body.Close() }()
	reply, err := io.ReadAll(io.LimitReader(response.Body, browserPanelReplyLimit))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": "browser bridge response could not be read",
			"code":  "bad_gateway",
		})
		return
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(reply)
}

// writeBrowserPanelUnavailable is the upstream unavailable envelope: 200,
// available:false, a reason, and the route's own collection as an empty array.
// An empty collection keeps the body self-describing (upstream always carries
// the collection key too).
func writeBrowserPanelUnavailable(w http.ResponseWriter, reason, collection string) {
	body := map[string]any{
		"available": false,
		"reason":    reason,
	}
	if collection != "" {
		body[collection] = []any{}
	}
	writeJSON(w, http.StatusOK, body)
}
