package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"gopkg.in/yaml.v3"
)

func queenToolTestMux(home hivehome.Home, manager *SessionManager, mutate func(*QueenToolRouteDeps)) *http.ServeMux {
	deps := QueenToolRouteDeps{Home: home, Manager: manager}
	if mutate != nil {
		mutate(&deps)
	}
	mux := http.NewServeMux()
	RegisterQueenToolRoutes(mux, deps)
	return mux
}

func queenToolTestProfile(t *testing.T, home hivehome.Home, queenID, name, title string) {
	t.Helper()
	queenTestWriteProfile(t, home, queenID, map[string]any{"name": name, "title": title})
}

func queenToolCatalog(entries map[string][]map[string]any) func(string, *Session) map[string][]map[string]any {
	return func(string, *Session) map[string][]map[string]any { return entries }
}

func queenToolNames(t *testing.T, value any) []string {
	t.Helper()
	names := queenTestStringList(t, value)
	sort.Strings(names)
	return names
}

func queenToolServer(t *testing.T, body map[string]any, server string) map[string]map[string]any {
	t.Helper()
	servers, ok := body["mcp_servers"].([]any)
	if !ok {
		t.Fatalf("mcp_servers = %#v", body["mcp_servers"])
	}
	for _, entry := range servers {
		object := entry.(map[string]any)
		if object["name"] != server {
			continue
		}
		tools, _ := object["tools"].([]any)
		out := map[string]map[string]any{}
		for _, tool := range tools {
			toolObject := tool.(map[string]any)
			out[toolObject["name"].(string)] = toolObject
		}
		return out
	}
	t.Fatalf("server %s missing from %#v", server, body["mcp_servers"])
	return nil
}

// Parity: framework/server/tests/test_queen_tools.py::test_get_tools_default_excludes_oauth_for_unknown_queen
func TestGetToolsDefaultExcludesOAuthForUnknownQueen(t *testing.T) {
	home := queenTestHome(t)
	const customID = "queen_custom_unknown"
	queenToolTestProfile(t, home, customID, "Custom", "Custom Role")
	catalog := map[string][]map[string]any{
		"files-tools": {
			{"name": "read_file", "description": "read", "input_schema": map[string]any{}},
			{"name": "write_file", "description": "write", "input_schema": map[string]any{}},
		},
		"hive_tools": {
			{"name": "gmail_list_messages", "description": "list", "input_schema": map[string]any{}, "provider": "google"},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) {
		deps.MCPCatalog = queenToolCatalog(catalog)
		deps.CredentialedTools = func() map[string]string { return map[string]string{"gmail_list_messages": "google"} }
	})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/"+customID+"/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	enabled := queenToolNames(t, body["enabled_mcp_tools"])
	for _, want := range []string{"read_file", "write_file"} {
		if !containsStringValue(enabled, want) {
			t.Fatalf("enabled = %#v, want %s", enabled, want)
		}
	}
	if containsStringValue(enabled, "gmail_list_messages") {
		t.Fatalf("OAuth tool leaked into the default: %#v", enabled)
	}
	if body["is_role_default"] != true || body["stale"] != false {
		t.Fatalf("flags = %#v/%#v", body["is_role_default"], body["stale"])
	}
	files := queenToolServer(t, body, "files-tools")
	if files["read_file"]["enabled"] != true {
		t.Fatalf("read_file = %#v", files["read_file"])
	}
	hive := queenToolServer(t, body, "hive_tools")
	if hive["gmail_list_messages"]["enabled"] != false {
		t.Fatalf("gmail enabled = %#v", hive["gmail_list_messages"])
	}
}

func containsStringValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Parity: framework/server/tests/test_queen_tools.py::test_get_tools_applies_role_default
func TestGetToolsAppliesRoleDefault(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"hive_tools": {
			{"name": "pdf_read", "description": "", "input_schema": map[string]any{}},
			{"name": "attach_file", "description": "", "input_schema": map[string]any{}},
		},
		"unrelated-server": {
			{"name": "fluffy_unknown_tool", "description": "", "input_schema": map[string]any{}},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) { deps.MCPCatalog = queenToolCatalog(catalog) })
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["is_role_default"] != true {
		t.Fatalf("is_role_default = %#v", body["is_role_default"])
	}
	enabled := queenToolNames(t, body["enabled_mcp_tools"])
	for _, want := range []string{"pdf_read", "attach_file"} {
		if !containsStringValue(enabled, want) {
			t.Fatalf("enabled = %#v, want %s", enabled, want)
		}
	}
	if containsStringValue(enabled, "fluffy_unknown_tool") {
		t.Fatalf("unrelated server leaked: %#v", enabled)
	}
}

