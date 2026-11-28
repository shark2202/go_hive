package builtin

// Parity: A1 tool definition golden and adapter input contract. The schema is
// frozen in go_hive/docs/ast-grep-design.json; this lane deliberately does not
// register the tool in registry.go, so the golden pins the standalone def.

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Parity-exempt: pins the frozen JSON schema in docs/ast-grep-design.json;
// ast_grep is a post-1.0 Go-only tool with no Python adapter.
func TestAstGrepToolDefinitionGolden(t *testing.T) {
	def := AstGrepToolDef()
	if def.Name != "ast_grep" {
		t.Fatalf("name = %q, want ast_grep", def.Name)
	}
	if def.Description != AstGrepDescription {
		t.Fatalf("description drift:\n%q", def.Description)
	}
	want := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"pat": map[string]any{
				"type":        "string",
				"description": "Single structural pattern using $NAME, $_, $$$NAME or $$$.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute file, directory, glob or semicolon-separated list inside the workspace root; defaults to the workspace root. Relative paths are rejected.",
			},
			"lang": map[string]any{
				"type":        "string",
				"description": "Optional canonical language override.",
			},
			"skip": map[string]any{
				"type":        "integer",
				"minimum":     0,
				"description": "Match offset; defaults to 0.",
			},
		},
		"required": []string{"pat"},
	}
	if !reflect.DeepEqual(def.Parameters, want) {
		t.Fatalf("parameter schema drift:\ngot  %#v\nwant %#v", def.Parameters, want)
	}
	properties, ok := def.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties is not a map")
	}
	if _, hasLimit := properties["limit"]; hasLimit {
		t.Fatal("limit must not be an input field in v1")
	}
	if !def.ConcurrencySafe {
		t.Fatal("read-only ast_grep should be concurrency-safe")
	}
	if got := (&AstGrepTool{}).Def(); !reflect.DeepEqual(got, def) {
		t.Fatal("(*AstGrepTool).Def drifted from AstGrepToolDef")
	}
}

func writeBuiltinTempFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

func writeGoWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	content := "package main\n\nfunc main() {\n	println(\"hi\")\n	println(\"bye\")\n}\n"
	writeBuiltinTempFile(t, root, "main.go", content)
	return root
}

