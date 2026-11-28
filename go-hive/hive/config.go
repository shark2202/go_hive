package hive

import "github.com/shark2202/go_hive/go-hive/internal/server"

// PreferredModel resolves the model the user configured in Hive, falling back
// to the runtime default. It never exposes the API key.
//
// Parity: framework/config.py::get_preferred_model
func PreferredModel(home Home) string {
	return server.MainSlotModel(home, "")
}

// NewConfiguredProvider builds the model transport for this Hive home using
// the configured model, key, base URL and subscription settings.
//
// The resolved key stays inside the runtime: the SDK never returns it, and
// there is deliberately no get_api_key equivalent. model overrides the
// configured model when non-empty.
//
// Parity: framework/config.py::get_api_key (resolved, never exported)
func NewConfiguredProvider(home Home, model string) (Model, error) {
	return server.MainSlotProvider(home, model)
}
