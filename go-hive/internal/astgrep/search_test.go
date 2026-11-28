package astgrep

// Parity: A2/A6/A8. The 50-result cap, skip paging, explicit no-match text,
// parse-issue reporting, cancellation and workspace default are frozen by the
// Go tool contract; ast-grep CLI's limit is native-only, so these assertions
// are package goldens rather than an upstream parity target.

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Parity-exempt: the 50-result cap and skip paging are the Go tool contract;
// ast-grep CLI's limit is native-only, so no Python test target.
func TestSearchCapAndSkipPaging(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package main\n\nfunc main() {\n")
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "\tx := %d\n", i)
	}
	b.WriteString("}\n")
	writeTempFile(t, root, "many.go", b.String())
	cfg := Config{WorkspaceRoot: root}
	cases := []struct {
		skip        int
		wantVisible int
		wantTotal   int
		wantLimit   bool
		wantFirst   int
	}{
		{0, 50, 51, true, 4},
		{10, 50, 60, false, 14},
		{55, 5, 60, false, 59},
		{60, 0, 60, false, 0},
	}
	for _, tc := range cases {
		result, err := Search(context.Background(), cfg, Request{Pattern: "x := $N", Path: "many.go", Lang: "go", Skip: tc.skip})
		if err != nil {
			t.Fatalf("skip=%d: %v", tc.skip, err)
		}
		if len(result.Matches) != tc.wantVisible {
			t.Fatalf("skip=%d visible=%d, want %d", tc.skip, len(result.Matches), tc.wantVisible)
		}
		if result.TotalMatches != tc.wantTotal {
			t.Fatalf("skip=%d total=%d, want %d", tc.skip, result.TotalMatches, tc.wantTotal)
		}
		if result.LimitReached != tc.wantLimit {
			t.Fatalf("skip=%d limit=%v, want %v", tc.skip, result.LimitReached, tc.wantLimit)
		}
		if tc.wantVisible > 0 && result.Matches[0].Line != tc.wantFirst {
			t.Fatalf("skip=%d first line=%d, want %d", tc.skip, result.Matches[0].Line, tc.wantFirst)
		}
	}
}

// Parity-exempt: defaulting the search path to the workspace root is the Go contract;
// no Python counterpart.
func TestSearchDefaultPathIsWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "nested/main.go", "package main\nfunc main() {}\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Lang: "go"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "nested/main.go" {
		t.Fatalf("matches=%+v, want nested/main.go", result.Matches)
	}
	if result.ScopePath != "." || result.SearchPath != "." {
		t.Fatalf("scope/search = %q/%q, want ./.", result.ScopePath, result.SearchPath)
	}
}

// Parity-exempt: semicolon-separated roots are a Go-only path API; no Python counterpart.
func TestSearchSemicolonTwoRoots(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "a/one.go", "package a\nfunc one() {}\n")
	writeTempFile(t, root, "b/two.go", "package b\nfunc two() {}\n")
	writeTempFile(t, root, "c/three.go", "package c\nfunc three() {}\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "a; b", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("matches=%d, want 2", len(result.Matches))
	}
	if result.SearchPath != "a; b" {
		t.Fatalf("searchPath=%q, want a; b", result.SearchPath)
	}
	for _, match := range result.Matches {
		if strings.HasPrefix(match.Path, "c/") {
			t.Fatalf("unexpected c/ match: %+v", match)
		}
	}
}

// Parity-exempt: glob and ** expansion are implemented only by this Go package;
// no Python counterpart.
func TestSearchGlobAndRecursiveGlob(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "src/a.go", "package src\nfunc a() {}\n")
	writeTempFile(t, root, "src/b.go", "package src\nfunc b() {}\n")
	writeTempFile(t, root, "other/c.go", "package other\nfunc c() {}\n")
	for _, tc := range []struct {
		path string
		want int
	}{{"src/*.go", 2}, {"**/*.go", 3}} {
		result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: tc.path, Lang: "go"})
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if len(result.Matches) != tc.want {
			t.Fatalf("%s matches=%d, want %d", tc.path, len(result.Matches), tc.want)
		}
	}
}

// Parity-exempt: the explicit no-match text is the Go tool contract; no Python counterpart.
func TestSearchNoMatch(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func neverCalled() {}", Path: "go/main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatalf("matches=%d, want 0", len(result.Matches))
	}
	if Render(result) != "No matches found" {
		t.Fatalf("no-match text = %q", Render(result))
	}
}

