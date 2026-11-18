package senders

// End-to-end action tests: the nine senders_tool actions against a temporary
// Hive home and a fake SMTP server. These pin the user-visible contract the
// parent lane cares about — a send with no configuration must error, and a
// configured mailbox must actually deliver and be recorded.

import (
	"context"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

func newTestEngine(t *testing.T, opts ...Option) *Engine {
	t.Helper()
	options := append([]Option{WithClock(testClock())}, opts...)
	engine, err := Open(t.TempDir(), options...)
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	return engine
}

func createMailbox(t *testing.T, engine *Engine, server *fakeSMTP, name string, canReceive bool, limit *int) Sender {
	t.Helper()
	host, port := server.hostPort(t)
	sender, err := engine.store.Create(Sender{
		Name:       name,
		Provider:   ProviderSMTP,
		AuthType:   AuthAPIKey,
		FromEmail:  strings.ToLower(strings.ReplaceAll(name, " ", ".")) + "@example.com",
		FromName:   name,
		Weight:     1,
		DailyLimit: limit,
		Enabled:    true,
		CanReceive: canReceive,
		Secret: map[string]any{
			"host":     host,
			"port":     port,
			"username": "sender@example.com",
			"password": "app-password",
			"security": "none",
		},
	})
	if err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	return sender
}

func execute(t *testing.T, engine *Engine, input map[string]any) map[string]any {
	t.Helper()
	result, err := engine.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("execute %v: %v", input["action"], err)
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("execute %v returned %T, want a map", input["action"], result)
	}
	return out
}

// Parity-exempt: upstream self-describes an empty pool (no Python test); this
// pins the requirement that an unconfigured send is a loud error, never a fake
// success or a logged reservation.
func TestSendFromSenderWithoutConfiguredSendersFailsLoudly(t *testing.T) {
	engine := newTestEngine(t)

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "anything",
		"to_email": "sarah@example.com",
		"subject":  "Hi",
		"text":     "Hello",
	})
	message, _ := out["error"].(string)
	if !strings.Contains(message, "No email senders are configured") || !strings.Contains(message, "setup_email_sender") {
		t.Fatalf("error = %q, want an explicit empty-pool error pointing at setup_email_sender", message)
	}
	if _, ok := out["success"]; ok {
		t.Fatalf("unconfigured send reported success: %#v", out)
	}
	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	if history["count"] != 0 {
		t.Fatalf("unconfigured send wrote %v log rows", history["count"])
	}
}

// Parity-exempt: no Python test for the local SMTP path; pins send -> log ->
// usage -> history as one observable loop.
func TestSendFromSenderDeliversAndLogsUsage(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Outbound One",
		"to_email": "sarah@example.com",
		"subject":  "Quick question",
		"text":     "Hi Sarah,\n\nWorth a chat?",
	})
	if out["success"] != true {
		t.Fatalf("send = %#v, want success", out)
	}
	if out["provider"] != ProviderSMTP || out["success"] != true {
		t.Fatalf("send = %#v, want provider smtp and success", out)
	}
	if _, ok := out["reply_tracking"]; !ok {
		t.Fatalf("send-only sender must warn about replies: %#v", out)
	}

	listing := execute(t, engine, map[string]any{"action": ActionListSenders})
	rows := listing["senders"].([]map[string]any)
	if len(rows) != 1 || rows[0]["sent_today"] != 1 {
		t.Fatalf("list_senders = %#v, want sent_today 1", listing)
	}
	if rows[0]["remaining_today"] != nil {
		t.Fatalf("remaining_today = %#v, want nil for an uncapped sender", rows[0]["remaining_today"])
	}

	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	sends := history["sends"].([]map[string]any)
	if len(sends) != 1 || sends[0]["status"] != StatusSent || sends[0]["to_email"] != "sarah@example.com" {
		t.Fatalf("history = %#v", history)
	}
}

// Parity-exempt: the cloud reservation is upstream's cap; this pins the local
// cap at the send boundary (not just in the picker).
func TestSendFromSenderEnforcesDailyLimit(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	limit := 1
	createMailbox(t, engine, server, "Capped", false, &limit)

	first := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Capped",
		"to_email": "one@example.com",
		"subject":  "First",
		"text":     "Hello",
	})
	if first["success"] != true {
		t.Fatalf("first send = %#v", first)
	}
	if first["remaining_today"] != 0 || first["daily_limit"] != 1 {
		t.Fatalf("first send budget = %#v", first)
	}

	second := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Capped",
		"to_email": "two@example.com",
		"subject":  "Second",
		"text":     "Hello",
	})
	if second["success"] == true || second["error"] == nil {
		t.Fatalf("second send = %#v, want a refusal", second)
	}
	if second["remaining_today"] != 0 {
		t.Fatalf("second remaining = %#v, want 0", second["remaining_today"])
	}
	guidance, _ := second["guidance"].(string)
	if !strings.Contains(guidance, "exhausted") {
		t.Fatalf("guidance = %q, want a daily-limit hint", guidance)
	}
}

