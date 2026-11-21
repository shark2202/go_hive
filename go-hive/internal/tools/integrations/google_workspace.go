// Batch 5, lane 3 of the integration port: the four Google Workspace modules
//
//	google_sheets_tool (10 actions)
//	google_docs_tool   (11 actions)
//	gmail_tool         (10 actions)
//	calendar_tool      ( 8 actions)
//
// = 39 actions total.
//
// Every module authenticates with a Google OAuth2 access token from
// GOOGLE_ACCESS_TOKEN (or WithCredential) and sends it as a Bearer token,
// exactly like the upstream Python clients. Requests go through Runtime.HTTP
// so every action is exercisable against an httptest server.
//
// Deliberate limits, matching the existing batches:
//   - The upstream `account` alias selector is accepted as a parameter, but the
//     Go runtime holds one token per module, so aliases resolve to that token.
//   - workspace_id/agent_id/session_id are framework tracking parameters
//     upstream, not model parameters, and are not part of the catalog.
//   - Google Docs uses two hosts upstream (docs.googleapis.com for documents,
//     www.googleapis.com/drive for comments/export); the drive calls are
//     configured through the google_docs_drive module entry and reuse the
//     google_docs_tool credential.
package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch5Lane3MaxBody = 8 << 20

	batch5Lane3SheetsModule = "google_sheets_tool"
	batch5Lane3DocsModule   = "google_docs_tool"
	batch5Lane3DriveModule  = "google_docs_drive"
	batch5Lane3GmailModule  = "gmail_tool"
	batch5Lane3CalModule    = "calendar_tool"

	batch5Lane3SheetsPrefix = "/v4/spreadsheets"
	batch5Lane3DocsPrefix   = "/v1/documents"
	batch5Lane3DrivePrefix  = "/drive/v3"
	batch5Lane3GmailPrefix  = "/gmail/v1/users/me"
	batch5Lane3CalPrefix    = "/calendar/v3"
)

// InstallBatch5Lane3 publishes the Google Workspace tools into a shared
// registry and returns the number of tools registered (4).
func InstallBatch5Lane3(target *tools.ToolRegistry) int {
	return newBatch5Lane3Registry().Install(target)
}

// newBatch5Lane3Registry builds a registry containing only the batch-5 lane-3
// tools, sharing the Runtime surface used by the rest of the port.
func newBatch5Lane3Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch5Lane3Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch5Lane3SheetsTool,
		batch5Lane3DocsTool,
		batch5Lane3GmailTool,
		batch5Lane3CalendarTool,
	} {
		registry.AddTool(tool)
	}
	impls := map[string]map[string]ActionFunc{
		"google_sheets": batch5Lane3SheetsImpls(),
		"google_docs":   batch5Lane3DocsImpls(),
		"gmail":         batch5Lane3GmailImpls(),
		"calendar":      batch5Lane3CalendarImpls(),
	}
	for tool, actions := range impls {
		for action, impl := range actions {
			registry.AddImpl(tool, action, impl)
		}
	}
	return registry
}

// batch5Lane3Modules declares the public endpoints and the credential variable
// shared by all four modules. Tests override BaseURL with WithBaseURL. The
// three retrievable modules also declare the standard OAuth2 refresh-token
// source; the sheet/docs drive entry reuses the docs credential.
func batch5Lane3Modules() map[string]*ModuleConfig {
	google := []string{"GOOGLE_ACCESS_TOKEN"}
	return map[string]*ModuleConfig{
		batch5Lane3SheetsModule: {ID: batch5Lane3SheetsModule, BaseURL: "https://sheets.googleapis.com", EnvVars: google, OAuth: batch5Lane3OAuth},
		batch5Lane3DocsModule:   {ID: batch5Lane3DocsModule, BaseURL: "https://docs.googleapis.com", EnvVars: google, OAuth: batch5Lane3OAuth},
		batch5Lane3DriveModule:  {ID: batch5Lane3DriveModule, BaseURL: "https://www.googleapis.com"},
		batch5Lane3GmailModule:  {ID: batch5Lane3GmailModule, BaseURL: "https://gmail.googleapis.com", EnvVars: google, OAuth: batch5Lane3OAuth},
		batch5Lane3CalModule:    {ID: batch5Lane3CalModule, BaseURL: "https://www.googleapis.com", EnvVars: google, OAuth: batch5Lane3OAuth},
	}
}

