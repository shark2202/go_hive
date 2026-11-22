package integrations

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/schedchat"
)

// batch7Lane8TestRegistry builds a lane-8 registry pointed at one fake
// upstream, with a credential supplied for the module under test.
func batch7Lane8TestRegistry(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	options := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
		WithCredential(module, "test-token"),
	}
	return newBatch7Lane8Registry(append(options, opts...)...), recorder
}

// batch7Lane8NoSleep removes the 429 backoff so retry tests stay fast.
func batch7Lane8NoSleep(t *testing.T) {
	t.Helper()
	previous := schedchat.RetrySleep
	schedchat.RetrySleep = func(time.Duration) {}
	t.Cleanup(func() { schedchat.RetrySleep = previous })
}

// batch7Lane8JSONText renders a decoded JSON value for body assertions.
func batch7Lane8JSONText(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not this registry surface.
func TestBatch7Lane8Catalog(t *testing.T) {
	registry := newBatch7Lane8Registry()
	names := registry.Names()
	wantNames := []string{"brevo", "calcom", "calendly", "discord", "mattermost"}
	if len(names) != len(wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	for index, name := range wantNames {
		if names[index] != name {
			t.Fatalf("tools = %v, want %v", names, wantNames)
		}
	}
	wantActions := map[string]int{
		"brevo": 9, "calcom": 9, "calendly": 8, "discord": 7, "mattermost": 7,
	}
	for _, tool := range registry.Tools() {
		if len(tool.Actions) != wantActions[tool.Name] {
			t.Fatalf("%s actions = %d, want %d", tool.Name, len(tool.Actions), wantActions[tool.Name])
		}
		if len(registry.Impls(tool.Name)) != len(tool.Actions) {
			t.Fatalf("%s impls = %d, want %d", tool.Name, len(registry.Impls(tool.Name)), len(tool.Actions))
		}
		for _, action := range tool.Actions {
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
	if got := registry.ActionCount(); got != 40 {
		t.Fatalf("actions = %d, want 40", got)
	}
	if got := registry.ImplementedActions(); got != 40 {
		t.Fatalf("implemented = %d, want 40", got)
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestBatch7Lane8Install(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch7Lane8(shared); installed != 5 {
		t.Fatalf("installed = %d, want 5", installed)
	}
	for _, name := range []string{"brevo", "calcom", "calendly", "discord", "mattermost"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("brevo")
	if !ok {
		t.Fatal("brevo tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema = %v", tool.Parameters)
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema = %v", properties["action"])
	}
	if items, ok := action["enum"].([]string); !ok || len(items) != 9 {
		t.Fatalf("action enum = %v", action["enum"])
	}
	if installed := InstallBatch7Lane8(nil); installed != 0 {
		t.Fatalf("nil install = %d, want 0", installed)
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoCredentials::test_no_credentials_returns_error
func TestBatch7Lane8BrevoMissingCredentials(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_send_email", "to_email": "user@example.com", "to_name": "Test User",
		"subject": "Test", "html_content": "<p>Test</p>", "from_email": "sender@example.com",
		"from_name": "Sender",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Brevo credentials not configured") {
		t.Fatalf("error = %q", got)
	}
	if _, ok := result["help"].(string); !ok {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestCredentialHandling::test_no_credentials_returns_error
func TestBatch7Lane8CalcomMissingCredentials(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "calcom", map[string]any{"action": "calcom_list_bookings"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "Cal.com API key not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyGetCurrentUser::test_missing_credentials
func TestBatch7Lane8CalendlyMissingCredentials(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "calendly", map[string]any{"action": "calendly_get_current_user"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "CALENDLY_PAT") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_discord_tool.py::test_missing_credentials_fail_loudly
func TestBatch7Lane8DiscordMissingCredentials(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "discord", map[string]any{"action": "discord_list_guilds"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_mattermost_tool.py::test_missing_token_fails_loudly
func TestBatch7Lane8MattermostMissingToken(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "mattermost", map[string]any{"action": "mattermost_list_teams"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "not configured") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_mattermost_tool.py::test_missing_url_fails_loudly
func TestBatch7Lane8MattermostMissingURL(t *testing.T) {
	registry := newBatch7Lane8Registry(WithGetenv(func(name string) string {
		if name == "MATTERMOST_ACCESS_TOKEN" {
			return "test-token"
		}
		return ""
	}))
	result := batch4Execute(t, registry, "mattermost", map[string]any{"action": "mattermost_list_teams"})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "URL") {
		t.Fatalf("error = %q", got)
	}
}

// ---------------------------------------------------------------- brevo ---

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_success, tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_with_text_content
func TestBatch7Lane8BrevoSendEmail(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/smtp/email" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("api-key"); got != "test-token" {
				t.Fatalf("api-key = %q", got)
			}
			if body["subject"] != "Hello" || body["htmlContent"] != "<p>Hello!</p>" {
				t.Fatalf("body = %v", body)
			}
			if got := batch7Lane8JSONText(t, body["sender"]); got != `{"email":"sender@example.com","name":"Sender"}` {
				t.Fatalf("sender = %s", got)
			}
			if got := batch7Lane8JSONText(t, body["to"]); got != `[{"email":"user@example.com","name":"John Doe"}]` {
				t.Fatalf("to = %s", got)
			}
			if body["textContent"] != "Hello!" {
				t.Fatalf("textContent = %v", body["textContent"])
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"messageId": "<abc123@smtp-relay.brevo.com>"})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_send_email", "to_email": "user@example.com", "to_name": "John Doe",
		"subject": "Hello", "html_content": "<p>Hello!</p>", "from_email": "sender@example.com",
		"from_name": "Sender", "text_content": "Hello!",
	})
	if result["success"] != true || result["message_id"] != "<abc123@smtp-relay.brevo.com>" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoSendSMS::test_send_sms_success
func TestBatch7Lane8BrevoSendSms(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/transactionalSMS/sms" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["sender"] != "HiveAgent" || body["recipient"] != "+919876543210" || body["content"] != "Your OTP is 1234" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"reference": "ref123", "remainingCredits": 95.0,
			})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_send_sms", "to": "+919876543210", "content": "Your OTP is 1234", "sender": "HiveAgent",
	})
	if result["success"] != true || result["reference"] != "ref123" {
		t.Fatalf("result = %v", result)
	}
	if result["remaining_credits"] != float64(95) {
		t.Fatalf("remaining_credits = %v", result["remaining_credits"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoCreateContact::test_create_contact_success, tools/tests/tools/test_brevo_tool.py::TestBrevoCreateContact::test_create_contact_with_list_ids
func TestBatch7Lane8BrevoCreateContact(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/contacts" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["email"] != "user@example.com" {
				t.Fatalf("body = %v", body)
			}
			if got := batch7Lane8JSONText(t, body["attributes"]); got != `{"FIRSTNAME":"John","LASTNAME":"Doe"}` {
				t.Fatalf("attributes = %s", got)
			}
			if got := batch7Lane8JSONText(t, body["listIds"]); got != `[2,5,8]` {
				t.Fatalf("listIds = %s", got)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"id": 42})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_create_contact", "email": "user@example.com",
		"first_name": "John", "last_name": "Doe", "list_ids": "2,5,8",
	})
	if result["success"] != true || result["id"] != float64(42) || result["email"] != "user@example.com" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoGetContact::test_get_contact_success
func TestBatch7Lane8BrevoGetContact(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/contacts/user@example.com" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id":    42,
				"email": "user@example.com",
				"attributes": map[string]any{
					"FIRSTNAME": "John", "LASTNAME": "Doe", "SMS": "+919876543210",
				},
				"listIds":          []any{2, 5},
				"emailBlacklisted": false,
				"smsBlacklisted":   false,
				"createdAt":        "2024-01-15T10:30:00Z",
				"modifiedAt":       "2024-01-20T12:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_get_contact", "email": "user@example.com",
	})
	if result["success"] != true || result["id"] != float64(42) || result["first_name"] != "John" {
		t.Fatalf("result = %v", result)
	}
	if result["last_name"] != "Doe" || result["phone"] != "+919876543210" {
		t.Fatalf("result = %v", result)
	}
	if got := batch7Lane8JSONText(t, result["list_ids"]); got != `[2,5]` {
		t.Fatalf("list_ids = %s", got)
	}
	if result["email_blacklisted"] != false || result["created_at"] != "2024-01-15T10:30:00Z" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoUpdateContact::test_update_contact_success, tools/tests/tools/test_brevo_tool.py::TestBrevoUpdateContact::test_update_contact_with_list_ids
func TestBatch7Lane8BrevoUpdateContact(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPut || r.URL.Path != "/contacts/user@example.com" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := batch7Lane8JSONText(t, body["attributes"]); got != `{"FIRSTNAME":"Jane","LASTNAME":"Smith","SMS":"+15551234567"}` {
				t.Fatalf("attributes = %s", got)
			}
			if got := batch7Lane8JSONText(t, body["listIds"]); got != `[2,5,8]` {
				t.Fatalf("listIds = %s", got)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_update_contact", "email": "user@example.com",
		"first_name": "Jane", "last_name": "Smith", "phone": "+15551234567", "list_ids": "2,5,8",
	})
	if result["success"] != true || result["email"] != "user@example.com" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream has no unit test for brevo_list_contacts; this
// pins the projection and query shape against a fake upstream instead.
func TestBatch7Lane8BrevoListContacts(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/contacts" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"count": 120,
				"contacts": []any{
					map[string]any{
						"id":    42,
						"email": "user@example.com",
						"attributes": map[string]any{
							"FIRSTNAME": "John", "LASTNAME": "Doe",
						},
						"listIds":          []any{2, 5},
						"emailBlacklisted": false,
						"modifiedAt":       "2024-01-20T12:00:00Z",
					},
				},
			})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_list_contacts", "limit": 50, "offset": 0, "modified_since": "2024-01-01T00:00:00Z",
	})
	if result["count"] != 1 || result["total"] != 120 {
		t.Fatalf("result = %v", result)
	}
	contact := batch4BodyMap(t, batch4BodySlice(t, result["contacts"])[0])
	if contact["email"] != "user@example.com" || contact["first_name"] != "John" {
		t.Fatalf("contact = %v", contact)
	}
	query := recorder.last(t).Query
	if query.Get("limit") != "50" || query.Get("offset") != "0" || query.Get("modifiedSince") != "2024-01-01T00:00:00Z" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream has no unit test for brevo_delete_contact; the 204
// path is pinned here against a fake upstream.
func TestBatch7Lane8BrevoDeleteContact(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/contacts/user@example.com" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_delete_contact", "email": "user@example.com",
	})
	if result["success"] != true || result["email"] != "user@example.com" || result["status"] != "deleted" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream has no unit test for brevo_list_email_campaigns;
// this pins the projection and query shape against a fake upstream.
func TestBatch7Lane8BrevoListEmailCampaigns(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/emailCampaigns" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"count": 1,
				"campaigns": []any{
					map[string]any{
						"id": 7, "name": "Launch", "subject": "Hello", "status": "sent", "type": "classic",
						"createdAt": "2024-01-15T10:30:00Z", "scheduledAt": "2024-01-16T10:30:00Z",
						"statistics": map[string]any{"globalStats": map[string]any{"sent": 10}},
					},
				},
			})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_list_email_campaigns", "status": "sent", "limit": 50, "offset": 0,
	})
	if result["count"] != 1 || result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	campaign := batch4BodyMap(t, batch4BodySlice(t, result["campaigns"])[0])
	if campaign["name"] != "Launch" || campaign["status"] != "sent" {
		t.Fatalf("campaign = %v", campaign)
	}
	if got := batch7Lane8JSONText(t, campaign["statistics"]); got != `{"sent":10}` {
		t.Fatalf("statistics = %s", got)
	}
	query := recorder.last(t).Query
	if query.Get("status") != "sent" || query.Get("limit") != "50" || query.Get("offset") != "0" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoGetEmailStats::test_get_email_stats_success
func TestBatch7Lane8BrevoGetEmailStats(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/smtp/emails/<abc123@smtp-relay.brevo.com>" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"messageId": "<abc123@smtp-relay.brevo.com>",
				"email":     "user@example.com",
				"subject":   "Hello",
				"date":      "2024-01-15T10:30:00Z",
				"events":    []any{map[string]any{"name": "delivered", "time": "2024-01-15T10:30:05Z"}},
			})
		})
	result := batch4Execute(t, registry, "brevo", map[string]any{
		"action": "brevo_get_email_stats", "message_id": "<abc123@smtp-relay.brevo.com>",
	})
	if result["success"] != true || result["email"] != "user@example.com" || result["subject"] != "Hello" {
		t.Fatalf("result = %v", result)
	}
	events := batch4BodySlice(t, result["events"])
	if len(events) != 1 || batch4BodyMap(t, events[0])["name"] != "delivered" {
		t.Fatalf("events = %v", events)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_invalid_email, tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_empty_subject, tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_empty_content, tools/tests/tools/test_brevo_tool.py::TestBrevoSendSMS::test_send_sms_invalid_phone_format, tools/tests/tools/test_brevo_tool.py::TestBrevoSendSMS::test_send_sms_content_too_long, tools/tests/tools/test_brevo_tool.py::TestBrevoCreateContact::test_create_contact_invalid_list_ids, tools/tests/tools/test_brevo_tool.py::TestBrevoGetEmailStats::test_get_email_stats_empty_message_id
func TestBatch7Lane8BrevoValidation(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "brevo_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name: "invalid recipient",
			input: map[string]any{
				"action": "brevo_send_email", "to_email": "not-an-email", "to_name": "John",
				"subject": "Hello", "html_content": "<p>Hello!</p>",
				"from_email": "sender@example.com", "from_name": "Sender",
			},
			want: "Invalid recipient email",
		},
		{
			name: "empty subject",
			input: map[string]any{
				"action": "brevo_send_email", "to_email": "user@example.com", "to_name": "John",
				"subject": "", "html_content": "<p>Hello!</p>",
				"from_email": "sender@example.com", "from_name": "Sender",
			},
			want: "subject",
		},
		{
			name: "empty content",
			input: map[string]any{
				"action": "brevo_send_email", "to_email": "user@example.com", "to_name": "John",
				"subject": "Hello", "html_content": "",
				"from_email": "sender@example.com", "from_name": "Sender",
			},
			want: "content",
		},
		{
			name:  "phone format",
			input: map[string]any{"action": "brevo_send_sms", "to": "919876543210", "content": "Hello", "sender": "HiveAgent"},
			want:  "international format",
		},
		{
			name:  "content too long",
			input: map[string]any{"action": "brevo_send_sms", "to": "+919876543210", "content": strings.Repeat("x", 641), "sender": "HiveAgent"},
			want:  "too long",
		},
		{
			name:  "invalid list ids",
			input: map[string]any{"action": "brevo_create_contact", "email": "user@example.com", "list_ids": "abc,def"},
			want:  "list_ids",
		},
		{
			name:  "empty message id",
			input: map[string]any{"action": "brevo_get_email_stats", "message_id": ""},
			want:  "message_id",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := batch4Execute(t, registry, "brevo", testCase.input)
			if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), strings.ToLower(testCase.want)) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// --------------------------------------------------------------- calcom ---

