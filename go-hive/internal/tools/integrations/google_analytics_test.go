package integrations

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ------------------------------------------------------- google analytics ---

// batch7Lane9GAHandler answers one runReport with the canonical GA4 shape.
func batch7Lane9GAHandler(t *testing.T, wantPath string, body map[string]any) func(http.ResponseWriter, *http.Request, map[string]any) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request, request map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Fatalf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if body["metrics"] != nil {
			metrics := batch4BodySlice(t, request["metrics"])
			if len(metrics) != len(batch4BodySlice(t, body["metrics"])) {
				t.Fatalf("metrics = %v, want %v", request["metrics"], body["metrics"])
			}
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"dimensionHeaders": []any{map[string]any{"name": "pagePath"}},
			"metricHeaders":    []any{map[string]any{"name": "sessions"}},
			"rowCount":         1,
			"rows": []any{map[string]any{
				"dimensionValues": []any{map[string]any{"value": "/home"}},
				"metricValues":    []any{map[string]any{"value": "500"}},
			}},
		})
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaRunReport::test_successful_report
func TestBatch7Lane9GARunReport(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_analytics_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/properties/123456:runReport" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if body["limit"] != float64(100) {
				t.Fatalf("limit = %v", body["limit"])
			}
			ranges := batch4BodySlice(t, body["dateRanges"])
			first := batch4BodyMap(t, ranges[0])
			if first["startDate"] != "28daysAgo" || first["endDate"] != "today" {
				t.Fatalf("dateRanges = %v", ranges)
			}
			metrics := batch4BodySlice(t, body["metrics"])
			if batch4BodyMap(t, metrics[0])["name"] != "sessions" {
				t.Fatalf("metrics = %v", metrics)
			}
			dimensions := batch4BodySlice(t, body["dimensions"])
			if batch4BodyMap(t, dimensions[0])["name"] != "pagePath" {
				t.Fatalf("dimensions = %v", dimensions)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"dimensionHeaders": []any{map[string]any{"name": "pagePath"}},
				"metricHeaders":    []any{map[string]any{"name": "sessions"}},
				"rowCount":         1,
				"rows": []any{map[string]any{
					"dimensionValues": []any{map[string]any{"value": "/home"}},
					"metricValues":    []any{map[string]any{"value": "500"}},
				}},
			})
		})
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_run_report", "property_id": "properties/123456",
		"metrics": []any{"sessions"}, "dimensions": []any{"pagePath"},
	})
	if batch7Lane9Numeric(t, result["row_count"]) != 1 {
		t.Fatalf("result = %v", result)
	}
	row := batch4BodyMap(t, batch4BodySlice(t, result["rows"])[0])
	if row["pagePath"] != "/home" || row["sessions"] != "500" {
		t.Fatalf("row = %v", row)
	}
	if headers, ok := result["dimension_headers"].([]string); !ok || headers[0] != "pagePath" {
		t.Fatalf("dimension_headers = %v", result["dimension_headers"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaRunReport::test_invalid_property_id_returns_error
func TestBatch7Lane9GAValidation(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "google_analytics_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"property prefix", map[string]any{"property_id": "123456", "metrics": []any{"sessions"}},
			"property_id must start with 'properties/' (e.g., 'properties/123456')"},
		{"empty property", map[string]any{"property_id": "", "metrics": []any{"sessions"}},
			"property_id must start with 'properties/' (e.g., 'properties/123456')"},
		{"limit low", map[string]any{"property_id": "properties/1", "metrics": []any{"sessions"}, "limit": 0},
			"limit must be between 1 and 10000"},
		{"limit high", map[string]any{"property_id": "properties/1", "metrics": []any{"sessions"}, "limit": 10001},
			"limit must be between 1 and 10000"},
		{"empty metrics", map[string]any{"property_id": "properties/1", "metrics": []any{}},
			"metrics list must not be empty"},
	}
	for _, testCase := range cases {
		input := map[string]any{"action": "ga_run_report"}
		for key, value := range testCase.input {
			input[key] = value
		}
		result := batch4Execute(t, registry, "google_analytics", input)
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("%s: error = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaRunReport::test_no_credentials_returns_error
func TestBatch7Lane9GANoCredentials(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_run_report", "property_id": "properties/1", "metrics": []any{"sessions"},
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaRunReport::test_api_error_returns_error_dict
func TestBatch7Lane9GAApiError(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "google_analytics_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			batch4WriteJSON(w, http.StatusForbidden, map[string]any{
				"error": map[string]any{"message": "Permission denied"},
			})
		})
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_run_report", "property_id": "properties/1", "metrics": []any{"sessions"},
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Permission denied") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaGetRealtime::test_default_metrics
func TestBatch7Lane9GARealtimeDefaults(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_analytics_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/properties/123:runRealtimeReport" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			metrics := batch4BodySlice(t, body["metrics"])
			if batch4BodyMap(t, metrics[0])["name"] != "activeUsers" {
				t.Fatalf("metrics = %v", metrics)
			}
			ranges := batch4BodySlice(t, body["minuteRanges"])
			first := batch4BodyMap(t, ranges[0])
			if first["startMinutesAgo"] != float64(29) || first["endMinutesAgo"] != float64(0) {
				t.Fatalf("minuteRanges = %v", ranges)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"metricHeaders": []any{map[string]any{"name": "activeUsers"}},
				"rowCount":      1,
				"rows": []any{map[string]any{
					"metricValues": []any{map[string]any{"value": "12"}},
				}},
			})
		})
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_get_realtime", "property_id": "properties/123",
	})
	row := batch4BodyMap(t, batch4BodySlice(t, result["rows"])[0])
	if row["activeUsers"] != "12" {
		t.Fatalf("row = %v", row)
	}
	if _, present := result["dimension_headers"]; present {
		t.Fatalf("realtime result should not carry dimension_headers: %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaGetRealtime::test_custom_metrics
func TestBatch7Lane9GARealtimeCustomMetrics(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "google_analytics_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			metrics := batch4BodySlice(t, body["metrics"])
			if len(metrics) != 2 {
				t.Fatalf("metrics = %v", metrics)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"metricHeaders": []any{}, "rows": []any{}})
		})
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_get_realtime", "property_id": "properties/1", "metrics": []any{"activeUsers", "screenPageViews"},
	})
	if batch7Lane9Numeric(t, result["row_count"]) != 0 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestGaGetTopPages::test_correct_dimensions_and_metrics
