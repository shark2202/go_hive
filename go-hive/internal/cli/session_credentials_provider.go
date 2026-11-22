package cli

import "github.com/shark2202/go_hive/go-hive/internal/hivehome"

// sessionCredentialsProvider snapshots the connected accounts for the
// session's static "# Credentials" block, exactly like upstream's
// queen_orchestrator._build_credentials_provider: the same store the
// credential routes read, captured when the prompt is built.
//
// Parity: framework/server/queen_orchestrator.py::_build_credentials_provider
func sessionCredentialsProvider(home hivehome.Home) func() []map[string]any {
	return func() []map[string]any {
		adapter := buildSessionCredentialAdapter(home)
		if adapter == nil {
			return nil
		}
		return adapter.GetAllAccountInfo()
	}
}
