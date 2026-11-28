package senders

// providers.py dispatches ESP/OAuth sends through the Aden cloud connector.
// That connector is not part of this build (it needs team credentials and
// network), so the local engine keeps its explicit refusal by default. These
// tests pin the adapter seam with a deterministic fake: quota, suppression,
// send-log and thread tracking must behave identically around either
// transport, and a provider failure must never masquerade as a send.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeProviderAdapter is the deterministic cloud/ESP stand-in. It records
// every request and returns queued outcomes or an injected transport error;
// the default success id is derived from the call index, never randomness.
type fakeProviderAdapter struct {
	name     string
	err      error
	outcomes []ProviderResult

	mu       sync.Mutex
	requests []ProviderRequest
}

func (f *fakeProviderAdapter) Name() string {
	if f.name == "" {
		return "fake-cloud"
	}
	return f.name
}

func (f *fakeProviderAdapter) Send(_ context.Context, req ProviderRequest) (ProviderResult, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	index := len(f.requests)
	transportErr := f.err
	outcome := ProviderResult{Success: true, Provider: req.Sender.Provider, ID: fmt.Sprintf("fake-%06d", index)}
	if len(f.outcomes) > 0 {
		outcome = f.outcomes[0]
		f.outcomes = f.outcomes[1:]
		if outcome.Provider == "" {
			outcome.Provider = req.Sender.Provider
		}
		if outcome.Success && outcome.ID == "" {
			outcome.ID = fmt.Sprintf("fake-%06d", index)
		}
	}
	f.mu.Unlock()
	if transportErr != nil {
		return ProviderResult{}, transportErr
	}
	return outcome, nil
}

func (f *fakeProviderAdapter) Calls() []ProviderRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProviderRequest(nil), f.requests...)
}

func createProviderSender(t *testing.T, engine *Engine, provider, name string, canReceive bool, limit *int) Sender {
	t.Helper()
	sender := Sender{
		Name:       name,
		Provider:   provider,
		AuthType:   authTypeFor(provider),
		FromEmail:  strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com",
		Weight:     1,
		DailyLimit: limit,
		Enabled:    true,
		CanReceive: canReceive,
		Secret:     map[string]any{"api_key": "fake-key"},
	}
	if sender.AuthType == AuthOAuth {
		sender.IntegrationID = "int_1"
	}
	created, err := engine.store.Create(sender)
	if err != nil {
		t.Fatalf("create provider sender: %v", err)
	}
	return created
}

// Parity-exempt: providers.py send_one has no pytest coverage in this repo;
// this pins the adapter request/response mapping and the send log.
func TestProviderAdapterDeliversESPAndLogs(t *testing.T) {
	adapter := &fakeProviderAdapter{}
	engine := newTestEngine(t, WithProviderAdapter(adapter))
	createProviderSender(t, engine, ProviderSendGrid, "ESP One", false, nil)

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "ESP One",
		"to_email": "sarah@example.com",
		"subject":  "Worth a chat?",
		"text":     "Hi Sarah",
	})
	if out["success"] != true {
		t.Fatalf("send = %#v, want success through the adapter", out)
	}
	if out["provider"] != ProviderSendGrid || out["id"] != "fake-000001" {
		t.Fatalf("send = %#v, want provider sendgrid and a deterministic fake id", out)
	}
	if _, ok := out["reply_tracking"]; !ok {
		t.Fatalf("send-only ESP sender must warn about replies: %#v", out)
	}

	calls := adapter.Calls()
	if len(calls) != 1 {
		t.Fatalf("adapter calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.ToEmail != "sarah@example.com" || call.Subject != "Worth a chat?" {
		t.Fatalf("adapter request = %+v", call)
	}
	if call.HTML != "<p>Hi Sarah</p>" || call.Text != "Hi Sarah" {
		t.Fatalf("adapter body = html %q text %q, want the resolved pair", call.HTML, call.Text)
	}
	if call.MessageID != "" {
		t.Fatalf("send-only sender minted a Message-ID: %q", call.MessageID)
	}
	if call.Sender.ID == "" || call.Sender.Secret["api_key"] != "fake-key" {
		t.Fatalf("adapter did not receive the stored sender row: %+v", call.Sender)
	}

	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	sends := history["sends"].([]map[string]any)
	if history["count"] != 1 || sends[0]["status"] != StatusSent {
		t.Fatalf("history = %#v", history)
	}
}

