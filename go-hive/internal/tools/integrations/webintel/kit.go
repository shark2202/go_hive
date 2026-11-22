package webintel

// Local type aliases keep the moved provider code reading like the rest of the
// integrations port while this package depends on integrationkit only; the
// small option/argument helpers below are the terse names the moved call
// sites already use.

import (
	"net/http"
	"time"

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
func WithClock(now func() time.Time) Option {
	return integrationkit.WithClock(now)
}

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

func optionalFloat(input map[string]any, name string) (float64, bool) {
	return integrationkit.OptionalFloat(input, name)
}

func clampFloat(value, low, high float64) float64 {
	return integrationkit.ClampFloat(value, low, high)
}

func minInt(left, right int) int { return integrationkit.MinInt(left, right) }
