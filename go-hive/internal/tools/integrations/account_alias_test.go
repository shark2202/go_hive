package integrations

// Account-alias routing tests: every module whose upstream tool exposes the
// `account` selector must resolve that alias through the credential view before
// falling back to its single configured token, and the request must carry the
// resolved token in its auth header. Modules without upstream account support
// (obsidian, google sheets) must keep ignoring the parameter; their tests here
// pin that difference so the support matrix stays visible.

import (
	"context"
	"net/http"
	"sort"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// aliasAccounts is an in-test AccountSource: provider -> alias -> token.
type aliasAccounts map[string]map[string]string

func (a aliasAccounts) Accounts(provider string) []map[string]any {
	out := []map[string]any{}
	providers := make([]string, 0, len(a))
	for name := range a {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	for _, name := range providers {
		if provider != "" && provider != name {
			continue
		}
		aliases := make([]string, 0, len(a[name]))
		for alias := range a[name] {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			out = append(out, map[string]any{"provider": name, "alias": alias})
		}
	}
	return out
}

func (a aliasAccounts) AccountToken(provider, alias string) (string, bool) {
	aliases, ok := a[provider]
	if !ok {
		return "", false
	}
	token, ok := aliases[alias]
	return token, ok
}

// aliasSource is the credential view the account-aware modules resolve against.
func aliasSource() aliasAccounts {
	return aliasAccounts{
		"hubspot":    {"work": "hubspot-work-token", "sandbox": "hubspot-sandbox-token"},
		"notion":     {"work": "notion-work-token"},
		"discord":    {"work": "discord-work-token"},
		"mattermost": {"work": "mattermost-work-token"},
		"google":     {"work": "google-work-token"},
	}
}

// Parity: tools/tests/tools/test_hubspot_tool.py::test_credential_store_account_alias
func TestBatch5HubSpotAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch5Lane4TestRegistry(t, "hubspot_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	}, WithAccountSource(aliasSource()))
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane", "account": "work",
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer hubspot-work-token" {
		t.Fatalf("Authorization = %q", got)
	}

	// A second alias must select the second token, not the module default.
	other, otherRecorder := batch5Lane4TestRegistry(t, "hubspot_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	}, WithAccountSource(aliasSource()))
	batch5Lane4Run(t, other, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane", "account": "sandbox",
	})
	if got := otherRecorder.last(t).Header.Get("Authorization"); got != "Bearer hubspot-sandbox-token" {
		t.Fatalf("second alias Authorization = %q", got)
	}

	// Without an alias the module token stays in charge.
	plain, plainRecorder := batch5Lane4TestRegistry(t, "hubspot_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	}, WithAccountSource(aliasSource()))
	batch5Lane4Run(t, plain, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane",
	})
	if got := plainRecorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("module token Authorization = %q", got)
	}
}

// Parity-exempt: upstream returns None from get_by_alias and raises its own
// "credentials not configured" value; it has no test for the miss path.
func TestBatch5HubSpotUnresolvedAccountAliasReportsAuthError(t *testing.T) {
	registry, recorder := batch5Lane4TestRegistry(t, "hubspot_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		t.Fatalf("request must not be sent for an unknown alias")
	}, WithAccountSource(aliasSource()))
	result := batch5Lane4Run(t, registry, "hubspot", map[string]any{
		"action": "hubspot_search_contacts", "query": "jane", "account": "missing",
	})
	if batch5Lane4ErrorText(t, result) != "HubSpot credentials not configured" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity-exempt: upstream notion_tool has no account-alias test; the behavior
// mirrors _get_credentials(credentials, account) -> get_by_alias("notion",
// account).
func TestBatch4NotionAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"results": []any{}})
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_search", "query": "roadmap", "account": "work",
	})
	if result["count"] != 0 {
		t.Fatalf("result = %v", result)
	}
	request := recorder.last(t)
	if got := request.Header.Get("Authorization"); got != "Bearer notion-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := request.Header.Get("Notion-Version"); got != batch4NotionVersion {
		t.Fatalf("Notion-Version = %q", got)
	}
}

