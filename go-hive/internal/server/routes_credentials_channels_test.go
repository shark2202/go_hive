package server

// Parity: framework/server/tests/test_channels.py

import (
	"net/http"
	"testing"
)

func channelRouteTestMux(deps ChannelRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterChannelRoutes(mux, deps)
	return mux
}

// Parity: framework/server/tests/test_channels.py::test_the_server_starts_enabled_channels_and_reports_the_rest
func TestChannelRoutesListsSortedStates(t *testing.T) {
	states := map[string]map[string]any{
		"good": {"state": "running"},
		"bad":  {"state": "failed", "error": "RuntimeError: no QQ client connected"},
		"idle": {"state": "off"},
	}
	mux := channelRouteTestMux(ChannelRouteDeps{States: func() map[string]map[string]any { return states }})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/channels", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	list := credentialRouteJSONList(t, body["channels"])
	if len(list) != 3 {
		t.Fatalf("channels = %#v", list)
	}
	bad := credentialRouteJSONMap(t, list[0])
	if bad["name"] != "bad" || bad["state"] != "failed" || bad["error"] != "RuntimeError: no QQ client connected" {
		t.Fatalf("bad = %#v", bad)
	}
	good := credentialRouteJSONMap(t, list[1])
	if good["name"] != "good" || good["state"] != "running" {
		t.Fatalf("good = %#v", good)
	}
	idle := credentialRouteJSONMap(t, list[2])
	if idle["name"] != "idle" || idle["state"] != "off" {
		t.Fatalf("idle = %#v", idle)
	}
}

// Parity-exempt: no channels are registered in the bare test app; pins the empty-list envelope.
func TestChannelRoutesEmptyList(t *testing.T) {
	mux := channelRouteTestMux(ChannelRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/channels", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	list, ok := body["channels"].([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("channels = %#v", body["channels"])
	}
}
