package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// cloudSurface mirrors the subset of contracts/cloud-surface.json this test
// needs. The surface is the single registry for the module's API/SSE/WS
// invariants: routes may not drift from it in either direction.
type cloudSurface struct {
	Schema    string `json:"schema"`
	Endpoints []struct {
		ID        string   `json:"id"`
		Service   string   `json:"service"`
		Method    string   `json:"method"`
		Path      string   `json:"path"`
		Auth      string   `json:"auth"`
		Status    string   `json:"status"`
		Milestone string   `json:"milestone"`
		SSEEvents []string `json:"sse_events"`
	} `json:"endpoints"`
	Streams []struct {
		Path  string `json:"path"`
		Event string `json:"event"`
	} `json:"streams"`
	Websockets []struct {
		Path string `json:"path"`
	} `json:"websockets"`
}

var surfaceParam = regexp.MustCompile(`\{[^}]+\}`)

func loadSurface(t *testing.T) cloudSurface {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "cloud-surface.json"))
	if err != nil {
		t.Fatalf("read cloud surface: %v", err)
	}
	var surface cloudSurface
	if err := json.Unmarshal(raw, &surface); err != nil {
		t.Fatalf("parse cloud surface: %v", err)
	}
	if surface.Schema != "hive-cloud-surface/v1" {
		t.Fatalf("unexpected surface schema %q", surface.Schema)
	}
	if len(surface.Endpoints) == 0 {
		t.Fatal("cloud surface has no endpoints")
	}
	return surface
}

// TestContractCoverage is the route-vs-surface invariant:
//   - the set of routes registered by server.go::register() equals the set of
//     endpoints marked implemented in contracts/cloud-surface.json;
//   - auth classes and public flags agree;
//   - every declared SSE stream belongs to an implemented route and appears in
//     that endpoint's sse_events;
//   - implemented endpoints do not fall through to 404.
//
// M2+ work adds routes to the surface (status flips to implemented) instead of
// editing a hardcoded list here; deleting a route without touching the surface
// fails just as loudly.
func TestContractCoverage(t *testing.T) {
	e := newEnv(t)
	surface := loadSurface(t)

	validAuth := map[string]bool{"bearer-jwt": true, "bearer-stream-token": true, "api-key": true, "none": true}
	implemented := map[string]string{} // "METHOD /path" -> auth
	streams := map[string][]string{}   // "METHOD /path" -> declared events
	for _, ep := range surface.Endpoints {
		if !validAuth[ep.Auth] {
			t.Errorf("endpoint %s has unknown auth %q", ep.ID, ep.Auth)
		}
		switch ep.Status {
		case "implemented":
			key := strings.TrimSpace(ep.Method + " " + ep.Path)
			if _, dup := implemented[key]; dup {
				t.Errorf("surface lists %s twice as implemented", key)
			}
			implemented[key] = ep.Auth
			streams[key] = append(streams[key], ep.SSEEvents...)
		case "planned", "inferred", "unknown":
			// Not registered today; asserted by TestCloudSurfacePlannedRoutesUnregistered.
		default:
			t.Errorf("endpoint %s has unknown status %q", ep.ID, ep.Status)
		}
	}
	if len(implemented) == 0 {
		t.Fatal("surface marks no endpoint implemented")
	}

	registered := map[string]bool{}
	public := map[string]bool{}
	for _, route := range e.app.Routes() {
		key := route.Method + " " + route.Pattern
		if registered[key] {
			t.Errorf("route %s registered twice", key)
		}
		registered[key] = true
		public[key] = route.Public
	}

	var missing, extra []string
	for key := range implemented {
		if !registered[key] {
			missing = append(missing, key)
		}
	}
	for key := range registered {
		if _, ok := implemented[key]; !ok {
			extra = append(extra, key)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("routes and cloud-surface implemented set differ\n--- surface only (not registered) ---\n%s\n--- registered only (missing from surface) ---\n%s",
			strings.Join(sortedList(missing), "\n"), strings.Join(sortedList(extra), "\n"))
	}

	for key, auth := range implemented {
		wantPublic := auth != "bearer-jwt" && auth != "api-key"
		if public[key] != wantPublic {
			t.Errorf("%s: route public=%v, surface auth=%q implies public=%v", key, public[key], auth, wantPublic)
		}
	}

	// Every declared SSE stream must be attached to an implemented endpoint and
	// listed in its sse_events (event names are re-checked against code by
	// scripts/gen_cloud_surface.py --check).
	if len(surface.Streams) == 0 {
		t.Error("cloud surface declares no SSE streams, but /v1/inbox/runtime/stream serves one")
	}
	for _, stream := range surface.Streams {
		key := "GET " + stream.Path
		if _, ok := implemented[key]; !ok {
			t.Errorf("SSE stream %s is not an implemented endpoint", key)
		}
		if stream.Event == "" {
			t.Errorf("SSE stream %s has an empty event name", key)
		}
		if !contains(streams[key], stream.Event) {
			t.Errorf("SSE stream %s event %q missing from endpoint sse_events %v", key, stream.Event, streams[key])
		}
	}
	if len(surface.Websockets) != 0 {
		t.Errorf("test expects no WebSocket today; surface added %d — extend this test deliberately", len(surface.Websockets))
	}

	// Behavioural check: registered endpoints must reach a handler (401 for
	// authed ones, a real response for public ones) — never 404.
	for key := range implemented {
		method, pattern, _ := strings.Cut(key, " ")
		req := httptest.NewRequest(method, concretePath(pattern), nil)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("registered endpoint %s routes to 404", key)
		}
	}
}

