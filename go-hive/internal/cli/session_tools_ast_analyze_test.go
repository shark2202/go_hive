package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

func writeAstAnalyzeFixture(t *testing.T, root string) {
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

func TestSessionCatalogueAstAnalyzeLazyTier(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeAstAnalyzeFixture(t, sessionDir)

	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	defer func() { _ = catalogue.Close() }()

	def, ok := codeIntelDef(t, catalogue, builtin.ToolAstAnalyze)
	if !ok {
		t.Fatal("ast_analyze definition missing")
	}
	if flatSurfaceGroupedModule(def) {
		t.Fatal("ast_analyze was misclassified as a grouped module")
	}
	if names := defNames(catalogue.ModelDefs()); flatContainsName(names, builtin.ToolAstAnalyze) {
		t.Fatal("ast_analyze leaked into the frozen default surface")
	}
	if lazy := defNames(catalogue.LazyModelDefs()); !flatContainsName(lazy, builtin.ToolAstAnalyze) {
		t.Fatalf("ast_analyze missing from the lazy layer: %v", lazy)
	}

	content, isError, err := catalogue.Execute(context.Background(), builtin.ToolAstAnalyze, map[string]any{
		"path":   sessionDir,
		"detail": "summary",
	})
	if err != nil || isError {
		t.Fatalf("execute: err=%v content=%s", err, content)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(content), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	summary, _ := report["summary"].(map[string]any)
	if summary["units"].(float64) != 2 {
		t.Fatalf("units = %v", summary["units"])
	}
}

func TestSessionCatalogueAstAnalyzeConfigGate(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeAstAnalyzeFixture(t, sessionDir)
	userConfig := filepath.Join(homeRoot, "ast_analyze.json")
	sessionConfig := filepath.Join(sessionDir, ".hive", "ast_analyze.json")

	hasTool := func(catalogue *sessionToolCatalogue) bool {
		_, ok := codeIntelDef(t, catalogue, builtin.ToolAstAnalyze)
		return ok
	}
	newCatalogue := func() *sessionToolCatalogue {
		return newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	}

	on := newCatalogue()
	if !hasTool(on) {
		t.Fatal("default should enable ast_analyze")
	}
	_ = on.Close()

	if err := os.WriteFile(userConfig, []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	off := newCatalogue()
	if hasTool(off) {
		t.Fatal("enabled=false did not disable ast_analyze")
	}
	_ = off.Close()

	_ = os.Remove(userConfig)
	if err := os.MkdirAll(filepath.Dir(sessionConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionConfig, []byte(`{"enabled":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ignored := newCatalogue()
	if !hasTool(ignored) {
		t.Fatal("session-dir config disabled ast_analyze; only $HIVE_HOME may gate it")
	}
	_ = ignored.Close()

	t.Setenv("HIVE_AST_ANALYZE", "0")
	killed := newCatalogue()
	if hasTool(killed) {
		t.Fatal("HIVE_AST_ANALYZE=0 did not disable ast_analyze")
	}
	_ = killed.Close()
}

func TestSessionCatalogueAstAnalyzeWithRoot(t *testing.T) {
	t.Setenv(ToolSurfaceEnvVar, "")
	sessionDir := testTempDir(t)
	homeRoot := testTempDir(t)
	home := hivehome.Home{Root: homeRoot}
	writeAstAnalyzeFixture(t, sessionDir)
	workerDir := testTempDir(t)
	writeAstAnalyzeFixture(t, workerDir)

	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), home)
	defer func() { _ = catalogue.Close() }()
	worker := catalogue.WithRoot(workerDir)
	defer func() { _ = worker.Close() }()

	if _, ok := codeIntelDef(t, worker, builtin.ToolAstAnalyze); !ok {
		t.Fatal("ast_analyze missing from the worker catalogue")
	}
	content, isError, err := worker.Execute(context.Background(), builtin.ToolAstAnalyze, map[string]any{
		"path": workerDir,
	})
	if err != nil || isError {
		t.Fatalf("worker execute: err=%v content=%s", err, content)
	}
}
