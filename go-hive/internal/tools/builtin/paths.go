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
	// ErrWriteSafeRoot is returned when a write target is outside every
	// configured write_safe_root. The message body mirrors file_ops.py:
	// "write denied: '<original>' is outside the configured
	// write_safe_root(s) (<roots>)".
	ErrWriteSafeRoot = errors.New("write denied")
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
	abs, err := absoluteCandidate(root, raw)
	if err != nil {
		return "", err
	}
	return resolveUnder(root, raw, abs)
}

// resolveWritePath is resolvePath plus the optional write_safe_root ceiling
// ported from file_ops.py::_FilePolicy.write_path:
//
//   - With no configured roots the sandbox root remains the only ceiling
//     (unchanged Go behaviour).
//   - With roots configured, a write must land under at least one of them.
//     A path outside the sandbox but inside a configured root is allowed —
//     that is what makes the upstream ceiling usable for hosted deployments —
//     and anything outside every root is denied with the upstream wording.
//
// Reads never consult the safe roots; file_ops.py restricts reads only through
// its credential deny list, which the sandbox subsumes here.
func resolveWritePath(root, raw string, safeRoots []string) (string, error) {
	if len(safeRoots) == 0 {
		return resolvePath(root, raw)
	}
	abs, err := absoluteCandidate(root, raw)
	if err != nil {
		return "", err
	}
	if path, err := resolveUnder(root, raw, abs); err == nil {
		return path, nil
	} else if !errors.Is(err, ErrPathEscape) {
		return "", err
	}
	for _, safeRoot := range safeRoots {
		if path, err := resolveUnder(safeRoot, raw, abs); err == nil {
			return path, nil
		} else if !errors.Is(err, ErrPathEscape) {
			return "", err
		}
	}
	return "", fmt.Errorf("%w: '%s' is outside the configured write_safe_root(s) (%s)",
		ErrWriteSafeRoot, raw, strings.Join(safeRoots, ", "))
}

// absoluteCandidate applies the shared path model without any containment
// check: ~ expands to the OS home, relative paths anchor to root, and the
// result is absolute.
func absoluteCandidate(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", ErrPathEmpty
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	candidate, err := expandTilde(raw)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	return abs, nil
}

// expandTilde expands a leading ~ or ~/ (or ~\) to the OS user home.
func expandTilde(raw string) (string, error) {
	if raw != "~" && !strings.HasPrefix(raw, "~/") && !strings.HasPrefix(raw, `~\`) {
		return raw, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	if raw == "~" {
		return home, nil
	}
	return filepath.Join(home, raw[2:]), nil
}

// resolveUnder enforces one containment ceiling. abs must live under root once
// symlinks are resolved; when abs does not exist yet, its deepest existing
// ancestor must.
func resolveUnder(root, raw, abs string) (string, error) {
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
	// resolve inside the ceiling, otherwise a symlinked parent could redirect
	// the later open or create. The returned spelling is the realpath of that
	// ancestor plus the remaining components, so a path keeps one canonical
	// form before and after it is created (macOS reports /var and
	// /private/var for the same directory, and the read-state cache keys on
	// the resolved string).
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
	remainder, err := filepath.Rel(ancestor, abs)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	return filepath.Join(realAncestor, remainder), nil
}

// SetWriteSafeRoots configures the optional write ceiling from file_ops.py's
// register_file_tools(write_safe_root=...). Each root is normalized like the
// upstream _norm (expand ~, make absolute, resolve symlinks). Passing no roots
// disables the ceiling; an unreadable root is reported instead of silently
// widening or narrowing the policy.
func (t *Toolbox) SetWriteSafeRoots(roots ...string) error {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		clean, err := normalizeWriteRoot(root)
		if err != nil {
			return err
		}
		normalized = append(normalized, clean)
	}
	t.stateMu.Lock()
	t.safeWriteRoots = normalized
	t.stateMu.Unlock()
	return nil
}

// writeSafeRoots returns a snapshot of the configured ceiling.
func (t *Toolbox) writeSafeRoots() []string {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if len(t.safeWriteRoots) == 0 {
		return nil
	}
	out := make([]string, len(t.safeWriteRoots))
	copy(out, t.safeWriteRoots)
	return out
}

// normalizeWriteRoot canonicalizes one configured write ceiling the way
// file_ops.py::_norm does: expand ~, resolve against the process working
// directory when relative, and realpath the result when it exists.
func normalizeWriteRoot(raw string) (string, error) {
	candidate, err := expandTilde(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("builtin: resolve write_safe_root %q: %w", raw, err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs), nil
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