// Parity: tools/tests/tools/test_calcom_tool.py::TestListBookings::test_list_bookings_success, tools/tests/tools/test_calcom_tool.py::TestListBookings::test_list_bookings_with_filters
func TestBatch7Lane8CalcomListBookings(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/bookings" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"bookings": []any{
					map[string]any{"id": 1, "title": "Meeting 1"},
					map[string]any{"id": 2, "title": "Meeting 2"},
				},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_list_bookings", "status": "upcoming", "event_type_id": 123,
		"start_date": "2024-01-01", "end_date": "2024-01-31", "limit": 10,
	})
	if got := len(batch4BodySlice(t, result["bookings"])); got != 2 {
		t.Fatalf("bookings = %v", result["bookings"])
	}
	query := recorder.last(t).Query
	if query.Get("apiKey") != "test-token" || query.Get("status") != "upcoming" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("eventTypeId") != "123" || query.Get("afterStart") != "2024-01-01" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("beforeEnd") != "2024-01-31" || query.Get("limit") != "10" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestGetBooking::test_get_booking_success
func TestBatch7Lane8CalcomGetBooking(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/bookings/123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"booking": map[string]any{"id": 123, "title": "Meeting", "status": "accepted"},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_get_booking", "booking_id": 123,
	})
	booking := batch4BodyMap(t, result["booking"])
	if booking["title"] != "Meeting" || booking["status"] != "accepted" {
		t.Fatalf("booking = %v", booking)
	}
	if recorder.last(t).Query.Get("apiKey") != "test-token" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestCreateBooking::test_create_booking_success
