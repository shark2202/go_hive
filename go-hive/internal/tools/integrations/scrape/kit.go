package scrape

// Type aliases and the schema/argument primitives the moved implementation
// calls. This file depends on integrationkit and the standard library only.

import (
	"os"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

type (
	Tool         = integrationkit.Tool
	Action       = integrationkit.Action
	Runtime      = integrationkit.Runtime
	Registry     = integrationkit.Registry
	ActionFunc   = integrationkit.ActionFunc
	ModuleConfig = integrationkit.ModuleConfig
)

// param builds a JSON-schema property with only a type.
func param(typ string) map[string]any { return integrationkit.Param(typ) }

// paramDefault builds a JSON-schema property with an upstream default.
func paramDefault(typ string, value any) map[string]any {
	return integrationkit.ParamDefault(typ, value)
}

func stringArg(input map[string]any, name string) string {
	return integrationkit.StringArg(input, name)
}

func boolArg(input map[string]any, name string, fallback bool) bool {
	return integrationkit.BoolArg(input, name, fallback)
}

// errTimeout reports whether err is a transport timeout (kit seam).
func errTimeout(err error) bool { return integrationkit.ErrTimeout(err) }

// envValue reads one environment value through the runtime seam.
func envValue(rt *Runtime, name string) string {
	if rt == nil || rt.Getenv == nil {
		return os.Getenv(name)
	}
	return rt.Getenv(name)
}
