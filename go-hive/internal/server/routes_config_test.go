package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func newConfigRoutes(t *testing.T, deps ConfigRouteDeps) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	RegisterConfigRoutes(mux, deps)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func configDoRaw(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("%s %s: decode body: %v", method, url, err)
	}
	return resp.StatusCode, decoded
}

func configDo(t *testing.T, method, url string, body any) (int, map[string]any) {
	t.Helper()
	if body == nil {
		return configDoRaw(t, method, url, "")
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return configDoRaw(t, method, url, string(data))
}

func featuresBlock(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	block, ok := body["features"].(map[string]any)
	if !ok {
		t.Fatalf("body has no features object: %#v", body)
	}
	return block
}

// Parity: tests/test_routes_config_features.py::test_get_features_default
func TestTheFeaturesRouteReportsTheDefaultToggles(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/features", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	want := map[string]any{"features": map[string]any{
		"adaptive_tool_budget": true,
		"email_senders":        false,
		"memory_timeline":      false,
		"stall_check":          false,
	}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v, want %#v", body, want)
	}
}

// Parity: tests/test_routes_config_features.py::test_put_memory_timeline_persists
func TestTheFeaturesRoutePersistsTheMemoryTimeline(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"features": map[string]any{"memory_timeline": true}})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := body["features"]; !reflect.DeepEqual(got, map[string]any{"memory_timeline": true}) {
		t.Fatalf("response features = %#v", got)
	}
	if got := readFeatureConfig(t, home)["memory_timeline"]; got != true {
		t.Fatalf("on-disk memory_timeline = %#v, want true", got)
	}
	if !MemoryTimelineEnabled(home) {
		t.Fatal("getter should see the persisted value")
	}
	status, body = configDo(t, http.MethodGet, server.URL+"/api/config/features", nil)
	if status != 200 {
		t.Fatalf("GET status = %d, want 200", status)
	}
	if featuresBlock(t, body)["memory_timeline"] != true {
		t.Fatalf("GET memory_timeline = %#v, want true", featuresBlock(t, body)["memory_timeline"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_email_senders_persists_and_publishes_env
func TestTheFeaturesRoutePublishesTheEmailSendersEnvironment(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"features": map[string]any{"email_senders": true}})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := body["features"]; !reflect.DeepEqual(got, map[string]any{"email_senders": true}) {
		t.Fatalf("response features = %#v", got)
	}
	if got := readFeatureConfig(t, home)["email_senders"]; got != true {
		t.Fatalf("on-disk email_senders = %#v, want true", got)
	}
	if got := os.Getenv("HIVE_EMAIL_SENDERS"); got != "1" {
		t.Fatalf("HIVE_EMAIL_SENDERS = %q, want \"1\"", got)
	}

	status, _ = configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"features": map[string]any{"email_senders": false}})
	if status != 200 {
		t.Fatalf("second PUT status = %d, want 200", status)
	}
	if got := os.Getenv("HIVE_EMAIL_SENDERS"); got != "0" {
		t.Fatalf("HIVE_EMAIL_SENDERS = %q after disable, want \"0\"", got)
	}
	_, body = configDo(t, http.MethodGet, server.URL+"/api/config/features", nil)
	if featuresBlock(t, body)["email_senders"] != false {
		t.Fatalf("GET email_senders = %#v, want false", featuresBlock(t, body)["email_senders"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_persists_and_get_reflects
func TestTheFeaturesRoutePersistsTheAdaptiveBudget(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"features": map[string]any{"adaptive_tool_budget": false}})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := body["features"]; !reflect.DeepEqual(got, map[string]any{"adaptive_tool_budget": false}) {
		t.Fatalf("response features = %#v", got)
	}
	if got := readFeatureConfig(t, home)["adaptive_tool_budget"]; got != false {
		t.Fatalf("on-disk adaptive_tool_budget = %#v, want false", got)
	}
	_, body = configDo(t, http.MethodGet, server.URL+"/api/config/features", nil)
	if featuresBlock(t, body)["adaptive_tool_budget"] != false {
		t.Fatalf("GET adaptive_tool_budget = %#v, want false", featuresBlock(t, body)["adaptive_tool_budget"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_preserves_other_config_keys
func TestTheFeaturesRoutePreservesUnrelatedConfigKeys(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"llm":         map[string]any{"provider": "anthropic"},
		"gcu_enabled": false,
	})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"adaptive_tool_budget": true})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	onDisk := readFeatureConfig(t, home)
	if !reflect.DeepEqual(onDisk["llm"], map[string]any{"provider": "anthropic"}) {
		t.Fatalf("llm = %#v, want it preserved", onDisk["llm"])
	}
	if onDisk["gcu_enabled"] != false {
		t.Fatalf("gcu_enabled = %#v, want false preserved", onDisk["gcu_enabled"])
	}
	if onDisk["adaptive_tool_budget"] != true {
		t.Fatalf("adaptive_tool_budget = %#v, want true", onDisk["adaptive_tool_budget"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_validation
func TestTheFeaturesRouteValidatesTheBody(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"non-boolean value", map[string]any{"features": map[string]any{"adaptive_tool_budget": "yes"}}},
		{"no known keys", map[string]any{"features": map[string]any{"unknown_flag": true}}},
		{"non-object block", map[string]any{"features": []any{1, 2}}},
	}
	for _, tc := range cases {
		status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/features", tc.body)
		if status != 400 {
			t.Fatalf("%s: status = %d, want 400", tc.name, status)
		}
	}
	if _, err := os.Stat(home.ConfigurationFile()); !os.IsNotExist(err) {
		t.Fatal("validation failures must not write configuration.json")
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_hot_applies_respecting_colony_pins
func TestTheFeaturesRouteHotAppliesRespectingColonyPins(t *testing.T) {
	home := newFeatureHome(t)

	unpinned := &stubFeatureColony{value: true, defined: true}
	pinned := &stubFeatureColony{value: true, defined: true}
	alreadyOff := &stubFeatureColony{value: false, defined: true}

	metaDir := filepath.Join(home.ColoniesDir(), "colony_b")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir metadata dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "metadata.json"), []byte(`{"adaptive_tool_budget": true}`), 0o644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	sessions := []FeatureSession{
		{Colony: unpinned, ColonyID: "colony_a"},
		{Colony: pinned, ColonyID: "colony_b"},
		{Colony: nil, ColonyID: ""},
		{Colony: alreadyOff, ColonyID: "colony_c"},
	}
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home:     home,
		Sessions: func() []FeatureSession { return sessions },
	})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/features",
		map[string]any{"features": map[string]any{"adaptive_tool_budget": false}})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["colonies_applied"] != float64(1) {
		t.Fatalf("colonies_applied = %#v, want 1", body["colonies_applied"])
	}
	if unpinned.value != false {
		t.Fatal("unpinned colony should flip to false")
	}
	if pinned.value != true {
		t.Fatal("pinned colony should keep its pinned true")
	}
	if alreadyOff.value != false {
		t.Fatal("already-off colony should stay false")
	}
}

func libraryEntry() map[string]any {
	return map[string]any{
		"provider":     "deepseek",
		"model":        "deepseek-chat",
		"api_key":      "sk-test-1234",
		"custom_extra": map[string]any{"nested": true},
	}
}

// Parity: tests/test_routes_provider_library.py::test_get_library_empty
func TestTheProviderLibraryRouteStartsEmpty(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/provider-library", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	want := map[string]any{"library": map[string]any{}}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v, want %#v", body, want)
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_saves_entry_verbatim
func TestTheProviderLibraryRouteSavesEntriesVerbatim(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})
	entry := libraryEntry()

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ds-prod", "section": entry})
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if !reflect.DeepEqual(body["section"], entry) {
		t.Fatalf("response section = %#v, want %#v", body["section"], entry)
	}
	stored, ok := readFeatureConfig(t, home)["provider_library"].(map[string]any)
	if !ok {
		t.Fatal("provider_library missing on disk")
	}
	if !reflect.DeepEqual(stored["ds-prod"], entry) {
		t.Fatalf("on-disk entry = %#v, want %#v", stored["ds-prod"], entry)
	}
	_, body = configDo(t, http.MethodGet, server.URL+"/api/config/provider-library", nil)
	want := map[string]any{"ds-prod": entry}
	if !reflect.DeepEqual(body["library"], want) {
		t.Fatalf("GET library = %#v, want %#v", body["library"], want)
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_validation
func TestTheProviderLibraryRouteValidatesTheBody(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})
	entry := libraryEntry()

	cases := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"section": entry}},
		{"missing model", map[string]any{"name": "x", "section": map[string]any{"provider": "openai"}}},
		{"non-object section", map[string]any{"name": "x", "section": []any{1}}},
	}
	for _, tc := range cases {
		status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library", tc.body)
		if status != 400 {
			t.Fatalf("%s: status = %d, want 400", tc.name, status)
		}
	}
}

