package crm

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomClientHeaders::test_headers_contain_bearer_token
func TestBatch6Lane2IntercomHeaders(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"teams": []any{}})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{"action": "intercom_list_teams"})
	request := recorder.at(t, 0)
	if got := request.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("Intercom-Version"); got != "2.11" {
		t.Fatalf("Intercom-Version = %q", got)
	}
	if got := request.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomSearchConversations::test_no_filters_returns_recent
func TestBatch6Lane2IntercomSearchConversationsNoFilters(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"conversations": []any{map[string]any{"id": "conv-1"}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_search_conversations",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/conversations/search" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	query := batch6Lane2FieldMap(t, request.Body, "query")
	if query["field"] != "created_at" || query["operator"] != ">" {
		t.Fatalf("query = %v", query)
	}
	batch6Lane2EqualInt(t, query["value"], 0)
	pagination := batch6Lane2FieldMap(t, request.Body, "pagination")
	batch6Lane2EqualInt(t, pagination["per_page"], 20)
	if count, _ := result["conversations"].([]any); len(count) != 1 {
		t.Fatalf("conversations = %v", result["conversations"])
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomSearchConversations::test_status_filter_applied
func TestBatch6Lane2IntercomSearchConversationsFilters(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/tags":
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
					"data": []any{map[string]any{"id": "tag-9", "name": "vip"}},
				})
			case r.Method == http.MethodPost && r.URL.Path == "/conversations/search":
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"conversations": []any{}})
			default:
				t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action":        "intercom_search_conversations",
		"status":        "open",
		"assignee_id":   "admin-2",
		"tag":           "vip",
		"created_after": "2026-01-15",
		"limit":         5,
	})
	if got := recorder.count(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	search := recorder.at(t, 1)
	if search.Path != "/conversations/search" {
		t.Fatalf("path = %s", search.Path)
	}
	query := batch6Lane2FieldMap(t, search.Body, "query")
	if query["operator"] != "AND" {
		t.Fatalf("query = %v", query)
	}
	filters := batch6Lane2FieldSlice(t, query, "value")
	if len(filters) != 4 {
		t.Fatalf("filters = %v", filters)
	}
	for _, raw := range filters {
		filter := raw.(map[string]any)
		if _, ok := filter["field"].(string); !ok {
			t.Fatalf("filter missing field: %v", filter)
		}
	}
	tagFilter := filters[2].(map[string]any)
	batch6Lane2FieldSlice(t, tagFilter, "value")
	created := filters[3].(map[string]any)
	wantTS := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC).Unix()
	if got := batch6Lane2Number(t, created["value"]); got != wantTS {
		t.Fatalf("created_at filter = %d, want %d", got, wantTS)
	}
	pagination := batch6Lane2FieldMap(t, search.Body, "pagination")
	batch6Lane2EqualInt(t, pagination["per_page"], 5)
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomSearchConversations::test_invalid_status, tools/tests/tools/test_intercom_tool.py::TestIntercomSearchConversations::test_invalid_limit_too_high, tools/tests/tools/test_intercom_tool.py::TestIntercomSearchConversations::test_invalid_created_after
func TestBatch6Lane2IntercomSearchConversationsValidation(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"conversations": []any{}})
		},
	)
	cases := []map[string]any{
		{"action": "intercom_search_conversations", "limit": 0},
		{"action": "intercom_search_conversations", "limit": 151},
		{"action": "intercom_search_conversations", "status": "pending"},
		{"action": "intercom_search_conversations", "created_after": "not-a-date"},
	}
	for _, input := range cases {
		result := batch6Lane2Execute(t, registry, "intercom", input)
		if batch6Lane2ErrorText(t, result) == "" {
			t.Fatalf("input %v produced no error", input)
		}
	}
	if got := recorder.count(); got != 0 {
		t.Fatalf("validation issued %d requests", got)
	}
}

// Parity-exempt: upstream has no test for an unknown tag name; the not-found
// branch mirrors TestIntercomGetContact::test_email_not_found's error style.
func TestBatch6Lane2IntercomSearchConversationsUnknownTag(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{"id": "tag-1", "name": "other"}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_search_conversations", "tag": "vip",
	})
	if got := batch6Lane2ErrorText(t, result); got != "Tag not found: vip" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomGetConversation::test_success
func TestBatch6Lane2IntercomGetConversation(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "conv-1", "state": "open"})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_get_conversation", "conversation_id": "conv-1",
	})
	request := recorder.at(t, 0)
	if request.Path != "/conversations/conv-1" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("display_as"); got != "plaintext" {
		t.Fatalf("display_as = %q", got)
	}
	if result["state"] != "open" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomGetContact::test_by_id
func TestBatch6Lane2IntercomGetContactByID(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "contact-1", "email": "a@b.c"})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_get_contact", "contact_id": "contact-1",
	})
	if recorder.at(t, 0).Path != "/contacts/contact-1" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["email"] != "a@b.c" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomGetContact::test_by_email_fallback
