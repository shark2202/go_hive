package integrations

import (
	"encoding/base64"
	"net/http"
	"net/mail"
	"strings"
	"testing"
)

// batch7Lane9EmailFake serves one email provider module.
func batch7Lane9EmailFake(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*Registry, *batch4Recorder) {
	t.Helper()
	return batch7Lane9Fake(t, module, env, handler)
}

// batch7Lane9EmailDecodeRaw decodes a base64url MIME body from a send payload.
func batch7Lane9EmailDecodeRaw(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, ok := payload["raw"].(string)
	if !ok {
		t.Fatalf("payload has no raw message: %v", payload)
	}
	decoded, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	return string(decoded)
}

// batch7Lane9EmailParse asserts the raw message is valid MIME and returns it.
func batch7Lane9EmailParse(t *testing.T, payload map[string]any) *mail.Message {
	t.Helper()
	message, err := mail.ReadMessage(strings.NewReader(batch7Lane9EmailDecodeRaw(t, payload)))
	if err != nil {
		t.Fatalf("parse MIME: %v", err)
	}
	return message
}

// Parity: tools/tests/tools/test_email_tool.py::TestResendProvider::test_resend_success
func TestBatch7Lane9EmailResendSend(t *testing.T) {
	registry, recorder := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/emails" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			if body["from"] != "test@example.com" || body["subject"] != "Test" || body["html"] != "<p>Hi</p>" {
				t.Fatalf("body = %v", body)
			}
			to := batch4BodySlice(t, body["to"])
			if len(to) != 1 || to[0] != "test@example.com" {
				t.Fatalf("to = %v", to)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "email_789"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend",
	})
	if result["success"] != true || result["provider"] != "resend" || result["id"] != "email_789" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_none_cc_bcc_not_included_in_payload
