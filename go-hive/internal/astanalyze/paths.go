package astanalyze

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/astgrep"
)

// sourceFile is one candidate the analyzer will parse.
type sourceFile struct {
	Rel  string
	Abs  string
	Lang *astgrep.Lang
	Kind FileKind
	Data []byte
}

var errStopWalk = errors.New("astanalyze: stop walk")

// rootPath resolves the session sandbox root to a real, absolute directory.
func rootPath(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("sandbox root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox root %s is not a directory", real)
	}
	return filepath.Clean(real), nil
}

// resolveUserPath requires an absolute path and resolves it to a real path
// inside root. Relative paths are rejected because go_hive has no project cwd.
func resolveUserPath(root, raw string, create bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &Error{Kind: ErrInvalidInput, Message: "path is required"}
	}
	if !filepath.IsAbs(raw) {
		return "", &Error{Kind: ErrPathNotAbsolute, Message: "path must be absolute: " + raw}
	}
	realRoot, err := rootPath(root)
	if err != nil {
		return "", &Error{Kind: ErrInvalidInput, Message: err.Error()}
	}
	candidate := filepath.Clean(raw)
	ancestor := candidate
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
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
		return "", &Error{Kind: ErrPathEscape, Message: "cannot resolve path: " + raw}
	}
	rel, err := filepath.Rel(ancestor, candidate)
	if err != nil {
		return "", &Error{Kind: ErrPathEscape, Message: "cannot resolve path: " + raw}
	}
	real := filepath.Clean(filepath.Join(realAncestor, rel))
	if !within(realRoot, real) {
		return "", &Error{Kind: ErrPathEscape, Message: "path escapes the workspace root: " + raw}
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(real), 0o700); err != nil {
			return "", &Error{Kind: ErrWriteFailed, Message: err.Error(), Err: err}
		}
	}
	return real, nil
}

// ResolvePath exposes the adapter's absolute-path/root-containment rule.
func ResolvePath(root, raw string, create bool) (string, error) {
	return resolveUserPath(root, raw, create)
}

// WriteReport writes an already-rendered report atomically.
func WriteReport(path string, data []byte) error { return atomicWrite(path, data) }

// SpillReport writes a report under <root>/data/tool-output and returns the
// absolute path. The extension follows format (json or md).
func SpillReport(root, format string, data []byte) (string, error) {
	ext := "json"
	if strings.EqualFold(strings.TrimSpace(format), "markdown") || strings.EqualFold(strings.TrimSpace(format), "md") {
		ext = "md"
	}
	sum := sha256.Sum256(data)
	name := "ast_analyze_" + hex.EncodeToString(sum[:8]) + "." + ext
	return resolveUserPath(root, filepath.Join("data", "tool-output", name), true)
}

func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func openUnder(root, target string, flag int, perm os.FileMode) (*os.File, error) {
	realRoot, err := rootPath(root)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(target)
	if !within(realRoot, clean) {
		return nil, &Error{Kind: ErrPathEscape, Message: "path escapes the workspace root: " + target}
	}
	rel, err := filepath.Rel(realRoot, clean)
	if err != nil || rel == "." {
		return nil, &Error{Kind: ErrPathEscape, Message: "invalid file path: " + target}
	}
	handle, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return handle.OpenFile(rel, flag, perm)
}

