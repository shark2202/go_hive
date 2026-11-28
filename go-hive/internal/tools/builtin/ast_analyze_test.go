package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAstFixture(t *testing.T, root string) {
	t.Helper()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo 1.26\n")
	write("main.go", "package main\n\nimport \"example.com/app/lib\"\n\nfunc main() { lib.Run() }\n")
	write("lib/lib.go", "package lib\n\nfunc Run() {}\n")
}

func TestAstAnalyzeToolDefinition(t *testing.T) {
	def := AstAnalyzeToolDef()
	if def.Name != ToolAstAnalyze {
		t.Fatalf("name = %q", def.Name)
	}
	if def.ConcurrencySafe {
		t.Fatal("ast_analyze is CPU-heavy and must not be concurrency-safe")
	}
	props, _ := def.Parameters["properties"].(map[string]any)
	pathSchema, _ := props["path"].(map[string]any)
	if !strings.Contains(pathSchema["description"].(string), "Absolute") {
		t.Fatalf("path description = %q", pathSchema["description"])
	}
}

func TestAstAnalyzeHandlerExecute(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeAstFixture(t, root)
	tool := NewAstAnalyzeToolbox(root, home)

	result, err := tool.Execute(context.Background(), map[string]any{
		"path":   root,
		"detail": "units",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(result.Content), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, result.Content)
	}
	summary, _ := report["summary"].(map[string]any)
	if summary["units"].(float64) != 2 {
		t.Fatalf("units = %v", summary["units"])
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	writeTo := filepath.Join(realRoot, "report.json")
	result, err = tool.Execute(context.Background(), map[string]any{
		"path":     root,
		"detail":   "full",
		"write_to": writeTo,
	})
	if err != nil || result.IsError {
		t.Fatalf("write_to: err=%v content=%s", err, result.Content)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(result.Content), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["saved_to"] != writeTo {
		t.Fatalf("saved_to = %v", meta["saved_to"])
	}
	if _, err := os.Stat(writeTo); err != nil {
		t.Fatalf("report not written: %v", err)
	}

	result, err = tool.Execute(context.Background(), map[string]any{"path": "main.go"})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "path must be absolute") {
		t.Fatalf("relative path result = %+v / %v", result, err)
	}

	result, err = tool.Execute(context.Background(), map[string]any{"path": root, "format": "markdown"})
	if err != nil || result.IsError || !strings.HasPrefix(result.Content, "# AST analysis") {
		t.Fatalf("markdown = %+v / %v", result, err)
	}
}

func TestAstAnalyzeConfigGate(t *testing.T) {
	home := t.TempDir()
	if !AstAnalyzeEnabled(home) {
		t.Fatal("default should be enabled")
	}
	if err := os.WriteFile(filepath.Join(home, "ast_analyze.json"), []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if AstAnalyzeEnabled(home) {
		t.Fatal("enabled=false did not disable")
	}
	tool := NewAstAnalyzeToolbox(t.TempDir(), home)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "disabled") {
		t.Fatalf("disabled result = %+v / %v", result, err)
	}
}
