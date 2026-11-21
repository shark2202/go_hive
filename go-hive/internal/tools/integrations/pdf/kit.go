package pdf

// Type aliases and the schema/argument primitives the moved provider code
// calls. This package depends on integrationkit and the standard library only.

import "github.com/aden-hive/hive-go/internal/tools/integrationkit"

type (
	Tool         = integrationkit.Tool
	Action       = integrationkit.Action
	Runtime      = integrationkit.Runtime
	Registry     = integrationkit.Registry
	ActionFunc   = integrationkit.ActionFunc
	ModuleConfig = integrationkit.ModuleConfig
	Option       = integrationkit.Option
)

// param builds a JSON-schema property with only a type.
func param(typ string) map[string]any { return integrationkit.Param(typ) }

// paramDefault builds a JSON-schema property with an upstream default.
func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

// paramItems builds an array property whose items carry the upstream element
// type (Python list[str]).
func paramItems(typ, itemType string) map[string]any {
	return integrationkit.ParamItems(typ, itemType)
}

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

// errTimeout reports whether err is a transport timeout (kit seam).
func errTimeout(err error) bool { return integrationkit.ErrTimeout(err) }
