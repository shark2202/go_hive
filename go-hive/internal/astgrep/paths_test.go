package astgrep

// Parity: A7 path safety and glob semantics. Workspace-root containment,
// symlink evaluation, semicolon lists and binary/oversize skipping are Go
// implementation guarantees with no upstream Python test to align to.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, root, rel, content string) string {
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

// Parity-exempt: semicolon path-list parsing is a Go tool contract; no Python counterpart.
func TestSplitPathEntries(t *testing.T) {
	cases := []struct {
		input   string
		want    []string
		wantErr bool
	}{
		{"", []string{"."}, false},
		{"   ", []string{"."}, false},
		{"go", []string{"go"}, false},
		{" go ; python ", []string{"go", "python"}, false},
		{"a;b;c", []string{"a", "b", "c"}, false},
		{"a;;b", nil, true},
		{"a; ;b", nil, true},
		{";", nil, true},
		{"a\x00b", nil, true},
	}
	for _, tc := range cases {
		got, err := splitPathEntries(tc.input)
		if tc.wantErr {
			if !IsKind(err, ErrInvalidInput) {
				t.Errorf("splitPathEntries(%q) error = %v, want ErrInvalidInput", tc.input, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitPathEntries(%q): %v", tc.input, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("splitPathEntries(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitPathEntries(%q)[%d] = %q, want %q", tc.input, i, got[i], tc.want[i])
			}
		}
	}
}

// Parity-exempt: scope expansion over the Go testdata workspace; no Python test target.
func TestExpandScopeSemicolonList(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := expandScope(root, "go; python")
	if err != nil {
		t.Fatalf("expandScope: %v", err)
	}
	if scope.display != "go; python" {
		t.Fatalf("display = %q, want go; python", scope.display)
	}
	var sawGo, sawPython bool
	for _, file := range scope.candidates {
		if strings.HasPrefix(file.display, "go/") {
			sawGo = true
		}
		if strings.HasPrefix(file.display, "python/") {
			sawPython = true
		}
	}
	if !sawGo || !sawPython {
		t.Fatalf("candidates = %+v, want go/ and python/", scope.candidates)
	}
}

// Parity-exempt: glob expansion is implemented only by this Go package; no Python counterpart.
func TestExpandScopeGlobs(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pattern string
		want    []string
	}{
		{"go/*.go", []string{"go/check.go", "go/main.go"}},
		{"**/*.go", []string{"go/check.go", "go/main.go"}},
		{"python/*.py", []string{"python/main.py"}},
		{"markdown/*.md", []string{"markdown/README.md"}},
	} {
		scope, err := expandScope(root, tc.pattern)
		if err != nil {
			t.Fatalf("expandScope(%q): %v", tc.pattern, err)
		}
		var got []string
		for _, file := range scope.candidates {
			got = append(got, file.display)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("expandScope(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

// Parity-exempt: matchGlob is an unexported Go helper for ** and character classes;
// no Python equivalent.
func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, target string
		want            bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "nested/main.go", false},
		{"**/*.go", "nested/main.go", true},
		{"**/*.go", "main.go", true},
		{"src/**", "src/a/b.go", true},
		{"src/*", "src/a/b.go", false},
		{"src/?.go", "src/a.go", true},
		{"src/[ab].go", "src/a.go", true},
		{"src/[ab].go", "src/c.go", false},
		{"src/[", "src/a.go", false},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pattern, tc.target); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}

// Parity-exempt: workspace-root containment is enforced by the Go Search API;
// no Python counterpart.
func TestSearchPathTraversal(t *testing.T) {
	root := t.TempDir()
	inside := writeTempFile(t, root, "inside.go", "package main\nfunc inside() {}\n")
	_ = inside
	outsideDir := t.TempDir()
	outside := writeTempFile(t, outsideDir, "outside.go", "package main\nfunc outside() {}\n")
	for _, pathArg := range []string{"../outside.go", filepath.ToSlash(filepath.Join("..", filepath.Base(outsideDir), "outside.go")), outside} {
		_, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: pathArg, Lang: "go"})
		if !IsKind(err, ErrPathEscape) {
			t.Errorf("Search(path=%q) error = %v, want ErrPathEscape", pathArg, err)
		}
	}
}

// Parity-exempt: symlink escape rejection is a Go path-safety guarantee; no Python counterpart.
func TestSearchSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := writeTempFile(t, outsideDir, "outside.go", "package main\nfunc outside() {}\n")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "link.go", Lang: "go"})
	if !IsKind(err, ErrPathEscape) {
		t.Fatalf("symlink escape error = %v, want ErrPathEscape", err)
	}
}