func TestBatch7Lane9EmailResendOmitsEmptyCC(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if _, present := body["cc"]; present {
				t.Fatalf("unexpected cc: %v", body)
			}
			if _, present := body["bcc"]; present {
				t.Fatalf("unexpected bcc: %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "email_1"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_cc_and_bcc_lists_passed_to_provider
func TestBatch7Lane9EmailResendCCAndBCC(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			cc := batch4BodySlice(t, body["cc"])
			if len(cc) != 2 || cc[0] != "cc1@example.com" || cc[1] != "cc2@example.com" {
				t.Fatalf("cc = %v", cc)
			}
			bcc := batch4BodySlice(t, body["bcc"])
			if len(bcc) != 1 || bcc[0] != "bcc@example.com" {
				t.Fatalf("bcc = %v", bcc)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "email_2"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": []any{"a@example.com", "b@example.com"},
		"subject": "Test", "html": "<p>Hi</p>", "provider": "resend",
		"cc": []any{"cc1@example.com", "cc2@example.com"}, "bcc": "bcc@example.com",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	to, ok := result["to"].([]string)
	if !ok || len(to) != 2 || to[0] != "a@example.com" {
		t.Fatalf("to = %v", result["to"])
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailProvider::test_gmail_success
func TestBatch7Lane9EmailGmailSend(t *testing.T) {
	registry, recorder := batch7Lane9EmailFake(t, "email_gmail_tool", map[string]string{"EMAIL_FROM": "user@gmail.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/users/me/messages/send" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			message := batch7Lane9EmailParse(t, body)
			if message.Header.Get("To") != "recipient@example.com" || message.Header.Get("Subject") != "Test Gmail" {
				t.Fatalf("headers = %v", message.Header)
			}
			if message.Header.Get("From") != "user@gmail.com" {
				t.Fatalf("From = %q", message.Header.Get("From"))
			}
			if got := message.Header.Get("Content-Type"); !strings.HasPrefix(got, "multipart/alternative") {
				t.Fatalf("Content-Type = %q", got)
			}
			raw := batch7Lane9EmailDecodeRaw(t, body)
			if !strings.Contains(raw, "<p>Hello from Gmail</p>") || !strings.Contains(raw, "text/html") {
				t.Fatalf("raw = %s", raw)
			}
			if _, present := body["threadId"]; present {
				t.Fatalf("unexpected threadId on a fresh send: %v", body)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "gmail_msg_123"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "recipient@example.com", "subject": "Test Gmail",
		"html": "<p>Hello from Gmail</p>", "provider": "gmail",
	})
	if result["success"] != true || result["provider"] != "gmail" || result["id"] != "gmail_msg_123" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailProvider::test_gmail_missing_credentials
func TestBatch7Lane9EmailGmailMissingCredentials(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{"EMAIL_FROM": "test@example.com"})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "gmail",
	})
	if got := batch4ErrorText(t, result); got != "Gmail credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_resend_explicit_missing_key
func TestBatch7Lane9EmailResendMissingCredentials(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{"EMAIL_FROM": "test@example.com"})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "Resend credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_missing_from_email_returns_error
func TestBatch7Lane9EmailMissingSender(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "Sender email is required" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_explicit_from_email_overrides_env_var
func TestBatch7Lane9EmailSenderPrecedence(t *testing.T) {
	cases := []struct {
		name  string
		env   map[string]string
		input map[string]any
		want  string
	}{
		{"env fallback", map[string]string{"EMAIL_FROM": "default@company.com"}, map[string]any{}, "default@company.com"},
		{"explicit wins", map[string]string{"EMAIL_FROM": "default@company.com"}, map[string]any{"from_email": "custom@other.com"}, "custom@other.com"},
	}
	for _, testCase := range cases {
		registry, _ := batch7Lane9EmailFake(t, "email_tool", testCase.env,
			func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				if body["from"] != testCase.want {
					t.Fatalf("%s: from = %v, want %q", testCase.name, body["from"], testCase.want)
				}
				batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "email_1"})
			})
		input := map[string]any{
			"action": "send_email", "to": "test@example.com", "subject": "Test",
			"html": "<p>Hi</p>", "provider": "resend",
		}
		for key, value := range testCase.input {
			input[key] = value
		}
		if result := batch4Execute(t, registry, "email", input); result["success"] != true {
			t.Fatalf("%s: result = %v", testCase.name, result)
		}
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_empty_recipient_returns_error
func TestBatch7Lane9EmailEmptyRecipient(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "", "subject": "Test", "html": "<p>Hi</p>", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "At least one recipient email is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_empty_subject_returns_error
func TestBatch7Lane9EmailEmptySubject(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "", "html": "<p>Hi</p>", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "Subject must be 1-998 characters" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_subject_too_long_returns_error
func TestBatch7Lane9EmailSubjectTooLong(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": strings.Repeat("x", 999),
		"html": "<p>Hi</p>", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "Subject must be 1-998 characters" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestSendEmail::test_empty_html_returns_error
func TestBatch7Lane9EmailEmptyHTML(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test", "html": "", "provider": "resend",
	})
	if got := batch4ErrorText(t, result); got != "Email body (html) is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestProviderRequired::test_missing_provider_raises_type_error
func TestBatch7Lane9EmailInvalidProvider(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test", "html": "<p>Hi</p>", "provider": "carrier-pigeon",
	})
	if got := batch4ErrorText(t, result); got != "provider must be 'gmail' or 'resend'" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: EMAIL_OVERRIDE_TO is a testing seam upstream documents but
// does not cover with a unit test; the redirect shape is pinned here.
func TestBatch7Lane9EmailOverrideRecipient(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{
		"EMAIL_FROM": "test@example.com", "EMAIL_OVERRIDE_TO": "qa@example.com",
	}, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		to := batch4BodySlice(t, body["to"])
		if len(to) != 1 || to[0] != "qa@example.com" {
			t.Fatalf("to = %v", to)
		}
		if body["subject"] != "[TEST -> real@example.com] Test" {
			t.Fatalf("subject = %v", body["subject"])
		}
		if _, present := body["cc"]; present {
			t.Fatalf("unexpected cc: %v", body)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "email_3"})
	})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "real@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend", "cc": "cc@example.com",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailProvider::test_gmail_token_expired
func TestBatch7Lane9EmailGmailTokenExpired(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", map[string]string{"EMAIL_FROM": "user@gmail.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("Invalid credentials"))
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "gmail",
	})
	if got := batch4ErrorText(t, result); !strings.Contains(strings.ToLower(got), "expired") && !strings.Contains(strings.ToLower(got), "invalid") {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailProvider::test_gmail_api_error
func TestBatch7Lane9EmailGmailAPIError(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", map[string]string{"EMAIL_FROM": "user@gmail.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("Insufficient permissions"))
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "gmail",
	})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "403") || !strings.Contains(got, "Insufficient permissions") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestResendProvider::test_resend_api_error
func TestBatch7Lane9EmailResendAPIError(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_tool", map[string]string{"EMAIL_FROM": "test@example.com"},
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "test@example.com", "subject": "Test",
		"html": "<p>Hi</p>", "provider": "resend",
	})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "Resend API error") || !strings.Contains(got, "429") {
		t.Fatalf("error = %q", got)
	}
}