// Parity-exempt: providers.py has no pytest coverage for the OAuth path; this
// pins that an OAuth sender goes through the adapter and that its reply
// conversation is persisted with the minted Message-ID.
func TestProviderAdapterOAuthSenderRecordsThread(t *testing.T) {
	adapter := &fakeProviderAdapter{}
	engine := newTestEngine(t, WithProviderAdapter(adapter))
	createProviderSender(t, engine, ProviderGoogle, "Google One", true, nil)

	out := execute(t, engine, map[string]any{
		"action":      ActionSendFromSender,
		"sender":      "Google One",
		"to_email":    "sarah@example.com",
		"subject":     "Hello",
		"text":        "Body",
		"colony_id":   "colony-1",
		"campaign_id": "campaign-1",
	})
	if out["success"] != true {
		t.Fatalf("send = %#v", out)
	}
	token, ok := out["conversation"].(string)
	if !ok || token == "" {
		t.Fatalf("trackable OAuth send has no conversation token: %#v", out)
	}

	calls := adapter.Calls()
	if len(calls) != 1 || calls[0].MessageID == "" {
		t.Fatalf("adapter request = %+v, want a minted Message-ID", calls)
	}
	thread, err := engine.threads.Get(token)
	if err != nil {
		t.Fatal(err)
	}
	if thread == nil {
		t.Fatalf("conversation %q was not persisted", token)
	}
	if thread.SenderID == "" || thread.ColonyID != "colony-1" || thread.FromEmail != "google.one@example.com" {
		t.Fatalf("thread = %+v", thread)
	}
	if thread.MessageID != calls[0].MessageID {
		t.Fatalf("thread message_id = %q, adapter saw %q", thread.MessageID, calls[0].MessageID)
	}
	if ExtractThreadToken(thread.MessageID) != token {
		t.Fatalf("thread message_id %q does not carry token %q", thread.MessageID, token)
	}

	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	sends := history["sends"].([]map[string]any)
	if sends[0]["conversation_token"] != token {
		t.Fatalf("history lost the conversation token: %#v", sends[0])
	}
}

// Parity-exempt: providers.py has no pytest coverage for its error mapping;
// this pins the deliberate failure policy around the adapter: a failed send
// releases the quota slot, writes no conversation, and is retryable.
func TestProviderFailureFailsClosedAndReleasesQuota(t *testing.T) {
	limit := 1
	cases := []struct {
		name      string
		configure func(*fakeProviderAdapter)
		wantError string
	}{
		{
			name:      "transport error",
			configure: func(adapter *fakeProviderAdapter) { adapter.err = errors.New("cloud unreachable") },
			wantError: "cloud unreachable",
		},
		{
			name: "provider rejection",
			configure: func(adapter *fakeProviderAdapter) {
				adapter.outcomes = []ProviderResult{{Success: false, Error: "SendGrid API error (HTTP 401): bad key"}}
			},
			wantError: "SendGrid API error (HTTP 401): bad key",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			adapter := &fakeProviderAdapter{}
			testCase.configure(adapter)
			engine := newTestEngine(t, WithProviderAdapter(adapter))
			createProviderSender(t, engine, ProviderGoogle, "Google One", true, &limit)

			out := execute(t, engine, map[string]any{
				"action":    ActionSendFromSender,
				"sender":    "Google One",
				"to_email":  "one@example.com",
				"subject":   "First",
				"text":      "Body",
				"colony_id": "colony-1",
			})
			if out["success"] == true {
				t.Fatalf("failed provider reported success: %#v", out)
			}
			message, _ := out["error"].(string)
			if !strings.Contains(message, testCase.wantError) {
				t.Fatalf("error = %q, want %q", message, testCase.wantError)
			}
			if _, ok := out["conversation"]; ok {
				t.Fatalf("failed send claimed a conversation: %#v", out)
			}

			history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
			sends := history["sends"].([]map[string]any)
			if history["count"] != 1 || sends[0]["status"] != StatusFailed {
				t.Fatalf("history = %#v, want one failed row", history)
			}
			threads, err := engine.threads.Conversations()
			if err != nil || len(threads) != 0 {
				t.Fatalf("failed send persisted %d conversations (err %v)", len(threads), err)
			}

			// The failed slot was released, so a retry fits under the cap.
			adapter.mu.Lock()
			adapter.err = nil
			adapter.outcomes = nil
			adapter.mu.Unlock()
			retry := execute(t, engine, map[string]any{
				"action":    ActionSendFromSender,
				"sender":    "Google One",
				"to_email":  "two@example.com",
				"subject":   "Second",
				"text":      "Body",
				"colony_id": "colony-1",
			})
			if retry["success"] != true {
				t.Fatalf("retry after failure = %#v, want the released slot to be usable", retry)
			}
			if retry["remaining_today"] != 0 {
				t.Fatalf("retry remaining_today = %#v, want 0", retry["remaining_today"])
			}
		})
	}
}

