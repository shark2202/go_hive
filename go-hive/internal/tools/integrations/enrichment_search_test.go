package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/enrichment"
)

// --------------------------------------------------------------- test kit ---

type batch7Lane5Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch7Lane5Recorder struct {
	mu       sync.Mutex
	requests []batch7Lane5Request
}

func (rec *batch7Lane5Recorder) wrap(
	next func(http.ResponseWriter, *http.Request, map[string]any),
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch7Lane5Request{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.Query(),
			Header: r.Header.Clone(),
			Body:   body,
		})
		rec.mu.Unlock()
		next(w, r, body)
	}
}

func (rec *batch7Lane5Recorder) at(t *testing.T, index int) batch7Lane5Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch7Lane5Recorder) last(t *testing.T) batch7Lane5Request {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch7Lane5Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch7Lane5TestRegistry builds a lane-5 registry pointed at one fake
// upstream with a fixed credential.
func batch7Lane5TestRegistry(
	t *testing.T,
	module string,
	env map[string]string,
	handler func(http.ResponseWriter, *http.Request, map[string]any),
) (*Registry, *batch7Lane5Recorder) {
	t.Helper()
	recorder := &batch7Lane5Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	return batch7Lane5NewRegistry(
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
		WithGetenv(func(name string) string { return env[name] }),
	), recorder
}

// batch7Lane5TestRegistryNoAuth builds a registry with no credential at all.
func batch7Lane5TestRegistryNoAuth(t *testing.T, module string) *Registry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected request without credentials")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return batch7Lane5NewRegistry(
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(string) string { return "" }),
	)
}

func batch7Lane5Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

func batch7Lane5WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch7Lane5ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch7Lane5FieldMap(t *testing.T, source map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := source[name].(map[string]any)
	if !ok {
		t.Fatalf("field %q = %T, want map (%v)", name, source[name], source)
	}
	return value
}

