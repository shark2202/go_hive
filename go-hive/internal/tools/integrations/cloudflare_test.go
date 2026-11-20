package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane2TestZoneID    = "023e105f4ecef8ad9ca31a8372d0c353"
	batch7Lane2TestAccountID = "acct7lane2"
	batch7Lane2TestRecordID  = "rec7"
	batch7Lane2TestRuleID    = "rule7"
	batch7Lane2TestMemberID  = "member7"
	batch7Lane2TestAppID     = "app7"
)

// --------------------------------------------------------------- test kit ---

type batch7Lane2Recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch7Lane2Recorder struct {
	mu       sync.Mutex
	requests []batch7Lane2Recorded
}

func (rec *batch7Lane2Recorder) wrap(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch7Lane2Recorded{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r)
	}
}

func (rec *batch7Lane2Recorder) reset() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = nil
}

func (rec *batch7Lane2Recorder) at(t *testing.T, index int) batch7Lane2Recorded {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch7Lane2Recorder) first(t *testing.T) batch7Lane2Recorded {
	t.Helper()
	return rec.at(t, 0)
}

func (rec *batch7Lane2Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch7Lane2TestRegistry builds a Cloudflare registry pointed at one fake
// upstream with a fixed credential.
func batch7Lane2TestRegistry(
	t *testing.T,
	handler http.HandlerFunc,
) (*Registry, *batch7Lane2Recorder) {
	t.Helper()
	recorder := &batch7Lane2Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	registry := batch7Lane2NewRegistry(
		WithBaseURL(batch7Lane2ModuleID, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(batch7Lane2ModuleID, "test-token"),
	)
	return registry, recorder
}

func batch7Lane2Execute(t *testing.T, registry *Registry, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), "cloudflare", input)
	if err != nil {
		t.Fatalf("cloudflare execute: %v", err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("cloudflare returned %T, want map", result)
	}
	return object
}

func batch7Lane2WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch7Lane2ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch7Lane2FieldMap(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := source[name].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %T, want map (%v)", name, source[name], source)
	}
	return value
}

func batch7Lane2FieldSlice(t *testing.T, source map[string]any, name string) []any {
	t.Helper()
	value, present := source[name]
	if !present || value == nil {
		t.Fatalf("field %q missing (%v)", name, source)
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Slice {
		t.Fatalf("field %q = %T, want array (%v)", name, value, source)
	}
	out := make([]any, 0, rv.Len())
	for index := 0; index < rv.Len(); index++ {
		out = append(out, rv.Index(index).Interface())
	}
	return out
}

func batch7Lane2Number(t *testing.T, value any) int64 {
	t.Helper()
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		t.Fatalf("value = %T, want a number", value)
		return 0
	}
}

