package tools

import "github.com/aden-hive/hive-go/internal/llm"

// StripContextParams returns defs with every CONTEXT_PARAMS name removed from
// each tool's model-visible properties and required list.
//
// Parity: core/framework/loader/tool_registry.py::ToolRegistry
//
// This is the Go equivalent of ToolRegistry._convert_mcp_tool_to_framework_tool:
// tools declare framework-injected arguments (session_cwd, principal, ...) so
// the host knows what to inject, and the schema handed to the model never
// carries them. Injection happens at execution time in the host, never through
// the schema, so stripping here cannot break the internal path.
func StripContextParams(defs []llm.ToolDef) []llm.ToolDef {
	out := make([]llm.ToolDef, len(defs))
	copy(out, defs)
	for i := range out {
		if out[i].Parameters == nil {
			continue
		}
		out[i].Parameters = stripContextParamsFromSchema(out[i].Parameters)
	}
	return out
}

// stripContextParamsFromSchema returns a copy of one JSON-schema object
// without the context parameters. The original map is left untouched.
func stripContextParamsFromSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		out[key] = value
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		cleaned := make(map[string]any, len(properties))
		for name, property := range properties {
			if IsContextParam(name) {
				continue
			}
			cleaned[name] = property
		}
		out["properties"] = cleaned
	}
	switch required := schema["required"].(type) {
	case []string:
		out["required"] = filterContextStrings(required)
	case []any:
		filtered := make([]any, 0, len(required))
		for _, name := range required {
			if text, ok := name.(string); ok && IsContextParam(text) {
				continue
			}
			filtered = append(filtered, name)
		}
		out["required"] = filtered
	}
	return out
}

func filterContextStrings(names []string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if IsContextParam(name) {
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
}

// IsContextParam reports whether name is framework context rather than a
// model-facing argument.
func IsContextParam(name string) bool {
	_, ok := ContextParams[name]
	return ok
}
