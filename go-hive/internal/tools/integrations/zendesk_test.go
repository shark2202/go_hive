package integrations

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func batch6Lane2ZendeskEnv() map[string]string {
	return map[string]string{
		"ZENDESK_SUBDOMAIN": "acme",
		"ZENDESK_EMAIL":     "agent@acme.test",
		"ZENDESK_API_TOKEN": "secret",
	}
}

func batch6Lane2ZendeskFixture() map[string]any {
	return map[string]any{
		"id":           123,
		"subject":      "Printer issue",
		"description":  "Not printing",
		"status":       "open",
		"priority":     "high",
		"type":         "problem",
		"tags":         []any{"hardware"},
		"requester_id": 100,
		"assignee_id":  200,
		"created_at":   "2024-01-01T00:00:00Z",
		"updated_at":   "2024-01-15T00:00:00Z",
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskListTickets::test_successful_list
func TestBatch6Lane2ZendeskListTickets(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"tickets": []any{batch6Lane2ZendeskFixture()},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_list_tickets",
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("page[size]"); got != "25" {
		t.Fatalf("page[size] = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Basic "+
		base64.StdEncoding.EncodeToString([]byte("agent@acme.test/token:secret")) {
		t.Fatalf("Authorization = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	ticket := batch6Lane2FieldSlice(t, result, "tickets")[0].(map[string]any)
	if ticket["subject"] != "Printer issue" || ticket["priority"] != "high" {
		t.Fatalf("ticket = %v", ticket)
	}
	tags := ticket["tags"].([]string)
	if len(tags) != 1 || tags[0] != "hardware" {
		t.Fatalf("tags = %v", tags)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskListTickets::test_missing_credentials
func TestBatch6Lane2ZendeskMissingCredentials(t *testing.T) {
	registry := newBatch6Lane2Registry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{"action": "zendesk_list_tickets"})
	if got := batch6Lane2ErrorText(t, result); got != "ZENDESK_SUBDOMAIN, ZENDESK_EMAIL, and ZENDESK_API_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskGetTicket::test_successful_get
func TestBatch6Lane2ZendeskGetTicket(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"ticket": batch6Lane2ZendeskFixture(),
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_get_ticket", "ticket_id": 123,
	})
	if recorder.at(t, 0).Path != "/tickets/123" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["subject"] != "Printer issue" || result["priority"] != "high" {
		t.Fatalf("result = %v", result)
	}
	batch6Lane2EqualInt(t, result["requester_id"], 100)
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskGetTicket::test_missing_id
func TestBatch6Lane2ZendeskGetTicketValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_get_ticket", "ticket_id": 0,
	})
	if got := batch6Lane2ErrorText(t, result); got != "ticket_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskCreateTicket::test_successful_create
func TestBatch6Lane2ZendeskCreateTicket(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{
				"ticket": map[string]any{"id": 456, "subject": "New ticket", "status": "new"},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_create_ticket", "subject": "New ticket", "body": "Help needed",
		"ticket_type": "question", "tags": "vip, urgent",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/tickets" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	ticket := batch6Lane2FieldMap(t, request.Body, "ticket")
	if ticket["priority"] != "normal" || ticket["type"] != "question" {
		t.Fatalf("ticket = %v", ticket)
	}
	comment := batch6Lane2FieldMap(t, ticket, "comment")
	if comment["body"] != "Help needed" {
		t.Fatalf("comment = %v", comment)
	}
	if tags := batch6Lane2FieldSlice(t, ticket, "tags"); len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	if result["result"] != "created" {
		t.Fatalf("result = %v", result)
	}
	batch6Lane2EqualInt(t, result["id"], 456)
	if got := result["url"]; got != "https://acme.zendesk.com/agent/tickets/456" {
		t.Fatalf("url = %v", got)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskCreateTicket::test_missing_params
func TestBatch6Lane2ZendeskCreateTicketValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_create_ticket", "subject": "", "body": "",
	})
	if got := batch6Lane2ErrorText(t, result); got != "subject and body are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskUpdateTicket::test_successful_update
func TestBatch6Lane2ZendeskUpdateTicket(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			updated := batch6Lane2ZendeskFixture()
			updated["status"] = "pending"
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"ticket": updated})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_update_ticket", "ticket_id": 123, "status": "pending",
		"comment": "Working on it", "comment_public": false, "tags": "hardware",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPut || request.Path != "/tickets/123" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	ticket := batch6Lane2FieldMap(t, request.Body, "ticket")
	if ticket["status"] != "pending" {
		t.Fatalf("ticket = %v", ticket)
	}
	comment := batch6Lane2FieldMap(t, ticket, "comment")
	if comment["body"] != "Working on it" || comment["public"] != false {
		t.Fatalf("comment = %v", comment)
	}
	if result["status"] != "pending" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskUpdateTicket::test_missing_id
func TestBatch6Lane2ZendeskUpdateTicketValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	missingID := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_update_ticket", "ticket_id": 0,
	})
	if got := batch6Lane2ErrorText(t, missingID); got != "ticket_id is required" {
		t.Fatalf("error = %q", got)
	}
	empty := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_update_ticket", "ticket_id": 7,
	})
	if got := batch6Lane2ErrorText(t, empty); got != "At least one field to update is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskSearchTickets::test_successful_search
func TestBatch6Lane2ZendeskSearchTickets(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"results": []any{map[string]any{
					"id": 123, "subject": "Printer issue", "status": "open", "priority": "high",
				}},
				"count": 1,
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_search_tickets", "query": "status:open priority:high",
	})
	request := recorder.at(t, 0)
	if request.Path != "/search" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("query"); got != "type:ticket status:open priority:high" {
		t.Fatalf("query = %q", got)
	}
	if got := request.Query.Get("sort_by"); got != "updated_at" {
		t.Fatalf("sort_by = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	first := batch6Lane2FieldSlice(t, result, "results")[0].(map[string]any)
	if first["subject"] != "Printer issue" {
		t.Fatalf("results = %v", result["results"])
	}
}

// Parity: tools/tests/tools/test_zendesk_tool.py::TestZendeskSearchTickets::test_missing_query
func TestBatch6Lane2ZendeskSearchTicketsValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_search_tickets", "query": "",
	})
	if got := batch6Lane2ErrorText(t, result); got != "query is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream zendesk_tool tests stop at search_tickets; comments,