func batch7Lane2EqualInt(t *testing.T, value any, want int) {
	t.Helper()
	if got := batch7Lane2Number(t, value); got != int64(want) {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

// ------------------------------------------------------------- fixtures ---

func batch7Lane2ZoneFixture() map[string]any {
	return map[string]any{
		"id":           batch7Lane2TestZoneID,
		"name":         "example.com",
		"status":       "active",
		"name_servers": []any{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"},
		"plan":         map[string]any{"name": "Pro"},
		"created_on":   "2024-01-01T00:00:00Z",
		"modified_on":  "2024-02-01T00:00:00Z",
		"type":         "full",
	}
}

func batch7Lane2RecordFixture() map[string]any {
	return map[string]any{
		"id":          batch7Lane2TestRecordID,
		"type":        "A",
		"name":        "www.example.com",
		"content":     "192.0.2.10",
		"ttl":         300,
		"proxied":     true,
		"priority":    10,
		"created_on":  "2024-01-02T00:00:00Z",
		"modified_on": "2024-01-03T00:00:00Z",
	}
}

func batch7Lane2DashboardFixture() map[string]any {
	return map[string]any{
		"totals": map[string]any{
			"requests": map[string]any{
				"all":         1000,
				"country":     map[string]any{"US": 500, "DE": 120},
				"http_status": map[string]any{"200": 900, "404": 100},
				"content_type": map[string]any{
					"text/html": 800,
				},
				"threats": map[string]any{"all": 3},
			},
			"bandwidth":   map[string]any{"all": 2048},
			"threats":     map[string]any{"all": 7},
			"pageviews":   map[string]any{"all": 700},
			"uniques":     map[string]any{"all": 300},
			"performance": map[string]any{"ttfb": 120},
		},
		"query": map[string]any{"since": "2024-01-01T00:00:00Z", "until": "2024-01-02T00:00:00Z"},
	}
}

// ------------------------------------------------------- expected routes ---

type batch7Lane2ExpectedRoute struct {
	Method string
	Path   string
}

// batch7Lane2ExpectedRoutes is the independent wire expectation for the first
// request of every action, transcribed from the upstream f-strings. It is
// deliberately not derived from the action table.
var batch7Lane2ExpectedRoutes = map[string]batch7Lane2ExpectedRoute{
	"cloudflare_list_zones":                   {http.MethodGet, "/zones"},
	"cloudflare_get_zone":                     {http.MethodGet, "/zones/" + batch7Lane2TestZoneID},
	"cloudflare_get_zone_settings":            {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings"},
	"cloudflare_list_zone_custom_pages":       {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/custom_pages"},
	"cloudflare_get_ssl_verification":         {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/ssl/verification"},
	"cloudflare_list_zone_certificates":       {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/ssl/universal/settings"},
	"cloudflare_list_zone_subscriptions":      {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/subscriptions"},
	"cloudflare_get_dnssec_status":            {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/dnssec"},
	"cloudflare_update_zone_setting":          {http.MethodPatch, "/zones/" + batch7Lane2TestZoneID + "/settings/ssl"},
	"cloudflare_list_dns_records":             {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/dns_records"},
	"cloudflare_get_dns_record":               {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/dns_records/" + batch7Lane2TestRecordID},
	"cloudflare_list_dns_record_scan":         {http.MethodPost, "/zones/" + batch7Lane2TestZoneID + "/dns_records/scan"},
	"cloudflare_get_dns_settings":             {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/dns_settings"},
	"cloudflare_list_dns_analytics_report":    {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/dns_analytics/report"},
	"cloudflare_check_domain_dns_health":      {http.MethodGet, "/zones"},
	"cloudflare_create_dns_record":            {http.MethodPost, "/zones/" + batch7Lane2TestZoneID + "/dns_records"},
	"cloudflare_update_dns_record":            {http.MethodPatch, "/zones/" + batch7Lane2TestZoneID + "/dns_records/" + batch7Lane2TestRecordID},
	"cloudflare_delete_dns_record":            {http.MethodDelete, "/zones/" + batch7Lane2TestZoneID + "/dns_records/" + batch7Lane2TestRecordID},
	"cloudflare_get_zone_analytics":           {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/analytics/dashboard"},
	"cloudflare_get_top_analytics":            {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/analytics/dashboard"},
	"cloudflare_get_security_analytics":       {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/security/analytics"},
	"cloudflare_get_cache_analytics":          {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/cache/analytics/dashboard"},
	"cloudflare_get_performance_analytics":    {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/analytics/dashboard"},
	"cloudflare_get_http_analytics_report":    {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/analytics/dashboard"},
	"cloudflare_list_firewall_events":         {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/firewall/events"},
	"cloudflare_get_security_settings":        {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings/security_level"},
	"cloudflare_list_page_rules":              {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/pagerules"},
	"cloudflare_list_waf_rulesets":            {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/rulesets"},
	"cloudflare_get_bot_management_settings":  {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/bot_management"},
	"cloudflare_list_managed_transforms":      {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/managed_headers"},
	"cloudflare_get_ddos_protection_settings": {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/ddos_protection/settings"},
	"cloudflare_create_firewall_rule": {
		http.MethodPost,
		"/zones/" + batch7Lane2TestZoneID + "/rulesets/phases/http_request_firewall_custom/entrypoint/rules",
	},
	"cloudflare_delete_firewall_rule": {
		http.MethodDelete,
		"/zones/" + batch7Lane2TestZoneID + "/rulesets/phases/http_request_firewall_custom/entrypoint/rules/" + batch7Lane2TestRuleID,
	},
	"cloudflare_get_speed_settings":     {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings/minify"},
	"cloudflare_get_cache_settings":     {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings/browser_cache_ttl"},
	"cloudflare_get_http_config":        {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings/http2"},
	"cloudflare_get_network_settings":   {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/settings/websockets"},
	"cloudflare_purge_cache_all":        {http.MethodPost, "/zones/" + batch7Lane2TestZoneID + "/purge_cache"},
	"cloudflare_purge_cache_files":      {http.MethodPost, "/zones/" + batch7Lane2TestZoneID + "/purge_cache"},
	"cloudflare_list_advanced_services": {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/workers/scripts"},
	"cloudflare_list_accounts":          {http.MethodGet, "/accounts"},
	"cloudflare_get_account_details":    {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID},
	"cloudflare_list_account_members":   {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID + "/members"},
	"cloudflare_invite_account_member":  {http.MethodPost, "/accounts/" + batch7Lane2TestAccountID + "/members"},
	"cloudflare_delete_account_member":  {http.MethodDelete, "/accounts/" + batch7Lane2TestAccountID + "/members/" + batch7Lane2TestMemberID},
	"cloudflare_list_custom_hostnames":  {http.MethodGet, "/zones/" + batch7Lane2TestZoneID + "/custom_hostnames"},
	"cloudflare_list_audit_logs":        {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID + "/audit_logs"},
	"cloudflare_list_firewall_rules": {
		http.MethodGet,
		"/zones/" + batch7Lane2TestZoneID + "/rulesets/phases/http_request_firewall_custom/entrypoint",
	},
	"cloudflare_list_access_applications": {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID + "/access/apps"},
	"cloudflare_list_r2_buckets":          {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID + "/r2/buckets"},
	"cloudflare_list_pages_projects":      {http.MethodGet, "/accounts/" + batch7Lane2TestAccountID + "/pages/projects"},
	"cloudflare_create_access_policy": {
		http.MethodPost,
		"/accounts/" + batch7Lane2TestAccountID + "/access/apps/" + batch7Lane2TestAppID + "/policies",
	},
	"cloudflare_create_worker_route": {http.MethodPost, "/zones/" + batch7Lane2TestZoneID + "/workers/routes"},
	"cloudflare_set_ssl_mode":        {http.MethodPatch, "/zones/" + batch7Lane2TestZoneID + "/settings/ssl"},
}

// batch7Lane2ProbeValues supplies a legal value for every required parameter.
var batch7Lane2ProbeValues = map[string]any{
	"zone_id":        batch7Lane2TestZoneID,
	"account_id":     batch7Lane2TestAccountID,
	"record_id":      batch7Lane2TestRecordID,
	"setting_id":     "ssl",
	"rule_id":        batch7Lane2TestRuleID,
	"member_id":      batch7Lane2TestMemberID,
	"application_id": batch7Lane2TestAppID,
	"domain":         "example.com",
	"type":           "A",
	"name":           "www.example.com",
	"content":        "192.0.2.10",
	"rule_action":    "block",
	"expression":     "ip.src eq 192.0.2.10",
	"urls":           []any{"https://example.com/app.css"},
	"email":          "ops@example.com",
	"roles":          []any{"admin"},
	"mode":           "full",
	"pattern":        "api.example.com/*",
	"script_name":    "edge",
	"decision":       "allow",
	"include":        []any{},
	"value":          "on",
}

func batch7Lane2ProbeInput(t *testing.T, action Action) map[string]any {
	t.Helper()
	input := map[string]any{"action": action.Name}
	for _, name := range action.Required {
		value, ok := batch7Lane2ProbeValues[name]
		if !ok {
			t.Fatalf("%s: no probe value for required parameter %q", action.Name, name)
		}
		input[name] = value
	}
	return input
}

// ---------------------------------------------------------------- catalog ---

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareIntegration::test_spec_tools_registered
func TestBatch7Lane2Catalog(t *testing.T) {
	registry := batch7Lane2NewRegistry()
	if names := registry.Names(); len(names) != 1 || names[0] != "cloudflare" {
		t.Fatalf("names = %v", names)
	}
	if got := registry.ActionCount(); got != 54 {
		t.Fatalf("actions = %d, want 54", got)
	}
	if got := registry.ImplementedActions(); got != 54 {
		t.Fatalf("implemented = %d, want 54", got)
	}
	tool, ok := registry.Lookup("cloudflare")
	if !ok {
		t.Fatal("cloudflare tool missing")
	}
	if tool.ID != batch7Lane2ModuleID || tool.DisplayName == "" || tool.Summary == "" {
		t.Fatalf("tool identity = %+v", tool)
	}
	if len(tool.Actions) != len(batch7Lane2ExpectedRoutes) {
		t.Fatalf("catalog = %d, expected routes = %d", len(tool.Actions), len(batch7Lane2ExpectedRoutes))
	}
	seen := map[string]bool{}
	for _, action := range tool.Actions {
		if seen[action.Name] {
			t.Fatalf("duplicate action %q", action.Name)
		}
		seen[action.Name] = true
		if action.Source == "" {
			t.Fatalf("%s missing source", action.Name)
		}
		if registry.impls["cloudflare"][action.Name] == nil {
			t.Fatalf("%s has no implementation", action.Name)
		}
		if _, present := batch7Lane2ExpectedRoutes[action.Name]; !present {
			t.Fatalf("%s missing an expected route", action.Name)
		}
	}
	if got := strings.Count(tool.Description(), "\n- "); got != 54 {
		t.Fatalf("description lists %d actions, want 54", got)
	}
}

// Parity-exempt: table-driven sweep over all 54 upstream @mcp.tool functions;
// upstream exercises them one test per action, mirrored by the per-action
// cases below.
func TestBatch7Lane2AllActions(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{},
		})
	})
	tool, ok := registry.Lookup("cloudflare")
	if !ok {
		t.Fatal("cloudflare tool missing")
	}
	for _, action := range tool.Actions {
		action := action
		t.Run(action.Name, func(t *testing.T) {
			recorder.reset()
			input := batch7Lane2ProbeInput(t, action)
			result := batch7Lane2Execute(t, registry, input)
			if message, present := result["error"]; present {
				t.Fatalf("%s returned error: %v", action.Name, message)
			}
			expected, present := batch7Lane2ExpectedRoutes[action.Name]
			if !present {
				t.Fatalf("%s missing an expected route", action.Name)
			}
			request := recorder.first(t)
			if request.Method != expected.Method || request.Path != expected.Path {
				t.Fatalf(
					"request = %s %s, want %s %s",
					request.Method, request.Path, expected.Method, expected.Path,
				)
			}
			if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
		})
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_list_zones
func TestBatch7Lane2ListZones(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"result":      []any{batch7Lane2ZoneFixture()},
			"result_info": map[string]any{"total_count": 7, "page": 2, "per_page": 50},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_list_zones", "page": 2, "per_page": 50,
	})
	request := recorder.first(t)
	if request.Path != "/zones" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("page"); got != "2" {
		t.Fatalf("page = %q", got)
	}
	if got := request.Query.Get("per_page"); got != "50" {
		t.Fatalf("per_page = %q", got)
	}
	batch7Lane2EqualInt(t, result["total"], 7)
	batch7Lane2EqualInt(t, result["page"], 2)
	zones := batch7Lane2FieldSlice(t, result, "zones")
	zone := zones[0].(map[string]any)
	if zone["name"] != "example.com" || zone["status"] != "active" || zone["plan"] != "Pro" {
		t.Fatalf("zone = %v", zone)
	}
	if servers := zone["name_servers"].([]string); len(servers) != 2 {
		t.Fatalf("name_servers = %v", servers)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_get_zone
func TestBatch7Lane2GetZone(t *testing.T) {
	registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  batch7Lane2ZoneFixture(),
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_zone", "zone_id": batch7Lane2TestZoneID,
	})
	if result["id"] != batch7Lane2TestZoneID || result["type"] != "full" {
		t.Fatalf("result = %v", result)
	}
	if result["plan"] != "Pro" || result["created_on"] != "2024-01-01T00:00:00Z" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_list_dns_records
func TestBatch7Lane2ListDNSRecords(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"result":      []any{batch7Lane2RecordFixture()},
			"result_info": map[string]any{"total_count": 3},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_list_dns_records", "zone_id": batch7Lane2TestZoneID,
		"name": "www.example.com", "type": "a", "per_page": 250,
	})
	request := recorder.first(t)
	if got := request.Query.Get("type"); got != "A" {
		t.Fatalf("type = %q", got)
	}
	if got := request.Query.Get("name"); got != "www.example.com" {
		t.Fatalf("name = %q", got)
	}
	if got := request.Query.Get("per_page"); got != "100" {
		t.Fatalf("per_page = %q, want clamp to 100", got)
	}
	batch7Lane2EqualInt(t, result["total"], 3)
	if result["zone_id"] != batch7Lane2TestZoneID {
		t.Fatalf("zone_id = %v", result["zone_id"])
	}
	record := batch7Lane2FieldSlice(t, result, "records")[0].(map[string]any)
	if record["content"] != "192.0.2.10" || record["proxied"] != true {
		t.Fatalf("record = %v", record)
	}
	batch7Lane2EqualInt(t, record["ttl"], 300)
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_get_dns_record
func TestBatch7Lane2GetDNSRecord(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  batch7Lane2RecordFixture(),
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action":  "cloudflare_get_dns_record",
		"zone_id": batch7Lane2TestZoneID, "record_id": batch7Lane2TestRecordID,
	})
	if recorder.first(t).Path != "/zones/"+batch7Lane2TestZoneID+"/dns_records/"+batch7Lane2TestRecordID {
		t.Fatalf("path = %s", recorder.first(t).Path)
	}
	if result["created_on"] != "2024-01-02T00:00:00Z" {
		t.Fatalf("result = %v", result)
	}
	if result["type"] != "A" || result["name"] != "www.example.com" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_create_dns_record
func TestBatch7Lane2CreateDNSRecord(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "rec9", "type": "A"},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_create_dns_record", "zone_id": batch7Lane2TestZoneID,
		"type": "a", "name": "www.example.com", "content": "192.0.2.10",
		"ttl": 300, "proxied": true, "priority": 10,
	})
	request := recorder.first(t)
	if request.Method != http.MethodPost || request.Path != "/zones/"+batch7Lane2TestZoneID+"/dns_records" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["type"] != "A" {
		t.Fatalf("type = %v", request.Body["type"])
	}
	if request.Body["proxied"] != true {
		t.Fatalf("proxied = %v", request.Body["proxied"])
	}
	batch7Lane2EqualInt(t, request.Body["ttl"], 300)
	batch7Lane2EqualInt(t, request.Body["priority"], 10)
	created := batch7Lane2FieldMap(t, result, "created_record")
	if created["id"] != "rec9" {
		t.Fatalf("created_record = %v", created)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_update_dns_record
func TestBatch7Lane2UpdateDNSRecord(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": batch7Lane2TestRecordID},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_update_dns_record", "zone_id": batch7Lane2TestZoneID,
		"record_id": batch7Lane2TestRecordID, "content": "192.0.2.11",
	})
	request := recorder.first(t)
	if request.Method != http.MethodPatch {
		t.Fatalf("method = %s", request.Method)
	}
	if len(request.Body) != 1 || request.Body["content"] != "192.0.2.11" {
		t.Fatalf("body = %v", request.Body)
	}
	if _, present := result["updated_record"]; !present {
		t.Fatalf("result = %v", result)
	}

	recorder.reset()
	_ = batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_update_dns_record", "zone_id": batch7Lane2TestZoneID,
		"record_id": batch7Lane2TestRecordID, "proxied": false,
	})
	body := recorder.first(t).Body
	if value, present := body["proxied"]; !present || value != false {
		t.Fatalf("proxied body = %v", body)
	}
	if len(body) != 1 {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_update_zone_setting
func TestBatch7Lane2UpdateZoneSetting(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "ssl", "value": "on"},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_update_zone_setting", "zone_id": batch7Lane2TestZoneID,
		"setting_id": "ssl", "value": "on",
	})
	request := recorder.first(t)
	if request.Method != http.MethodPatch || request.Path != "/zones/"+batch7Lane2TestZoneID+"/settings/ssl" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["value"] != "on" {
		t.Fatalf("body = %v", request.Body)
	}
	if got := batch7Lane2FieldMap(t, result, "updated_setting")["value"]; got != "on" {
		t.Fatalf("updated_setting = %v", result["updated_setting"])
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_get_zone_analytics
func TestBatch7Lane2Analytics(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  batch7Lane2DashboardFixture(),
		})
	}
	t.Run("zone", func(t *testing.T) {
		registry, _ := batch7Lane2TestRegistry(t, handler)
		result := batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_get_zone_analytics", "zone_id": batch7Lane2TestZoneID,
		})
		batch7Lane2EqualInt(t, result["requests"], 1000)
		batch7Lane2EqualInt(t, result["bandwidth_bytes"], 2048)
		batch7Lane2EqualInt(t, result["threats"], 7)
		batch7Lane2EqualInt(t, result["uniques"], 300)
		if result["since"] != "2024-01-01T00:00:00Z" {
			t.Fatalf("since = %v", result["since"])
		}
	})
	t.Run("top", func(t *testing.T) {
		registry, _ := batch7Lane2TestRegistry(t, handler)
		result := batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_get_top_analytics", "zone_id": batch7Lane2TestZoneID,
		})
		countries := batch7Lane2FieldMap(t, result, "top_countries")
		batch7Lane2EqualInt(t, countries["US"], 500)
		statuses := batch7Lane2FieldMap(t, result, "top_http_status")
		batch7Lane2EqualInt(t, statuses["200"], 900)
	})
	t.Run("performance", func(t *testing.T) {
		registry, _ := batch7Lane2TestRegistry(t, handler)
		result := batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_get_performance_analytics", "zone_id": batch7Lane2TestZoneID,
		})
		metrics := batch7Lane2FieldMap(t, result, "performance_metrics")
		batch7Lane2EqualInt(t, metrics["ttfb"], 120)
	})
	t.Run("http-report", func(t *testing.T) {
		registry, _ := batch7Lane2TestRegistry(t, handler)
		result := batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_get_http_analytics_report", "zone_id": batch7Lane2TestZoneID,
		})
		codes := batch7Lane2FieldMap(t, result, "status_codes")
		batch7Lane2EqualInt(t, codes["404"], 100)
		threats := batch7Lane2FieldMap(t, result, "threats")
		batch7Lane2EqualInt(t, threats["all"], 3)
	})
}

// Parity-exempt: upstream dashboard tests never return the analytics payload
// as a one-element array; the Go port keeps that upstream branch covered.
func TestBatch7Lane2AnalyticsArrayPayload(t *testing.T) {
	registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  []any{batch7Lane2DashboardFixture()},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_zone_analytics", "zone_id": batch7Lane2TestZoneID,
	})
	batch7Lane2EqualInt(t, result["requests"], 1000)
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_get_security_settings
func TestBatch7Lane2SecuritySettings(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		values := map[string]string{
			"/zones/" + batch7Lane2TestZoneID + "/settings/security_level": "high",
			"/zones/" + batch7Lane2TestZoneID + "/settings/ssl":            "full",
			"/zones/" + batch7Lane2TestZoneID + "/settings/waf":            "on",
		}
		value, present := values[r.URL.Path]
		if !present {
			t.Errorf("unexpected path %s", r.URL.Path)
			value = "off"
		}
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "setting", "value": value},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_security_settings", "zone_id": batch7Lane2TestZoneID,
	})
	if result["security_level"] != "high" || result["ssl_mode"] != "full" {
		t.Fatalf("result = %v", result)
	}
	if result["waf_enabled"] != true {
		t.Fatalf("waf_enabled = %v", result["waf_enabled"])
	}
	if recorder.count() != 3 {
		t.Fatalf("requests = %d, want 3", recorder.count())
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_create_firewall_rule
func TestBatch7Lane2CreateFirewallRuleFallback(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			batch7Lane2WriteJSON(w, http.StatusNotFound, map[string]any{
				"success": false,
				"errors":  []any{map[string]any{"message": "entrypoint not found"}},
			})
			return
		}
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "ruleset1"},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_create_firewall_rule", "zone_id": batch7Lane2TestZoneID,
		"rule_action": "BLOCK", "expression": "ip.src eq 192.0.2.10",
	})
	entrypoint := "/zones/" + batch7Lane2TestZoneID + "/rulesets/phases/http_request_firewall_custom/entrypoint"
	post := recorder.at(t, 0)
	if post.Method != http.MethodPost || post.Path != entrypoint+"/rules" {
		t.Fatalf("post = %s %s", post.Method, post.Path)
	}
	if post.Body["action"] != "block" || post.Body["description"] != "Rule created via MCP" {
		t.Fatalf("post body = %v", post.Body)
	}
	if post.Body["enabled"] != true {
		t.Fatalf("post body = %v", post.Body)
	}
	put := recorder.at(t, 1)
	if put.Method != http.MethodPut || put.Path != entrypoint {
		t.Fatalf("fallback = %s %s", put.Method, put.Path)
	}
	rules := batch7Lane2FieldSlice(t, put.Body, "rules")
	if len(rules) != 1 {
		t.Fatalf("rules = %v", rules)
	}
	if rules[0].(map[string]any)["expression"] != "ip.src eq 192.0.2.10" {
		t.Fatalf("rules = %v", rules)
	}
	if got := batch7Lane2FieldMap(t, result, "created_rules")["id"]; got != "ruleset1" {
		t.Fatalf("created_rules = %v", result["created_rules"])
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_delete_firewall_rule
func TestBatch7Lane2DeleteFirewallRule(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": batch7Lane2TestRuleID},
		})
	})
	batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_delete_firewall_rule", "zone_id": batch7Lane2TestZoneID,
		"rule_id": batch7Lane2TestRuleID,
	})
	request := recorder.first(t)
	if request.Method != http.MethodDelete {
		t.Fatalf("method = %s", request.Method)
	}
	if !strings.HasSuffix(request.Path, "/entrypoint/rules/"+batch7Lane2TestRuleID) {
		t.Fatalf("path = %s", request.Path)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_check_domain_dns_health
func TestBatch7Lane2DomainHealth(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zones" {
			batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"result": []any{map[string]any{
					"id": batch7Lane2TestZoneID, "name": "example.com", "status": "active",
				}},
			})
			return
		}
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result": []any{
				map[string]any{"id": "r1", "type": "A", "name": "example.com", "content": "192.0.2.10", "proxied": true},
				map[string]any{"id": "r2", "type": "CNAME", "name": "www.example.com", "content": "example.com", "proxied": true},
				map[string]any{"id": "r3", "type": "NS", "name": "example.com", "content": "ada.ns.cloudflare.com"},
				map[string]any{"id": "r4", "type": "TXT", "name": "example.com", "content": "v=spf1 -all"},
			},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_check_domain_dns_health", "domain": "Example.COM",
	})
	zonesRequest := recorder.at(t, 0)
	if zonesRequest.Path != "/zones" || zonesRequest.Query.Get("name") != "example.com" {
		t.Fatalf("zones request = %s %v", zonesRequest.Path, zonesRequest.Query)
	}
	if got := zonesRequest.Query.Get("per_page"); got != "1" {
		t.Fatalf("per_page = %q", got)
	}
	recordsRequest := recorder.at(t, 1)
	if recordsRequest.Path != "/zones/"+batch7Lane2TestZoneID+"/dns_records" {
		t.Fatalf("records path = %s", recordsRequest.Path)
	}
	if got := recordsRequest.Query.Get("per_page"); got != "100" {
		t.Fatalf("per_page = %q", got)
	}
	if result["zone_found"] != true || result["zone_id"] != batch7Lane2TestZoneID {
		t.Fatalf("result = %v", result)
	}
	batch7Lane2EqualInt(t, result["total_records"], 4)
	if got := len(batch7Lane2FieldSlice(t, result, "root_records")); got != 1 {
		t.Fatalf("root_records = %d", got)
	}
	if got := len(batch7Lane2FieldSlice(t, result, "ns_records")); got != 1 {
		t.Fatalf("ns_records = %d", got)
	}
	issues := batch7Lane2FieldSlice(t, result, "issues")
	if len(issues) != 1 {
		t.Fatalf("issues = %v", issues)
	}
	if issues[0].(map[string]any)["code"] != "MX_MISSING" {
		t.Fatalf("issues = %v", issues)
	}
	if got := result["summary"]; got != "Zone is active. Found 4 DNS records. Issues detected: MX_MISSING." {
		t.Fatalf("summary = %v", got)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_purge_cache_all
func TestBatch7Lane2PurgeCache(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "purge1"},
		})
	}
	t.Run("everything", func(t *testing.T) {
		registry, recorder := batch7Lane2TestRegistry(t, handler)
		batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_purge_cache_all", "zone_id": batch7Lane2TestZoneID,
		})
		if recorder.first(t).Body["purge_everything"] != true {
			t.Fatalf("body = %v", recorder.first(t).Body)
		}
	})
	t.Run("files", func(t *testing.T) {
		registry, recorder := batch7Lane2TestRegistry(t, handler)
		batch7Lane2Execute(t, registry, map[string]any{
			"action": "cloudflare_purge_cache_files", "zone_id": batch7Lane2TestZoneID,
			"urls": []any{"https://example.com/app.css"},
		})
		files := batch7Lane2FieldSlice(t, recorder.first(t).Body, "files")
		if len(files) != 1 || files[0] != "https://example.com/app.css" {
			t.Fatalf("files = %v", files)
		}
	})
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_list_accounts
func TestBatch7Lane2ListAccounts(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success":     true,
			"result":      []any{map[string]any{"id": "a1", "name": "Account 1", "status": "active"}},
			"result_info": map[string]any{"total_count": 50},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_list_accounts", "page": 1, "per_page": 20,
	})
	request := recorder.first(t)
	if request.Path != "/accounts" || request.Query.Get("per_page") != "20" {
		t.Fatalf("request = %s %v", request.Path, request.Query)
	}
	batch7Lane2EqualInt(t, result["total"], 50)
	accounts := batch7Lane2FieldSlice(t, result, "accounts")
	if accounts[0].(map[string]any)["name"] != "Account 1" {
		t.Fatalf("accounts = %v", accounts)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareEdgeCases::test_api_error_responses
func TestBatch7Lane2ErrorMapping(t *testing.T) {
	cases := []struct {
		status  int
		message string
	}{
		{http.StatusUnauthorized, "Unauthorized - invalid or missing CLOUDFLARE_API_TOKEN"},
		{http.StatusForbidden, "Forbidden - token lacks required permissions (Zone/DNS read or edit)"},
		{http.StatusNotFound, "Not found"},
		{http.StatusTooManyRequests, "Too many requests - rate limited"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(fmt.Sprint(testCase.status), func(t *testing.T) {
			registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "60")
				batch7Lane2WriteJSON(w, testCase.status, map[string]any{
					"success": false,
					"errors":  []any{map[string]any{"message": "provider failure"}},
				})
			})
			result := batch7Lane2Execute(t, registry, map[string]any{
				"action": "cloudflare_list_zones",
			})
			if got := batch7Lane2ErrorText(t, result); got != testCase.message {
				t.Fatalf("error = %q, want %q", got, testCase.message)
			}
			batch7Lane2EqualInt(t, result["status_code"], testCase.status)
			if testCase.status == http.StatusTooManyRequests {
				if result["retry_after"] != "60" {
					t.Fatalf("retry_after = %v", result["retry_after"])
				}
			}
		})
	}
}

