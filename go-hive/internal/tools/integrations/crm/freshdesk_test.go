package crm

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func batch6Lane2FreshdeskEnv() map[string]string {
	return map[string]string{"FRESHDESK_DOMAIN": "acme.freshdesk.com"}
}

func batch6Lane2FreshdeskFixture() map[string]any {
	return map[string]any{
		"id":           123,
		"subject":      "Printer jam",
		"description":  "Paper stuck",
		"status":       2,
		"priority":     3,
		"type":         "Question",
		"tags":         []any{"hardware"},
		"requester_id": 100,
		"responder_id": 200,
		"created_at":   "2024-01-01T00:00:00Z",
		"updated_at":   "2024-01-15T00:00:00Z",
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestHelpers::test_base_url_plain_domain, tools/tests/tools/test_freshdesk_tool.py::TestHelpers::test_base_url_strips_https, tools/tests/tools/test_freshdesk_tool.py::TestHelpers::test_base_url_strips_http, tools/tests/tools/test_freshdesk_tool.py::TestHelpers::test_base_url_strips_whitespace
func TestBatch6Lane2FreshdeskBaseURL(t *testing.T) {
	runtime := &Runtime{Modules: map[string]*ModuleConfig{}}
	cases := map[string]string{
		"acme.freshdesk.com":         "https://acme.freshdesk.com/api/v2",
		"https://acme.freshdesk.com": "https://acme.freshdesk.com/api/v2",
		"http://acme.freshdesk.com":  "https://acme.freshdesk.com/api/v2",
		"  acme.freshdesk.com  ":     "https://acme.freshdesk.com/api/v2",
	}
	for domain, want := range cases {
		if got := freshdeskBaseURL(runtime, domain); got != want {
			t.Fatalf("base URL for %q = %q, want %q", domain, got, want)
		}
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestHelpers::test_auth_header_format
func TestBatch6Lane2FreshdeskAuthHeader(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{})
		},
	)
	batch6Lane2Execute(t, registry, "freshdesk", map[string]any{"action": "freshdesk_list_tickets"})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-token:X"))
	if got := recorder.at(t, 0).Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q, want %q", got, want)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListTickets::test_success_single_ticket
func TestBatch6Lane2FreshdeskListTickets(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{batch6Lane2FreshdeskFixture()})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_tickets", "page": 2, "per_page": 500,
		"filter": "new_and_my_open", "updated_since": "2026-01-01T00:00:00Z",
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("page"); got != "2" {
		t.Fatalf("page = %q", got)
	}
	if got := request.Query.Get("per_page"); got != "100" {
		t.Fatalf("per_page = %q", got)
	}
	if got := request.Query.Get("filter"); got != "new_and_my_open" {
		t.Fatalf("filter = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	ticket := batch6Lane2FieldSlice(t, result, "tickets")[0].(map[string]any)
	if ticket["subject"] != "Printer jam" {
		t.Fatalf("ticket = %v", ticket)
	}
	batch6Lane2EqualInt(t, ticket["status"], 2)
	batch6Lane2EqualInt(t, ticket["priority"], 3)
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskFilterTickets::test_success_query_wrapped_in_quotes, tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskFilterTickets::test_page_param_clamped_to_max_10
func TestBatch6Lane2FreshdeskFilterTickets(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"total":   2,
				"results": []any{batch6Lane2FreshdeskFixture(), batch6Lane2FreshdeskFixture()},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_filter_tickets", "query": "priority:3", "page": 15,
	})
	request := recorder.at(t, 0)
	if request.Path != "/search/tickets" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("query"); got != "\"priority:3\"" {
		t.Fatalf("query = %q", got)
	}
	if got := request.Query.Get("page"); got != "10" {
		t.Fatalf("page = %q", got)
	}
	batch6Lane2EqualInt(t, result["total"], 2)
	batch6Lane2EqualInt(t, result["count"], 2)
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListTicketConversations::test_success_with_conversations
func TestBatch6Lane2FreshdeskListTicketConversations(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 1, "body_text": "First reply", "private": false, "user_id": 10,
				"created_at": "2026-03-01T02:00:00Z",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_ticket_conversations", "ticket_id": 123, "page": 2, "per_page": 10,
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets/123/conversations" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("page"); got != "2" {
		t.Fatalf("page = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	conversation := batch6Lane2FieldSlice(t, result, "conversations")[0].(map[string]any)
	if conversation["body_text"] != "First reply" || conversation["private"] != false {
		t.Fatalf("conversation = %v", conversation)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetTicket::test_success_returns_ticket
func TestBatch6Lane2FreshdeskGetTicket(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, batch6Lane2FreshdeskFixture())
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_ticket", "ticket_id": 123,
	})
	if recorder.at(t, 0).Path != "/tickets/123" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["subject"] != "Printer jam" {
		t.Fatalf("result = %v", result)
	}
	batch6Lane2EqualInt(t, result["requester_id"], 100)
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskCreateTicket::test_success_minimal
func TestBatch6Lane2FreshdeskCreateTicket(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{
				"id": 55, "subject": "New ticket", "status": 2,
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_create_ticket", "email": "jane@acme.test",
		"subject": "New ticket", "description": "Help", "priority": 3, "tags": "vip, urgent",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/tickets" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["email"] != "jane@acme.test" || request.Body["description"] != "Help" {
		t.Fatalf("body = %v", request.Body)
	}
	batch6Lane2EqualInt(t, request.Body["priority"], 3)
	if tags, _ := request.Body["tags"].([]any); len(tags) != 2 {
		t.Fatalf("tags = %v", request.Body["tags"])
	}
	batch6Lane2EqualInt(t, result["id"], 55)
	if result["url"] != "https://acme.freshdesk.com/a/tickets/55" {
		t.Fatalf("url = %v", result["url"])
	}
	if result["result"] != "created" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskUpdateTicket::test_success_update_status_only
func TestBatch6Lane2FreshdeskUpdateTicket(t *testing.T) {
	updated := batch6Lane2FreshdeskFixture()
	updated["status"] = 3
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if strings.HasSuffix(r.URL.Path, "/notes") {
				batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{"id": 9})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, updated)
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_update_ticket", "ticket_id": 123, "status": 3,
		"tags": "hardware", "note": "Checked", "note_private": false,
	})
	if got := recorder.count(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	update := recorder.at(t, 0)
	if update.Method != http.MethodPut || update.Path != "/tickets/123" {
		t.Fatalf("request = %s %s", update.Method, update.Path)
	}
	batch6Lane2EqualInt(t, update.Body["status"], 3)
	note := recorder.at(t, 1)
	if note.Path != "/tickets/123/notes" {
		t.Fatalf("note path = %s", note.Path)
	}
	if note.Body["body"] != "Checked" || note.Body["private"] != false {
		t.Fatalf("note body = %v", note.Body)
	}
	batch6Lane2EqualInt(t, result["id"], 123)
}

// Parity-exempt: upstream only covers the update-with-changes branch; the
// note-only branch fetches the ticket before posting the note.
func TestBatch6Lane2FreshdeskUpdateTicketNoteOnly(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if strings.HasSuffix(r.URL.Path, "/notes") {
				batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{"id": 9})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, batch6Lane2FreshdeskFixture())
		},
	)
	batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_update_ticket", "ticket_id": 123, "note": "Note only",
	})
	if got := recorder.count(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if recorder.at(t, 0).Method != http.MethodGet {
		t.Fatalf("first request = %s", recorder.at(t, 0).Method)
	}
	if recorder.at(t, 1).Body["private"] != true {
		t.Fatalf("note default private = %v", recorder.at(t, 1).Body["private"])
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskUpdateTicket::test_missing_updates
func TestBatch6Lane2FreshdeskUpdateTicketValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_update_ticket", "ticket_id": 123,
	})
	if got := batch6Lane2ErrorText(t, result); !strings.Contains(got, "At least one field") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskAddTicketReply::test_success_public_reply_default
