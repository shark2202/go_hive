package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// --------------------------------------------------------------- test kit ---

type batch4LoggedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

type batch4Recorder struct {
	mu       sync.Mutex
	requests []batch4LoggedRequest
}

func (rec *batch4Recorder) wrap(next func(http.ResponseWriter, *http.Request, map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &body)
			}
		}
		rec.mu.Lock()
		rec.requests = append(rec.requests, batch4LoggedRequest{
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

func (rec *batch4Recorder) at(t *testing.T, index int) batch4LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if index < 0 || index >= len(rec.requests) {
		t.Fatalf("request %d missing (have %d)", index, len(rec.requests))
	}
	return rec.requests[index]
}

func (rec *batch4Recorder) last(t *testing.T) batch4LoggedRequest {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.requests) == 0 {
		t.Fatal("no request recorded")
	}
	return rec.requests[len(rec.requests)-1]
}

func (rec *batch4Recorder) count() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.requests)
}

// batch4TestRegistry builds a batch-4 registry pointed at one fake upstream.
func batch4TestRegistry(t *testing.T, module string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	base := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithCredential(module, "test-token"),
	}
	return newBatch4Registry(append(base, opts...)...), recorder
}

func batch4Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
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

func batch4WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func batch4ErrorText(t *testing.T, result map[string]any) string {
	t.Helper()
	text, ok := result["error"].(string)
	if !ok {
		t.Fatalf("result has no error string: %v", result)
	}
	return text
}

func batch4BodyMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %T, want map", value)
	}
	return object
}

func batch4BodySlice(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value = %T, want array", value)
	}
	return items
}

type batch4TimeoutError struct{}

func (batch4TimeoutError) Error() string   { return "i/o timeout" }
func (batch4TimeoutError) Timeout() bool   { return true }
func (batch4TimeoutError) Temporary() bool { return true }

type batch4TimeoutTransport struct{}