func TestBatch6Lane2IntercomGetContactByEmail(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]any{"id": "contact-2", "email": "x@y.z"}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_get_contact", "email": "x@y.z",
	})
	request := recorder.at(t, 0)
	if request.Path != "/contacts/search" {
		t.Fatalf("path = %s", request.Path)
	}
	query := batch6Lane2FieldMap(t, request.Body, "query")
	if query["field"] != "email" || query["value"] != "x@y.z" {
		t.Fatalf("query = %v", query)
	}
	pagination := batch6Lane2FieldMap(t, request.Body, "pagination")
	batch6Lane2EqualInt(t, pagination["per_page"], 1)
	if result["id"] != "contact-2" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomGetContact::test_no_id_or_email
func TestBatch6Lane2IntercomGetContactRequiresTarget(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_get_contact",
	})
	if got := batch6Lane2ErrorText(t, result); got != "Either contact_id or email must be provided" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomSearchContacts::test_success
func TestBatch6Lane2IntercomSearchContacts(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"data": []any{}})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_search_contacts", "query": "jane", "limit": 30,
	})
	request := recorder.at(t, 0)
	if request.Path != "/contacts/search" {
		t.Fatalf("path = %s", request.Path)
	}
	query := batch6Lane2FieldMap(t, request.Body, "query")
	if query["operator"] != "OR" {
		t.Fatalf("query = %v", query)
	}
	if filters := batch6Lane2FieldSlice(t, query, "value"); len(filters) != 2 {
		t.Fatalf("filters = %v", filters)
	}
	pagination := batch6Lane2FieldMap(t, request.Body, "pagination")
	batch6Lane2EqualInt(t, pagination["per_page"], 30)
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomAddNote::test_success
func TestBatch6Lane2IntercomAddNote(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if r.URL.Path == "/me" {
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "admin-1"})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"type": "conversation_part"})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_note", "conversation_id": "conv-1", "body": "Internal note",
	})
	if got := recorder.count(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	reply := recorder.at(t, 1)
	if reply.Path != "/conversations/conv-1/reply" {
		t.Fatalf("path = %s", reply.Path)
	}
	if reply.Body["message_type"] != "note" || reply.Body["body"] != "Internal note" {
		t.Fatalf("body = %v", reply.Body)
	}
	if reply.Body["admin_id"] != "admin-1" || reply.Body["type"] != "admin" {
		t.Fatalf("body = %v", reply.Body)
	}
	if result["type"] != "conversation_part" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomAddTag::test_tag_conversation_success
func TestBatch6Lane2IntercomAddTagConversation(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/tags":
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "tag-1", "name": "vip"})
			case r.URL.Path == "/me":
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "admin-1"})
			default:
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"tags": []any{}})
			}
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_tag", "name": "vip", "conversation_id": "conv-1",
	})
	if got := recorder.count(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
	tagRequest := recorder.at(t, 2)
	if tagRequest.Path != "/conversations/conv-1/tags" {
		t.Fatalf("path = %s", tagRequest.Path)
	}
	if tagRequest.Body["id"] != "tag-1" || tagRequest.Body["admin_id"] != "admin-1" {
		t.Fatalf("body = %v", tagRequest.Body)
	}
}

// Parity-exempt: upstream only tests the conversation branch of
// intercom_add_tag (TestIntercomAddTag::test_tag_conversation_success); the
// contact branch mirrors the same create-then-attach flow without /me.
func TestBatch6Lane2IntercomAddTagContact(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if r.URL.Path == "/tags" {
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "tag-1"})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"tags": []any{}})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_tag", "name": "vip", "contact_id": "contact-1",
	})
	if got := recorder.count(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	attach := recorder.at(t, 1)
	if attach.Path != "/contacts/contact-1/tags" {
		t.Fatalf("path = %s", attach.Path)
	}
	if attach.Body["id"] != "tag-1" {
		t.Fatalf("body = %v", attach.Body)
	}
	if _, present := attach.Body["admin_id"]; present {
		t.Fatalf("contact tag body should not carry admin_id: %v", attach.Body)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomAddTag::test_must_provide_target, tools/tests/tools/test_intercom_tool.py::TestIntercomAddTag::test_cannot_provide_both_targets
func TestBatch6Lane2IntercomAddTagValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	missing := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_tag", "name": "vip",
	})
	if got := batch6Lane2ErrorText(t, missing); got != "Either conversation_id or contact_id must be provided" {
		t.Fatalf("error = %q", got)
	}
	both := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_tag", "name": "vip", "conversation_id": "c1", "contact_id": "ct1",
	})
	if got := batch6Lane2ErrorText(t, both); got != "Provide conversation_id or contact_id, not both" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomAssignConversation::test_success
func TestBatch6Lane2IntercomAssignConversation(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if r.URL.Path == "/me" {
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "admin-1"})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "conv-1"})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_assign_conversation", "conversation_id": "conv-1", "assignee_id": "admin-2",
	})
	request := recorder.at(t, 1)
	if request.Path != "/conversations/conv-1/parts" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Body["assignee_id"] != "admin-2" || request.Body["assignee_type"] != "admin" {
		t.Fatalf("body = %v", request.Body)
	}
	if request.Body["message_type"] != "assignment" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomAssignConversation::test_invalid_assignee_type
