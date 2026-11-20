package browserbridge

// Panel HTTP surface: the two read routes and the close route the desktop's
// browser panel proxies need. Upstream keeps this state in the same process its
// server route runs in; here the registry is the bridge, so the routes are part
// of the bridge's local agent API (additive to /contexts, /status, /command).
//
// Parity: framework/server/routes_workers.py::handle_browser_contexts
// Parity: framework/server/routes_workers.py::handle_browser_tabs
// Parity: framework/server/routes_workers.py::handle_close_browser_context

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// handlePanelTabsHTTP serves GET /tabs: every tab the connected extension can
// see, unfiltered by group. The upstream server route calls bridge.list_tabs()
// with no group_id and enriches isHiveGroup itself; the bridge returns the
// extension's rows so the caller sees real data (or a real not_connected 503).
func handlePanelTabsHTTP(b *Bridge, w http.ResponseWriter, r *http.Request) {
	result, err := cmdTabsList(r.Context(), b, "", map[string]any{})
	if err != nil {
		e := AsError(err)
		writeJSON(w, statusForCode(e.Code), map[string]any{
			"ok":    false,
			"error": e.Message,
			"code":  e.Code,
			"tabs":  []any{},
		})
		return
	}
	payload, ok := result.(map[string]any)
	if !ok {
		payload = map[string]any{}
	}
	payload["ok"] = true
	if _, present := payload["tabs"]; !present {
		payload["tabs"] = []any{}
	}
	writeJSON(w, http.StatusOK, payload)
}

// handlePanelCloseContextsHTTP serves POST /contexts/close with the upstream
// body contract: {"profile": "<profile>"} closes one context, {"all": true}
// closes every registered one. Each result reports its own outcome so one
// failed teardown does not hide the others.
func handlePanelCloseContextsHTTP(b *Bridge, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "could not read request body"})
		return
	}
	var req struct {
		Profile string `json:"profile"`
		All     bool   `json:"all"`
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
			return
		}
	}

	profiles := make([]string, 0, 1)
	if req.All {
		for _, meta := range b.contexts.List() {
			if strings.TrimSpace(meta.Profile) != "" {
				profiles = append(profiles, meta.Profile)
			}
		}
	} else {
		profile := strings.TrimSpace(req.Profile)
		if profile == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "profile (or all=true) is required"})
			return
		}
		profiles = append(profiles, profile)
	}

	closed := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		result, err := b.CloseProfileContext(r.Context(), profile, "")
		if err != nil {
			e := AsError(err)
			closed = append(closed, map[string]any{
				"profile": profile,
				"ok":      false,
				"error":   e.Message,
				"code":    e.Code,
			})
			continue
		}
		entry := map[string]any{"profile": profile}
		for key, value := range result {
			entry[key] = value
		}
		if _, present := entry["ok"]; !present {
			entry["ok"] = true
		}
		closed = append(closed, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"closed": closed})
}
