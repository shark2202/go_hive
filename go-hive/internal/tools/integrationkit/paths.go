package integrationkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// ResolveSafePath expands ~ and enforces the allowed roots. An empty root list
// falls back to the upstream defaults (~/.hive_go and ~/aden/hive/exports).
func (rt *Runtime) ResolveSafePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", errors.New("Path cannot be empty.")
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(os.PathSeparator)))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	roots := rt.Roots
	if len(roots) == 0 {
		roots = DefaultRoots()
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolvedRoot := filepath.Clean(root)
		if absolute == resolvedRoot || strings.HasPrefix(absolute, resolvedRoot+string(os.PathSeparator)) {
			return absolute, nil
		}
	}
	return "", fmt.Errorf("Access denied: '%s' is outside allowed directories. Use absolute paths under ~/.hive_go/ or exports/.", raw)
}

// DefaultRoots is the upstream-compatible sandbox used when Runtime.Roots is
// empty.
func DefaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, hivehome.DefaultDirName),
		filepath.Join(home, "aden", "hive", "exports"),
	}
}
