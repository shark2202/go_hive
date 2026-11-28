package cli

// Regression guard for the chat fileops sandbox root: a live session's
// read_file/write_file/edit_file/search_files must resolve relative paths in
// the session directory, the same root ast_grep and lsp use. Before this
// guard the chat toolbox was built with an empty root, so file tools silently
// fell back to the serve process working directory while the code-intelligence
// adapters used the session directory.

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// unexportedStringField reads an unexported string field off a pointer value.
// Builtin adapters keep their root private; the test observes it instead of
// widening the production API for a single assertion.
func unexportedStringField(t *testing.T, value any, field string) string {
	t.Helper()
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		t.Fatalf("%T: want a non-nil pointer", value)
	}
	f := rv.Elem().FieldByName(field)
	if !f.IsValid() || f.Kind() != reflect.String {
		t.Fatalf("%T has no string field %q", value, field)
	}
	return f.String()
}

// TestChatSessionFileopsRootIsSessionDir builds the same catalogue the chat
// and preloaded-session loaders build and proves the four file tools resolve
// relative paths under the session directory while the code-intelligence
// adapters keep the same root.
//
// Parity-exempt: Go-only regression guard for rooting chat fileops at the
// session directory; no Python unit counterpart.
func TestChatSessionFileopsRootIsSessionDir(t *testing.T) {
	homeRoot := t.TempDir()
	sessionDir := filepath.Join(homeRoot, "queens", "default", "sessions", "sess-fileops")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// Mirrors the loader: builtin.New(sessionDir) feeding
	// newSessionToolCatalogue(sessionDir, ...). The ast_grep adapter is
	// opt-in, so enable its gate in the explicit HIVE_HOME root.
	if err := os.WriteFile(filepath.Join(homeRoot, "ast_grep.json"), []byte(`{"enabled":true}`), 0o600); err != nil {
		t.Fatalf("write ast_grep.json: %v", err)
	}
	toolbox := builtin.New(sessionDir)
	catalogue := newSessionToolCatalogue(sessionDir, toolbox, hivehome.Home{Root: homeRoot})
	ctx := context.Background()

	// A relative write anchors to sessionDir.
	const relPath = "notes/relative.txt"
	content, isError, err := catalogue.Execute(ctx, builtin.ToolWriteFile, map[string]any{
		"path":    relPath,
		"content": "chat fileops\n",
	})
	if err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if isError {
		t.Fatalf("write_file failed: %s", content)
	}
	onDisk := filepath.Join(sessionDir, "notes", "relative.txt")
	data, err := os.ReadFile(onDisk)
	if err != nil {
		t.Fatalf("relative write did not land under the session dir %s: %v", sessionDir, err)
	}
	if string(data) != "chat fileops\n" {
		t.Fatalf("on-disk content = %q, want %q", data, "chat fileops\n")
	}

	// The same relative path reads back through read_file.
	read, isError, err := catalogue.Execute(ctx, builtin.ToolReadFile, map[string]any{"path": relPath})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	if isError {
		t.Fatalf("read_file failed: %s", read)
	}
	if !strings.Contains(read, "chat fileops") {
		t.Fatalf("read_file = %q, want the content written under sessionDir", read)
	}

	// An absolute path outside the root is refused by the Go sandbox. The
	// denial is plain text (file_ops.py parity), not an is_error result.
	outside := t.TempDir()
	escapePath := filepath.Join(outside, "escape.txt")
	content, _, err = catalogue.Execute(ctx, builtin.ToolWriteFile, map[string]any{
		"path":    escapePath,
		"content": "outside the sandbox",
	})
	if err != nil {
		t.Fatalf("write_file outside root: %v", err)
	}
	if !strings.Contains(content, builtin.ErrPathEscape.Error()) {
		t.Fatalf("write_file outside root = %q, want the sandbox escape error %q", content, builtin.ErrPathEscape)
	}
	if _, statErr := os.Stat(escapePath); !os.IsNotExist(statErr) {
		t.Fatalf("write outside root created %s (stat err %v)", escapePath, statErr)
	}

	// ast_grep, lsp and the file tools share one root: the session directory.
	if got := catalogue.root; got != sessionDir {
		t.Fatalf("catalogue root = %q, want sessionDir %q", got, sessionDir)
	}
	if catalogue.astGrepTool == nil {
		t.Fatal("ast_grep adapter was not built")
	}
	if got := unexportedStringField(t, catalogue.astGrepTool, "root"); got != sessionDir {
		t.Fatalf("ast_grep root = %q, want sessionDir %q", got, sessionDir)
	}
	if catalogue.lspToolbox != nil && catalogue.lspToolbox.Root != sessionDir {
		t.Fatalf("lsp root = %q, want sessionDir %q", catalogue.lspToolbox.Root, sessionDir)
	}
}

// TestChatLoadersRootFileopsToolboxAtSessionDir guards the production call
// sites: both the created-session loader and the preloaded-session runtime
// must pass sessionDir, never an empty root, to builtin.New.
//
// Parity-exempt: Go-only source guard for the loader call sites' fileops root;
// no Python unit counterpart.
func TestChatLoadersRootFileopsToolboxAtSessionDir(t *testing.T) {
	for _, name := range []string{"loader.go", "loader_preloaded_session.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		var roots []string
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "New" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "builtin" {
				return true
			}
			if len(call.Args) != 1 {
				t.Fatalf("%s: builtin.New got %d args, want 1", name, len(call.Args))
			}
			ident, ok := call.Args[0].(*ast.Ident)
			if !ok {
				t.Fatalf("%s: builtin.New arg is %T, want the sessionDir identifier", name, call.Args[0])
			}
			roots = append(roots, ident.Name)
			return true
		})
		if len(roots) != 1 {
			t.Fatalf("%s: found %d builtin.New calls, want 1", name, len(roots))
		}
		if roots[0] != "sessionDir" {
			t.Fatalf("%s: builtin.New(%s), want builtin.New(sessionDir)", name, roots[0])
		}
	}
}