func batch7Lane5FieldSlice(t *testing.T, source map[string]any, name string) []any {
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

func batch7Lane5Number(t *testing.T, value any) int64 {
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

func batch7Lane5EqualInt(t *testing.T, value any, want int) {
	t.Helper()
	if got := batch7Lane5Number(t, value); got != int64(want) {
		t.Fatalf("value = %d, want %d", got, want)
	}
}

func batch7Lane5StatusHandler(status int) func(http.ResponseWriter, *http.Request, map[string]any) {
	return func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch7Lane5WriteJSON(w, status, map[string]any{"error": "failure"})
	}
}

// ------------------------------------------------------------------ apify ---

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyRunActor::test_successful_run
func TestBatch7Lane5ApifyRunActor(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusCreated, map[string]any{
				"data": map[string]any{
					"id":                     "run-1",
					"status":                 "RUNNING",
					"defaultDatasetId":       "ds-1",
					"defaultKeyValueStoreId": "kv-1",
					"startedAt":              "2024-01-01T00:00:00Z",
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{
		"action":        "apify_run_actor",
		"actor_id":      "apify/web-scraper",
		"input_data":    map[string]any{"url": "https://example.com"},
		"memory_mbytes": 256,
		"timeout_secs":  60,
		"build":         "latest",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/acts/apify/web-scraper/runs" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Query.Get("memory") != "256" || request.Query.Get("timeout") != "60" || request.Query.Get("build") != "latest" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Body["url"] != "https://example.com" {
		t.Fatalf("body = %v", request.Body)
	}
	if result["run_id"] != "run-1" || result["status"] != "RUNNING" || result["dataset_id"] != "ds-1" {
		t.Fatalf("result = %v", result)
	}
	if result["kv_store_id"] != "kv-1" || result["started_at"] != "2024-01-01T00:00:00Z" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyGetRun::test_successful_get
func TestBatch7Lane5ApifyGetRun(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"id":                     "run-1",
					"status":                 "SUCCEEDED",
					"startedAt":              "2024-01-01T00:00:00Z",
					"finishedAt":             "2024-01-01T00:01:00Z",
					"defaultDatasetId":       "ds-1",
					"defaultKeyValueStoreId": "kv-1",
					"usage":                  map[string]any{"ACTOR_COMPUTE_UNITS": 0.005},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{
		"action": "apify_get_run", "actor_id": "apify/web-scraper", "run_id": "run-1",
	})
	if recorder.last(t).Path != "/acts/apify/web-scraper/runs/run-1" {
		t.Fatalf("path = %s", recorder.last(t).Path)
	}
	if result["status"] != "SUCCEEDED" || result["usage_usd"] != 0.005 {
		t.Fatalf("result = %v", result)
	}
	if result["finished_at"] != "2024-01-01T00:01:00Z" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyGetDatasetItems::test_successful_get
func TestBatch7Lane5ApifyGetDatasetItems(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, []any{
				map[string]any{"url": "https://example.com", "title": "Example"},
				map[string]any{"url": "https://test.com", "title": "Test"},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{
		"action": "apify_get_dataset_items", "dataset_id": "ds-1",
	})
	request := recorder.at(t, 0)
	if request.Path != "/datasets/ds-1/items" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Query.Get("limit") != "100" || request.Query.Get("format") != "json" {
		t.Fatalf("query = %v", request.Query)
	}
	batch7Lane5EqualInt(t, result["count"], 2)
	items := batch7Lane5FieldSlice(t, result, "items")
	if items[0].(map[string]any)["url"] != "https://example.com" {
		t.Fatalf("items = %v", items)
	}
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyListActors::test_successful_list
func TestBatch7Lane5ApifyListActors(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"items": []any{
					map[string]any{
						"id": "act-1", "name": "web-scraper", "title": "Web Scraper",
						"description": "Crawls websites", "stats": map[string]any{"totalRuns": 100},
					},
				}},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{"action": "apify_list_actors"})
	batch7Lane5EqualInt(t, result["count"], 1)
	actors := batch7Lane5FieldSlice(t, result, "actors")
	actor := actors[0].(map[string]any)
	if actor["name"] != "web-scraper" || actor["title"] != "Web Scraper" {
		t.Fatalf("actor = %v", actor)
	}
	batch7Lane5EqualInt(t, actor["total_runs"], 100)
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyListRuns::test_successful_list
func TestBatch7Lane5ApifyListRuns(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{"items": []any{
					map[string]any{
						"id": "run-1", "actId": "act-1", "status": "SUCCEEDED",
						"startedAt": "2024-01-01T00:00:00Z", "finishedAt": "2024-01-01T00:01:00Z",
						"defaultDatasetId": "ds-1",
					},
				}},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{"action": "apify_list_runs"})
	if recorder.last(t).Path != "/actor-runs" {
		t.Fatalf("path = %s", recorder.last(t).Path)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	run := batch7Lane5FieldSlice(t, result, "runs")[0].(map[string]any)
	if run["status"] != "SUCCEEDED" || run["actor_id"] != "act-1" || run["dataset_id"] != "ds-1" {
		t.Fatalf("run = %v", run)
	}
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyGetKvStoreRecord::test_successful_get
func TestBatch7Lane5ApifyGetKVStoreRecord(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apify_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{"screenshot": "base64..."})
		},
	)
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{
		"action": "apify_get_kv_store_record", "store_id": "kv-1", "key": "OUTPUT",
	})
	request := recorder.at(t, 0)
	if request.Path != "/key-value-stores/kv-1/records/OUTPUT" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Header.Get("Authorization") != "Bearer test-token" {
		t.Fatalf("Authorization = %q", request.Header.Get("Authorization"))
	}
	if result["key"] != "OUTPUT" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane5FieldMap(t, result, "value")["screenshot"] != "base64..." {
		t.Fatalf("value = %v", result["value"])
	}
}

// Parity: tools/tests/tools/test_apify_tool.py::TestApifyRunActor::test_missing_token
func TestBatch7Lane5ApifyMissingToken(t *testing.T) {
	registry := batch7Lane5TestRegistryNoAuth(t, "apify_tool")
	result := batch7Lane5Execute(t, registry, "apify", map[string]any{
		"action": "apify_run_actor", "actor_id": "apify/web-scraper",
	})
	if got := batch7Lane5ErrorText(t, result); got != "APIFY_API_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream tests mock httpx and never map non-401 statuses;
// the fall-through messages come from apify_tool._get/_post.
func TestBatch7Lane5ApifyErrorStatuses(t *testing.T) {
	cases := []map[string]any{
		{"action": "apify_run_actor", "actor_id": "actor"},
		{"action": "apify_get_run", "actor_id": "actor", "run_id": "run"},
		{"action": "apify_get_dataset_items", "dataset_id": "dataset"},
		{"action": "apify_list_actors"},
		{"action": "apify_list_runs"},
		{"action": "apify_get_kv_store_record", "store_id": "store", "key": "key"},
	}
	statuses := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Unauthorized. Check your APIFY_API_TOKEN."},
		{http.StatusPaymentRequired, "Apify API error 402"},
		{http.StatusTooManyRequests, "Apify API error 429"},
	}
	for _, input := range cases {
		for _, tc := range statuses {
			t.Run(input["action"].(string), func(t *testing.T) {
				registry, _ := batch7Lane5TestRegistry(t, "apify_tool", map[string]string{}, batch7Lane5StatusHandler(tc.status))
				result := batch7Lane5Execute(t, registry, "apify", input)
				got := batch7Lane5ErrorText(t, result)
				want := tc.fragment
				if tc.status == http.StatusUnauthorized &&
					(input["action"] == "apify_get_dataset_items" || input["action"] == "apify_get_kv_store_record") {
					want = "Apify API error 401"
				}
				if !strings.Contains(got, want) {
					t.Fatalf("error = %q, want %q", got, want)
				}
			})
		}
	}
}

// ----------------------------------------------------------------- apollo ---

// Parity: tools/tests/tools/test_apollo_tool.py::TestApolloClient::test_enrich_person_by_email
func TestBatch7Lane5ApolloEnrichPerson(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"person": map[string]any{
					"id": "p1", "first_name": "John", "last_name": "Doe", "name": "John Doe",
					"title": "VP Sales", "email": "john@acme.com", "email_status": "verified",
					"phone_numbers": []any{}, "linkedin_url": "https://linkedin.com/in/john",
					"city": "San Francisco", "state": "CA", "country": "US",
					"organization": map[string]any{
						"id": "o1", "name": "Acme Inc", "primary_domain": "acme.com",
						"industry": "Software", "estimated_num_employees": 500,
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action": "apollo_enrich_person", "email": "john@acme.com",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/people/match" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Header.Get("X-Api-Key") != "test-token" {
		t.Fatalf("X-Api-Key = %q", request.Header.Get("X-Api-Key"))
	}
	if request.Body["email"] != "john@acme.com" || request.Body["reveal_personal_emails"] != false {
		t.Fatalf("body = %v", request.Body)
	}
	if result["match_found"] != true {
		t.Fatalf("result = %v", result)
	}
	person := batch7Lane5FieldMap(t, result, "person")
	if person["first_name"] != "John" || person["title"] != "VP Sales" {
		t.Fatalf("person = %v", person)
	}
	organization := batch7Lane5FieldMap(t, person, "organization")
	if organization["name"] != "Acme Inc" || organization["domain"] != "acme.com" {
		t.Fatalf("organization = %v", organization)
	}
	batch7Lane5EqualInt(t, organization["employee_count"], 500)
}

// Parity: tools/tests/tools/test_apollo_tool.py::TestApolloClient::test_enrich_company
func TestBatch7Lane5ApolloEnrichCompany(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"organization": map[string]any{
					"id": "o1", "name": "OpenAI", "primary_domain": "openai.com",
					"industry": "Artificial Intelligence", "estimated_num_employees": 1500,
					"keywords":     []any{"ai", "research"},
					"technologies": []any{"python", "kubernetes"},
					"founded_year": 2015,
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action": "apollo_enrich_company", "domain": "openai.com",
	})
	if result["match_found"] != true {
		t.Fatalf("result = %v", result)
	}
	organization := batch7Lane5FieldMap(t, result, "organization")
	if organization["name"] != "OpenAI" || organization["industry"] != "Artificial Intelligence" {
		t.Fatalf("organization = %v", organization)
	}
	batch7Lane5EqualInt(t, organization["employee_count"], 1500)
	technologies := batch7Lane5FieldSlice(t, organization, "technologies")
	if technologies[0] != "python" {
		t.Fatalf("technologies = %v", technologies)
	}
}

// Parity: tools/tests/tools/test_apollo_tool.py::TestApolloClient::test_search_people
func TestBatch7Lane5ApolloSearchPeople(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"people": []any{
					map[string]any{
						"id": "p1", "name": "Jane", "title": "VP Sales",
						"email": "jane@acme.com", "seniority": "vp",
						"organization": map[string]any{
							"id": "o1", "name": "Company Inc", "primary_domain": "company.com",
						},
					},
					map[string]any{"id": "p2", "organization": nil},
				},
				"pagination": map[string]any{"total_entries": 150, "page": 1, "per_page": 25},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action":     "apollo_search_people",
		"titles":     []any{"VP Sales"},
		"industries": []any{"technology"},
		"limit":      25,
	})
	request := recorder.at(t, 0)
	if request.Path != "/mixed_people/search" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Body["per_page"] != float64(25) {
		t.Fatalf("body = %v", request.Body)
	}
	if titles, _ := request.Body["person_titles"].([]any); len(titles) != 1 || titles[0] != "VP Sales" {
		t.Fatalf("body = %v", request.Body)
	}
	batch7Lane5EqualInt(t, result["total"], 150)
	results := batch7Lane5FieldSlice(t, result, "results")
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	first := results[0].(map[string]any)
	organization := batch7Lane5FieldMap(t, first, "organization")
	if organization["name"] != "Company Inc" {
		t.Fatalf("organization = %v", organization)
	}
	second := results[1].(map[string]any)
	if secondOrg := batch7Lane5FieldMap(t, second, "organization"); secondOrg["name"] != nil {
		t.Fatalf("organization = %v", secondOrg)
	}
}