// Parity-exempt: resolving a symlink to its real display path is Go-only behavior;
// no Python counterpart.
func TestSearchSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	target := writeTempFile(t, root, "real.go", "package main\nfunc real() {}\n")
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "link.go", Lang: "go"})
	if err != nil {
		t.Fatalf("Search(link.go): %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(result.Matches))
	}
	if result.Matches[0].Path != "real.go" {
		t.Fatalf("display path = %q, want resolved real.go", result.Matches[0].Path)
	}
}

// Parity-exempt: directory-walk symlink skipping is a Go implementation guarantee;
// no Python counterpart.
func TestSearchWalkSkipsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outside := writeTempFile(t, outsideDir, "outside.go", "package main\nfunc outside() {}\n")
	link := filepath.Join(root, "escape.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeTempFile(t, root, "real.go", "package main\nfunc real() {}\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Lang: "go"})
	if err != nil {
		t.Fatalf("directory walk should skip symlink entries, got error: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "real.go" {
		t.Fatalf("matches = %+v, want only real.go", result.Matches)
	}
	if result.SkippedFiles < 1 {
		t.Fatalf("skippedFiles = %d, want >=1 for walked symlink", result.SkippedFiles)
	}
}

// Parity-exempt: failing the whole request on one bad semicolon entry is the Go contract;
// Python has no equivalent path-list API.
func TestSearchSemicolonOneBadEntryFails(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "a/one.go", "package a\nfunc one() {}\n")
	_, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "a; missing", Lang: "go"})
	if !IsKind(err, ErrInvalidInput) {
		t.Fatalf("bad semicolon entry error = %v, want ErrInvalidInput", err)
	}
}

// Parity-exempt: binary (NUL) and oversize skipping are Go scanner guarantees;
// no Python counterpart.
func TestSearchBinaryAndOversizeSkipped(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "text.go", "package main\nfunc good() {}\n")
	writeTempFile(t, root, "binary.go", "package main\n\x00func hidden() {}\n")
	writeTempFile(t, root, "large.go", "package main\nfunc large() {}\n"+strings.Repeat("// padding\n", 10))
	result, err := Search(context.Background(), Config{WorkspaceRoot: root, MaxFileBytes: 64}, Request{Pattern: "func $NAME() {}", Lang: "go"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "text.go" {
		t.Fatalf("matches = %+v, want only text.go", result.Matches)
	}
	if result.FilesSearched != 1 {
		t.Fatalf("filesSearched = %d, want 1", result.FilesSearched)
	}
	if result.SkippedFiles < 2 {
		t.Fatalf("skippedFiles = %d, want >= 2 (binary + oversize)", result.SkippedFiles)
	}
}

// Parity-exempt: exact-size boundary of the Go MaxFileBytes scanner; no Python counterpart.
func TestSearchOversizeExactlyLineLimit(t *testing.T) {
	root := t.TempDir()
	content := "package main\nfunc fits() {}\n"
	writeTempFile(t, root, "fits.go", content)
	result, err := Search(context.Background(), Config{WorkspaceRoot: root, MaxFileBytes: int64(len(content))}, Request{Pattern: "func $NAME() {}", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("exact-size file should be read, matches=%d", len(result.Matches))
	}
}

// Parity-exempt: inferFilterLanguage is an unexported Go helper for path filters;
// no Python equivalent.
func TestInferFilterLanguage(t *testing.T) {
	if _, ok := inferFilterLanguage("README.txt"); ok {
		t.Fatalf("inferFilterLanguage(README.txt) = true, want false")
	}
	if _, ok := inferFilterLanguage("header.h"); ok {
		t.Fatalf("inferFilterLanguage(header.h) = true, want false (ambiguous)")
	}
	if lang, ok := inferFilterLanguage("main.go"); !ok || lang.Name() != "go" {
		t.Fatalf("inferFilterLanguage(main.go) = %v,%v", lang, ok)
	}
}

func ExampleRender() {
	result := &Result{
		Matches: []Match{{
			Path: "main.go", Line: 3, Text: "func main() {}",
			Captures: []Capture{{Name: "NAME", Text: "main"}},
		}},
		FileCount:   1,
		SearchPath:  ".",
		ScopePath:   ".",
		ParseErrors: nil,
	}
	fmt.Println(Render(result))
	_ = context.Background()
	_ = os.Stderr
	// Output:
	// main.go:
	// *3:func main() {}
	//   meta: NAME=main
}