// Parity: framework/server/tests/test_queen_tools.py::test_get_tools_exposes_categories
func TestGetToolsExposesCategories(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"hive_tools": {
			{"name": "pdf_read", "description": "", "input_schema": map[string]any{}},
			{"name": "attach_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) { deps.MCPCatalog = queenToolCatalog(catalog) })
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	categories := map[string]map[string]any{}
	for _, entry := range body["categories"].([]any) {
		object := entry.(map[string]any)
		categories[object["name"].(string)] = object
	}
	if categories["file_ops"]["in_role_default"] != true || categories["browser_basic"]["in_role_default"] != true {
		t.Fatalf("role-default categories = %#v", categories)
	}
	if _, ok := categories["spreadsheet_advanced"]; !ok {
		t.Fatalf("spreadsheet_advanced missing: %#v", categories)
	}
	if categories["spreadsheet_advanced"]["in_role_default"] != false {
		t.Fatalf("spreadsheet in role default: %#v", categories["spreadsheet_advanced"])
	}
	if _, ok := categories["security"]; ok {
		t.Fatalf("security category must not exist: %#v", categories)
	}
	members := categories["file_ops"]["tools"].([]any)
	names := []string{}
	for _, member := range members {
		names = append(names, member.(string))
	}
	for _, want := range []string{"pdf_read", "attach_file"} {
		if !containsStringValue(names, want) {
			t.Fatalf("file_ops = %#v, want %s", names, want)
		}
	}
}

// Parity: framework/server/tests/test_queen_tools.py::test_patch_persists_and_validates
func TestPatchQueenToolsPersistsAndValidates(t *testing.T) {
	home := queenTestHome(t)
	// Current release makes the GA sidecar heal a no-op, isolating the
	// persistence/validation contract (the heal is covered in
	// tools_ga_migration_test.go).
	t.Setenv("HIVE_APP_VERSION", "0.7.2")
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"files-tools": {
			{"name": "read_file", "description": "", "input_schema": map[string]any{}},
			{"name": "write_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) { deps.MCPCatalog = queenToolCatalog(catalog) })
	toolsPath := filepath.Join(home.QueenDir("queen_technology"), "tools.json")
	profilePath := filepath.Join(home.QueenDir("queen_technology"), "profile.yaml")

	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":["read_file"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if !reflect.DeepEqual(queenTestStringList(t, body["enabled_mcp_tools"]), []string{"read_file"}) {
		t.Fatalf("enabled = %#v", body["enabled_mcp_tools"])
	}
	sidecar := readQueenToolsSidecar(t, toolsPath)
	if !reflect.DeepEqual(queenTestStringList(t, sidecar["enabled_mcp_tools"]), []string{"read_file"}) {
		t.Fatalf("sidecar = %#v", sidecar)
	}
	if _, ok := sidecar["updated_at"]; !ok {
		t.Fatalf("sidecar has no updated_at: %#v", sidecar)
	}
	profile := readQueenProfileFile(t, profilePath)
	if _, ok := profile["enabled_mcp_tools"]; ok {
		t.Fatalf("profile was touched: %#v", profile)
	}

	_, listed := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if listed["is_role_default"] != false {
		t.Fatalf("is_role_default = %#v", listed["is_role_default"])
	}
	servers := queenToolServer(t, listed, "files-tools")
	if servers["read_file"]["enabled"] != true || servers["write_file"]["enabled"] != false {
		t.Fatalf("servers = %#v", servers)
	}

	_, reset := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":null}`)
	if reset["enabled_mcp_tools"] != nil {
		t.Fatalf("reset = %#v", reset["enabled_mcp_tools"])
	}
	sidecar = readQueenToolsSidecar(t, toolsPath)
	if sidecar["enabled_mcp_tools"] != nil {
		t.Fatalf("sidecar reset = %#v", sidecar)
	}

	badRecorder, badBody := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":["nope_not_a_tool"]}`)
	if badRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d", badRecorder.Code)
	}
	unknown, _ := badBody["unknown"].([]any)
	if len(unknown) != 1 || unknown[0] != "nope_not_a_tool" {
		t.Fatalf("unknown = %#v", badBody["unknown"])
	}
	if sidecar = readQueenToolsSidecar(t, toolsPath); sidecar["enabled_mcp_tools"] != nil {
		t.Fatalf("sidecar changed on rejection: %#v", sidecar)
	}
}

func readQueenToolsSidecar(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tools.json: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parse tools.json: %v", err)
	}
	return decoded
}

func readQueenProfileFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	var decoded map[string]any
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("parse profile: %v", err)
	}
	return decoded
}