func TestBatch6Lane2FreshdeskAddTicketReply(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{
				"id": 9, "body_text": "On it", "created_at": "2026-03-01T02:00:00Z",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_add_ticket_reply", "ticket_id": 123, "body": "On it",
		"from_email": "agent@acme.test",
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets/123/reply" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Body["from_email"] != "agent@acme.test" {
		t.Fatalf("body = %v", request.Body)
	}
	if result["body"] != "On it" || result["public"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream covers only the public reply branch; a private reply
// becomes a /notes post with private=true.
func TestBatch6Lane2FreshdeskAddTicketReplyPrivate(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{"id": 9, "body": "Internal"})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_add_ticket_reply", "ticket_id": 123, "body": "Internal", "public": false,
	})
	request := recorder.at(t, 0)
	if request.Path != "/tickets/123/notes" {
		t.Fatalf("path = %s", request.Path)
	}
	if request.Body["private"] != true {
		t.Fatalf("body = %v", request.Body)
	}
	if result["public"] != false || result["body"] != "Internal" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListContacts::test_success_with_email_filter
func TestBatch6Lane2FreshdeskListContacts(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 5, "name": "Jane", "email": "jane@acme.test", "active": true,
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_contacts", "email": "jane@acme.test",
	})
	request := recorder.at(t, 0)
	if request.Path != "/contacts" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("email"); got != "jane@acme.test" {
		t.Fatalf("email = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	contact := batch6Lane2FieldSlice(t, result, "contacts")[0].(map[string]any)
	if contact["name"] != "Jane" || contact["active"] != true {
		t.Fatalf("contact = %v", contact)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetContact::test_success_by_id
func TestBatch6Lane2FreshdeskGetContactByID(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id": 5, "name": "Jane", "email": "jane@acme.test",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_contact", "contact_id": 5,
	})
	if recorder.at(t, 0).Path != "/contacts/5" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetContact::test_success_by_email
func TestBatch6Lane2FreshdeskGetContactByEmail(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 5, "name": "Jane", "email": "jane@acme.test",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_contact", "email": "jane@acme.test",
	})
	request := recorder.at(t, 0)
	if request.Path != "/contacts" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("email"); got != "jane@acme.test" {
		t.Fatalf("email = %q", got)
	}
	if result["email"] != "jane@acme.test" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream has no not-found test for get_contact by email.
func TestBatch6Lane2FreshdeskGetContactNotFound(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_contact", "email": "ghost@acme.test",
	})
	if got := batch6Lane2ErrorText(t, result); got != "Contact not found" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskCreateContact::test_success_with_name_phone_company_id
