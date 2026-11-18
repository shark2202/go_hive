package mcpregistry

import (
	"os"
	"path/filepath"
	"testing"
)

// Parity: core/tests/test_mcp_registry.py::test_get_hive_version_section_aware
func TestGetHiveVersionSectionAware(t *testing.T) {
	tomlPath := filepath.Join(t.TempDir(), "pyproject.toml")
	content := "[tool.something]\nversion = \"9.9.9\"\n\n[project]\nname = \"framework\"\nversion = \"0.7.1\"\n"
	if err := os.WriteFile(tomlPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := hiveVersionFrom("", tomlPath); got != "0.7.1" {
		t.Fatalf("version = %q, want 0.7.1", got)
	}
}

// Parity: core/tests/test_mcp_registry.py::test_get_hive_version_missing_toml
func TestGetHiveVersionMissingTOML(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "pyproject.toml")
	if got := hiveVersionFrom("", missing); got != "unknown" {
		t.Fatalf("version = %q, want unknown", got)
	}
}
