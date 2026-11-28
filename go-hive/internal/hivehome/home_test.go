package hivehome

import (
	"os"
	"path/filepath"
	"testing"
)

// Parity: core/tests/test_hive_home_isolation.py::test_import_time_paths_are_in_a_temporary_directory
func TestResolveEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvHome, dir)
	h, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if h.Root != dir {
		t.Fatalf("root = %s, want %s", h.Root, dir)
	}
	if err := h.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{h.QueensDir(), h.ColoniesDir(), h.MemoriesDir(), h.CredentialsDir(), h.SecretsDir()} {
		if info, err := os.Stat(p); err != nil || !info.IsDir() {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	for _, p := range []string{h.CredentialsDir(), h.SecretsDir()} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s mode = %o, want 700", p, perm)
		}
	}
	col := h.Colony("alpha")
	want := filepath.Join(dir, "colonies", "alpha", "tracker", "tracker.db")
	if col.TrackerDB != want {
		t.Fatalf("tracker = %s, want %s", col.TrackerDB, want)
	}
}

// Parity: framework/config.py::_resolve_hive_home — Path(override).expanduser().
func TestResolveExpandsTildeLikePython(t *testing.T) {
	usr, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvHome, "~/hive-tilde-check")
	h, err := Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(usr, "hive-tilde-check")
	if h.Root != want {
		t.Fatalf("root = %s, want %s (a literal ~ directory must not be created)", h.Root, want)
	}
}

// Parity-exempt: empty-root validation is a Go guard with no Python counterpart.
func TestResolveAtRejectsEmpty(t *testing.T) {
	if _, err := ResolveAt(""); err == nil {
		t.Fatal("empty root must fail")
	}
}

// Parity-exempt: colony path binding determinism has no Python pytest counterpart.
func TestColonyBindingStable(t *testing.T) {
	h, _ := ResolveAt(t.TempDir())
	a := h.Colony("c")
	b := h.Colony("c")
	if a != b {
		t.Fatalf("binding drifted: %+v vs %+v", a, b)
	}
}
