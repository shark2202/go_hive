package attachments

// Parity: tools/src/aden_tools/utils/attachments.py

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var invalidCharsRe = regexp.MustCompile(`[\x00-\x1f\\/:*?"<>|]`)

var whitespaceRe = regexp.MustCompile(`[\s\p{Zs}\x{0085}\x{2028}\x{2029}]+`)

const MaxBasenameLen = 200

var TextExtToMIME = map[string]string{
	".md":    "text/markdown",
	".txt":   "text/plain",
	".csv":   "text/csv",
	".tsv":   "text/tab-separated-values",
	".json":  "application/json",
	".jsonl": "application/x-ndjson",
	".yaml":  "application/yaml",
	".yml":   "application/yaml",
	".toml":  "application/toml",
	".xml":   "application/xml",
	".html":  "text/html",
	".htm":   "text/html",
	".svg":   "image/svg+xml",
	".css":   "text/css",
	".scss":  "text/x-scss",
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".ts":    "text/x-typescript",
	".tsx":   "text/x-typescript",
	".jsx":   "text/javascript",
	".py":    "text/x-python",
	".rb":    "text/x-ruby",
	".go":    "text/x-go",
	".rs":    "text/x-rust",
	".java":  "text/x-java",
	".kt":    "text/x-kotlin",
	".swift": "text/x-swift",
	".c":     "text/x-c",
	".cpp":   "text/x-c++",
	".cc":    "text/x-c++",
	".h":     "text/x-c",
	".hpp":   "text/x-c++",
	".cs":    "text/x-csharp",
	".sh":    "text/x-shellscript",
	".bash":  "text/x-shellscript",
	".zsh":   "text/x-shellscript",
	".fish":  "text/x-shellscript",
	".sql":   "application/sql",
	".ini":   "text/x-ini",
	".cfg":   "text/x-ini",
	".conf":  "text/x-conf",
	".log":   "text/plain",
	".rst":   "text/x-rst",
	".tex":   "text/x-tex",
}

// Parity: tools/src/aden_tools/utils/attachments.py::looks_like_text
func LooksLikeText(sample []byte) bool {
	if bytesContainsNUL(sample) {
		return false
	}
	return utf8.Valid(sample)
}

// Parity: tools/src/aden_tools/utils/attachments.py::sanitize_attachment_basename
func SanitizeBasename(originalName, forceExt, fallbackStem string) string {
	base := baseName(originalName)
	if base == "" {
		base = fallbackStem
	}
	base = invalidCharsRe.ReplaceAllString(base, "_")
	base = strings.TrimSpace(whitespaceRe.ReplaceAllString(base, " "))
	if base == "" {
		base = fallbackStem
	}
	stem, ext := splitExt(base)
	if forceExt != "" && (ext == "" || !strings.EqualFold(ext, forceExt)) {
		ext = forceExt
		if stem == "" {
			stem = fallbackStem
		}
		base = stem + ext
	}
	if runes := []rune(base); len(runes) > MaxBasenameLen {
		stemRunes := []rune(stem)
		suffixRunes := []rune(ext)
		keep := MaxBasenameLen - len(suffixRunes)
		if keep < 0 {
			keep = 0
		}
		if keep > len(stemRunes) {
			keep = len(stemRunes)
		}
		base = string(stemRunes[:keep]) + string(suffixRunes)
	}
	return base
}

// Parity: tools/src/aden_tools/utils/attachments.py::disambiguate_attachment_filename
func DisambiguateFilename(directory, basename string) string {
	if !pathExists(filepath.Join(directory, basename)) {
		return basename
	}
	stem, suffix := splitExt(basename)
	for i := 2; i < 10000; i++ {
		candidate := stem + " (" + strconv.Itoa(i) + ")" + suffix
		if !pathExists(filepath.Join(directory, candidate)) {
			return candidate
		}
	}
	return stem + "-" + strconv.FormatInt(time.Now().UnixMilli(), 10) + suffix
}

func baseName(name string) string {
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

func splitExt(name string) (string, string) {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return name, ""
	}
	for i := 0; i < dot; i++ {
		if name[i] != '.' {
			return name[:dot], name[dot:]
		}
	}
	return name, ""
}

func bytesContainsNUL(sample []byte) bool {
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
