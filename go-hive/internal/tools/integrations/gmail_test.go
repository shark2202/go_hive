package integrations

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_gmail_tool.py::TestListMessages::test_list_success
func TestBatch5Lane3GmailListMessages(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/gmail/v1/users/me/messages" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("q") != "is:unread" || r.URL.Query().Get("maxResults") != "100" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"messages":           []any{map[string]any{"id": "msg-1", "threadId": "thread-1"}},
			"resultSizeEstimate": 1,
		})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_list_messages",
	})
	if len(batch4BodySlice(t, result["messages"])) != 1 || result["result_size_estimate"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestListMessages::test_list_max_results_clamped
func TestBatch5Lane3GmailListMessagesClampsLimit(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"messages": []any{}, "resultSizeEstimate": 0})
	})
	batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_list_messages", "query": "label:INBOX", "max_results": 1000,
	})
	query := recorder.last(t).Query
	if query.Get("maxResults") != "500" || query.Get("q") != "label:INBOX" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGetMessage::test_get_metadata
func TestBatch5Lane3GmailGetMessage(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/gmail/v1/users/me/messages/msg-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("format") != "metadata" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id":       "msg-1",
			"threadId": "thread-1",
			"labelIds": []any{"INBOX", "UNREAD"},
			"snippet":  "hello there",
			"payload": map[string]any{"headers": []any{
				map[string]any{"name": "Subject", "value": "Hi"},
				map[string]any{"name": "From", "value": "Alice <alice@example.com>"},
				map[string]any{"name": "To", "value": "bob@example.com"},
				map[string]any{"name": "Date", "value": "Mon, 1 Jan 2024 09:00:00 +0000"},
				map[string]any{"name": "X-Other", "value": "ignored"},
			}},
		})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_get_message", "message_id": "msg-1",
	})
	if result["subject"] != "Hi" || result["from"] != "Alice <alice@example.com>" || result["snippet"] != "hello there" {
		t.Fatalf("result = %v", result)
	}
	if _, present := result["x-other"]; present {
		t.Fatalf("unexpected header in %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGetMessage::test_get_full_with_body
func TestBatch5Lane3GmailGetMessageFullBody(t *testing.T) {
	bodyData := base64.URLEncoding.EncodeToString([]byte("Hello body"))
	registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id":      "msg-1",
			"payload": map[string]any{"body": map[string]any{"data": bodyData}},
		})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_get_message", "message_id": "msg-1", "format": "full",
	})
	if result["body"] != "Hello body" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestTrashMessage::test_trash_success
func TestBatch5Lane3GmailTrashMessage(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/messages/msg-1/trash" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_trash_message", "message_id": "msg-1",
	})
	if result["success"] != true || result["message_id"] != "msg-1" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestModifyMessage::test_star_message
func TestBatch5Lane3GmailModifyMessage(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/messages/msg-1/modify" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"labelIds": []any{"STARRED", "INBOX"}})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_modify_message", "message_id": "msg-1", "add_labels": "STARRED",
	})
	if len(batch4BodySlice(t, result["labels"])) != 2 {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	added := batch4BodySlice(t, body["addLabelIds"])
	if len(added) != 1 || added[0] != "STARRED" {
		t.Fatalf("body = %v", body)
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestModifyMessage::test_modify_no_labels_returns_error
func TestBatch5Lane3GmailModifyMessageRequiresLabels(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Fatal("no request expected without label changes")
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_modify_message", "message_id": "msg-1",
	})
	if message := batch4ErrorText(t, result); !containsAll(message, "At least one of add_labels or remove_labels") {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestBatchModifyMessages::test_batch_success
func TestBatch5Lane3GmailBatchModifyMessages(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/messages/batchModify" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_batch_modify_messages", "message_ids": []any{"m1", "m2"},
		"remove_labels": []any{"UNREAD"},
	})
	if result["success"] != true || result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if len(batch4BodySlice(t, body["ids"])) != 2 || len(batch4BodySlice(t, body["removeLabelIds"])) != 1 {
		t.Fatalf("body = %v", body)
	}
}