// Parity-exempt: no upstream test for the notion alias miss path.
func TestBatch4NotionUnresolvedAccountAliasReportsAuthError(t *testing.T) {
	registry, recorder := batch4TestRegistry(t, "notion_tool", func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		t.Fatalf("request must not be sent for an unknown alias")
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "notion", map[string]any{
		"action": "notion_search", "query": "roadmap", "account": "missing",
	})
	if result["error"] != "NOTION_API_TOKEN not set" {
		t.Fatalf("result = %v", result)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(recorder.requests))
	}
}

// Parity-exempt: upstream discord_tool has no account-alias test; the behavior
// mirrors _get_token(account) -> get_by_alias("discord", account).
func TestBatch7Lane8DiscordAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			batch4WriteJSON(w, http.StatusOK, []any{})
		}, WithAccountSource(aliasSource()))
	result := batch7Lane8Execute(t, registry, "discord", map[string]any{
		"action": "discord_list_guilds", "account": "work",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bot discord-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: upstream mattermost_tool has no account-alias test; the
// behavior mirrors _get_token(account) -> get_by_alias("mattermost", account).
func TestBatch7Lane8MattermostAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			batch4WriteJSON(w, http.StatusOK, []any{})
		}, WithAccountSource(aliasSource()))
	result := batch7Lane8Execute(t, registry, "mattermost", map[string]any{
		"action": "mattermost_list_teams", "account": "work",
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer mattermost-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: no upstream test for the discord/mattermost alias miss path.
func TestBatch7Lane8UnresolvedAccountAliasReportsAuthError(t *testing.T) {
	discordRegistry, discordRecorder := batch7Lane8TestRegistry(t, "discord_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			t.Fatalf("request must not be sent for an unknown alias")
		}, WithAccountSource(aliasSource()))
	result := batch7Lane8Execute(t, discordRegistry, "discord", map[string]any{
		"action": "discord_list_guilds", "account": "missing",
	})
	if result["error"] != "Discord credentials not configured" {
		t.Fatalf("discord = %v", result)
	}
	if len(discordRecorder.requests) != 0 {
		t.Fatalf("discord requests = %d, want 0", len(discordRecorder.requests))
	}

	mattermostRegistry, mattermostRecorder := batch7Lane8TestRegistry(t, "mattermost_tool", nil,
		func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			t.Fatalf("request must not be sent for an unknown alias")
		}, WithAccountSource(aliasSource()))
	result = batch7Lane8Execute(t, mattermostRegistry, "mattermost", map[string]any{
		"action": "mattermost_list_teams", "account": "missing",
	})
	if result["error"] != "Mattermost credentials not configured" {
		t.Fatalf("mattermost = %v", result)
	}
	if len(mattermostRecorder.requests) != 0 {
		t.Fatalf("mattermost requests = %d, want 0", len(mattermostRecorder.requests))
	}
}

// Parity: tools/tests/tools/test_google_docs_tool.py::test_credential_store_account_alias
func TestBatch5Lane3GoogleDocsAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"documentId": "doc-1"})
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_get_document", "document_id": "doc-1", "account": "work",
	})
	if result["documentId"] != "doc-1" {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer google-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: no upstream test for the google docs alias miss path.
func TestBatch5Lane3GoogleDocsUnresolvedAccountAliasReportsAuthError(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		t.Fatalf("request must not be sent for an unknown alias")
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "google_docs", map[string]any{
		"action": "google_docs_get_document", "document_id": "doc-1", "account": "missing",
	})
	if result["error"] != "Google Docs credentials not configured" {
		t.Fatalf("result = %v", result)
	}
	if len(recorder.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(recorder.requests))
	}
}

// Parity-exempt: upstream obsidian_tool has no `account` parameter at all.
// Passing one must not change the credential a note call uses.
func TestBatch5ObsidianIgnoresAccountAlias(t *testing.T) {
	registry, recorder := batch5Lane4TestRegistry(t, "obsidian_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"path": "Notes/meeting.md", "content": "ok"})
	}, WithAccountSource(aliasSource()))
	batch5Lane4Run(t, registry, "obsidian", map[string]any{
		"action": "obsidian_read_note", "path": "Notes/meeting.md", "account": "work",
	})
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want the module token", got)
	}
}

