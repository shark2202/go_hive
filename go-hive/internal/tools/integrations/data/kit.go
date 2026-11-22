package data

// Local type aliases and argument/schema helpers keep the moved provider code
// reading like the rest of the integrations port while this package depends on
// integrationkit only.

import (
	"net/http"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
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

// WithBaseURL points one module at an alternate base URL (tests).
func WithBaseURL(moduleID, baseURL string) Option {
	return integrationkit.WithBaseURL(moduleID, baseURL)
}

// WithCredential injects a resolved module credential (tests).
func WithCredential(moduleID, value string) Option {
	return integrationkit.WithCredential(moduleID, value)
}

// WithGetenv replaces the environment lookup (tests).
func WithGetenv(fn func(string) string) Option {
	return integrationkit.WithGetenv(fn)
}

// WithHTTPClient replaces the runtime transport (tests).
func WithHTTPClient(client *http.Client) Option {
	return integrationkit.WithHTTPClient(client)
}

// WithClock replaces the runtime clock (tests).
func WithClock(now func() time.Time) Option { return integrationkit.WithClock(now) }

func newHTTPClient(client *http.Client) *httpClient { return integrationkit.NewHTTPClient(client) }

// newRegistry builds the kernel registry for one runtime (historical name).
func newRegistry(rt *Runtime) *Registry { return integrationkit.NewRegistry(rt) }

var (
	// ErrUnknownTool is returned when no integration owns the requested name.
	ErrUnknownTool = integrationkit.ErrUnknownTool
	// ErrUnknownAction is returned when a tool has no such action.
	ErrUnknownAction = integrationkit.ErrUnknownAction
)

func param(typ string) map[string]any { return integrationkit.Param(typ) }

func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func paramItems(typ, itemType string) map[string]any {
	return integrationkit.ParamItems(typ, itemType)
}

func paramEnumDefault(typ string, value any, values []string) map[string]any {
	return integrationkit.ParamEnumDefault(typ, value, values)
}

func paramArrayOfObjects() map[string]any { return integrationkit.ParamArrayOfObjects() }

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func intArg(input map[string]any, name string, fallback int) int {
	return integrationkit.IntArg(input, name, fallback)
}

func floatArg(input map[string]any, name string, fallback float64) float64 {
	return integrationkit.FloatArg(input, name, fallback)
}

func boolArg(input map[string]any, name string, fallback bool) bool {
	return integrationkit.BoolArg(input, name, fallback)
}

func stringSliceArg(input map[string]any, name string) []string {
	return integrationkit.StringSliceArg(input, name)
}

func scalarString(value any) string { return integrationkit.ScalarString(value) }

func jsonString(v any) string { return integrationkit.JSONString(v) }
