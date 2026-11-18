package credentials

// Reliability probes for the credentials index. The index is a derived
// artifact: a truncated or half-written metadata/index.json must never hide
// credentials that still exist as encrypted .enc files, and index write
// failures must surface instead of being swallowed.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
)

// Parity-exempt: fault-injection probe for a corrupted metadata index; the
// Python reference rebuilds the index on open and has no dedicated test.
func TestCorruptCredentialsIndexRebuildsFromDisk(t *testing.T) {
	storage := newEncryptedStorage(t)
	for _, id := range []string{"alpha", "beta"} {
		cred := NewCredentialObject(id)
		cred.Keys.Set(NewCredentialKey("api_key", "value-"+id))
		if err := storage.Save(cred); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}

	indexPath := filepath.Join(storage.BasePath(), "metadata", "index.json")
	if err := os.WriteFile(indexPath, []byte(`{"version":"2.0","credentials":{`), 0o644); err != nil {
		t.Fatalf("corrupt index: %v", err)
	}

	ids, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll after corruption: %v", err)
	}
	sort.Strings(ids)
	if len(ids) != 2 || ids[0] != "alpha" || ids[1] != "beta" {
		t.Fatalf("ListAll after corruption = %v, want [alpha beta] rebuilt from .enc files", ids)
	}

	// A later save must not persist an index that forgets the credentials it
	// just recovered from disk.
	gamma := NewCredentialObject("gamma")
	gamma.Keys.Set(NewCredentialKey("api_key", "value-gamma"))
	if err := storage.Save(gamma); err != nil {
		t.Fatalf("save gamma: %v", err)
	}
	ids, err = storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll after save: %v", err)
	}
	sort.Strings(ids)
	if len(ids) != 3 {
		t.Fatalf("ListAll after save = %v, want all three credentials", ids)
	}
}

// Parity-exempt: reliability probe that a read-only metadata directory is
// reported, not silently swallowed, when the index cannot be written.
func TestCredentialsSaveReportsIndexWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	storage := newEncryptedStorage(t)
	first := NewCredentialObject("first")
	first.Keys.Set(NewCredentialKey("api_key", "value"))
	if err := storage.Save(first); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	metadataDir := filepath.Join(storage.BasePath(), "metadata")
	if err := os.Chmod(metadataDir, 0o555); err != nil {
		t.Fatalf("chmod metadata: %v", err)
	}
	defer os.Chmod(metadataDir, 0o755)

	second := NewCredentialObject("second")
	second.Keys.Set(NewCredentialKey("api_key", "value"))
	err := storage.Save(second)
	if err == nil {
		t.Fatal("Save returned nil while the metadata index was read-only; failure was silent")
	}
	if !os.IsPermission(err) && !os.IsExist(err) {
		t.Logf("save error = %v (permission classification not required)", err)
	}
}

// Parity-exempt: concurrent credential saves must not lose index entries via
// an unlocked read-modify-write of metadata/index.json.
func TestConcurrentCredentialSavesKeepIndexEntries(t *testing.T) {
	storage := newEncryptedStorage(t)
	const writers = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			cred := NewCredentialObject(fmt.Sprintf("cred-%02d", n))
			cred.Keys.Set(NewCredentialKey("api_key", "value"))
			if err := storage.Save(cred); err != nil {
				errs <- err
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent save: %v", err)
	}

	ids, err := storage.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(ids) != writers {
		t.Fatalf("index has %d entries after %d concurrent saves: %v", len(ids), writers, ids)
	}
}
