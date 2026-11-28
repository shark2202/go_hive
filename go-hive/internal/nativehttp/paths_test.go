package nativehttp

import (
	"os"
	"path/filepath"
	"testing"
)

// openUnder must refuse a symlink that points outside the sandbox even when the
// link itself sits inside it; resolveUnder's checks happen earlier, so this
// covers a link planted between resolution and open.
// Parity-exempt: Go-only path confinement helper; no Python counterpart.
func TestOpenUnderRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	realRoot, err := rootPath(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(realRoot, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if file, err := openUnder(root, link, os.O_RDONLY, 0); err == nil {
		_ = file.Close()
		t.Fatal("openUnder followed a symlink outside the root")
	}

	inside := filepath.Join(realRoot, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := openUnder(root, inside, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("openUnder inside root: %v", err)
	}
	_ = file.Close()
}