func TestBatch7Lane9GAReportWrappers(t *testing.T) {
	cases := []struct {
		action     string
		dimensions []string
		metrics    []string
		limit      float64
	}{
		{"ga_get_top_pages", []string{"pagePath", "pageTitle"}, []string{"screenPageViews", "averageSessionDuration", "bounceRate"}, 10},
		{"ga_get_traffic_sources", []string{"sessionSource", "sessionMedium"}, []string{"sessions", "totalUsers", "conversions"}, 10},
		{"ga_get_user_demographics", []string{"country", "language", "deviceCategory"}, []string{"totalUsers", "sessions", "engagedSessions"}, 20},
		{"ga_get_conversion_events", []string{"eventName"}, []string{"eventCount", "conversions", "totalRevenue"}, 20},
		{"ga_get_landing_pages", []string{"landingPagePlusQueryString"}, []string{"sessions", "bounceRate", "conversions", "averageSessionDuration"}, 10},
	}
	for _, testCase := range cases {
		t.Run(testCase.action, func(t *testing.T) {
			registry, _ := batch7Lane9Fake(t, "google_analytics_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					if body["limit"] != testCase.limit {
						t.Fatalf("limit = %v, want %v", body["limit"], testCase.limit)
					}
					dimensions := batch4BodySlice(t, body["dimensions"])
					if len(dimensions) != len(testCase.dimensions) {
						t.Fatalf("dimensions = %v", dimensions)
					}
					for index, name := range testCase.dimensions {
						if batch4BodyMap(t, dimensions[index])["name"] != name {
							t.Fatalf("dimensions = %v", dimensions)
						}
					}
					metrics := batch4BodySlice(t, body["metrics"])
					if len(metrics) != len(testCase.metrics) {
						t.Fatalf("metrics = %v", metrics)
					}
					for index, name := range testCase.metrics {
						if batch4BodyMap(t, metrics[index])["name"] != name {
							t.Fatalf("metrics = %v", metrics)
						}
					}
					batch4WriteJSON(w, http.StatusOK, map[string]any{"rows": []any{}, "rowCount": 0})
				})
			result := batch4Execute(t, registry, "google_analytics", map[string]any{
				"action": testCase.action, "property_id": "properties/1",
			})
			if _, failed := result["error"]; failed {
				t.Fatalf("result = %v", result)
			}
		})
	}
}

