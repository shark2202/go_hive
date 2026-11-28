package astgrep

import (
	"path/filepath"
	"sort"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
	cgrammar "github.com/odvcencio/gotreesitter/grammars/c"
	csharp "github.com/odvcencio/gotreesitter/grammars/c_sharp"
	cppgrammar "github.com/odvcencio/gotreesitter/grammars/cpp"
	golang "github.com/odvcencio/gotreesitter/grammars/go"
	javagrammar "github.com/odvcencio/gotreesitter/grammars/java"
	javascript "github.com/odvcencio/gotreesitter/grammars/javascript"
	jsongrammar "github.com/odvcencio/gotreesitter/grammars/json"
	markdown "github.com/odvcencio/gotreesitter/grammars/markdown"
	python "github.com/odvcencio/gotreesitter/grammars/python"
	rust "github.com/odvcencio/gotreesitter/grammars/rust"
	tsx "github.com/odvcencio/gotreesitter/grammars/tsx"
	typescript "github.com/odvcencio/gotreesitter/grammars/typescript"
	yamlgrammar "github.com/odvcencio/gotreesitter/grammars/yaml"
)

// AmbiguousHExtension is the extension that needs an explicit language
// override. It is exported so adapters and tests can name the refusal without
// depending on an error string.
const AmbiguousHExtension = ".h"

// Lang is one compiled, supported grammar. Values are immutable and shared;
// callers may compare pointers from ResolveLanguage / DetectLanguage.
type Lang struct {
	name       string
	extensions []string
	grammar    func() *gotreesitter.Language
}

// Name returns the canonical language name used by the lang input.
func (l *Lang) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

// Extensions returns a copy of the extensions inferred for this language.
func (l *Lang) Extensions() []string {
	if l == nil {
		return nil
	}
	out := make([]string, len(l.extensions))
	copy(out, l.extensions)
	return out
}

// Grammar returns the gotreesitter language for this registry entry.
func (l *Lang) Grammar() *gotreesitter.Language {
	if l == nil || l.grammar == nil {
		return nil
	}
	return l.grammar()
}

// String implements fmt.Stringer.
func (l *Lang) String() string { return l.Name() }

// registry is the curated default grammar set. Keeping standalone grammar
// packages (not the full grammars registry) is what stops 206 grammar blobs
// from entering the binary.
var registry = map[string]*Lang{
	"go":         {name: "go", extensions: []string{".go", ".mod"}, grammar: golang.Language},
	"python":     {name: "python", extensions: []string{".py", ".pyi"}, grammar: python.Language},
	"typescript": {name: "typescript", extensions: []string{".ts", ".mts", ".cts"}, grammar: typescript.Language},
	"tsx":        {name: "tsx", extensions: []string{".tsx"}, grammar: tsx.Language},
	"javascript": {name: "javascript", extensions: []string{".js", ".mjs", ".cjs", ".jsx"}, grammar: javascript.Language},
	"rust":       {name: "rust", extensions: []string{".rs"}, grammar: rust.Language},
	"json":       {name: "json", extensions: []string{".json"}, grammar: jsongrammar.Language},
	"yaml":       {name: "yaml", extensions: []string{".yaml", ".yml"}, grammar: yamlgrammar.Language},
	"markdown":   {name: "markdown", extensions: []string{".md", ".markdown"}, grammar: markdown.Language},
	"c":          {name: "c", extensions: []string{".c"}, grammar: cgrammar.Language},
	"cpp":        {name: "cpp", extensions: []string{".cc", ".cpp", ".cxx", ".c++", ".hpp", ".hh", ".hxx", ".cu", ".cuh"}, grammar: cppgrammar.Language},
	"java":       {name: "java", extensions: []string{".java"}, grammar: javagrammar.Language},
	"csharp":     {name: "csharp", extensions: []string{".cs"}, grammar: csharp.Language},
}

var aliases = map[string]string{
	"go":         "go",
	"golang":     "go",
	"python":     "python",
	"py":         "python",
	"python3":    "python",
	"typescript": "typescript",
	"ts":         "typescript",
	"tsx":        "tsx",
	"javascript": "javascript",
	"js":         "javascript",
	"jsx":        "javascript",
	"rust":       "rust",
	"rs":         "rust",
	"json":       "json",
	"yaml":       "yaml",
	"yml":        "yaml",
	"markdown":   "markdown",
	"md":         "markdown",
	"c":          "c",
	"cpp":        "cpp",
	"c++":        "cpp",
	"cxx":        "cpp",
	"cc":         "cpp",
	"java":       "java",
	"csharp":     "csharp",
	"c#":         "csharp",
	"cs":         "csharp",
	"c_sharp":    "csharp",
}

var extensionToLanguage = buildExtensionIndex()

func buildExtensionIndex() map[string]*Lang {
	index := map[string]*Lang{}
	for _, lang := range registry {
		for _, ext := range lang.extensions {
			index[ext] = lang
		}
	}
	return index
}

// SupportedLanguages returns the canonical names of the compiled grammars, in
// stable sorted order.
func SupportedLanguages() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ResolveLanguage maps a canonical name, an alias or a capitalized language
// name to a compiled grammar. Unknown names are ErrUnsupportedLanguage.
func ResolveLanguage(name string) (*Lang, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, NewError(ErrUnsupportedLanguage, "empty language override")
	}
	canonical, ok := aliases[key]
	if !ok {
		return nil, NewError(ErrUnsupportedLanguage, "unsupported language: %s", name)
	}
	lang := registry[canonical]
	if lang == nil {
		return nil, NewError(ErrUnsupportedLanguage, "unsupported language: %s", name)
	}
	return lang, nil
}

// InferLanguage resolves a file path through its extension. The .h extension
// is ambiguous and returns ErrUnsupportedLanguage asking for an explicit lang;
// .cu and .cuh infer as cpp. Unknown extensions are typed errors: callers that
// walk a directory should filter candidates first, while an explicitly named
// file gets a clear refusal.
func InferLanguage(path string) (*Lang, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == AmbiguousHExtension {
		return nil, NewError(ErrUnsupportedLanguage,
			"ambiguous extension .h: pass lang \"cpp\" or \"c\" explicitly")
	}
	if lang, ok := extensionToLanguage[ext]; ok {
		return lang, nil
	}
	if ext == "" {
		return nil, NewError(ErrUnsupportedLanguage, "unsupported file without an extension: %s", filepath.Base(path))
	}
	return nil, NewError(ErrUnsupportedLanguage, "unsupported file extension %s: %s", ext, filepath.Base(path))
}

// DetectLanguage implements the input contract: an explicit lang wins for all
// candidates; otherwise language is inferred per path.
func DetectLanguage(path, explicit string) (*Lang, error) {
	if strings.TrimSpace(explicit) != "" {
		return ResolveLanguage(explicit)
	}
	return InferLanguage(path)
}

// extensionMatches reports whether path has an extension the language claims.
// Ambiguous .h is accepted only when the explicit language is c or cpp.
func (l *Lang) extensionMatches(path string) bool {
	if l == nil {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == AmbiguousHExtension {
		return l.name == "cpp" || l.name == "c"
	}
	for _, candidate := range l.extensions {
		if candidate == ext {
			return true
		}
	}
	return false
}

// inferFilterLanguage returns the language for a walked (not explicitly named)
// candidate, and false when the extension is not part of the curated source set.
func inferFilterLanguage(path string) (*Lang, bool) {
	lang, err := InferLanguage(path)
	if err != nil {
		return nil, false
	}
	return lang, true
}
