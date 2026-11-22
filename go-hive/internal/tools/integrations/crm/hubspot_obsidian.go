// Batch 5, lane 4 of the integration port: HubSpot (upstream hubspot_tool,
// 15 actions) and Obsidian (upstream obsidian_tool, 6 actions).
//
// Both modules keep upstream action names, parameter names/defaults and
// response shapes. Requests go through Runtime.HTTP so every action is
// exercisable against an httptest server; credentials resolve from
// WithCredential or HUBSPOT_ACCESS_TOKEN / OBSIDIAN_REST_API_KEY.
//
// Deliberate limits:
//   - hubspot_* accept the upstream `account` workspace selector, but the Go
//     runtime holds one token per module; credential-store aliases are not
//     reproduced here.
//   - obsidian_* share the runtime transport, so upstream's per-request
//     `verify=False` (self-signed local certificates) is a runtime TLS
//     configuration concern rather than an action knob.
//   - Neither provider is retried on 429; upstream has no retry either.
package crm

import (
	"os"
	"strings"
	"time"
)

// newBatch5Lane4Registry builds a registry containing only the batch-5 lane-4
// tools, sharing the Runtime surface used by the rest of the port.
func NewHubSpotObsidianRegistry(opts ...Option) *Registry {
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
	for _, tool := range []*Tool{hubSpotTool, obsidianTool} {
		registry.AddTool(tool)
	}
	for action, impl := range hubSpotImpls() {
		registry.AddImpl("hubspot", action, impl)
	}
	for action, impl := range obsidianImpls() {
		registry.AddImpl("obsidian", action, impl)
	}
	return registry
}

// modules declares the public endpoints and credential variables
// for the two modules. Tests override BaseURL with WithBaseURL.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"hubspot_tool": {
			ID:      "hubspot_tool",
			BaseURL: "https://api.hubapi.com",
			EnvVars: []string{"HUBSPOT_ACCESS_TOKEN"},
			OAuth:   hubSpotOAuth,
		},
		// The Obsidian base URL is instance-specific, so it stays empty here:
		// WithBaseURL wins, then OBSIDIAN_REST_BASE_URL, then the plugin default.
		"obsidian_tool": {
			ID:      "obsidian_tool",
			EnvVars: []string{"OBSIDIAN_REST_API_KEY"},
		},
	}
}

// ------------------------------------------------------------ shared bits ---

// moduleURL resolves a module base URL, honouring WithBaseURL, and falls
// back to the public endpoint when the module has no configured base.
func moduleURL(rt *Runtime, moduleID, fallback string) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			if base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"); base != "" {
				return base
			}
		}
	}
	return fallback
}

// moduleToken resolves the module credential from WithCredential or the
// configured environment lookup.
func moduleToken(rt *Runtime, moduleID string) string {
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