// Parity-exempt: upstream test_apollo_tool.py does not cover activities.
func TestBatch7Lane5ApolloGetPersonActivities(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"activities": []any{
					map[string]any{
						"id": "a1", "type": "email", "subject": "Intro",
						"body": strings.Repeat("x", 600), "status": "completed",
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action": "apollo_get_person_activities", "person_id": "p1",
	})
	if recorder.last(t).Query.Get("contact_id") != "p1" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	activity := batch7Lane5FieldSlice(t, result, "activities")[0].(map[string]any)
	if activity["subject"] != "Intro" {
		t.Fatalf("activity = %v", activity)
	}
	if len(activity["body"].(string)) != 500 {
		t.Fatalf("body length = %d", len(activity["body"].(string)))
	}
}

// Parity-exempt: upstream test_apollo_tool.py does not cover email accounts.
func TestBatch7Lane5ApolloListEmailAccounts(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"email_accounts": []any{
					map[string]any{
						"id": "acct-1", "email": "sender@acme.com", "type": "gmail",
						"active": true, "default": true, "sending_daily_limit": 200,
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{"action": "apollo_list_email_accounts"})
	batch7Lane5EqualInt(t, result["count"], 1)
	account := batch7Lane5FieldSlice(t, result, "email_accounts")[0].(map[string]any)
	if account["email"] != "sender@acme.com" || account["active"] != true {
		t.Fatalf("account = %v", account)
	}
}

// Parity-exempt: upstream test_apollo_tool.py does not cover bulk enrichment.
func TestBatch7Lane5ApolloBulkEnrichPeople(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"matches": []any{
					map[string]any{
						"id": "p1", "name": "John Doe", "title": "VP",
						"email": "john@acme.com", "organization": map[string]any{"name": "Acme"},
					},
					nil,
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action":       "apollo_bulk_enrich_people",
		"details_json": `[{"email": "john@acme.com"}]`,
	})
	request := recorder.at(t, 0)
	if request.Path != "/people/bulk_match" {
		t.Fatalf("path = %s", request.Path)
	}
	if details, _ := request.Body["details"].([]any); len(details) != 1 {
		t.Fatalf("body = %v", request.Body)
	}
	batch7Lane5EqualInt(t, result["count"], 2)
	results := batch7Lane5FieldSlice(t, result, "results")
	if results[0].(map[string]any)["organization_name"] != "Acme" {
		t.Fatalf("results = %v", results)
	}
	if results[1].(map[string]any)["match_found"] != false {
		t.Fatalf("results = %v", results)
	}
}

// Parity: tools/tests/tools/test_apollo_tool.py::TestApolloClient::test_search_companies
func TestBatch7Lane5ApolloSearchCompanies(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"organizations": []any{
					map[string]any{
						"id": "o1", "name": "Tech Startup", "primary_domain": "tech.io",
						"industry": "Technology", "estimated_num_employees": 40,
					},
				},
				"pagination": map[string]any{"total_entries": 50, "page": 1, "per_page": 10},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action": "apollo_search_companies", "industries": []any{"technology"},
	})
	batch7Lane5EqualInt(t, result["total"], 50)
	organization := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if organization["name"] != "Tech Startup" || organization["industry"] != "Technology" {
		t.Fatalf("organization = %v", organization)
	}
}

// Parity: tools/tests/tools/test_apollo_tool.py::TestToolRegistration::test_no_credentials_returns_error
func TestBatch7Lane5ApolloMissingCredentials(t *testing.T) {
	registry := batch7Lane5TestRegistryNoAuth(t, "apollo_tool")
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{
		"action": "apollo_enrich_person", "email": "john@acme.com",
	})
	if got := batch7Lane5ErrorText(t, result); got != "Apollo credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_apollo_tool.py::TestEnrichPersonTool::test_enrich_person_requires_email_or_linkedin
func TestBatch7Lane5ApolloEnrichPersonValidation(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "apollo_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
			w.WriteHeader(http.StatusInternalServerError)
		},
	)
	result := batch7Lane5Execute(t, registry, "apollo", map[string]any{"action": "apollo_enrich_person"})
	if got := batch7Lane5ErrorText(t, result); !strings.Contains(got, "Invalid search criteria") {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream test_apollo_tool.py mocks httpx and does not assert
// the ported status table; messages come from _ApolloClient._handle_response.
func TestBatch7Lane5ApolloErrorStatuses(t *testing.T) {
	cases := []map[string]any{
		{"action": "apollo_enrich_person", "email": "john@acme.com"},
		{"action": "apollo_enrich_company", "domain": "acme.com"},
		{"action": "apollo_search_people"},
		{"action": "apollo_get_person_activities", "person_id": "p1"},
		{"action": "apollo_list_email_accounts"},
		{"action": "apollo_bulk_enrich_people", "details_json": `[{"email":"a@b.com"}]`},
		{"action": "apollo_search_companies"},
	}
	statuses := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid Apollo API key"},
		{http.StatusPaymentRequired, "Apollo API error (HTTP 402)"},
		{http.StatusTooManyRequests, "Apollo rate limit exceeded. Try again later."},
	}
	for _, input := range cases {
		for _, tc := range statuses {
			t.Run(input["action"].(string), func(t *testing.T) {
				registry, _ := batch7Lane5TestRegistry(t, "apollo_tool", map[string]string{}, batch7Lane5StatusHandler(tc.status))
				result := batch7Lane5Execute(t, registry, "apollo", input)
				if got := batch7Lane5ErrorText(t, result); !strings.Contains(got, tc.fragment) {
					t.Fatalf("error = %q, want %q", got, tc.fragment)
				}
			})
		}
	}
}

