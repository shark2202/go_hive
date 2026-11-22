package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func newFeatureHome(t *testing.T) hivehome.Home {
	t.Helper()
	t.Setenv("HIVE_ADAPTIVE_TOOL_BUDGET", "")
	t.Setenv("HIVE_EMAIL_SENDERS", "")
	t.Setenv("HIVE_MEMORY_TIMELINE", "")
	t.Setenv("HIVE_STALL_CHECK", "")
	return hivehome.Home{Root: t.TempDir()}
}

func writeFeatureConfig(t *testing.T, home hivehome.Home, config map[string]any) {
	t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(home.ConfigurationFile()), 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(home.ConfigurationFile(), data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func readFeatureConfig(t *testing.T, home hivehome.Home) map[string]any {
	t.Helper()
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return config
}

type stubFeatureColony struct {
	value   bool
	defined bool
}

func (c *stubFeatureColony) AdaptiveToolBudget() (bool, bool) { return c.value, c.defined }

func (c *stubFeatureColony) SetAdaptiveToolBudget(value bool) { c.value = value }

// Parity: tests/test_routes_config_features.py::test_getter_defaults_true
func TestTheAdaptiveBudgetGetterDefaultsToOn(t *testing.T) {
	home := newFeatureHome(t)
	if !AdaptiveToolBudgetEnabled(home) {
		t.Fatal("adaptive_tool_budget should default to true")
	}
}

// Parity: tests/test_routes_config_features.py::test_getter_reads_config_file
func TestTheAdaptiveBudgetGetterReadsTheConfigFile(t *testing.T) {
	home := newFeatureHome(t)

	writeFeatureConfig(t, home, map[string]any{"adaptive_tool_budget": false})
	if AdaptiveToolBudgetEnabled(home) {
		t.Fatal("false in configuration.json should win over the default")
	}

	writeFeatureConfig(t, home, map[string]any{"adaptive_tool_budget": true})
	if !AdaptiveToolBudgetEnabled(home) {
		t.Fatal("true in configuration.json should win over the default")
	}

	writeFeatureConfig(t, home, map[string]any{"adaptive_tool_budget": "nope"})
	if !AdaptiveToolBudgetEnabled(home) {
		t.Fatal("non-boolean values are ignored, falling back to true")
	}
}

// Parity: tests/test_routes_config_features.py::test_getter_env_wins_over_file
func TestTheAdaptiveBudgetEnvironmentVariableWinsOverTheFile(t *testing.T) {
	home := newFeatureHome(t)

	writeFeatureConfig(t, home, map[string]any{"adaptive_tool_budget": true})
	t.Setenv("HIVE_ADAPTIVE_TOOL_BUDGET", "0")
	if AdaptiveToolBudgetEnabled(home) {
		t.Fatal("HIVE_ADAPTIVE_TOOL_BUDGET=0 should disable the feature")
	}

	t.Setenv("HIVE_ADAPTIVE_TOOL_BUDGET", "1")
	writeFeatureConfig(t, home, map[string]any{"adaptive_tool_budget": false})
	if !AdaptiveToolBudgetEnabled(home) {
		t.Fatal("HIVE_ADAPTIVE_TOOL_BUDGET=1 should enable the feature")
	}
}

// Parity: tests/test_routes_config_features.py::test_get_features_default
func TestTheFeaturesEndpointReportsTheDefaultToggles(t *testing.T) {
	home := newFeatureHome(t)
	got := FeaturesResponse(home)
	want := map[string]any{
		"features": map[string]any{
			"adaptive_tool_budget": true,
			"email_senders":        false,
			"memory_timeline":      false,
			"stall_check":          false,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("features = %#v, want %#v", got, want)
	}
}

// Parity: tests/test_routes_config_features.py::test_put_memory_timeline_persists
func TestPuttingTheMemoryTimelinePersistsAndTheGetterAndGetSeeIt(t *testing.T) {
	home := newFeatureHome(t)

	status, body := UpdateFeatures(home, map[string]any{"features": map[string]any{"memory_timeline": true}}, nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200 (body %#v)", status, body)
	}
	if got := readFeatureConfig(t, home)["memory_timeline"]; got != true {
		t.Fatalf("on-disk memory_timeline = %#v, want true", got)
	}
	if !MemoryTimelineEnabled(home) {
		t.Fatal("getter should see the persisted value")
	}
	features := FeaturesResponse(home)["features"].(map[string]any)
	if features["memory_timeline"] != true {
		t.Fatalf("GET memory_timeline = %#v, want true", features["memory_timeline"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_email_senders_persists_and_publishes_env
func TestPuttingEmailSendersPersistsAndRepublishesTheEnvironment(t *testing.T) {
	home := newFeatureHome(t)

	status, body := UpdateFeatures(home, map[string]any{"features": map[string]any{"email_senders": true}}, nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	wantFeatures := map[string]any{"email_senders": true}
	if !reflect.DeepEqual(body["features"], wantFeatures) {
		t.Fatalf("response features = %#v, want %#v", body["features"], wantFeatures)
	}
	if got := readFeatureConfig(t, home)["email_senders"]; got != true {
		t.Fatalf("on-disk email_senders = %#v, want true", got)
	}
	if got := os.Getenv("HIVE_EMAIL_SENDERS"); got != "1" {
		t.Fatalf("HIVE_EMAIL_SENDERS = %q, want \"1\"", got)
	}

	status, _ = UpdateFeatures(home, map[string]any{"features": map[string]any{"email_senders": false}}, nil)
	if status != 200 {
		t.Fatalf("second PUT status = %d, want 200", status)
	}
	if got := os.Getenv("HIVE_EMAIL_SENDERS"); got != "0" {
		t.Fatalf("HIVE_EMAIL_SENDERS = %q after disable, want \"0\"", got)
	}
	features := FeaturesResponse(home)["features"].(map[string]any)
	if features["email_senders"] != false {
		t.Fatalf("GET email_senders = %#v, want false", features["email_senders"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_persists_and_get_reflects
func TestPuttingAFeaturePersistsItAndGetReflectsIt(t *testing.T) {
	home := newFeatureHome(t)

	status, body := UpdateFeatures(home, map[string]any{"features": map[string]any{"adaptive_tool_budget": false}}, nil)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	wantFeatures := map[string]any{"adaptive_tool_budget": false}
	if !reflect.DeepEqual(body["features"], wantFeatures) {
		t.Fatalf("response features = %#v, want %#v", body["features"], wantFeatures)
	}
	if got := readFeatureConfig(t, home)["adaptive_tool_budget"]; got != false {
		t.Fatalf("on-disk adaptive_tool_budget = %#v, want false", got)
	}
	features := FeaturesResponse(home)["features"].(map[string]any)
	if features["adaptive_tool_budget"] != false {
		t.Fatalf("GET adaptive_tool_budget = %#v, want false", features["adaptive_tool_budget"])
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_preserves_other_config_keys
func TestPuttingFeaturesPreservesUnrelatedConfigKeys(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"llm":         map[string]any{"provider": "anthropic"},
		"gcu_enabled": false,
	})

	status, _ := UpdateFeatures(home, map[string]any{"adaptive_tool_budget": true}, nil)
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
func TestPuttingFeaturesValidatesTheBody(t *testing.T) {
	home := newFeatureHome(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"non-boolean value", map[string]any{"features": map[string]any{"adaptive_tool_budget": "yes"}}},
		{"no known keys", map[string]any{"features": map[string]any{"unknown_flag": true}}},
		{"non-object block", map[string]any{"features": []any{1, 2}}},
	}
	for _, tc := range cases {
		status, _ := UpdateFeatures(home, tc.body, nil)
		if status != 400 {
			t.Fatalf("%s: status = %d, want 400", tc.name, status)
		}
	}
	if _, err := os.Stat(home.ConfigurationFile()); !os.IsNotExist(err) {
		t.Fatal("validation failures must not write configuration.json")
	}
}

// Parity: tests/test_routes_config_features.py::test_put_features_hot_applies_respecting_colony_pins
func TestPuttingTheAdaptiveBudgetHotAppliesButColonyPinsWin(t *testing.T) {
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
	status, body := UpdateFeatures(home, map[string]any{"features": map[string]any{"adaptive_tool_budget": false}}, sessions)
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["colonies_applied"] != 1 {
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