// the comment writer, and list_users are uncovered there.
func TestBatch6Lane2ZendeskTicketComments(t *testing.T) {
	longBody := strings.Repeat("x", 600)
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"comments": []any{map[string]any{
					"id": 9, "body": longBody, "author_id": 4, "created_at": "2024-01-02T00:00:00Z",
				}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_get_ticket_comments", "ticket_id": 123, "page_size": 10,
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets/123/comments" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("page[size]"); got != "10" {
		t.Fatalf("page[size] = %q", got)
	}
	batch6Lane2EqualInt(t, result["ticket_id"], 123)
	comment := batch6Lane2FieldSlice(t, result, "comments")[0].(map[string]any)
	if got := comment["body"].(string); len([]rune(got)) != 500 {
		t.Fatalf("body length = %d, want 500", len([]rune(got)))
	}
	if comment["public"] != true {
		t.Fatalf("public default = %v", comment["public"])
	}
}

// Parity-exempt: upstream zendesk_tool has no test for add_ticket_comment.
func TestBatch6Lane2ZendeskAddTicketComment(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"ticket": map[string]any{"id": 123, "subject": "Printer issue", "status": "open"},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_add_ticket_comment", "ticket_id": 123, "body": "On it",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPut {
		t.Fatalf("method = %s", request.Method)
	}
	ticket := batch6Lane2FieldMap(t, request.Body, "ticket")
	comment := batch6Lane2FieldMap(t, ticket, "comment")
	if comment["body"] != "On it" || comment["public"] != true {
		t.Fatalf("comment = %v", comment)
	}
	if result["result"] != "comment_added" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream zendesk_tool has no test for list_users.
func TestBatch6Lane2ZendeskListUsers(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"users": []any{map[string]any{
					"id": 7, "name": "Agent", "email": "a@acme.test", "role": "agent",
					"active": true, "created_at": "2024-01-01T00:00:00Z",
				}},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{
		"action": "zendesk_list_users", "role": "agent", "page_size": 10,
	})
	request := recorder.at(t, 0)
	if request.Path != "/users" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("role"); got != "agent" {
		t.Fatalf("role = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	user := batch6Lane2FieldSlice(t, result, "users")[0].(map[string]any)
	if user["role"] != "agent" || user["active"] != true {
		t.Fatalf("user = %v", user)
	}
}

// Parity-exempt: upstream zendesk_tool has no error-mapping unit test; the
// strings mirror zendesk_tool._request.
func TestBatch6Lane2ZendeskErrorMapping(t *testing.T) {
	batch6Lane2ErrorTable(
		t, "zendesk_tool", "zendesk", batch6Lane2ZendeskEnv(),
		map[string]any{"action": "zendesk_list_tickets"},
		map[int]string{
			http.StatusUnauthorized:    "Unauthorized. Check your Zendesk credentials.",
			http.StatusForbidden:       "Forbidden. Check your Zendesk permissions.",
			http.StatusNotFound:        "Not found.",
			http.StatusTooManyRequests: "Rate limited. Try again shortly.",
		},
	)
}

// Parity-exempt: generic provider errors have no upstream unit test.
func TestBatch6Lane2ZendeskGenericError(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "zendesk_tool", batch6Lane2ZendeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		},
	)
	result := batch6Lane2Execute(t, registry, "zendesk", map[string]any{"action": "zendesk_list_tickets"})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Zendesk API error 500: ") || !strings.Contains(text, "boom") {
		t.Fatalf("error = %q", text)
	}
}
