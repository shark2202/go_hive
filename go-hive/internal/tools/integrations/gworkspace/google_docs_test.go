package gworkspace

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsCreateDocument::test_success_returns_url
func TestBatch5Lane3DocsCreateDocument(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if body["title"] != "My Doc" {
			t.Fatalf("body = %v", body)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"documentId": "doc-1", "title": "My Doc"})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_create_document", "title": "My Doc",
	})
	if result["document_id"] != "doc-1" || result["document_url"] != "https://docs.google.com/document/d/doc-1/edit" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsGetDocument::test_success
func TestBatch5Lane3DocsGetDocument(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/documents/doc-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"documentId": "doc-1", "title": "Doc"})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_get_document", "document_id": "doc-1",
	})
	if result["title"] != "Doc" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsInsertText::test_success,
// tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsClientInsertText::test_insert_at_end_fetches_doc
func TestBatch5Lane3DocsInsertTextAppends(t *testing.T) {
	calls := 0
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls++
		if calls == 1 {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/documents/doc-1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"documentId": "doc-1",
				"body": map[string]any{"content": []any{
					map[string]any{"endIndex": 1},
					map[string]any{"endIndex": 10},
				}},
			})
		} else {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{map[string]any{}}})
		}
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_insert_text", "document_id": "doc-1", "text": "Hello",
	})
	if _, ok := result["replies"]; !ok {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	insertText := asMap(asMap(requests[0])["insertText"])
	if asMap(insertText["location"])["index"] != float64(9) || insertText["text"] != "Hello" {
		t.Fatalf("insertText = %v", insertText)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsReplaceAllText::test_success_with_count
func TestBatch5Lane3DocsReplaceAllText(t *testing.T) {
	registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"replies": []any{map[string]any{"replaceAllText": map[string]any{"occurrencesChanged": 3}}},
		})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_replace_all_text", "document_id": "doc-1",
		"find_text": "{{Name}}", "replace_text": "Alice", "match_case": false,
	})
	if result["occurrences_replaced"] != 3 || result["find_text"] != "{{Name}}" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsClientInsertImage::test_valid_image_insertion