func TestBatch6Lane2IntercomAssignConversationValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_assign_conversation", "conversation_id": "c1",
		"assignee_id": "1", "assignee_type": "bot",
	})
	if got := batch6Lane2ErrorText(t, result); got != "assignee_type must be 'admin' or 'team'" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomListTeams::test_success
func TestBatch6Lane2IntercomListTeams(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"teams": []any{map[string]any{"id": "t1", "name": "Support"}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{"action": "intercom_list_teams"})
	if recorder.at(t, 0).Path != "/teams" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if teams, _ := result["teams"].([]any); len(teams) != 1 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomCloseConversation::test_success
func TestBatch6Lane2IntercomCloseConversation(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if r.URL.Path == "/me" {
				batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "admin-1"})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"state": "closed"})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_close_conversation", "conversation_id": "conv-1", "body": "Done",
	})
	request := recorder.at(t, 1)
	if request.Body["message_type"] != "close" || request.Body["body"] != "Done" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomCreateContact::test_success
func TestBatch6Lane2IntercomCreateContact(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": "contact-1"})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_create_contact", "email": "jane@acme.test", "name": "Jane",
	})
	request := recorder.at(t, 0)
	if request.Path != "/contacts" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Body["role"] != "user" || request.Body["email"] != "jane@acme.test" {
		t.Fatalf("body = %v", request.Body)
	}
	for _, absent := range []string{"phone", "external_id"} {
		if _, present := request.Body[absent]; present {
			t.Fatalf("body unexpectedly carries %s: %v", absent, request.Body)
		}
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomCreateContact::test_invalid_role
func TestBatch6Lane2IntercomCreateContactValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_create_contact", "role": "admin",
	})
	if got := batch6Lane2ErrorText(t, result); got != "role must be 'user' or 'lead'" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomListConversations::test_success
func TestBatch6Lane2IntercomListConversations(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"conversations": []any{map[string]any{"id": "conv-1"}},
			})
		},
	)
	batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_list_conversations", "limit": 5, "starting_after": "cursor-9",
	})
	request := recorder.at(t, 0)
	if request.Path != "/conversations" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("per_page"); got != "5" {
		t.Fatalf("per_page = %q", got)
	}
	if got := request.Query.Get("display_as"); got != "plaintext" {
		t.Fatalf("display_as = %q", got)
	}
	if got := request.Query.Get("starting_after"); got != "cursor-9" {
		t.Fatalf("starting_after = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomClientHandleResponse::test_known_error_codes
func TestBatch6Lane2IntercomErrorMapping(t *testing.T) {
	batch6Lane2ErrorTable(
		t, "intercom_tool", "intercom", nil,
		map[string]any{"action": "intercom_list_teams"},
		map[int]string{
			http.StatusUnauthorized:    "Invalid or expired Intercom access token",
			http.StatusForbidden:       "Insufficient permissions. Check your Intercom app scopes.",
			http.StatusNotFound:        "Resource not found",
			http.StatusTooManyRequests: "Intercom rate limit exceeded. Try again later.",
		},
	)
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomClientHandleResponse::test_generic_error_fallback_to_text
func TestBatch6Lane2IntercomGenericError(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream exploded"))
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{"action": "intercom_list_teams"})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Intercom API error (HTTP 500): ") || !strings.Contains(text, "upstream exploded") {
		t.Fatalf("error = %q", text)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomClientHandleResponse::test_intercom_error_list_format
func TestBatch6Lane2IntercomErrorListDetail(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusBadRequest, map[string]any{
				"type":   "error.list",
				"errors": []any{map[string]any{"message": "Parameter invalid"}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{"action": "intercom_list_teams"})
	if got := batch6Lane2ErrorText(t, result); got != "Intercom API error (HTTP 400): Parameter invalid" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomClientAdminId::test_returns_error_on_failure
func TestBatch6Lane2IntercomAdminIDFailure(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "intercom_tool", nil,
		batch6Lane2StatusHandler(http.StatusInternalServerError, map[string]any{"errors": []any{}}),
	)
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{
		"action": "intercom_add_note", "conversation_id": "conv-1", "body": "note",
	})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Intercom API error (HTTP 500): ") {
		t.Fatalf("error = %q", text)
	}
	if got := recorder.count(); got != 1 {
		t.Fatalf("requests = %d, want only the /me call", got)
	}
}

// Parity: tools/tests/tools/test_intercom_tool.py::TestIntercomCredentials::test_no_credentials_returns_error
func TestBatch6Lane2IntercomMissingCredential(t *testing.T) {
	registry := NewSupportRegistry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane2Execute(t, registry, "intercom", map[string]any{"action": "intercom_list_teams"})
	if got := batch6Lane2ErrorText(t, result); got != "Intercom credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}
