package builtin

// search_files: the unified search tool. target='content' regex-greps file
// contents; target='files' lists files matching a glob, newest first. Both
// targets walk the sandbox natively (no shell-out, no dependence on ripgrep)
// and mirror tools/src/aden_tools/file_ops.py's output shape: relative paths,
// line numbers, pagination hints and the same error strings.
//
// The process-level anti-loop guard is ported too: repeating one exact query
// warns on the third consecutive call and blocks on the fourth.

import (
	"context"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultSearchLimit is the page size when limit is omitted.
	DefaultSearchLimit = 50
	// MaxSearchLineLength truncates one reported line, like file_ops.py.
	MaxSearchLineLength = 2000
)

// searchSkipDirs are build/cache directories neither target walks.
var searchSkipDirs = map[string]struct{}{
	".git":         {},
	"__pycache__":  {},
	"node_modules": {},
	".venv":        {},
	".tox":         {},
	".mypy_cache":  {},
	".ruff_cache":  {},
}

// ── anti-loop guard ──────────────────────────────────────────────────────

type searchTrackerEntry struct {
	lastKey     string
	consecutive int
}

var searchTracker = struct {
	sync.Mutex
	entries map[string]*searchTrackerEntry
}{entries: map[string]*searchTrackerEntry{}}

// bumpSearchTracker records one query per task bucket and returns the current
// consecutive count for an identical query.
func bumpSearchTracker(bucket, key string) int {
	if bucket == "" {
		bucket = "_default"
	}
	searchTracker.Lock()
	defer searchTracker.Unlock()
	entry := searchTracker.entries[bucket]
	if entry == nil {
		entry = &searchTrackerEntry{}
		searchTracker.entries[bucket] = entry
	}
	if entry.lastKey == key {
		entry.consecutive++
	} else {
		entry.lastKey = key
		entry.consecutive = 1
	}
	return entry.consecutive
}

// resetSearchTracker clears the process-level guard (tests).
func resetSearchTracker() {
	searchTracker.Lock()
	searchTracker.entries = map[string]*searchTrackerEntry{}
	searchTracker.Unlock()
}

// ── tool ─────────────────────────────────────────────────────────────────

// searchFiles implements search_files.
func (t *Toolbox) searchFiles(ctx context.Context, input map[string]any) (Result, error) {
	pattern, ok := stringArg(input, "pattern")
	if !ok {
		return fail("Error: pattern is required"), nil
	}
	target, _ := stringArg(input, "target")
	if target == "" {
		target = "content"
	}
	switch target {
	case "grep":
		target = "content"
	case "find", "ls":
		target = "files"
	}
	if target != "content" && target != "files" {
		return fail("Error: invalid target '%s'. Use 'content' or 'files'.", target), nil
	}

	outputMode, _ := stringArg(input, "output_mode")
	if outputMode == "" {
		outputMode = "content"
	}
	if outputMode != "content" && outputMode != "files_only" && outputMode != "count" {
		return fail("Error: invalid output_mode '%s'. Use 'content', 'files_only', or 'count'.", outputMode), nil
	}

	pathArg, _ := stringArg(input, "path")
	if pathArg == "" {
		pathArg = "."
	}
	fileGlob, _ := stringArg(input, "file_glob")
	limit := DefaultSearchLimit
	if v, present := input["limit"]; present && v != nil {
		parsed, ok := intArg(input, "limit")
		if !ok {
			return fail("Error: limit must be an integer"), nil
		}
		limit = parsed
	}
	offset := 0
	if v, present := input["offset"]; present && v != nil {
		parsed, ok := intArg(input, "offset")
		if !ok {
			return fail("Error: offset must be an integer"), nil
		}
		offset = parsed
	}
	contextLines := 0
	if v, present := input["context"]; present && v != nil {
		parsed, ok := intArg(input, "context")
		if !ok {
			return fail("Error: context must be an integer"), nil
		}
		contextLines = parsed
	}
	hashline := boolArgOr(input, "hashline", false)
	taskID, _ := stringArg(input, "task_id")

	// The anti-loop key includes everything that changes results, so paging
	// through one query never trips the guard.
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d",
		target, pattern, pathArg, fileGlob, limit, offset, outputMode, contextLines)
	consecutive := bumpSearchTracker(taskID, key)
	if consecutive >= 4 {
		return Result{
			Content: fmt.Sprintf(
				"BLOCKED: this exact search has run %d times in a row. "+
					"Results have NOT changed. Use the information you already have and proceed.",
				consecutive),
			IsError: true,
		}, nil
	}

	root, err := t.rootPath()
	if err != nil {
		return Result{}, err
	}
	resolved, err := resolvePath(root, pathArg)
	if err != nil {
		return fail("Error: %v", err), nil
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	// Output paths are relative to the sandbox root, the way upstream
	// relativizes against its home directory.
	displayRoot := root
	var out string
	if target == "files" {
		out = t.searchFilesTarget(resolved, displayRoot, pattern, limit, offset)
	} else {
		info, statErr := os.Stat(resolved)
		if statErr != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fail("Error: Path not found: %s", pathArg), nil
		}
		out = t.searchContentTarget(resolved, displayRoot, pattern, fileGlob, limit, offset, outputMode, contextLines, hashline)
	}

	if consecutive == 3 {
		out += fmt.Sprintf(
			"\n\n[Warning: this exact search has run %d times consecutively. "+
				"Results have not changed — use what you have instead of re-searching.]",
			consecutive)
	}
	return Result{Content: out, IsError: strings.HasPrefix(out, "Error:")}, nil
}