func TestBatch5Lane3DocsInsertImage(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{}})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_insert_image", "document_id": "doc-1",
		"image_uri": "https://example.com/chart.png", "index": 5, "width_pt": 120,
	})
	if _, ok := result["replies"]; !ok {
		t.Fatalf("result = %v", result)
	}
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	image := asMap(asMap(requests[0])["insertInlineImage"])
	if image["uri"] != "https://example.com/chart.png" || asMap(image["location"])["index"] != float64(5) {
		t.Fatalf("image = %v", image)
	}
	objectSize := asMap(image["objectSize"])
	if asMap(objectSize["width"])["magnitude"] != float64(120) {
		t.Fatalf("objectSize = %v", objectSize)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsClientInsertImage::test_invalid_uri
func TestBatch5Lane3DocsInsertImageRejectsBadURI(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Fatal("no request expected for an invalid image URI")
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_insert_image", "document_id": "doc-1",
		"image_uri": "file:///etc/passwd", "index": 1,
	})
	if message := batch4ErrorText(t, result); !containsAll(message, "Invalid image URI scheme") {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsClientFormatText::test_bold_formatting
func TestBatch5Lane3DocsFormatText(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{}})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_format_text", "document_id": "doc-1",
		"start_index": 1, "end_index": 6, "bold": true, "font_size_pt": 12.5,
	})
	if _, ok := result["replies"]; !ok {
		t.Fatalf("result = %v", result)
	}
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	style := asMap(asMap(requests[0])["updateTextStyle"])
	textStyle := asMap(style["textStyle"])
	if textStyle["bold"] != true {
		t.Fatalf("textStyle = %v", textStyle)
	}
	if asMap(textStyle["fontSize"])["magnitude"] != float64(12.5) {
		t.Fatalf("textStyle = %v", textStyle)
	}
	if style["fields"] != "bold,fontSize" {
		t.Fatalf("fields = %v", style["fields"])
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsBatchUpdate::test_success
func TestBatch5Lane3DocsBatchUpdate(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{map[string]any{}}})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action":        "google_docs_batch_update",
		"document_id":   "doc-1",
		"requests_json": `[{"deleteContentRange":{"range":{"startIndex":1,"endIndex":5}}}]`,
	})
	if _, ok := result["replies"]; !ok {
		t.Fatalf("result = %v", result)
	}
	if requests := batch4BodySlice(t, recorder.last(t).Body["requests"]); len(requests) != 1 {
		t.Fatalf("requests = %v", requests)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsBatchUpdate::test_invalid_json
func TestBatch5Lane3DocsBatchUpdateInvalidJSON(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Fatal("no request expected for invalid JSON")
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_batch_update", "document_id": "doc-1", "requests_json": "{",
	})
	if message := batch4ErrorText(t, result); !containsAll(message, "Invalid JSON") {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsCreateList::test_bullet_list
func TestBatch5Lane3DocsCreateList(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/documents/doc-1:batchUpdate" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"replies": []any{}})
	})
	batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_create_list", "document_id": "doc-1",
		"start_index": 1, "end_index": 20,
	})
	requests := batch4BodySlice(t, recorder.last(t).Body["requests"])
	bullets := asMap(asMap(requests[0])["createParagraphBullets"])
	if bullets["bulletPreset"] != "BULLET_DISC_CIRCLE_SQUARE" {
		t.Fatalf("bullets = %v", bullets)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsAddComment::test_success
func TestBatch5Lane3DocsAddComment(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/drive/v3/files/doc-1/comments" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "comment-1", "content": "Nice"})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_add_comment", "document_id": "doc-1",
		"content": "Nice", "quoted_text": "paragraph",
	})
	if result["id"] != "comment-1" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["content"] != "Nice" || asMap(body["quotedFileContent"])["value"] != "paragraph" {
		t.Fatalf("body = %v", body)
	}
	if recorder.last(t).Query.Get("fields") != "*" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsListComments::test_success_returns_structured
func TestBatch5Lane3DocsListComments(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/drive/v3/files/doc-1/comments" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"comments":      []any{map[string]any{"id": "c1"}},
			"nextPageToken": "next-token",
		})
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_list_comments", "document_id": "doc-1", "page_size": 500,
	})
	if len(batch4BodySlice(t, result["comments"])) != 1 || result["next_page_token"] != "next-token" {
		t.Fatalf("result = %v", result)
	}
	query := recorder.last(t).Query
	if query.Get("pageSize") != "100" || query.Get("includeDeleted") != "false" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::TestGoogleDocsExportContent::test_export_pdf
func TestBatch5Lane3DocsExportContent(t *testing.T) {
	payload := []byte("%PDF-1.4 fake")
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/drive/v3/files/doc-1/export" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("mimeType") != "application/pdf" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	})
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_export_content", "document_id": "doc-1",
	})
	if result["content_base64"] != base64.StdEncoding.EncodeToString(payload) {
		t.Fatalf("result = %v", result)
	}
	if result["size_bytes"] != len(payload) || result["mime_type"] != "application/pdf" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: Go-side response mapping; upstream tests mock one status at a
// time and do not cover an empty 2xx body.
func TestBatch5Lane3DocsErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, "Invalid or expired Google access token"},
		{"forbidden", http.StatusForbidden, `{}`, "Insufficient permissions"},
		{"not found", http.StatusNotFound, `{}`, "Document not found"},
		{"rate limit", http.StatusTooManyRequests, `{}`, "rate limit exceeded"},
		{"api error", http.StatusBadRequest, `{"error":{"message":"bad index"}}`, "bad index"},
		{"empty", http.StatusOK, ``, "invalid JSON response"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			})
			result := batch4Execute(t, registry, "google_docs", map[string]any{
				"action": "google_docs_get_document", "document_id": "doc-1",
			})
			if message := batch4ErrorText(t, result); !containsAll(message, testCase.message) {
				t.Fatalf("error = %q, want %q", message, testCase.message)
			}
		})
	}
}
