package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

func colonyToolsDeps(t *testing.T) (ColonyToolsDeps, hivehome.Home) {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return ColonyToolsDeps{Home: home}, home
}

func seedToolColony(t *testing.T, home hivehome.Home, name string, metadata map[string]any) {
	t.Helper()
	dir := filepath.Join(home.ColoniesDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	data, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

func catalogWith(names ...string) map[string][]map[string]any {
	entries := make([]map[string]any, 0, len(names))
	for _, name := range names {
		entries = append(entries, map[string]any{
			"name":         name,
			"description":  "desc of " + name,
			"input_schema": map[string]any{"type": "object"},
		})
	}
	return map[string][]map[string]any{"files-tools": entries}
}

func colonyToolsMux(deps ColonyToolsDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterColonyToolRoutes(mux, deps)
	return mux
}

// Parity-exempt: Go-only path guard; colony tools paths must stay under
// HIVE_HOME/colonies even when metadata exists outside it.
func TestColonyToolsRejectsTraversalColonyID(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	outside := filepath.Join(home.Root, "escape")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "metadata.json"), []byte(`{"colony_id":"escape"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if deps.colonyExists("../escape") {
		t.Fatal("colonyExists accepted a traversal colony id")
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_get_tools_default_allow
func TestGetToolsDefaultAllow(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	seedToolColony(t, home, "my_colony", map[string]any{"colony_id": "my_colony", "queen_name": "queen_technology"})
	deps.MCPCatalog = func() map[string][]map[string]any { return catalogWith("read_file", "write_file") }
	recorder, body := serveColonyRequest(colonyToolsMux(deps), httptest.NewRequest(http.MethodGet, "/api/colony/my_colony/tools", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["enabled_mcp_tools"] != nil {
		t.Fatalf("enabled_mcp_tools = %v, want null", body["enabled_mcp_tools"])
	}
	if body["stale"] != false {
		t.Fatalf("stale = %v, want false", body["stale"])
	}
	servers, _ := body["mcp_servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("mcp_servers = %v", body["mcp_servers"])
	}
	tools, _ := servers[0].(map[string]any)["tools"].([]any)
	for _, tool := range tools {
		if tool.(map[string]any)["enabled"] != true {
			t.Fatalf("tool not enabled: %v", tool)
		}
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_patch_persists_and_validates
func TestPatchPersistsAndValidates(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	seedToolColony(t, home, "my_colony", map[string]any{"colony_id": "my_colony", "queen_name": "queen_technology"})
	deps.MCPCatalog = func() map[string][]map[string]any { return catalogWith("read_file", "write_file") }
	mux := colonyToolsMux(deps)

	patchRecorder, patchBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPatch, "/api/colony/my_colony/tools", map[string]any{"enabled_mcp_tools": []string{"read_file"}}))
	if patchRecorder.Code != http.StatusOK {
		t.Fatalf("patch = %d, body %s", patchRecorder.Code, patchRecorder.Body.String())
	}
	if tools, _ := patchBody["enabled_mcp_tools"].([]any); len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("enabled = %v", patchBody["enabled_mcp_tools"])
	}

	sidecar, err := readJSONObject(filepath.Join(home.ColoniesDir(), "my_colony", "tools.json"))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if tools, _ := sidecar["enabled_mcp_tools"].([]any); len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("sidecar = %v", sidecar)
	}
	if _, present := sidecar["updated_at"]; !present {
		t.Fatalf("updated_at missing: %v", sidecar)
	}
	metadata, _ := readJSONObject(filepath.Join(home.ColoniesDir(), "my_colony", "metadata.json"))
	if _, present := metadata["enabled_mcp_tools"]; present {
		t.Fatalf("metadata should not carry the allowlist: %v", metadata)
	}

	_, getBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colony/my_colony/tools", nil))
	servers := getBody["mcp_servers"].([]any)
	tools := servers[0].(map[string]any)["tools"].([]any)
	flags := map[string]bool{}
	for _, tool := range tools {
		entry := tool.(map[string]any)
		flags[entry["name"].(string)] = entry["enabled"].(bool)
	}
	if !flags["read_file"] || flags["write_file"] {
		t.Fatalf("enabled flags = %v", flags)
	}

	unknownRecorder, unknownBody := serveColonyRequest(mux, colonyRequest(t, http.MethodPatch, "/api/colony/my_colony/tools", map[string]any{"enabled_mcp_tools": []string{"ghost"}}))
	if unknownRecorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d", unknownRecorder.Code)
	}
	unknown, _ := unknownBody["unknown"].([]any)
	if len(unknown) != 1 || unknown[0] != "ghost" {
		t.Fatalf("unknown = %v", unknownBody)
	}
}

type fakeAllowlistColony struct {
	allowlist []string
	calls     int
}

func (f *fakeAllowlistColony) StreamID() string { return "colony:my_colony" }

func (f *fakeAllowlistColony) SubscribeToEvents([]eventbus.EventType, eventbus.EventHandler, ...eventbus.SubscribeOption) (string, error) {
	return "", nil
}

func (f *fakeAllowlistColony) InjectInput(string, string) error { return nil }

func (f *fakeAllowlistColony) SetToolAllowlist(enabled []string, _ map[string]struct{}) {
	f.allowlist = enabled
	f.calls++
}

// Parity: framework/server/tests/test_colony_tools.py::test_patch_refreshes_live_runtime
func TestPatchRefreshesLiveRuntime(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	seedToolColony(t, home, "my_colony", map[string]any{"colony_id": "my_colony"})
	deps.MCPCatalog = func() map[string][]map[string]any { return catalogWith("read_file", "write_file") }
	runtime := &fakeAllowlistColony{}
	manager := NewSessionManager()
	manager.AddSession(&Session{ID: "sess-1", ColonyID: "my_colony", Colony: runtime})
	deps.Manager = manager
	recorder, body := serveColonyRequest(colonyToolsMux(deps), colonyRequest(t, http.MethodPatch, "/api/colony/my_colony/tools", map[string]any{"enabled_mcp_tools": []string{"read_file"}}))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["refreshed_runtimes"] != float64(1) {
		t.Fatalf("refreshed = %v", body["refreshed_runtimes"])
	}
	if len(runtime.allowlist) != 1 || runtime.allowlist[0] != "read_file" || runtime.calls != 1 {
		t.Fatalf("runtime allowlist = %v calls %d", runtime.allowlist, runtime.calls)
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_404_for_unknown_colony
func Test404ForUnknownColony(t *testing.T) {
	deps, _ := colonyToolsDeps(t)
	mux := colonyToolsMux(deps)
	getRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colony/unknown/tools", nil))
	if getRecorder.Code != http.StatusNotFound {
		t.Fatalf("get = %d, want 404", getRecorder.Code)
	}
	patchRecorder, _ := serveColonyRequest(mux, colonyRequest(t, http.MethodPatch, "/api/colony/unknown/tools", map[string]any{"enabled_mcp_tools": nil}))
	if patchRecorder.Code != http.StatusNotFound {
		t.Fatalf("patch = %d, want 404", patchRecorder.Code)
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_tools_index_lists_colonies
func TestToolsIndexListsColonies(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	seedToolColony(t, home, "my_colony", map[string]any{"colony_id": "my_colony", "queen_name": "queen_technology"})
	recorder, body := serveColonyRequest(colonyToolsMux(deps), httptest.NewRequest(http.MethodGet, "/api/colonies/tools-index", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	colonies, _ := body["colonies"].([]any)
	found := false
	for _, entry := range colonies {
		colony := entry.(map[string]any)
		if colony["name"] == "my_colony" {
			found = true
			if colony["queen_name"] != "queen_technology" || colony["has_allowlist"] != false {
				t.Fatalf("entry = %v", colony)
			}
		}
	}
	if !found {
		t.Fatalf("my_colony missing: %v", body)
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_queen_allowlist_inherits_into_new_colony
func TestQueenAllowlistInheritsIntoNewColony(t *testing.T) {
	_, home := colonyToolsDeps(t)
	colonyID := "forked_child"
	if err := os.MkdirAll(filepath.Join(home.ColoniesDir(), colonyID), 0o755); err != nil {
		t.Fatal(err)
	}
	queenAllowlist := []string{"read_file", "web_scrape", "csv_read"}
	if err := updateColonyToolsConfig(home, colonyID, queenAllowlist); err != nil {
		t.Fatalf("update: %v", err)
	}
	loaded := loadColonyToolsConfig(home, colonyID)
	if len(loaded) != len(queenAllowlist) {
		t.Fatalf("loaded = %v", loaded)
	}
	for index, name := range queenAllowlist {
		if loaded[index] != name {
			t.Fatalf("loaded = %v", loaded)
		}
	}
}

// Parity: framework/server/tests/test_colony_tools.py::test_legacy_metadata_field_migrates_to_sidecar
func TestLegacyMetadataFieldMigratesToSidecar(t *testing.T) {
	_, home := colonyToolsDeps(t)
	seedToolColony(t, home, "my_colony", map[string]any{
		"colony_id":         "my_colony",
		"queen_name":        "queen_technology",
		"enabled_mcp_tools": []string{"read_file"},
	})
	loaded := loadColonyToolsConfig(home, "my_colony")
	if len(loaded) != 1 || loaded[0] != "read_file" {
		t.Fatalf("loaded = %v", loaded)
	}
	sidecar, err := readJSONObject(filepath.Join(home.ColoniesDir(), "my_colony", "tools.json"))
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	if tools, _ := sidecar["enabled_mcp_tools"].([]any); len(tools) != 1 || tools[0] != "read_file" {
		t.Fatalf("sidecar = %v", sidecar)
	}
	metadata, _ := readJSONObject(filepath.Join(home.ColoniesDir(), "my_colony", "metadata.json"))
	if _, present := metadata["enabled_mcp_tools"]; present {
		t.Fatalf("metadata field not removed: %v", metadata)
	}
	if metadata["queen_name"] != "queen_technology" {
		t.Fatalf("provenance lost: %v", metadata)
	}
	again := loadColonyToolsConfig(home, "my_colony")
	if len(again) != 1 || again[0] != "read_file" {
		t.Fatalf("second load = %v", again)
	}
}

// Parity-exempt: 上游把该语义放在 ColonyRuntime._apply_tool_allowlist 单测；
// Go 侧运行时过滤属 internal/colony 车道，这里只钉住 HTTP 渲染的 enabled 标记。
func TestRenderServersAppliesAllowlistAndProviders(t *testing.T) {
	catalog := map[string][]ColonyToolEntry{
		"files-tools": {
			{Name: "read_file", Description: "read", InputSchema: map[string]any{}, Provider: "google"},
			{Name: "write_file", Description: "write", InputSchema: map[string]any{}, Provider: "slack"},
		},
	}
	servers := renderColonyToolServers(catalog, []string{"read_file"}, []string{"google"})
	tools := servers[0]["tools"].([]map[string]any)
	read := tools[0]
	write := tools[1]
	if read["enabled"] != true || read["provider_connected"] != true {
		t.Fatalf("read = %v", read)
	}
	if write["enabled"] != false || write["provider_connected"] != false {
		t.Fatalf("write = %v", write)
	}
	if read["provider"] != "google" || write["provider"] != "slack" {
		t.Fatalf("providers = %v", tools)
	}
	allEnabled := renderColonyToolServers(catalog, nil, nil)
	for _, tool := range allEnabled[0]["tools"].([]map[string]any) {
		if tool["enabled"] != true {
			t.Fatalf("default-allow tool disabled: %v", tool)
		}
	}
	noneEnabled := renderColonyToolServers(catalog, []string{}, nil)
	for _, tool := range noneEnabled[0]["tools"].([]map[string]any) {
		if tool["enabled"] != false {
			t.Fatalf("empty allowlist enabled a tool: %v", tool)
		}
	}
}

// Parity-exempt: 上游 tools-index 未覆盖空 allowlist / 无 metadata 目录的分支。
func TestToolsIndexReportsEmptyAllowlistAndSkipsNonColonies(t *testing.T) {
	deps, home := colonyToolsDeps(t)
	if err := os.MkdirAll(filepath.Join(home.ColoniesDir(), "bare_dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedToolColony(t, home, "explicit_none", map[string]any{"colony_id": "explicit_none"})
	if err := updateColonyToolsConfig(home, "explicit_none", []string{}); err != nil {
		t.Fatal(err)
	}
	_, body := serveColonyRequest(colonyToolsMux(deps), httptest.NewRequest(http.MethodGet, "/api/colonies/tools-index", nil))
	colonies, _ := body["colonies"].([]any)
	if len(colonies) != 1 {
		t.Fatalf("colonies = %v", colonies)
	}
	entry := colonies[0].(map[string]any)
	if entry["name"] != "explicit_none" || entry["has_allowlist"] != true || entry["enabled_count"] != float64(0) {
		t.Fatalf("entry = %v", entry)
	}
}