// Parity-exempt: AstGrepTool handler behavior is Go-only; no Python tool adapter exists.
func TestAstGrepHandlerSearch(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Run(context.Background(), map[string]any{
		"pat":  "println($A)",
		"path": filepath.Join(root, "main.go"),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", result.Content)
	}
	want := "main.go:\n*4:println(\"hi\")\n  meta: A=\"hi\"\n*5:println(\"bye\")\n  meta: A=\"bye\""
	if result.Content != want {
		t.Fatalf("content mismatch\n--- got ---\n%s\n--- want ---\n%s", result.Content, want)
	}
	if result.Details["matchCount"] != 2 || result.Details["fileCount"] != 1 {
		t.Fatalf("details = %+v", result.Details)
	}
	if result.ToolName != ToolAstGrep {
		t.Fatalf("toolName = %q", result.ToolName)
	}
}

// Parity-exempt: path defaulting is the Go handler contract; no Python tool adapter.
func TestAstGrepHandlerDefaultsPathToRoot(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Run(context.Background(), map[string]any{"pat": "println($A)"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if result.Details["scopePath"] != "." || result.Details["searchPath"] != "." {
		t.Fatalf("defaults scope/search = %+v", result.Details)
	}
}

// Parity-exempt: skip argument handling is Go-only; no Python tool adapter.
func TestAstGrepHandlerSkip(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Run(context.Background(), map[string]any{"pat": "println($A)", "skip": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(result.Content, "*") != 1 {
		t.Fatalf("skip=1 should return one match:\n%s", result.Content)
	}
	if result.Details["matchCount"] != 1 {
		t.Fatalf("details = %+v", result.Details)
	}
}

// Parity-exempt: Go tool argument validation messages; no Python tool adapter.
func TestAstGrepHandlerValidation(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"missing pat", map[string]any{}, "pat is required"},
		{"empty pat", map[string]any{"pat": "  "}, "pat is required"},
		{"non-string pat", map[string]any{"pat": 3}, "pat is required"},
		{"non-string path", map[string]any{"pat": "println($A)", "path": 3}, "path must be a string"},
		{"non-string lang", map[string]any{"pat": "println($A)", "lang": 3}, "lang must be a string"},
		{"negative skip", map[string]any{"pat": "println($A)", "skip": float64(-1)}, "skip must be a non-negative integer"},
		{"fractional skip", map[string]any{"pat": "println($A)", "skip": 1.5}, "skip must be a non-negative integer"},
		{"infinite skip", map[string]any{"pat": "println($A)", "skip": math.Inf(1)}, "skip must be a non-negative integer"},
		{"relative path", map[string]any{"pat": "println($A)", "path": "main.go"}, "path must be absolute"},
		{"bad semicolon", map[string]any{"pat": "println($A)", "path": filepath.Join(root, "main.go") + ";;"}, "empty entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tool.Run(context.Background(), tc.input)
			if err != nil {
				t.Fatalf("Run returned infrastructure error: %v", err)
			}
			if !result.IsError {
				t.Fatalf("IsError = false for %s", tc.name)
			}
			if !strings.HasPrefix(result.Content, "Error: ") || !strings.Contains(result.Content, tc.want) {
				t.Fatalf("content = %q, want prefix Error: and %q", result.Content, tc.want)
			}
		})
	}
}

// Parity-exempt: the Go handler error and no-match contract; no Python tool adapter.
func TestAstGrepHandlerUnsupportedAndNoMatch(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Run(context.Background(), map[string]any{"pat": "println($A)", "lang": "klingon"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "unsupported language") {
		t.Fatalf("unsupported language result = %+v", result)
	}
	result, err = tool.Run(context.Background(), map[string]any{"pat": "println()"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("no-match should not be an error: %+v", result)
	}
	if result.Content != "No matches found" {
		t.Fatalf("content = %q", result.Content)
	}
}

// Parity-exempt: Go handler pattern-error text; no Python tool adapter.
func TestAstGrepHandlerInvalidPattern(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Run(context.Background(), map[string]any{"pat": "func (", "lang": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content, "pattern does not parse") {
		t.Fatalf("invalid pattern result = %+v", result)
	}
}

// Parity-exempt: builtin.Tool Execute adapter is the Go tool interface; no Python counterpart.
func TestAstGrepExecuteAdapter(t *testing.T) {
	root := writeGoWorkspace(t)
	tool := NewAstGrepTool(root)
	result, err := tool.Execute(context.Background(), map[string]any{"pat": "println($A)"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError || !strings.Contains(result.Content, "println") {
		t.Fatalf("builtin.Result = %+v", result)
	}
	result, err = tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("empty pat should be a tool error: %+v", result)
	}
}

// Parity-exempt: zero-value tool falling back to os.Getwd is Go struct semantics;
// no Python analogue.
func TestAstGrepZeroValueUsesWorkingDirectory(t *testing.T) {
	cwd := t.TempDir()
	writeBuiltinTempFile(t, cwd, "main.go", "package main\n\nfunc main() { println(\"cwd\") }\n")
	t.Chdir(cwd)
	tool := &AstGrepTool{}
	result, err := tool.Run(context.Background(), map[string]any{"pat": "println($A)", "path": filepath.Join(cwd, "main.go"), "lang": "go"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("zero-value tool error: %s", result.Content)
	}
	if result.Details["filesSearched"] != 1 || result.Details["matchCount"] != 1 {
		t.Fatalf("zero-value tool details = %+v", result.Details)
	}
}