func TestBatch7Lane8CalcomCreateBooking(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/bookings" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["eventTypeId"] != float64(123) || body["language"] != "en" || body["timeZone"] != "UTC" {
				t.Fatalf("body = %v", body)
			}
			if got := batch7Lane8JSONText(t, body["metadata"]); got != `{}` {
				t.Fatalf("metadata = %s", got)
			}
			if got := batch7Lane8JSONText(t, body["responses"]); got != `{"email":"john@example.com","guests":["guest@example.com"],"name":"John Doe","notes":"Bring notes"}` {
				t.Fatalf("responses = %s", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": 456, "status": "accepted"})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_create_booking", "event_type_id": 123,
		"start": "2024-01-20T14:00:00Z", "name": "John Doe", "email": "john@example.com",
		"timezone": "UTC", "language": "en", "notes": "Bring notes",
		"guests": []any{"guest@example.com"},
	})
	if result["id"] != float64(456) {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestCreateBooking::test_create_booking_missing_required_fields
func TestBatch7Lane8CalcomCreateBookingRequired(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_create_booking", "event_type_id": 123,
		"start": "2024-01-20T14:00:00Z", "name": "", "email": "john@example.com",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(got, "name is required") {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestCancelBooking::test_cancel_booking_with_reason
func TestBatch7Lane8CalcomCancelBooking(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/bookings/123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["cancellationReason"] != "Schedule conflict" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"success": true})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_cancel_booking", "booking_id": 123, "reason": "Schedule conflict",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Query.Get("apiKey") != "test-token" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestCancelBooking::test_cancel_booking_success
func TestBatch7Lane8CalcomCancelBookingWithoutReason(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/bookings/123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body != nil {
				t.Fatalf("body = %v, want no body", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"success": true})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_cancel_booking", "booking_id": 123,
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestGetAvailability::test_get_availability_success
func TestBatch7Lane8CalcomGetAvailability(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/slots" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"slots": map[string]any{"2024-01-20": []any{"09:00", "10:00", "14:00"}},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_get_availability", "event_type_id": 123,
		"start_time": "2024-01-20T00:00:00Z", "end_time": "2024-01-21T00:00:00Z",
	})
	if _, ok := result["slots"].(map[string]any); !ok {
		t.Fatalf("result = %v", result)
	}
	query := recorder.last(t).Query
	if query.Get("eventTypeId") != "123" || query.Get("startTime") != "2024-01-20T00:00:00Z" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("endTime") != "2024-01-21T00:00:00Z" || query.Get("timeZone") != "UTC" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestUpdateSchedule::test_update_schedule_with_availability
func TestBatch7Lane8CalcomUpdateSchedule(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPatch || r.URL.Path != "/schedules/1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["name"] != "Working Hours" || body["timeZone"] != "America/New_York" {
				t.Fatalf("body = %v", body)
			}
			if got := batch7Lane8JSONText(t, body["availability"]); got != `[{"days":[1,2,3,4,5],"endTime":"17:00","startTime":"09:00"}]` {
				t.Fatalf("availability = %s", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"schedule": map[string]any{"id": 1}})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_update_schedule", "schedule_id": 1, "name": "Working Hours",
		"timezone": "America/New_York",
		"availability": []any{map[string]any{
			"days": []any{1, 2, 3, 4, 5}, "startTime": "09:00", "endTime": "17:00",
		}},
	})
	if _, ok := result["schedule"].(map[string]any); !ok {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestListSchedules::test_list_schedules_success, tools/tests/tools/test_calcom_tool.py::TestListSchedules::test_list_schedules_empty
func TestBatch7Lane8CalcomListSchedules(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/schedules" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"schedules": []any{
					map[string]any{"id": 1, "name": "Working Hours", "timeZone": "America/New_York"},
				},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{"action": "calcom_list_schedules"})
	if got := len(batch4BodySlice(t, result["schedules"])); got != 1 {
		t.Fatalf("schedules = %v", result["schedules"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestListEventTypes::test_list_event_types_success
func TestBatch7Lane8CalcomListEventTypes(t *testing.T) {
	registry, _ := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/event-types" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"event_types": []any{
					map[string]any{"id": 1, "title": "30 Min Meeting"},
					map[string]any{"id": 2, "title": "60 Min Meeting"},
				},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{"action": "calcom_list_event_types"})
	if got := len(batch4BodySlice(t, result["event_types"])); got != 2 {
		t.Fatalf("event_types = %v", result["event_types"])
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestGetEventType::test_get_event_type_success
func TestBatch7Lane8CalcomGetEventType(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/event-types/123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"event_type": map[string]any{"id": 123, "title": "30 Min Meeting", "length": 30},
			})
		})
	result := batch4Execute(t, registry, "calcom", map[string]any{
		"action": "calcom_get_event_type", "event_type_id": 123,
	})
	eventType := batch4BodyMap(t, result["event_type"])
	if eventType["title"] != "30 Min Meeting" || eventType["length"] != float64(30) {
		t.Fatalf("event_type = %v", eventType)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestGetEventType::test_get_event_type_missing_id, tools/tests/tools/test_calcom_tool.py::TestGetAvailability::test_get_availability_missing_required
func TestBatch7Lane8CalcomValidation(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calcom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "missing event type id",
			input: map[string]any{"action": "calcom_get_event_type", "event_type_id": 0},
			want:  "event_type_id is required",
		},
		{
			name: "missing availability window",
			input: map[string]any{
				"action": "calcom_get_availability", "event_type_id": 123,
				"start_time": "", "end_time": "2024-01-21T00:00:00Z",
			},
			want: "start_time and end_time are required",
		},
		{
			name:  "missing schedule id",
			input: map[string]any{"action": "calcom_update_schedule", "schedule_id": 0},
			want:  "schedule_id is required",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := batch4Execute(t, registry, "calcom", testCase.input)
			if got := batch4ErrorText(t, result); !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// --------------------------------------------------------------- discord ---

// Parity-exempt: upstream's discord smoke suite covers send_message and the
// rate-limit path only; guild listing is pinned here against a fake upstream.
func TestBatch7Lane8DiscordListGuilds(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/users/@me/guilds" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bot test-token" {
				t.Fatalf("authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"id": "g1", "name": "Hive"},
				map[string]any{"id": "g2", "name": "Nous"},
			})
		})
	result := batch4Execute(t, registry, "discord", map[string]any{"action": "discord_list_guilds"})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if got := len(batch4BodySlice(t, result["guilds"])); got != 2 {
		t.Fatalf("guilds = %v", result["guilds"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream's discord smoke suite does not cover list_channels;
// the text_only filter is pinned here against a fake upstream.
func TestBatch7Lane8DiscordListChannels(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/guilds/g1/channels" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"id": "c1", "name": "general", "type": 0},
				map[string]any{"id": "c2", "name": "voice", "type": 2},
				map[string]any{"id": "c3", "name": "news", "type": 5},
			})
		})
	textOnly := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_list_channels", "guild_id": "g1",
	})
	if got := len(batch4BodySlice(t, textOnly["channels"])); got != 2 {
		t.Fatalf("text channels = %v", textOnly["channels"])
	}
	all := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_list_channels", "guild_id": "g1", "text_only": false,
	})
	if got := len(batch4BodySlice(t, all["channels"])); got != 3 {
		t.Fatalf("channels = %v", all["channels"])
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
}

