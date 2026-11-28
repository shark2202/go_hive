package astanalyze

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLanguageMatrix exercises every grammar ast_grep links: parsing, LOC,
// unit grouping and the report envelope must work for each one. Import/risk
// detectors are language-specific and may be empty; the point here is that no
// grammar is silently skipped or crashes the analyzer.
// Parity-exempt: Go-only post-1.0 ast_analyze language matrix; no Python counterpart.
func TestLanguageMatrix(t *testing.T) {
	cases := []struct {
		lang string
		rel  string
		body string
	}{
		{"go", "go/main.go", "package main\n\nfunc main() {}\n"},
		{"python", "python/main.py", "def main():\n    return 1\n"},
		{"typescript", "typescript/main.ts", "export function main() { return 1 }\n"},
		{"tsx", "tsx/main.tsx", "export function App() { return null }\n"},
		{"javascript", "javascript/main.js", "console.log('hi');\n"},
		{"rust", "rust/main.rs", "fn main() {}\n"},
		{"json", "json/config.json", "{\"key\": 1}\n"},
		{"yaml", "yaml/sample.yml", "key: value\n"},
		{"markdown", "markdown/README.md", "# Title\n\nBody.\n"},
		{"c", "c/main.c", "int main(void) { return 0; }\n"},
		{"cpp", "cpp/main.cc", "int main() { return 0; }\n"},
		{"java", "java/Main.java", "class Main { }\n"},
		{"csharp", "csharp/Program.cs", "class Program { }\n"},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			report, err := Analyze(context.Background(), DefaultConfig(), root, Request{
				Path: path,
				Lang: tc.lang,
			})
			if err != nil {
				t.Fatalf("Analyze(%s): %v", tc.lang, err)
			}
			if report.Language != tc.lang {
				t.Fatalf("language = %q, want %q", report.Language, tc.lang)
			}
			if report.Summary.Files != 1 || report.Summary.Units != 1 {
				t.Fatalf("summary = %+v", report.Summary)
			}
			if report.Summary.LOC == 0 {
				t.Fatalf("LOC = 0 for %s", tc.lang)
			}
		})
	}
}
