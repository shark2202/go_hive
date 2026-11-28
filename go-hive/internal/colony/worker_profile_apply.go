package colony

// Spawn-time worker-profile application.
//
// Upstream applies a resolved profile in two places:
//   - spawn_tools is filtered through credentials.validation
//     .compute_unavailable_mcp_tools, so a worker never sees a credentialed
//     tool whose provider has no live account (framework/host/
//     colony_runtime.py::spawn); and
//   - Worker.run wraps the whole loop in account_overrides(profile
//     .integrations), so every provider-mapped tool call defaults to the
//     profile's account alias unless the call passes an explicit account.
//
// The Go port keeps the same two effects: the spawn surface drops tools whose
// credential provider has no live account, and the tool executor injects the
// profile's alias as the `account` argument for provider-mapped calls. The
// explicit `account` always wins.
//
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn (MCP
// credential filter, profile integrations)
// Parity: framework/host/worker.py::Worker.run (account_overrides)
// Parity: framework/credentials/validation.py::compute_unavailable_mcp_tools

import (
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

var (
	generatedProviderMapOnce sync.Once
	generatedProviderMap     map[string]string
)

// toolCredentialProvider resolves the credential provider that gates a tool:
// the caller-supplied colony map first (it also carries namespaced MCP tools),
// then the generated CREDENTIAL_SPECS reverse index the Python adapter uses.
func (r *ColonyRuntime) toolCredentialProvider(toolName string) string {
	if r != nil {
		if provider := strings.TrimSpace(r.toolProviderMap[toolName]); provider != "" {
			return provider
		}
	}
	generatedProviderMapOnce.Do(func() {
		generatedProviderMap = credentials.ToolProviderMap()
	})
	return strings.TrimSpace(generatedProviderMap[toolName])
}

// liveCredentialProviders returns the providers with a connected account. The
// bool is false when the runtime has no account view (or it failed), in which
// case the spawn filter is a no-op — upstream's adapter-unavailable path.
func (r *ColonyRuntime) liveCredentialProviders() (map[string]struct{}, bool) {
	if r == nil || r.credentialAccounts == nil {
		return nil, false
	}
	accounts := r.credentialAccounts()
	if accounts == nil {
		return nil, false
	}
	live := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		if account == nil {
			continue
		}
		provider := strings.TrimSpace(pythonStringValue(account["provider"]))
		if provider != "" {
			live[provider] = struct{}{}
		}
	}
	return live, true
}

// filterUnavailableMCPTools drops tools whose credentialed provider has no
// live account. Tools with no provider mapping are always kept. A nil account
// view keeps every tool (upstream degrades the same way).
func (r *ColonyRuntime) filterUnavailableMCPTools(defs []llm.ToolDef) []llm.ToolDef {
	live, ok := r.liveCredentialProviders()
	if !ok || len(defs) == 0 {
		return defs
	}
	out := make([]llm.ToolDef, 0, len(defs))
	for _, def := range defs {
		provider := r.toolCredentialProvider(def.Name)
		if provider != "" {
			if _, connected := live[provider]; !connected {
				continue
			}
		}
		out = append(out, def)
	}
	return out
}

// applyProfileAccountDefault copies the call input when the worker's profile
// binds a provider alias and the model did not pass an explicit account. An
// explicit non-empty account wins, matching account_overrides semantics.
func (r *ColonyRuntime) applyProfileAccountDefault(worker *Worker, req agentloop.ToolCallRequest) agentloop.ToolCallRequest {
	if worker == nil || len(worker.Integrations) == 0 {
		return req
	}
	provider := r.toolCredentialProvider(req.Name)
	if provider == "" {
		return req
	}
	alias := strings.TrimSpace(worker.Integrations[provider])
	if alias == "" {
		return req
	}
	if existing, ok := req.Input["account"]; ok {
		if text, _ := existing.(string); strings.TrimSpace(text) != "" {
			return req
		}
	}
	input := make(map[string]any, len(req.Input)+1)
	for key, value := range req.Input {
		input[key] = value
	}
	input["account"] = alias
	req.Input = input
	return req
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}