// Parity: tools/tests/tools/test_discord_tool.py::test_send_message_success
func TestBatch7Lane8DiscordSendMessage(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/channels/c1/messages" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["content"] != "hi" || body["tts"] != true {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "m1", "channel_id": "c1", "content": "hi",
			})
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_send_message", "channel_id": "c1", "content": "hi", "tts": true,
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	message := batch4BodyMap(t, result["message"])
	if message["content"] != "hi" || message["id"] != "m1" {
		t.Fatalf("message = %v", message)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_discord_tool.py::test_send_message_length_validation
func TestBatch7Lane8DiscordSendMessageLength(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_send_message", "channel_id": "c1",
		"content": strings.Repeat("x", schedchat.DiscordMaxMessage+1),
	})
	batch4ErrorText(t, result)
	if result["max_length"] != schedchat.DiscordMaxMessage {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity-exempt: upstream's discord smoke suite does not cover
// get_messages; pagination and the 100 limit clamp are pinned here.
func TestBatch7Lane8DiscordGetMessages(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/channels/c1/messages" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"id": "m2", "content": "newer"},
			})
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_get_messages", "channel_id": "c1",
		"limit": 500, "before": "m3", "after": "m1",
	})
	if got := len(batch4BodySlice(t, result["messages"])); got != 1 {
		t.Fatalf("messages = %v", result["messages"])
	}
	query := recorder.last(t).Query
	if query.Get("limit") != "100" || query.Get("before") != "m3" || query.Get("after") != "m1" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream's discord smoke suite does not cover get_channel;
