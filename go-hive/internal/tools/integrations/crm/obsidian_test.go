package crm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianReadNote::test_successful_read
func TestBatch5ObsidianReadNote(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{
			"path":        "Notes/meeting.md",
			"content":     "# Meeting Notes\n\nDiscussed project roadmap.",
			"tags":        []any{"meeting", "project"},
			"frontmatter": map[string]any{"status": "draft"},
			"stat":        map[string]any{"ctime": 1705334400000, "mtime": 1705420800000, "size": 2048},
		})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "Notes/meeting.md",
	})
	if result["path"] != "Notes/meeting.md" || !strings.Contains(result["content"].(string), "Meeting Notes") {
		t.Fatalf("result = %v", result)
	}
	tags, _ := result["tags"].([]any)
	if len(tags) != 2 || tags[0] != "meeting" {
		t.Fatalf("tags = %v", result["tags"])
	}
	if _, ok := result["stat"].(map[string]any); !ok {
		t.Fatalf("stat = %v", result["stat"])
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodGet, "/vault/Notes/meeting.md")
	if got := request.Header.Get("Accept"); got != obsidianNoteMediaType {
		t.Fatalf("Accept = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: upstream mocks httpx and only returns JSON; the port pins the
// markdown/text response branch of _handle_response.
func TestBatch5ObsidianReadNoteTextResponse(t *testing.T) {
	registry, _ := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4Text(w, http.StatusOK, "text/markdown", "# March 3\n")
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "Daily/2025-03-03.md",
	})
	if result["path"] != "Daily/2025-03-03.md" || result["content"] != "# March 3\n" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianReadNote::test_missing_path
func TestBatch5ObsidianReadNoteRequiresPath(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_read_note", "path": ""})
	if message := batch5Lane4ErrorText(t, result); message != "path is required" {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianReadNote::test_missing_credentials
func TestBatch5ObsidianMissingCredentials(t *testing.T) {
	registry := NewHubSpotObsidianRegistry(WithGetenv(func(string) string { return "" }))
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_read_note", "path": ""})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "not configured") {
		t.Fatalf("error = %q", message)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("missing help: %v", result)
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianWriteNote::test_successful_write
func TestBatch5ObsidianWriteNote(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	content := "# March 3\n\n- Morning tasks"
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_write_note", "path": "Daily/2025-03-03.md", "content": content,
	})
	if result["success"] != true || result["path"] != "Daily/2025-03-03.md" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPut, "/vault/Daily/2025-03-03.md")
	if got := request.Header.Get("Content-Type"); got != "text/markdown" {
		t.Fatalf("Content-Type = %q", got)
	}
	if request.text() != content {
		t.Fatalf("body = %q", request.text())
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianWriteNote::test_missing_path
func TestBatch5ObsidianWriteNoteRequiresPath(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_write_note", "path": "", "content": "test",
	})
	if message := batch5Lane4ErrorText(t, result); message != "path is required" {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianAppendNote::test_successful_append
func TestBatch5ObsidianAppendNote(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_append_note", "path": "Daily/2025-03-03.md", "content": "\n## Afternoon\n- Review PR",
	})
	if result["success"] != true || result["path"] != "Daily/2025-03-03.md" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/vault/Daily/2025-03-03.md")
	if request.text() != "\n## Afternoon\n- Review PR" {
		t.Fatalf("body = %q", request.text())
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianSearch::test_successful_search
func TestBatch5ObsidianSearch(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, []any{
			map[string]any{
				"filename": "Daily/2025-03-01.md",
				"score":    0.85,
				"matches": []any{
					map[string]any{"context": "one"},
					map[string]any{"context": "two"},
					map[string]any{"context": "three"},
					map[string]any{"context": "four"},
					map[string]any{"context": "five"},
					map[string]any{"context": "six"},
				},
			},
		})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_search", "query": "meeting",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	results, _ := result["results"].([]map[string]any)
	if len(results) != 1 || results[0]["filename"] != "Daily/2025-03-01.md" {
		t.Fatalf("results = %v", result["results"])
	}
	if results[0]["match_count"] != 6 {
		t.Fatalf("match_count = %v", results[0]["match_count"])
	}
	contexts, _ := results[0]["contexts"].([]any)
	if len(contexts) != 5 || contexts[0] != "one" || contexts[4] != "five" {
		t.Fatalf("contexts = %v", results[0]["contexts"])
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodPost, "/search/simple/")
	if request.Query.Get("query") != "meeting" || request.Query.Get("contextLength") != "100" {
		t.Fatalf("query = %v", request.Query)
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianSearch::test_missing_query
func TestBatch5ObsidianSearchRequiresQuery(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, []any{})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_search", "query": ""})
	if message := batch5Lane4ErrorText(t, result); message != "query is required" {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity-exempt: upstream's non-list search response branch has no test; the
// port pins the empty-result shape and the custom contextLength passthrough.
func TestBatch5ObsidianSearchNonListResponseAndContextLength(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"files": []any{"a.md"}})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_search", "query": "meeting", "context_length": 250,
	})
	if result["count"] != 0 {
		t.Fatalf("result = %v", result)
	}
	if results, _ := result["results"].([]map[string]any); len(results) != 0 {
		t.Fatalf("results = %v", result["results"])
	}
	if got := recorder.last(t).Query.Get("contextLength"); got != "250" {
		t.Fatalf("contextLength = %q", got)
	}
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianListFiles::test_successful_list
func TestBatch5ObsidianListFilesRoot(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, []any{"Daily/", "Projects/", "README.md", "Templates/"})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_list_files"})
	if result["count"] != 4 || result["path"] != "/" {
		t.Fatalf("result = %v", result)
	}
	files, _ := result["files"].([]any)
	if len(files) != 4 || files[2] != "README.md" {
		t.Fatalf("files = %v", result["files"])
	}
	batch5Lane4GetRequest(t, recorder, http.MethodGet, "/vault/")
}

// Parity-exempt: upstream only covers the flat-list listing; the port pins the
// trailing-slash directory form and the {"files": [...]} envelope.
func TestBatch5ObsidianListFilesSubdirectory(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"files": []any{"roadmap.md", "notes.md"}})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_list_files", "path": "Projects/",
	})
	if result["count"] != 2 || result["path"] != "Projects/" {
		t.Fatalf("result = %v", result)
	}
	files, _ := result["files"].([]any)
	if len(files) != 2 || files[0] != "roadmap.md" {
		t.Fatalf("files = %v", result["files"])
	}
	batch5Lane4GetRequest(t, recorder, http.MethodGet, "/vault/Projects/")
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianGetActive::test_successful_get
func TestBatch5ObsidianGetActive(t *testing.T) {
	registry, recorder := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{
			"path":        "Projects/current.md",
			"content":     "# Current Note\n\nWorking on this.",
			"tags":        []any{"active"},
			"frontmatter": map[string]any{"status": "wip"},
		})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_get_active"})
	if result["path"] != "Projects/current.md" || !strings.Contains(result["content"].(string), "Current Note") {
		t.Fatalf("result = %v", result)
	}
	if _, present := result["stat"]; present {
		t.Fatalf("active note should not carry stat: %v", result)
	}
	batch5Lane4GetRequest(t, recorder, http.MethodGet, "/active/")
}

