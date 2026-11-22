package infra

// Local type aliases keep the moved provider code reading like the rest of the
// integrations port while this package depends on integrationkit only. The
// schema-param builders and argument readers forward to the kit so tool
// definitions and action bodies keep their historical spelling.

import (
	"net/http"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

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

func newHTTPClient(client *http.Client) *httpClient { return integrationkit.NewHTTPClient(client) }

func WithHTTPClient(client *http.Client) Option { return integrationkit.WithHTTPClient(client) }

func WithBaseURL(moduleID, baseURL string) Option {
	return integrationkit.WithBaseURL(moduleID, baseURL)
}

func WithCredential(moduleID, value string) Option {
	return integrationkit.WithCredential(moduleID, value)
}

func WithGetenv(fn func(string) string) Option { return integrationkit.WithGetenv(fn) }

// param builds a JSON-schema property with only a type.
func param(typ string) map[string]any { return integrationkit.Param(typ) }

// paramDefault builds a JSON-schema property with an upstream default.
func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func intArg(input map[string]any, name string, fallback int) int {
	return integrationkit.IntArg(input, name, fallback)
}

func boolArg(input map[string]any, name string, fallback bool) bool {
	return integrationkit.BoolArg(input, name, fallback)
}
