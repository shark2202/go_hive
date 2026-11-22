package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/mcpregistry"
)

type fakeMCPRegistry struct {
	initErr      error
	servers      map[string]map[string]any
	listErr      error
	addErr       error
	addedName    string
	addedOpts    mcpregistry.AddLocalOptions
	getErr       error
	removeErr    error
	enableErr    error
	disableErr   error
	setModeErr   error
	healthErr    error
	healthResult map[string]any
	modeCalls    []string
	enabledCalls []string
	removedCalls []string
}

func newFakeMCPRegistry() *fakeMCPRegistry {
	return &fakeMCPRegistry{
		servers:      map[string]map[string]any{},
		healthResult: map[string]any{"name": "comfy", "status": "healthy", "tools": 2, "error": nil},
	}
}

func (f *fakeMCPRegistry) Initialize() error { return f.initErr }

func (f *fakeMCPRegistry) ListInstalled() ([]map[string]any, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	names := make([]string, 0, len(f.servers))
	for name := range f.servers {
		names = append(names, name)
	}
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rows = append(rows, f.servers[name])
	}
	return rows, nil
}

func (f *fakeMCPRegistry) AddLocal(name string, opts mcpregistry.AddLocalOptions) error {
	if f.addErr != nil {
		return f.addErr
	}
	f.addedName = name
	f.addedOpts = opts
	f.servers[name] = map[string]any{
		"name":      name,
		"source":    "local",
		"transport": opts.Transport,
		"mode":      opts.Mode,
		"enabled":   true,
		"manifest": map[string]any{
			"name":        name,
			"description": opts.Description,
			"transport":   map[string]any{"supported": []any{opts.Transport}, "default": opts.Transport},
			"stdio": map[string]any{
				"command": opts.Command,
				"args":    opts.Args,
				"env":     opts.Env,
				"cwd":     nil,
			},
		},
	}
	return nil
}

func (f *fakeMCPRegistry) GetServer(name string) (map[string]any, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.servers[name], nil
}

func (f *fakeMCPRegistry) Remove(name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removedCalls = append(f.removedCalls, name)
	delete(f.servers, name)
	return nil
}

func (f *fakeMCPRegistry) Enable(name string) error {
	if f.enableErr != nil {
		return f.enableErr
	}
	f.enabledCalls = append(f.enabledCalls, "enable:"+name)
	return nil
}

func (f *fakeMCPRegistry) Disable(name string) error {
	if f.disableErr != nil {
		return f.disableErr
	}
	f.enabledCalls = append(f.enabledCalls, "disable:"+name)
	return nil
}

func (f *fakeMCPRegistry) SetMode(name, mode string) error {
	if f.setModeErr != nil {
		return f.setModeErr
	}
	f.modeCalls = append(f.modeCalls, name+":"+mode)
	if entry, ok := f.servers[name]; ok {
		entry["mode"] = mode
	}
	return nil
}

func (f *fakeMCPRegistry) HealthCheck(name string) (map[string]any, error) {
	if f.healthErr != nil {
		return nil, f.healthErr
	}
	return f.healthResult, nil
}

func mcpTestMux(t *testing.T, registry MCPRegistryAPI) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterMCPRoutes(mux, MCPRouteDeps{
		Home:     hivehome.Home{Root: t.TempDir()},
		Registry: func(hivehome.Home) MCPRegistryAPI { return registry },
	})
	return mux
}

func mcpNames(servers []any) []string {
	names := make([]string, 0, len(servers))
	for _, item := range servers {
		row, _ := item.(map[string]any)
		name, _ := row["name"].(string)
		names = append(names, name)
	}
	return names
}

// Parity-exempt: upstream only asserts the merged list through test_mcp_cli.py's end-to-end route test; built-in rows are Go-visible UI data.
func TestMCPListServersMergesBuiltins(t *testing.T) {
	registry := newFakeMCPRegistry()
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	servers, _ := body["servers"].([]any)
	names := mcpNames(servers)
	want := []string{"terminal-tools", "files-tools", "chart-tools", "memory-tools", "hive_tools"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("names = %v", names)
	}
	first, _ := servers[0].(map[string]any)
	if first["source"] != "built-in" || first["transport"] != "harness" || first["removable"] != false {
		t.Fatalf("first = %#v", first)
	}
	if first["tool_count"] != nil || first["last_health_status"] != nil {
		t.Fatalf("first = %#v", first)
	}
}

