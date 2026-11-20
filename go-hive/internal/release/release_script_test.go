// Package release_test guards the release packaging contract without building
// all six platforms: the scripts must keep shipping both CLIs.
package release_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readPackagingFile(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join("..", "..", "..", rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// Parity-exempt: release.sh packaging guard; the Python tree has no release
// script, and the worker prompt requires hive-cred to exist after install.
func TestReleaseScriptBuildsAndShipsCredCLI(t *testing.T) {
	script := readPackagingFile(t, "scripts/release.sh")
	for _, want := range []string{"./cmd/hive\n", "./cmd/hive-cred\n", "hive-cred.exe"} {
		if !strings.Contains(script, want) {
			t.Fatalf("scripts/release.sh lost %q; every archive must build and ship hive and hive-cred", want)
		}
	}
}

// Parity-exempt: install.sh packaging guard; a release without hive-cred must
// fail installation rather than leave workers calling a missing binary.
func TestInstallScriptInstallsCredCLI(t *testing.T) {
	script := readPackagingFile(t, "install.sh")
	for _, want := range []string{"hive-cred", "archive does not contain $cred_bin_name"} {
		if !strings.Contains(script, want) {
			t.Fatalf("install.sh lost %q; it must install hive-cred or fail loudly", want)
		}
	}
}
