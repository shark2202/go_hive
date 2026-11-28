package llm

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// hiveHomePlaceholder is the literal the prompt/tool text uses for the canonical
// hive root. Only this literal is rewritten, matching framework/config.py's
// resolve_hive_paths_in_text (a plain substring replacement, not a path parse).
const hiveHomePlaceholder = "~/.hive"

// toolDescriptionHiveHome resolves the HIVE_HOME that tool descriptions are
// rewritten to. Indirected through a variable so tests can inject homes that
// contain spaces or non-ASCII characters without mutating the process
// environment.
//
// Parity: framework.config._resolve_hive_home — $HIVE_HOME (expanduser), else
// ~/.hive. hivehome.Resolve additionally makes a relative override absolute;
// the rewrite target stays the runtime's real hive root either way.
var toolDescriptionHiveHome = func() (string, bool) {
	home, err := hivehome.Resolve()
	if err != nil || strings.TrimSpace(home.Root) == "" {
		return "", false
	}
	return home.Root, true
}

// resolveHivePathsDeep mirrors framework.config.resolve_hive_paths_deep: every
// string *value* inside the tool definition is rewritten, dict keys and
// non-string scalars are left alone, and containers are rebuilt so the caller's
// ToolDef is never mutated by the projection.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._tool_to_openai_format
// — Python applies resolve_hive_paths_deep to the whole OpenAI function dict
// (name, description, and the nested parameters/property descriptions).
func resolveHivePathsDeep(value any, home string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, hiveHomePlaceholder, home)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = resolveHivePathsDeep(item, home)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = resolveHivePathsDeep(item, home)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for i, item := range v {
			rewritten, _ := resolveHivePathsDeep(item, home).(map[string]any)
			out[i] = rewritten
		}
		return out
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = strings.ReplaceAll(item, hiveHomePlaceholder, home)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, item := range v {
			out[key] = strings.ReplaceAll(item, hiveHomePlaceholder, home)
		}
		return out
	}
	return value
}