// batch7Lane9EmailOriginalMessage renders the Gmail get-message response for a
// reply, with the HTML body double-encoded as Gmail returns it.
func batch7Lane9EmailOriginalMessage(subject string) map[string]any {
	body := base64.URLEncoding.EncodeToString([]byte("<p>Original body</p>"))
	return map[string]any{
		"threadId": "thread-1",
		"payload": map[string]any{
			"mimeType": "multipart/alternative",
			"headers": []any{
				map[string]any{"name": "Message-ID", "value": "<m1@example.com>"},
				map[string]any{"name": "Subject", "value": subject},
				map[string]any{"name": "From", "value": "Bob <bob@example.com>"},
				map[string]any{"name": "Date", "value": "Mon, 1 Jan 2024 00:00:00 +0000"},
			},
			"parts": []any{map[string]any{
				"mimeType": "text/html",
				"body":     map[string]any{"data": body},
			}},
		},
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_successful_reply
func TestBatch7Lane9EmailReply(t *testing.T) {
	registry, recorder := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/users/me/messages/orig-1":
				if r.URL.Query().Get("format") != "full" {
					t.Fatalf("format = %q", r.URL.Query().Get("format"))
				}
				batch4WriteJSON(w, http.StatusOK, batch7Lane9EmailOriginalMessage("Hello"))
			case r.Method == http.MethodPost && r.URL.Path == "/users/me/messages/send":
				if body["threadId"] != "thread-1" {
					t.Fatalf("threadId = %v", body["threadId"])
				}
				message := batch7Lane9EmailParse(t, body)
				if message.Header.Get("To") != "Bob <bob@example.com>" {
					t.Fatalf("To = %q", message.Header.Get("To"))
				}
				if message.Header.Get("Subject") != "Re: Hello" {
					t.Fatalf("Subject = %q", message.Header.Get("Subject"))
				}
				if message.Header.Get("In-Reply-To") != "<m1@example.com>" || message.Header.Get("References") != "<m1@example.com>" {
					t.Fatalf("threading headers = %v", message.Header)
				}
				raw := batch7Lane9EmailDecodeRaw(t, body)
				if !strings.Contains(raw, `<div class="gmail_quote">`) || !strings.Contains(raw, "<p>Original body</p>") {
					t.Fatalf("raw = %s", raw)
				}
				batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "reply-1", "threadId": "thread-1"})
			default:
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
			}
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "<p>Thanks</p>",
	})
	if result["success"] != true || result["id"] != "reply-1" || result["threadId"] != "thread-1" {
		t.Fatalf("result = %v", result)
	}
	if result["to"] != "Bob <bob@example.com>" || result["subject"] != "Re: Hello" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_reply_preserves_existing_re_prefix
func TestBatch7Lane9EmailReplyExistingPrefix(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method == http.MethodGet {
				batch4WriteJSON(w, http.StatusOK, batch7Lane9EmailOriginalMessage("RE: Already"))
				return
			}
			message := batch7Lane9EmailParse(t, body)
			if message.Header.Get("Subject") != "RE: Already" {
				t.Fatalf("Subject = %q", message.Header.Get("Subject"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "reply-2", "threadId": "thread-1"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "<p>Thanks</p>",
	})
	if result["subject"] != "RE: Already" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_reply_with_cc
func TestBatch7Lane9EmailReplyWithCC(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method == http.MethodGet {
				batch4WriteJSON(w, http.StatusOK, batch7Lane9EmailOriginalMessage("Hello"))
				return
			}
			message := batch7Lane9EmailParse(t, body)
			if message.Header.Get("Cc") != "carol@example.com, dave@example.com" {
				t.Fatalf("Cc = %q", message.Header.Get("Cc"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "reply-3", "threadId": "thread-1"})
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "<p>Thanks</p>",
		"cc": []any{"carol@example.com", "dave@example.com"},
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_original_message_not_found
func TestBatch7Lane9EmailReplyOriginalNotFound(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("not found"))
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "missing", "html": "<p>Thanks</p>",
	})
	if got := batch4ErrorText(t, result); got != "Original message not found: missing" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_empty_message_id
func TestBatch7Lane9EmailReplyEmptyMessageID(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "  ", "html": "<p>Thanks</p>",
	})
	if got := batch4ErrorText(t, result); got != "message_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_empty_html
func TestBatch7Lane9EmailReplyEmptyHTML(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "",
	})
	if got := batch4ErrorText(t, result); got != "Reply body (html) is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_missing_credentials
func TestBatch7Lane9EmailReplyMissingCredentials(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "<p>Thanks</p>",
	})
	if got := batch4ErrorText(t, result); got != "Gmail credentials not configured" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_email_tool.py::TestGmailReplyEmail::test_send_401_returns_token_error
func TestBatch7Lane9EmailReplySendUnauthorized(t *testing.T) {
	registry, _ := batch7Lane9EmailFake(t, "email_gmail_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method == http.MethodGet {
				batch4WriteJSON(w, http.StatusOK, batch7Lane9EmailOriginalMessage("Hello"))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("expired"))
		})
	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "gmail_reply_email", "message_id": "orig-1", "html": "<p>Thanks</p>",
	})
	if got := batch4ErrorText(t, result); got != "Gmail token expired or invalid" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}