// Parity-exempt: upstream google_sheets_tool treats `account` as a framework
// tracking parameter and its _get_client() ignores it.
func TestBatch5Lane3GoogleSheetsIgnoresAccountAlias(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"range": "Sheet1!A1:B2", "values": []any{}})
	}, WithAccountSource(aliasSource()))
	batch4Execute(t, registry, "google_sheets", map[string]any{
		"action": "google_sheets_get_values", "spreadsheet_id": "sheet-1",
		"range_name": "Sheet1!A1:B2", "account": "work",
	})
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q, want the module token", got)
	}
}

// batch7Lane8Execute runs one lane-8 action through the registry.
func batch7Lane8Execute(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %T, want map", tool, result)
	}
	return object
}

// Parity-exempt: upstream gmail_tool resolves via get_by_alias("google",
// account) but has no Python test for it.
func TestBatch5Lane3GmailAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"labels": []any{}})
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "gmail", map[string]any{
		"action": "gmail_list_labels", "account": "work",
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer google-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: upstream calendar_tool resolves via get_by_alias("google",
// account) but has no Python test for it.
func TestBatch5Lane3CalendarAccountAliasSelectsToken(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_list_calendars", "account": "work",
	})
	if _, failed := result["error"]; failed {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer google-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}

// Parity-exempt: no upstream test for the gmail/calendar alias miss path.
func TestBatch5Lane3GoogleModulesUnresolvedAccountAliasReportsAuthError(t *testing.T) {
	gmailRegistry, gmailRecorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		t.Fatalf("request must not be sent for an unknown alias")
	}, WithAccountSource(aliasSource()))
	result := batch4Execute(t, gmailRegistry, "gmail", map[string]any{
		"action": "gmail_list_labels", "account": "missing",
	})
	if result["error"] != "Gmail credentials not configured" {
		t.Fatalf("gmail = %v", result)
	}
	if len(gmailRecorder.requests) != 0 {
		t.Fatalf("gmail requests = %d, want 0", len(gmailRecorder.requests))
	}

	calRegistry, calRecorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		t.Fatalf("request must not be sent for an unknown alias")
	}, WithAccountSource(aliasSource()))
	result = batch4Execute(t, calRegistry, "calendar", map[string]any{
		"action": "calendar_list_calendars", "account": "missing",
	})
	if result["error"] != "Calendar credentials not configured" {
		t.Fatalf("calendar = %v", result)
	}
	if len(calRecorder.requests) != 0 {
		t.Fatalf("calendar requests = %d, want 0", len(calRecorder.requests))
	}
}

// TestInstalledRegistryReadsTargetAccountSource pins the session wiring: a batch
// installed into a shared registry resolves aliases through the view attached to
// that registry (tools.ToolRegistry.SetAccountSource), not through the batch's
// own runtime. This is the path internal/cli uses in production.
// Parity-exempt: Go-only wiring guard; upstream has no shared-registry seam.
func TestInstalledRegistryReadsTargetAccountSource(t *testing.T) {
	batch, recorder := batch5Lane4TestRegistry(t, "hubspot_tool", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		batch5Lane4JSON(w, http.StatusOK, map[string]any{"total": 0, "results": []any{}})
	})
	target := tools.NewToolRegistry()
	target.SetAccountSource(aliasSource())
	batch.Install(target)

	result, err := target.GetExecutor().Execute(context.Background(), tools.ToolUse{
		Name: "hubspot",
		Input: map[string]any{
			"action": "hubspot_search_contacts", "query": "jane", "account": "work",
		},
	})
	if err != nil {
		t.Fatalf("hubspot call: %v", err)
	}
	if result.IsError {
		t.Fatalf("hubspot call failed: %s", result.Content)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer hubspot-work-token" {
		t.Fatalf("Authorization = %q", got)
	}
}