// Parity: framework/server/tests/test_queen_tools.py::test_patch_hot_reloads_live_session
func TestPatchQueenToolsHotReloadsLiveSession(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"files-tools": {
			{"name": "read_file", "description": "", "input_schema": map[string]any{}},
			{"name": "write_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "sess-1", QueenName: "queen_technology", PhaseState: "state"})
	refreshed := []string{}
	mux := queenToolTestMux(home, manager, func(deps *QueenToolRouteDeps) {
		deps.MCPCatalog = queenToolCatalog(catalog)
		deps.RefreshSessionTools = func(session *Session, enabled any) bool {
			refreshed = append(refreshed, session.ID)
			return true
		}
	})
	recorder, body := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":["read_file"]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["refreshed_sessions"] != float64(1) {
		t.Fatalf("refreshed_sessions = %#v", body["refreshed_sessions"])
	}
	if !reflect.DeepEqual(refreshed, []string{"sess-1"}) {
		t.Fatalf("refreshed = %#v", refreshed)
	}
}

// Parity: framework/server/tests/test_queen_tools.py::test_missing_queen_returns_404
func TestQueenToolsMissingQueenReturns404(t *testing.T) {
	home := queenTestHome(t)
	mux := queenToolTestMux(home, NewSessionManager(), nil)
	if recorder, _ := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_nonexistent/tools", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("GET status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_nonexistent/tools", `{"enabled_mcp_tools":null}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("PATCH status = %d", recorder.Code)
	}
}

