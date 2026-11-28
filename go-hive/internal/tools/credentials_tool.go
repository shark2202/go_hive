package tools

// The single CLI-style `credentials` model tool: help / collect / attach /
// detach. Python surfaces it as one tool with an `action` discriminator so the
// model sees a tiny schema; the heavy lifting lives in pure helpers here so the
// chat runtime's dispatch stays lean.
//
// The Go flat surface already advertises this tool name and schema from the
// frozen Python contract (internal/cli/tool_surface_flat.json); this file is
// the execution half that was missing, plus the session-attachment store the
// `attach`/`detach` actions write.
//
// Security model (unchanged from upstream):
//   - No action returns a secret value. `collect` only produces a no-secret
//     form spec; the frontend posts values straight to the encrypted store.
//   - `attach`/`detach` pin a {credential_id, account} reference. They never
//     resolve, decrypt or refresh the credential, so an unknown or expired
//     credential pins by reference exactly like a live one.
//
// Parity: framework/agent_loop/internals/credential_tool.py

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
)

// CredentialsToolName is the model-visible tool name (upstream's
// `build_credentials_tool`).
const CredentialsToolName = "credentials"

// credentialAttachmentFile is the per-session file holding pinned refs,
// upstream's `sessions/<session_id>/attached_credentials.json`. The Go session
// root already is the session's directory, so the file lands beside state.json.
const credentialAttachmentFile = "attached_credentials.json"

const credentialMaxFields = 8

//go:embed credentials_help.txt
var credentialsHelpText string

// credentialSecretNameHints decides a field's masked-input default when the
// agent does not say. Mirrors `_SECRET_NAME_HINTS`.
var credentialSecretNameHints = []string{
	"api_key", "apikey", "token", "access_token", "secret", "password", "key",
}

// CredentialRetiredActions maps an action that used to live in the tool to the
// terminal command that replaced it, so a stale call still teaches the agent
// the new surface instead of returning a value.
var CredentialRetiredActions = map[string]struct{}{
	"browse":  {},
	"inspect": {},
	"reveal":  {},
}

// CredentialAttachment is one pinned credential reference. Field order matches
// upstream's dict insertion order so the on-disk JSON is byte-comparable.
type CredentialAttachment struct {
	CredentialID string `json:"credential_id"`
	Account      string `json:"account"`
}

// CredentialsHelp returns the fresh usage text for action=help / no action.
func CredentialsHelp() string { return credentialsHelpText }

// CredentialsAction resolves the input's action the way the loop does: absent
// or blank means "help".
func CredentialsAction(input map[string]any) string {
	action := strings.ToLower(strings.TrimSpace(credentialText(input["action"])))
	if action == "" {
		return "help"
	}
	return action
}

// RetiredCredentialActionMessage is the reply to a retired action: the exact
// CLI call to make instead.
//
// Parity: framework/agent_loop/internals/credential_tool.py::retired_action_message
func RetiredCredentialActionMessage(action, credentialID, account, key string) string {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		credentialID = "<credential_id>"
	}
	accountSuffix := ""
	if strings.TrimSpace(account) != "" {
		accountSuffix = " --account " + strings.TrimSpace(account)
	}
	var cmd string
	switch action {
	case "browse":
		cmd = "hive-cred browse --json"
	case "inspect":
		cmd = "hive-cred inspect " + credentialID + accountSuffix + " --json"
	case "reveal":
		cmd = "hive-cred token " + credentialID + accountSuffix
		if strings.TrimSpace(key) != "" {
			cmd += " --key " + strings.TrimSpace(key)
		}
	default:
		cmd = "hive-cred browse --json"
	}
	why := ""
	if action == "reveal" {
		why = " It returns a fresh, refreshed value each call; never paste the output into a script — call it at request time."
	}
	return fmt.Sprintf("ERROR: `credentials` action '%s' was retired. Run this in the terminal instead: `%s`.%s", action, cmd, why)
}