// Parity-exempt: upstream mocks the google client exception and cannot produce
// provider status codes; this table pins the GA error prefix per status.
func TestBatch7Lane9GAStatusMapping(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusTooManyRequests} {
		registry, _ := batch7Lane9Fake(t, "google_analytics_tool", nil,
			func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch4WriteJSON(w, status, map[string]any{"error": map[string]any{"message": "provider detail"}})
			})
		result := batch4Execute(t, registry, "google_analytics", map[string]any{
			"action": "ga_run_report", "property_id": "properties/1", "metrics": []any{"sessions"},
		})
		got := batch4ErrorText(t, result)
		if !strings.Contains(got, "Google Analytics API error: HTTP") || !strings.Contains(got, "provider detail") {
			t.Fatalf("status %d: error = %q", status, got)
		}
	}
}

// batch7Lane9ServiceAccountJSON writes a service-account key for the token flow.
func batch7Lane9ServiceAccountJSON(t *testing.T, tokenURI string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"client_email": "svc@example.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    tokenURI,
		"project_id":   "test-project",
	})
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	path := filepath.Join(t.TempDir(), "service-account.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return path
}

// Parity: tools/tests/tools/test_google_analytics_tool.py::TestCredentialRetrieval::test_credentials_from_env
func TestBatch7Lane9GAServiceAccountToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Fatalf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("assertion") == "" {
			t.Fatal("missing assertion")
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"access_token": "minted-token"})
	}))
	t.Cleanup(tokenServer.Close)

	keyPath := batch7Lane9ServiceAccountJSON(t, tokenServer.URL+"/token")
	gaServer, recorder := batch7Lane9Server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if got := r.Header.Get("Authorization"); got != "Bearer minted-token" {
			t.Fatalf("Authorization = %q", got)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"rows": []any{}, "rowCount": 0})
	})
	registry := newBatch7Lane9Registry(
		WithBaseURL("google_analytics_tool", gaServer.URL),
		WithHTTPClient(gaServer.Client()),
		WithGetenv(func(name string) string {
			if name == "GOOGLE_APPLICATION_CREDENTIALS" {
				return keyPath
			}
			return ""
		}),
	)
	result := batch4Execute(t, registry, "google_analytics", map[string]any{
		"action": "ga_run_report", "property_id": "properties/1", "metrics": []any{"sessions"},
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// ----------------------------------------------------- google search console ---

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscSearchAnalytics::test_successful_query
func TestBatch7Lane9GSCSearchAnalytics(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/sites/https://example.com/searchAnalytics/query" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["startDate"] != "2024-01-01" || body["endDate"] != "2024-01-31" {
				t.Fatalf("body = %v", body)
			}
			dimensions := batch4BodySlice(t, body["dimensions"])
			if len(dimensions) != 1 || dimensions[0] != "query" {
				t.Fatalf("dimensions = %v", dimensions)
			}
			if body["rowLimit"] != float64(100) || body["type"] != "web" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"rows": []any{
					map[string]any{"keys": []any{"best crm software"}, "clicks": 150, "impressions": 5000, "ctr": 0.03, "position": 4.2},
					map[string]any{"keys": []any{"crm"}, "clicks": 20, "impressions": 900, "ctr": 0.123456, "position": 12.3456},
				},
			})
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{
		"action": "gsc_search_analytics", "site_url": "https://example.com",
		"start_date": "2024-01-01", "end_date": "2024-01-31",
	})
	if result["count"] != 2 || result["site_url"] != "https://example.com" {
		t.Fatalf("result = %v", result)
	}
	first := batch4BodyMap(t, batch4BodySlice(t, result["rows"])[0])
	if batch7Lane9Numeric(t, first["clicks"]) != 150 || batch7Lane9Numeric(t, first["impressions"]) != 5000 {
		t.Fatalf("row = %v", first)
	}
	if first["ctr"] != 0.03 || first["position"] != 4.2 {
		t.Fatalf("row = %v", first)
	}
	second := batch4BodyMap(t, batch4BodySlice(t, result["rows"])[1])
	if second["ctr"] != 0.1235 || second["position"] != 12.3 {
		t.Fatalf("rounding = %v", second)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscSearchAnalytics::test_missing_params
func TestBatch7Lane9GSCMissingParams(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{"action": "gsc_search_analytics"})
	if got := batch4ErrorText(t, result); got != "site_url, start_date, and end_date are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscListSites::test_successful_list
func TestBatch7Lane9GSCListSites(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/sites" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"siteEntry": []any{map[string]any{"siteUrl": "https://example.com", "permissionLevel": "siteOwner"}},
			})
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{"action": "gsc_list_sites"})
	sites := batch4BodySlice(t, result["sites"])
	site := batch4BodyMap(t, sites[0])
	if site["site_url"] != "https://example.com" || site["permission_level"] != "siteOwner" {
		t.Fatalf("site = %v", site)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscListSitemaps::test_successful_list
func TestBatch7Lane9GSCListSitemaps(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/sites/sc-domain:example.com/sitemaps" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"sitemap": []any{map[string]any{
					"path": "https://example.com/sitemap.xml", "lastSubmitted": "2024-01-01T00:00:00Z",
					"isPending": false, "isSitemapsIndex": true, "warnings": 0, "errors": 1,
				}},
			})
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{
		"action": "gsc_list_sitemaps", "site_url": "sc-domain:example.com",
	})
	sitemap := batch4BodyMap(t, batch4BodySlice(t, result["sitemaps"])[0])
	if sitemap["path"] != "https://example.com/sitemap.xml" || sitemap["is_index"] != true {
		t.Fatalf("sitemap = %v", sitemap)
	}
	if batch7Lane9Numeric(t, sitemap["errors"]) != 1 {
		t.Fatalf("sitemap = %v", sitemap)
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscInspectUrl::test_successful_inspect
func TestBatch7Lane9GSCInspectURL(t *testing.T) {
	server, recorder := batch7Lane9Server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/urlInspection/index:inspect" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if body["inspectionUrl"] != "https://example.com/page" || body["siteUrl"] != "https://example.com" {
			t.Fatalf("body = %v", body)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"inspectionResult": map[string]any{
				"indexStatusResult": map[string]any{
					"verdict": "PASS", "coverageState": "Submitted and indexed",
					"indexingState": "INDEXING_ALLOWED", "lastCrawlTime": "2024-01-01T00:00:00Z",
					"crawledAs": "MOBILE", "pageFetchState": "SUCCESSFUL", "robotsTxtState": "ALLOWED",
				},
				"mobileUsabilityResult": map[string]any{"verdict": "PASS"},
			},
		})
	})
	registry := newBatch7Lane9Registry(
		WithBaseURL("google_search_console_tool", server.URL),
		WithBaseURL("google_search_console_inspection_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(string) string { return "" }),
		WithCredential("google_search_console_tool", "test-token"),
	)
	result := batch4Execute(t, registry, "google_search_console", map[string]any{
		"action": "gsc_inspect_url", "site_url": "https://example.com",
		"inspection_url": "https://example.com/page",
	})
	if result["verdict"] != "PASS" || result["coverage_state"] != "Submitted and indexed" {
		t.Fatalf("result = %v", result)
	}
	if result["crawled_as"] != "MOBILE" || result["mobile_verdict"] != "PASS" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscSubmitSitemap::test_successful_submit
func TestBatch7Lane9GSCSubmitSitemap(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPut {
				t.Fatalf("method = %s", r.Method)
			}
			want := "/sites/https://example.com/sitemaps/https://example.com/sitemap.xml"
			if r.URL.Path != want {
				t.Fatalf("path = %s, want %s", r.URL.Path, want)
			}
			w.WriteHeader(http.StatusOK)
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{
		"action": "gsc_submit_sitemap", "site_url": "https://example.com",
		"sitemap_url": "https://example.com/sitemap.xml",
	})
	if result["status"] != "submitted" || result["sitemap_url"] != "https://example.com/sitemap.xml" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: upstream has no unit test for the top-query/top-page wrappers;
// sorting and single-dimension bodies are pinned here.
func TestBatch7Lane9GSCTopQueriesAndPages(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			dimensions := batch4BodySlice(t, body["dimensions"])
			if len(dimensions) != 1 {
				t.Fatalf("dimensions = %v", dimensions)
			}
			if body["rowLimit"] != float64(25) {
				t.Fatalf("rowLimit = %v", body["rowLimit"])
			}
			if dimensions[0] == "query" {
				batch4WriteJSON(w, http.StatusOK, map[string]any{"rows": []any{
					map[string]any{"keys": []any{"low"}, "clicks": 1, "impressions": 10, "ctr": 0.1, "position": 9},
					map[string]any{"keys": []any{"high"}, "clicks": 9, "impressions": 90, "ctr": 0.1, "position": 2},
				}})
				return
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"rows": []any{
				map[string]any{"keys": []any{"https://example.com/a"}, "clicks": 5, "impressions": 50, "ctr": 0.1, "position": 3},
			}})
		})
	input := map[string]any{
		"site_url": "https://example.com", "start_date": "2024-01-01", "end_date": "2024-01-31",
	}
	queriesInput := map[string]any{"action": "gsc_top_queries"}
	for key, value := range input {
		queriesInput[key] = value
	}
	queries := batch4Execute(t, registry, "google_search_console", queriesInput)
	rows := batch4BodySlice(t, queries["queries"])
	if len(rows) != 2 {
		t.Fatalf("queries = %v", queries)
	}
	first := batch4BodyMap(t, rows[0])
	if first["query"] != "high" || batch7Lane9Numeric(t, first["clicks"]) != 9 {
		t.Fatalf("sort order = %v", first)
	}
	pagesInput := map[string]any{"action": "gsc_top_pages"}
	for key, value := range input {
		pagesInput[key] = value
	}
	pages := batch4Execute(t, registry, "google_search_console", pagesInput)
	page := batch4BodyMap(t, batch4BodySlice(t, pages["pages"])[0])
	if page["page"] != "https://example.com/a" {
		t.Fatalf("page = %v", page)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: upstream has no unit test for gsc_delete_sitemap; the DELETE
// verb and 204 handling are pinned here.
func TestBatch7Lane9GSCDeleteSitemap(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "google_search_console_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || !strings.HasSuffix(r.URL.Path, "/sitemaps/https://example.com/sitemap.xml") {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{
		"action": "gsc_delete_sitemap", "site_url": "https://example.com",
		"sitemap_url": "https://example.com/sitemap.xml",
	})
	if result["status"] != "deleted" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_search_console_tool.py::TestGscSearchAnalytics::test_missing_token