// Parity: tools/tests/tools/test_obsidian_tool.py::TestObsidianGetActive::test_no_active_file
func TestBatch5ObsidianGetActiveNoFile(t *testing.T) {
	registry, _ := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "No active file"})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{"action": "obsidian_get_active"})
	if message := batch5Lane4ErrorText(t, result); message != "No active file open in Obsidian" {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: _handle_response's 401 mapping has no dedicated upstream test.
func TestBatch5ObsidianUnauthorized(t *testing.T) {
	registry, _ := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusUnauthorized, map[string]any{"message": "unauthorized"})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "Notes/meeting.md",
	})
	if message := batch5Lane4ErrorText(t, result); message != "Invalid Obsidian REST API key" {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: _handle_response's 404 mapping has no dedicated upstream test.
func TestBatch5ObsidianNotFound(t *testing.T) {
	registry, _ := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusNotFound, map[string]any{"message": "not found"})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "Missing/note.md",
	})
	if message := batch5Lane4ErrorText(t, result); message != "File or resource not found in Obsidian vault" {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: upstream maps every other 4xx/5xx generically and never tests
// it; the port pins the 429 message and detail passthrough.
func TestBatch5ObsidianRateLimit(t *testing.T) {
	registry, _ := batch5Lane4ObsidianRegistry(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusTooManyRequests, map[string]any{"message": "too many requests"})
	})
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_list_files", "path": "Projects",
	})
	message := batch5Lane4ErrorText(t, result)
	if !strings.Contains(message, "Obsidian API error (HTTP 429)") || !strings.Contains(message, "too many requests") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: base-URL environment resolution is a Go runtime concern;
// upstream reads the same variable but tests patch httpx instead of URLs.
func TestBatch5ObsidianBaseURLFromEnvironment(t *testing.T) {
	recorder := &batch5Lane4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"path": "test.md", "content": "# hi"})
	}))
	t.Cleanup(server.Close)
	registry := NewHubSpotObsidianRegistry(
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string {
			switch name {
			case "OBSIDIAN_REST_API_KEY":
				return "env-key"
			case "OBSIDIAN_REST_BASE_URL":
				return server.URL + "/"
			default:
				return ""
			}
		}),
	)
	result := batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "test.md",
	})
	if result["content"] != "# hi" {
		t.Fatalf("result = %v", result)
	}
	request := batch5Lane4GetRequest(t, recorder, http.MethodGet, "/vault/test.md")
	if got := request.Header.Get("Authorization"); got != "Bearer env-key" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: Go-side catalogue/install contract; upstream tests assert the
// MCP tool count, not a registry surface.
func TestBatch5ObsidianCatalogue(t *testing.T) {
	registry := NewHubSpotObsidianRegistry()
	names := batch5Lane4ActionNames(registry, "obsidian")
	if len(names) != 6 {
		t.Fatalf("obsidian actions = %d, want 6", len(names))
	}
	for _, action := range []string{
		"obsidian_read_note", "obsidian_write_note", "obsidian_append_note",
		"obsidian_search", "obsidian_list_files", "obsidian_get_active",
	} {
		if !batch5Lane4HasAction(names, action) {
			t.Fatalf("missing action %s", action)
		}
	}
	installed, shared := batch5Lane4InstallCount(t, registry)
	if installed != 2 {
		t.Fatalf("installed = %d, want 2", installed)
	}
	if _, ok := shared.GetTool("obsidian"); !ok {
		t.Fatal("obsidian not installed")
	}
}
