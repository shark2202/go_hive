package cli

// Assembly guard: every exported batch installer under internal/tools/
// integrations must be referenced from the session tool table. A batch that
// ports cleanly but never reaches a session is invisible to the model, and the
// port's own tests cannot notice the omission — only a wiring-level check can.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// installerNamePrefixes are the exported entry-point shapes the port uses:
// InstallBatch7Lane3, InstallBatch3Doc, and the legacy Batch7Lane5Install.
var installerNamePrefixes = []string{"Install", "Batch"}

// parsePackage parses one directory's non-test files and returns its file set.
func parsePackage(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("parse %s: got %d packages, want 1", dir, len(pkgs))
	}
	for _, pkg := range pkgs {
		return pkg.Files
	}
	return nil
}

// integrationInstallerNames returns every exported top-level function in the
// integrations package whose name matches an installer shape, sorted.
func integrationInstallerNames(t *testing.T) []string {
	t.Helper()
	files := parsePackage(t, filepath.Join("..", "tools", "integrations"))

	var names []string
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name == nil {
				continue
			}
			name := fn.Name.Name
			for _, prefix := range installerNamePrefixes {
				if strings.HasPrefix(name, prefix) {
					names = append(names, name)
					break
				}
			}
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("found no installers: the parser or the naming convention changed")
	}
	return names
}

// referencedIdentifiers returns the identifiers internal/cli mentions, counting
// both selector names (integrations.InstallBatch2) and plain identifiers (dot
// imports).
func referencedIdentifiers(t *testing.T) map[string]struct{} {
	t.Helper()
	referenced := map[string]struct{}{}
	for _, file := range parsePackage(t, ".") {
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.SelectorExpr:
				referenced[typed.Sel.Name] = struct{}{}
			case *ast.Ident:
				referenced[typed.Name] = struct{}{}
			}
			return true
		})
	}
	return referenced
}

// funcName returns the bare name of a bound function value, e.g.
// "InstallBatch2" for integrations.InstallBatch2.
func funcName(fn func(*tools.ToolRegistry) int) string {
	target := runtime.FuncForPC(reflect.ValueOf(fn).Pointer())
	if target == nil {
		return ""
	}
	name := target.Name()
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		name = name[idx+1:]
	}
	return strings.TrimSuffix(name, "-fm")
}

// TestEveryIntegrationInstallerIsWired fails when an exported installer exists
// but no non-test file in internal/cli mentions it.
// Parity-exempt: Go 特有的接线守护测试，上游无对应物
func TestEveryIntegrationInstallerIsWired(t *testing.T) {
	installers := integrationInstallerNames(t)
	referenced := referencedIdentifiers(t)

	var missing []string
	for _, name := range installers {
		if _, ok := referenced[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("ported installers never wired into internal/cli: %s\n"+
			"add each one to sessionIntegrationInstallers in session_tools.go",
			strings.Join(missing, ", "))
	}
}

// TestSessionInstallerTableIsComplete fails when the installer table misses an
// entry, so a mere mention elsewhere in the package cannot satisfy the guard.
// Parity-exempt: Go 特有的接线守护测试，上游无对应物
func TestSessionInstallerTableIsComplete(t *testing.T) {
	installers := integrationInstallerNames(t)

	labels := map[string]struct{}{}
	for _, entry := range sessionIntegrationInstallers {
		if entry.name == "" {
			t.Fatal("sessionIntegrationInstallers has an empty label")
		}
		if _, seen := labels[entry.name]; seen {
			t.Fatalf("duplicate installer label %q", entry.name)
		}
		labels[entry.name] = struct{}{}
	}

	bound := map[string]struct{}{}
	for _, entry := range sessionIntegrationInstallers {
		if entry.install == nil {
			t.Fatalf("installer %q has a nil function", entry.name)
		}
		name := funcName(entry.install)
		if name == "" {
			t.Fatalf("installer %q has no resolvable function name", entry.name)
		}
		bound[name] = struct{}{}
	}

	var missing []string
	for _, name := range installers {
		if _, ok := bound[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("sessionIntegrationInstallers misses %s", strings.Join(missing, ", "))
	}
	if len(bound) != len(installers) {
		t.Fatalf("table binds %d installers, package exports %d; check for a duplicate binding", len(bound), len(installers))
	}
}