// the {"channel": ...} envelope is pinned here.
func TestBatch7Lane8DiscordGetChannel(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/channels/c1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "c1", "name": "general", "type": 0, "topic": "Welcome",
			})
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_get_channel", "channel_id": "c1",
	})
	channel := batch4BodyMap(t, result["channel"])
	if channel["name"] != "general" || channel["topic"] != "Welcome" {
		t.Fatalf("channel = %v", channel)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream's discord smoke suite does not cover
// create_reaction; the percent-encoded emoji path is pinned here.
func TestBatch7Lane8DiscordCreateReaction(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPut {
				t.Fatalf("method = %s", r.Method)
			}
			if r.URL.EscapedPath() != "/channels/c1/messages/m1/reactions/%F0%9F%91%8D/@me" {
				t.Fatalf("path = %s", r.URL.EscapedPath())
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_create_reaction", "channel_id": "c1", "message_id": "m1", "emoji": "👍",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream's discord smoke suite does not cover
// delete_message; the 204 success value is pinned here.
func TestBatch7Lane8DiscordDeleteMessage(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/channels/c1/messages/m1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_delete_message", "channel_id": "c1", "message_id": "m1",
	})
	if result["success"] != true || result["deleted_message_id"] != "m1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_discord_tool.py::test_rate_limit_exhaustion_is_classified
func TestBatch7Lane8DiscordRateLimit(t *testing.T) {
	batch7Lane8NoSleep(t)
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/channels/c1/messages" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusTooManyRequests, map[string]any{
				"message": "Rate limit", "retry_after": 5,
			})
		})
	result := batch4Execute(t, registry, "discord", map[string]any{
		"action": "discord_send_message", "channel_id": "c1", "content": "hi",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), "rate limit") {
		t.Fatalf("error = %q", got)
	}
	if result["retry_after"] != float64(5) {
		t.Fatalf("retry_after = %v", result["retry_after"])
	}
	if recorder.count() != schedchat.MaxRetries+1 {
		t.Fatalf("requests = %d, want %d", recorder.count(), schedchat.MaxRetries+1)
	}
}

// ------------------------------------------------------------ mattermost ---