// ------------------------------------------------------------ shared bits ---

// batch5Lane3AccountToken resolves a Google module credential for the current
// call: an account alias the action selected wins over the module's single
// token (upstream `_get_token(account)` -> credentials.get_by_alias("google",
// account)). Google Sheets is the one module that keeps moduleToken:
// upstream treats its `account` parameter as a framework tracking value.
func batch5Lane3AccountToken(rt *Runtime, moduleID string) string {
	return accountToken(rt, moduleID, "google")
}

// batch5Lane3AccountCredential reports whether an alias-aware Google module
// has a usable credential: the selected account resolves, or the module has a
// token or an OAuth2 refresh-token source that mints one.
func batch5Lane3AccountCredential(rt *Runtime, moduleID string) bool {
	if token, handled := rt.ResolveAccountToken("google"); handled {
		return token != ""
	}
	return batch5Lane3Credential(rt, moduleID)
}

// batch5Lane3Credential reports whether a single-credential Google module has
// a token or an OAuth2 refresh-token source.
func batch5Lane3Credential(rt *Runtime, moduleID string) bool {
	if rt.OAuthRefreshable(moduleID) {
		return true
	}
	return moduleToken(rt, moduleID) != ""
}

// batch5Lane3ValueError carries an upstream-style value error through the
// (status, body, error) transport signature so callers return it unchanged.
type batch5Lane3ValueError struct{ dict map[string]any }

func (e *batch5Lane3ValueError) Error() string {
	return fmt.Sprint(e.dict["error"])
}

// batch5Lane3AccessToken resolves the token for one Google request, refreshing
// an expired OAuth2 access token first. refreshable reports whether a 401 can
// be retried with a freshly minted token.
func batch5Lane3AccessToken(ctx context.Context, rt *Runtime, moduleID string, useAlias bool) (string, bool, map[string]any) {
	if useAlias {
		if token, handled := rt.ResolveAccountToken("google"); handled {
			// An aliased store credential is a single token; the refresh
			// material (client id/secret, refresh token) lives with the
			// module's own credential, not the alias.
			return token, false, nil
		}
	}
	return oauthModuleToken(ctx, rt, moduleID)
}

// batch5Lane3Segments percent-encodes each path segment and joins them.
// Segments are escaped individually so an A1 range keeps its ":" separator but
// spaces, "!" and "#" are encoded the way httpx would encode them.
func batch5Lane3Segments(segments ...string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, url.PathEscape(segment))
	}
	return strings.Join(escaped, "/")
}

