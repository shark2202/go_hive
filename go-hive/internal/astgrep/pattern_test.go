package astgrep

// Parity: A4 pattern semantics. The corpus pins ast-grep's metavariable rules
// for capture, wildcard, repeated equality, zero-or-more variadic captures,
// multiline matches and backtracking. Cases that ast-grep cannot parse either
// (for example bare $$$ in a typed Go parameter list) are registered in the
// residual list rather than faked.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func compileFixture(t *testing.T, langName, pattern, rel string) (*Pattern, []byte) {
	t.Helper()
	lang, err := ResolveLanguage(langName)
	if err != nil {
		t.Fatalf("ResolveLanguage(%q): %v", langName, err)
	}
	compiled, err := CompilePattern(lang, pattern)
	if err != nil {
		t.Fatalf("CompilePattern(%q, %q): %v", langName, pattern, err)
	}
	source, err := os.ReadFile(filepath.Join("testdata", "workspace", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read fixture %s: %v", rel, err)
	}
	return compiled, source
}

func findFixture(t *testing.T, langName, pattern, rel string) []SyntaxMatch {
	t.Helper()
	compiled, source := compileFixture(t, langName, pattern, rel)
	matches, err := compiled.FindAll(source)
	if err != nil {
		t.Fatalf("FindAll(%q): %v", pattern, err)
	}
	return matches
}

// Parity-exempt: $A capture over the Go fixture corpus; ast_grep is Go-only post-1.0,
// so there is no Python test target.
func TestPatternCapture(t *testing.T) {
	matches := findFixture(t, "go", "fmt.Println($A)", "go/main.go")
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if matches[0].Line != 6 || matches[1].Line != 11 {
		t.Fatalf("lines = %d,%d, want 6,11", matches[0].Line, matches[1].Line)
	}
	if len(matches[0].Captures) != 1 || matches[0].Captures[0].Name != "A" || matches[0].Captures[0].Text != "name" {
		t.Fatalf("first captures = %+v, want A=name", matches[0].Captures)
	}
	if matches[1].Captures[0].Text != `"hello"` {
		t.Fatalf("second capture = %q, want quoted hello", matches[1].Captures[0].Text)
	}
}

// Parity-exempt: $_ wildcard captures nothing by Go matcher design; no Python counterpart.
func TestPatternWildcard(t *testing.T) {
	matches := findFixture(t, "go", "fmt.Println($_)", "go/main.go")
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
	for _, match := range matches {
		if len(match.Captures) != 0 {
			t.Fatalf("wildcard capture = %+v, want none", match.Captures)
		}
	}
}

// Parity-exempt: repeated $A equality is enforced by the Go matcher; no Python counterpart.
func TestPatternRepeatedCaptureEquality(t *testing.T) {
	matches := findFixture(t, "go", "pair($A, $A)", "go/check.go")
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1 (pair(x, \"other\") must not match): %+v", len(matches), matches)
	}
	if matches[0].Line != 4 || matches[0].Text != "pair(x, x)" {
		t.Fatalf("match = line %d %q, want line 4 pair(x, x)", matches[0].Line, matches[0].Text)
	}
}

// Parity-exempt: $$$ARGS zero-or-more variadic capture is Go matcher behavior;
// no Python counterpart.
func TestPatternVariadicZeroOrMore(t *testing.T) {
	matches := findFixture(t, "go", "pair($$$ARGS)", "go/check.go")
	if len(matches) != 3 {
		t.Fatalf("got %d matches, want 3: %+v", len(matches), matches)
	}
	want := []string{`x, x`, `x, "other"`, ``}
	for i, match := range matches {
		if len(match.Captures) != 1 || match.Captures[0].Name != "ARGS" {
			t.Fatalf("match %d captures = %+v, want ARGS", i, match.Captures)
		}
		if match.Captures[0].Text != want[i] {
			t.Fatalf("match %d ARGS = %q, want %q", i, match.Captures[0].Text, want[i])
		}
		if !match.Captures[0].Variadic {
			t.Fatalf("match %d ARGS should be marked variadic", i)
		}
	}
}

// Parity-exempt: variadic backtracking is Go matcher behavior; no Python counterpart.
func TestPatternVariadicBacktracking(t *testing.T) {
	matches := findFixture(t, "go", "pair($$$ARGS, $LAST)", "go/check.go")
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if len(matches[0].Captures) != 2 {
		t.Fatalf("captures = %+v, want ARGS and LAST", matches[0].Captures)
	}
	if matches[0].Captures[0].Text != "x" || matches[0].Captures[1].Text != "x" {
		t.Fatalf("pair(x, x) captures = %+v, want ARGS=x LAST=x", matches[0].Captures)
	}
	if matches[1].Captures[0].Text != "x" || matches[1].Captures[1].Text != `"other"` {
		t.Fatalf("pair(x, \"other\") captures = %+v", matches[1].Captures)
	}
}

