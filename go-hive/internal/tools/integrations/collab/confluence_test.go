package collab

import (
	"net/http"
	"strings"
	"testing"
)

func batch5Lane1ConfluenceRegistry(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch5Lane1Recorder) {
	t.Helper()
	env := map[string]string{
		"CONFLUENCE_DOMAIN": "test.atlassian.net",
		"CONFLUENCE_EMAIL":  "user@test.com",
	}
	return batch5Lane1TestRegistry(t, "confluence_tool", env, handler, opts...)
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceListSpaces::test_successful_list
func TestBatch5Lane1ConfluenceListSpacesSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"results": []any{
				map[string]any{"id": "123", "key": "DEV", "name": "Development", "type": "global", "status": "current"},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action": "confluence_list_spaces",
		"limit":  500,
	})
	spaces := batch5Lane1SliceField(t, result, "spaces")
	if len(spaces) != 1 || spaces[0].(map[string]any)["key"] != "DEV" {
		t.Fatalf("spaces = %v", spaces)
	}
	request := recorder.last(t)
	if request.Path != "/wiki/api/v2/spaces" || request.Query.Get("limit") != "250" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceListPages::test_successful_list
func TestBatch5Lane1ConfluenceListPagesSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"results": []any{
				map[string]any{
					"id": "page-1", "title": "Getting Started", "spaceId": "123",
					"status": "current", "version": map[string]any{"number": 3},
					"createdAt": "2024-01-01T00:00:00Z",
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":   "confluence_list_pages",
		"space_id": "123",
		"title":    "Getting Started",
	})
	pages := batch5Lane1SliceField(t, result, "pages")
	if len(pages) != 1 {
		t.Fatalf("pages = %v", pages)
	}
	page := pages[0].(map[string]any)
	if page["title"] != "Getting Started" || page["version"] != 3 {
		t.Fatalf("page = %v", page)
	}
	request := recorder.last(t)
	if request.Path != "/wiki/api/v2/spaces/123/pages" {
		t.Fatalf("path = %q", request.Path)
	}
	if request.Query.Get("title") != "Getting Started" || request.Query.Get("limit") != "25" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceGetPage::test_successful_get
func TestBatch5Lane1ConfluenceGetPageSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"id": "page-1", "title": "Getting Started", "spaceId": "123", "status": "current",
			"version":   map[string]any{"number": 3},
			"body":      map[string]any{"storage": map[string]any{"value": "<p>Hello</p>"}},
			"createdAt": "2024-01-01T00:00:00Z",
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_get_page",
		"page_id": "page-1",
	})
	if result["title"] != "Getting Started" || result["body"] != "<p>Hello</p>" {
		t.Fatalf("result = %v", result)
	}
	if request := recorder.last(t); request.Query.Get("body-format") != "storage" {
		t.Fatalf("body-format = %q", request.Query.Get("body-format"))
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceGetPage::test_missing_id
func TestBatch5Lane1ConfluenceGetPageValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_get_page",
		"page_id": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "page_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceCreatePage::test_successful_create
func TestBatch5Lane1ConfluenceCreatePageSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusCreated, map[string]any{"id": "page-new", "title": "New Page"})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":    "confluence_create_page",
		"space_id":  "123",
		"title":     "New Page",
		"body":      "<p>Content</p>",
		"parent_id": "page-parent",
	})
	if result["status"] != "created" || result["id"] != "page-new" {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPost || request.Path != "/wiki/api/v2/pages" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	if request.Body["spaceId"] != "123" || request.Body["parentId"] != "page-parent" {
		t.Fatalf("body = %v", request.Body)
	}
	body := batch5Lane1MapField(t, request.Body, "body")
	if body["representation"] != "storage" || body["value"] != "<p>Content</p>" {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceCreatePage::test_missing_params
func TestBatch5Lane1ConfluenceCreatePageValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":   "confluence_create_page",
		"space_id": "",
		"title":    "",
		"body":     "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "space_id, title, and body are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceSearch::test_successful_search
func TestBatch5Lane1ConfluenceSearchSuccess(t *testing.T) {
	longExcerpt := strings.Repeat("x", 350)
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"results": []any{
				map[string]any{
					"title":        "Deploy Guide",
					"excerpt":      longExcerpt,
					"content":      map[string]any{"id": "page-1", "space": map[string]any{"key": "DEV", "name": "Development"}},
					"lastModified": "2024-06-01T00:00:00Z",
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":    "confluence_search",
		"query":     "deployment",
		"space_key": "DEV",
	})
	results := batch5Lane1SliceField(t, result, "results")
	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	hit := results[0].(map[string]any)
	if hit["title"] != "Deploy Guide" || hit["page_id"] != "page-1" || hit["space_key"] != "DEV" {
		t.Fatalf("hit = %v", hit)
	}
	if got := hit["excerpt"].(string); len(got) != 300 {
		t.Fatalf("excerpt length = %d, want 300", len(got))
	}
	request := recorder.last(t)
	wantCQL := `type = page AND text ~ "deployment" AND space = "DEV" ORDER BY lastModified desc`
	if request.Query.Get("cql") != wantCQL {
		t.Fatalf("cql = %q, want %q", request.Query.Get("cql"), wantCQL)
	}
	if request.Query.Get("limit") != "25" {
		t.Fatalf("limit = %q", request.Query.Get("limit"))
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceSearch::test_missing_query
func TestBatch5Lane1ConfluenceSearchValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action": "confluence_search",
		"query":  "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "query is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream confluence_tool defines confluence_update_page, but
// test_confluence_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1ConfluenceUpdatePageSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"id": "page-1", "title": "Getting Started", "version": map[string]any{"number": 4},
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":         "confluence_update_page",
		"page_id":        "page-1",
		"title":          "Getting Started",
		"body":           "<p>New</p>",
		"version_number": 4,
	})
	if result["status"] != "updated" || result["version"] != 4 {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if request.Method != http.MethodPut || request.Path != "/wiki/api/v2/pages/page-1" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
	version := batch5Lane1MapField(t, request.Body, "version")
	if version["number"] != float64(4) || version["message"] != "Updated via API" {
		t.Fatalf("version = %v", version)
	}
}

// Parity-exempt: upstream confluence_tool defines confluence_update_page, but
// test_confluence_tool.py has no unit test for its validation branches.
func TestBatch5Lane1ConfluenceUpdatePageValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	cases := []map[string]any{
		{"action": "confluence_update_page", "page_id": "", "title": "T", "body": "B", "version_number": 2},
		{"action": "confluence_update_page", "page_id": "p", "title": "T", "body": "B", "version_number": 0},
	}
	want := []string{"page_id, title, and body are required", "version_number must be >= 1"}
	for index, input := range cases {
		result := batch5Lane1Execute(t, registry, "confluence", input)
		if got := batch5Lane1ErrorText(t, result); got != want[index] {
			t.Fatalf("case %d error = %q, want %q", index, got, want[index])
		}
	}
}

// Parity-exempt: upstream confluence_tool defines confluence_delete_page, but
// test_confluence_tool.py has no unit test for it; this Go test covers the port.
func TestBatch5Lane1ConfluenceDeletePageSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_delete_page",
		"page_id": "page-1",
	})
	if result["status"] != "deleted" || result["page_id"] != "page-1" {
		t.Fatalf("result = %v", result)
	}
	if request := recorder.last(t); request.Method != http.MethodDelete || request.Path != "/wiki/api/v2/pages/page-1" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
}