// ------------------------------------------------------------------ lusha ---

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaEnrichPerson::test_successful_enrich_by_name
func TestBatch7Lane5LushaEnrichPerson(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"firstName": "Jane", "lastName": "Doe", "fullName": "Jane Doe",
				"jobTitle": "CTO", "company": "Acme Inc",
				"emailAddresses": []any{map[string]any{"email": "jane@acme.com"}},
				"phoneNumbers":   []any{map[string]any{"phone": "+123"}},
				"linkedinUrl":    "https://linkedin.com/in/janedoe",
				"location":       "San Francisco, CA",
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_enrich_person", "first_name": "Jane", "last_name": "Doe", "company_domain": "acme.com",
	})
	request := recorder.at(t, 0)
	if request.Path != "/v2/person" || request.Header.Get("api_key") != "test-token" {
		t.Fatalf("request = %s %v", request.Path, request.Header)
	}
	if request.Query.Get("firstName") != "Jane" || request.Query.Get("companyDomain") != "acme.com" {
		t.Fatalf("query = %v", request.Query)
	}
	if result["full_name"] != "Jane Doe" || result["job_title"] != "CTO" {
		t.Fatalf("result = %v", result)
	}
	if len(batch7Lane5FieldSlice(t, result, "email_addresses")) != 1 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaEnrichCompany::test_successful_enrich
func TestBatch7Lane5LushaEnrichCompany(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"name": "Acme Inc", "domain": "acme.com", "industry": "Technology",
				"employeeCount": 500, "revenue": "$50M-$100M", "location": "SF",
				"description": "A tech company", "foundedYear": 2015,
				"technologies": []any{"Python", "AWS"},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_enrich_company", "domain": "acme.com",
	})
	if recorder.last(t).Query.Get("domain") != "acme.com" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	if result["name"] != "Acme Inc" {
		t.Fatalf("result = %v", result)
	}
	batch7Lane5EqualInt(t, result["employee_count"], 500)
	if technologies := batch7Lane5FieldSlice(t, result, "technologies"); technologies[0] != "Python" {
		t.Fatalf("technologies = %v", technologies)
	}
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaSearchContacts::test_successful_search
func TestBatch7Lane5LushaSearchContacts(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{
					map[string]any{
						"contactId": "abc-123", "firstName": "John", "lastName": "Smith",
						"jobTitle": "VP Engineering", "seniority": "VP", "department": "Engineering",
						"companyName": "Acme Inc", "companyDomain": "acme.com", "location": "New York",
					},
				},
				"total": 1,
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_search_contacts", "seniorities": "4,5", "company_domains": "acme.com",
	})
	request := recorder.at(t, 0)
	if request.Path != "/prospecting/contact/search" {
		t.Fatalf("path = %s", request.Path)
	}
	filters := batch7Lane5FieldMap(t, request.Body, "filters")
	contacts := batch7Lane5FieldMap(t, filters, "contacts")
	include := batch7Lane5FieldMap(t, contacts, "include")
	if seniorities := batch7Lane5FieldSlice(t, include, "seniorities"); len(seniorities) != 2 {
		t.Fatalf("seniorities = %v", seniorities)
	}
	companies := batch7Lane5FieldMap(t, filters, "companies")
	companyInclude := batch7Lane5FieldMap(t, companies, "include")
	if domains := batch7Lane5FieldSlice(t, companyInclude, "domains"); domains[0] != "acme.com" {
		t.Fatalf("domains = %v", domains)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	batch7Lane5EqualInt(t, result["total"], 1)
	contact := batch7Lane5FieldSlice(t, result, "contacts")[0].(map[string]any)
	if contact["first_name"] != "John" || contact["company_name"] != "Acme Inc" {
		t.Fatalf("contact = %v", contact)
	}
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaSearchCompanies::test_successful_search
func TestBatch7Lane5LushaSearchCompanies(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{
					map[string]any{
						"companyName": "Acme Inc", "companyDomain": "acme.com",
						"industry": "Technology", "employeeCount": 500,
					},
				},
				"total": 1,
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_search_companies", "country": "United States", "min_employees": 50,
	})
	batch7Lane5EqualInt(t, result["count"], 1)
	company := batch7Lane5FieldSlice(t, result, "companies")[0].(map[string]any)
	if company["name"] != "Acme Inc" || company["domain"] != "acme.com" {
		t.Fatalf("company = %v", company)
	}
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaGetUsage::test_successful_get
func TestBatch7Lane5LushaGetUsage(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"credits_used": 150, "credits_remaining": 850, "plan": "Professional",
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{"action": "lusha_get_usage"})
	batch7Lane5EqualInt(t, result["credits_used"], 150)
	if result["plan"] != "Professional" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream test_lusha_tool.py does not cover bulk enrichment.
func TestBatch7Lane5LushaBulkEnrichPersons(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{"firstName": "Jane", "lastName": "Doe", "fullName": "Jane Doe"}},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_bulk_enrich_persons", "details_json": `[{"email": "j@acme.com"}]`,
	})
	if recorder.last(t).Path != "/v2/person/bulk" {
		t.Fatalf("path = %s", recorder.last(t).Path)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	person := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if person["full_name"] != "Jane Doe" {
		t.Fatalf("person = %v", person)
	}
}

// Parity-exempt: upstream test_lusha_tool.py does not cover technologies.
func TestBatch7Lane5LushaGetTechnologies(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"name": "Acme Inc", "technologies": []any{"Python", "AWS"}, "industry": "Technology",
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_get_technologies", "domain": "acme.com",
	})
	if recorder.last(t).Query.Get("domain") != "acme.com" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	if result["company_name"] != "Acme Inc" || result["domain"] != "acme.com" {
		t.Fatalf("result = %v", result)
	}
	if technologies := batch7Lane5FieldSlice(t, result, "technologies"); len(technologies) != 2 {
		t.Fatalf("technologies = %v", technologies)
	}
}

