package credentials

// Parity: tools/src/aden_tools/credentials/health_check.py

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func healthTestServer(t *testing.T, status int, body string, inspect func(*http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if inspect != nil {
			inspect(r)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// Parity: tools/src/aden_tools/credentials/health_check.py::check_credential_health — a spec endpoint without a
// dedicated checker falls back to the generic OAuth bearer checker; without an
// endpoint the credential is assumed valid.
func TestCheckCredentialHealthFallbackDispatch(t *testing.T) {
	server := healthTestServer(t, 200, `{}`, func(r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer vendor-key" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
	})
	result := CheckCredentialHealth("some_vendor", "vendor-key", server.URL, "GET")
	if !result.Valid || result.Message != "Some Vendor credentials valid" {
		t.Fatalf("result = %#v", result)
	}

	assumed := CheckCredentialHealth("some_vendor", "vendor-key", "", "GET")
	if !assumed.Valid || assumed.Message != "No health checker for 'some_vendor', assuming valid" {
		t.Fatalf("assumed = %#v", assumed)
	}
	if noChecker, ok := assumed.Details["no_checker"].(bool); !ok || !noChecker {
		t.Fatalf("details = %#v", assumed.Details)
	}
}

// Parity: tools/src/aden_tools/credentials/health_check.py::OAuthBearerHealthChecker.check — status mapping and
// message text.
func TestOAuthBearerHealthCheckStatusMapping(t *testing.T) {
	cases := []struct {
		status  int
		want    string
		valid   bool
		details map[string]any
	}{
		{401, "Vendor token is invalid or expired", false, map[string]any{"status_code": 401}},
		{403, "Vendor token lacks required scopes", false, map[string]any{"status_code": 403}},
		{500, "Vendor API returned status 500", false, map[string]any{"status_code": 500}},
	}
	for _, tc := range cases {
		server := healthTestServer(t, tc.status, `{}`, nil)
		result := runOAuthBearerHealthCheck("token", server.URL, "Vendor")
		if result.Valid != tc.valid || result.Message != tc.want {
			t.Fatalf("status %d: result = %#v, want %q", tc.status, result, tc.want)
		}
		if result.Details["status_code"] != tc.details["status_code"] {
			t.Fatalf("status %d: details = %#v", tc.status, result.Details)
		}
	}
}

// Parity: tools/src/aden_tools/credentials/health_check.py::BaseHttpHealthChecker._interpret_response — the
// shared status interpretation behind the declarative checkers, including the
// 429-valid and identity-extraction branches.
func TestBaseHTTPHealthCheckInterpretation(t *testing.T) {
	identity := func(data map[string]any) map[string]string {
		out := map[string]string{}
		if email := pyMapGetString(data, "email", ""); email != "" {
			out["email"] = email
		}
		return out
	}
	cfg := healthRequestConfig{endpoint: "placeholder", serviceName: "Brevo", authType: healthAuthBearer, extractIdentity: identity}

	server := healthTestServer(t, 200, `{"email":"ops@example.com"}`, nil)
	cfg.endpoint = server.URL
	result := runBaseHTTPHealthCheck(cfg, "brevo-key", []string{"Bearer", "Authorization", "api_key", "token"})
	if !result.Valid || result.Message != "Brevo credentials valid" {
		t.Fatalf("200 result = %#v", result)
	}
	detailsIdentity, ok := result.Details["identity"].(map[string]any)
	if !ok || detailsIdentity["email"] != "ops@example.com" {
		t.Fatalf("identity = %#v", result.Details)
	}

	server = healthTestServer(t, 429, `{}`, nil)
	cfg.endpoint = server.URL
	result = runBaseHTTPHealthCheck(cfg, "brevo-key", nil)
	if !result.Valid || result.Message != "Brevo credentials valid (rate limited)" {
		t.Fatalf("429 result = %#v", result)
	}
	if result.Details["rate_limited"] != true {
		t.Fatalf("429 details = %#v", result.Details)
	}

	server = healthTestServer(t, 401, `{}`, nil)
	cfg.endpoint = server.URL
	result = runBaseHTTPHealthCheck(cfg, "brevo-key", nil)
	if result.Valid || result.Message != "Brevo credentials are invalid or expired" {
		t.Fatalf("401 result = %#v", result)
	}

	server = healthTestServer(t, 403, `{}`, nil)
	cfg.endpoint = server.URL
	result = runBaseHTTPHealthCheck(cfg, "brevo-key", nil)
	if result.Valid || result.Message != "Brevo credentials lack required permissions" {
		t.Fatalf("403 result = %#v", result)
	}

	server = healthTestServer(t, 500, `{}`, nil)
	cfg.endpoint = server.URL
	result = runBaseHTTPHealthCheck(cfg, "brevo-key", nil)
	if result.Valid || result.Message != "Brevo API returned status 500" {
		t.Fatalf("500 result = %#v", result)
	}
}

// Auth shapes: bearer header, custom header template, query param, basic auth
// and a POST JSON body (Exa's shape).
// Parity: tools/src/aden_tools/credentials/health_check.py::BaseHttpHealthChecker.check
func TestBaseHTTPHealthCheckAuthShapes(t *testing.T) {
	var gotPath, gotAuth, gotCustom, gotQuery, gotMethod, gotBody string
	server := healthTestServer(t, 200, `{}`, func(r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCustom = r.Header.Get("PRIVATE-TOKEN")
		gotQuery = r.URL.Query().Get("apiKey")
		gotMethod = r.Method
		buffer := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buffer)
		gotBody = string(buffer)
	})
	cases := []healthRequestConfig{
		{endpoint: server.URL + "/bearer", serviceName: "V", authType: healthAuthBearer},
		{endpoint: server.URL + "/header", serviceName: "V", authType: healthAuthHeader, authHeaderName: "PRIVATE-TOKEN", authHeaderTemplate: "{token}"},
		{endpoint: server.URL + "/query", serviceName: "V", authType: healthAuthQuery, authQueryParam: "apiKey"},
		{endpoint: server.URL + "/basic", serviceName: "V", authType: healthAuthBasic},
		{endpoint: server.URL + "/post", serviceName: "V", authType: healthAuthBearer, method: "POST", jsonBody: map[string]any{"query": "test"}},
	}
	for index, cfg := range cases {
		gotPath, gotAuth, gotCustom, gotQuery, gotMethod = "", "", "", "", ""
		result := runBaseHTTPHealthCheck(cfg, "secret", nil)
		if !result.Valid {
			t.Fatalf("case %d: result = %#v", index, result)
		}
		switch index {
		case 0:
			if gotAuth != "Bearer secret" || !strings.HasSuffix(gotPath, "/bearer") {
				t.Fatalf("bearer = %q %q", gotAuth, gotPath)
			}
		case 1:
			if gotCustom != "secret" {
				t.Fatalf("custom header = %q", gotCustom)
			}
		case 2:
			if gotQuery != "secret" {
				t.Fatalf("query = %q", gotQuery)
			}
		case 3:
			if !strings.HasPrefix(gotAuth, "Basic ") {
				t.Fatalf("basic auth = %q", gotAuth)
			}
		case 4:
			if gotMethod != http.MethodPost || gotBody != `{"query":"test"}` {
				t.Fatalf("post = %q %q", gotMethod, gotBody)
			}
		}
	}
}

// A too-slow endpoint reports the timeout message (httpx.TimeoutException
// branch); the probe uses a short per-request timeout instead of waiting the
// production 10s.
// Parity: tools/src/aden_tools/credentials/health_check.py::BaseHttpHealthChecker.check
func TestHealthCheckTimeoutMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(200)
	}))
	t.Cleanup(server.Close)

	_, _, reqErr := performHealthRequest(healthRequestConfig{
		endpoint:    server.URL,
		serviceName: "Slow",
		authType:    healthAuthBearer,
		timeout:     20 * time.Millisecond,
	}, "token")
	if reqErr == nil || !reqErr.timeout {
		t.Fatalf("reqErr = %#v, want timeout", reqErr)
	}

	result := runBaseHTTPHealthCheck(healthRequestConfig{
		endpoint:    server.URL,
		serviceName: "Slow",
		authType:    healthAuthBearer,
		timeout:     20 * time.Millisecond,
	}, "token", nil)
	if result.Valid || result.Message != "Slow API request timed out" {
		t.Fatalf("result = %#v", result)
	}
	if result.Details["error"] != "timeout" {
		t.Fatalf("details = %#v", result.Details)
	}
}

// The registered checker table mirrors HEALTH_CHECKERS: every upstream key is
// present (a missing registration would silently fall back to bearer auth).
// Parity: tools/src/aden_tools/credentials/health_check.py::HEALTH_CHECKERS
func TestHealthCheckerRegistryMatchesUpstream(t *testing.T) {
	expected := []string{
		"apify", "apollo", "asana", "attio", "brave_search", "brevo", "calcom",
		"calendly_pat", "cloudflare", "discord", "docker_hub", "exa_search",
		"finlight", "github", "gitlab_token", "google", "google_maps",
		"google_search", "google_search_console", "greenhouse_token", "hubspot",
		"huggingface", "intercom", "linear", "lusha_api_key", "microsoft_graph",
		"newsdata", "notion", "pinecone", "pipedrive", "prometheus", "resend",
		"serpapi", "similarweb", "slack", "stripe", "telegram", "trello_key",
		"trello_token", "vercel", "youtube", "zoho_crm",
	}
	if len(healthCheckers) != len(expected) {
		t.Fatalf("healthCheckers = %d, want %d", len(healthCheckers), len(expected))
	}
	for _, name := range expected {
		if _, present := healthCheckers[name]; !present {
			t.Fatalf("checker %q missing from registry", name)
		}
	}
}