// Parity-exempt: upstream's mattermost smoke suite covers send_message and the
// rate-limit path only; team listing is pinned here against a fake upstream.
func TestBatch7Lane8MattermostListTeams(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v4/users/me/teams" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"id": "t1", "name": "engineering"},
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{"action": "mattermost_list_teams"})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if got := len(batch4BodySlice(t, result["teams"])); got != 1 {
		t.Fatalf("teams = %v", result["teams"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not cover
// list_channels; the /api/v4 base and per_page clamp are pinned here.
func TestBatch7Lane8MattermostListChannels(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v4/teams/t1/channels" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, []any{
				map[string]any{"id": "c1", "name": "town-square"},
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_list_channels", "team_id": "t1",
	})
	if got := len(batch4BodySlice(t, result["channels"])); got != 1 {
		t.Fatalf("channels = %v", result["channels"])
	}
	if recorder.last(t).Query.Get("per_page") != "100" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not cover get_channel;
// the {"channel": ...} envelope is pinned here.
func TestBatch7Lane8MattermostGetChannel(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v4/channels/c1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "c1", "name": "town-square", "display_name": "Town Square",
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_get_channel", "channel_id": "c1",
	})
	channel := batch4BodyMap(t, result["channel"])
	if channel["display_name"] != "Town Square" {
		t.Fatalf("channel = %v", channel)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_mattermost_tool.py::test_send_message_success
func TestBatch7Lane8MattermostSendMessage(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/posts" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["channel_id"] != "c1" || body["message"] != "hi" || body["root_id"] != "p1" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"id": "p2", "channel_id": "c1", "message": "hi",
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_send_message", "channel_id": "c1", "message": "hi", "root_id": "p1",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	post := batch4BodyMap(t, result["post"])
	if post["message"] != "hi" || post["id"] != "p2" {
		t.Fatalf("post = %v", post)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_mattermost_tool.py::test_send_message_length_validation
func TestBatch7Lane8MattermostSendMessageLength(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_send_message", "channel_id": "c1",
		"message": strings.Repeat("x", schedchat.MattermostMaxText+1),
	})
	batch4ErrorText(t, result)
	if result["max_length"] != schedchat.MattermostMaxText {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not cover get_posts;
// pagination and the 200 per_page clamp are pinned here.
func TestBatch7Lane8MattermostGetPosts(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/v4/channels/c1/posts" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"order": []any{"p2", "p1"},
				"posts": map[string]any{"p1": map[string]any{"id": "p1", "message": "hi"}},
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_get_posts", "channel_id": "c1",
		"per_page": 500, "page": 2, "before": "p9", "after": "p0",
	})
	posts := batch4BodyMap(t, result["posts"])
	if _, ok := posts["posts"].(map[string]any); !ok {
		t.Fatalf("posts = %v", posts)
	}
	query := recorder.last(t).Query
	if query.Get("per_page") != "200" || query.Get("page") != "2" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("before") != "p9" || query.Get("after") != "p0" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not cover
// create_reaction; the /users/me lookup before POST /reactions is pinned here.
func TestBatch7Lane8MattermostCreateReaction(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method == http.MethodGet && r.URL.Path == "/api/v4/users/me" {
				batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "u1", "username": "bot"})
				return
			}
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/reactions" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["user_id"] != "u1" || body["post_id"] != "p1" || body["emoji_name"] != "thumbsup" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"user_id": "u1", "post_id": "p1", "emoji_name": "thumbsup",
			})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_create_reaction", "post_id": "p1", "emoji_name": "thumbsup",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d, want 2", recorder.count())
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not cover delete_post;
// the 204 success value is pinned here.
func TestBatch7Lane8MattermostDeletePost(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/api/v4/posts/p1" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_delete_post", "post_id": "p1",
	})
	if result["success"] != true || result["deleted_post_id"] != "p1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_mattermost_tool.py::test_rate_limit_exhaustion_is_classified
func TestBatch7Lane8MattermostRateLimit(t *testing.T) {
	batch7Lane8NoSleep(t)
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v4/posts" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Retry-After", "5")
			batch4WriteJSON(w, http.StatusTooManyRequests, map[string]any{"message": "Rate limit"})
		})
	result := batch4Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_send_message", "channel_id": "c1", "message": "hi",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), "rate limit") {
		t.Fatalf("error = %q", got)
	}
	if result["retry_after"] != float64(5) {
		t.Fatalf("retry_after = %v", result["retry_after"])
	}
	if recorder.count() != schedchat.MaxRetries+1 {
		t.Fatalf("requests = %d, want %d", recorder.count(), schedchat.MaxRetries+1)
	}
}

// ---------------------------------------------------------- error mapping ---

// Parity: tools/tests/tools/test_brevo_tool.py::TestBrevoSendEmail::test_send_email_invalid_auth
func TestBatch7Lane8BrevoErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Invalid Brevo API key"},
		{http.StatusForbidden, "Access forbidden"},
		{http.StatusNotFound, "Resource not found"},
		{http.StatusTooManyRequests, "Rate limit exceeded"},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			registry, _ := batch7Lane8TestRegistry(t, "brevo_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					batch4WriteJSON(w, testCase.status, map[string]any{"message": "nope"})
				})
			result := batch4Execute(t, registry, "brevo", map[string]any{
				"action": "brevo_send_email", "to_email": "user@example.com", "to_name": "John",
				"subject": "Hello", "html_content": "<p>Hello!</p>",
				"from_email": "sender@example.com", "from_name": "Sender",
			})
			if got := batch4ErrorText(t, result); !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_calcom_tool.py::TestErrorHandling::test_401_unauthorized, tools/tests/tools/test_calcom_tool.py::TestErrorHandling::test_429_rate_limit
func TestBatch7Lane8CalcomErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Invalid or expired Cal.com API key"},
		{http.StatusForbidden, "Access forbidden"},
		{http.StatusNotFound, "Resource not found"},
		{http.StatusTooManyRequests, "Rate limit exceeded"},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			registry, _ := batch7Lane8TestRegistry(t, "calcom_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					batch4WriteJSON(w, testCase.status, map[string]any{"message": "nope"})
				})
			result := batch4Execute(t, registry, "calcom", map[string]any{"action": "calcom_list_bookings"})
			if got := batch4ErrorText(t, result); !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
}

// Parity-exempt: upstream's calendly tests do not exercise HTTP error codes;
// the shared "HTTP <code>: <body>" mapping is pinned here.
func TestBatch7Lane8CalendlyErrorMapping(t *testing.T) {
	cases := []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests}
	for _, status := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			registry, _ := batch7Lane8TestRegistry(t, "calendly_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					batch4WriteJSON(w, status, map[string]any{"message": "nope"})
				})
			result := batch4Execute(t, registry, "calendly", map[string]any{"action": "calendly_get_current_user"})
			got := batch4ErrorText(t, result)
			if !strings.Contains(got, "HTTP "+strconv.Itoa(status)) || !strings.Contains(got, "nope") {
				t.Fatalf("error = %q", got)
			}
		})
	}
}