// Parity-exempt: suppression is a cloud check upstream; this pins that the
// local list is enforced at send time, by any sender.
func TestSendFromSenderRefusesSuppressedRecipient(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	suppressed := execute(t, engine, map[string]any{
		"action": ActionSuppressRecipient,
		"email":  "Sarah@Example.com",
		"reason": "unsubscribed",
		"note":   "remove me",
	})
	if suppressed["suppressed"] != "sarah@example.com" {
		t.Fatalf("suppress = %#v", suppressed)
	}

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Outbound One",
		"to_email": "sarah@example.com",
		"subject":  "Follow-up",
		"text":     "Hello again",
	})
	if out["suppressed"] != true {
		t.Fatalf("send = %#v, want a suppressed refusal", out)
	}
	guidance, _ := out["guidance"].(string)
	if !strings.Contains(guidance, "any sender") {
		t.Fatalf("guidance = %q, want it to forbid trying another sender", guidance)
	}
}

// Parity-exempt: the duplicate key contract is upstream sendlog.py, untested
// there; this pins the "do not retry" refusal.
func TestSendFromSenderRefusesDuplicateMessage(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	request := map[string]any{
		"action":      ActionSendFromSender,
		"sender":      "Outbound One",
		"to_email":    "sarah@example.com",
		"subject":     "Same message",
		"text":        "Hello",
		"campaign_id": "campaign-1",
	}
	if out := execute(t, engine, request); out["success"] != true {
		t.Fatalf("first send = %#v", out)
	}
	second := execute(t, engine, request)
	if second["duplicate"] != true {
		t.Fatalf("second send = %#v, want duplicate=true", second)
	}
	guidance, _ := second["guidance"].(string)
	if !strings.Contains(guidance, "Do NOT retry") {
		t.Fatalf("guidance = %q", guidance)
	}
}

// Parity-exempt: no Python test for the SMTP wire body; pins the 652-email
// regression guard through the real send path.
func TestSendFromSenderDerivesHTMLFromPlainText(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "Outbound One",
		"to_email": "sarah@example.com",
		"subject":  "Line breaks",
		"text":     "First line\nSecond line",
	})
	if out["success"] != true {
		t.Fatalf("send = %#v", out)
	}
	raw := <-server.messages
	if !strings.Contains(raw, "First line<br>Second line") {
		t.Fatalf("plain text was not converted to real HTML line breaks:\n%s", raw)
	}
}

