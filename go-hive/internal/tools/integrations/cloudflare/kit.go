package cloudflare

// Type aliases and the schema/argument primitives the moved provider code
// calls. This package depends on integrationkit and the standard library
// only.

import (
	"context"
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

func param(typ string) map[string]any { return integrationkit.Param(typ) }

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

func clampInt(value, low, high int) int { return integrationkit.ClampInt(value, low, high) }

func asMap(value any) map[string]any { return integrationkit.AsMap(value) }

func asString(value any) string { return integrationkit.AsString(value) }

func decodeObject(raw []byte) (map[string]any, bool) { return integrationkit.DecodeObject(raw) }

func isTimeout(_ context.Context, err error) bool { return integrationkit.IsTimeout(err) }

func runtimeHTTP(rt *Runtime) *httpClient { return rt.HTTPClientOrNew() }

func runtimeGetenv(rt *Runtime) func(string) string { return rt.GetenvFunc() }

// anyParam declares a parameter with no type restriction, matching
// the upstream `value: Any` signature.
func anyParam() map[string]any { return map[string]any{} }

// listParam declares a JSON array parameter.
func listParam(items string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": items}}
}

// zoneParams builds the zone-scoped parameter map.
func zoneParams(extra map[string]any) map[string]any {
	out := map[string]any{"zone_id": param("string")}
	for name, schema := range extra {
		out[name] = schema
	}
	return out
}