// Parity-exempt: upstream has no batch_get_messages test; behavior follows the
// per-message loop in gmail_batch_get_messages.
func TestBatch5Lane3GmailBatchGetMessages(t *testing.T) {
	calls := 0
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		calls++
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id":       "msg-" + string(rune('0'+calls)),
			"labelIds": []any{"INBOX"},
			"payload": map[string]any{"headers": []any{
				map[string]any{"name": "Subject", "value": "S" + string(rune('0'+calls))},
			}},
		})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_batch_get_messages", "message_ids": []any{"msg-1", "msg-2"},
	})
	if result["count"] != 2 || len(batch4BodySlice(t, result["errors"])) != 0 {
		t.Fatalf("result = %v", result)
	}
	messages := batch4BodySlice(t, result["messages"])
	if len(messages) != 2 || asMap(messages[1])["subject"] != "S2" {
		t.Fatalf("messages = %v", messages)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGmailCreateDraft::test_new_draft_happy_path
func TestBatch5Lane3GmailCreateDraft(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/drafts" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{
			"id":      "draft-1",
			"message": map[string]any{"id": "msg-9"},
		})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_create_draft", "html": "<p>Hello</p>",
		"to": "bob@example.com", "subject": "Greetings",
	})
	if result["draft_id"] != "draft-1" || result["message_id"] != "msg-9" {
		t.Fatalf("result = %v", result)
	}
	raw := asString(asMap(recorder.last(t).Body["message"])["raw"])
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("raw base64: %v", err)
	}
	message := string(decoded)
	if !strings.Contains(message, "To: bob@example.com") || !strings.Contains(message, "Subject: Greetings") ||
		!strings.Contains(message, "<p>Hello</p>") {
		t.Fatalf("message = %q", message)
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGmailCreateDraft::test_reply_draft_happy_path
func TestBatch5Lane3GmailCreateDraftReply(t *testing.T) {
	calls := 0
	originalBody := base64.URLEncoding.EncodeToString([]byte("<p>Original body</p>"))
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls++
		if calls == 1 {
			if r.Method != http.MethodGet || r.URL.Path != "/gmail/v1/users/me/messages/orig-1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id":       "orig-1",
				"threadId": "thread-1",
				"payload": map[string]any{
					"headers": []any{
						map[string]any{"name": "Message-ID", "value": "<orig@example.com>"},
						map[string]any{"name": "Subject", "value": "Original Subject"},
						map[string]any{"name": "From", "value": "alice@example.com"},
						map[string]any{"name": "Date", "value": "Mon, 1 Jan 2024 09:00:00 +0000"},
					},
					"parts": []any{map[string]any{
						"mimeType": "text/html",
						"body":     map[string]any{"data": originalBody},
					}},
				},
			})
			return
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "draft-2", "message": map[string]any{"id": "msg-10"}})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_create_draft", "html": "<p>My reply</p>", "reply_to_message_id": "orig-1",
	})
	if result["thread_id"] != "thread-1" {
		t.Fatalf("result = %v", result)
	}
	messageBody := asMap(recorder.last(t).Body["message"])
	if messageBody["threadId"] != "thread-1" {
		t.Fatalf("threadId = %v", messageBody["threadId"])
	}
	decoded, err := base64.URLEncoding.DecodeString(asString(messageBody["raw"]))
	if err != nil {
		t.Fatalf("raw base64: %v", err)
	}
	message := string(decoded)
	for _, want := range []string{
		"To: alice@example.com",
		"Subject: Re: Original Subject",
		"In-Reply-To: <orig@example.com>",
		"<p>My reply</p>",
		"<p>Original body</p>",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("message missing %q: %q", want, message)
		}
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGmailGetSignature::test_primary_alias
func TestBatch5Lane3GmailGetSignature(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/gmail/v1/users/me/settings/sendAs" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"sendAs": []any{
			map[string]any{"sendAsEmail": "alias@example.com", "signature": "alias"},
			map[string]any{"sendAsEmail": "me@example.com", "signature": "<b>Me</b>", "displayName": "Me", "isPrimary": true},
		}})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_get_signature",
	})
	if result["signature"] != "<b>Me</b>" || result["send_as_email"] != "me@example.com" || result["is_primary"] != true {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestGmailGetSignature::test_missing_scope_returns_helpful_error
func TestBatch5Lane3GmailGetSignatureMissingScope(t *testing.T) {
	registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{"action": "gmail_get_signature"})
	if message := batch4ErrorText(t, result); message != "Missing OAuth scope: gmail.settings.basic" {
		t.Fatalf("error = %q", message)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestListLabels::test_list_labels_success
func TestBatch5Lane3GmailListLabels(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/gmail/v1/users/me/labels" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"labels": []any{
			map[string]any{"id": "INBOX", "name": "INBOX", "type": "system"},
		}})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{"action": "gmail_list_labels"})
	if len(batch4BodySlice(t, result["labels"])) != 1 {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_gmail_tool.py::TestCreateLabel::test_create_label_success
func TestBatch5Lane3GmailCreateLabel(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/gmail/v1/users/me/labels" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "Label_1", "name": "Agent/Important", "type": "user"})
	})
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_create_label", "name": "Agent/Important",
	})
	if result["id"] != "Label_1" || result["type"] != "user" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["labelListVisibility"] != "labelShow" || body["messageListVisibility"] != "show" {
		t.Fatalf("body = %v", body)
	}
}

// Parity-exempt: Go-side response mapping; upstream tests mock one status at a
// time and do not cover an empty 2xx body.
func TestBatch5Lane3GmailErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, "Gmail token expired or invalid"},
		{"not found", http.StatusNotFound, `{}`, "Message not found"},
		{"rate limit", http.StatusTooManyRequests, `{"error":"quota"}`, "Gmail API error (HTTP 429)"},
		{"api error", http.StatusBadRequest, `bad request`, "Gmail API error (HTTP 400): bad request"},
		{"empty", http.StatusOK, ``, "invalid JSON response"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			})
			result := batch4Execute(t, registry, "gmail", map[string]any{"action": "gmail_list_labels"})
			if message := batch4ErrorText(t, result); !containsAll(message, testCase.message) {
				t.Fatalf("error = %q, want %q", message, testCase.message)
			}
		})
	}
}