// Parity-exempt: upstream's discord smoke suite does not exercise HTTP error
// codes; the shared "HTTP <code>: <message>" mapping is pinned here.
func TestBatch7Lane8DiscordErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Missing Access"},
		{http.StatusForbidden, "Missing Permissions"},
		{http.StatusNotFound, "Unknown Guild"},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			registry, _ := batch7Lane8TestRegistry(t, "discord_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					batch4WriteJSON(w, testCase.status, map[string]any{"message": testCase.want})
				})
			result := batch4Execute(t, registry, "discord", map[string]any{"action": "discord_list_guilds"})
			got := batch4ErrorText(t, result)
			if !strings.Contains(got, "HTTP "+strconv.Itoa(testCase.status)) || !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q", got)
			}
		})
	}
}

// Parity-exempt: upstream's mattermost smoke suite does not exercise HTTP error
// codes; the shared "HTTP <code>: <message>" mapping is pinned here.
func TestBatch7Lane8MattermostErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Invalid or expired session"},
		{http.StatusForbidden, "You do not have the appropriate permissions"},
		{http.StatusNotFound, "Resource not found"},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			registry, _ := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
				func(w http.ResponseWriter, r *http.Request, body map[string]any) {
					batch4WriteJSON(w, testCase.status, map[string]any{"message": testCase.want})
				})
			result := batch4Execute(t, registry, "mattermost", map[string]any{"action": "mattermost_list_teams"})
			got := batch4ErrorText(t, result)
			if !strings.Contains(got, "HTTP "+strconv.Itoa(testCase.status)) || !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q", got)
			}
		})
	}
}

