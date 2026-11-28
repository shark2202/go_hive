package lsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func filepathJoin(t *testing.T, root, name string) string {
	t.Helper()
	return filepath.Join(root, name)
}

// Parity-exempt: result-shape goldens cover the bounded renderer required by
// docs/lsp-design.json R6/A8; no Python source test exists.
func TestRenderLocationsDedupesSortsAndCaps(t *testing.T) {
	root := t.TempDir()
	fileA := filepathJoin(t, root, "a.go")
	fileB := filepathJoin(t, root, "b.go")
	writeFile(t, fileA, "package a\n\nfunc A() {}\n")
	writeFile(t, fileB, "package b\n\nfunc B() {}\n")
	locations := []Location{
		{URI: fileURI(fileB), Range: Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 1}}},
		{URI: fileURI(fileA), Range: Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 1}}},
		{URI: fileURI(fileA), Range: Range{Start: Position{Line: 2, Character: 0}, End: Position{Line: 2, Character: 1}}},
		{URI: "jdt://contents/foo", Range: Range{}},
	}
	text, data := renderLocations("definition", "gopls", root, locations, 0)
	want := "definition [gopls] 2 result(s)\n" +
		"  a.go:3:1  func A() {}\n" +
		"  b.go:3:1  func B() {}\n" +
		"  1 non-file location(s) skipped"
	if text != want {
		t.Fatalf("render:\n%s\nwant:\n%s", text, want)
	}
	if data["skipped"] != 1 {
		t.Fatalf("skipped = %v", data["skipped"])
	}

	var many []Location
	for i := 0; i < 60; i++ {
		many = append(many, Location{
			URI: fileURI(fileA),
			Range: Range{
				Start: Position{Line: i % 3, Character: i},
				End:   Position{Line: i % 3, Character: i + 1},
			},
		})
	}
	text, data = renderLocations("references", "gopls", root, many, MaxReferences)
	if !strings.Contains(text, "... 10 more result(s) omitted (cap 50)") {
		t.Fatalf("cap text:\n%s", text)
	}
	if data["truncated"] != true {
		t.Fatalf("truncated = %v", data["truncated"])
	}
}

// Parity-exempt: diagnostics ordering/dedupe is the frozen A8 rule; no
// upstream golden.
func TestRenderDiagnosticsOrderingAndDedupe(t *testing.T) {
	root := t.TempDir()
	file := filepathJoin(t, root, "main.go")
	writeFile(t, file, "package main\n")
	uri := fileURI(file)
	item := func(line, severity int, message string) Diagnostic {
		return Diagnostic{
			Range:    Range{Start: Position{Line: line}, End: Position{Line: line, Character: 1}},
			Severity: severity,
			Message:  message,
			Source:   "fake",
		}
	}
	snapshots := []DiagnosticSnapshot{
		{URI: uri, Version: 2, Items: []Diagnostic{
			item(4, SeverityWarning, "warning"),
			item(0, SeverityError, "error"),
			item(0, SeverityError, "error"),
		}},
	}
	text, data := renderDiagnostics("fake", root, snapshots, true, "")
	want := "diagnostics [fake] 2 diagnostic(s)\n" +
		"  main.go:1:1  error  error  (fake)\n" +
		"  main.go:5:1  warning  warning  (fake)"
	if text != want {
		t.Fatalf("diagnostics:\n%s\nwant:\n%s", text, want)
	}
	if data["fresh"] != true {
		t.Fatalf("fresh = %v", data["fresh"])
	}
	text, _ = renderDiagnostics("fake", root, snapshots, false, "last publish is older")
	if !strings.Contains(text, "(last publish is older)") {
		t.Fatalf("note missing:\n%s", text)
	}
}

// Parity-exempt: symbol caps are frozen by docs/lsp-design.json D9/R8; no
// Python test.
func TestRenderSymbolsCapsAndEmptyText(t *testing.T) {
	if text, _ := renderSymbols("gopls", t.TempDir(), "", nil); text != "symbols [gopls]: no symbols" {
		t.Fatalf("empty symbols text = %q", text)
	}
	views := make([]symbolView, 0, MaxSymbols+5)
	for i := 0; i < MaxSymbols+5; i++ {
		views = append(views, symbolView{Name: "S", Kind: "function", Line: i + 1, Column: 1})
	}
	text, data := renderSymbols("gopls", t.TempDir(), "main.go", views)
	if !strings.Contains(text, "symbols [gopls] 205 symbol(s)") {
		t.Fatalf("header:\n%s", text)
	}
	if !strings.Contains(text, "5 more symbol(s) omitted (cap 200)") {
		t.Fatalf("cap:\n%s", text)
	}
	if data["truncated"] != true {
		t.Fatalf("truncated = %v", data["truncated"])
	}
}

// Parity-exempt: hover flattening covers the three LSP content shapes from
// R6/A8; no upstream test.
func TestFlattenHoverShapes(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`"plain text"`, "plain text"},
		{`{"kind":"markdown","value":"**bold**"}`, "**bold**"},
		{`["one",{"language":"go","value":"two"}]`, "one\ntwo"},
		{`null`, ""},
	}
	for _, tc := range cases {
		if got := flattenHover(json.RawMessage(tc.raw)); got != tc.want {
			t.Fatalf("flattenHover(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// Parity-exempt: byte-bound truncation is a new deterministic-output guard
// (R6/R8); no Python counterpart.
func TestBoundTextKeepsValidUTF8(t *testing.T) {
	text := strings.Repeat("你", 100)
	got := boundText(text, 10)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Fatalf("missing suffix: %q", got)
	}
	trimmed := strings.TrimSuffix(got, "\n... (truncated)")
	if strings.ContainsRune(trimmed, '\uFFFD') {
		t.Fatalf("cut split a rune: %q", got)
	}
	if len(trimmed) > 10 {
		t.Fatalf("trimmed length = %d", len(trimmed))
	}
}