// Parity: tests/test_routes_provider_library.py::test_put_null_deletes_and_prunes_empty_library
func TestTheProviderLibraryRouteDeletesAndPrunes(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "a", "section": libraryEntry()}); status != 200 {
		t.Fatalf("save status = %d, want 200", status)
	}
	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "a", "section": nil})
	if status != 200 {
		t.Fatalf("delete status = %d, want 200", status)
	}
	if body["section"] != nil {
		t.Fatalf("delete section = %#v, want null", body["section"])
	}
	if _, present := readFeatureConfig(t, home)["provider_library"]; present {
		t.Fatal("deleting the last entry should prune provider_library")
	}
	status, _ = configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ghost", "section": nil})
	if status != 200 {
		t.Fatalf("deleting a missing entry: status = %d, want 200", status)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_to_worker_slot
func TestTheProviderLibraryApplyRouteFillsTheWorkerSlot(t *testing.T) {
	home := newFeatureHome(t)
	entry := libraryEntry()
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home: home,
		Role: RoleWriteDeps{
			HotSwap: func(string, string, string) int {
				t.Error("worker_llm apply must not hot-swap sessions")
				return 0
			},
		},
	})

	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ds", "section": entry}); status != 200 {
		t.Fatalf("library save status = %d, want 200", status)
	}
	status, body := configDo(t, http.MethodPost, server.URL+"/api/config/provider-library/apply",
		map[string]any{"name": "ds", "role": "worker_llm"})
	if status != 200 {
		t.Fatalf("apply status = %d, want 200", status)
	}
	if body["role"] != "worker_llm" {
		t.Fatalf("role = %#v, want worker_llm", body["role"])
	}
	if !reflect.DeepEqual(body["section"], entry) {
		t.Fatalf("section = %#v, want %#v", body["section"], entry)
	}
	if body["sessions_swapped"] != float64(0) {
		t.Fatalf("sessions_swapped = %#v, want 0", body["sessions_swapped"])
	}
	onDisk := readFeatureConfig(t, home)
	if !reflect.DeepEqual(onDisk["worker_llm"], entry) {
		t.Fatalf("on-disk worker_llm = %#v, want %#v", onDisk["worker_llm"], entry)
	}
	stored := onDisk["provider_library"].(map[string]any)
	if !reflect.DeepEqual(stored["ds"], entry) {
		t.Fatalf("library copy = %#v, want it kept", stored["ds"])
	}
	_, body = configDo(t, http.MethodGet, server.URL+"/api/config/llm-sections", nil)
	if !reflect.DeepEqual(body["worker_llm"], entry) {
		t.Fatalf("GET worker_llm = %#v, want %#v", body["worker_llm"], entry)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_to_llm_slot_hot_swaps_sessions
func TestTheProviderLibraryApplyRouteHotSwapsTheMainSlot(t *testing.T) {
	home := newFeatureHome(t)
	type reconfigure struct {
		model   string
		apiKey  string
		apiBase string
	}
	var swapped []reconfigure
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home: home,
		Role: RoleWriteDeps{
			HotSwap: func(fullModel, apiKey, apiBase string) int {
				swapped = append(swapped, reconfigure{fullModel, apiKey, apiBase})
				return 1
			},
		},
	})

	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ds", "section": libraryEntry()}); status != 200 {
		t.Fatalf("library save status = %d, want 200", status)
	}
	status, body := configDo(t, http.MethodPost, server.URL+"/api/config/provider-library/apply",
		map[string]any{"name": "ds", "role": "llm"})
	if status != 200 {
		t.Fatalf("apply status = %d, want 200", status)
	}
	if body["sessions_swapped"] != float64(1) {
		t.Fatalf("sessions_swapped = %#v, want 1", body["sessions_swapped"])
	}
	want := []reconfigure{{model: "deepseek/deepseek-chat", apiKey: "sk-test-1234", apiBase: ""}}
	if !reflect.DeepEqual(swapped, want) {
		t.Fatalf("hot swap calls = %#v, want %#v", swapped, want)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_unknown_name_and_role
func TestTheProviderLibraryApplyRouteRejectsAnUnknownNameOrRole(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, _ := configDo(t, http.MethodPost, server.URL+"/api/config/provider-library/apply",
		map[string]any{"name": "nope", "role": "llm"})
	if status != 404 {
		t.Fatalf("unknown name: status = %d, want 404", status)
	}
	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ds", "section": libraryEntry()}); status != 200 {
		t.Fatalf("library save status = %d, want 200", status)
	}
	status, _ = configDo(t, http.MethodPost, server.URL+"/api/config/provider-library/apply",
		map[string]any{"name": "ds", "role": "bogus"})
	if status != 400 {
		t.Fatalf("unknown role: status = %d, want 400", status)
	}
}