// Parity-exempt: list/merge ordering for installed rows has no dedicated Python route test.
func TestMCPListServersIncludesInstalled(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.servers["comfy"] = map[string]any{
		"name":                 "comfy",
		"source":               "local",
		"transport":            "stdio",
		"mode":                 "cli",
		"enabled":              true,
		"manifest":             map[string]any{"description": "Comfy UI", "tools": []any{map[string]any{"name": "a"}}},
		"last_health_status":   "healthy",
		"last_error":           nil,
		"last_health_check_at": "2026-01-01T00:00:00Z",
	}
	mux := mcpTestMux(t, registry)
	_, body := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	servers, _ := body["servers"].([]any)
	names := mcpNames(servers)
	if names[len(names)-1] != "comfy" {
		t.Fatalf("names = %v", names)
	}
	row, _ := servers[len(servers)-1].(map[string]any)
	if row["mode"] != "cli" || row["tool_count"] != float64(1) || row["description"] != "Comfy UI" {
		t.Fatalf("row = %#v", row)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_the_routes_add_a_server_in_cli_mode_and_switch_it
func TestMCPAddServerRegistersAndSwitchesMode(t *testing.T) {
	registry := newFakeMCPRegistry()
	mux := mcpTestMux(t, registry)
	recorder, added := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers",
		`{"name":"comfy","transport":"stdio","command":"uvx","args":["comfy-mcp"],"mode":"cli"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	server, _ := added["server"].(map[string]any)
	if server["mode"] != "cli" {
		t.Fatalf("server = %#v", server)
	}
	if added["hint"] != "Start a new queen session to use this server's tools." {
		t.Fatalf("hint = %#v", added["hint"])
	}
	if registry.addedName != "comfy" || registry.addedOpts.Command != "uvx" || registry.addedOpts.Mode != "cli" {
		t.Fatalf("added = %#v %#v", registry.addedName, registry.addedOpts)
	}
	if len(registry.addedOpts.Args) != 1 || registry.addedOpts.Args[0] != "comfy-mcp" {
		t.Fatalf("args = %#v", registry.addedOpts.Args)
	}

	recorder, switched := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/comfy/mode", `{"mode":"tools"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("switch status = %d", recorder.Code)
	}
	if switched["mode"] != "tools" || switched["hint"] != "Start a new queen session to apply it." {
		t.Fatalf("switched = %#v", switched)
	}

	_, listed := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	servers, _ := listed["servers"].([]any)
	found := ""
	for _, item := range servers {
		row, _ := item.(map[string]any)
		if row["name"] == "comfy" {
			found, _ = row["mode"].(string)
		}
	}
	if found != "tools" {
		t.Fatalf("listed mode = %q", found)
	}
}

// Parity: core/tests/test_mcp_cli.py::test_the_routes_add_a_server_in_cli_mode_and_switch_it
func TestMCPRoutesWithRealRegistry(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := http.NewServeMux()
	RegisterMCPRoutes(mux, MCPRouteDeps{Home: home})

	recorder, added := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers",
		`{"name":"echo","transport":"stdio","command":"echo","args":["hi"],"mode":"cli","description":"Echo"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("add status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	server, _ := added["server"].(map[string]any)
	if server["mode"] != "cli" || server["description"] != "Echo" || server["source"] != "local" {
		t.Fatalf("server = %#v", server)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/echo/mode", `{"mode":"tools"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mode status = %d", recorder.Code)
	}
	_, listed := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	servers, _ := listed["servers"].([]any)
	found := ""
	for _, item := range servers {
		row, _ := item.(map[string]any)
		if row["name"] == "echo" {
			found, _ = row["mode"].(string)
		}
	}
	if found != "tools" {
		t.Fatalf("mode after switch = %q", found)
	}

	recorder, _ = doSessionJSON(t, mux, http.MethodDelete, "/api/mcp/servers/echo", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity-exempt: invalid-JSON 400 branch has no Python route test.
func TestMCPAddServerRejectsInvalidJSON(t *testing.T) {
	mux := mcpTestMux(t, newFakeMCPRegistry())
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers", `{`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: per-field validation branches are not exercised by the upstream route test.
func TestMCPAddServerValidatesBody(t *testing.T) {
	cases := []string{
		`{}`,
		`{"name":"  ","transport":"stdio"}`,
		`{"name":"x","transport":"carrier-pigeon"}`,
		`{"name":"x","transport":"stdio","mode":"turbo"}`,
	}
	for _, body := range cases {
		mux := mcpTestMux(t, newFakeMCPRegistry())
		recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers", body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d", body, recorder.Code)
		}
	}
}

// Parity-exempt: conflict mapping (409) is a Go route detail; Python raises MCPError with the same message.
func TestMCPAddServerConflictReturns409(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.addErr = &mcpregistry.Error{
		Code: mcpregistry.CodeInstallFailed,
		What: "Server 'comfy' already exists",
		Why:  "A server with this name is already registered locally.",
		Fix:  "Run: hive mcp remove comfy  — then add it again.",
	}
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers",
		`{"name":"comfy","transport":"stdio","command":"uvx"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["what"] != "Server 'comfy' already exists" || body["fix"] == nil {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: MCPError serialization shape is asserted only through the conflict case upstream.
func TestMCPAddServerErrorShape(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.addErr = &mcpregistry.Error{Code: mcpregistry.CodeInstallFailed, What: "Cannot register", Why: "command is required", Fix: "Pass --command"}
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers",
		`{"name":"x","transport":"stdio"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["error"] != "Cannot register" || body["code"] != mcpregistry.CodeInstallFailed || body["why"] != "command is required" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: remove/enable/disable/health error and success paths have no Python route tests.
func TestMCPRemoveServerNotFound(t *testing.T) {
	mux := mcpTestMux(t, newFakeMCPRegistry())
	recorder, _ := doSessionJSON(t, mux, http.MethodDelete, "/api/mcp/servers/ghost", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: built-in removal guard has no Python route test.
func TestMCPRemoveBuiltinRejected(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.servers["hive_tools"] = map[string]any{"name": "hive_tools", "source": "built-in", "transport": "harness"}
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/mcp/servers/hive_tools", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "cannot be removed") {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: successful local removal has no Python route test.
func TestMCPRemoveLocalServer(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.servers["comfy"] = map[string]any{"name": "comfy", "source": "local", "transport": "stdio"}
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/mcp/servers/comfy", "")
	if recorder.Code != http.StatusOK || body["removed"] != "comfy" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if len(registry.removedCalls) != 1 {
		t.Fatalf("removed = %#v", registry.removedCalls)
	}
}

// Parity-exempt: enable/disable have no Python route test beyond mode switching.
func TestMCPEnableDisableServer(t *testing.T) {
	registry := newFakeMCPRegistry()
	mux := mcpTestMux(t, registry)

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/comfy/enable", "")
	if recorder.Code != http.StatusOK || body["enabled"] != true {
		t.Fatalf("enable status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/comfy/disable", "")
	if recorder.Code != http.StatusOK || body["enabled"] != false {
		t.Fatalf("disable status = %d, body = %#v", recorder.Code, body)
	}
	if strings.Join(registry.enabledCalls, ",") != "enable:comfy,disable:comfy" {
		t.Fatalf("calls = %#v", registry.enabledCalls)
	}
}

// Parity-exempt: unknown-server mapping for enable/disable uses the shared MCPError path.
func TestMCPEnableServerMissingReturns404(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.enableErr = &mcpregistry.Error{Code: mcpregistry.CodeInstallFailed, What: "Cannot enable server 'ghost'", Why: "Server is not installed.", Fix: "Run: hive mcp list"}
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/ghost/enable", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["what"] != "Cannot enable server 'ghost'" {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: mode validation has no dedicated Python route test.
func TestMCPSetModeRejectsInvalid(t *testing.T) {
	mux := mcpTestMux(t, newFakeMCPRegistry())
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/comfy/mode", `{"mode":"turbo"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: health passthrough has no Python route test.
func TestMCPHealthPassthrough(t *testing.T) {
	registry := newFakeMCPRegistry()
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/comfy/health", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["status"] != "healthy" || body["tools"] != float64(2) {
		t.Fatalf("body = %#v", body)
	}
}

// Parity-exempt: health error mapping has no Python route test.
func TestMCPHealthMissingReturns404(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.healthErr = &mcpregistry.Error{Code: mcpregistry.CodeHealthFailed, What: "Cannot health-check server 'ghost'", Why: "Server is not installed.", Fix: "Run: hive mcp list"}
	mux := mcpTestMux(t, registry)
	recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/mcp/servers/ghost/health", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

// Parity-exempt: registry bootstrap failures are a Go seam path with no Python route test.
func TestMCPRegistryInitFailure(t *testing.T) {
	registry := newFakeMCPRegistry()
	registry.initErr = errors.New("boom")
	mux := mcpTestMux(t, registry)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/mcp/servers", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["error"] != "boom" {
		t.Fatalf("body = %#v", body)
	}
}