// batch5Lane3Do sends one authorized request and returns status and body.
// Transport failures are returned as errors for the caller's message mapping.
// An expired OAuth2 token is refreshed before the request; a 401 retries once
// with a freshly minted token.
func batch5Lane3Do(ctx context.Context, rt *Runtime, moduleID, method, path string, query map[string]string, body any) (int, []byte, error) {
	target := moduleBaseURL(rt, moduleID) + path
	if len(query) > 0 {
		values := url.Values{}
		for name, value := range query {
			values.Set(name, value)
		}
		target += "?" + values.Encode()
	}
	// Drive calls belong to the Docs tool but live on another host; they reuse
	// the Docs credential, like upstream's single client. Docs, Drive, Gmail and
	// Calendar honour the `account` alias; Sheets keeps its single token because
	// upstream's tool ignores the parameter.
	credentialModule := moduleID
	useAlias := true
	switch moduleID {
	case batch5Lane3DriveModule:
		credentialModule = batch5Lane3DocsModule
	case batch5Lane3SheetsModule:
		useAlias = false
	}
	token, _, problem := batch5Lane3AccessToken(ctx, rt, credentialModule, useAlias)
	if problem != nil {
		return 0, nil, &batch5Lane3ValueError{dict: problem}
	}
	build := func(token string) (*http.Request, error) {
		var reader io.Reader
		if body != nil {
			encoded, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			reader = bytes.NewReader(encoded)
		}
		req, err := http.NewRequestWithContext(ctx, method, target, reader)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}
	req, err := build(token)
	if err != nil {
		return 0, nil, err
	}
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return 0, nil, err
	}
	if token != "" && resp.StatusCode == http.StatusUnauthorized {
		resolution := rt.ResolveUnauthorized(ctx, credentialModule, "Google")
		switch {
		case resolution.Retry:
			resp.Body.Close()
			req, err = build(resolution.Token)
			if err != nil {
				return 0, nil, err
			}
			resp, err = runtimeHTTP(rt).Do(req)
			if err != nil {
				return 0, nil, err
			}
		case resolution.Failure != nil:
			resp.Body.Close()
			return 0, nil, &batch5Lane3ValueError{dict: resolution.Failure}
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch5Lane3MaxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

// batch5Lane3TransportError maps a transport failure onto the upstream
// timeout/network error dicts.
func batch5Lane3TransportError(ctx context.Context, err error) map[string]any {
	var valueErr *batch5Lane3ValueError
	if errors.As(err, &valueErr) {
		return valueErr.dict
	}
	if isTimeout(ctx, err) {
		return map[string]any{"error": "Request timed out"}
	}
	message := err.Error()
	if strings.Contains(message, "Bearer") || strings.Contains(message, "Authorization") {
		return map[string]any{"error": "Network error: Request failed (details redacted for security)"}
	}
	if runes := []rune(message); len(runes) > 200 {
		message = string(runes[:200]) + "..."
	}
	return map[string]any{"error": "Network error: " + message}
}

// batch5Lane3ErrorDetail mirrors response.json().get("error", {}).get("message",
// response.text), falling back to the raw body when the shape differs.
func batch5Lane3ErrorDetail(raw []byte) string {
	if payload, ok := jsonObject(raw); ok {
		if errorObject, ok := payload["error"].(map[string]any); ok {
			if message, ok := errorObject["message"].(string); ok {
				return message
			}
		}
	}
	return string(raw)
}

// batch5Lane3PyType names a decoded value the way Python's type().__name__ would.
func batch5Lane3PyType(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case bool:
		return "bool"
	case int, int64:
		return "int"
	case float64:
		return "float"
	case json.Number:
		return "int"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// batch5Lane3OptionalFloat distinguishes an absent/null argument from 0.0.
func batch5Lane3OptionalFloat(input map[string]any, name string) (float64, bool) {
	value, present := input[name]
	if !present || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// batch5Lane3RequestID builds the 12-hex-character Meet request id upstream
// generates with uuid4().hex[:12].
func batch5Lane3RequestID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("%012x", time.Now().UnixNano()&0xffffffffffff)
}

// batch5Lane3PyISO renders a timestamp the way Python's datetime.isoformat()
// does: "+00:00" for UTC and microseconds only when non-zero.
func batch5Lane3PyISO(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if micros := value.Nanosecond() / 1000; micros != 0 {
		base += fmt.Sprintf(".%06d", micros)
	}
	if _, offset := value.Zone(); offset == 0 {
		return base + "+00:00"
	}
	return base + value.Format("-07:00")
}

// --------------------------------------------------------------- catalog ----

var batch5Lane3SheetsTool = &Tool{
	ID:          batch5Lane3SheetsModule,
	Name:        "google_sheets",
	DisplayName: "Google Sheets",
	Summary:     "Google Sheets integration (10 actions).",
	Actions: []Action{
		{
			Name:     "google_sheets_get_spreadsheet",
			Summary:  "Get Google Sheets spreadsheet metadata.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id"},
			Params: map[string]any{
				"spreadsheet_id":    param("string"),
				"include_grid_data": paramDefault("boolean", false),
				"account":           paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_create_spreadsheet",
			Summary:  "Create a new Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":        param("string"),
				"sheet_titles": param("array"),
				"account":      paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_get_values",
			Summary:  "Get values from a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name"},
			Params: map[string]any{
				"spreadsheet_id":      param("string"),
				"range_name":          param("string"),
				"value_render_option": paramDefault("string", "FORMATTED_VALUE"),
				"account":             paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_update_values",
			Summary:  "Update values in a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name", "values"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"range_name":         param("string"),
				"values":             param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_append_values",
			Summary:  "Append values to a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name", "values"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"range_name":         param("string"),
				"values":             param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_clear_values",
			Summary:  "Clear values in a Google Sheets range.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "range_name"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"range_name":     param("string"),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_batch_update_values",
			Summary:  "Batch update multiple ranges in a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "data"},
			Params: map[string]any{
				"spreadsheet_id":     param("string"),
				"data":               param("array"),
				"value_input_option": paramDefault("string", "USER_ENTERED"),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_batch_clear_values",
			Summary:  "Batch clear multiple ranges in a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "ranges"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"ranges":         param("array"),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_add_sheet",
			Summary:  "Add a new sheet/tab to a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "title"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"title":          param("string"),
				"row_count":      paramDefault("integer", 1000),
				"column_count":   paramDefault("integer", 26),
				"account":        paramDefault("string", ""),
			},
		},
		{
			Name:     "google_sheets_delete_sheet",
			Summary:  "Delete a sheet/tab from a Google Sheets spreadsheet.",
			Source:   "google_sheets_tool/google_sheets_tool.py",
			Required: []string{"spreadsheet_id", "sheet_id"},
			Params: map[string]any{
				"spreadsheet_id": param("string"),
				"sheet_id":       param("integer"),
				"account":        paramDefault("string", ""),
			},
		},
	},
}

var batch5Lane3DocsTool = &Tool{
	ID:          batch5Lane3DocsModule,
	Name:        "google_docs",
	DisplayName: "Google Docs",
	Summary:     "Google Docs integration (11 actions).",
	Actions: []Action{
		{
			Name:     "google_docs_create_document",
			Summary:  "Create a new blank Google Docs document with a specified title.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":   param("string"),
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_get_document",
			Summary:  "Retrieve the full structural content, metadata, and elements of a document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_insert_text",
			Summary:  "Insert text at a specific index or at the end of the document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "text"},
			Params: map[string]any{
				"document_id": param("string"),
				"text":        param("string"),
				"index":       param("integer"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_replace_all_text",
			Summary:  "Global find-and-replace (ideal for populating templates with dynamic data).",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "find_text", "replace_text"},
			Params: map[string]any{
				"document_id":  param("string"),
				"find_text":    param("string"),
				"replace_text": param("string"),
				"match_case":   paramDefault("boolean", true),
				"account":      paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_insert_image",
			Summary:  "Insert an image into the document body via URI.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "image_uri", "index"},
			Params: map[string]any{
				"document_id": param("string"),
				"image_uri":   param("string"),
				"index":       param("integer"),
				"width_pt":    param("number"),
				"height_pt":   param("number"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_format_text",
			Summary:  "Apply styling (bold, italic, font size, colors) to specific text ranges.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "start_index", "end_index"},
			Params: map[string]any{
				"document_id":            param("string"),
				"start_index":            param("integer"),
				"end_index":              param("integer"),
				"bold":                   param("boolean"),
				"italic":                 param("boolean"),
				"underline":              param("boolean"),
				"font_size_pt":           param("number"),
				"foreground_color_red":   param("number"),
				"foreground_color_green": param("number"),
				"foreground_color_blue":  param("number"),
				"account":                paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_batch_update",
			Summary:  "Execute multiple requests (inserts, deletes, formatting) in a single atomic operation.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "requests_json"},
			Params: map[string]any{
				"document_id":   param("string"),
				"requests_json": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_create_list",
			Summary:  "Create or modify bulleted and numbered lists within the document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "start_index", "end_index"},
			Params: map[string]any{
				"document_id": param("string"),
				"start_index": param("integer"),
				"end_index":   param("integer"),
				"list_type":   paramDefault("string", "bullet"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_add_comment",
			Summary:  "Create a comment or anchor a discussion thread to a specific text segment.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "content"},
			Params: map[string]any{
				"document_id": param("string"),
				"content":     param("string"),
				"quoted_text": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_list_comments",
			Summary:  "Retrieve comments for a document, with pagination support.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id":     param("string"),
				"page_size":       paramDefault("integer", 20),
				"page_token":      param("string"),
				"include_deleted": paramDefault("boolean", false),
				"account":         paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_export_content",
			Summary:  "Export the document to different formats (PDF, DOCX, TXT).",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id": param("string"),
				"format":      paramDefault("string", "pdf"),
				"account":     paramDefault("string", ""),
			},
		},
	},
}

var batch5Lane3GmailTool = &Tool{
	ID:          batch5Lane3GmailModule,
	Name:        "gmail",
	DisplayName: "Gmail",
	Summary:     "Gmail integration (10 actions).",
	Actions: []Action{
		{
			Name:    "gmail_list_messages",
			Summary: "List Gmail messages matching a search query.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"query":       paramDefault("string", "is:unread"),
				"max_results": paramDefault("integer", 100),
				"page_token":  param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_get_message",
			Summary:  "Get a Gmail message by ID.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
				"format":     paramDefault("string", "metadata"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_trash_message",
			Summary:  "Move a Gmail message to trash.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_modify_message",
			Summary:  "Modify labels on a Gmail message.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id":    param("string"),
				"add_labels":    param("string"),
				"remove_labels": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_batch_modify_messages",
			Summary:  "Modify labels on multiple Gmail messages at once.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_ids"},
			Params: map[string]any{
				"message_ids":   param("string"),
				"add_labels":    param("string"),
				"remove_labels": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_batch_get_messages",
			Summary:  "Fetch multiple Gmail messages by ID in a single call.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"message_ids"},
			Params: map[string]any{
				"message_ids": param("array"),
				"format":      paramDefault("string", "metadata"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_create_draft",
			Summary:  "Create a draft email in the user's Gmail Drafts folder.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"html"},
			Params: map[string]any{
				"html":                param("string"),
				"to":                  paramDefault("string", ""),
				"subject":             paramDefault("string", ""),
				"account":             paramDefault("string", ""),
				"reply_to_message_id": paramDefault("string", ""),
				"signature":           paramDefault("string", ""),
			},
		},
		{
			Name:    "gmail_get_signature",
			Summary: "Get the user's Gmail signature from their account settings.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"send_as_email": paramDefault("string", ""),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:    "gmail_list_labels",
			Summary: "List all Gmail labels for the user's account.",
			Source:  "gmail_tool/gmail_tool.py",
			Params: map[string]any{
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_create_label",
			Summary:  "Create a new Gmail label.",
			Source:   "gmail_tool/gmail_tool.py",
			Required: []string{"name"},
			Params: map[string]any{
				"name":                    param("string"),
				"label_list_visibility":   paramDefault("string", "labelShow"),
				"message_list_visibility": paramDefault("string", "show"),
				"account":                 paramDefault("string", ""),
			},
		},
	},
}

var batch5Lane3CalendarTool = &Tool{
	ID:          batch5Lane3CalModule,
	Name:        "calendar",
	DisplayName: "Google Calendar",
	Summary:     "Google Calendar integration (8 actions).",
	Actions: []Action{
		{
			Name:    "calendar_list_events",
			Summary: "List upcoming calendar events.",
			Source:  "calendar_tool/calendar_tool.py",
			Params: map[string]any{
				"calendar_id": paramDefault("string", "primary"),
				"time_min":    param("string"),
				"time_max":    param("string"),
				"max_results": paramDefault("integer", 10),
				"query":       param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_get_event",
			Summary:  "Get details of a specific calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":    param("string"),
				"calendar_id": paramDefault("string", "primary"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_create_event",
			Summary:  "Create a new calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"summary", "start_time", "end_time"},
			Params: map[string]any{
				"summary":            param("string"),
				"start_time":         param("string"),
				"end_time":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"description":        param("string"),
				"location":           param("string"),
				"attendees":          param("array"),
				"send_notifications": paramDefault("boolean", true),
				"timezone":           param("string"),
				"all_day":            paramDefault("boolean", false),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_update_event",
			Summary:  "Update an existing calendar event. Only provided fields are changed.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"summary":            param("string"),
				"start_time":         param("string"),
				"end_time":           param("string"),
				"description":        param("string"),
				"location":           param("string"),
				"attendees":          param("array"),
				"remove_attendees":   param("array"),
				"send_notifications": paramDefault("boolean", true),
				"timezone":           param("string"),
				"all_day":            paramDefault("boolean", false),
				"add_meet_link":      paramDefault("boolean", false),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_delete_event",
			Summary:  "Delete a calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"send_notifications": paramDefault("boolean", true),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:    "calendar_list_calendars",
			Summary: "List all calendars accessible to the user.",
			Source:  "calendar_tool/calendar_tool.py",
			Params: map[string]any{
				"max_results": paramDefault("integer", 100),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_get_calendar",
			Summary:  "Get details of a specific calendar.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"calendar_id"},
			Params: map[string]any{
				"calendar_id": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_check_availability",
			Summary:  "Check availability by listing actual events in the time range.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"time_min", "time_max"},
			Params: map[string]any{
				"time_min":  param("string"),
				"time_max":  param("string"),
				"calendars": param("array"),
				"timezone":  paramDefault("string", "UTC"),
				"account":   paramDefault("string", ""),
			},
		},
	},
}
