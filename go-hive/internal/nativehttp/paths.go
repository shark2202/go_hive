package nativehttp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	errPathEmpty  = errors.New("path is required")
	errPathEscape = errors.New("path escapes the sandbox root")
)

// pathError carries the closed error_kind for a caller-supplied path. The
// adapter and core must not invent a new kind for a bad path.
type pathError struct {
	kind    string
	message string
}

func (e *pathError) Error() string { return e.message }

// pathFailure maps a path error to the result envelope's closed kind set.
func pathFailure(err error, fallback string) (string, string) {
	var target *pathError
	if errors.As(err, &target) {
		return target.kind, target.message
	}
	return fallback, err.Error()
}

// resolveUserPath resolves a caller-supplied path. Unlike the internal
// auto-spill path, a user path must be absolute: go_hive has no project cwd,
// so a relative path is ambiguous and is rejected. The absolute path must still
// resolve inside the session sandbox root.
func resolveUserPath(root, raw string, create bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", &pathError{kind: ErrInvalidInput, message: "path is required"}
	}
	if !filepath.IsAbs(raw) {
		return "", &pathError{kind: ErrPathNotAbsolute, message: "path must be absolute: " + raw}
	}
	resolved, err := resolveUnder(root, raw, create)
	if err != nil {
		if errors.Is(err, errPathEscape) {
			return "", &pathError{kind: ErrPathEscape, message: err.Error()}
		}
		kind := ErrReadFailed
		if create {
			kind = ErrWriteFailed
		}
		return "", &pathError{kind: kind, message: err.Error()}
	}
	return resolved, nil
}

// resolveUnder resolves raw under root and rejects symlink escapes. Relative
// paths are anchored to root; absolute paths must stay inside it. create
// creates the parent directories for a write target.
func resolveUnder(root, raw string, create bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errPathEmpty
	}
	realRoot, err := rootPath(root)
	if err != nil {
		return "", err
	}
	candidate := raw
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(realRoot, candidate)
	}
	candidate = filepath.Clean(candidate)
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
		return "", fmt.Errorf("resolve %s: %w", raw, err)
	}
	rel, err := filepath.Rel(ancestor, candidate)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", raw, err)
	}
	realCandidate := filepath.Clean(filepath.Join(realAncestor, rel))
	if !within(realRoot, realCandidate) {
		return "", errPathEscape
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(realCandidate), 0o700); err != nil {
			return "", fmt.Errorf("create parent of %s: %w", raw, err)
		}
	}
	return realCandidate, nil
}

func rootPath(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("sandbox root is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox root: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve sandbox root %s: %w", abs, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("stat sandbox root %s: %w", real, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("sandbox root %s is not a directory", real)
	}
	return filepath.Clean(real), nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// openUnder opens path (already produced by resolveUnder) through an os.Root
// anchored at the sandbox root. Unlike os.Open/os.OpenFile, os.Root refuses any
// symlink that resolves outside the tree, so a link planted between resolution
// and open cannot redirect the read or the write outside the session sandbox.
func openUnder(root, path string, flag int, perm os.FileMode) (*os.File, error) {
	realRoot, err := rootPath(root)
	if err != nil {
		return nil, err
	}
	clean := filepath.Clean(path)
	if !within(realRoot, clean) {
		return nil, errPathEscape
	}
	rel, err := filepath.Rel(realRoot, clean)
	if err != nil || rel == "." {
		return nil, errPathEscape
	}
	handle, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, fmt.Errorf("open sandbox root %s: %w", realRoot, err)
	}
	defer handle.Close()
	return handle.OpenFile(rel, flag, perm)
}

// readFileUnder is os.ReadFile constrained to the sandbox root.
func readFileUnder(root, path string) ([]byte, error) {
	file, err := openUnder(root, path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

// spillPath returns the destination for an auto-spilled response body. A
// caller-supplied path wins; otherwise the file lands under
// <root>/data/tool-output.
func spillPath(root, requested, method, url string) (string, error) {
	if strings.TrimSpace(requested) != "" {
		return resolveUserPath(root, requested, true)
	}
	sum := sha256.Sum256([]byte(method + "\x00" + url + "\x00" + time.Now().UTC().Format(time.RFC3339Nano)))
	name := "http_" + hex.EncodeToString(sum[:8]) + ".bin"
	return resolveUnder(root, filepath.Join("data", "tool-output", name), true)
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
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
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
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}
