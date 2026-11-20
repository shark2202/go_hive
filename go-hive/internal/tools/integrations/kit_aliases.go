package integrations

// This file is the compatibility seam for the integrationkit extraction. The
// shared primitives now live in internal/tools/integrationkit; provider files
// keep their historical unqualified names through aliases, and the helpers
// below forward to the kit implementation so no call site changes.

import (
	"net/http"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

type (
	Runtime       = integrationkit.Runtime
	Option        = integrationkit.Option
	AccountSource = integrationkit.AccountSource
	ModuleConfig  = integrationkit.ModuleConfig
	Tool          = integrationkit.Tool
	Action        = integrationkit.Action
	ActionFunc    = integrationkit.ActionFunc
	RestSpec      = integrationkit.RestSpec
	httpClient    = integrationkit.HTTPClient
)

func newHTTPClient(client *http.Client) *httpClient { return integrationkit.NewHTTPClient(client) }

func WithHTTPClient(client *http.Client) Option { return integrationkit.WithHTTPClient(client) }

func WithBaseURL(moduleID, baseURL string) Option {
	return integrationkit.WithBaseURL(moduleID, baseURL)
}

func WithCredential(moduleID, value string) Option {
	return integrationkit.WithCredential(moduleID, value)
}

func WithRoots(roots ...string) Option { return integrationkit.WithRoots(roots...) }

func WithClock(now func() time.Time) Option { return integrationkit.WithClock(now) }

func WithAccountSource(source AccountSource) Option {
	return integrationkit.WithAccountSource(source)
}

func WithGetenv(fn func(string) string) Option { return integrationkit.WithGetenv(fn) }

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
