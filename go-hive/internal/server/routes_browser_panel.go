package server

// Parity: framework/server/routes_workers.py::handle_browser_contexts
// Parity: framework/server/routes_workers.py::handle_browser_tabs
// Parity: framework/server/routes_workers.py::handle_close_browser_context
//
// The desktop's browser panel reads three process-global routes upstream:
// GET /api/browser/contexts (tab-group registry), GET /api/browser/tabs (every
// tab the extension sees) and POST /api/browser/contexts/close. In this port
// that state lives in the separate Browser Bridge process, so the routes are
// thin proxies to the bridge's local agent API: real response body, real status
// code, no synthesised rows. With no bridge reachable they answer 503
// `not_connected` with the expected collection empty, so a caller can never
// mistake "bridge down" for "registry empty".

import (
	"bytes"
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

// BrowserPanelDeps carries the seams the three panel proxies need. Both are
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

type browserPanelRoute struct {
	method     string
	path       string
	collection string
}

func RegisterBrowserPanelRoutes(mux *http.ServeMux, deps BrowserPanelDeps) {
	mux.HandleFunc("GET /api/browser/contexts", func(w http.ResponseWriter, r *http.Request) {
		proxyBrowserPanel(w, r, deps, browserPanelRoute{method: http.MethodGet, path: "/contexts", collection: "contexts"})
	})
	mux.HandleFunc("GET /api/browser/tabs", func(w http.ResponseWriter, r *http.Request) {
		proxyBrowserPanel(w, r, deps, browserPanelRoute{method: http.MethodGet, path: "/tabs", collection: "tabs"})
	})
	mux.HandleFunc("POST /api/browser/contexts/close", func(w http.ResponseWriter, r *http.Request) {
		proxyBrowserPanel(w, r, deps, browserPanelRoute{method: http.MethodPost, path: "/contexts/close", collection: "closed"})
	})
}

// proxyBrowserPanel forwards one panel request to the bridge and copies its
// status code and body through verbatim. A body read failure on the way in or
// out is reported as 502: the bridge answered, but the exchange could not be
// completed.
func proxyBrowserPanel(w http.ResponseWriter, r *http.Request, deps BrowserPanelDeps, route browserPanelRoute) {
	var payload []byte
	if route.method != http.MethodGet {
		var err error
		payload, err = io.ReadAll(io.LimitReader(r.Body, browserPanelBodyLimit))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "could not read request body"})
			return
		}
	}
	base := deps.baseURL()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(r.Context(), route.method, base+route.path, body)
	if err != nil {
		writeBrowserPanelUnavailable(w, base, route)
		return
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := deps.client().Do(request)
	if err != nil {
		writeBrowserPanelUnavailable(w, base, route)
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

// writeBrowserPanelUnavailable is the explicit no-bridge contract: 503,
// `not_connected`, and the route's own collection as an empty array.
func writeBrowserPanelUnavailable(w http.ResponseWriter, base string, route browserPanelRoute) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"available":      false,
		"error":          "not_connected",
		"reason":         "browser bridge not reachable at " + base,
		route.collection: []any{},
	})
}
