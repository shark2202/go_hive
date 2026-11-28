package llm

import (
	"reflect"
	"strings"
	"testing"
)

// stubToolDescriptionHome replaces the resolved HIVE_HOME for the duration of a
// test. Tests inject homes with spaces and non-ASCII characters: the desktop
// runtime ships its hive root under "Application Support", so a rewrite that
// mangles those bytes would corrupt every tool description.
func stubToolDescriptionHome(t *testing.T, home string, ok bool) {
	t.Helper()
	previous := toolDescriptionHiveHome
	toolDescriptionHiveHome = func() (string, bool) {
		return home, ok
	}
	t.Cleanup(func() {
		toolDescriptionHiveHome = previous
	})
}

func descriptionToolDef() ToolDef {
	return ToolDef{
		Name:        "skills_list",
		Description: "List skills under ~/.hive/skills; memories live in ~/.hive/memories.",
		Parameters: map[string]any{
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path under ~/.hive/skills",
					"enum":        []any{"~/.hive/skills", "~/.hive/memories"},
				},
				"limit": map[string]any{
					"type":    "integer",
					"default": float64(10),
				},
			},
			"required": []any{"path"},
		},
	}
}

// TestToolToOpenAIFormatRewritesHiveHomeInDescriptions pins R6: the projection
// rewrites every ~/.hive occurrence to the resolved HIVE_HOME, deeply, exactly
// like the Python LLM boundary.
//
// Parity: core/framework/llm/litellm.py::_tool_to_openai_format ->
// framework.config.resolve_hive_paths_deep. The home deliberately contains a
// space and CJK characters.
func TestToolToOpenAIFormatRewritesHiveHomeInDescriptions(t *testing.T) {
	const home = "/Users/测试 用户/Hive Home"
	stubToolDescriptionHome(t, home, true)

	wire := ToolToOpenAIFormat(descriptionToolDef())
	fn, ok := wire["function"].(map[string]any)
	if !ok {
		t.Fatalf("function = %T", wire["function"])
	}

	wantDescription := "List skills under " + home + "/skills; memories live in " + home + "/memories."
	if fn["description"] != wantDescription {
		t.Fatalf("description = %q, want %q", fn["description"], wantDescription)
	}
	if fn["name"] != "skills_list" {
		t.Fatalf("name = %v", fn["name"])
	}

	parameters, _ := fn["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	path, _ := properties["path"].(map[string]any)
	if got, want := path["description"], "Absolute path under "+home+"/skills"; got != want {
		t.Fatalf("nested description = %q, want %q", got, want)
	}
	if got, want := path["enum"], []any{home + "/skills", home + "/memories"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("enum = %#v, want %#v", got, want)
	}
	if got, want := parameters["required"], []any{"path"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("required = %#v, want %#v", got, want)
	}

	// Non-string scalars survive the deep walk untouched.
	limit, _ := properties["limit"].(map[string]any)
	if limit["default"] != float64(10) || limit["type"] != "integer" {
		t.Fatalf("limit = %#v", limit)
	}
}

// TestToolToOpenAIFormatUsesPlainSubstringReplacement pins the replacement
// semantics: Python does text.replace("~/.hive", HIVE_HOME), so occurrences
// inside a longer path are rewritten too — it is not a path-component match.
//
// Parity: core/framework/config.py::resolve_hive_paths_in_text (plain
// str.replace, applied by litellm.py::_tool_to_openai_format).
func TestToolToOpenAIFormatUsesPlainSubstringReplacement(t *testing.T) {
	const home = "/opt/hive home"
	stubToolDescriptionHome(t, home, true)

	wire := ToolToOpenAIFormat(ToolDef{
		Name:        "read_file",
		Description: "Read ~/.hive/../~/.hive-backup/file.txt",
	})
	fn, _ := wire["function"].(map[string]any)
	if got, want := fn["description"], "Read "+home+"/../"+home+"-backup/file.txt"; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

// TestToolToOpenAIFormatLeavesOtherTextAlone is the negative regression against
// an over-eager rewrite: only the exact "~/.hive" literal changes. "~/hive",
// ".hive", and "$HIVE_HOME" are different spellings and must survive verbatim.
//
// Parity-exempt: negative half of the lib/test_litellm_provider.py boundary
// check; the positive parity case is
// TestToolToOpenAIFormatRewritesHiveHomeInDescriptions.
func TestToolToOpenAIFormatLeavesOtherTextAlone(t *testing.T) {
	const home = "/Users/tester/.hive"
	stubToolDescriptionHome(t, home, true)

	const description = "Defaults: ~/hive, .hive, $HIVE_HOME, and only ~/.hive is rewritten."
	wire := ToolToOpenAIFormat(ToolDef{Name: "read_file", Description: description})
	fn, _ := wire["function"].(map[string]any)
	want := "Defaults: ~/hive, .hive, $HIVE_HOME, and only " + home + " is rewritten."
	if got := fn["description"]; got != want {
		t.Fatalf("description = %q, want %q", got, want)
	}
}

// TestToolToOpenAIFormatSkipsRewriteWhenHomeUnresolved is the negative
// regression for an unresolvable HIVE_HOME: replacing the literal with "" would
// silently corrupt every description, so the projection must pass through
// unchanged instead.
//
// Parity-exempt: defensive Go branch; config.py::_resolve_hive_home is total
// (Path.home()/".hive"), so Python has no equivalent failure state to mirror.
func TestToolToOpenAIFormatSkipsRewriteWhenHomeUnresolved(t *testing.T) {
	stubToolDescriptionHome(t, "", false)

	def := descriptionToolDef()
	wire := ToolToOpenAIFormat(def)
	fn, _ := wire["function"].(map[string]any)
	if fn["description"] != def.Description {
		t.Fatalf("description = %q, want it unchanged", fn["description"])
	}
	parameters, _ := fn["parameters"].(map[string]any)
	properties, _ := parameters["properties"].(map[string]any)
	path, _ := properties["path"].(map[string]any)
	if path["description"] != "Absolute path under ~/.hive/skills" {
		t.Fatalf("nested description = %q, want it unchanged", path["description"])
	}
}

// TestToolToOpenAIFormatDoesNotMutateDefinition pins that the rewrite is a
// projection, not an in-place edit: the registry keeps the canonical ~/.hive
// text (Python builds a fresh dict), so a second call on the same ToolDef must
// produce the same output rather than a double-rewritten path.
//
// Parity: core/framework/llm/litellm.py::_tool_to_openai_format (builds a new
// dict; the Tool the registry owns is never mutated).
func TestToolToOpenAIFormatDoesNotMutateDefinition(t *testing.T) {
	const home = "/Users/tester/Hive Home"
	stubToolDescriptionHome(t, home, true)

	def := descriptionToolDef()
	first := ToolToOpenAIFormat(def)
	if strings.Contains(def.Description, home) {
		t.Fatalf("description was mutated in place: %q", def.Description)
	}
	if !strings.Contains(def.Description, "~/.hive") {
		t.Fatalf("description lost its canonical text: %q", def.Description)
	}
	nested := def.Parameters["properties"].(map[string]any)["path"].(map[string]any)
	if !strings.Contains(nested["description"].(string), "~/.hive") {
		t.Fatalf("nested description was mutated: %q", nested["description"])
	}

	second := ToolToOpenAIFormat(def)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second projection differs\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

// TestBuildCompletionKwargsRewritesToolDescriptions proves the rewrite is wired
// into the actual request payload, not just the helper: both the non-streaming
// (buildCompletionKwargs) and streaming (completionRequestKwargs) transports
// assemble their tools through it.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.complete /
// .stream (both pass _tool_to_openai_format output to litellm).
func TestBuildCompletionKwargsRewritesToolDescriptions(t *testing.T) {
	const home = "/Users/测试 用户/Hive Home"
	stubToolDescriptionHome(t, home, true)

	provider := NewProvider(ProviderOptions{})
	def := descriptionToolDef()

	for name, kwargs := range map[string]map[string]any{
		"complete": provider.buildCompletionKwargs(nil, CompleteOptions{Tools: []ToolDef{def}}),
		"stream": provider.completionRequestKwargs(CompletionRequest{
			Messages: []ChatMessage{{Role: "user", Content: "hi"}},
			Tools:    []ToolDef{def},
		}),
	} {
		tools, ok := kwargs["tools"].([]any)
		if !ok || len(tools) != 1 {
			t.Fatalf("%s: tools = %#v", name, kwargs["tools"])
		}
		entry, _ := tools[0].(map[string]any)
		fn, _ := entry["function"].(map[string]any)
		if !strings.Contains(fn["description"].(string), home+"/skills") {
			t.Fatalf("%s: description = %q, want the resolved home", name, fn["description"])
		}
		parameters, _ := fn["parameters"].(map[string]any)
		properties, _ := parameters["properties"].(map[string]any)
		path, _ := properties["path"].(map[string]any)
		if !strings.Contains(path["description"].(string), home+"/skills") {
			t.Fatalf("%s: nested description = %q, want the resolved home", name, path["description"])
		}
	}
}

// TestResolveHivePathsDeepLeavesKeysAndNonStringsAlone covers the deep walker
// directly: Python's dict comprehension rewrites values only, so a key that
// contains the literal must not change, and non-string scalars pass through.
//
// Parity: core/framework/config.py::resolve_hive_paths_deep.
func TestResolveHivePathsDeepLeavesKeysAndNonStringsAlone(t *testing.T) {
	const home = "/hive/home"
	value := map[string]any{
		"~/.hive/key": "~/.hive/value",
		"nil":         nil,
		"bool":        true,
		"number":      float64(1.5),
		"list":        []any{"~/.hive/a", float64(2)},
		"stringList":  []string{"~/.hive/b"},
		"stringMap":   map[string]string{"~/.hive/c": "~/.hive/d"},
	}

	rewritten, ok := resolveHivePathsDeep(value, home).(map[string]any)
	if !ok {
		t.Fatalf("resolveHivePathsDeep = %T", resolveHivePathsDeep(value, home))
	}
	if _, ok := rewritten["~/.hive/key"]; !ok {
		t.Fatalf("map key was rewritten: %#v", rewritten)
	}
	if rewritten["~/.hive/key"] != home+"/value" {
		t.Fatalf("value = %v", rewritten["~/.hive/key"])
	}
	if rewritten["nil"] != nil || rewritten["bool"] != true || rewritten["number"] != float64(1.5) {
		t.Fatalf("scalars = nil:%v bool:%v number:%v", rewritten["nil"], rewritten["bool"], rewritten["number"])
	}
	if !reflect.DeepEqual(rewritten["list"], []any{home + "/a", float64(2)}) {
		t.Fatalf("list = %#v", rewritten["list"])
	}
	if !reflect.DeepEqual(rewritten["stringList"], []string{home + "/b"}) {
		t.Fatalf("stringList = %#v", rewritten["stringList"])
	}
	if !reflect.DeepEqual(rewritten["stringMap"], map[string]string{"~/.hive/c": home + "/d"}) {
		t.Fatalf("stringMap = %#v", rewritten["stringMap"])
	}
}
