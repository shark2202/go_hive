package testframework

// Helpers shared by the package tests.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func stringPointer(value string) *string { return &value }

func unmarshalJSON(data []byte, value any) error { return json.Unmarshal(data, value) }

func makeDir(t *testing.T, base, relative string) error {
	t.Helper()
	return os.MkdirAll(filepath.Join(base, relative), 0o755)
}