// Parity-exempt: local build has no ESP delivery; pins the explicit refusal
// instead of a fake success or an empty log row.
func TestSendFromSenderRejectsNonSMTPProviders(t *testing.T) {
	engine := newTestEngine(t)
	if _, err := engine.store.Create(Sender{
		Name: "ESP", Provider: ProviderSendGrid, AuthType: AuthAPIKey,
		FromEmail: "esp@example.com", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	out := execute(t, engine, map[string]any{
		"action":   ActionSendFromSender,
		"sender":   "ESP",
		"to_email": "sarah@example.com",
		"subject":  "Hi",
		"text":     "Hello",
	})
	message, _ := out["error"].(string)
	if !strings.Contains(message, "Aden cloud connector") {
		t.Fatalf("error = %q, want an explicit unsupported-provider reason", message)
	}
	if out["success"] == true {
		t.Fatalf("unsupported provider reported success: %#v", out)
	}
	history := execute(t, engine, map[string]any{"action": ActionSenderHistory})
	if history["count"] != 0 {
		t.Fatalf("failed provider gate wrote %v log rows", history["count"])
	}
}

// Parity-exempt: setup.py has no pytest coverage in this repo; behavior ported from setup_sender().
func TestSetupSenderSavesMailboxAndInfersInbox(t *testing.T) {
	engine := newTestEngine(t)

	out := execute(t, engine, map[string]any{
		"action":      ActionSetupSender,
		"provider":    "smtp.gmail.com",
		"from_email":  "richard@example.com",
		"from_name":   "Richard",
		"password":    "app-password",
		"smtp_port":   float64(587),
		"daily_limit": float64(40),
		"tags":        "cold, outbound",
	})
	if out["status"] != "saved" {
		t.Fatalf("setup = %#v, want saved", out)
	}
	if out["can_receive"] != true || out["imap_host"] != "imap.gmail.com" {
		t.Fatalf("setup = %#v, want the inbox inferred from smtp.gmail.com", out)
	}
	if out["credential_valid"] != nil {
		t.Fatalf("credential_valid = %#v, want null (not verified locally)", out["credential_valid"])
	}
	if _, ok := out["warning"]; !ok {
		t.Fatalf("saved sender must carry a verification warning: %#v", out)
	}

	sender, err := engine.store.Get("richard@example.com")
	if err != nil || sender == nil {
		// Name defaults to the from address; resolve by that label.
		sender, err = engine.store.Get("Richard")
	}
	if err != nil || sender == nil {
		t.Fatalf("stored sender missing: %v", err)
	}
	if sender.Provider != ProviderSMTP || !sender.CanReceive || sender.DailyLimit == nil || *sender.DailyLimit != 40 {
		t.Fatalf("stored sender = %+v", sender)
	}
	if sender.Secret["security"] != "starttls" || sender.Secret["imap_host"] != "imap.gmail.com" {
		t.Fatalf("secret = %#v", sender.Secret)
	}
	if len(sender.Tags) != 2 {
		t.Fatalf("tags = %#v", sender.Tags)
	}
}

// Parity-exempt: setup.py has no pytest coverage in this repo; behavior ported from setup_sender().
func TestSetupSenderNeedsUserForMissingPassword(t *testing.T) {
	engine := newTestEngine(t)
	out := execute(t, engine, map[string]any{
		"action":     ActionSetupSender,
		"provider":   "smtp",
		"from_email": "richard@example.com",
		"smtp_host":  "smtp.gmail.com",
	})
	if out["status"] != "needs_user" {
		t.Fatalf("setup = %#v, want needs_user", out)
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "app password") {
		t.Fatalf("reason = %q", reason)
	}
	url, _ := out["setup_url"].(string)
	if !strings.HasPrefix(url, "hive://senders/add") {
		t.Fatalf("setup_url = %q", url)
	}
}

// Parity-exempt: setup.py has no pytest coverage in this repo; behavior ported from setup_sender().
func TestSetupSenderSendsESPProvidersToHandoff(t *testing.T) {
	engine := newTestEngine(t)
	out := execute(t, engine, map[string]any{
		"action":     ActionSetupSender,
		"provider":   "sendgrid",
		"from_email": "richard@example.com",
		"api_key":    "SG.key",
	})
	if out["status"] != "needs_user" || out["provider"] != ProviderSendGrid {
		t.Fatalf("setup = %#v, want a needs_user handoff", out)
	}
	reason, _ := out["reason"].(string)
	if !strings.Contains(reason, "SMTP") {
		t.Fatalf("reason = %q, want the local SMTP constraint", reason)
	}
}

// Parity-exempt: setup.py has no pytest coverage in this repo; behavior ported from setup_sender().
func TestSetupSenderRejectsInvalidFromAddress(t *testing.T) {
	engine := newTestEngine(t)
	out := execute(t, engine, map[string]any{
		"action":     ActionSetupSender,
		"provider":   "smtp",
		"from_email": "not-an-email",
	})
	if out["status"] != "error" {
		t.Fatalf("setup = %#v, want error", out)
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestAdjustSenderReportsChanges(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	created := createMailbox(t, engine, server, "Outbound One", false, nil)

	out := execute(t, engine, map[string]any{
		"action":      ActionAdjustSender,
		"sender":      created.Name,
		"daily_limit": float64(5),
		"weight":      float64(3),
		"enabled":     "false",
		"name":        "Renamed Sender",
		"tags":        "cold,outbound",
	})
	changed, _ := out["changed"].(map[string]any)
	for _, key := range []string{"daily_limit", "weight", "enabled", "name", "tags"} {
		if _, ok := changed[key]; !ok {
			t.Fatalf("changed missing %q: %#v", key, changed)
		}
	}
	if out["sender"] != "Renamed Sender" || out["weight"] != 3 || out["enabled"] != false {
		t.Fatalf("adjust = %#v", out)
	}

	listing := execute(t, engine, map[string]any{"action": ActionListSenders})
	rows := listing["senders"].([]map[string]any)
	if len(rows) != 1 || rows[0]["name"] != "Renamed Sender" || rows[0]["weight"] != 3 || rows[0]["enabled"] != false {
		t.Fatalf("list after adjust = %#v", listing)
	}
	if rows[0]["daily_limit"] != 5 {
		t.Fatalf("daily_limit = %#v", rows[0]["daily_limit"])
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestAdjustSenderRejectsNoop(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	out := execute(t, engine, map[string]any{"action": ActionAdjustSender, "sender": "Outbound One"})
	message, _ := out["error"].(string)
	if !strings.Contains(message, "Nothing to change") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestPickSenderRotatesRoundRobin(t *testing.T) {
	engine := newTestEngine(t)
	if _, err := engine.store.Create(Sender{Name: "A", Provider: ProviderSMTP, FromEmail: "a@example.com", Enabled: true, Weight: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.store.Create(Sender{Name: "B", Provider: ProviderSMTP, FromEmail: "b@example.com", Enabled: true, Weight: 1}); err != nil {
		t.Fatal(err)
	}

	first := execute(t, engine, map[string]any{"action": ActionPickSender})
	second := execute(t, engine, map[string]any{"action": ActionPickSender})
	third := execute(t, engine, map[string]any{"action": ActionPickSender})
	if first["name"] == second["name"] {
		t.Fatalf("rotation did not advance: %#v then %#v", first, second)
	}
	if third["name"] != first["name"] {
		t.Fatalf("rotation did not wrap: %#v vs %#v", third, first)
	}
}

// Parity-exempt: rotation.daily-limit eligibility has no Python test; this pins
// the zero-limit meaning ("block all sends").
func TestPickSenderHonorsDailyLimit(t *testing.T) {
	engine := newTestEngine(t)
	zero := 0
	if _, err := engine.store.Create(Sender{Name: "Blocked", Provider: ProviderSMTP, FromEmail: "a@example.com", Enabled: true, Weight: 1, DailyLimit: &zero}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.store.Create(Sender{Name: "Open", Provider: ProviderSMTP, FromEmail: "b@example.com", Enabled: true, Weight: 1}); err != nil {
		t.Fatal(err)
	}

	out := execute(t, engine, map[string]any{"action": ActionPickSender})
	if out["name"] != "Open" {
		t.Fatalf("pick = %#v, want the uncapped sender", out)
	}
}

// Parity: tools/tests/test_senders_feature_flag.py::test_senders_present_when_enabled
func TestActionNamesMatchUpstreamSuite(t *testing.T) {
	want := map[string]struct{}{
		"list_senders": {}, "send_from_sender": {}, "send_campaign": {},
		"setup_email_sender": {}, "adjust_sender": {}, "pick_sender": {},
		"sender_history": {}, "suppress_recipient": {}, "list_suppressed": {},
	}
	names := ActionNames()
	if len(names) != len(want) {
		t.Fatalf("actions = %#v, want %d", names, len(want))
	}
	for _, name := range names {
		if _, ok := want[name]; !ok {
			t.Fatalf("unexpected action %q", name)
		}
	}
}

// Parity-exempt: upstream registers nine separate MCP tools; this port follows
// the repo's one-tool/action-selector convention, so the registry shape has no
// Python counterpart.
func TestRegisterInstallsToolWithNineActions(t *testing.T) {
	engine := newTestEngine(t)
	registry := tools.NewToolRegistry()
	if installed := engine.Register(registry); installed != 1 {
		t.Fatalf("installed = %d, want 1", installed)
	}
	tool, ok := registry.GetTool(ToolName)
	if !ok {
		t.Fatalf("tool %q not registered", ToolName)
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties = %#v", tool.Parameters)
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema = %#v", properties["action"])
	}
	enum, ok := action["enum"].([]string)
	if !ok || len(enum) != 9 {
		t.Fatalf("action enum = %#v, want 9 actions", action["enum"])
	}

	result, err := registry.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name:  ToolName,
		Input: map[string]any{"action": ActionListSenders},
	})
	if err != nil {
		t.Fatalf("registry execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("list_senders errored: %s", result.Content)
	}
	if !strings.Contains(result.Content, `"count"`) {
		t.Fatalf("list_senders content = %q", result.Content)
	}
}

// Parity-exempt: error shape for unknown actions has no Python counterpart
// (FastMCP rejects them before the function body).
func TestExecuteReportsUnknownAction(t *testing.T) {
	engine := newTestEngine(t)
	result, err := engine.Execute(context.Background(), map[string]any{"action": "not-real"})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	message, _ := out["error"].(string)
	if !strings.Contains(message, "unknown action") {
		t.Fatalf("error = %q", message)
	}
	actions, ok := out["actions"].([]string)
	if !ok || len(actions) != 9 {
		t.Fatalf("actions = %#v", out["actions"])
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestListSuppressedDeduplicatesByAddress(t *testing.T) {
	engine := newTestEngine(t)
	execute(t, engine, map[string]any{"action": ActionSuppressRecipient, "email": "a@example.com", "reason": "manual"})
	execute(t, engine, map[string]any{"action": ActionSuppressRecipient, "email": "A@Example.com", "reason": "complained"})

	out := execute(t, engine, map[string]any{"action": ActionListSuppressed})
	if out["count"] != 1 {
		t.Fatalf("list_suppressed = %#v, want one deduplicated row", out)
	}
	rows := out["suppressed"].([]map[string]any)
	if rows[0]["reason"] != "complained" {
		t.Fatalf("reason = %#v, want the refreshed reason", rows[0]["reason"])
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestSendCampaignRotatesAndTracksReplies(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", true, nil)
	createMailbox(t, engine, server, "Outbound Two", true, nil)

	out := execute(t, engine, map[string]any{
		"action":      ActionSendCampaign,
		"recipients":  `["r1@example.com","r2@example.com","r3@example.com","r4@example.com"]`,
		"subject":     "Hello",
		"text":        "Body",
		"policy":      PolicyRoundRobin,
		"campaign_id": "campaign-1",
		"colony_id":   "colony-1",
	})
	if out["sent"] != 4 || out["failed"] != 0 || out["total"] != 4 {
		t.Fatalf("campaign = %#v", out)
	}
	if out["tracked_for_replies"] != 4 {
		t.Fatalf("tracked_for_replies = %#v, want 4", out["tracked_for_replies"])
	}
	results := out["results"].([]map[string]any)
	sendersUsed := map[string]struct{}{}
	tokens := map[string]struct{}{}
	for _, row := range results {
		if row["success"] != true {
			t.Fatalf("campaign row = %#v", row)
		}
		sendersUsed[row["sender"].(string)] = struct{}{}
		token, ok := row["conversation"].(string)
		if !ok || token == "" {
			t.Fatalf("tracked row has no conversation token: %#v", row)
		}
		tokens[token] = struct{}{}
	}
	if len(sendersUsed) != 2 {
		t.Fatalf("senders used = %#v, want both", sendersUsed)
	}
	if len(tokens) != 4 {
		t.Fatalf("tokens = %d, want one per message", len(tokens))
	}

	history := execute(t, engine, map[string]any{"action": ActionSenderHistory, "limit": float64(10)})
	if history["count"] != 4 {
		t.Fatalf("history = %#v", history)
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestSendCampaignWarnsWhenSendersCannotReceive(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Send Only", false, nil)

	out := execute(t, engine, map[string]any{
		"action":     ActionSendCampaign,
		"recipients": "a@example.com,b@example.com",
		"subject":    "Hello",
		"text":       "Body",
	})
	if out["sent"] != 2 || out["tracked_for_replies"] != 0 {
		t.Fatalf("campaign = %#v", out)
	}
	warning, _ := out["warning"].(string)
	if !strings.Contains(warning, "No recipient is tracked") {
		t.Fatalf("warning = %q", warning)
	}
}

// Parity-exempt: the action functions in senders_tool.py have no pytest coverage (only the body helpers do); behavior ported from the documented action contract.
func TestSenderHistoryFiltersByRecipient(t *testing.T) {
	server := newFakeSMTP(t)
	engine := newTestEngine(t)
	createMailbox(t, engine, server, "Outbound One", false, nil)

	for _, to := range []string{"one@example.com", "two@example.com"} {
		out := execute(t, engine, map[string]any{
			"action":   ActionSendFromSender,
			"sender":   "Outbound One",
			"to_email": to,
			"subject":  "Hi",
			"text":     "Hello",
		})
		if out["success"] != true {
			t.Fatalf("send to %s = %#v", to, out)
		}
	}

	out := execute(t, engine, map[string]any{"action": ActionSenderHistory, "to_email": "TWO@example.com"})
	sends := out["sends"].([]map[string]any)
	if out["count"] != 1 || sends[0]["to_email"] != "two@example.com" {
		t.Fatalf("history filter = %#v", out)
	}
}