// Parity-exempt: sendlog.py reserves in cloud; this pins that quota refusal
// still happens locally, before the adapter is ever invoked.
func TestProviderQuotaRejectionSkipsAdapter(t *testing.T) {
	adapter := &fakeProviderAdapter{}
	engine := newTestEngine(t, WithProviderAdapter(adapter))
	zero := 0
	createProviderSender(t, engine, ProviderSendGrid, "Capped ESP", false, &zero)

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Capped ESP",
		"to_email": "sarah@example.com",
		"subject":  "Hi",
		"text":     "Body",
	})
	if out["success"] == true || out["daily_limit"] != 0 || out["remaining_today"] != 0 {
		t.Fatalf("quota refusal = %#v", out)
	}
	guidance, _ := out["guidance"].(string)
	if !strings.Contains(guidance, "exhausted") {
		t.Fatalf("guidance = %q, want a daily-limit hint", guidance)
	}
	if calls := adapter.Calls(); len(calls) != 0 {
		t.Fatalf("adapter was called %d times for an exhausted sender", len(calls))
	}
	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	if history["count"] != 0 {
		t.Fatalf("refused send wrote log rows: %#v", history)
	}
}

// Parity-exempt: providers.py has no pytest coverage for its unsupported
// dispatch fallback; this pins the default path: with no adapter, a non-SMTP
// sender stays an explicit refusal and touches neither the log nor threads.
func TestProviderWithoutAdapterKeepsExplicitFallbackRefusal(t *testing.T) {
	engine := newTestEngine(t)
	createProviderSender(t, engine, ProviderMailjet, "Mailjet One", true, nil)

	out := execute(t, engine, map[string]any{
		"action":    ActionSendFromSender,
		"sender":    "Mailjet One",
		"to_email":  "sarah@example.com",
		"subject":   "Hi",
		"text":      "Body",
		"colony_id": "colony-1",
	})
	message, _ := out["error"].(string)
	if !strings.Contains(message, "Aden cloud connector") {
		t.Fatalf("error = %q, want the explicit connector refusal", message)
	}
	if out["success"] == true {
		t.Fatalf("unconfigured provider reported success: %#v", out)
	}
	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	if history["count"] != 0 {
		t.Fatalf("refused send wrote log rows: %#v", history)
	}
	threads, err := engine.threads.Conversations()
	if err != nil || len(threads) != 0 {
		t.Fatalf("refused send persisted conversations: %+v (err %v)", threads, err)
	}
}