// ------------------------------------------------------------- calendly ---

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyGetCurrentUser::test_successful_get
func TestBatch7Lane8CalendlyGetCurrentUser(t *testing.T) {
	userURI := "https://api.calendly.com/users/AAAA"
	orgURI := "https://api.calendly.com/organizations/BBBB"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/users/me" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"resource": map[string]any{
					"uri":                  userURI,
					"name":                 "John Doe",
					"email":                "john@example.com",
					"scheduling_url":       "https://calendly.com/johndoe",
					"timezone":             "America/New_York",
					"current_organization": orgURI,
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{"action": "calendly_get_current_user"})
	if result["name"] != "John Doe" || result["uri"] != userURI || result["organization"] != orgURI {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyListEventTypes::test_successful_list
func TestBatch7Lane8CalendlyListEventTypes(t *testing.T) {
	userURI := "https://api.calendly.com/users/AAAA"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/event_types" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"collection": []any{
					map[string]any{
						"uri": "https://api.calendly.com/event_types/CCCC", "name": "30 Minute Meeting",
						"slug": "30min", "active": true, "duration": 30, "kind": "solo",
						"scheduling_url": "https://calendly.com/johndoe/30min", "description_plain": "Quick chat",
					},
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_list_event_types", "user_uri": userURI,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	eventType := batch4BodyMap(t, batch4BodySlice(t, result["event_types"])[0])
	if eventType["name"] != "30 Minute Meeting" || eventType["duration"] != float64(30) {
		t.Fatalf("event_type = %v", eventType)
	}
	if eventType["description"] != "Quick chat" || eventType["active"] != true {
		t.Fatalf("event_type = %v", eventType)
	}
	query := recorder.last(t).Query
	if query.Get("user") != userURI || query.Get("count") != "20" || query.Get("active") != "true" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyListScheduledEvents::test_successful_list
func TestBatch7Lane8CalendlyListScheduledEvents(t *testing.T) {
	userURI := "https://api.calendly.com/users/AAAA"
	eventURI := "https://api.calendly.com/scheduled_events/DDDD"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/scheduled_events" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"collection": []any{
					map[string]any{
						"uri": eventURI, "name": "30 Minute Meeting", "status": "active",
						"start_time": "2024-03-15T14:00:00.000000Z", "end_time": "2024-03-15T14:30:00.000000Z",
						"event_type":       "https://api.calendly.com/event_types/CCCC",
						"location":         map[string]any{"location": "https://zoom.us/j/12345"},
						"invitees_counter": map[string]any{"total": 1},
					},
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_list_scheduled_events", "user_uri": userURI,
		"status": "active", "min_start_time": "2024-03-01T00:00:00Z", "max_start_time": "2024-04-01T00:00:00Z",
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	event := batch4BodyMap(t, batch4BodySlice(t, result["events"])[0])
	if event["name"] != "30 Minute Meeting" || event["invitees_count"] != 1 {
		t.Fatalf("event = %v", event)
	}
	if event["location"] != "https://zoom.us/j/12345" {
		t.Fatalf("event = %v", event)
	}
	query := recorder.last(t).Query
	if query.Get("status") != "active" || query.Get("min_start_time") != "2024-03-01T00:00:00Z" {
		t.Fatalf("query = %v", query)
	}
	if query.Get("max_start_time") != "2024-04-01T00:00:00Z" || query.Get("count") != "20" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyGetScheduledEvent::test_successful_get
func TestBatch7Lane8CalendlyGetScheduledEvent(t *testing.T) {
	eventURI := "https://api.calendly.com/scheduled_events/DDDD"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/scheduled_events/DDDD" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"resource": map[string]any{
					"uri": eventURI, "name": "30 Minute Meeting", "status": "active",
					"start_time": "2024-03-15T14:00:00.000000Z", "end_time": "2024-03-15T14:30:00.000000Z",
					"event_type":       "https://api.calendly.com/event_types/CCCC",
					"location":         map[string]any{"type": "zoom", "location": "https://zoom.us/j/12345"},
					"invitees_counter": map[string]any{"total": 1, "active": 1, "limit": 1},
					"event_memberships": []any{
						map[string]any{"user_email": "john@example.com"},
					},
					"created_at": "2024-03-10T12:00:00.000000Z",
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_get_scheduled_event", "event_uri": eventURI,
	})
	if result["name"] != "30 Minute Meeting" || result["status"] != "active" {
		t.Fatalf("result = %v", result)
	}
	if got := batch7Lane8JSONText(t, result["invitees_counter"]); got != `{"active":1,"limit":1,"total":1}` {
		t.Fatalf("invitees_counter = %s", got)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyListInvitees::test_successful_list
func TestBatch7Lane8CalendlyListInvitees(t *testing.T) {
	eventURI := "https://api.calendly.com/scheduled_events/DDDD"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/scheduled_events/DDDD/invitees" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"collection": []any{
					map[string]any{
						"uri": eventURI + "/invitees/EEEE", "name": "Jane Smith", "email": "jane@example.com",
						"status": "active", "timezone": "America/Chicago",
						"questions_and_answers": []any{
							map[string]any{"question": "Topic?", "answer": "Product demo"},
						},
						"created_at": "2024-03-10T12:00:00.000000Z",
					},
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_list_invitees", "event_uri": eventURI,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	invitee := batch4BodyMap(t, batch4BodySlice(t, result["invitees"])[0])
	if invitee["name"] != "Jane Smith" || invitee["email"] != "jane@example.com" {
		t.Fatalf("invitee = %v", invitee)
	}
	if got := len(batch4BodySlice(t, invitee["questions_and_answers"])); got != 1 {
		t.Fatalf("questions_and_answers = %v", invitee["questions_and_answers"])
	}
	if recorder.last(t).Query.Get("count") != "25" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity-exempt: upstream has no unit test for calendly_cancel_event; this
// pins the UUID extraction, body and projection against a fake upstream.
func TestBatch7Lane8CalendlyCancelEvent(t *testing.T) {
	eventURI := "https://api.calendly.com/scheduled_events/DDDD"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/scheduled_events/DDDD/cancellation" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["reason"] != "Conflict" {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"resource": map[string]any{
					"canceled_by": "john@example.com", "reason": "Conflict",
					"created_at": "2024-03-11T09:00:00.000000Z",
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_cancel_event", "event_uri": eventURI, "reason": "Conflict",
	})
	if result["status"] != "canceled" || result["reason"] != "Conflict" {
		t.Fatalf("result = %v", result)
	}
	if result["canceled_by"] != "john@example.com" || result["created_at"] != "2024-03-11T09:00:00.000000Z" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity-exempt: upstream has no unit test for calendly_list_webhooks; this
// pins the query and projection against a fake upstream.
func TestBatch7Lane8CalendlyListWebhooks(t *testing.T) {
	orgURI := "https://api.calendly.com/organizations/BBBB"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/webhook_subscriptions" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"collection": []any{
					map[string]any{
						"uri":          "https://api.calendly.com/webhook_subscriptions/FFFF",
						"callback_url": "https://example.com/hook", "state": "active",
						"events": []any{"invitee.created"}, "scope": "organization",
						"created_at": "2024-03-10T12:00:00.000000Z",
					},
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_list_webhooks", "organization_uri": orgURI,
	})
	if result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	webhook := batch4BodyMap(t, batch4BodySlice(t, result["webhooks"])[0])
	if webhook["callback_url"] != "https://example.com/hook" || webhook["state"] != "active" {
		t.Fatalf("webhook = %v", webhook)
	}
	query := recorder.last(t).Query
	if query.Get("organization") != orgURI || query.Get("scope") != "organization" || query.Get("count") != "20" {
		t.Fatalf("query = %v", query)
	}
}

// Parity-exempt: upstream has no unit test for calendly_get_event_type; this
// pins the UUID extraction and projection against a fake upstream.
func TestBatch7Lane8CalendlyGetEventType(t *testing.T) {
	eventTypeURI := "https://api.calendly.com/event_types/CCCC"
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/event_types/CCCC" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"resource": map[string]any{
					"uri": eventTypeURI, "name": "30 Minute Meeting", "slug": "30min",
					"active": true, "duration": 30, "kind": "solo", "type": "StandardEventType",
					"color": "#0069ff", "scheduling_url": "https://calendly.com/johndoe/30min",
					"description_plain": "Quick chat",
					"custom_questions": []any{
						map[string]any{"name": "Topic", "type": "text", "required": true},
					},
				},
			})
		})
	result := batch4Execute(t, registry, "calendly", map[string]any{
		"action": "calendly_get_event_type", "event_type_uri": eventTypeURI,
	})
	if result["name"] != "30 Minute Meeting" || result["slug"] != "30min" {
		t.Fatalf("result = %v", result)
	}
	if result["duration"] != float64(30) || result["active"] != true || result["color"] != "#0069ff" {
		t.Fatalf("result = %v", result)
	}
	if got := len(batch4BodySlice(t, result["custom_questions"])); got != 1 {
		t.Fatalf("custom_questions = %v", result["custom_questions"])
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calendly_tool.py::TestCalendlyListEventTypes::test_missing_user_uri, tools/tests/tools/test_calendly_tool.py::TestCalendlyListInvitees::test_missing_event_uri
func TestBatch7Lane8CalendlyValidation(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "calendly_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		})
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{
			name:  "missing user uri",
			input: map[string]any{"action": "calendly_list_event_types", "user_uri": ""},
			want:  "user_uri is required",
		},
		{
			name:  "missing event uri",
			input: map[string]any{"action": "calendly_list_invitees", "event_uri": ""},
			want:  "event_uri is required",
		},
		{
			name:  "missing organization uri",
			input: map[string]any{"action": "calendly_list_webhooks", "organization_uri": ""},
			want:  "organization_uri is required",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := batch4Execute(t, registry, "calendly", testCase.input)
			if got := batch4ErrorText(t, result); !strings.Contains(got, testCase.want) {
				t.Fatalf("error = %q, want %q", got, testCase.want)
			}
		})
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}
