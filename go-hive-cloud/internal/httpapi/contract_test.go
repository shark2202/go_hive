package httpapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestContractCoverage parses contracts/observed-contract.json and asserts that
// every contract endpoint in M1 scope (non-unknown, not CRM) has a registered
// route, and that no route outside the documented M1 surface exists.
//
// CRM (/v1/crm/*) is a later milestone (README §4, M4) and is deliberately not
// registered; LLM-proxy and credential endpoints live on other hosts and are
// separate services in the contract.
func TestContractCoverage(t *testing.T) {
	e := newEnv(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "observed-contract.json"))
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var contract struct {
		Schema   string `json:"schema"`
		Services map[string]struct {
			Endpoints []struct {
				Method string `json:"method"`
				Path   string `json:"path"`
				Status string `json:"status"`
			} `json:"endpoints"`
		} `json:"services"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse contract: %v", err)
	}
	backend, ok := contract.Services["hive-backend"]
	if !ok {
		t.Fatalf("contract missing hive-backend service")
	}

	registered := map[string]bool{}
	for _, route := range e.app.Routes() {
		registered[route.Method+" "+route.Pattern] = true
	}

	m1Prefixes := []string{"/v1/global-db/", "/v1/inbox/", "/v1/gateway/", "/user/"}
	covered := 0
	for _, endpoint := range backend.Endpoints {
		if endpoint.Status == "unknown" {
			continue // contract explicitly marks these UNKNOWN
		}
		if strings.HasPrefix(endpoint.Path, "/v1/crm/") {
			continue // M4 scope
		}
		inScope := false
		for _, prefix := range m1Prefixes {
			if strings.HasPrefix(endpoint.Path, prefix) {
				inScope = true
			}
		}
		if !inScope {
			continue
		}
		covered++
		key := endpoint.Method + " " + endpoint.Path
		if !registered[key] {
			t.Errorf("contract endpoint %s has no registered route (routes: %v)", key, sortedKeys(registered))
			continue
		}
		// Behavioural check: the pattern must not fall through to 404. Authed
		// endpoints answer 401 without a token; public ones answer non-404.
		concrete := strings.NewReplacer("{table}", "leads", "{entity}", "leads", "{id}", "id1").Replace(endpoint.Path)
		req := httptest.NewRequest(endpoint.Method, concrete, nil)
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("contract endpoint %s routes to 404", key)
		}
	}
	if covered != 13 {
		t.Errorf("in-scope contract endpoints = %d, want 13 (global-db 8, inbox 3, gateway 1, stream-token 1)", covered)
	}

	// M1 surface: exactly these patterns, no more.
	wantRoutes := []string{
		"GET /health",
		"GET /v1/gateway/availability",
		"GET /v1/global-db/changes",
		"GET /v1/global-db/tables",
		"GET /v1/global-db/tables/{table}/rows",
		"GET /v1/inbox/runtime/stream",
		"PATCH /v1/global-db/tables/{table}/rows",
		"POST /internal/inbox/reply",
		"POST /v1/global-db/import",
		"POST /v1/global-db/query",
		"POST /v1/global-db/sql",
		"POST /v1/global-db/upsert",
		"POST /v1/inbox/messages",
		"POST /v1/inbox/resolve",
		"POST /user/refresh-stream-token-from-stream",
	}
	sort.Strings(wantRoutes)
	gotRoutes := sortedKeys(registered)
	if strings.Join(wantRoutes, "\n") != strings.Join(gotRoutes, "\n") {
		t.Fatalf("registered routes differ from M1 surface\n--- want ---\n%s\n--- got ---\n%s",
			strings.Join(wantRoutes, "\n"), strings.Join(gotRoutes, "\n"))
	}

	// CRM endpoints stay unregistered in M1.
	req := httptest.NewRequest(http.MethodGet, "/v1/crm/grid/schema", nil)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("CRM route should be out of M1 scope, got %d", rec.Code)
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