// Parity-exempt: upstream test_lusha_tool.py does not cover decision makers.
func TestBatch7Lane5LushaSearchDecisionMakers(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"data":  []any{map[string]any{"contactId": "c1", "firstName": "Ann", "jobTitle": "CEO"}},
				"total": 1,
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_search_decision_makers", "company_domains": "acme.com,example.com",
	})
	filters := batch7Lane5FieldMap(t, recorder.last(t).Body, "filters")
	include := batch7Lane5FieldMap(t, batch7Lane5FieldMap(t, filters, "contacts"), "include")
	seniorities := batch7Lane5FieldSlice(t, include, "seniorities")
	if len(seniorities) != 4 || seniorities[0] != "4" || seniorities[3] != "7" {
		t.Fatalf("seniorities = %v", seniorities)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaEnrichPerson::test_missing_credentials
func TestBatch7Lane5LushaMissingCredentials(t *testing.T) {
	registry := batch7Lane5TestRegistryNoAuth(t, "lusha_tool")
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{
		"action": "lusha_enrich_person", "first_name": "Jane", "last_name": "Doe",
	})
	if got := batch7Lane5ErrorText(t, result); got != "LUSHA_API_KEY is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_lusha_tool.py::TestLushaSearchContacts::test_missing_filters
func TestBatch7Lane5LushaSearchContactsValidation(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "lusha_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
			w.WriteHeader(http.StatusInternalServerError)
		},
	)
	result := batch7Lane5Execute(t, registry, "lusha", map[string]any{"action": "lusha_search_contacts"})
	if got := batch7Lane5ErrorText(t, result); got != "At least one filter is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream test_lusha_tool.py mocks httpx and does not assert
// the ported "HTTP <status>" mapping from _get/_post.
func TestBatch7Lane5LushaErrorStatuses(t *testing.T) {
	cases := []map[string]any{
		{"action": "lusha_enrich_person", "email": "jane@acme.com"},
		{"action": "lusha_enrich_company", "domain": "acme.com"},
		{"action": "lusha_search_contacts", "country": "US"},
		{"action": "lusha_search_companies", "country": "US"},
		{"action": "lusha_get_usage"},
		{"action": "lusha_bulk_enrich_persons", "details_json": `[{"email":"a@b.com"}]`},
		{"action": "lusha_get_technologies", "domain": "acme.com"},
		{"action": "lusha_search_decision_makers", "company_domains": "acme.com"},
	}
	statuses := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "HTTP 401"},
		{http.StatusPaymentRequired, "HTTP 402"},
		{http.StatusTooManyRequests, "HTTP 429"},
	}
	for _, input := range cases {
		for _, tc := range statuses {
			t.Run(input["action"].(string), func(t *testing.T) {
				registry, _ := batch7Lane5TestRegistry(t, "lusha_tool", map[string]string{}, batch7Lane5StatusHandler(tc.status))
				result := batch7Lane5Execute(t, registry, "lusha", input)
				if got := batch7Lane5ErrorText(t, result); !strings.Contains(got, tc.fragment) {
					t.Fatalf("error = %q, want %q", got, tc.fragment)
				}
			})
		}
	}
}

// ---------------------------------------------------------------- serpapi ---