func (batch4TimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, batch4TimeoutError{}
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not the registry surface.
func TestBatch4CatalogIntegrity(t *testing.T) {
	registry := newBatch4Registry()
	if got := len(registry.Names()); got != 2 {
		t.Fatalf("tools = %d, want 2 (%v)", got, registry.Names())
	}
	if got := registry.ActionCount(); got != 34 {
		t.Fatalf("actions = %d, want 34", got)
	}
	if got := registry.ImplementedActions(); got != registry.ActionCount() {
		t.Fatalf("implemented = %d, catalog = %d", got, registry.ActionCount())
	}
	notion, ok := registry.Lookup("notion")
	if !ok {
		t.Fatal("notion tool missing")
	}
	if got := len(notion.Actions); got != 13 {
		t.Fatalf("notion actions = %d, want 13", got)
	}
	linear, ok := registry.Lookup("linear")
	if !ok {
		t.Fatal("linear tool missing")
	}
	if got := len(linear.Actions); got != 21 {
		t.Fatalf("linear actions = %d, want 21", got)
	}
	for _, tool := range registry.Tools() {
		if tool.ID == "" || tool.Summary == "" {
			t.Fatalf("tool missing identity: %+v", tool)
		}
		for _, action := range tool.Actions {
			if action.Source == "" {
				t.Fatalf("%s.%s missing source", tool.Name, action.Name)
			}
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstallBatch4(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch4(shared); installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	for _, name := range []string{"notion", "linear"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("notion")
	if !ok {
		t.Fatal("notion tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("notion missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 13 {
		t.Fatalf("notion action enum = %v", action["enum"])
	}
	linear, _ := shared.GetTool("linear")
	linearProps := linear.Parameters["properties"].(map[string]any)
	linearAction := linearProps["action"].(map[string]any)
	if enum, _ := linearAction["enum"].([]string); len(enum) != 21 {
		t.Fatalf("linear action enum = %v", linearAction["enum"])
	}
}

// ----------------------------------------------------------------- notion ---

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_successful_search, tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_filter_type_page, tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_filter_type_database, tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_page_size_clamped
func TestBatch4NotionSearch(t *testing.T) {
	data := map[string]any{
		"results": []any{
			map[string]any{
				"object":           "page",
				"id":               "page-1",
				"url":              "https://notion.so/page-1",
				"created_time":     "2024-01-01T00:00:00Z",
				"last_edited_time": "2024-01-15T00:00:00Z",
				"properties": map[string]any{
					"Name": map[string]any{
						"type":  "title",
						"title": []any{map[string]any{"text": map[string]any{"content": "My Page"}}},
					},
				},
			},
			map[string]any{
				"object":           "database",
				"id":               "db-1",
				"url":              "https://notion.so/db-1",
				"created_time":     "2024-01-01T00:00:00Z",
				"last_edited_time": "2024-01-15T00:00:00Z",
				"title":            []any{map[string]any{"text": map[string]any{"content": "My DB"}}},
			},
		},
		"has_more": true,
	}
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/search" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, data)
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_search", "query": "My Page", "filter_type": "page", "page_size": 200,
	})
	if result["count"] != 2 || result["has_more"] != true {
		t.Fatalf("result = %v", result)
	}
	results := result["results"].([]map[string]any)
	if results[0]["title"] != "My Page" || results[1]["title"] != "My DB" {
		t.Fatalf("titles = %v", results)
	}
	request := recorder.last(t)
	if request.Header.Get("Authorization") != "Bearer test-token" || request.Header.Get("Notion-Version") != "2022-06-28" {
		t.Fatalf("headers = %v", request.Header)
	}
	if request.Body["page_size"] != float64(100) {
		t.Fatalf("page_size = %v", request.Body["page_size"])
	}
	filter := batch4BodyMap(t, request.Body["filter"])
	if filter["property"] != "object" || filter["value"] != "page" {
		t.Fatalf("filter = %v", filter)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_filter_type_invalid_ignored
func TestBatch4NotionSearchInvalidFilterIgnored(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"results": []any{}, "has_more": false})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_search", "filter_type": "invalid"})
	if result["count"] != 0 || result["has_more"] != false {
		t.Fatalf("result = %v", result)
	}
	if _, present := recorder.last(t).Body["filter"]; present {
		t.Fatalf("invalid filter was sent: %v", recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionGetPage::test_successful_get, tools/tests/tools/test_notion_tool.py::TestNotionGetPage::test_all_property_types
func TestBatch4NotionGetPage(t *testing.T) {
	data := map[string]any{
		"id":       "page-1",
		"url":      "https://notion.so/page-1",
		"archived": false,
		"properties": map[string]any{
			"Name": map[string]any{
				"type":  "title",
				"title": []any{map[string]any{"text": map[string]any{"content": "Test Page"}}},
			},
			"Status": map[string]any{"type": "select", "select": map[string]any{"name": "Done"}},
			"Description": map[string]any{
				"type":      "rich_text",
				"rich_text": []any{map[string]any{"text": map[string]any{"content": "Hello "}}, map[string]any{"text": map[string]any{"content": "World"}}},
			},
			"Tags":        map[string]any{"type": "multi_select", "multi_select": []any{map[string]any{"name": "bug"}, map[string]any{"name": "urgent"}}},
			"Priority":    map[string]any{"type": "number", "number": 5},
			"Done":        map[string]any{"type": "checkbox", "checkbox": true},
			"Due":         map[string]any{"type": "date", "date": map[string]any{"start": "2024-06-01"}},
			"Progress":    map[string]any{"type": "status", "status": map[string]any{"name": "In Progress"}},
			"EmptySelect": map[string]any{"type": "select", "select": nil},
			"EmptyDate":   map[string]any{"type": "date", "date": nil},
		},
		"created_time":     "2024-01-01T00:00:00Z",
		"last_edited_time": "2024-01-15T00:00:00Z",
	}
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/pages/page-1" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, data)
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_get_page", "page_id": "page-1"})
	if result["title"] != "Test Page" || result["archived"] != false {
		t.Fatalf("result = %v", result)
	}
	properties := batch4BodyMap(t, result["properties"])
	if properties["Description"] != "Hello World" || properties["Priority"] != float64(5) || properties["Done"] != true {
		t.Fatalf("properties = %v", properties)
	}
	if properties["Due"] != "2024-06-01" || properties["Progress"] != "In Progress" {
		t.Fatalf("properties = %v", properties)
	}
	if properties["EmptySelect"] != "" || properties["EmptyDate"] != "" {
		t.Fatalf("empty properties = %v", properties)
	}
	tags, _ := properties["Tags"].([]string)
	if len(tags) != 2 || tags[0] != "bug" || tags[1] != "urgent" {
		t.Fatalf("tags = %v", properties["Tags"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_successful_create, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_with_properties_json, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_with_content, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_create_under_parent_page
func TestBatch4NotionCreatePage(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/pages" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusCreated, map[string]any{"id": "new-page", "url": "https://notion.so/new-page"})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action":             "notion_create_page",
		"title":              "New Page",
		"parent_database_id": "db-1",
		"title_property":     "Name",
		"properties_json":    `{"Status": {"select": {"name": "Open"}}}`,
		"content":            "Some body text",
	})
	if result["status"] != "created" || result["id"] != "new-page" {
		t.Fatalf("result = %v", result)
	}
	first := recorder.at(t, 0)
	parent := batch4BodyMap(t, first.Body["parent"])
	if parent["database_id"] != "db-1" {
		t.Fatalf("parent = %v", parent)
	}
	properties := batch4BodyMap(t, first.Body["properties"])
	if properties["Name"].(map[string]any)["title"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"] != "New Page" {
		t.Fatalf("title property = %v", properties["Name"])
	}
	if properties["Status"].(map[string]any)["select"].(map[string]any)["name"] != "Open" {
		t.Fatalf("properties = %v", properties)
	}
	children := batch4BodySlice(t, first.Body["children"])
	if len(children) != 1 || children[0].(map[string]any)["type"] != "paragraph" {
		t.Fatalf("children = %v", children)
	}

	result = batch4Execute(t, registry, "notion", map[string]any{
		"action":         "notion_create_page",
		"title":          "Child Page",
		"parent_page_id": "parent-page-1",
	})
	if result["status"] != "created" {
		t.Fatalf("result = %v", result)
	}
	second := recorder.at(t, 1)
	parent = batch4BodyMap(t, second.Body["parent"])
	if parent["page_id"] != "parent-page-1" {
		t.Fatalf("parent = %v", parent)
	}
	properties = batch4BodyMap(t, second.Body["properties"])
	if properties["title"].(map[string]any)["title"].([]any)[0].(map[string]any)["text"].(map[string]any)["content"] != "Child Page" {
		t.Fatalf("properties = %v", properties)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_missing_title, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_missing_parent, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_both_parents, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_missing_title_property_for_database, tools/tests/tools/test_notion_tool.py::TestNotionCreatePage::test_invalid_properties_json
func TestBatch4NotionCreatePageValidation(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("validation failure reached the network")
	})
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"missing title", map[string]any{"action": "notion_create_page"}, "title is required"},
		{"missing parent", map[string]any{"action": "notion_create_page", "title": "T"}, "Provide parent_database_id or parent_page_id"},
		{"both parents", map[string]any{"action": "notion_create_page", "title": "T", "parent_database_id": "db", "parent_page_id": "pg"}, "not both"},
		{"missing title property", map[string]any{"action": "notion_create_page", "title": "T", "parent_database_id": "db"}, "title_property is required"},
		{"invalid properties json", map[string]any{"action": "notion_create_page", "title": "T", "parent_database_id": "db", "title_property": "Name", "properties_json": "not valid json{{{"}, "not valid JSON"},
	}
	for _, tc := range cases {
		if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", tc.input)); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: error = %q, want %q", tc.name, got, tc.want)
		}
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_successful_query, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_with_filter_json, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_with_sorts_json, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_with_start_cursor, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_next_cursor_returned, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_page_size_clamped
func TestBatch4NotionQueryDatabase(t *testing.T) {
	data := map[string]any{
		"results": []any{
			map[string]any{
				"id":               "row-1",
				"url":              "https://notion.so/row-1",
				"created_time":     "2024-01-01T00:00:00Z",
				"last_edited_time": "2024-01-15T00:00:00Z",
				"properties": map[string]any{
					"Name": map[string]any{"type": "title", "title": []any{map[string]any{"text": map[string]any{"content": "Task 1"}}}},
				},
			},
		},
		"has_more":    true,
		"next_cursor": "cursor-next-456",
	}
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/databases/db-1/query" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, data)
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action":       "notion_query_database",
		"database_id":  "db-1",
		"filter_json":  `{"property": "Status", "select": {"equals": "Done"}}`,
		"sorts_json":   `[{"property": "Created", "direction": "descending"}]`,
		"start_cursor": "cursor-abc-123",
		"page_size":    0,
	})
	if result["count"] != 1 || result["has_more"] != true || result["next_cursor"] != "cursor-next-456" {
		t.Fatalf("result = %v", result)
	}
	pages := result["pages"].([]map[string]any)
	if pages[0]["title"] != "Task 1" {
		t.Fatalf("pages = %v", pages)
	}
	body := recorder.last(t).Body
	if body["page_size"] != float64(1) || body["start_cursor"] != "cursor-abc-123" {
		t.Fatalf("body = %v", body)
	}
	if batch4BodyMap(t, body["filter"])["property"] != "Status" {
		t.Fatalf("filter = %v", body["filter"])
	}
	sorts := batch4BodySlice(t, body["sorts"])
	if len(sorts) != 1 || sorts[0].(map[string]any)["direction"] != "descending" {
		t.Fatalf("sorts = %v", sorts)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_invalid_filter_json, tools/tests/tools/test_notion_tool.py::TestNotionQueryDatabase::test_invalid_sorts_json
func TestBatch4NotionQueryDatabaseInvalidJSON(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("invalid JSON reached the network")
	})
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_query_database", "database_id": "db-1", "filter_json": "not json!!!"})); !strings.Contains(got, "not valid JSON") {
		t.Fatalf("error = %q", got)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_query_database", "database_id": "db-1", "sorts_json": "not json!!!"})); !strings.Contains(got, "not valid JSON") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionGetDatabase::test_successful_get
func TestBatch4NotionGetDatabase(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/databases/db-1" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id":    "db-1",
			"title": []any{map[string]any{"text": map[string]any{"content": "Tasks"}}},
			"url":   "https://notion.so/db-1",
			"properties": map[string]any{
				"Name":   map[string]any{"type": "title", "id": "title"},
				"Status": map[string]any{"type": "select", "id": "abc"},
			},
			"created_time":     "2024-01-01T00:00:00Z",
			"last_edited_time": "2024-01-15T00:00:00Z",
		})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_get_database", "database_id": "db-1"})
	if result["title"] != "Tasks" {
		t.Fatalf("result = %v", result)
	}
	properties := batch4BodyMap(t, result["properties"])
	if properties["Name"].(map[string]any)["type"] != "title" || properties["Status"].(map[string]any)["id"] != "abc" {
		t.Fatalf("properties = %v", properties)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionCreateDatabase::test_successful_create_default_properties, tools/tests/tools/test_notion_tool.py::TestNotionCreateDatabase::test_with_extra_properties
func TestBatch4NotionCreateDatabase(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/databases" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusCreated, map[string]any{"id": "db-new", "url": "https://notion.so/db-new"})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_create_database", "parent_page_id": "page-1", "title": "Tasks",
		"properties_json": `{"Priority": {"number": {}}}`,
	})
	if result["status"] != "created" || result["id"] != "db-new" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	parent := batch4BodyMap(t, body["parent"])
	if parent["page_id"] != "page-1" || parent["type"] != "page_id" {
		t.Fatalf("parent = %v", parent)
	}
	properties := batch4BodyMap(t, body["properties"])
	if _, present := properties["Name"]; !present {
		t.Fatalf("default Name property missing: %v", properties)
	}
	if _, present := properties["Priority"]; !present {
		t.Fatalf("extra property missing: %v", properties)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionUpdateDatabase::test_update_title, tools/tests/tools/test_notion_tool.py::TestNotionUpdateDatabase::test_update_properties, tools/tests/tools/test_notion_tool.py::TestNotionUpdateDatabase::test_archive_database
func TestBatch4NotionUpdateDatabase(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/databases/db-1" || r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "db-1", "url": "https://notion.so/db-1"})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_update_database", "database_id": "db-1", "title": "Renamed DB",
		"properties_json": `{"Priority": {"number": {}}}`, "archived": true,
	})
	if result["status"] != "updated" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	title := batch4BodySlice(t, body["title"])
	if title[0].(map[string]any)["text"].(map[string]any)["content"] != "Renamed DB" {
		t.Fatalf("title = %v", title)
	}
	if body["archived"] != true {
		t.Fatalf("archived = %v", body["archived"])
	}
	if _, present := batch4BodyMap(t, body["properties"])["Priority"]; !present {
		t.Fatalf("properties = %v", body["properties"])
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionUpdateDatabase::test_no_updates_provided, tools/tests/tools/test_notion_tool.py::TestNotionUpdatePage::test_no_updates_provided, tools/tests/tools/test_notion_tool.py::TestNotionUpdatePage::test_invalid_properties_json
func TestBatch4NotionUpdateValidation(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("validation failure reached the network")
	})
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_update_database", "database_id": "db-1"})); !strings.Contains(got, "No updates") {
		t.Fatalf("error = %q", got)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_update_page", "page_id": "page-1"})); !strings.Contains(got, "No updates") {
		t.Fatalf("error = %q", got)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_update_page", "page_id": "page-1", "properties_json": "{bad json"})); !strings.Contains(got, "not valid JSON") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionUpdatePage::test_successful_update_properties, tools/tests/tools/test_notion_tool.py::TestNotionUpdatePage::test_archive_page
func TestBatch4NotionUpdatePage(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/pages/page-1" || r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "page-1", "url": "https://notion.so/page-1"})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_update_page", "page_id": "page-1",
		"properties_json": `{"Status": {"select": {"name": "Done"}}}`,
	})
	if result["status"] != "updated" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if batch4BodyMap(t, body["properties"])["Status"].(map[string]any)["select"].(map[string]any)["name"] != "Done" {
		t.Fatalf("properties = %v", body["properties"])
	}

	result = batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_update_page", "page_id": "page-1", "archived": true,
	})
	if result["status"] != "updated" || recorder.last(t).Body["archived"] != true {
		t.Fatalf("archive result = %v, body = %v", result, recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionGetBlockChildren::test_successful_get
func TestBatch4NotionGetBlockChildren(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/blocks/page-1/children" || r.URL.Query().Get("page_size") != "50" {
			t.Fatalf("request = %s", r.URL.String())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"results": []any{
				map[string]any{"id": "block-1", "type": "paragraph", "has_children": false, "paragraph": map[string]any{"rich_text": []any{map[string]any{"text": map[string]any{"content": "Hello world"}}}}},
				map[string]any{"id": "block-2", "type": "heading_2", "has_children": false, "heading_2": map[string]any{"rich_text": []any{map[string]any{"text": map[string]any{"content": "Section"}}}}},
				map[string]any{"id": "block-3", "type": "divider", "has_children": false, "divider": map[string]any{}},
			},
			"has_more": false,
		})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_get_block_children", "block_id": "page-1"})
	if result["count"] != 3 {
		t.Fatalf("result = %v", result)
	}
	blocks := result["blocks"].([]map[string]any)
	if blocks[0]["text"] != "Hello world" || blocks[1]["text"] != "Section" {
		t.Fatalf("blocks = %v", blocks)
	}
	if _, present := blocks[2]["text"]; present {
		t.Fatalf("divider must not carry text: %v", blocks[2])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionGetBlock::test_successful_get_paragraph
func TestBatch4NotionGetBlock(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/blocks/block-1" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id": "block-1", "type": "paragraph", "has_children": false, "archived": false,
			"created_time": "2024-01-01T00:00:00Z", "last_edited_time": "2024-01-15T00:00:00Z",
			"paragraph": map[string]any{"rich_text": []any{map[string]any{"text": map[string]any{"content": "Hello world"}}}},
		})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_get_block", "block_id": "block-1"})
	if result["id"] != "block-1" || result["type"] != "paragraph" || result["text"] != "Hello world" || result["archived"] != false {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionUpdateBlock::test_successful_content_update, tools/tests/tools/test_notion_tool.py::TestNotionUpdateBlock::test_invalid_block_type, tools/tests/tools/test_notion_tool.py::TestNotionUpdateBlock::test_content_without_block_type
func TestBatch4NotionUpdateBlock(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/blocks/block-1" || r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "block-1", "type": "paragraph"})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_update_block", "block_id": "block-1", "content": "Updated text", "block_type": "paragraph",
	})
	if result["status"] != "updated" {
		t.Fatalf("result = %v", result)
	}
	paragraph := batch4BodyMap(t, recorder.last(t).Body["paragraph"])
	richText := paragraph["rich_text"].([]any)
	if richText[0].(map[string]any)["text"].(map[string]any)["content"] != "Updated text" {
		t.Fatalf("paragraph = %v", paragraph)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_update_block", "block_id": "block-1", "content": "x"})); !strings.Contains(got, "block_type is required") {
		t.Fatalf("error = %q", got)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_update_block", "block_id": "block-1", "content": "x", "block_type": "invalid_type"})); !strings.Contains(got, "Invalid block_type") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionDeleteBlock::test_successful_delete
func TestBatch4NotionDeleteBlock(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/blocks/block-1" || r.Method != http.MethodDelete {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "block-1", "archived": true})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_delete_block", "block_id": "block-1"})
	if result["status"] != "deleted" || result["id"] != "block-1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Method != http.MethodDelete {
		t.Fatalf("method = %s", recorder.last(t).Method)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_successful_append, tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_blank_lines_stripped, tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_block_type_to_do, tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_block_type_heading
func TestBatch4NotionAppendBlocks(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.URL.Path != "/v1/blocks/page-1/children" || r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"results": []any{}})
	})
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_append_blocks", "block_id": "page-1", "content": "Line one\n\n\nLine two",
	})
	if result["status"] != "appended" || result["blocks_added"] != 2 || result["block_id"] != "page-1" {
		t.Fatalf("result = %v", result)
	}
	children := batch4BodySlice(t, recorder.last(t).Body["children"])
	if len(children) != 2 || children[0].(map[string]any)["type"] != "paragraph" {
		t.Fatalf("children = %v", children)
	}

	result = batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_append_blocks", "block_id": "page-1", "content": "Buy milk\nWalk the dog", "block_type": "to_do",
	})
	if result["blocks_added"] != 2 {
		t.Fatalf("result = %v", result)
	}
	children = batch4BodySlice(t, recorder.last(t).Body["children"])
	toDo := children[0].(map[string]any)
	if toDo["type"] != "to_do" || toDo["to_do"].(map[string]any)["checked"] != false {
		t.Fatalf("to_do block = %v", toDo)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_only_blank_lines, tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_invalid_block_type, tools/tests/tools/test_notion_tool.py::TestNotionAppendBlocks::test_exceeds_100_block_limit
func TestBatch4NotionAppendBlocksValidation(t *testing.T) {
	registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		t.Fatal("validation failure reached the network")
	})
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_append_blocks", "block_id": "page-1", "content": "\n\n\n"})); !strings.Contains(got, "empty") {
		t.Fatalf("error = %q", got)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_append_blocks", "block_id": "page-1", "content": "text", "block_type": "invalid_type"})); !strings.Contains(got, "Invalid block_type") {
		t.Fatalf("error = %q", got)
	}
	lines := make([]string, 101)
	for index := range lines {
		lines[index] = fmt.Sprintf("line %d", index)
	}
	if got := batch4ErrorText(t, batch4Execute(t, registry, "notion", map[string]any{"action": "notion_append_blocks", "block_id": "page-1", "content": strings.Join(lines, "\n")})); !strings.Contains(got, "100") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestRequestErrors::test_http_error_codes
