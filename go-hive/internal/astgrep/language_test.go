package astgrep

// Parity: A5 language matrix. Canonical names, aliases and extension
// inference mirror ast-grep's SupportLang set for the curated grammars linked
// into this binary; .h is intentionally ambiguous and .cu/.cuh map to cpp.

import (
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: pins the curated grammar set compiled into this Go binary;
// ast_grep is a post-1.0 Go-only tool with no Python module or test to mirror.
func TestSupportedLanguages(t *testing.T) {
	got := SupportedLanguages()
	want := []string{"c", "cpp", "csharp", "go", "java", "javascript", "json", "markdown", "python", "rust", "tsx", "typescript", "yaml"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SupportedLanguages() = %v, want %v", got, want)
	}
}

// Parity-exempt: language alias normalization exists only in the Go resolver;
// there is no Python ast_grep implementation to compare against.
func TestResolveLanguageAliases(t *testing.T) {
	cases := map[string]string{
		"go":         "go",
		"golang":     "go",
		"PY":         "python",
		"py":         "python",
		"ts":         "typescript",
		"tsx":        "tsx",
		"js":         "javascript",
		"jsx":        "javascript",
		"rs":         "rust",
		"yml":        "yaml",
		"md":         "markdown",
		"c++":        "cpp",
		"cxx":        "cpp",
		"C#":         "csharp",
		"c_sharp":    "csharp",
		"  Markdown": "markdown",
	}
	for input, want := range cases {
		lang, err := ResolveLanguage(input)
		if err != nil {
			t.Errorf("ResolveLanguage(%q): %v", input, err)
			continue
		}
		if lang.Name() != want {
			t.Errorf("ResolveLanguage(%q) = %s, want %s", input, lang.Name(), want)
		}
	}
	for _, bad := range []string{"", "   ", "klingon"} {
		if _, err := ResolveLanguage(bad); !IsKind(err, ErrUnsupportedLanguage) {
			t.Errorf("ResolveLanguage(%q) error = %v, want ErrUnsupportedLanguage", bad, err)
		}
	}
}

// Parity-exempt: extension-to-language inference is Go-only plumbing;
// no Python InferLanguage counterpart exists.
func TestInferLanguageMatrix(t *testing.T) {
	cases := map[string]string{
		"main.go":             "go",
		"go.mod":              "go",
		"script.py":           "python",
		"types.pyi":           "python",
		"app.ts":              "typescript",
		"app.mts":             "typescript",
		"view.tsx":            "tsx",
		"index.js":            "javascript",
		"index.mjs":           "javascript",
		"lib.rs":              "rust",
		"data.json":           "json",
		"config.yaml":         "yaml",
		"config.yml":          "yaml",
		"README.md":           "markdown",
		"notes.markdown":      "markdown",
		"main.c":              "c",
		"main.cc":             "cpp",
		"kernel.cu":           "cpp",
		"kernel.cuh":          "cpp",
		"App.java":            "java",
		"Program.cs":          "csharp",
		"/tmp/nested/main.go": "go",
	}
	for path, want := range cases {
		lang, err := InferLanguage(path)
		if err != nil {
			t.Errorf("InferLanguage(%q): %v", path, err)
			continue
		}
		if lang.Name() != want {
			t.Errorf("InferLanguage(%q) = %s, want %s", path, lang.Name(), want)
		}
	}
}

// Parity-exempt: the .h ambiguity and unsupported-extension policy is a Go
// design decision (docs/ast-grep-design.json); no Python test covers it.
func TestInferLanguageAmbiguousAndUnsupported(t *testing.T) {
	if _, err := InferLanguage(filepath.Join("include", "header.h")); !IsKind(err, ErrUnsupportedLanguage) {
		t.Fatalf("InferLanguage(.h) error = %v, want ErrUnsupportedLanguage", err)
	} else if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("InferLanguage(.h) error should name ambiguity: %v", err)
	}
	for _, path := range []string{"README", "notes.txt", "archive.tar.gz"} {
		if _, err := InferLanguage(path); !IsKind(err, ErrUnsupportedLanguage) {
			t.Errorf("InferLanguage(%q) error = %v, want ErrUnsupportedLanguage", path, err)
		}
	}
}

// Parity-exempt: DetectLanguage's explicit override is a Go-only API surface;
// no Python counterpart.
func TestDetectLanguageExplicitOverride(t *testing.T) {
	lang, err := DetectLanguage("include/header.h", "cpp")
	if err != nil {
		t.Fatalf("DetectLanguage(.h, cpp): %v", err)
	}
	if lang.Name() != "cpp" {
		t.Fatalf("explicit .h override = %s, want cpp", lang.Name())
	}
	if _, err := DetectLanguage("include/header.h", ""); !IsKind(err, ErrUnsupportedLanguage) {
		t.Fatalf("DetectLanguage(.h, no lang) error = %v, want unsupported", err)
	}
	if _, err := DetectLanguage("main.go", "klingon"); !IsKind(err, ErrUnsupportedLanguage) {
		t.Fatalf("DetectLanguage(main.go, klingon) error = %v, want unsupported", err)
	}
}

// Parity-exempt: extensionMatches is an unexported Go helper; no Python equivalent.
func TestLangExtensionMatches(t *testing.T) {
	cpp, err := ResolveLanguage("cpp")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"a.cpp", "a.h", "a.cu", "a.cuh", "a.hpp"} {
		if !cpp.extensionMatches(path) {
			t.Errorf("cpp.extensionMatches(%q) = false, want true", path)
		}
	}
	if cpp.extensionMatches("a.go") {
		t.Errorf("cpp.extensionMatches(a.go) = true, want false")
	}
	goLang, err := ResolveLanguage("go")
	if err != nil {
		t.Fatal(err)
	}
	if goLang.extensionMatches("a.h") {
		t.Errorf("go.extensionMatches(a.h) = true, want false")
	}
}