// Parity: tools/tests/tools/test_serpapi_tool.py::TestScholarSearch::test_successful_search
func TestBatch7Lane5SerpapiScholarSearch(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"search_information": map[string]any{"total_results": 1000},
				"organic_results": []any{
					map[string]any{
						"title": "Deep Learning", "link": "https://example.com/paper",
						"snippet": "A paper", "result_id": "vhbKQo7YFEEJ",
						"publication_info": map[string]any{
							"summary": "Yann LeCun - 2015",
							"authors": []any{
								map[string]any{"name": "Yann LeCun", "author_id": "WLN3QrAAAAAJ"},
							},
						},
						"inline_links": map[string]any{
							"cited_by": map[string]any{"total": 45000, "cites_id": "123456"},
						},
						"resources": []any{map[string]any{"link": "https://example.com/paper.pdf"}},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_search", "query": "deep learning",
		"year_low": 2020, "year_high": 2024, "sort_by_date": true,
	})
	request := recorder.at(t, 0)
	if request.Path != "/search.json" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Query.Get("engine") != "google_scholar" || request.Query.Get("q") != "deep learning" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Query.Get("as_ylo") != "2020" || request.Query.Get("as_yhi") != "2024" || request.Query.Get("scisbd") != "1" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Query.Get("api_key") != "test-token" {
		t.Fatalf("query = %v", request.Query)
	}
	batch7Lane5EqualInt(t, result["total_results"], 1000)
	batch7Lane5EqualInt(t, result["count"], 1)
	paper := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if paper["title"] != "Deep Learning" || paper["cites_id"] != "123456" {
		t.Fatalf("paper = %v", paper)
	}
	batch7Lane5EqualInt(t, paper["cited_by_count"], 45000)
	if authors := batch7Lane5FieldSlice(t, paper, "authors"); authors[0].(map[string]any)["name"] != "Yann LeCun" {
		t.Fatalf("authors = %v", authors)
	}
	if paper["pdf_link"] != "https://example.com/paper.pdf" {
		t.Fatalf("paper = %v", paper)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestScholarCite::test_successful_cite
func TestBatch7Lane5SerpapiScholarCitations(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"citations": []any{map[string]any{"title": "MLA", "snippet": "LeCun, Yann."}},
				"links":     []any{map[string]any{"name": "BibTeX", "link": "https://example.com/bib"}},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_get_citations", "result_id": "vhbKQo7YFEEJ",
	})
	if recorder.last(t).Query.Get("engine") != "google_scholar_cite" || recorder.last(t).Query.Get("q") != "vhbKQo7YFEEJ" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	if result["result_id"] != "vhbKQo7YFEEJ" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane5FieldSlice(t, result, "citations")[0].(map[string]any)["title"] != "MLA" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestScholarAuthor::test_successful_author
func TestBatch7Lane5SerpapiScholarAuthor(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"author": map[string]any{
					"name": "Yann LeCun", "affiliations": "NYU & Meta", "email": "yann@example.com",
					"interests": []any{map[string]any{"title": "Deep Learning"}},
					"thumbnail": "https://example.com/yann.jpg",
				},
				"cited_by": map[string]any{
					"table": []any{
						map[string]any{"h_index": map[string]any{"all": 165, "since_2020": 100}},
						map[string]any{"citations": map[string]any{"all": 300000}},
					},
				},
				"articles": []any{
					map[string]any{
						"title": "Deep Learning Nature", "authors": "LeCun, Bengio, Hinton",
						"publication": "Nature", "year": "2015",
						"cited_by": map[string]any{"value": 45000}, "citation_id": "abc",
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_get_author", "author_id": "WLN3QrAAAAAJ",
	})
	if result["name"] != "Yann LeCun" || result["affiliations"] != "NYU & Meta" {
		t.Fatalf("result = %v", result)
	}
	metrics := batch7Lane5FieldMap(t, result, "metrics")
	if batch7Lane5Number(t, batch7Lane5FieldMap(t, metrics, "h_index")["all"]) != 165 {
		t.Fatalf("metrics = %v", metrics)
	}
	batch7Lane5EqualInt(t, result["article_count"], 1)
	article := batch7Lane5FieldSlice(t, result, "articles")[0].(map[string]any)
	batch7Lane5EqualInt(t, article["cited_by_count"], 45000)
	if result["email"] != "yann@example.com" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestPatentsSearch::test_successful_search
func TestBatch7Lane5SerpapiPatentsSearch(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"search_information": map[string]any{"total_results": 500},
				"organic_results": []any{
					map[string]any{
						"title": "Machine learning model", "patent_id": "patent/US123",
						"publication_number": "US123", "inventor": "John Smith",
						"assignee": "Acme", "filing_date": "2020-01-01",
						"publication_date": "2021-01-01", "priority_date": "2019-01-01",
						"pdf": "https://example.com/patent.pdf",
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "patents_search", "query": "machine learning", "country": "US",
		"status": "GRANT", "before_date": "20240101", "after_date": "20200101",
	})
	request := recorder.at(t, 0)
	if request.Query.Get("engine") != "google_patents" || request.Query.Get("country") != "US" {
		t.Fatalf("query = %v", request.Query)
	}
	if request.Query.Get("before") != "priority:20240101" || request.Query.Get("after") != "priority:20200101" {
		t.Fatalf("query = %v", request.Query)
	}
	batch7Lane5EqualInt(t, result["total_results"], 500)
	patent := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if patent["inventor"] != "John Smith" || patent["grant_date"] != nil {
		t.Fatalf("patent = %v", patent)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestPatentsDetails::test_successful_details
func TestBatch7Lane5SerpapiPatentsDetails(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"organic_results": []any{
					map[string]any{
						"title":    "Machine learning model for prediction",
						"inventor": "John Smith", "assignee": "Acme",
						"classifications": map[string]any{"CPC": []any{"G06N"}},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "patents_get_details", "patent_id": "US20210012345A1",
	})
	if result["patent_id"] != "US20210012345A1" || result["title"] != "Machine learning model for prediction" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane5FieldMap(t, result, "classifications")["CPC"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream test_serpapi_tool.py does not cover cited-by search.
func TestBatch7Lane5SerpapiScholarCitedBy(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"organic_results": []any{
					map[string]any{
						"title": "Citing paper", "result_id": "r2",
						"inline_links": map[string]any{"cited_by": map[string]any{"total": 5}},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_cited_by", "cites_id": "123456",
	})
	if recorder.last(t).Query.Get("cites") != "123456" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	paper := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if paper["title"] != "Citing paper" {
		t.Fatalf("paper = %v", paper)
	}
}

// Parity-exempt: upstream test_serpapi_tool.py does not cover profile search.
func TestBatch7Lane5SerpapiScholarProfiles(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"profiles": []any{
					map[string]any{
						"name": "Geoffrey Hinton", "author_id": "abc",
						"affiliations": "University of Toronto", "cited_by": 1000,
						"interests": []any{map[string]any{"title": "Neural Networks"}},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_search_profiles", "query": "Geoffrey Hinton",
	})
	if recorder.last(t).Query.Get("engine") != "google_scholar_profiles" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	profile := batch7Lane5FieldSlice(t, result, "profiles")[0].(map[string]any)
	if profile["name"] != "Geoffrey Hinton" {
		t.Fatalf("profile = %v", profile)
	}
	batch7Lane5EqualInt(t, profile["cited_by"], 1000)
}

// Parity-exempt: upstream test_serpapi_tool.py does not cover web search.
func TestBatch7Lane5SerpapiGoogleSearch(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"organic_results": []any{
					map[string]any{
						"title": "Example", "link": "https://example.com",
						"snippet": "An example", "displayed_link": "example.com", "position": 1,
					},
				},
				"answer_box":      map[string]any{"type": "answer", "title": "Q", "answer": "A"},
				"knowledge_graph": map[string]any{"title": "Example", "type": "Thing", "description": "Desc"},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "serpapi_google_search", "query": "example", "country": "us",
	})
	if recorder.last(t).Query.Get("gl") != "us" || recorder.last(t).Query.Get("engine") != "google" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	batch7Lane5EqualInt(t, result["count"], 1)
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if hit["displayed_link"] != "example.com" {
		t.Fatalf("hit = %v", hit)
	}
	if batch7Lane5FieldMap(t, result, "answer_box")["answer"] != "A" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane5FieldMap(t, result, "knowledge_graph")["type"] != "Thing" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestCredentials::test_scholar_search_no_creds
func TestBatch7Lane5SerpapiMissingCredentials(t *testing.T) {
	registry := batch7Lane5TestRegistryNoAuth(t, "serpapi_tool")
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{
		"action": "scholar_search", "query": "deep learning",
	})
	if got := batch7Lane5ErrorText(t, result); got != "SerpAPI credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_serpapi_tool.py::TestInputValidation::test_scholar_empty_query
func TestBatch7Lane5SerpapiQueryValidation(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "serpapi_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
			w.WriteHeader(http.StatusInternalServerError)
		},
	)
	result := batch7Lane5Execute(t, registry, "serpapi", map[string]any{"action": "scholar_search", "query": ""})
	if got := batch7Lane5ErrorText(t, result); got != "Query must be 1-500 characters" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream test_serpapi_tool.py covers 401/429 for one action
// only; this table asserts the ported status mapping for every action.
func TestBatch7Lane5SerpapiErrorStatuses(t *testing.T) {
	cases := []map[string]any{
		{"action": "scholar_search", "query": "q"},
		{"action": "scholar_get_citations", "result_id": "r"},
		{"action": "scholar_get_author", "author_id": "a"},
		{"action": "patents_search", "query": "q"},
		{"action": "patents_get_details", "patent_id": "p"},
		{"action": "scholar_cited_by", "cites_id": "c"},
		{"action": "scholar_search_profiles", "query": "q"},
		{"action": "serpapi_google_search", "query": "q"},
	}
	statuses := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid SerpAPI API key"},
		{http.StatusPaymentRequired, "SerpAPI error (HTTP 402)"},
		{http.StatusTooManyRequests, "SerpAPI rate limit exceeded. Try again later."},
	}
	for _, input := range cases {
		for _, tc := range statuses {
			t.Run(input["action"].(string), func(t *testing.T) {
				registry, _ := batch7Lane5TestRegistry(t, "serpapi_tool", map[string]string{}, batch7Lane5StatusHandler(tc.status))
				result := batch7Lane5Execute(t, registry, "serpapi", input)
				if got := batch7Lane5ErrorText(t, result); !strings.Contains(got, tc.fragment) {
					t.Fatalf("error = %q, want %q", got, tc.fragment)
				}
			})
		}
	}
}

// -------------------------------------------------------------------- exa ---

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchWithKey::test_search_with_key_makes_request
func TestBatch7Lane5ExaSearch(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"title": "Result", "url": "https://example.com", "publishedDate": "2024-01-01",
						"author": "Author", "text": "Body", "highlights": []any{"hl"},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_search", "query": "neural search", "search_type": "neural",
		"include_domains": []any{"example.com"}, "include_highlights": true,
		"start_published_date": "2024-01-01",
	})
	request := recorder.at(t, 0)
	if request.Path != "/search" || request.Header.Get("x-api-key") != "test-token" {
		t.Fatalf("request = %s %v", request.Path, request.Header)
	}
	if request.Body["query"] != "neural search" || request.Body["type"] != "neural" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["numResults"] != float64(10) {
		t.Fatalf("body = %v", request.Body)
	}
	if domains, _ := request.Body["includeDomains"].([]any); len(domains) != 1 {
		t.Fatalf("body = %v", request.Body)
	}
	contents := batch7Lane5FieldMap(t, request.Body, "contents")
	if contents["text"] != true || contents["highlights"] != true {
		t.Fatalf("contents = %v", contents)
	}
	batch7Lane5EqualInt(t, result["total"], 1)
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if hit["text"] != "Body" || len(batch7Lane5FieldSlice(t, hit, "highlights")) != 1 {
		t.Fatalf("hit = %v", hit)
	}
}

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchWithKey::test_find_similar_with_key
func TestBatch7Lane5ExaFindSimilar(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{"title": "Similar", "url": "https://similar.example", "text": "Text"},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_find_similar", "url": "https://example.com",
	})
	request := recorder.at(t, 0)
	if request.Path != "/findSimilar" || request.Body["url"] != "https://example.com" {
		t.Fatalf("request = %s %v", request.Path, request.Body)
	}
	if result["source_url"] != "https://example.com" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)["title"] != "Similar" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchWithKey::test_get_contents_with_key