// Parity: framework/server/tests/test_queen_tools.py::test_delete_restores_role_default
func TestDeleteQueenToolsRestoresRoleDefault(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	catalog := map[string][]map[string]any{
		"hive_tools": {
			{"name": "pdf_read", "description": "", "input_schema": map[string]any{}},
			{"name": "attach_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) { deps.MCPCatalog = queenToolCatalog(catalog) })
	toolsPath := filepath.Join(home.QueenDir("queen_technology"), "tools.json")

	if recorder, _ := doSessionJSON(t, mux, http.MethodPatch, "/api/queen/queen_technology/tools", `{"enabled_mcp_tools":["pdf_read"]}`); recorder.Code != http.StatusOK {
		t.Fatalf("seed status = %d", recorder.Code)
	}
	if _, err := os.Stat(toolsPath); err != nil {
		t.Fatalf("seed sidecar missing: %v", err)
	}

	recorder, body := doSessionJSON(t, mux, http.MethodDelete, "/api/queen/queen_technology/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["removed"] != true || body["is_role_default"] != true {
		t.Fatalf("body = %#v", body)
	}
	if _, err := os.Stat(toolsPath); err == nil {
		t.Fatal("tools.json survived delete")
	}
	enabled := queenToolNames(t, body["enabled_mcp_tools"])
	for _, want := range []string{"pdf_read", "attach_file"} {
		if !containsStringValue(enabled, want) {
			t.Fatalf("enabled = %#v, want %s", enabled, want)
		}
	}
	for _, unwanted := range []string{"port_scan", "subdomain_enumerate"} {
		if containsStringValue(enabled, unwanted) {
			t.Fatalf("enabled = %#v, must omit %s", enabled, unwanted)
		}
	}

	_, after := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if after["is_role_default"] != true {
		t.Fatalf("after = %#v", after["is_role_default"])
	}
	_, again := doSessionJSON(t, mux, http.MethodDelete, "/api/queen/queen_technology/tools", "")
	if again["removed"] != false {
		t.Fatalf("second delete = %#v", again["removed"])
	}
}

// Parity: framework/server/tests/test_queen_tools.py::test_legacy_profile_field_migrates_to_sidecar
func TestLegacyProfileFieldMigratesToSidecar(t *testing.T) {
	home := queenTestHome(t)
	// Current release makes the GA sidecar heal a no-op: this test isolates
	// the profile.yaml -> tools.json hoist.
	t.Setenv("HIVE_APP_VERSION", "0.7.2")
	queenTestWriteProfile(t, home, "queen_technology", map[string]any{
		"name":              "Alexandra",
		"title":             "Head of Technology",
		"enabled_mcp_tools": []any{"read_file", "write_file"},
	})
	toolsPath := filepath.Join(home.QueenDir("queen_technology"), "tools.json")
	mux := queenToolTestMux(home, nil, nil)

	_, first := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if !reflect.DeepEqual(queenTestStringList(t, first["enabled_mcp_tools"]), []string{"read_file", "write_file"}) {
		t.Fatalf("enabled = %#v", first["enabled_mcp_tools"])
	}
	sidecar := readQueenToolsSidecar(t, toolsPath)
	if !reflect.DeepEqual(queenTestStringList(t, sidecar["enabled_mcp_tools"]), []string{"read_file", "write_file"}) {
		t.Fatalf("sidecar = %#v", sidecar)
	}
	profile := readQueenProfileFile(t, filepath.Join(home.QueenDir("queen_technology"), "profile.yaml"))
	if _, ok := profile["enabled_mcp_tools"]; ok {
		t.Fatalf("legacy field survived: %#v", profile)
	}
	if profile["name"] != "Alexandra" {
		t.Fatalf("profile = %#v", profile)
	}
	_, second := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if !reflect.DeepEqual(queenTestStringList(t, second["enabled_mcp_tools"]), []string{"read_file", "write_file"}) {
		t.Fatalf("second enabled = %#v", second["enabled_mcp_tools"])
	}
}

// A saved sidecar for a known queen is healed against the role default: tools
// added to a category after saved_on_version are granted again.
//
// Parity: framework/agents/queen/queen_tools_config.py::load_queen_tools_config
func TestGetToolsHealsSavedSidecarAdditions(t *testing.T) {
	home := queenTestHome(t)
	queenToolTestProfile(t, home, "queen_technology", "Alexandra", "Head of Technology")
	toolsPath := filepath.Join(home.QueenDir("queen_technology"), "tools.json")
	seed := []byte(`{"enabled_mcp_tools":["pdf_read"],"saved_on_version":"0.2.18"}`)
	if err := os.WriteFile(toolsPath, seed, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	mux := queenToolTestMux(home, nil, nil)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_technology/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["is_role_default"] != false {
		t.Fatalf("is_role_default = %#v, want false (sidecar exists)", body["is_role_default"])
	}
	want := append(queenTechnologyGrantAdditions(), "pdf_read")
	sort.Strings(want)
	if got := queenToolNames(t, body["enabled_mcp_tools"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled = %#v, want %#v", got, want)
	}
}

// A custom queen (no role default) is healed by the baseline-coverage
// heuristic: it grants additions only when the saved list covered the whole
// pre-addition category.
//
// Parity: framework/agents/queen/queen_tools_defaults.py::infer_category_additions
func TestGetToolsHealsCustomQueenByBaseline(t *testing.T) {
	home := queenTestHome(t)
	const customID = "queen_custom_heal"
	queenToolTestProfile(t, home, customID, "Custom", "Custom Role")
	toolsPath := filepath.Join(home.QueenDir(customID), "tools.json")
	seed := []byte(`{"enabled_mcp_tools":["pdf_read"]}`)
	if err := os.WriteFile(toolsPath, seed, 0o644); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	catalog := map[string][]map[string]any{
		"hive_tools": {
			{"name": "pdf_read", "description": "", "input_schema": map[string]any{}},
			{"name": "attach_file", "description": "", "input_schema": map[string]any{}},
		},
	}
	mux := queenToolTestMux(home, nil, func(deps *QueenToolRouteDeps) { deps.MCPCatalog = queenToolCatalog(catalog) })
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/queen/"+customID+"/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := queenToolNames(t, body["enabled_mcp_tools"]); !reflect.DeepEqual(got, []string{"attach_file", "pdf_read"}) {
		t.Fatalf("enabled = %#v, want attach_file + pdf_read", got)
	}
}