func TestBatch7Lane9GSCMissingToken(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "google_search_console", map[string]any{"action": "gsc_list_sites"})
	if got := batch4ErrorText(t, result); got != "GOOGLE_SEARCH_CONSOLE_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream resolves Search Console credentials only from a
// direct token; the service-account fallback is a Go-port seam over
// GOOGLE_APPLICATION_CREDENTIALS and is pinned here.
func TestBatch7Lane9GSCServiceAccountToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"access_token": "gsc-minted-token"})
	}))
	t.Cleanup(tokenServer.Close)

	keyPath := batch7Lane9ServiceAccountJSON(t, tokenServer.URL+"/token")
	gscServer, recorder := batch7Lane9Server(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if got := r.Header.Get("Authorization"); got != "Bearer gsc-minted-token" {
			t.Fatalf("Authorization = %q", got)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"siteEntry": []any{}})
	})
	registry := newBatch7Lane9Registry(
		WithBaseURL("google_search_console_tool", gscServer.URL),
		WithHTTPClient(gscServer.Client()),
		WithGetenv(func(name string) string {
			if name == "GOOGLE_APPLICATION_CREDENTIALS" {
				return keyPath
			}
			return ""
		}),
	)
	result := batch4Execute(t, registry, "google_search_console", map[string]any{"action": "gsc_list_sites"})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: provider statuses are not exercised upstream; this table pins
// the Search Console error vocabulary.
func TestBatch7Lane9GSCStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Unauthorized. Check your GOOGLE_SEARCH_CONSOLE_TOKEN."},
		{http.StatusForbidden, "Forbidden: forbidden detail"},
		{http.StatusNotFound, "Google API error 404: not found detail"},
		{http.StatusTooManyRequests, "Google API error 429: rate limited detail"},
	}
	details := map[int]string{
		http.StatusForbidden:       "forbidden detail",
		http.StatusNotFound:        "not found detail",
		http.StatusTooManyRequests: "rate limited detail",
	}
	for _, testCase := range cases {
		registry, _ := batch7Lane9Fake(t, "google_search_console_tool", nil,
			func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(details[testCase.status]))
			})
		result := batch4Execute(t, registry, "google_search_console", map[string]any{"action": "gsc_list_sites"})
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("status %d: error = %q, want %q", testCase.status, got, testCase.want)
		}
	}
}