func TestBatch4NotionHTTPErrors(t *testing.T) {
	cases := []struct {
		status   int
		fragment string
	}{
		{http.StatusUnauthorized, "Unauthorized"},
		{http.StatusForbidden, "Forbidden"},
		{http.StatusNotFound, "Not found"},
		{http.StatusTooManyRequests, "Rate limited"},
		{http.StatusInternalServerError, "Notion API error 500"},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			registry, _ := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				batch4WriteJSON(w, tc.status, map[string]any{"message": "nope"})
			})
			result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_search"})
			if got := batch4ErrorText(t, result); !strings.Contains(got, tc.fragment) {
				t.Fatalf("error = %q, want %q", got, tc.fragment)
			}
		})
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestNotionSearch::test_missing_credentials
func TestBatch4NotionMissingCredentials(t *testing.T) {
	registry := newBatch4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_search"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "NOTION_API_TOKEN not set") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestRequestErrors::test_timeout_exception
func TestBatch4NotionTimeout(t *testing.T) {
	registry := newBatch4Registry(
		WithCredential("notion_tool", "test-token"),
		WithHTTPClient(&http.Client{Transport: batch4TimeoutTransport{}}),
	)
	result := batch4Execute(t, registry, "notion", map[string]any{"action": "notion_search"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "timed out") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_notion_tool.py::TestCredentialStoreAdapter::test_credential_store_used_when_provided
func TestBatch4NotionCredentialInjected(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"results": []any{}, "has_more": false})
	})
	batch4Execute(t, registry, "notion", map[string]any{"action": "notion_search"})
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}