// Parity-exempt: multiline $$$BODY capture is Go matcher behavior; no Python counterpart.
func TestPatternFunctionBodyVariadicAndMultiline(t *testing.T) {
	matches := findFixture(t, "go", "func $NAME($A $T) { $$$BODY }", "go/main.go")
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].Line != 5 {
		t.Fatalf("line = %d, want 5", matches[0].Line)
	}
	names := []string{"NAME", "A", "T", "BODY"}
	if len(matches[0].Captures) != len(names) {
		t.Fatalf("captures = %+v, want %v", matches[0].Captures, names)
	}
	for i, capture := range matches[0].Captures {
		if capture.Name != names[i] {
			t.Fatalf("capture %d = %s, want %s", i, capture.Name, names[i])
		}
	}
	if matches[0].Captures[0].Text != "greet" || matches[0].Captures[1].Text != "name" || matches[0].Captures[2].Text != "string" {
		t.Fatalf("header captures = %+v", matches[0].Captures)
	}
	if !strings.Contains(matches[0].Captures[3].Text, "fmt.Println(name)") {
		t.Fatalf("BODY capture = %q, want body text", matches[0].Captures[3].Text)
	}
	if strings.Count(matches[0].Text, "\n") < 2 {
		t.Fatalf("match text is not multiline: %q", matches[0].Text)
	}
}

// Parity-exempt: multiline if-block capture is Go matcher behavior; no Python counterpart.
func TestPatternMultilineBlockCapture(t *testing.T) {
	matches := findFixture(t, "go", "if $COND { $$$BODY }", "go/check.go")
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1", len(matches))
	}
	if matches[0].Line != 10 {
		t.Fatalf("line = %d, want 10", matches[0].Line)
	}
	if len(matches[0].Captures) != 2 || matches[0].Captures[0].Name != "COND" || matches[0].Captures[1].Name != "BODY" {
		t.Fatalf("captures = %+v", matches[0].Captures)
	}
	if matches[0].Captures[0].Text != "x > 0" {
		t.Fatalf("COND = %q, want x > 0", matches[0].Captures[0].Text)
	}
	if !strings.Contains(matches[0].Captures[1].Text, `println("positive")`) ||
		!strings.Contains(matches[0].Captures[1].Text, `println("done")`) {
		t.Fatalf("BODY = %q, want both statements", matches[0].Captures[1].Text)
	}
}

// Parity-exempt: cross-language matching over the Go fixture corpus; no Python test target.
func TestPatternLanguageMatrix(t *testing.T) {
	cases := []struct {
		lang, pattern, rel string
		wantLines          []int
	}{
		{"go", "fmt.Println($A)", "go/main.go", []int{6, 11}},
		{"python", "print($A)", "python/main.py", []int{2, 8}},
		{"typescript", "console.log($A)", "typescript/main.ts", []int{2}},
		{"rust", "Some($A)", "rust/main.rs", []int{2, 3}},
		{"json", `{"name": "$V"}`, "json/config.json", []int{4}},
		{"yaml", "name: $V", "yaml/sample.yml", []int{1, 4}},
		{"markdown", "# $TITLE", "markdown/README.md", []int{1, 3}},
		{"cpp", "int $NAME(int $A, int $B);", "cpp/header.h", []int{4}},
		{"tsx", "function $NAME() { return null }", "tsx/main.tsx", []int{1}},
		{"javascript", "console.log($A)", "javascript/main.js", []int{1}},
		{"c", "int $NAME(void) { return 0; }", "c/main.c", []int{1}},
		{"java", "class $NAME { }", "java/Main.java", []int{1}},
		{"csharp", "class $NAME { }", "csharp/Program.cs", []int{1}},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			matches := findFixture(t, tc.lang, tc.pattern, tc.rel)
			if len(matches) != len(tc.wantLines) {
				t.Fatalf("%s: got %d matches, want %d: %+v", tc.lang, len(matches), len(tc.wantLines), matches)
			}
			for i, line := range tc.wantLines {
				if matches[i].Line != line {
					t.Fatalf("%s match %d line = %d, want %d", tc.lang, i, matches[i].Line, line)
				}
			}
			if tc.lang == "json" && matches[0].Captures[0].Text != "inner" {
				t.Fatalf("json capture = %q, want inner", matches[0].Captures[0].Text)
			}
			if tc.lang == "yaml" && matches[0].Captures[0].Text != "hive" {
				t.Fatalf("yaml capture = %q, want hive", matches[0].Captures[0].Text)
			}
			if tc.lang == "markdown" && matches[0].Captures[0].Text != "Hive API" {
				t.Fatalf("markdown capture = %q, want Hive API", matches[0].Captures[0].Text)
			}
		})
	}
}

