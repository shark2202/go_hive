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
package integrations

import (
	"os"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// InstallBatch5Lane4 publishes the batch-5 lane-4 tools (hubspot and obsidian)
// into a shared registry and returns the number of tools registered.
func InstallBatch5Lane4(target *tools.ToolRegistry) int {
	return newBatch5Lane4Registry().Install(target)
}

// newBatch5Lane4Registry builds a registry containing only the batch-5 lane-4
// tools, sharing the Runtime surface used by the rest of the port.
func newBatch5Lane4Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch5Lane4Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{batch5HubSpotTool, batch5ObsidianTool} {
		registry.AddTool(tool)
	}
	for action, impl := range batch5HubSpotImpls() {
		registry.AddImpl("hubspot", action, impl)
	}
	for action, impl := range batch5ObsidianImpls() {
		registry.AddImpl("obsidian", action, impl)
	}
	return registry
}

// batch5Lane4Modules declares the public endpoints and credential variables
// for the two modules. Tests override BaseURL with WithBaseURL.
func batch5Lane4Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"hubspot_tool": {
			ID:      "hubspot_tool",
			BaseURL: "https://api.hubapi.com",
			EnvVars: []string{"HUBSPOT_ACCESS_TOKEN"},
			OAuth:   batch5Lane4HubSpotOAuth,
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

// batch5Lane4Getenv returns the runtime environment lookup.
func batch5Lane4Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch5Lane4ModuleURL resolves a module base URL, honouring WithBaseURL, and falls
// back to the public endpoint when the module has no configured base.
func batch5Lane4ModuleURL(rt *Runtime, moduleID, fallback string) string {
	if rt != nil && rt.Modules != nil {
		if cfg := rt.Modules[moduleID]; cfg != nil {
			if base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"); base != "" {
				return base
			}
		}
	}
	return fallback
}

// batch5Lane4ModuleToken resolves the module credential from WithCredential or the
// configured environment lookup.
func batch5Lane4ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(batch5Lane4Getenv(rt))
	return strings.TrimSpace(token)
}

// batch5Lane4AccountToken resolves a lane-4 credential for the current call: an
// account alias the action selected wins over the module's single token
// (upstream `_get_token(account)` -> credentials.get_by_alias(provider,
// account)).
func batch5Lane4AccountToken(rt *Runtime, moduleID, provider string) string {
	return accountToken(rt, moduleID, provider)
}

// batch5Lane4Get mirrors Python's dict.get: a present key wins even when null.
func batch5Lane4Get(object map[string]any, key string, fallback any) any {
	if value, present := object[key]; present {
		return value
	}
	return fallback
}