// searchFilesTarget implements target=files: glob the tree, sort newest first.
func (t *Toolbox) searchFilesTarget(resolved, displayRoot, pattern string, limit, offset int) string {
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "Error: Directory not found: " + resolved
	}
	glob := pattern
	if glob == "" {
		glob = "*"
	}

	type fileEntry struct {
		mod  time.Time
		path string
	}
	var files []fileEntry
	_ = filepath.WalkDir(resolved, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if candidate == resolved {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			if _, skip := searchSkipDirs[name]; skip {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if glob != "*" && !globMatch(glob, name) {
			return nil
		}
		stat, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		files = append(files, fileEntry{mod: stat.ModTime(), path: candidate})
		return nil
	})

	// Newest first; ties break by descending path, matching Python's
	// reverse sort of (mtime, path) tuples.
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mod.Equal(files[j].mod) {
			return files[i].mod.After(files[j].mod)
		}
		return files[i].path > files[j].path
	})

	total := len(files)
	page := pageSlice(files, offset, max(0, limit))
	if len(page) == 0 {
		if total == 0 {
			return "No files found."
		}
		return fmt.Sprintf("No files at offset %d (total: %d).", offset, total)
	}
	lines := make([]string, 0, len(page))
	for _, file := range page {
		lines = append(lines, relativeDisplayPath(displayRoot, file.path))
	}
	out := strings.Join(lines, "\n")
	nextOffset := offset + len(page)
	if total > nextOffset {
		out += fmt.Sprintf(
			"\n\n[Hint: showing %d of %d files. Use offset=%d for more, or narrow with a more specific glob.]",
			len(page), total, nextOffset)
	}
	return out
}

// searchContentTarget implements target=content.
func (t *Toolbox) searchContentTarget(
	resolved, displayRoot, pattern, fileGlob string,
	limit, offset int, outputMode string, contextLines int, hashline bool,
) string {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return "Error: invalid regex: " + err.Error()
	}
	cap := max(1, limit)

	maxBytes := t.maxReadSize
	if maxBytes <= 0 {
		maxBytes = MaxReadFileBytes
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil {
		return "Error: Path not found: " + resolved
	}
	var candidates []string
	if info.Mode().IsRegular() {
		candidates = []string{resolved}
	} else {
		candidates = walkSearchFiles(resolved, true, fileGlob, int64(maxBytes))
	}

	// files_only / count need per-file aggregation.
	if outputMode == "files_only" || outputMode == "count" {
		type fileCount struct {
			path  string
			count int
		}
		var items []fileCount
		for _, candidate := range candidates {
			content, ok := readSearchFile(candidate, int64(maxBytes))
			if !ok {
				continue
			}
			count := 0
			for _, line := range pythonLines(content) {
				if compiled.MatchString(strings.TrimRightFunc(line, unicode.IsSpace)) {
					count++
				}
			}
			if count > 0 {
				items = append(items, fileCount{path: candidate, count: count})
			}
		}
		total := len(items)
		page := pageSlice(items, offset, cap)
		if len(page) == 0 {
			return noMatchesMessage(total, offset)
		}
		lines := make([]string, 0, len(page))
		for _, item := range page {
			rel := relativeDisplayPath(displayRoot, item.path)
			if outputMode == "count" {
				lines = append(lines, fmt.Sprintf("%s:%d", rel, item.count))
			} else {
				lines = append(lines, rel)
			}
		}
		out := strings.Join(lines, "\n")
		nextOffset := offset + len(page)
		if total > nextOffset {
			out += fmt.Sprintf("\n\n[Hint: showing %d of %d. Use offset=%d for more.]", len(page), total, nextOffset)
		}
		return out
	}

	// output_mode == "content"
	var matches []string
	for _, candidate := range candidates {
		rel := relativeDisplayPath(displayRoot, candidate)
		content, ok := readSearchFile(candidate, int64(maxBytes))
		if !ok {
			continue
		}
		lines := pythonLines(content)
		for i, raw := range lines {
			stripped := strings.TrimRightFunc(raw, unicode.IsSpace)
			if !compiled.MatchString(stripped) {
				continue
			}
			if contextLines > 0 {
				lo := max(0, i-contextLines)
				hi := min(len(lines), i+1+contextLines)
				group := make([]string, 0, hi-lo)
				for j := lo; j < hi; j++ {
					marker := "-"
					if j == i {
						marker = ":"
					}
					group = append(group, fmt.Sprintf("%s:%d%s%s",
						rel, j+1, marker, truncateRunes(strings.TrimRightFunc(lines[j], unicode.IsSpace), MaxSearchLineLength)))
				}
				matches = append(matches, strings.Join(group, "\n"))
				continue
			}
			if hashline {
				matches = append(matches, fmt.Sprintf("%s:%d:%s|%s", rel, i+1, computeLineHash(stripped), stripped))
				continue
			}
			matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, i+1, truncateRunes(stripped, MaxSearchLineLength)))
		}
	}

	total := len(matches)
	page := pageSlice(matches, offset, cap)
	if len(page) == 0 {
		return noMatchesMessage(total, offset)
	}
	separator := "\n"
	if contextLines > 0 {
		separator = "\n\n"
	}
	out := strings.Join(page, separator)
	nextOffset := offset + len(page)
	if total > nextOffset {
		out += fmt.Sprintf(
			"\n\n[Hint: showing %d of %d matches. Use offset=%d for more, or narrow with file_glob/pattern.]",
			len(page), total, nextOffset)
	}
	return out
}