func TestBatch7Lane5ExaGetContents(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{"url": "https://example.com", "title": "Example", "text": "Body"},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_get_contents", "urls": []any{"https://example.com"},
	})
	request := recorder.at(t, 0)
	if request.Path != "/contents" {
		t.Fatalf("path = %s", request.Path)
	}
	if ids, _ := request.Body["ids"].([]any); len(ids) != 1 || ids[0] != "https://example.com" {
		t.Fatalf("body = %v", request.Body)
	}
	batch7Lane5EqualInt(t, result["total"], 1)
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if hit["url"] != "https://example.com" || hit["text"] != "Body" {
		t.Fatalf("hit = %v", hit)
	}
}

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchWithKey::test_answer_with_key
func TestBatch7Lane5ExaAnswer(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"answer": "42",
				"citations": []any{
					map[string]any{"title": "Source", "url": "https://example.com", "publishedDate": "2024-01-01"},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_answer", "query": "meaning of life",
	})
	if recorder.at(t, 0).Path != "/answer" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["answer"] != "42" {
		t.Fatalf("result = %v", result)
	}
	citation := batch7Lane5FieldSlice(t, result, "citations")[0].(map[string]any)
	if citation["title"] != "Source" || citation["published_date"] != "2024-01-01" {
		t.Fatalf("citation = %v", citation)
	}
}

// Parity-exempt: upstream test_exa_search_tool.py does not cover news search.
func TestBatch7Lane5ExaSearchNews(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"title": "News", "url": "https://news.example", "publishedDate": "2024-01-01",
						"text": "Body", "highlights": []any{"hl"},
					},
				},
			})
		},
	)
	registry.Runtime().Now = func() time.Time { return time.Date(2026, time.November, 19, 12, 0, 0, 0, time.UTC) }
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_search_news", "query": "AI news", "days_back": 7,
	})
	request := recorder.at(t, 0)
	if request.Body["category"] != "news" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["startPublishedDate"] != "2026-11-12T00:00:00.000Z" {
		t.Fatalf("body = %v", request.Body)
	}
	if contents := batch7Lane5FieldMap(t, request.Body, "contents"); contents["highlights"] != true {
		t.Fatalf("contents = %v", contents)
	}
	batch7Lane5EqualInt(t, result["days_back"], 7)
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if hit["title"] != "News" || len(batch7Lane5FieldSlice(t, hit, "highlights")) != 1 {
		t.Fatalf("hit = %v", hit)
	}
}

// Parity-exempt: upstream test_exa_search_tool.py does not cover paper search.
func TestBatch7Lane5ExaSearchPapers(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"title": "Paper", "url": "https://arxiv.org/abs/1",
						"publishedDate": "2021-01-01", "text": "Body", "highlights": []any{"hl"},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_search_papers", "query": "transformers", "year_start": 2020,
	})
	request := recorder.at(t, 0)
	if request.Body["category"] != "research paper" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["startPublishedDate"] != "2020-01-01T00:00:00.000Z" {
		t.Fatalf("body = %v", request.Body)
	}
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if _, present := hit["text"]; present {
		t.Fatalf("text should be omitted: %v", hit)
	}
	if len(batch7Lane5FieldSlice(t, hit, "highlights")) != 1 {
		t.Fatalf("hit = %v", hit)
	}
}