func TestBatch6Lane2FreshdeskCreateContact(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{
				"id": 5, "name": "Jane", "email": "jane@acme.test", "phone": "555",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_create_contact", "email": "jane@acme.test",
		"name": "Jane", "phone": "555", "company_id": 7,
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/contacts" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	batch6Lane2EqualInt(t, request.Body["company_id"], 7)
	if request.Body["name"] != "Jane" {
		t.Fatalf("body = %v", request.Body)
	}
	if result["name"] != "Jane" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskUpdateContact::test_success_update
func TestBatch6Lane2FreshdeskUpdateContact(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id": 5, "name": "Jane Doe", "company_id": -1,
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_update_contact", "contact_id": 5,
		"name": "Jane Doe", "company_id": -1,
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPut || request.Path != "/contacts/5" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["name"] != "Jane Doe" {
		t.Fatalf("body = %v", request.Body)
	}
	batch6Lane2EqualInt(t, request.Body["company_id"], -1)
	if result["name"] != "Jane Doe" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListAgents::test_success_single_agent
func TestBatch6Lane2FreshdeskListAgents(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 8, "contact_id": 9, "email": "agent@acme.test",
				"occasional": false, "available": true,
				"contact": map[string]any{"name": "Agent Smith"},
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_agents", "page": 1, "per_page": 10,
	})
	if recorder.at(t, 0).Path != "/agents" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	agent := batch6Lane2FieldSlice(t, result, "agents")[0].(map[string]any)
	if agent["name"] != "Agent Smith" || agent["available"] != true {
		t.Fatalf("agent = %v", agent)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetAgent::test_success_returns_agent
func TestBatch6Lane2FreshdeskGetAgent(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id": 8, "email": "agent@acme.test", "contact": map[string]any{"name": "Agent Smith"},
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_agent", "agent_id": 8,
	})
	if recorder.at(t, 0).Path != "/agents/8" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "Agent Smith" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListGroups::test_success_single_group
func TestBatch6Lane2FreshdeskListGroups(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 3, "name": "Support", "description": "Tier 1", "unassigned_for": "1h",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_groups",
	})
	if recorder.at(t, 0).Path != "/groups" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	group := batch6Lane2FieldSlice(t, result, "groups")[0].(map[string]any)
	if group["name"] != "Support" || group["unassigned_for"] != "1h" {
		t.Fatalf("group = %v", group)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetGroup::test_success_returns_group
func TestBatch6Lane2FreshdeskGetGroup(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": 3, "name": "Support"})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_group", "group_id": 3,
	})
	if recorder.at(t, 0).Path != "/groups/3" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "Support" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskListCompanies::test_success_single_company
func TestBatch6Lane2FreshdeskListCompanies(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 4, "name": "Acme", "domains": []any{"acme.test"}, "note": "key account",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_list_companies", "updated_since": "2026-01-01T00:00:00Z",
	})
	request := recorder.at(t, 0)
	if request.Path != "/companies" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("updated_since"); got != "2026-01-01T00:00:00Z" {
		t.Fatalf("updated_since = %q", got)
	}
	company := batch6Lane2FieldSlice(t, result, "companies")[0].(map[string]any)
	if company["name"] != "Acme" {
		t.Fatalf("company = %v", company)
	}
	if domains, _ := company["domains"].([]string); len(domains) != 1 {
		t.Fatalf("domains = %v", company["domains"])
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskGetCompany::test_success_returns_company
func TestBatch6Lane2FreshdeskGetCompany(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{"id": 4, "name": "Acme"})
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_get_company", "company_id": 4,
	})
	if recorder.at(t, 0).Path != "/companies/4" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "Acme" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskFilterTickets::test_api_returns_401