// CredentialAttachmentsPath is the per-session pinned-refs file inside a
// session directory.
func CredentialAttachmentsPath(sessionDir string) string {
	return filepath.Join(sessionDir, credentialAttachmentFile)
}

// CredentialAttachmentsDir returns upstream's per-session attachment
// directory, `$HIVE_HOME/sessions/<session_id>`.
//
// Upstream's `attachments_path()` hardcodes that root even when the chat tree
// lives under `queens/<queen>/sessions/<sid>` or a colony session directory.
// The global root is therefore the only location a Python runtime reads, and
// the Go writer has to put the file there for a runtime switch to see the
// pins.
func CredentialAttachmentsDir(hiveHome, sessionID string) string {
	hiveHome = strings.TrimSpace(hiveHome)
	sessionID = strings.TrimSpace(sessionID)
	if hiveHome == "" || sessionID == "" || sessionID == "." || sessionID == ".." ||
		strings.ContainsAny(sessionID, "/\\\x00") {
		return ""
	}
	return filepath.Join(hiveHome, "sessions", sessionID)
}

// readCredentialAttachmentsFile returns the pinned refs in one directory
// (best effort): a missing, unreadable or malformed file reads as no pins, and
// refs without a credential_id are dropped, exactly like upstream
// `read_attachments`.
func readCredentialAttachmentsFile(sessionDir string) []CredentialAttachment {
	if strings.TrimSpace(sessionDir) == "" {
		return nil
	}
	data, err := os.ReadFile(CredentialAttachmentsPath(sessionDir))
	if err != nil {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	refs := make([]CredentialAttachment, 0, len(raw))
	for _, item := range raw {
		// Upstream filters a list of dicts at read time, so one malformed
		// element (a string, a null, an object without credential_id) must not
		// discard the valid refs beside it.
		var ref CredentialAttachment
		if err := json.Unmarshal(item, &ref); err != nil {
			continue
		}
		if strings.TrimSpace(ref.CredentialID) == "" {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// ReadCredentialAttachments returns the pins in one session directory. It is
// the pre-interop entry point; callers that know HIVE_HOME and the session id
// should use ReadCredentialAttachmentsForSession.
func ReadCredentialAttachments(sessionDir string) []CredentialAttachment {
	return readCredentialAttachmentsFile(sessionDir)
}

// ReadCredentialAttachmentsForSession returns the session's pinned refs,
// preferring upstream's global file and falling back to a session-local file
// written by an older Go build. The two locations are merged (global first),
// so an upgrade does not lose pins.
func ReadCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir string) []CredentialAttachment {
	dirs := make([]string, 0, 2)
	if dir := CredentialAttachmentsDir(hiveHome, sessionID); dir != "" {
		dirs = append(dirs, dir)
	}
	if legacy := strings.TrimSpace(sessionDir); legacy != "" {
		dirs = append(dirs, legacy)
	}
	merged := make([]CredentialAttachment, 0, 2)
	seen := map[string]struct{}{}
	for _, dir := range dirs {
		for _, ref := range readCredentialAttachmentsFile(dir) {
			key := ref.CredentialID + "\x00" + credentialAccount(ref.Account)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, ref)
		}
	}
	return merged
}

// writeCredentialAttachments persists the refs with upstream's `indent=2`
// shape. The file holds references only — never a secret — so the mode matches
// upstream's default 0644.
func writeCredentialAttachments(sessionDir string, refs []CredentialAttachment) error {
	if strings.TrimSpace(sessionDir) == "" {
		return fmt.Errorf("no active session")
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(CredentialAttachmentsPath(sessionDir), payload, 0o644)
}

// writeCredentialAttachmentsForSession writes to upstream's global directory
// when it is resolvable, else to the session directory. After a successful
// global write the session-local file from an older Go build is removed: the
// merged refs are now in the global file, so keeping it would resurrect a
// detached pin on the next read.
func writeCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir string, refs []CredentialAttachment) error {
	target := strings.TrimSpace(sessionDir)
	if dir := CredentialAttachmentsDir(hiveHome, sessionID); dir != "" {
		target = dir
	}
	if err := writeCredentialAttachments(target, refs); err != nil {
		return err
	}
	legacy := strings.TrimSpace(sessionDir)
	if legacy != "" && filepath.Clean(legacy) != filepath.Clean(target) {
		_ = os.Remove(CredentialAttachmentsPath(legacy))
	}
	return nil
}

// syncLegacyCredentialAttachments moves refs from the older session-local file
// into upstream's global file. It is best effort and only acts when the global
// directory resolves; a read-only home must not turn a duplicate attach into an
// error.
func syncLegacyCredentialAttachments(hiveHome, sessionID, sessionDir string, refs []CredentialAttachment) {
	if CredentialAttachmentsDir(hiveHome, sessionID) == "" {
		return
	}
	legacy := strings.TrimSpace(sessionDir)
	if legacy == "" {
		return
	}
	if _, err := os.Stat(CredentialAttachmentsPath(legacy)); err != nil {
		return
	}
	_ = writeCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir, refs)
}

// AddCredentialAttachment pins a credential to a session directory.
func AddCredentialAttachment(sessionDir, credentialID, account string) (string, bool) {
	return AddCredentialAttachmentForSession("", "", sessionDir, credentialID, account)
}

// AddCredentialAttachmentForSession pins a credential to the session and
// returns the user-facing message plus whether it is an error (the "ERROR: "
// prefix is the upstream error signal).
//
// Parity: framework/agent_loop/internals/credential_tool.py::add_attachment
func AddCredentialAttachmentForSession(hiveHome, sessionID, sessionDir, credentialID, account string) (string, bool) {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return "ERROR: attach requires credential_id.", true
	}
	if strings.TrimSpace(sessionDir) == "" && CredentialAttachmentsDir(hiveHome, sessionID) == "" {
		return "ERROR: no active session to attach to.", true
	}
	account = credentialAccount(account)
	refs := ReadCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir)
	for _, ref := range refs {
		if ref.CredentialID == credentialID && credentialAccount(ref.Account) == account {
			// A pin found only in the older session-local file still needs the
			// upgrade move, or the Python runtime would not see it.
			syncLegacyCredentialAttachments(hiveHome, sessionID, sessionDir, refs)
			return fmt.Sprintf("Credential '%s' (account '%s') is already attached to this session.", credentialID, account), false
		}
	}
	refs = append(refs, CredentialAttachment{CredentialID: credentialID, Account: account})
	if err := writeCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir, refs); err != nil {
		return "ERROR: could not save the session attachment: " + err.Error(), true
	}
	return fmt.Sprintf("Attached '%s' (account '%s') to this session. It will be reminded in your context each turn.", credentialID, account), false
}