// Parity-exempt: upstream test_exa_search_tool.py does not cover company search.
func TestBatch7Lane5ExaSearchCompanies(t *testing.T) {
	registry, recorder := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch7Lane5WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{
					map[string]any{
						"title": "Acme", "url": "https://acme.com",
						"publishedDate": "2024-01-01", "text": "About Acme", "highlights": []any{"hl"},
					},
				},
			})
		},
	)
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_search_companies", "query": "AI startups",
	})
	request := recorder.at(t, 0)
	if request.Body["category"] != "company" {
		t.Fatalf("body = %v", request.Body)
	}
	hit := batch7Lane5FieldSlice(t, result, "results")[0].(map[string]any)
	if hit["title"] != "Acme" || hit["text"] != "About Acme" {
		t.Fatalf("hit = %v", hit)
	}
}

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchCredentials::test_no_credentials_returns_error
func TestBatch7Lane5ExaMissingCredentials(t *testing.T) {
	registry := batch7Lane5TestRegistryNoAuth(t, "exa_search_tool")
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_search", "query": "test",
	})
	if got := batch7Lane5ErrorText(t, result); got != "Exa credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_exa_search_tool.py::TestExaSearchValidation::test_get_contents_too_many_urls
func TestBatch7Lane5ExaGetContentsValidation(t *testing.T) {
	registry, _ := batch7Lane5TestRegistry(
		t, "exa_search_tool", map[string]string{},
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
			w.WriteHeader(http.StatusInternalServerError)
		},
	)
	urls := make([]any, 0, 11)
	for index := 0; index < 11; index++ {
		urls = append(urls, "https://example.com")
	}
	result := batch7Lane5Execute(t, registry, "exa_search", map[string]any{
		"action": "exa_get_contents", "urls": urls,
	})
	if got := batch7Lane5ErrorText(t, result); got != "Maximum 10 URLs per request" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream test_exa_search_tool.py mocks httpx and does not
// assert the ported status mapping (including the 429 retry budget).
func TestBatch7Lane5ExaErrorStatuses(t *testing.T) {
	originalSleep := enrichment.RetrySleep
	enrichment.RetrySleep = func(time.Duration) {}
	t.Cleanup(func() { enrichment.RetrySleep = originalSleep })

	cases := []map[string]any{
		{"action": "exa_search", "query": "q"},
		{"action": "exa_find_similar", "url": "https://example.com"},
		{"action": "exa_get_contents", "urls": []any{"https://example.com"}},
		{"action": "exa_answer", "query": "q"},
		{"action": "exa_search_news", "query": "q"},
		{"action": "exa_search_papers", "query": "q"},
		{"action": "exa_search_companies", "query": "q"},
	}
	statuses := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Invalid Exa API key"},
		{http.StatusPaymentRequired, "Exa API request failed: HTTP 402"},
		{http.StatusTooManyRequests, "Exa rate limit exceeded. Try again later."},
	}
	for _, input := range cases {
		for _, tc := range statuses {
			t.Run(input["action"].(string), func(t *testing.T) {
				registry, recorder := batch7Lane5TestRegistry(t, "exa_search_tool", map[string]string{}, batch7Lane5StatusHandler(tc.status))
				result := batch7Lane5Execute(t, registry, "exa_search", input)
				if got := batch7Lane5ErrorText(t, result); !strings.Contains(got, tc.fragment) {
					t.Fatalf("error = %q, want %q", got, tc.fragment)
				}
				if tc.status == http.StatusTooManyRequests && recorder.count() != 4 {
					t.Fatalf("attempts = %d, want 4", recorder.count())
				}
			})
		}
	}
}

// --------------------------------------------------------------- registry ---

// Parity-exempt: catalog/install invariants are Go-side contracts.
func TestBatch7Lane5CatalogIntegrity(t *testing.T) {
	registry := batch7Lane5NewRegistry()
	if got := len(registry.Names()); got != 5 {
		t.Fatalf("tools = %d, want 5 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 36 {
		t.Fatalf("actions = %d, want 36", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	expected := map[string]int{
		"apify":      6,
		"apollo":     7,
		"lusha":      8,
		"serpapi":    8,
		"exa_search": 7,
	}
	for name, want := range expected {
		tool, ok := registry.Lookup(name)
		if !ok {
			t.Fatalf("tool %q missing", name)
		}
		if got := len(tool.Actions); got != want {
			t.Fatalf("%s actions = %d, want %d", name, got, want)
		}
		if tool.ID == "" || tool.Summary == "" || tool.DisplayName == "" {
			t.Fatalf("%s missing identity: %+v", name, tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", name, action.Name)
			}
			if registry.Impls(name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestBatch7Lane5Install(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := Batch7Lane5Install(shared); installed != 5 {
		t.Fatalf("installed = %d, want 5", installed)
	}
	for _, name := range []string{"apify", "apollo", "lusha", "serpapi", "exa_search"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("exa_search")
	if !ok {
		t.Fatal("exa_search tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("exa_search missing action selector: %v", properties)
	}
	enum, _ := action["enum"].([]string)
	if len(enum) != 7 {
		t.Fatalf("exa_search action enum = %v", action["enum"])
	}
	if tool.Description == "" {
		t.Fatal("exa_search empty description")
	}
}

// Parity-exempt: schema shape is a Go-side contract with tools.ToolRegistry.
func TestBatch7Lane5SchemaShape(t *testing.T) {
	registry := batch7Lane5NewRegistry()
	for _, tool := range registry.Tools() {
		schema := tool.Schema()
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v", tool.Name, schema["type"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing", tool.Name)
		}
		action, ok := properties["action"].(map[string]any)
		if !ok {
			t.Fatalf("%s action property missing", tool.Name)
		}
		enum, ok := action["enum"].([]string)
		if !ok || len(enum) != len(tool.Actions) {
			t.Fatalf("%s action enum = %v", tool.Name, action["enum"])
		}
		if required, _ := schema["required"].([]string); len(required) != 1 || required[0] != "action" {
			t.Fatalf("%s required = %v", tool.Name, schema["required"])
		}
	}
}

// Parity-exempt: unknown-action dispatch is a Go-side registry contract.
func TestBatch7Lane5UnknownAction(t *testing.T) {
	registry := batch7Lane5NewRegistry()
	if _, err := registry.Execute(context.Background(), "apify", map[string]any{"action": "nope"}); err == nil {
		t.Fatal("expected unknown action error")
	}
}
