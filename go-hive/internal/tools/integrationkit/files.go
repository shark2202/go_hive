package integrationkit

// File helpers shared by the file-backed integrations (CSV, Excel, attach
// file). They are deliberately dependency-free os/path helpers; sandbox
// enforcement itself stays on Runtime.ResolveSafePath.

import (
	"os"
	"strings"
)

// MapRows accepts the shapes JSON and Go callers produce for a list of
// objects: a typed slice, a []any of objects, or a single object.
func MapRows(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{typed}
	default:
		return nil
	}
}

// FileExists reports whether path is an existing regular file (directories
// count as absent).
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// FileSize returns the size of a regular file.
func FileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// HasSuffixFold is a case-insensitive strings.HasSuffix.
func HasSuffixFold(value, suffix string) bool {
	return len(value) >= len(suffix) && strings.EqualFold(value[len(value)-len(suffix):], suffix)
}