// Parity: tests/test_routes_provider_library.py::test_apply_health_checks_key_against_base
func TestTheProviderLibraryApplyRouteHealthChecksTheKey(t *testing.T) {
	home := newFeatureHome(t)
	type validateCall struct {
		provider string
		apiKey   string
		apiBase  string
		model    string
	}
	var calls []validateCall
	server := newConfigRoutes(t, ConfigRouteDeps{
		Home: home,
		Role: RoleWriteDeps{
			Validate: func(provider, apiKey, apiBase, model string) (any, string) {
				calls = append(calls, validateCall{provider, apiKey, apiBase, model})
				return false, "bad key"
			},
		},
	})

	entry := libraryEntry()
	entry["api_base"] = "https://ds.example.com/v1"
	if status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/provider-library",
		map[string]any{"name": "ds", "section": entry}); status != 200 {
		t.Fatalf("library save status = %d, want 200", status)
	}
	if len(calls) != 0 {
		t.Fatalf("library save must not health-check, got %#v", calls)
	}

	status, _ := configDo(t, http.MethodPost, server.URL+"/api/config/provider-library/apply",
		map[string]any{"name": "ds", "role": "worker_llm"})
	if status != 400 {
		t.Fatalf("failed health check: status = %d, want 400", status)
	}
	want := []validateCall{{"deepseek", "sk-test-1234", "https://ds.example.com/v1", "deepseek-chat"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("validator calls = %#v, want %#v", calls, want)
	}
	_, body := configDo(t, http.MethodGet, server.URL+"/api/config/llm-sections", nil)
	if body["worker_llm"] != nil {
		t.Fatalf("worker_llm = %#v, want null after a failed check", body["worker_llm"])
	}
}