// Parity-exempt: upstream only asserts the 401/403/404/429 branches never
// exercise a generic 5xx; the Go port covers the HTTP-N error shape.
func TestBatch7Lane2ProviderError(t *testing.T) {
	registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusInternalServerError, map[string]any{
			"success": false,
			"errors":  []any{map[string]any{"message": "something broke"}},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{"action": "cloudflare_list_zones"})
	if got := batch7Lane2ErrorText(t, result); got != "HTTP 500: something broke" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream wraps provider errors under the response key for
// actions without an explicit `if "error" in result` guard, and collapses
// setting reads to None. The Go port mirrors that split.
func TestBatch7Lane2WrappedProviderErrors(t *testing.T) {
	registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusForbidden, map[string]any{
			"success": false,
			"errors":  []any{map[string]any{"message": "denied"}},
		})
	})
	const forbidden = "Forbidden - token lacks required permissions (Zone/DNS read or edit)"

	purge := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_purge_cache_all", "zone_id": batch7Lane2TestZoneID,
	})
	if _, present := purge["error"]; present {
		t.Fatalf("purge surfaced a top-level error: %v", purge)
	}
	purgeStatus := batch7Lane2FieldMap(t, purge, "purge_status")
	if purgeStatus["error"] != forbidden {
		t.Fatalf("purge_status = %v", purgeStatus)
	}
	batch7Lane2EqualInt(t, purgeStatus["status_code"], http.StatusForbidden)

	created := batch7Lane2FieldMap(t, batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_create_dns_record", "zone_id": batch7Lane2TestZoneID,
		"type": "A", "name": "www.example.com", "content": "192.0.2.10",
	}), "created_record")
	if created["error"] != forbidden {
		t.Fatalf("created_record = %v", created)
	}

	settings := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_speed_settings", "zone_id": batch7Lane2TestZoneID,
	})
	for _, key := range []string{"minify", "brotli", "rocket_loader"} {
		if value, present := settings[key]; !present || value != nil {
			t.Fatalf("%s = %v, want nil", key, value)
		}
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareEdgeCases::test_missing_or_invalid_token
func TestBatch7Lane2MissingToken(t *testing.T) {
	registry := batch7Lane2NewRegistry(WithGetenv(func(string) string { return "" }))
	result := batch7Lane2Execute(t, registry, map[string]any{"action": "cloudflare_list_zones"})
	if got := batch7Lane2ErrorText(t, result); got != "CLOUDFLARE_API_TOKEN is required" {
		t.Fatalf("error = %q", got)
	}
	if help := batch7Lane2Text(t, result, "help"); !strings.Contains(help, "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("help = %q", help)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareEdgeCases::test_invalid_zone_id_format
func TestBatch7Lane2ZoneValidation(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request")
		w.WriteHeader(http.StatusInternalServerError)
	})
	empty := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_zone", "zone_id": "",
	})
	if got := batch7Lane2ErrorText(t, empty); got != "zone_id must be a non-empty string" {
		t.Fatalf("error = %q", got)
	}
	invalid := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_get_zone", "zone_id": "invalid-length-and-chars-here",
	})
	if got := batch7Lane2ErrorText(t, invalid); got != "Invalid zone_id format: invalid-length-and-chars-here" {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 0 {
		t.Fatalf("validation issued %d requests", recorder.count())
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareEdgeCases::test_invalid_domain_format
func TestBatch7Lane2DomainValidation(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request")
		w.WriteHeader(http.StatusInternalServerError)
	})
	long := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_check_domain_dns_health", "domain": strings.Repeat("a", 260),
	})
	if got := batch7Lane2ErrorText(t, long); !strings.HasPrefix(got, "Domain length must be 3-255 characters") {
		t.Fatalf("error = %q", got)
	}
	chars := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_check_domain_dns_health", "domain": "ex*mple.com",
	})
	if got := batch7Lane2ErrorText(t, chars); got != "Invalid domain format: ex*mple.com" {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 0 {
		t.Fatalf("validation issued %d requests", recorder.count())
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_list_zone_certificates
func TestBatch7Lane2ListZoneCertificates(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/custom_certificates") {
			batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"success": true,
				"result":  []any{map[string]any{"id": "cert1", "hosts": []any{"example.com"}}},
			})
			return
		}
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"enabled": true, "certificate_authority": "digicert"},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_list_zone_certificates", "zone_id": batch7Lane2TestZoneID,
	})
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
	universal := batch7Lane2FieldMap(t, result, "universal_ssl")
	if universal["enabled"] != true {
		t.Fatalf("universal_ssl = %v", universal)
	}
	custom := batch7Lane2FieldSlice(t, result, "custom_certificates")
	if len(custom) != 1 {
		t.Fatalf("custom_certificates = %v", custom)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_list_firewall_rules
func TestBatch7Lane2ListFirewallRules(t *testing.T) {
	registry, _ := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result": map[string]any{
				"id": "entrypoint1",
				"rules": []any{map[string]any{
					"id": "rule1", "action": "block", "expression": "ip.src eq 192.0.2.10",
				}},
			},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_list_firewall_rules", "zone_id": batch7Lane2TestZoneID,
	})
	rules := batch7Lane2FieldSlice(t, result, "firewall_rules")
	if len(rules) != 1 || rules[0].(map[string]any)["action"] != "block" {
		t.Fatalf("firewall_rules = %v", rules)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_create_access_policy
func TestBatch7Lane2CreateAccessPolicy(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "policy1", "decision": "allow"},
		})
	})
	result := batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_create_access_policy", "account_id": batch7Lane2TestAccountID,
		"application_id": batch7Lane2TestAppID, "name": "p1", "decision": "allow",
		"include": []any{map[string]any{"email": map[string]any{"email": "ops@example.com"}}},
	})
	request := recorder.first(t)
	if request.Method != http.MethodPost {
		t.Fatalf("method = %s", request.Method)
	}
	if request.Body["name"] != "p1" || request.Body["decision"] != "allow" {
		t.Fatalf("body = %v", request.Body)
	}
	include := batch7Lane2FieldSlice(t, request.Body, "include")
	if len(include) != 1 {
		t.Fatalf("include = %v", include)
	}
	if got := batch7Lane2FieldMap(t, result, "policy")["id"]; got != "policy1" {
		t.Fatalf("policy = %v", result["policy"])
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_create_worker_route
func TestBatch7Lane2CreateWorkerRoute(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "route1"},
		})
	})
	batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_create_worker_route", "zone_id": batch7Lane2TestZoneID,
		"pattern": "api.example.com/*", "script_name": "edge",
	})
	body := recorder.first(t).Body
	if body["pattern"] != "api.example.com/*" || body["script"] != "edge" {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_invite_account_member
func TestBatch7Lane2InviteAccountMember(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "invitation1"},
		})
	})
	batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_invite_account_member", "account_id": batch7Lane2TestAccountID,
		"email": "ops@example.com", "roles": []any{"admin"},
	})
	body := recorder.first(t).Body
	if body["email"] != "ops@example.com" {
		t.Fatalf("body = %v", body)
	}
	roles := batch7Lane2FieldSlice(t, body, "roles")
	if len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("roles = %v", roles)
	}
}

