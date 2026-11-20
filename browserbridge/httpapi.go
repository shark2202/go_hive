package browserbridge

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// NewHTTPHandler serves the local agent API: /health, /status, /profiles and
// /command. It refuses non-loopback clients even if the listener is
// misconfigured to bind a public address.
func NewHTTPHandler(b *Bridge) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		b.mu.RLock()
		n := len(b.conns)
		b.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":          true,
			"status":      "ok",
			"connected":   n > 0,
			"connections": n,
			"uptime_ms":   time.Since(b.started).Milliseconds(),
		})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, b.Status(r.Context()))
	})
	mux.HandleFunc("/profiles", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "profiles": b.Profiles()})
	})
	mux.HandleFunc("/command", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		handleCommandHTTP(b, w, r)
	})
	mux.HandleFunc("GET /contexts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "contexts": b.ListContexts(r.Context())})
	})
	mux.HandleFunc("GET /tabs", func(w http.ResponseWriter, r *http.Request) {
		handlePanelTabsHTTP(b, w, r)
	})
	mux.HandleFunc("POST /contexts/close", func(w http.ResponseWriter, r *http.Request) {
		handlePanelCloseContextsHTTP(b, w, r)
	})
	mux.HandleFunc("GET /tabs/{id}/health", func(w http.ResponseWriter, r *http.Request) {
		tabID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || tabID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found", "blockers": []Blocker{}})
			return
		}
		force := false
		switch r.URL.Query().Get("force") {
		case "1", "true", "yes":
			force = true
		}
		writeJSON(w, http.StatusOK, b.TabHealth(r.Context(), tabID, force))
	})
	mux.HandleFunc("GET /tabs/{id}/actions", func(w http.ResponseWriter, r *http.Request) {
		tabID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || tabID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found", "actions": []TabAction{}, "last_action_ts_ms": nil})
			return
		}
		limit := 8
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil {
				limit = n
			}
		}
		var since *int64
		if raw := r.URL.Query().Get("since"); raw != "" {
			if n, err := strconv.ParseFloat(raw, 64); err == nil {
				v := int64(n)
				since = &v
			}
		}
		payload := b.TabActions(tabID, limit, since)
		payload["ok"] = true
		writeJSON(w, http.StatusOK, payload)
	})
	mux.HandleFunc("POST /tabs/{id}/release", func(w http.ResponseWriter, r *http.Request) {
		tabID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || tabID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
			return
		}
		result, err := b.ReleaseTab(r.Context(), tabID, true)
		if err != nil {
			code := statusForCode(AsError(err).Code)
			writeJSON(w, code, map[string]any{"ok": false, "error": AsError(err).Message, "code": AsError(err).Code})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /tabs/{id}/reveal", func(w http.ResponseWriter, r *http.Request) {
		// User-initiated jump-to-tab: activate + raise the Chrome window.
		// Distinct from the agent-facing tab.activate so automated actions
		// never steal the user's window focus.
		tabID, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || tabID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "not found"})
			return
		}
		result, err := b.RevealTab(r.Context(), tabID)
		if err != nil {
			e := AsError(err)
			writeJSON(w, statusForCode(e.Code), map[string]any{"ok": false, "error": e.Message, "code": e.Code})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		// Manual teardown, driven from the side panel's "stop orphaned
		// bridge" button. Upstream SIGTERMs its supervisor so the graceful
		// path runs; the supervised Go binary wires ShutdownRequest to a
		// graceful exit with the same effect (the supervisor reads exit
		// code 0 as intentional and does not respawn).
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "message": "Bridge shutdown initiated."})
		if b.cfg.ShutdownRequest != nil {
			b.cfg.Logf("shutdown requested via POST /shutdown")
			go b.cfg.ShutdownRequest()
		}
	})
	mux.HandleFunc("POST /contexts/{profile}/adopt-tab", func(w http.ResponseWriter, r *http.Request) {
		profile := r.PathValue("profile")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "could not read request body"})
			return
		}
		var req struct {
			TabID int `json:"tabId"`
		}
		if json.Unmarshal(body, &req) != nil || req.TabID <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": `Body must be {"tabId": int}`})
			return
		}
		result, err := b.AdoptTab(r.Context(), profile, req.TabID, true)
		if err != nil {
			e := AsError(err)
			code := statusForCode(e.Code)
			if e.Code == CodeConflict {
				code = http.StatusConflict
			}
			writeJSON(w, code, map[string]any{"ok": false, "error": e.Message, "code": e.Code})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})
	mux.HandleFunc("POST /profiles/default", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "could not read request body"})
			return
		}
		var req struct {
			Label       string `json:"label"`
			ExtensionID string `json:"extensionId"`
		}
		if json.Unmarshal(body, &req) != nil || strings.TrimSpace(req.Label) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": `Body must be {"label": str}`})
			return
		}
		b.SetStarredDefault(req.Label, req.ExtensionID)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	return localOnly(cors(mux))
}

func handleCommandHTTP(b *Bridge, w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, badRequest("could not read request body: %v", err))
		return
	}
	var req struct {
		Profile string         `json:"profile"`
		Type    string         `json:"type"`
		Params  map[string]any `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, badRequest("invalid JSON body: %v", err))
		return
	}
	if req.Params == nil {
		req.Params = map[string]any{}
	}
	result, err := b.Execute(r.Context(), req.Profile, req.Type, req.Params)
	if err != nil {
		writeError(w, AsError(err))
		return
	}
	if result == nil {
		result = map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, e *Error) {
	writeJSON(w, statusForCode(e.Code), map[string]any{
		"ok":      false,
		"code":    e.Code,
		"message": e.Message,
		"error":   e,
	})
}

func statusForCode(code string) int {
	switch code {
	case CodeBadRequest, CodeUnknownCommand:
		return http.StatusBadRequest
	case CodeNotConnected:
		return http.StatusServiceUnavailable
	case CodeAmbiguousProfile:
		return http.StatusConflict
	case CodeTimeout:
		return http.StatusGatewayTimeout
	case CodeConnectionLost, CodeExtensionError, CodeCDPError:
		return http.StatusBadGateway
	case CodeNotFound:
		return http.StatusNotFound
	case CodeInvalidState:
		return http.StatusConflict
	case CodeConflict:
		return http.StatusConflict
	case CodeRateLimited:
		return http.StatusTooManyRequests
	case CodeNoBrowserProfile, CodeNoSuchProfile, CodeUnsupportedExt, CodeUngroupNoOp:
		return http.StatusBadRequest
	case CodeCreateFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, newError(CodeBadRequest, "method not allowed; use "+allow, false))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /command is a powerful local control plane. Do not grant browser
		// origins CORS access to it: only local processes (curl, agent
		// runtime) should invoke commands. Read-only endpoints stay readable
		// by the extension side panel.
		if r.URL.Path != "/command" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// localOnly rejects non-loopback peers. The bridge is an agent control plane,
// not a network service.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			writeError(w, newError(CodeBadRequest, "unrecognised remote address", false))
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			writeError(w, newError(CodeBadRequest, "the browser bridge API is local-only", false))
			return
		}
		next.ServeHTTP(w, r)
	})
}
