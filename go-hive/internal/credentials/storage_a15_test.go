package credentials

// A15 edge coverage for the encrypted credential store:
//
//   - permissions: the Go store keeps 0700/0600 even when a directory was
//     removed or created by the Python reference under the default umask
//     (storage-compat SC-04 records the deliberate Go-stricter posture);
//   - index: a corrupt or stale metadata/index.json is rebuilt from the
//     authoritative .enc files on open and before an upsert, so a derived
//     snapshot can never hide credentials that still exist on disk.
//
// Python's _read_index falls back to an empty skeleton on a malformed file and
// its _index_upsert/_index_remove swallow write failures; Go deliberately
// repairs the index and surfaces write failures instead (see reliability_test.go
// for the write-failure half). Those differences are registered here, not
// silently dropped, because the .enc files are authoritative on both sides.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

func saveA15Credential(t *testing.T, storage *EncryptedFileStorage, id, value string) {
	t.Helper()
	cred := NewCredentialObject(id)
	cred.Keys.Set(NewCredentialKey("api_key", value))
	if err := storage.Save(cred); err != nil {
		t.Fatalf("save %s: %v", id, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != want {
		t.Fatalf("%s mode = %o, want %o", path, perm, want)
	}
}

// Parity-exempt: Go-only hardening (storage-compat SC-04). Python's umask
// leaves the store at 0755/0644; the Go port re-applies 0700/0600 on every
// write so a removed directory cannot reopen a readable window.
func TestEncryptedStorageRecreatesPrivateDirectoriesOnWrite(t *testing.T) {
	storage := newEncryptedStorage(t)
	saveA15Credential(t, storage, "first", "one")

	base := storage.BasePath()
	if err := os.RemoveAll(filepath.Join(base, "credentials")); err != nil {
		t.Fatalf("remove credentials dir: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(base, "metadata")); err != nil {
		t.Fatalf("remove metadata dir: %v", err)
	}

	// The next save must recreate both directories with the private modes, not
	// atomicfs's generic 0755 parent.
	saveA15Credential(t, storage, "second", "two")

	for _, dir := range []string{base, filepath.Join(base, "credentials"), filepath.Join(base, "metadata")} {
		assertMode(t, dir, 0o700)
	}
	assertMode(t, storage.credPath("second"), 0o600)
	assertMode(t, storage.indexPath(), 0o600)

	loaded, err := storage.Load("second")
	if err != nil || loaded == nil {
		t.Fatalf("load second after directory recreation = %+v %v", loaded, err)
	}
}

// Parity-exempt: Go-only hardening (storage-compat SC-04). The Python reference
// writes its directories and files under the process umask; opening a shared
// home with Go must tighten those artifacts to the store's private posture.
func TestEncryptedStorageTightensPythonWrittenModesOnOpen(t *testing.T) {
	storage := newEncryptedStorage(t)
	saveA15Credential(t, storage, "python_written", "value")

	base := storage.BasePath()
	for _, dir := range []string{base, filepath.Join(base, "credentials"), filepath.Join(base, "metadata")} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
	}
	for _, file := range []string{storage.credPath("python_written"), storage.indexPath()} {
		if err := os.Chmod(file, 0o644); err != nil {
			t.Fatalf("chmod %s: %v", file, err)
		}
	}

	reopened, err := NewEncryptedFileStorage(base, "")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	for _, dir := range []string{base, filepath.Join(base, "credentials"), filepath.Join(base, "metadata")} {
		assertMode(t, dir, 0o700)
	}
	assertMode(t, reopened.credPath("python_written"), 0o600)
	assertMode(t, reopened.indexPath(), 0o600)
	if loaded, err := reopened.Load("python_written"); err != nil || loaded == nil {
		t.Fatalf("load after tightening = %+v %v", loaded, err)
	}
}

// Parity: core/framework/credentials/storage.py::EncryptedFileStorage._maybe_rebuild_index
// (missing/malformed index at open walks the .enc files). Python returns an
// empty skeleton from _read_index at runtime; Go repairs there too, which the
// comment above records as the deliberate A15 difference.
func TestEncryptedStorageRebuildsCorruptIndexOnOpen(t *testing.T) {
	storage := newEncryptedStorage(t)
	saveA15Credential(t, storage, "alpha", "one")
	saveA15Credential(t, storage, "beta", "two")

	indexPath := storage.indexPath()
	if err := os.WriteFile(indexPath, []byte(`{"version":"2.0","credentials":{`), 0o600); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	reopened, err := NewEncryptedFileStorage(storage.BasePath(), "")
	if err != nil {
		t.Fatalf("reopen with corrupt index: %v", err)
	}
	ids, err := reopened.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Fatalf("ListAll after open rebuild = %v, want [alpha beta]", ids)
	}

	var index IndexEnvelope
	if err := atomicfs.ReadJSON(indexPath, &index); err != nil {
		t.Fatalf("rebuilt index is not valid JSON: %v", err)
	}
	if index.Version != IndexVersion || len(index.Credentials) != 2 {
		t.Fatalf("rebuilt index = %+v", index)
	}
}

// Parity: core/framework/credentials/storage.py::EncryptedFileStorage._index_upsert
// (a stale index version triggers _rebuild_index before the new entry lands).
func TestEncryptedStorageSaveRebuildsStaleIndex(t *testing.T) {
	storage := newEncryptedStorage(t)
	saveA15Credential(t, storage, "alpha", "one")

	// Simulate an index written by an older implementation: valid JSON, old
	// schema, no entries. The next save must not persist this empty shape.
	stale := map[string]any{"version": "1.0", "credentials": map[string]any{}}
	if err := atomicfs.WriteJSON(storage.indexPath(), stale, 0o600); err != nil {
		t.Fatalf("write stale index: %v", err)
	}

	saveA15Credential(t, storage, "beta", "two")
	ids, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Fatalf("ListAll after stale-index save = %v, want [alpha beta]", ids)
	}
}