// Parity: tools/tests/tools/test_cloudflare.py::TestCloudflareTools::test_cloudflare_set_ssl_mode
func TestBatch7Lane2SetSSLMode(t *testing.T) {
	registry, recorder := batch7Lane2TestRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
		batch7Lane2WriteJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"result":  map[string]any{"id": "ssl", "value": "full"},
		})
	})
	batch7Lane2Execute(t, registry, map[string]any{
		"action": "cloudflare_set_ssl_mode", "zone_id": batch7Lane2TestZoneID, "mode": "full",
	})
	request := recorder.first(t)
	if request.Method != http.MethodPatch || request.Path != "/zones/"+batch7Lane2TestZoneID+"/settings/ssl" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["value"] != "full" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch7Lane2(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch7Lane2(shared); installed != 1 {
		t.Fatalf("installed = %d, want 1", installed)
	}
	if !shared.HasTool("cloudflare") {
		t.Fatal("shared registry missing cloudflare")
	}
	tool, ok := shared.GetTool("cloudflare")
	if !ok {
		t.Fatal("cloudflare tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("missing action selector: %v", properties)
	}
	enum, _ := action["enum"].([]string)
	if len(enum) != 54 {
		t.Fatalf("action enum = %d entries, want 54", len(enum))
	}
	if tool.Description == "" {
		t.Fatal("empty description")
	}
}

// Parity-exempt: unknown-action dispatch is a Go-side registry contract.
func TestBatch7Lane2UnknownAction(t *testing.T) {
	registry := batch7Lane2NewRegistry()
	_, err := registry.Execute(context.Background(), "cloudflare", map[string]any{"action": "nope"})
	if !errors.Is(err, ErrUnknownAction) {
		t.Fatalf("err = %v, want ErrUnknownAction", err)
	}
}

// Parity-exempt: schema shape is a Go-side contract with tools.ToolRegistry.
func TestBatch7Lane2SchemaShape(t *testing.T) {
	registry := batch7Lane2NewRegistry()
	tool, _ := registry.Lookup("cloudflare")
	schema := tool.Schema()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v", schema["type"])
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing")
	}
	for _, name := range []string{
		"action", "zone_id", "account_id", "record_id", "domain", "urls", "rule_action",
	} {
		if _, present := properties[name]; !present {
			t.Fatalf("property %q missing from schema", name)
		}
	}
	action := properties["action"].(map[string]any)
	if enum, _ := action["enum"].([]string); len(enum) != 54 {
		t.Fatalf("action enum = %v", action["enum"])
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "action" {
		t.Fatalf("required = %v", required)
	}
}

// batch7Lane2Text reads a string field from a result map.
func batch7Lane2Text(t *testing.T, source map[string]any, name string) string {
	t.Helper()
	value, ok := source[name].(string)
	if !ok {
		t.Fatalf("field %q = %T, want string", name, source[name])
	}
	return value
}
