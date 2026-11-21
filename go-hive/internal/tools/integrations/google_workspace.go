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
