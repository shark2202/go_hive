package astgrep

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// skipDirNames are VCS and cache directories a directory walk never enters.
// This is a safety and signal choice, not a gitignore implementation: the v1
// contract searches workspace paths and globs, not ignored-file metadata.
var skipDirNames = map[string]struct{}{
	".git":          {},
	".hg":           {},
	".svn":          {},
	"node_modules":  {},
	"__pycache__":   {},
	".venv":         {},
	".tox":          {},
	".mypy_cache":   {},
	".ruff_cache":   {},
	".pytest_cache": {},
	".cache":        {},
}

// candidate is one file selected by path resolution, before language filtering.
type candidate struct {
	abs      string
	display  string
	explicit bool
	lang     *Lang
}

// scope is the resolved search set plus the strings shown in result details.
type scope struct {
	candidates []candidate
	display    string
	skipped    int
}

// expandScope turns the path input into candidates. Every entry is validated
// independently: one traversal or symlink escape fails the whole request.
func expandScope(root, pathArg string) (*scope, error) {
	entries, err := splitPathEntries(pathArg)
	if err != nil {
		return nil, err
	}
	result := &scope{}
	displays := make([]string, 0, len(entries))
	seen := map[string]int{}
	for _, entry := range entries {
		display, files, skipped, err := expandEntry(root, entry)
		if err != nil {
			return nil, err
		}
		displays = append(displays, display)
		result.skipped += skipped
		for _, file := range files {
			if index, ok := seen[file.abs]; ok {
				if file.explicit && !result.candidates[index].explicit {
					result.candidates[index].explicit = true
				}
				continue
			}
			seen[file.abs] = len(result.candidates)
			result.candidates = append(result.candidates, file)
		}
	}
	sort.Slice(result.candidates, func(i, j int) bool {
		return result.candidates[i].display < result.candidates[j].display
	})
	result.display = strings.Join(displays, "; ")
	return result, nil
}