// TestCloudSurfacePlannedRoutesUnregistered asserts that nothing marked
// planned/inferred/unknown can leak into the router before its milestone. The
// invariant is what keeps "not built yet" honest. A documented path may collide
// with an implemented route for another method (GET /v1/inbox/messages vs
// POST), so 405 is accepted there — but never a 2xx.
func TestCloudSurfacePlannedRoutesUnregistered(t *testing.T) {
	e := newEnv(t)
	surface := loadSurface(t)

	checked := 0
	for _, ep := range surface.Endpoints {
		if ep.Status == "implemented" {
			continue
		}
		if ep.Path == "" || ep.Method == "" {
			continue // unknown with no recoverable path — nothing to probe
		}
		key := ep.Method + " " + ep.Path
		req := httptest.NewRequest(ep.Method, concretePath(ep.Path), nil)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusNotFound:
			// The expected "not built yet" answer.
		case http.StatusMethodNotAllowed:
			// The path is registered for another method; this endpoint is
			// still not served.
		default:
			t.Errorf("%s (%s) is status=%s but the router answered %d, want 404/405", key, ep.Milestone, ep.Status, rec.Code)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no planned/inferred/unknown endpoint with a concrete path was probed")
	}
}

// TestCloudSurfaceScope pins the extension boundary: /health and
// /internal/inbox/reply are private-deploy extensions, never official cloud
// contract endpoints.
func TestCloudSurfaceScope(t *testing.T) {
	surface := loadSurface(t)
	want := map[string]bool{"GET /health": true, "POST /internal/inbox/reply": true}
	seen := 0
	for _, ep := range surface.Endpoints {
		key := ep.Method + " " + ep.Path
		if !want[key] {
			continue
		}
		seen++
		if ep.Service != "go-hive-cloud-extension" {
			t.Errorf("%s: service=%q, want go-hive-cloud-extension", key, ep.Service)
		}
		if ep.Status != "implemented" {
			t.Errorf("%s: status=%q, want implemented", key, ep.Status)
		}
	}
	if seen != len(want) {
		t.Fatalf("extension endpoints registered in surface = %d, want %d", seen, len(want))
	}
}

func concretePath(pattern string) string {
	return surfaceParam.ReplaceAllString(pattern, "probe")
}

func sortedList(items []string) []string {
	out := append([]string(nil), items...)
	sort.Strings(out)
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