// Parity-exempt: parse-issue reporting is a Go Search result field; no Python counterpart.
func TestSearchParseErrorReported(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "good.go", "package main\n\nfunc good() {}\n")
	writeTempFile(t, root, "bad.go", "package main\n\nfunc (\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "good.go" {
		t.Fatalf("matches=%+v, want good.go", result.Matches)
	}
	if result.ParseErrorsTotal < 1 {
		t.Fatalf("parseErrorsTotal=%d, want >=1", result.ParseErrorsTotal)
	}
	foundBad := false
	for _, issue := range result.ParseErrors {
		if issue.Path == "bad.go" {
			foundBad = true
			if issue.Line == 0 {
				t.Fatalf("bad.go parse issue has no line: %+v", issue)
			}
		}
	}
	if !foundBad {
		t.Fatalf("parseErrors=%+v, want bad.go", result.ParseErrors)
	}
	text := Render(result)
	if !strings.Contains(text, "Parse issues") || !strings.Contains(text, "bad.go") {
		t.Fatalf("rendered text missing parse issues:\n%s", text)
	}
}

// Parity-exempt: the parse-issue no-match notice is Go Render text; no Python counterpart.
func TestSearchParseErrorNoMatchNotice(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "bad.go", "package main\n\nfunc (\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "bad.go", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	text := Render(result)
	wantPrefix := "No matches found. Parse issues mean the query may be mis-scoped; narrow `path` before concluding absence."
	if !strings.HasPrefix(text, wantPrefix) {
		t.Fatalf("text prefix = %q, want %q", text, wantPrefix)
	}
	if !strings.Contains(text, "Parse issues (1)") {
		t.Fatalf("text missing parse issue header:\n%s", text)
	}
}

// Parity-exempt: context cancellation mapping to ErrCancelled is Go-only;
// no Python counterpart.
func TestSearchCancelledContext(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Search(ctx, Config{WorkspaceRoot: root}, Request{Pattern: "fmt.Println($A)", Path: "go", Lang: "go"})
	if !IsKind(err, ErrCancelled) {
		t.Fatalf("cancelled search error = %v, want ErrCancelled", err)
	}
}

// Parity-exempt: Go Request validation taxonomy (ErrInvalidInput/ErrUnsupportedLanguage);
// no Python counterpart.
func TestSearchInvalidInput(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "main.go", "package main\nfunc main() {}\n")
	cases := []struct {
		name string
		req  Request
	}{
		{"empty pattern", Request{Pattern: "  ", Path: "main.go"}},
		{"negative skip", Request{Pattern: "func $NAME() {}", Path: "main.go", Skip: -1}},
		{"empty semicolon entry", Request{Pattern: "func $NAME() {}", Path: "main.go;;x", Lang: "go"}},
		{"unknown language", Request{Pattern: "func $NAME() {}", Path: "main.go", Lang: "klingon"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Search(context.Background(), Config{WorkspaceRoot: root}, tc.req)
			if err == nil {
				t.Fatalf("Search(%+v) succeeded, want error", tc.req)
			}
			want := ErrInvalidInput
			if tc.name == "unknown language" {
				want = ErrUnsupportedLanguage
			}
			if !IsKind(err, want) {
				t.Fatalf("%s error kind = %q, want %q (%v)", tc.name, KindOf(err), want, err)
			}
		})
	}
}

// Parity-exempt: requiring an explicit lang for .h is a Go tool design decision;
// no Python test covers it.
func TestSearchAmbiguousHeaderWithoutLang(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "int $NAME(int $A, int $B);", Path: "cpp/header.h"}); !IsKind(err, ErrUnsupportedLanguage) {
		t.Fatalf("ambiguous .h error = %v, want ErrUnsupportedLanguage", err)
	}
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "int $NAME(int $A, int $B);", Path: "cpp/header.h", Lang: "cpp"})
	if err != nil {
		t.Fatalf("explicit cpp override: %v", err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Line != 4 {
		t.Fatalf("override matches=%+v, want line 4", result.Matches)
	}
}

// Parity-exempt: Go goroutine-leak regression guard; no Python counterpart.
func TestSearchDoesNotLeakGoroutines(t *testing.T) {
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	start := 0
	for i := 0; i < 5; i++ {
		result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "fmt.Println($A)", Path: "go", Lang: "go"})
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatal("nil search result")
		}
		time.Sleep(10 * time.Millisecond)
	}
	start = runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "fmt.Println($A)", Path: "go", Lang: "go"})
		if err != nil {
			t.Fatal(err)
		}
		if result == nil {
			t.Fatal("nil search result")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= start+5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines = %d after searches, start=%d", runtime.NumGoroutine(), start)
}