// RemoveCredentialAttachment unpins a credential from a session directory.
func RemoveCredentialAttachment(sessionDir, credentialID, account string) (string, bool) {
	return RemoveCredentialAttachmentForSession("", "", sessionDir, credentialID, account)
}

// RemoveCredentialAttachmentForSession unpins a credential from the session.
//
// Parity: framework/agent_loop/internals/credential_tool.py::remove_attachment
func RemoveCredentialAttachmentForSession(hiveHome, sessionID, sessionDir, credentialID, account string) (string, bool) {
	credentialID = strings.TrimSpace(credentialID)
	if credentialID == "" {
		return "ERROR: detach requires credential_id.", true
	}
	if strings.TrimSpace(sessionDir) == "" && CredentialAttachmentsDir(hiveHome, sessionID) == "" {
		return "ERROR: no active session.", true
	}
	account = credentialAccount(account)
	refs := ReadCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir)
	kept := make([]CredentialAttachment, 0, len(refs))
	for _, ref := range refs {
		if ref.CredentialID == credentialID && credentialAccount(ref.Account) == account {
			continue
		}
		kept = append(kept, ref)
	}
	if len(kept) == len(refs) {
		return fmt.Sprintf("Credential '%s' (account '%s') was not attached.", credentialID, account), false
	}
	if err := writeCredentialAttachmentsForSession(hiveHome, sessionID, sessionDir, kept); err != nil {
		return "ERROR: could not save the session attachment: " + err.Error(), true
	}
	return fmt.Sprintf("Detached '%s' (account '%s') from this session.", credentialID, account), false
}

