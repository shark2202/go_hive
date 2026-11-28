package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// Parity: framework/agent_loop/internals/credential_tool.py::render_help
func TestCredentialsHelpMatchesPythonContract(t *testing.T) {
	help := CredentialsHelp()
	for _, want := range []string{
		"`credentials` — collect or pin credentials for this session.",
		"- help",
		"- collect  credential_id  [account]  [title]  [fields]  [instructions]",
		"- attach  credential_id  [account]",
		"- detach  credential_id  [account]",
		"`hive-cred` CLI",
		"credentials({\"action\": \"attach\", \"credential_id\": \"stripe\", \"account\": \"work\"})",
	} {
		if !strings.Contains(help, want) {
			t.Fatalf("help is missing %q", want)
		}
	}
	if !strings.HasSuffix(help, "\n") {
		t.Fatal("help text lost its trailing newline")
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::add_attachment
func TestCredentialsAttachDetachRoundTrip(t *testing.T) {
	sessionDir := t.TempDir()

	content, isError := AddCredentialAttachment(sessionDir, "stripe", "work")
	if isError || content != "Attached 'stripe' (account 'work') to this session. It will be reminded in your context each turn." {
		t.Fatalf("attach = (%q, %v)", content, isError)
	}
	payload, err := os.ReadFile(CredentialAttachmentsPath(sessionDir))
	if err != nil {
		t.Fatalf("read attachments: %v", err)
	}
	want := "[\n  {\n    \"credential_id\": \"stripe\",\n    \"account\": \"work\"\n  }\n]"
	if string(payload) != want {
		t.Fatalf("attachments = %q, want %q", payload, want)
	}

	content, isError = AddCredentialAttachment(sessionDir, "stripe", "work")
	if isError || content != "Credential 'stripe' (account 'work') is already attached to this session." {
		t.Fatalf("duplicate attach = (%q, %v)", content, isError)
	}

	// The account alias defaults to "default" and a second account is a
	// distinct pin, exactly like upstream's (credential_id, account) refs.
	if content, isError = AddCredentialAttachment(sessionDir, "stripe", ""); isError {
		t.Fatalf("default-account attach = (%q, %v)", content, isError)
	}
	if refs := ReadCredentialAttachments(sessionDir); len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}

	content, isError = RemoveCredentialAttachment(sessionDir, "stripe", "work")
	if isError || content != "Detached 'stripe' (account 'work') from this session." {
		t.Fatalf("detach = (%q, %v)", content, isError)
	}
	refs := ReadCredentialAttachments(sessionDir)
	if len(refs) != 1 || refs[0].CredentialID != "stripe" || refs[0].Account != "default" {
		t.Fatalf("refs after detach = %#v", refs)
	}

	content, isError = RemoveCredentialAttachment(sessionDir, "stripe", "work")
	if isError || content != "Credential 'stripe' (account 'work') was not attached." {
		t.Fatalf("second detach = (%q, %v)", content, isError)
	}
}

// Parity-exempt: the empty-id / no-session error strings are upstream messages; no Python test asserts them.
func TestCredentialsAttachDetachErrors(t *testing.T) {
	if content, isError := AddCredentialAttachment(t.TempDir(), "  ", ""); !isError || content != "ERROR: attach requires credential_id." {
		t.Fatalf("attach without id = (%q, %v)", content, isError)
	}
	if content, isError := AddCredentialAttachment("", "stripe", ""); !isError || content != "ERROR: no active session to attach to." {
		t.Fatalf("attach without session = (%q, %v)", content, isError)
	}
	if content, isError := RemoveCredentialAttachment(t.TempDir(), "", ""); !isError || content != "ERROR: detach requires credential_id." {
		t.Fatalf("detach without id = (%q, %v)", content, isError)
	}
	if content, isError := RemoveCredentialAttachment("", "stripe", ""); !isError || content != "ERROR: no active session." {
		t.Fatalf("detach without session = (%q, %v)", content, isError)
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::retired_action_message
func TestCredentialsUnknownAndRetiredActions(t *testing.T) {
	content, isError := ExecuteCredentialsTool(t.TempDir(), map[string]any{"action": "pin"})
	if !isError || content != "ERROR: unknown credentials action 'pin'. Call credentials() with no arguments for usage." {
		t.Fatalf("unknown action = (%q, %v)", content, isError)
	}
	content, isError = ExecuteCredentialsTool(t.TempDir(), map[string]any{
		"action": "inspect", "credential_id": "stripe", "account": "work",
	})
	want := "ERROR: `credentials` action 'inspect' was retired. Run this in the terminal instead: `hive-cred inspect stripe --account work --json`."
	if !isError || content != want {
		t.Fatalf("retired inspect = (%q, %v)", content, isError)
	}
	content, isError = ExecuteCredentialsTool(t.TempDir(), map[string]any{"action": "reveal", "credential_id": "stripe"})
	want = "ERROR: `credentials` action 'reveal' was retired. Run this in the terminal instead: `hive-cred token stripe`. It returns a fresh, refreshed value each call; never paste the output into a script — call it at request time."
	if !isError || content != want {
		t.Fatalf("retired reveal = (%q, %v)", content, isError)
	}
	if content, isError := ExecuteCredentialsTool(t.TempDir(), nil); isError || content != CredentialsHelp() {
		t.Fatalf("no action = (%q, %v)", content, isError)
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
func TestCredentialsValidateCollectBuildsNoSecretForm(t *testing.T) {
	payload, problem := ValidateCredentialsCollect(map[string]any{
		"credential_id": "stripe",
		"fields": []any{
			map[string]any{"name": "api_key", "label": "Secret key"},
			map[string]any{"name": "api_key"},                     // duplicate collapses
			map[string]any{"name": "webhook_signing_secret"},      // hint-derived secret
			map[string]any{"name": "account_id", "secret": false}, // explicit wins
		},
	})
	if problem != "" {
		t.Fatalf("validate: %s", problem)
	}
	if payload["credential_id"] != "stripe" || payload["account"] != "default" || payload["title"] != "Connect stripe" {
		t.Fatalf("payload = %#v", payload)
	}
	fields := payload["fields"].([]map[string]any)
	if len(fields) != 3 {
		t.Fatalf("fields = %#v", fields)
	}
	if fields[0]["name"] != "api_key" || fields[0]["label"] != "Secret key" || fields[0]["secret"] != true || fields[0]["required"] != true || fields[0]["placeholder"] != "" {
		t.Fatalf("field 0 = %#v", fields[0])
	}
	if fields[2]["secret"] != false {
		t.Fatalf("explicit secret=false lost: %#v", fields[2])
	}

	// A known spec supplies its own key and description when fields is absent.
	payload, problem = ValidateCredentialsCollect(map[string]any{"credential_id": "airtable_pat"})
	if problem != "" {
		t.Fatalf("spec default validate: %s", problem)
	}
	defaultField := payload["fields"].([]map[string]any)[0]
	if defaultField["name"] != "api_key" || defaultField["label"] != "Airtable Personal Access Token" {
		t.Fatalf("spec default field = %#v", defaultField)
	}

	for _, testCase := range []struct {
		input map[string]any
		want  string
	}{
		{map[string]any{"credential_id": "bad id"}, "collect requires a valid credential_id (e.g. 'stripe')."},
		{map[string]any{"credential_id": "stripe", "account": "bad/alias"}, "account alias must not contain spaces or slashes."},
		{map[string]any{"credential_id": "stripe", "fields": "not json"}, "fields must be a list of field objects."},
		{map[string]any{"credential_id": "stripe", "fields": []any{}}, "fields must be a non-empty list of field objects."},
		{map[string]any{"credential_id": "stripe", "fields": []any{map[string]any{"name": "bad name"}}}, "invalid field name: 'bad name'"},
	} {
		if _, got := ValidateCredentialsCollect(testCase.input); got != testCase.want {
			t.Fatalf("problem = %q, want %q", got, testCase.want)
		}
	}

	tooMany := make([]any, 0, credentialMaxFields+1)
	for index := 0; index <= credentialMaxFields; index++ {
		tooMany = append(tooMany, map[string]any{"name": "field_" + string(rune('a'+index))})
	}
	if _, got := ValidateCredentialsCollect(map[string]any{"credential_id": "stripe", "fields": tooMany}); got != "too many fields (max 8)." {
		t.Fatalf("too many fields problem = %q", got)
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::add_attachment
//
// Pinning is a reference write: upstream never resolves, decrypts or refreshes
// the credential, so an unknown id and an expired OAuth token both pin the same
// way a live credential does. This is also the security property that keeps a
// pinned secret out of the prompt (only metadata is re-injected later).
func TestCredentialsAttachUnknownOrExpiredCredentialPinsByRef(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC()
	expired := credentials.NewCredentialObject("oauth_expired")
	expired.CredentialType = credentials.CredentialTypeOAuth2
	expired.Keys.Set(&credentials.CredentialKey{Name: "access_token", Value: "expired-token", ExpiresAt: &past})
	expired.Keys.Set(credentials.NewCredentialKey("refresh_token", "refresh-token"))
	store := credentials.NewCredentialStore(
		credentials.WithStorage(credentials.NewInMemoryStorage(map[string]*credentials.CredentialObject{"oauth_expired": expired})),
		credentials.WithAutoRefresh(false),
	)

	sessionDir := t.TempDir()
	for _, credentialID := range []string{"does_not_exist", "oauth_expired"} {
		content, isError := AddCredentialAttachment(sessionDir, credentialID, "")
		if isError || !strings.Contains(content, "Attached '"+credentialID+"' (account 'default')") {
			t.Fatalf("attach %s = (%q, %v)", credentialID, content, isError)
		}
	}
	refs := ReadCredentialAttachments(sessionDir)
	if len(refs) != 2 {
		t.Fatalf("refs = %#v", refs)
	}

	// The pin did not touch the store: the expired token is still there, and
	// resolving it still reports the expiry the store owns.
	stored, err := store.GetCredential("oauth_expired", false)
	if err != nil || stored == nil {
		t.Fatalf("store lookup after pin = (%v, %v)", stored, err)
	}
	if key := stored.Keys.Get("access_token"); key == nil || !key.IsExpired() {
		t.Fatalf("expired key changed: %#v", key)
	}
}

// Parity: framework/agent_loop/internals/credential_tool.py::attachment_matches
func TestCredentialAttachmentMatchesProviderOrCredentialID(t *testing.T) {
	refs := []CredentialAttachment{{CredentialID: "google", Account: "work"}, {CredentialID: "stripe", Account: ""}}
	if !CredentialAttachmentMatches(map[string]any{"provider": "google", "alias": "work", "credential_id": "google_oauth"}, refs) {
		t.Fatal("provider/alias match failed")
	}
	if !CredentialAttachmentMatches(map[string]any{"provider": "stripe", "alias": "default"}, refs) {
		t.Fatal("default-account match failed")
	}
	if CredentialAttachmentMatches(map[string]any{"provider": "google", "alias": "default"}, refs) {
		t.Fatal("wrong alias matched")
	}
	if CredentialAttachmentMatches(map[string]any{"provider": "notion", "alias": "work"}, refs) {
		t.Fatal("unrelated provider matched")
	}
}

// Parity-exempt: Go path-safety guard for upstream's attachments_path();
// Python builds the global path without validating the session id.
func TestCredentialAttachmentsDirRejectsTraversal(t *testing.T) {
	home := t.TempDir()
	for _, sessionID := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "  "} {
		if got := CredentialAttachmentsDir(home, sessionID); got != "" {
			t.Fatalf("CredentialAttachmentsDir(%q) = %q, want empty", sessionID, got)
		}
	}
	want := filepath.Join(home, "sessions", "session_20260101_000000_abcd1234")
	if got := CredentialAttachmentsDir(home, "session_20260101_000000_abcd1234"); got != want {
		t.Fatalf("CredentialAttachmentsDir = %q, want %q", got, want)
	}
}

// Parity-exempt: Go-only JSON-shape guard; upstream's dict/list coercion is implicit.
func TestCredentialsAttachmentFileRoundTripsAsJSON(t *testing.T) {
	sessionDir := t.TempDir()
	if _, isError := AddCredentialAttachment(sessionDir, "stripe", "work"); isError {
		t.Fatal("attach failed")
	}
	data, err := os.ReadFile(CredentialAttachmentsPath(sessionDir))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != 1 || decoded[0]["credential_id"] != "stripe" || decoded[0]["account"] != "work" {
		t.Fatalf("decoded = %#v", decoded)
	}
}
