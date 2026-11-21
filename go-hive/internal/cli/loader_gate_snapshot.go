package cli

// MCP admission-gate snapshot for the live-tools diagnostic.
//
// Upstream's live-tools diagnostic re-runs
// ToolRegistry._compute_mcp_gate_cred_snapshot: the tool→credential provider
// map plus the providers with a live account. The Go registry reads that data
// through tools.SetMCPGateCredSnapshotProvider; this file builds it from the
// generated credential specs and the process's credential store adapter, so
// the diagnostic's per-provider counts are real instead of absent.
//
// Parity: framework/loader/tool_registry.py::ToolRegistry._compute_mcp_gate_cred_snapshot
// Parity: tools/src/aden_tools/credentials/store_adapter.py::CredentialStoreAdapter.get_tool_provider_map

import (
	"sort"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools"
)

// wireMCPGateCredSnapshot installs the serve process's admission-gate snapshot.
// Called once from the serve assembly, before any session or registry exists; a
// process that never calls it keeps the dormant empty snapshot.
func wireMCPGateCredSnapshot(home hivehome.Home) {
	tools.SetMCPGateCredSnapshotProvider(func() (map[string]string, []string) {
		providers := credentials.ToolProviderMap()
		live := []string{}
		seen := map[string]struct{}{}
		if adapter := buildSessionCredentialAdapter(home); adapter != nil {
			for _, account := range adapter.GetAllAccountInfo() {
				provider, _ := account["provider"].(string)
				if provider == "" {
					continue
				}
				if _, dup := seen[provider]; dup {
					continue
				}
				seen[provider] = struct{}{}
				live = append(live, provider)
			}
		}
		sort.Strings(live)
		return providers, live
	})
}
