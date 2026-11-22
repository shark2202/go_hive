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
package gworkspace

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
)

const (
	maxBody = 8 << 20

	sheetsModule = "google_sheets_tool"
	docsModule   = "google_docs_tool"
	driveModule  = "google_docs_drive"
	gmailModule  = "gmail_tool"
	calModule    = "calendar_tool"

	sheetsPrefix = "/v4/spreadsheets"
	docsPrefix   = "/v1/documents"
	drivePrefix  = "/drive/v3"
	gmailPrefix  = "/gmail/v1/users/me"
	calPrefix    = "/calendar/v3"
)

// newBatch5Lane3Registry builds a registry containing only the batch-5 lane-3
// tools, sharing the Runtime surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := newRegistry(runtime)
	for _, tool := range []*Tool{
		sheetsTool,
		docsTool,
		gmailTool,
		calendarTool,
	} {
		registry.AddTool(tool)
	}
	impls := map[string]map[string]ActionFunc{
		"google_sheets": sheetsImpls(),
		"google_docs":   docsImpls(),
		"gmail":         gmailImpls(),
		"calendar":      calendarImpls(),
	}
	for tool, actions := range impls {
		for action, impl := range actions {
			registry.AddImpl(tool, action, impl)
		}
	}
	return registry
}

// modules declares the public endpoints and the credential variable
// shared by all four modules. Tests override BaseURL with WithBaseURL. The
// three retrievable modules also declare the standard OAuth2 refresh-token
// source; the sheet/docs drive entry reuses the docs credential.
func modules() map[string]*ModuleConfig {
	google := []string{"GOOGLE_ACCESS_TOKEN"}
	return map[string]*ModuleConfig{
		sheetsModule: {ID: sheetsModule, BaseURL: "https://sheets.googleapis.com", EnvVars: google, OAuth: googleOAuth},
		docsModule:   {ID: docsModule, BaseURL: "https://docs.googleapis.com", EnvVars: google, OAuth: googleOAuth},
		driveModule:  {ID: driveModule, BaseURL: "https://www.googleapis.com"},
		gmailModule:  {ID: gmailModule, BaseURL: "https://gmail.googleapis.com", EnvVars: google, OAuth: googleOAuth},
		calModule:    {ID: calModule, BaseURL: "https://www.googleapis.com", EnvVars: google, OAuth: googleOAuth},
	}
}

// ------------------------------------------------------------ shared bits ---

// accountToken resolves a Google module credential for the current
// call: an account alias the action selected wins over the module's single
// token (upstream `_get_token(account)` -> credentials.get_by_alias("google",
// account)). Google Sheets is the one module that keeps moduleToken:
// upstream treats its `account` parameter as a framework tracking value.
func accountToken(rt *Runtime, moduleID string) string {
	return resolveAccountToken(rt, moduleID, "google")
}

// accountCredential reports whether an alias-aware Google module
// has a usable credential: the selected account resolves, or the module has a
// token or an OAuth2 refresh-token source that mints one.
func accountCredential(rt *Runtime, moduleID string) bool {
	if token, handled := rt.ResolveAccountToken("google"); handled {
		return token != ""
	}
	return credential(rt, moduleID)
}

// credential reports whether a single-credential Google module has
// a token or an OAuth2 refresh-token source.
func credential(rt *Runtime, moduleID string) bool {
	if rt.OAuthRefreshable(moduleID) {
		return true
	}
	return moduleToken(rt, moduleID) != ""
}

// valueError carries an upstream-style value error through the
// (status, body, error) transport signature so callers return it unchanged.
type valueError struct{ dict map[string]any }

func (e *valueError) Error() string {
	return fmt.Sprint(e.dict["error"])
}

// accessToken resolves the token for one Google request, refreshing
// an expired OAuth2 access token first. refreshable reports whether a 401 can
// be retried with a freshly minted token.
func accessToken(ctx context.Context, rt *Runtime, moduleID string, useAlias bool) (string, bool, map[string]any) {
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

// segments percent-encodes each path segment and joins them.
// Segments are escaped individually so an A1 range keeps its ":" separator but
// spaces, "!" and "#" are encoded the way httpx would encode them.
func segments(segments ...string) string {
	escaped := make([]string, 0, len(segments))
	for _, segment := range segments {
		escaped = append(escaped, url.PathEscape(segment))
	}
	return strings.Join(escaped, "/")
}

// do sends one authorized request and returns status and body.
// Transport failures are returned as errors for the caller's message mapping.
// An expired OAuth2 token is refreshed before the request; a 401 retries once
// with a freshly minted token.
func do(ctx context.Context, rt *Runtime, moduleID, method, path string, query map[string]string, body any) (int, []byte, error) {
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
	case driveModule:
		credentialModule = docsModule
	case sheetsModule:
		useAlias = false
	}
	token, _, problem := accessToken(ctx, rt, credentialModule, useAlias)
	if problem != nil {
		return 0, nil, &valueError{dict: problem}
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
			return 0, nil, &valueError{dict: resolution.Failure}
		}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, raw, nil
}

// transportError maps a transport failure onto the upstream
// timeout/network error dicts.
func transportError(ctx context.Context, err error) map[string]any {
	var valueErr *valueError
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

// errorDetail mirrors response.json().get("error", {}).get("message",
// response.text), falling back to the raw body when the shape differs.
func errorDetail(raw []byte) string {
	if payload, ok := jsonObject(raw); ok {
		if errorObject, ok := payload["error"].(map[string]any); ok {
			if message, ok := errorObject["message"].(string); ok {
				return message
			}
		}
	}
	return string(raw)
}

// pyType names a decoded value the way Python's type().__name__ would.
func pyType(value any) string {
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

// optionalFloat distinguishes an absent/null argument from 0.0.
func optionalFloat(input map[string]any, name string) (float64, bool) {
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

// requestID builds the 12-hex-character Meet request id upstream
// generates with uuid4().hex[:12].
func requestID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err == nil {
		return hex.EncodeToString(buffer)
	}
	return fmt.Sprintf("%012x", time.Now().UnixNano()&0xffffffffffff)
}

// pyISO renders a timestamp the way Python's datetime.isoformat()
// does: "+00:00" for UTC and microseconds only when non-zero.
func pyISO(value time.Time) string {
	base := value.Format("2006-01-02T15:04:05")
	if micros := value.Nanosecond() / 1000; micros != 0 {
		base += fmt.Sprintf(".%06d", micros)
	}
	if _, offset := value.Zone(); offset == 0 {
		return base + "+00:00"
	}
	return base + value.Format("-07:00")
}