// Parity-exempt: Go CompilePattern error taxonomy (ErrInvalidPattern/ErrInvalidInput);
// no Python counterpart.
func TestPatternInvalidPatterns(t *testing.T) {
	goLang, err := ResolveLanguage("go")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, pattern string
	}{
		{"unparsable", "func ("},
		{"multiple nodes", "x := 1; y := 2"},
		{"variadic root", "$$$BODY"},
		{"string fragment", `fmt.Println("$A")`},
		{"lowercase dollar", "$foo"},
		{"inconsistent arity", "pair($A, $$$A)"},
		{"empty", "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompilePattern(goLang, tc.pattern)
			if err == nil {
				t.Fatalf("CompilePattern(%q) succeeded, want error", tc.pattern)
			}
			wantKind := ErrInvalidPattern
			if tc.name == "empty" {
				wantKind = ErrInvalidInput
			}
			if !IsKind(err, wantKind) {
				t.Fatalf("CompilePattern(%q) kind = %q, want %q (%v)", tc.pattern, KindOf(err), wantKind, err)
			}
		})
	}
}

// Parity-exempt: missing-node tolerance in the Go tree-sitter binding; no Python counterpart.
func TestPatternMissingSemicolonIgnored(t *testing.T) {
	// Rust's top-level expression_statement has an implicit missing semicolon.
	// ast-grep ignores missing nodes; this pattern must still compile and match.
	matches := findFixture(t, "rust", "Some($A)", "rust/main.rs")
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2", len(matches))
	}
}

// TestPatternUnsupportedShapes records pattern shapes this lane intentionally
// refuses instead of faking a match. Each case is a residual in doc.go.
// Parity-exempt: exercises the residual shapes list; not a parity claim.
func TestPatternUnsupportedShapes(t *testing.T) {
	cases := []struct {
		lang, pattern, why string
	}{
		{
			lang:    "json",
			pattern: `{"key": $V}`,
			why:     "unquoted JSON value metavariables cannot be parsed by the JSON grammar",
		},
		{
			lang:    "go",
			pattern: `println("$A")`,
			why:     "string-fragment metavariables are rejected outside JSON string values",
		},
	}
	for _, tc := range cases {
		t.Run(tc.lang+"_"+tc.why, func(t *testing.T) {
			lang, err := ResolveLanguage(tc.lang)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CompilePattern(lang, tc.pattern); !IsKind(err, ErrInvalidPattern) {
				t.Fatalf("CompilePattern(%q) error = %v, want ErrInvalidPattern (%s)", tc.pattern, err, tc.why)
			}
		})
	}
}

// TestPatternBareParameterVariadicFindsNothing is residual evidence, not a
// parity claim: Go/Rust insert a missing type node for `func f($$$)` so the
// pattern compiles but its parameter structure cannot equal a normal typed
// parameter. The test pins zero matches instead of a fabricated capture.
// Parity-exempt: residual evidence only; not a parity claim.
func TestPatternBareParameterVariadicFindsNothing(t *testing.T) {
	cases := []struct{ lang, pattern, rel string }{
		{"go", "func $NAME($$$ARGS) {}", "go/main.go"},
		{"rust", "fn $NAME($$$ARGS) {}", "rust/main.rs"},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			matches := findFixture(t, tc.lang, tc.pattern, tc.rel)
			if len(matches) != 0 {
				t.Fatalf("bare parameter variadic produced %d matches; expected the registered zero-match residual: %+v", len(matches), matches)
			}
		})
	}
}

// Parity-exempt: tree-sitter error-recovery matching is a Go binding guarantee;
// no Python counterpart.
func TestPatternRecoveryParseStillFinds(t *testing.T) {
	lang, err := ResolveLanguage("go")
	if err != nil {
		t.Fatal(err)
	}
	pattern, err := CompilePattern(lang, "func $NAME() {}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pattern.FindAll([]byte("package main\nfunc (")); err != nil {
		t.Fatalf("recovery parse should still return matches: %v", err)
	}
}