// Parity-exempt: threads.py has no pytest coverage; this pins the inbound
// reply routing contract (In-Reply-To then References, unknown tokens inert).
func TestEngineRecordThreadReplyMapsHeaders(t *testing.T) {
	engine := newTestEngine(t)
	token := "1234567890abcdef"
	if _, err := engine.threads.RecordSend(sampleThreadSend(token)); err != nil {
		t.Fatal(err)
	}

	thread, err := engine.RecordThreadReply("", "<hive."+token+"@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	if thread == nil || thread.Replies != 1 || thread.LastReplyAt == nil {
		t.Fatalf("reply via References = %+v", thread)
	}
	thread, err = engine.RecordThreadReply("<hive."+token+"@example.com>", "")
	if err != nil {
		t.Fatal(err)
	}
	if thread == nil || thread.Replies != 2 {
		t.Fatalf("reply via In-Reply-To = %+v", thread)
	}

	foreign, err := engine.RecordThreadReply("<20260101.abcdef@mail.example.com>", "")
	if err != nil || foreign != nil {
		t.Fatalf("foreign reply = %+v (err %v), want nil", foreign, err)
	}
}

// Parity-exempt: providers.py has no pytest coverage for send_one's dispatch;
// this pins that a HubSpot sender must never reach an adapter without the
// marketing-email id that replaces subject/html.
func TestProviderHubSpotRequiresEmailIDBeforeQuota(t *testing.T) {
	adapter := &fakeProviderAdapter{}
	engine := newTestEngine(t, WithProviderAdapter(adapter))
	createProviderSender(t, engine, ProviderHubSpot, "HubSpot One", false, nil)

	refused := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "HubSpot One",
		"to_email": "sarah@example.com",
	})
	message, _ := refused["error"].(string)
	if !strings.Contains(message, "hubspot_email_id") {
		t.Fatalf("error = %q, want the missing marketing-email id", message)
	}
	if calls := adapter.Calls(); len(calls) != 0 {
		t.Fatalf("adapter was called without a hubspot_email_id")
	}
	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	if history["count"] != 0 {
		t.Fatalf("invalid HubSpot send wrote log rows: %#v", history)
	}

	out := execute(t, engine, map[string]any{
		"action":           ActionSendFromSender,
		"sender":           "HubSpot One",
		"to_email":         "sarah@example.com",
		"hubspot_email_id": float64(42),
	})
	if out["success"] != true {
		t.Fatalf("HubSpot send = %#v", out)
	}
	calls := adapter.Calls()
	if len(calls) != 1 || calls[0].HubspotEmailID != 42 {
		t.Fatalf("adapter request = %+v, want hubspot_email_id 42", calls)
	}
}

// Parity-exempt: threads.py swallows a write failure and still reports the
// token; this local negative regression refuses to claim a conversation that
// cannot resolve, while still reporting the already-delivered mail as sent.
func TestThreadPersistenceFailureDoesNotClaimConversation(t *testing.T) {
	adapter := &fakeProviderAdapter{}
	engine := newTestEngine(t, WithProviderAdapter(adapter))
	createProviderSender(t, engine, ProviderGoogle, "Google One", true, nil)

	if err := os.MkdirAll(engine.Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"0123456789abcdef": {"token": `)
	if err := os.WriteFile(filepath.Join(engine.Dir(), threadsFile), corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	out := execute(t, engine, map[string]any{
		"action":    ActionSendFromSender,
		"sender":    "Google One",
		"to_email":  "sarah@example.com",
		"subject":   "Hello",
		"text":      "Body",
		"colony_id": "colony-1",
	})
	if out["success"] != true {
		t.Fatalf("delivered mail must still report success: %#v", out)
	}
	if _, ok := out["conversation"]; ok {
		t.Fatalf("undurable conversation was reported as trackable: %#v", out)
	}
	if _, ok := out["thread_error"]; !ok {
		t.Fatalf("thread persistence failure was swallowed: %#v", out)
	}
	tracking, _ := out["reply_tracking"].(string)
	if !strings.Contains(tracking, "will not resolve") {
		t.Fatalf("reply_tracking = %q, want an explicit no-tracking warning", tracking)
	}

	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	sends := history["sends"].([]map[string]any)
	if history["count"] != 1 || sends[0]["status"] != StatusSent {
		t.Fatalf("history = %#v, want the delivered row sent", history)
	}
}