func splitPathEntries(pathArg string) ([]string, error) {
	if strings.TrimSpace(pathArg) == "" {
		return []string{"."}, nil
	}
	parts := strings.Split(pathArg, ";")
	entries := make([]string, 0, len(parts))
	for _, part := range parts {
		entry := strings.TrimSpace(part)
		if entry == "" {
			return nil, NewError(ErrInvalidInput, "empty entry in semicolon-separated path list")
		}
		if strings.ContainsRune(entry, 0) {
			return nil, NewError(ErrInvalidInput, "path contains a NUL byte")
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func expandEntry(root, entry string) (string, []candidate, int, error) {
	abs := entry
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, entry)
	}
	abs = filepath.Clean(abs)
	if isGlobEntry(entry) {
		return expandGlob(root, abs, entry)
	}
	// Resolve the deepest existing ancestor before containment so an absolute
	// path under a symlinked workspace root (macOS /var -> /private/var) is
	// accepted, while a target outside the root (including a non-existent
	// path reached through ..) is still rejected.
	resolved, ok := resolveRealCandidate(root, abs)
	if !ok {
		return "", nil, 0, NewError(ErrPathEscape, "path escapes the workspace root: %s", entry)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", nil, 0, NewError(ErrInvalidInput, "path not found: %s", entry)
	}
	display := displayPath(root, resolved)
	if info.IsDir() {
		var files []candidate
		skipped := 0
		if err := walkDirectory(root, resolved, &files, &skipped); err != nil {
			return "", nil, 0, err
		}
		return display, files, skipped, nil
	}
	if !info.Mode().IsRegular() {
		return "", nil, 0, NewError(ErrInvalidInput, "not a regular file: %s", entry)
	}
	return display, []candidate{{abs: resolved, display: display, explicit: true}}, 0, nil
}

// resolveRealCandidate resolves the deepest existing ancestor of abs and appends
// the remaining (not yet existing) path. It reports false when the real path is
// outside root.
func resolveRealCandidate(root, abs string) (string, bool) {
	ancestor := abs
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(ancestor, abs)
	if err != nil {
		return "", false
	}
	real := filepath.Clean(filepath.Join(realAncestor, rel))
	if !insideRoot(root, real) {
		return "", false
	}
	return real, true
}

func expandGlob(root, absPattern, entry string) (string, []candidate, int, error) {
	base, patternRel, err := globBase(root, absPattern)
	if err != nil {
		return "", nil, 0, err
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return "", nil, 0, NewError(ErrInvalidInput, "glob base is not a directory: %s", entry)
	}
	var walked []candidate
	skipped := 0
	if err := walkDirectory(root, base, &walked, &skipped); err != nil {
		return "", nil, 0, err
	}
	files := make([]candidate, 0, len(walked))
	for _, file := range walked {
		if matchGlob(patternRel, file.display) {
			file.explicit = true
			files = append(files, file)
		}
	}
	return filepath.ToSlash(patternRel), files, skipped, nil
}

func globBase(root, absPattern string) (string, string, error) {
	meta := strings.IndexAny(absPattern, "*?[")
	if meta < 0 {
		return "", "", NewError(ErrInvalidInput, "not a glob: %s", absPattern)
	}
	prefix := absPattern[:meta]
	baseRaw := prefix
	if cut := strings.LastIndexAny(baseRaw, `/\`); cut >= 0 {
		baseRaw = baseRaw[:cut]
	}
	if baseRaw == "" {
		baseRaw = string(os.PathSeparator)
	}
	baseRaw = filepath.Clean(baseRaw)
	// Resolve the base before containment so a symlinked workspace root is
	// accepted, while a glob that leaves the root is still rejected.
	base, ok := resolveRealCandidate(root, baseRaw)
	if !ok {
		return "", "", NewError(ErrPathEscape, "glob escapes the workspace root: %s", absPattern)
	}
	suffix := strings.TrimPrefix(absPattern, baseRaw)
	realPattern := filepath.Clean(filepath.Join(base, filepath.FromSlash(suffix)))
	if !insideRoot(root, realPattern) {
		return "", "", NewError(ErrPathEscape, "glob escapes the workspace root: %s", absPattern)
	}
	rel, err := filepath.Rel(root, realPattern)
	if err != nil {
		return "", "", NewError(ErrPathEscape, "glob escapes the workspace root: %s", absPattern)
	}
	return base, filepath.ToSlash(rel), nil
}

func walkDirectory(root, dir string, out *[]candidate, skipped *int) error {
	return filepath.WalkDir(dir, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if current != dir {
				if _, skip := skipDirNames[entry.Name()]; skip {
					return fs.SkipDir
				}
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			*skipped++
			return nil
		}
		if !entry.Type().IsRegular() {
			*skipped++
			return nil
		}
		rel, err := filepath.Rel(root, current)
		if err != nil {
			return nil
		}
		*out = append(*out, candidate{abs: current, display: filepath.ToSlash(rel), explicit: false})
		return nil
	})
}

func insideRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func displayPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}

func isGlobEntry(entry string) bool { return strings.ContainsAny(entry, "*?[") }

// matchGlob matches a slash-separated workspace-relative pattern. A whole
// `**` segment matches zero or more segments; other segments use path.Match.
func matchGlob(pattern, target string) bool {
	patSegs := splitSlash(pattern)
	targetSegs := splitSlash(target)
	return matchGlobSegments(patSegs, targetSegs)
}

func splitSlash(value string) []string {
	value = path.Clean(strings.ReplaceAll(value, `\`, "/"))
	if value == "." {
		return nil
	}
	parts := strings.Split(value, "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

func matchGlobSegments(pattern, target []string) bool {
	if len(pattern) == 0 {
		return len(target) == 0
	}
	if pattern[0] == "**" {
		rest := pattern[1:]
		for index := 0; index <= len(target); index++ {
			if matchGlobSegments(rest, target[index:]) {
				return true
			}
		}
		return false
	}
	if len(target) == 0 {
		return false
	}
	matched, err := path.Match(pattern[0], target[0])
	if err != nil || !matched {
		return false
	}
	return matchGlobSegments(pattern[1:], target[1:])
}

// assignLanguages filters the resolved candidates through the explicit lang
// override or extension inference.
func assignLanguages(files []candidate, explicit *Lang) ([]candidate, error) {
	out := make([]candidate, 0, len(files))
	for _, file := range files {
		if explicit != nil {
			if file.explicit || explicit.extensionMatches(file.abs) {
				file.lang = explicit
				out = append(out, file)
			}
			continue
		}
		if file.explicit {
			lang, err := InferLanguage(file.abs)
			if err != nil {
				return nil, err
			}
			file.lang = lang
			out = append(out, file)
			continue
		}
		lang, ok := inferFilterLanguage(file.abs)
		if !ok {
			continue
		}
		file.lang = lang
		out = append(out, file)
	}
	return out, nil
}