// walkSearchFiles walks root and returns the files a search may read. Hidden
// directories and the shared skip set are pruned; hidden files are optional.
func walkSearchFiles(root string, includeHiddenFiles bool, fileGlob string, maxBytes int64) []string {
	var files []string
	_ = filepath.WalkDir(root, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if candidate == root {
				return nil
			}
			name := entry.Name()
			if _, skip := searchSkipDirs[name]; skip {
				return fs.SkipDir
			}
			if strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !includeHiddenFiles && strings.HasPrefix(name, ".") {
			return nil
		}
		if fileGlob != "" && !globMatch(fileGlob, name) {
			return nil
		}
		if maxBytes > 0 {
			if info, err := entry.Info(); err != nil || info.Size() > maxBytes {
				return nil
			}
		}
		files = append(files, candidate)
		return nil
	})
	sort.Strings(files)
	return files
}

// readSearchFile reads a UTF-8 text file with invalid bytes dropped, matching
// Python's errors="ignore" behaviour.
func readSearchFile(path string, maxBytes int64) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	limit := maxBytes
	if limit <= 0 {
		limit = MaxReadFileBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return "", false
	}
	return strings.ToValidUTF8(string(data), ""), true
}

// pythonLines splits content the way Python's file.readlines() does: universal
// newlines, no trailing empty element.
func pythonLines(content string) []string {
	if content == "" {
		return nil
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	if strings.HasSuffix(content, "\n") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// computeLineHash is hashline.py's compute_line_hash: CRC32 mod 65536, four
// lowercase hex characters, trailing spaces/tabs ignored.
func computeLineHash(line string) string {
	stripped := strings.TrimRight(line, " \t")
	return fmt.Sprintf("%04x", crc32.ChecksumIEEE([]byte(stripped))%65536)
}

// relativeDisplayPath renders candidate relative to root; a cross-volume or
// otherwise unrelatable path keeps its absolute form.
func relativeDisplayPath(root, candidate string) string {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return candidate
	}
	return rel
}

// globMatch applies an fnmatch-style glob to a base name. A malformed glob
// falls back to exact equality instead of failing the whole search.
func globMatch(pattern, name string) bool {
	matched, err := path.Match(pattern, name)
	if err != nil {
		return pattern == name
	}
	return matched
}

// truncateRunes keeps at most limit characters and marks the cut with "...".
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}

// pageSlice returns source[offset:offset+size], tolerating negative offsets.
func pageSlice[T any](source []T, offset, size int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(source) || size <= 0 {
		return nil
	}
	end := offset + size
	if end > len(source) {
		end = len(source)
	}
	return source[offset:end]
}

// noMatchesMessage is the empty-page message both content modes share.
func noMatchesMessage(total, offset int) string {
	if total == 0 {
		return "No matches found."
	}
	return fmt.Sprintf("No matches at offset %d (total: %d).", offset, total)
}