// Parity-exempt: no upstream test pins the shared parser. An empty, malformed,
func TestTheConfigRoutesRejectInvalidJSONBodies(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	cases := []struct {
		name    string
		body    string
		status  int
		message string
	}{
		{"empty", "", 400, "Invalid JSON body"},
		{"malformed", "{", 400, "Invalid JSON body"},
		{"trailing garbage", `{"features": {}} trailing`, 400, "Invalid JSON body"},
		{"array", "[1, 2]", 500, internalServerError},
		{"null", "null", 500, internalServerError},
	}
	for _, tc := range cases {
		for _, route := range []struct{ method, path string }{
			{http.MethodPut, "/api/config/features"},
			{http.MethodPut, "/api/config/llm-sections"},
			{http.MethodPut, "/api/config/llm"},
			{http.MethodPut, "/api/config/provider-library"},
			{http.MethodPost, "/api/config/provider-library/apply"},
			{http.MethodPut, "/api/config/sentinel"},
			{http.MethodPut, "/api/config/rate-limits"},
			{http.MethodPut, "/api/config/profile"},
			{http.MethodPut, "/api/config/external-skills"},
		} {
			status, body := configDoRaw(t, route.method, server.URL+route.path, tc.body)
			if status != tc.status {
				t.Fatalf("%s %s: status = %d, want %d", tc.name, route.path, status, tc.status)
			}
			if body["error"] != tc.message {
				t.Fatalf("%s %s: error = %#v, want %q", tc.name, route.path, body["error"], tc.message)
			}
		}
	}
}

// Parity-exempt: aiohttp runs every handler on one event loop, so upstream's
func TestConcurrentConfigWritesDoNotOverwriteEachOther(t *testing.T) {
	home := newFeatureHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	const writers = 25
	start := make(chan struct{})
	errs := make(chan error, writers+1)
	var wg sync.WaitGroup

	put := func(body map[string]any, label string) {
		defer wg.Done()
		data, err := json.Marshal(body)
		if err != nil {
			errs <- err
			return
		}
		req, err := http.NewRequest(http.MethodPut, server.URL+"/api/config/provider-library", bytes.NewReader(data))
		if err == nil {
			var resp *http.Response
			resp, err = http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					err = fmt.Errorf("%s: status %d", label, resp.StatusCode)
				}
			}
		}
		if err != nil {
			errs <- err
		}
	}

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			<-start
			put(map[string]any{
				"name":    fmt.Sprintf("vendor-%02d", i),
				"section": map[string]any{"provider": "openai", "model": fmt.Sprintf("model-%02d", i)},
			}, fmt.Sprintf("library save %d", i))
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		body, _ := json.Marshal(map[string]any{"features": map[string]any{"memory_timeline": true}})
		req, err := http.NewRequest(http.MethodPut, server.URL+"/api/config/features", bytes.NewReader(body))
		if err != nil {
			errs <- err
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errs <- fmt.Errorf("features save: status %d", resp.StatusCode)
		}
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	config := readFeatureConfig(t, home)
	if config["memory_timeline"] != true {
		t.Fatalf("concurrent feature write was lost: %#v", config["memory_timeline"])
	}
	library, _ := config["provider_library"].(map[string]any)
	for i := 0; i < writers; i++ {
		name := fmt.Sprintf("vendor-%02d", i)
		if _, ok := library[name]; !ok {
			t.Fatalf("library entry %q was lost; %d of %d survived", name, len(library), writers)
		}
	}
}
