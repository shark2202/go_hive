package integrationkit

// Parameter helpers used by the generated and hand-written catalogs. They
// build the exact JSON-schema property shapes the upstream Python tool
// decorators produce, so a catalog entry can be read off the generator output
// without re-deriving the shape at every call site.

// Param builds a JSON-schema property with only a type.
func Param(typ string) map[string]any { return map[string]any{"type": typ} }

// ParamDefault builds a JSON-schema property with an upstream default.
func ParamDefault(typ string, value any) map[string]any {
	return map[string]any{"type": typ, "default": value}
}

// ParamItems builds an array property whose items carry the upstream element
// type (Python list[str] / list[dict]). The Go port keeps the same schema so a
// model that respects item types behaves identically on both runtimes.
func ParamItems(typ, itemType string) map[string]any {
	return map[string]any{"type": typ, "items": Param(itemType)}
}

// ParamEnumDefault builds an enum property with an upstream default and the
// upstream Literal member order (Python Literal[...]).
func ParamEnumDefault(typ string, value any, values []string) map[string]any {
	return map[string]any{"type": typ, "default": value, "enum": values}
}

// ParamArrayOfObjects is the upstream list[dict] shape: an array whose items
// are free-form objects (additionalProperties: true).
func ParamArrayOfObjects() map[string]any {
	return map[string]any{
		"type":  "array",
		"items": map[string]any{"type": "object", "additionalProperties": true},
	}
}

// MapArg reads a nested object input. Missing values return nil so callers can
// distinguish "absent" from "empty object".
func MapArg(input map[string]any, name string) map[string]any {
	value, ok := input[name]
	if !ok || value == nil {
		return nil
	}
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return nil
}
