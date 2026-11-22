package server

import (
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// fakeMCPAttacher is an in-memory tools.MCPAttacher: the routes only need the
// status snapshot and the reload call, not a live server.
type fakeMCPAttacher struct {
	mu        sync.Mutex
	reloads   int
	reloadErr error
	gen       int64
	statuses  []tools.MCPServerStatus
}

func (f *fakeMCPAttacher) Attach(*tools.ToolRegistry) []tools.ServerResolution { return nil }

func (f *fakeMCPAttacher) Reload() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
	if f.reloadErr != nil {
		return f.reloadErr
	}
	f.gen++
	return nil
}

func (f *fakeMCPAttacher) Status() []tools.MCPServerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]tools.MCPServerStatus(nil), f.statuses...)
}

func (f *fakeMCPAttacher) Generation() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

func installFakeAttacher(t *testing.T, attacher *fakeMCPAttacher) {
	t.Helper()
	tools.SetProcessMCP(attacher)
	t.Cleanup(func() { tools.SetProcessMCP(nil) })
}

func installedDemoRow() map[string]any {
	return map[string]any{
		"name":                 "demo",
		"source":               "local",
		"transport":            "http",
		"mode":                 "tools",
		"enabled":              true,
		"manifest":             map[string]any{"description": "Demo server"},
		"last_health_status":   nil,
		"last_error":           nil,
		"last_health_check_at": nil,
	}
}

// Parity-exempt: the serve loader's live connection state has no upstream route
// test — the Python route only reads installed registry state.
func TestMCPListServersShowsConnectionStatus(t *testing.T) {
	attacher := &fakeMCPAttacher{
		gen: 1,
		statuses: []tools.MCPServerStatus{
			{Name: "demo", Transport: "http", Enabled: true, Connected: true, Tools: 2, ToolNames: []string{"echo", "add"}},
			{Name: "pinned", Transport: "stdio", Enabled: true, Connected: true, Tools: 1, ToolNames: []string{"run"}},
		},
	}
	installFakeAttacher(t, attacher)

	registry := newFakeMCPRegistry()
	registry.servers["demo"] = installedDemoRow()
	mux := mcpTestMux(t, registry)

	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	servers, _ := body["servers"].([]any)
	rows := map[string]map[string]any{}
	for _, item := range servers {
		row, _ := item.(map[string]any)
		name, _ := row["name"].(string)
		rows[name] = row
	}

	demo := rows["demo"]
	if demo["connected"] != true || demo["tool_count"] != float64(2) {
		t.Fatalf("demo row = %#v", demo)
	}
	names, _ := demo["tool_names"].([]any)
	if len(names) != 2 {
		t.Fatalf("demo tool_names = %#v", demo["tool_names"])
	}
	// Only in configuration.json: appended from the loader's snapshot.
	pinned := rows["pinned"]
	if pinned == nil || pinned["connected"] != true || pinned["tool_count"] != float64(1) {
		t.Fatalf("pinned row = %#v", pinned)
	}
	if names, _ := pinned["tool_names"].([]any); len(names) != 1 || names[0] != "run" {
		t.Fatalf("pinned tool_names = %#v", pinned["tool_names"])
	}
	// Harness groups are in-process and must not gain connection fields.
	harness := rows["terminal-tools"]
	if harness["tool_count"] != nil || harness["connected"] != nil {
		t.Fatalf("harness row = %#v", harness)
	}
}

// Parity-exempt: reporting a failed server through the listing is the Go
// loader's degradation path; upstream has no equivalent field.
func TestMCPListServersShowsUnreachableServer(t *testing.T) {
	attacher := &fakeMCPAttacher{
		gen: 1,
		statuses: []tools.MCPServerStatus{
			{Name: "demo", Transport: "http", Enabled: true, Error: "dial tcp 127.0.0.1:1: connect: connection refused"},
		},
	}
	installFakeAttacher(t, attacher)

	registry := newFakeMCPRegistry()
	registry.servers["demo"] = installedDemoRow()
	mux := mcpTestMux(t, registry)

	_, body := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	servers, _ := body["servers"].([]any)
	var demo map[string]any
	for _, item := range servers {
		row, _ := item.(map[string]any)
		if row["name"] == "demo" {
			demo = row
		}
	}
	if demo == nil {
		t.Fatal("demo row missing")
	}
	if demo["connected"] != false {
		t.Fatalf("demo connected = %#v", demo["connected"])
	}
	if message, _ := demo["last_error"].(string); message == "" {
		t.Fatalf("demo last_error = %#v", demo["last_error"])
	}
}

// Parity-exempt: a manual reload route exists only in the Go port.
func TestMCPReloadRouteReloadsLoader(t *testing.T) {
	attacher := &fakeMCPAttacher{
		gen: 1,
		statuses: []tools.MCPServerStatus{
			{Name: "demo", Transport: "http", Enabled: true, Connected: true, Tools: 3, ToolNames: []string{"a", "b", "c"}},
		},
	}
	installFakeAttacher(t, attacher)
	mux := mcpTestMux(t, newFakeMCPRegistry())

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/reload", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["reloaded"] != true {
		t.Fatalf("body = %#v", body)
	}
	if attacher.reloads != 1 {
		t.Fatalf("reloads = %d", attacher.reloads)
	}
	rows, _ := body["servers"].([]any)
	first, _ := rows[0].(map[string]any)
	if first["name"] != "demo" || first["tool_count"] != float64(3) || first["connected"] != true {
		t.Fatalf("row = %#v", first)
	}
}

// Parity-exempt: reload error surfacing is Go-only.
func TestMCPReloadRouteReportsError(t *testing.T) {
	attacher := &fakeMCPAttacher{reloadErr: errors.New("config unreadable")}
	installFakeAttacher(t, attacher)
	mux := mcpTestMux(t, newFakeMCPRegistry())

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/reload", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["error"] != "config unreadable" || body["reloaded"] != false {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: no loader installed means MCP was never configured.
func TestMCPReloadRouteWithoutLoader(t *testing.T) {
	tools.SetProcessMCP(nil)
	mux := mcpTestMux(t, newFakeMCPRegistry())

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/reload", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["reloaded"] != false {
		t.Fatalf("body = %#v", body)
	}
}
