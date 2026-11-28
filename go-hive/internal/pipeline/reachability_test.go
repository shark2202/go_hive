package pipeline

// Production-reachability guards for the trigger middleware.
//
// Upstream's ColonyRuntime.trigger is an embedding/test surface: its only
// callers are trigger_and_wait (core/framework/host/colony_runtime.py:2425)
// and the colony timer loop (:2904), whose trigger registry (register_trigger,
// :2823) has no caller in the checkout. The production trigger path injects
// the queen node instead (core/framework/server/routes_sessions.py:1527).
// The Go port keeps the same shape: ColonyRuntime.Trigger is API parity, not a
// production entry point.
//
// If a production caller is ever wired, these tests fail on purpose so the
// status in docs/pipeline.md §7 (and the parity registry) is updated in the
// same change instead of silently claiming full alignment.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// pipelineModuleRoot resolves the go-hive module root from this test file.
func pipelineModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// Parity-exempt: Go-only production-reachability guard; upstream has no
// equivalent test (the upstream evidence is the call graph cited in
// docs/pipeline.md §7).
func TestTriggerPipelineHasNoProductionCaller(t *testing.T) {
	root := pipelineModuleRoot(t)
	var offenders []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "frontend", "testdata", "dist":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", rel, parseErr)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "Trigger":
				offenders = append(offenders, fmt.Sprintf("%s:%d .Trigger(...)", rel, fset.Position(call.Pos()).Line))
			case "Run":
				receiver, isIdent := selector.X.(*ast.Ident)
				if isIdent && receiver.Name == "triggerPipeline" && rel != "internal/colony/trigger.go" {
					offenders = append(offenders, fmt.Sprintf("%s:%d triggerPipeline.Run(...)", rel, fset.Position(call.Pos()).Line))
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("scan Go tree: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("production caller(s) of the trigger pipeline appeared:\n  %s\n"+
			"Update docs/pipeline.md §7 (production reachability) and the parity registry, then adjust this guard.",
			strings.Join(offenders, "\n  "))
	}
}

// Parity-exempt: Go-only documentation guard; it keeps the out_of_scope
// reachability status and its upstream anchors from being dropped.
func TestPipelineReachabilityStatusDocumented(t *testing.T) {
	root := pipelineModuleRoot(t)
	docPath := filepath.Join(root, "..", "docs", "pipeline.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	doc := string(raw)

	required := []string{
		"## 7. 生产可达性",
		"out_of_scope",
		"core/framework/host/colony_runtime.py:2376",
		"core/framework/host/colony_runtime.py:2904",
		"core/framework/server/routes_sessions.py:1527",
		"internal/server/routes_session_triggers.go:367",
	}
	for _, want := range required {
		if !strings.Contains(doc, want) {
			t.Fatalf("docs/pipeline.md is missing %q; the production-reachability status must stay documented", want)
		}
	}

	foundRow := false
	for _, line := range strings.Split(doc, "\n") {
		if !strings.Contains(line, "host/colony_runtime.py::trigger`") {
			continue
		}
		foundRow = true
		if !strings.Contains(line, "partial") {
			t.Fatalf("docs/pipeline.md claims full alignment for ColonyRuntime.trigger without the partial/reachability caveat:\n%s", line)
		}
	}
	if !foundRow {
		t.Fatal("docs/pipeline.md has no ColonyRuntime.trigger comparison row")
	}
}
