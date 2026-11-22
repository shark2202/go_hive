package server

// Parity: core/tests/test_routes_config_memory.py — Settings → Memory: how
// notes reach a queen, and which model writes them.

import (
	"net/http"
	"os"
	"reflect"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func newMemorySettingsHome(t *testing.T) hivehome.Home {
	t.Helper()
	home := newFeatureHome(t)
	t.Setenv("HIVE_MEMORY_RECALL", "")
	return home
}

func memoryBlock(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	block, ok := body["memory"].(map[string]any)
	if !ok {
		t.Fatalf("body has no memory object: %#v", body)
	}
	return block
}

// Parity: core/tests/test_routes_config_memory.py::test_get_shows_the_defaults_and_the_choices
func TestMemorySettingsGetShowsDefaultsAndChoices(t *testing.T) {
	home := newMemorySettingsHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodGet, server.URL+"/api/config/memory", nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	want := map[string]any{
		"memory": map[string]any{
			"recall_mode":       "index",
			"recall_modes":      []any{"index", "wait", "background"},
			"recall_mode_env":   nil,
			"reflection_model":  "worker",
			"reflection_models": []any{"worker", "main"},
		},
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("body = %#v, want %#v", body, want)
	}
}

// Parity: core/tests/test_routes_config_memory.py::test_get_says_when_the_env_overrides_the_recall_mode
func TestMemorySettingsGetReportsTheEnvOverride(t *testing.T) {
	home := newMemorySettingsHome(t)
	t.Setenv("HIVE_MEMORY_RECALL", "wait")
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	_, body := configDo(t, http.MethodGet, server.URL+"/api/config/memory", nil)
	block := memoryBlock(t, body)
	if block["recall_mode"] != "wait" || block["recall_mode_env"] != "wait" {
		t.Fatalf("memory = %#v", block)
	}
}

// Parity: core/tests/test_routes_config_memory.py::test_put_saves_the_choices_and_keeps_the_rest_of_each_section
func TestMemorySettingsPutSavesChoicesAndKeepsOtherSectionKeys(t *testing.T) {
	home := newMemorySettingsHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"llm":               map[string]any{"model": "m"},
		"memory_recall":     map[string]any{"wait_seconds": 4},
		"memory_reflection": map[string]any{"every_turns": 3},
	})
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDo(t, http.MethodPut, server.URL+"/api/config/memory",
		map[string]any{"memory": map[string]any{"recall_mode": "wait", "reflection_model": "main"}})
	if status != 200 {
		t.Fatalf("status = %d body %#v", status, body)
	}
	block := memoryBlock(t, body)
	if block["recall_mode"] != "wait" || block["reflection_model"] != "main" {
		t.Fatalf("memory = %#v", block)
	}
	want := map[string]any{
		"llm":               map[string]any{"model": "m"},
		"memory_recall":     map[string]any{"wait_seconds": float64(4), "mode": "wait"},
		"memory_reflection": map[string]any{"every_turns": float64(3), "model": "main"},
	}
	if got := readFeatureConfig(t, home); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuration.json = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_routes_config_memory.py::test_put_changes_only_the_setting_it_names
func TestMemorySettingsPutChangesOnlyTheSettingItNames(t *testing.T) {
	home := newMemorySettingsHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, _ := configDo(t, http.MethodPut, server.URL+"/api/config/memory",
		map[string]any{"memory": map[string]any{"reflection_model": "main"}})
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	want := map[string]any{"memory_reflection": map[string]any{"model": "main"}}
	if got := readFeatureConfig(t, home); !reflect.DeepEqual(got, want) {
		t.Fatalf("configuration.json = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_routes_config_memory.py::test_put_rejects_what_it_cannot_save_and_saves_nothing
func TestMemorySettingsPutRejectsWhatItCannotSaveAndSavesNothing(t *testing.T) {
	home := newMemorySettingsHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	cases := []struct {
		body  map[string]any
		error string
	}{
		{map[string]any{"memory": map[string]any{"recall_mode": "someday"}}, "recall_mode must be one of: index, wait, background"},
		{map[string]any{"memory": map[string]any{"recall_mode": "wait", "reflection_model": "cheap"}}, "reflection_model must be one of: worker, main"},
		{map[string]any{"memory": map[string]any{"timeline": true}}, "No known memory settings. Supported: recall_mode, reflection_model"},
		{map[string]any{"memory": "wait"}, "memory must be an object"},
	}
	for _, testCase := range cases {
		status, body := configDo(t, http.MethodPut, server.URL+"/api/config/memory", testCase.body)
		if status != 400 {
			t.Fatalf("%#v status = %d, want 400", testCase.body, status)
		}
		if body["error"] != testCase.error {
			t.Fatalf("%#v error = %#v, want %q", testCase.body, body["error"], testCase.error)
		}
		if _, err := os.Stat(home.ConfigurationFile()); err == nil {
			t.Fatalf("configuration.json was written for %#v", testCase.body)
		}
	}
}

// Parity-exempt: a valid-JSON non-object body reaches the handler (Python's
// isinstance(body, dict) guard); it must be rejected as "memory must be an
// object", not the generic 500 the map-only decoders return.
func TestMemorySettingsPutRejectsANonObjectBody(t *testing.T) {
	home := newMemorySettingsHome(t)
	server := newConfigRoutes(t, ConfigRouteDeps{Home: home})

	status, body := configDoRaw(t, http.MethodPut, server.URL+"/api/config/memory", `["wait"]`)
	if status != 400 || body["error"] != "memory must be an object" {
		t.Fatalf("array body = %d %#v", status, body)
	}
	status, body = configDoRaw(t, http.MethodPut, server.URL+"/api/config/memory", `not json`)
	if status != 400 || body["error"] != "Invalid JSON body" {
		t.Fatalf("invalid json = %d %#v", status, body)
	}
}
