package claudecode

import (
	"os"
	"path/filepath"
	"strings"
)

// The inbox and conversations record are this package's own disk surface
// (HIVE_HOME/claude_code/<queen>/). The two helpers below mirror the mail
// store's own guards; they are duplicated rather than exported from queenmail
// so that each package owns the files it writes and a change to one disk
// surface cannot silently move the other.

// validInboxID keeps a letter id (which becomes a filename under a queen's
// inbox) to one path segment. The inbox file is local state, but a tampered
// line must not reach outside the inbox directory.
func validInboxID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\\x00")
}

// writeFileAtomic replaces path with data through a temp file in the same
// directory, mode 0600, as the mail store writes its letters: a reader sees
// either the old file or the new one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}
