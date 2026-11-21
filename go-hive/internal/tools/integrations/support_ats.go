// Batch 6 lane 2 of the integration port: the support/ATS group — Intercom
// (11 actions), Zendesk (8), Freshdesk (17) and Greenhouse (9).
//
// Upstream reference:
// tools/src/aden_tools/tools/{intercom,zendesk,freshdesk,greenhouse}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - Intercom, Zendesk, Freshdesk and Greenhouse resolve credentials from the
//     environment (INTERCOM_ACCESS_TOKEN; ZENDESK_SUBDOMAIN, ZENDESK_EMAIL,
//     ZENDESK_API_TOKEN; FRESHDESK_API_KEY, FRESHDESK_DOMAIN;
//     GREENHOUSE_API_TOKEN). WithCredential covers the single-token modules;
//     multi-value bundles stay environment-only, like the upstream
//     CredentialStoreAdapter split entries.
//   - Every request runs through Runtime.HTTP, so each action is exercisable
//     against an httptest server with WithBaseURL.
//   - HTTP failures mirror the upstream `_handle_response` / `_request`
//     helpers, including the exact 401/403/404/429 messages.
//   - Optional integer arguments keep the upstream None/absent distinction;
//     intercom_list_conversations clamps per_page to 1..150 where upstream only
//     caps the upper bound.
package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	supportMaxBody  = 8 << 20
	supportMaxError = 500
	supportMaxItems = 150
)

// InstallBatch6Lane2 publishes the batch-6 lane-2 tools (intercom, zendesk,
// freshdesk, greenhouse) into a shared registry and returns the tool count.
func InstallBatch6Lane2(target *tools.ToolRegistry) int {
	return newBatch6Lane2Registry().Install(target)
}

// newBatch6Lane2Registry builds a registry containing only the lane-2 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch6Lane2Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: supportModules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		intercomTool,
		zendeskTool,
		freshdeskTool,
		greenhouseTool,
	} {
		registry.AddTool(tool)
	}
	for action, impl := range intercomImpls() {
		registry.AddImpl("intercom", action, impl)
	}
	for action, impl := range zendeskImpls() {
		registry.AddImpl("zendesk", action, impl)
	}
	for action, impl := range freshdeskImpls() {
		registry.AddImpl("freshdesk", action, impl)
	}
	for action, impl := range greenhouseImpls() {
		registry.AddImpl("greenhouse", action, impl)
	}
	return registry
}

// supportModules declares the public endpoints and credential variables.
// Zendesk and Freshdesk derive their host from account credentials, so their
// BaseURL stays empty until WithBaseURL (tests) or the credential fills it in.
func supportModules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"intercom_tool": {
			ID: "intercom_tool", BaseURL: "https://api.intercom.io",
			EnvVars: []string{"INTERCOM_ACCESS_TOKEN"},
		},
		"zendesk_tool": {
			ID: "zendesk_tool",
			EnvVars: []string{
				"ZENDESK_SUBDOMAIN",
				"ZENDESK_EMAIL",
				"ZENDESK_API_TOKEN",
			},
		},
		"freshdesk_tool": {
			ID: "freshdesk_tool", EnvVars: []string{"FRESHDESK_API_KEY"},
		},
		"greenhouse_tool": {
			ID: "greenhouse_tool", BaseURL: "https://harvest.greenhouse.io/v1",
			EnvVars: []string{"GREENHOUSE_API_TOKEN"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// supportModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func supportModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(runtimeGetenv(rt))
	return strings.TrimSpace(token)
}

// supportReply is the status/body pair every module helper inspects.
type supportReply struct {
	Status int
	Body   []byte
}

// supportDo performs one JSON request and reads a bounded body.
func supportDo(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (supportReply, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return supportReply{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return supportReply{}, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		return supportReply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, supportMaxBody))
	if err != nil {
		return supportReply{}, err
	}
	return supportReply{Status: resp.StatusCode, Body: raw}, nil
}

// supportStringList renders a decoded JSON array of names as []string. A
// missing field produces an empty (non-nil) slice so it encodes as [].
func supportStringList(value any) []string {
	items := asSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, asString(item))
	}
	return out
}

// supportNumberList renders a decoded JSON array of identifiers, keeping
// ints as ints.
func supportNumberList(value any) []any {
	items := asSlice(value)
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, jsonNumber(item))
	}
	return out
}

// supportParseISO mirrors datetime.fromisoformat for the timestamps these
// tools accept, defaulting naive values to UTC.
func supportParseISO(raw string) (time.Time, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			if parsed.Location() == time.UTC {
				return parsed, true
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