func readSource(root, target string, maxBytes int64) ([]byte, bool, error) {
	file, err := openUnder(root, target, os.O_RDONLY, 0)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, false, nil
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, false, nil
	}
	return data, true, nil
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// collectFiles walks the absolute analysis path, applies ignore/extension/size
// caps, and returns parsed-source candidates plus warnings.
func collectFiles(ctx context.Context, cfg Config, root, analysisPath, explicitLang string, includeTests, includeUI bool) ([]sourceFile, []string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	realRoot, err := rootPath(root)
	if err != nil {
		return nil, nil, &Error{Kind: ErrInvalidInput, Message: err.Error()}
	}
	target := analysisPath
	if strings.TrimSpace(target) == "" {
		target = realRoot
	}
	target, err = resolveUserPath(realRoot, target, false)
	if err != nil {
		return nil, nil, err
	}
	var explicit *astgrep.Lang
	if strings.TrimSpace(explicitLang) != "" {
		explicit, err = astgrep.ResolveLanguage(explicitLang)
		if err != nil {
			return nil, nil, &Error{Kind: ErrUnsupportedLanguage, Message: err.Error()}
		}
	}
	extensions := map[string]struct{}{}
	if explicit != nil {
		for _, ext := range explicit.Extensions() {
			extensions[ext] = struct{}{}
		}
	}
	var warnings []string
	var files []sourceFile
	totalBytes := int64(0)
	add := func(p string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if len(files) >= cfg.MaxFiles {
			warnings = append(warnings, fmt.Sprintf("stopped after max_files (%d)", cfg.MaxFiles))
			return errStopWalk
		}
		info, statErr := os.Lstat(p)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if base := filepath.Base(p); base == "go.mod" || base == "go.sum" {
			return nil
		}
		if info.Size() > cfg.MaxFileBytes {
			warnings = append(warnings, fmt.Sprintf("skipped %s: larger than max_file_bytes", displayRel(realRoot, p)))
			return nil
		}
		lang := explicit
		if lang == nil {
			var inferErr error
			lang, inferErr = astgrep.InferLanguage(p)
			if inferErr != nil {
				return nil
			}
		} else if _, ok := extensions[strings.ToLower(filepath.Ext(p))]; !ok {
			return nil
		}
		if !languageAllowed(cfg.Languages, lang.Name()) {
			return nil
		}
		rel, relErr := filepath.Rel(realRoot, p)
		if relErr != nil || !within(realRoot, p) {
			return nil
		}
		rel = filepath.ToSlash(rel)
		kind := classifyKind(rel)
		if kind == FileTest && !includeTests {
			return nil
		}
		if kind == FileUI && !includeUI {
			return nil
		}
		totalBytes += info.Size()
		if totalBytes > cfg.MaxTotalBytes {
			warnings = append(warnings, fmt.Sprintf("stopped after max_total_bytes (%d)", cfg.MaxTotalBytes))
			return errStopWalk
		}
		data, ok, readErr := readSource(realRoot, p, cfg.MaxFileBytes)
		if readErr != nil {
			warnings = append(warnings, fmt.Sprintf("skipped %s: %v", rel, readErr))
			return nil
		}
		if !ok {
			warnings = append(warnings, fmt.Sprintf("skipped %s: binary or oversized", rel))
			return nil
		}
		files = append(files, sourceFile{Rel: rel, Abs: p, Lang: lang, Kind: kind, Data: data})
		return nil
	}

	if strings.ContainsAny(target, "*?[") {
		matches, globErr := filepath.Glob(target)
		if globErr != nil {
			return nil, nil, &Error{Kind: ErrInvalidInput, Message: globErr.Error()}
		}
		for _, match := range matches {
			if statErr := add(match); statErr == errStopWalk {
				err = errStopWalk
				break
			}
		}
	} else if info, statErr := os.Stat(target); statErr != nil {
		return nil, nil, &Error{Kind: ErrInvalidInput, Message: "path not found: " + analysisPath}
	} else if info.IsDir() {
		err = filepath.WalkDir(target, func(p string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if entry != nil && entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				if p != target && ignoredDir(realRoot, p, cfg.Ignore) {
					return fs.SkipDir
				}
				return nil
			}
			return add(p)
		})
	} else if info.Mode().IsRegular() {
		err = add(target)
	}
	if ctx.Err() != nil {
		return nil, nil, &Error{Kind: ErrCancelled, Message: "ast_analyze cancelled", Err: ctx.Err()}
	}
	if err != nil && !errors.Is(err, errStopWalk) {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, warnings, &Error{Kind: ErrNoSourceFiles, Message: "no supported source files found"}
	}
	return files, warnings, nil
}

func displayRel(root, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

func languageAllowed(allowed []string, lang string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(strings.TrimSpace(item), lang) {
			return true
		}
	}
	return false
}

func ignoredDir(root, dir string, extras []string) bool {
	base := filepath.Base(dir)
	if isDefaultIgnored(base) {
		return true
	}
	rel, _ := filepath.Rel(root, dir)
	rel = filepath.ToSlash(rel)
	for _, item := range extras {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if item == base || item == rel {
			return true
		}
		if strings.ContainsAny(item, "*?[") {
			if ok, _ := path.Match(item, rel); ok {
				return true
			}
		}
	}
	return false
}

func isDefaultIgnored(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build", "out",
		".venv", "venv", "__pycache__", ".tox", ".mypy_cache", ".ruff_cache",
		".pytest_cache", ".cache", "target":
		return true
	}
	return false
}

func classifyKind(rel string) FileKind {
	lower := strings.ToLower(rel)
	base := path.Base(lower)
	isTest := strings.HasSuffix(base, "_test.go") ||
		strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.")
	for _, segment := range strings.Split(lower, "/") {
		if segment == "test" || segment == "tests" || segment == "__tests__" || segment == "e2e" {
			isTest = true
		}
	}
	if isTest {
		return FileTest
	}
	if strings.HasSuffix(base, ".tsx") || strings.HasSuffix(base, ".jsx") {
		return FileUI
	}
	return FileSource
}