// CredentialAttachmentMatches reports whether a connected-account dict is
// pinned by any ref. Either the provider or the credential id may name the
// pinned credential, and a ref without an account matches "default".
//
// Parity: framework/agent_loop/internals/credential_tool.py::attachment_matches
func CredentialAttachmentMatches(account map[string]any, refs []CredentialAttachment) bool {
	provider := credentials.AccountProvider(account)
	credentialID := strings.TrimSpace(credentialText(account["credential_id"]))
	if credentialID == "" {
		credentialID = provider
	}
	alias := strings.TrimSpace(credentialText(account["alias"]))
	if alias == "" {
		alias = "default"
	}
	for _, ref := range refs {
		if (ref.CredentialID == provider || ref.CredentialID == credentialID) && credentialAccount(ref.Account) == alias {
			return true
		}
	}
	return false
}

// ExecuteCredentialsTool runs the non-blocking actions (help / attach /
// detach / retired / unknown) against a session directory. `collect` is
// blocking — it parks the loop for the secure form — so the chat runtime
// handles it before delegating here; this fallback keeps a direct catalogue
// call honest instead of pretending the tool does not exist.
func ExecuteCredentialsTool(sessionDir string, input map[string]any) (string, bool) {
	return ExecuteCredentialsToolForSession("", "", sessionDir, input)
}

// ExecuteCredentialsToolForSession is ExecuteCredentialsTool with the
// Python-compatible attachment root: pins are read from and written to
// `$HIVE_HOME/sessions/<session_id>/attached_credentials.json` when both parts
// resolve, with the session directory kept as a read fallback for pins written
// by an older Go build.
func ExecuteCredentialsToolForSession(hiveHome, sessionID, sessionDir string, input map[string]any) (string, bool) {
	action := CredentialsAction(input)
	switch action {
	case "help":
		return CredentialsHelp(), false
	case "collect":
		return "ERROR: collect needs a live chat session to show the secure form; call it from the conversation.", true
	case "attach":
		return AddCredentialAttachmentForSession(hiveHome, sessionID, sessionDir, credentialText(input["credential_id"]), credentialText(input["account"]))
	case "detach":
		return RemoveCredentialAttachmentForSession(hiveHome, sessionID, sessionDir, credentialText(input["credential_id"]), credentialText(input["account"]))
	}
	if _, retired := CredentialRetiredActions[action]; retired {
		return RetiredCredentialActionMessage(action, credentialText(input["credential_id"]), credentialText(input["account"]), credentialText(input["key"])), true
	}
	return fmt.Sprintf("ERROR: unknown credentials action '%s'. Call credentials() with no arguments for usage.", action), true
}

// ValidateCredentialsCollect validates a collect call and builds the no-secret
// form payload. It returns (payload, "") on success or (nil, problem) on
// failure; the payload carries field specs only, never values.
//
// Parity: framework/agent_loop/internals/credential_tool.py::validate_collect_input
func ValidateCredentialsCollect(input map[string]any) (map[string]any, string) {
	credentialID := strings.TrimSpace(credentialText(input["credential_id"]))
	if credentialID == "" || strings.ContainsAny(credentialID, " /\\") {
		return nil, "collect requires a valid credential_id (e.g. 'stripe')."
	}
	account := strings.TrimSpace(credentialText(input["account"]))
	if account == "" {
		account = "default"
	}
	if strings.ContainsAny(account, " /\\") {
		return nil, "account alias must not contain spaces or slashes."
	}
	fields, problem := sanitizeCredentialFields(input["fields"], credentialID)
	if problem != "" {
		return nil, problem
	}
	title := strings.TrimSpace(credentialText(input["title"]))
	if title == "" {
		title = "Connect " + credentialID
	}
	payload := map[string]any{
		"credential_id": credentialID,
		"account":       account,
		"title":         title,
		"instructions":  credentialText(input["instructions"]),
		"fields":        fields,
	}
	return payload, ""
}

