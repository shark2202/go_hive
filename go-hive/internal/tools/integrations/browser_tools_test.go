package integrations

// browser_tools_test.go pins the definition layer this lane adds: the 22
// active gcu browser tools are generated from the live Python FastMCP
// registration (go_hive/scripts/gen_browser_tool_defs.py) and installed into
// the shared registry with those exact name/description/input_schema values.
//
// The artifact's per-tool sha256 is recomputed here from the definitions the
// registry actually serves, so a hand edit to the JSON (or a drift in the Go
// decoder) fails on a digest — not on a human eyeball.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/tools"
)

// canonicalDefinitionJSON mirrors the generator's sha_rule: {name, description,
// input_schema} as sorted-key compact JSON with UTF-8 kept raw and integral
// floats canonicalised to ints (pydantic writes 60.0, Go decodes float64(60)).
func canonicalDefinitionJSON(t *testing.T, name, description string, schema map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"name":         name,
		"description":  description,
		"input_schema": normalizeIntegralFloats(schema),
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		t.Fatalf("%s: encode canonical definition: %v", name, err)
	}
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// normalizeIntegralFloats rewrites whole float64 values as int64 so the encoded
// bytes match the Python generator's digest rule.
func normalizeIntegralFloats(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeIntegralFloats(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeIntegralFloats(item)
		}
		return out
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	default:
		return value
	}
}

func definitionSHA(t *testing.T, name, description string, schema map[string]any) string {
	t.Helper()
	sum := sha256.Sum256(canonicalDefinitionJSON(t, name, description, schema))
	return hex.EncodeToString(sum[:])
}

// Parity: tools/src/gcu/browser/tools/__init__.py::register_tools (the 22
// active tools) + tools/*.py::BROWSER_*_DOC descriptions.
func TestBrowserToolDefinitionGolden(t *testing.T) {
	defs := BrowserToolDefinitions()
	if len(defs) != 22 {
		t.Fatalf("golden tools = %d, want 22", len(defs))
	}
	names := make([]string, 0, len(defs))
	matches := 0
	for _, def := range defs {
		names = append(names, def.Name)
		got := definitionSHA(t, def.Name, def.Description, def.InputSchema)
		if got != def.SHA256 {
			t.Errorf("%s: sha256 = %s, golden says %s", def.Name, got, def.SHA256)
			continue
		}
		matches++
	}
	if matches != len(defs) {
		t.Fatalf("definition sha matches = %d/%d", matches, len(defs))
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != strings.Join(expectedBrowserToolNames, ",") {
		t.Fatalf("names = %v, want %v", sorted, expectedBrowserToolNames)
	}
	t.Logf("browser tool definitions: %d/%d name+description+schema sha matches", matches, len(defs))
}

var expectedBrowserToolNames = []string{
	"browser_activate_tab", "browser_close", "browser_console", "browser_dialog_respond",
	"browser_evaluate", "browser_get_text", "browser_html", "browser_interact",
	"browser_navigate", "browser_open", "browser_reload", "browser_resize",
	"browser_screenshot", "browser_script", "browser_select", "browser_setup",
	"browser_shadow_query", "browser_snapshot", "browser_status", "browser_stop",
	"browser_tabs", "browser_upload",
}

// Parity: tools/src/gcu/browser/tools/lifecycle.py::register_lifecycle_tools …
// script.py::register_script_tools (one registry tool per Python tool, same
// definition bytes).
func TestInstallBrowserToolsServesGoldenDefinitions(t *testing.T) {
	registry := tools.NewToolRegistry()
	registered := InstallBrowserTools(registry)
	if registered != 22 {
		t.Fatalf("registered = %d, want 22", registered)
	}
	for _, def := range BrowserToolDefinitions() {
		tool, ok := registry.GetTool(def.Name)
		if !ok {
			t.Fatalf("%s: not registered", def.Name)
		}
		if tool.Name != def.Name || tool.Description != def.Description {
			t.Fatalf("%s: name/description drifted", def.Name)
		}
		gotSHA := definitionSHA(t, tool.Name, tool.Description, tool.Parameters)
		if gotSHA != def.SHA256 {
			t.Fatalf("%s: served definition sha = %s, golden says %s", def.Name, gotSHA, def.SHA256)
		}
	}
	if members := registry.GetServerToolNames(BrowserToolGroup); len(members) != 22 {
		t.Fatalf("browser group members = %d, want 22: %v", len(members), members)
	}
}

// Parity-exempt: assembly precedence. The session installs browser_setup first
// (the core/framework discovery tool that is part of the eager default
// surface); InstallBrowserTools must not replace it with the gcu copy of the
// same name.
func TestInstallBrowserToolsKeepsFrameworkBrowserSetup(t *testing.T) {
	registry := tools.NewToolRegistry()
	if got := InstallBrowserSetup(registry); got != 1 {
		t.Fatalf("browser_setup install = %d", got)
	}
	InstallBrowserTools(registry)
	tool, ok := registry.GetTool(BrowserSetupToolName)
	if !ok {
		t.Fatal("browser_setup missing")
	}
	if tool.Description != BrowserSetupDescription {
		t.Fatalf("browser_setup definition was replaced by the gcu copy: %q", tool.Description)
	}
	names := registry.RegisteredNames()
	have := map[string]struct{}{}
	for _, name := range names {
		have[name] = struct{}{}
	}
	for _, want := range expectedBrowserToolNames {
		if _, ok := have[want]; !ok {
			t.Fatalf("browser tool %q missing from the registry", want)
		}
	}
	if len(have) != 22 {
		t.Fatalf("browser registry names = %d, want 22", len(have))
	}
}