func TestBatch6Lane2FreshdeskErrorMapping(t *testing.T) {
	batch6Lane2ErrorTable(
		t, "freshdesk_tool", "freshdesk", batch6Lane2FreshdeskEnv(),
		map[string]any{"action": "freshdesk_list_tickets"},
		map[int]string{
			http.StatusUnauthorized:    "Unauthorized. Check your Freshdesk API key.",
			http.StatusForbidden:       "Forbidden. Check your Freshdesk permissions.",
			http.StatusNotFound:        "Not found.",
			http.StatusTooManyRequests: "Rate limited. Try again shortly.",
		},
	)
}

// Parity-exempt: generic provider errors and unparseable bodies have no
// upstream unit test.
func TestBatch6Lane2FreshdeskGenericErrors(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("bad gateway"))
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{"action": "freshdesk_list_tickets"})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Freshdesk API error 502: ") || !strings.Contains(text, "bad gateway") {
		t.Fatalf("error = %q", text)
	}

	broken, _ := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		},
	)
	parsed := batch6Lane2Execute(t, broken, "freshdesk", map[string]any{"action": "freshdesk_list_tickets"})
	if got := batch6Lane2ErrorText(t, parsed); got != "Failed to parse Freshdesk response" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestHealthCheckCredentialVerification::test_missing_api_key_from_env
func TestBatch6Lane2FreshdeskMissingKey(t *testing.T) {
	registry := NewSupportRegistry(WithGetenv(func(name string) string {
		if name == "FRESHDESK_DOMAIN" {
			return "acme.freshdesk.com"
		}
		return ""
	}))
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{"action": "freshdesk_list_tickets"})
	if got := batch6Lane2ErrorText(t, result); got != "FRESHDESK_API_KEY environment variable not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestHealthCheckCredentialVerification::test_missing_domain_from_env
func TestBatch6Lane2FreshdeskMissingDomain(t *testing.T) {
	registry := NewSupportRegistry(
		WithCredential("freshdesk_tool", "key"),
		WithGetenv(func(string) string { return "" }),
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{"action": "freshdesk_list_tickets"})
	if got := batch6Lane2ErrorText(t, result); got != "FRESHDESK_DOMAIN is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskCreateTicket::test_success_minimal
func TestBatch6Lane2FreshdeskCreateTicketMinimal(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{"id": 56})
		},
	)
	batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_create_ticket", "email": "jane@acme.test",
		"subject": "Minimal", "description": "Body",
	})
	request := recorder.at(t, 0)
	for _, absent := range []string{"priority", "status", "tags"} {
		if _, present := request.Body[absent]; present {
			t.Fatalf("body unexpectedly carries %s: %v", absent, request.Body)
		}
	}
	if request.Body["subject"] != "Minimal" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_freshdesk_tool.py::TestFreshdeskUpdateTicket::test_note_post_failure_surfaces_error
func TestBatch6Lane2FreshdeskNoteFailure(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "freshdesk_tool", batch6Lane2FreshdeskEnv(),
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			if strings.HasSuffix(r.URL.Path, "/notes") {
				batch6Lane2WriteJSON(w, http.StatusInternalServerError, map[string]any{"message": "nope"})
				return
			}
			batch6Lane2WriteJSON(w, http.StatusOK, batch6Lane2FreshdeskFixture())
		},
	)
	result := batch6Lane2Execute(t, registry, "freshdesk", map[string]any{
		"action": "freshdesk_update_ticket", "ticket_id": 123, "note": "Note",
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("ticket update should survive a note failure: %v", result)
	}
	if _, present := result["note_error"]; !present {
		t.Fatalf("note_error missing: %v", result)
	}
}
