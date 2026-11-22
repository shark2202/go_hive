package enrichment

// Local type aliases keep the moved provider code reading like the rest of the
// integrations port while this package depends on integrationkit only.

import "github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"

type (
	Tool         = integrationkit.Tool
	Action       = integrationkit.Action
	Runtime      = integrationkit.Runtime
	Registry     = integrationkit.Registry
	ActionFunc   = integrationkit.ActionFunc
	ModuleConfig = integrationkit.ModuleConfig
	Option       = integrationkit.Option
	httpClient   = integrationkit.HTTPClient
)
