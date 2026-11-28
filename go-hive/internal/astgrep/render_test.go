package astgrep

// Parity: A6 result golden. Display mode is intentionally frozen in doc.go:
// path header + *LINE:text anchors without snapshot hashes, continuation lines
// with leading space, optional meta line. ast-grep's hashline mode needs an
// edit-store snapshot hash that this read-only tool does not own.

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func fixtureSearch(t *testing.T, request Request) *Result {
	t.Helper()
	root, err := filepath.Abs("testdata/workspace")
	if err != nil {
		t.Fatal(err)
	}
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, request)
	if err != nil {
		t.Fatalf("Search(%+v): %v", request, err)
	}
	return result
}

// Parity-exempt: display-mode golden for the Go Render contract; no Python counterpart.
func TestRenderGoldenSingleFile(t *testing.T) {
	result := fixtureSearch(t, Request{Pattern: "fmt.Println($A)", Path: "go/main.go"})
	want := "go/main.go:\n" +
		"*6:fmt.Println(name)\n" +
		"  meta: A=name\n" +
		"*11:fmt.Println(\"hello\")\n" +
		"  meta: A=\"hello\""
	if got := Render(result); got != want {
		t.Fatalf("Render mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Parity-exempt: multiline continuation golden for the Go Render contract;
// no Python counterpart.
func TestRenderGoldenMultilineContinuation(t *testing.T) {
	result := fixtureSearch(t, Request{Pattern: "if $COND { $$$BODY }", Path: "go/check.go"})
	want := "go/check.go:\n" +
		"*10:if x > 0 {\n" +
		" 11:		println(\"positive\")\n" +
		" 12:		println(\"done\")\n" +
		" 13:	}\n" +
		"  meta: COND=x > 0, BODY=println(\"positive\") println(\"done\")"
	if got := Render(result); got != want {
		t.Fatalf("Render mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Parity-exempt: grouping golden for the Go Render contract; no Python counterpart.
func TestRenderGoldenGrouping(t *testing.T) {
	result := fixtureSearch(t, Request{Pattern: "pair($$$ARGS)", Path: "go/check.go"})
	want := "go/check.go:\n" +
		"*4:pair(x, x)\n" +
		"  meta: ARGS=x, x\n" +
		"*5:pair(x, \"other\")\n" +
		"  meta: ARGS=x, \"other\"\n" +
		"*6:pair()\n" +
		"  meta: ARGS=\"\""
	if got := Render(result); got != want {
		t.Fatalf("Render mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// Parity-exempt: the "No matches found" text is the Go Render contract; no Python counterpart.
func TestRenderNoMatches(t *testing.T) {
	result := fixtureSearch(t, Request{Pattern: "func neverCalled() {}", Path: "go/main.go"})
	if got := Render(result); got != "No matches found" {
		t.Fatalf("Render = %q, want No matches found", got)
	}
}

// Parity-exempt: the 50-result limit notice is the Go Render contract; no Python counterpart.
func TestRenderLimitNotice(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	b.WriteString("package main\n\nfunc main() {\n")
	for i := 0; i < 55; i++ {
		fmt.Fprintf(&b, "\tx := %d\n", i)
	}
	b.WriteString("}\n")
	writeTempFile(t, root, "many.go", b.String())
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "x := $N", Path: "many.go", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	text := Render(result)
	wantSuffix := "\n\nResult limit reached; narrow path or use skip to page through results."
	if !strings.HasSuffix(text, wantSuffix) {
		t.Fatalf("Render suffix missing limit notice:\n%s", text)
	}
	if strings.Count(text, "*") != 50 {
		t.Fatalf("visible anchors = %d, want 50", strings.Count(text, "*"))
	}
}

// Parity-exempt: the parse-issue section is the Go Render contract; no Python counterpart.
func TestRenderParseIssues(t *testing.T) {
	root := t.TempDir()
	writeTempFile(t, root, "bad.go", "package main\n\nfunc (\n")
	result, err := Search(context.Background(), Config{WorkspaceRoot: root}, Request{Pattern: "func $NAME() {}", Path: "bad.go", Lang: "go"})
	if err != nil {
		t.Fatal(err)
	}
	text := Render(result)
	if !strings.HasPrefix(text, "No matches found. Parse issues mean the query may be mis-scoped; narrow `path` before concluding absence.") {
		t.Fatalf("Render prefix:\n%s", text)
	}
	if !strings.Contains(text, "\n\nParse issues (1):\n  bad.go:") {
		t.Fatalf("Render parse issue section:\n%s", text)
	}
}

// Parity-exempt: Details map keys and counts are the Go tool contract; no Python counterpart.
func TestDetailsContract(t *testing.T) {
	result := fixtureSearch(t, Request{Pattern: "fmt.Println($A)", Path: "go/main.go"})
	details := Details(result)
	var keys []string
	for key := range details {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	want := []string{"fileCount", "filesSearched", "limitReached", "matchCount", "parseErrors", "parseErrorsTotal", "scopePath", "searchPath"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("details keys = %v, want %v", keys, want)
	}
	if details["matchCount"] != 2 || details["fileCount"] != 1 || details["filesSearched"] != 1 {
		t.Fatalf("details counts = %+v", details)
	}
	if details["scopePath"] != "go/main.go" || details["searchPath"] != "go/main.go" {
		t.Fatalf("details paths = %+v", details)
	}
	if issues, ok := details["parseErrors"].([]string); !ok || len(issues) != 0 {
		t.Fatalf("details parseErrors = %#v, want empty []string", details["parseErrors"])
	}
}

// Parity-exempt: normalizeMeta is an unexported Go helper (trim, empty and
// 80-char truncation); no Python equivalent.
func TestNormalizeMeta(t *testing.T) {
	cases := map[string]string{
		"  a \n\tb  ":            "a b",
		"":                       `""`,
		strings.Repeat("x", 100): strings.Repeat("x", 77) + "...",
	}
	for input, want := range cases {
		if got := normalizeMeta(input); got != want {
			t.Errorf("normalizeMeta(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity-exempt: nil-result tolerance is Go API robustness; no Python counterpart.
func TestRenderNilResult(t *testing.T) {
	if got := Render(nil); got != "No matches found" {
		t.Fatalf("Render(nil) = %q", got)
	}
	if got := Details(nil); got["matchCount"] != 0 {
		t.Fatalf("Details(nil) = %+v", got)
	}
}
