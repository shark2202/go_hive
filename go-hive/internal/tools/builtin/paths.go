package builtin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	// ErrPathEmpty is returned when a tool call omits its path.
	ErrPathEmpty = errors.New("path is required")
	// ErrPathEscape is returned when a path resolves outside the sandbox root.
	ErrPathEscape = errors.New("path escapes the sandbox root")
)

// rootPath resolves the sandbox root to a clean, symlink-free absolute path.
func (t *Toolbox) rootPath() (string, error) {
	raw := strings.TrimSpace(t.root)
	if raw == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("builtin: resolve working directory: %w", err)
		}
		raw = cwd
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve sandbox root %q: %w", raw, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve sandbox root %q: %w", raw, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("builtin: stat sandbox root %q: %w", real, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("builtin: sandbox root %q is not a directory", real)
	}
	return filepath.Clean(real), nil
}

// resolvePath validates raw against root and returns the path to use on disk.
// Relative paths are anchored to root; absolute paths are accepted only when
// they stay inside root. Symlinks are always followed and rejected when their
// target leaves root, including for paths that do not exist yet (the deepest
// existing ancestor is checked).
func resolvePath(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}

	real, err := filepath.EvalSymlinks(abs)
	switch {
	case err == nil:
		if !isWithin(root, real) {
			return "", fmt.Errorf("%w: %s", ErrPathEscape, raw)
		}
		return real, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}

	// The path does not exist yet: the deepest existing ancestor still has to
	// resolve inside the sandbox, otherwise a symlinked parent could redirect
	// the later open or create.
	ancestor, err := deepestExisting(abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	realAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	if !isWithin(root, realAncestor) {
		return "", fmt.Errorf("%w: %s", ErrPathEscape, raw)
	}
	return abs, nil
}

// deepestExisting walks up from path until it finds an entry that exists.
func deepestExisting(path string) (string, error) {
	dir := path
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		dir = parent
	}
}

// isWithin reports whether path is root or a descendant of root.
func isWithin(root, path string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
