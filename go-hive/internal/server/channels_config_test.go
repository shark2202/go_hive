package server

// Tests for the `channels` section's field-level shape and the no-host notice.

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Parity: core/framework/server/channels.py::start_channels
func TestChannelSectionsReadsTheConfiguredShape(t *testing.T) {
	config := map[string]any{"channels": map[string]any{
		"mychat":  map[string]any{"enabled": true},
		"offchat": map[string]any{"enabled": false},
		"truthy":  map[string]any{"enabled": "true"},
		"number":  map[string]any{"enabled": 1},
		"missing": map[string]any{},
		"scalar":  "not an object",
		"plain":   map[string]any{"enabled": true, "token": "kept"},
	}}

	sections := ChannelSections(config)
	if len(sections) != 6 {
		t.Fatalf("sections = %#v, want six object entries", sections)
	}
	byName := map[string]bool{}
	for _, section := range sections {
		byName[section.Name] = section.Enabled
	}
	for name, want := range map[string]bool{
		"mychat": true, "offchat": false, "truthy": true, "number": true,
		"missing": false, "plain": true,
	} {
		if got, present := byName[name]; !present || got != want {
			t.Fatalf("%s enabled = %v (present %v), want %v", name, got, present, want)
		}
	}
	if _, present := byName["scalar"]; present {
		t.Fatal("a non-object channel section must not be listed")
	}

	// A missing or non-object section reads as none, like `... or {}`.
	if got := ChannelSections(map[string]any{"channels": "nope"}); got != nil {
		t.Fatalf("non-object section = %#v", got)
	}
	if got := ChannelSections(map[string]any{}); got != nil {
		t.Fatalf("missing section = %#v", got)
	}
}

// Parity: core/framework/server/channels.py::start_channels
func TestUnhostedChannelsNamesTheEnabledOnes(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"channels": map[string]any{
		"mychat":  map[string]any{"enabled": true},
		"offchat": map[string]any{"enabled": false},
	}})
	names := UnhostedChannels(home)
	if len(names) != 1 || names[0] != "mychat" {
		t.Fatalf("UnhostedChannels = %#v, want [mychat]", names)
	}
}

// Parity: core/framework/server/channels.py::handle_list_channels
func TestTheChannelsRouteKeepsTheUpstreamEmptyShape(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{"channels": map[string]any{
		"mychat": map[string]any{"enabled": true},
	}})

	mux := http.NewServeMux()
	RegisterChannelRoutes(mux, ChannelRouteDeps{Home: home})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	status, body := configDo(t, http.MethodGet, server.URL+"/api/channels", nil)
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	channels, ok := body["channels"].([]any)
	if !ok || len(channels) != 0 {
		t.Fatalf("body = %#v, want {\"channels\": []} while no host exists", body)
	}
}

// Parity: core/tests/test_routes_config_features.py::test_put_features_preserves_other_config_keys
func TestConfigWritesPreserveChannelsAndCreatedAt(t *testing.T) {
	home := newFeatureHome(t)
	writeFeatureConfig(t, home, map[string]any{
		"created_at": "2026-01-15T12:00:00+00:00",
		"channels":   map[string]any{"mychat": map[string]any{"enabled": true}},
	})

	status, response := UpdateFeatures(home, map[string]any{
		"features": map[string]any{"memory_timeline": true},
	}, nil)
	if status != 200 {
		t.Fatalf("UpdateFeatures status = %d (%#v)", status, response)
	}
	disk := readFeatureConfig(t, home)
	if disk["created_at"] != "2026-01-15T12:00:00+00:00" {
		t.Fatalf("created_at = %#v, want the installer timestamp preserved", disk["created_at"])
	}
	channels, ok := disk["channels"].(map[string]any)
	if !ok {
		t.Fatalf("channels = %#v, want the section preserved", disk["channels"])
	}
	entry, _ := channels["mychat"].(map[string]any)
	if entry["enabled"] != true {
		t.Fatalf("channels.mychat = %#v, want enabled preserved", channels["mychat"])
	}
}