// sanitizeCredentialFields mirrors `_sanitize_fields`: default fields for a
// known spec, JSON-string tolerance, a ≤8 bound, per-field name validation,
// duplicate-name collapse, and the derived label/secret/required/placeholder
// shape.
func sanitizeCredentialFields(raw any, credentialID string) ([]map[string]any, string) {
	if raw == nil {
		raw = defaultCredentialFields(credentialID)
	}
	if text, ok := raw.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			return nil, "fields must be a list of field objects."
		}
		raw = decoded
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, "fields must be a non-empty list of field objects."
	}
	if len(list) > credentialMaxFields {
		return nil, fmt.Sprintf("too many fields (max %d).", credentialMaxFields)
	}

	fields := make([]map[string]any, 0, len(list))
	seen := map[string]struct{}{}
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, "each field must be an object with at least a 'name'."
		}
		name := strings.TrimSpace(credentialText(object["name"]))
		if name == "" || strings.ContainsAny(name, " /\\") {
			return nil, fmt.Sprintf("invalid field name: '%s'", name)
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		secret, given := object["secret"].(bool)
		if !given {
			secret = credentialSecretName(name)
		}
		required := true
		if value, present := object["required"].(bool); present {
			required = value
		}
		label := strings.TrimSpace(credentialText(object["label"]))
		if label == "" {
			label = credentialFieldTitle(name)
		}
		placeholder := ""
		if value, present := object["placeholder"]; present {
			placeholder = credentialText(value)
		}
		fields = append(fields, map[string]any{
			"name":        name,
			"label":       label,
			"secret":      secret,
			"required":    required,
			"placeholder": placeholder,
		})
	}
	if len(fields) == 0 {
		return nil, "no valid fields provided."
	}
	return fields, ""
}

// defaultCredentialFields derives form fields from a known spec, else a single
// secret api_key field.
//
// Parity: framework/agent_loop/internals/credential_tool.py::_default_fields_for
func defaultCredentialFields(credentialID string) []any {
	var spec *credentials.GeneratedCredentialSpec
	for index := range credentials.GeneratedCredentialSpecs {
		candidate := &credentials.GeneratedCredentialSpecs[index]
		if candidate.Name == credentialID || candidate.CredentialID == credentialID {
			spec = candidate
			break
		}
	}
	keyName := "api_key"
	label := "API key"
	if spec != nil {
		if strings.TrimSpace(spec.CredentialKey) != "" {
			keyName = strings.TrimSpace(spec.CredentialKey)
		}
		if strings.TrimSpace(spec.Description) != "" {
			label = strings.TrimSpace(spec.Description)
		}
	}
	return []any{map[string]any{"name": keyName, "label": label, "secret": true, "required": true}}
}

// credentialSecretName applies the secret-field name hints.
func credentialSecretName(name string) bool {
	lower := strings.ToLower(name)
	for _, hint := range credentialSecretNameHints {
		if strings.Contains(lower, hint) {
			return true
		}
	}
	return false
}

// credentialFieldTitle renders "api_key" as "Api Key" for a missing label.
func credentialFieldTitle(name string) string {
	words := strings.Split(strings.ReplaceAll(name, "_", " "), " ")
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}

// credentialAccount applies the "default" alias fallback.
func credentialAccount(account string) string {
	account = strings.TrimSpace(account)
	if account == "" {
		return "default"
	}
	return account
}

// credentialText projects a JSON value to the string Python's str() would use
// for the scalar inputs here.
func credentialText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}