// Parity-exempt: upstream confluence_tool defines confluence_delete_page, but
// test_confluence_tool.py has no unit test for its validation branch.
func TestBatch5Lane1ConfluenceDeletePageValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_delete_page",
		"page_id": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "page_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream confluence_tool defines confluence_get_page_children,
// but test_confluence_tool.py has no unit test for it; this Go test covers it.
func TestBatch5Lane1ConfluenceGetPageChildrenSuccess(t *testing.T) {
	registry, recorder := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch5Lane1WriteJSON(w, http.StatusOK, map[string]any{
			"results": []any{
				map[string]any{
					"id": "page-child", "title": "Child", "status": "current",
					"version": map[string]any{"number": 2},
				},
			},
		})
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_get_page_children",
		"page_id": "page-1",
		"limit":   500,
	})
	if result["count"] != 1 {
		t.Fatalf("count = %v", result["count"])
	}
	children := batch5Lane1SliceField(t, result, "children")
	child := children[0].(map[string]any)
	if child["id"] != "page-child" || child["version"] != 2 {
		t.Fatalf("child = %v", child)
	}
	request := recorder.last(t)
	if request.Path != "/wiki/api/v2/pages/page-1/children" || request.Query.Get("limit") != "250" {
		t.Fatalf("request = %s", batch5Lane1RequestSummary(request))
	}
}

// Parity-exempt: upstream confluence_tool defines limits on
// confluence_get_page_children, but test_confluence_tool.py has no unit test
// for its validation branch.
func TestBatch5Lane1ConfluenceGetPageChildrenValidation(t *testing.T) {
	registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Error("validation failure should not reach upstream")
	})
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action":  "confluence_get_page_children",
		"page_id": "",
	})
	if got := batch5Lane1ErrorText(t, result); got != "page_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream confluence_tool._request has no Python unit tests for
// HTTP status branches; these assertions lock its 401/404/429 mapping.
func TestBatch5Lane1ConfluenceHTTPErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   string
	}{
		{"unauthorized", http.StatusUnauthorized, "Unauthorized. Check your Confluence credentials."},
		{"not_found", http.StatusNotFound, "Not found"},
		{"rate_limited", http.StatusTooManyRequests, "Confluence API error 429: boom"},
		{"server_error", http.StatusInternalServerError, "Confluence API error 500: boom"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane1ConfluenceRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte("boom"))
			})
			result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
				"action":  "confluence_get_page",
				"page_id": "page-1",
			})
			if got := batch5Lane1ErrorText(t, result); got != testCase.want {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_confluence_tool.py::TestConfluenceListSpaces::test_missing_credentials
func TestBatch5Lane1ConfluenceMissingCredentials(t *testing.T) {
	registry := NewAtlassianRegistry(
		WithBaseURL("confluence_tool", "https://example.invalid"),
		WithGetenv(func(string) string { return "" }),
	)
	result := batch5Lane1Execute(t, registry, "confluence", map[string]any{
		"action": "confluence_list_spaces",
	})
	errorText := batch5Lane1ErrorText(t, result)
	if !strings.Contains(errorText, "CONFLUENCE_DOMAIN") {
		t.Fatalf("error = %q", errorText)
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "api-tokens") {
		t.Fatalf("help = %v", result["help"])
	}
}
