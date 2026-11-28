package lsp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordedSync struct {
	opened  []Document
	changed []Document
	closed  []string
}

func (r *recordedSync) DidOpen(doc Document) error {
	r.opened = append(r.opened, doc)
	return nil
}

func (r *recordedSync) DidChange(doc Document) error {
	r.changed = append(r.changed, doc)
	return nil
}

func (r *recordedSync) DidClose(uri string) error {
	r.closed = append(r.closed, uri)
	return nil
}

func newTestDocumentManager(t *testing.T, sync DocumentSync) (*DocumentManager, string) {
	t.Helper()
	root := t.TempDir()
	manager, err := NewDocumentManager(root, time.Minute, sync)
	if err != nil {
		t.Fatalf("NewDocumentManager: %v", err)
	}
	return manager, root
}

// Parity-exempt: document synchronization (open/full change/idle close) is a
// new Go-only LSP requirement from docs/lsp-design.json R5/A7.
func TestDocumentOpenChangeExternalEditAndIdleClose(t *testing.T) {
	sync := &recordedSync{}
	manager, root := newTestDocumentManager(t, sync)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	manager.SetClock(func() time.Time { return now })

	doc, err := manager.Open("main.go", "go")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if doc.Version != 1 || doc.LanguageID != "go" {
		t.Fatalf("doc = %+v", doc)
	}
	if len(sync.opened) != 1 || len(sync.changed) != 0 {
		t.Fatalf("sync = %+v", sync)
	}
	// Reopening unchanged sends nothing.
	if _, err := manager.Open("main.go", "go"); err != nil {
		t.Fatal(err)
	}
	if len(sync.opened) != 1 || len(sync.changed) != 0 {
		t.Fatalf("unchanged reopen notified: %+v", sync)
	}
	// External edit becomes a full-text didChange with a new version.
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err = manager.Open("main.go", "go")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version != 2 || !strings.Contains(doc.Text, "func main") {
		t.Fatalf("external edit doc = %+v", doc)
	}
	if len(sync.changed) != 1 || sync.changed[0].Version != 2 {
		t.Fatalf("changed = %+v", sync.changed)
	}
	// Explicit change; a stale version is rejected before notification.
	doc, err = manager.Change("main.go", "package main\n", 3)
	if err != nil {
		t.Fatalf("Change: %v", err)
	}
	if doc.Version != 3 {
		t.Fatalf("version = %d", doc.Version)
	}
	if _, err := manager.Change("main.go", "package other\n", 3); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("stale change err = %v", err)
	}
	if len(sync.changed) != 2 {
		t.Fatalf("stale change notified the server: %+v", sync.changed)
	}
	// Idle close.
	now = now.Add(2 * time.Minute)
	closed := manager.CloseIdle(now)
	if len(closed) != 1 || len(sync.closed) != 1 {
		t.Fatalf("closed = %v / %v", closed, sync.closed)
	}
	if _, ok := manager.Document("main.go"); ok {
		t.Fatal("document still tracked after idle close")
	}
	// A closed document reopens with a fresh didOpen and version 1.
	doc, err = manager.Open("main.go", "go")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if doc.Version != 1 || len(sync.opened) != 2 {
		t.Fatalf("reopen doc = %+v, opened = %d", doc, len(sync.opened))
	}
}

// Parity-exempt: same-size/same-mtime external edits must still reconcile;
// this is the R5 disk-reconciliation case with no upstream test.
func TestDocumentReconcilesSameSizeSameMtimeEdit(t *testing.T) {
	sync := &recordedSync{}
	manager, root := newTestDocumentManager(t, sync)
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Open("main.go", "go"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package oink\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	doc, err := manager.Open("main.go", "go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Text, "oink") || doc.Version != 2 {
		t.Fatalf("doc = %+v", doc)
	}
}

// Parity-exempt: path safety (workspace escape, symlink escape, non-regular
// file) is R10/A11 and has no Python counterpart.
func TestDocumentRejectsUnsafePaths(t *testing.T) {
	manager, root := newTestDocumentManager(t, nil)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.go")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"../outside.go",
		outside,
		link,
		dir,
	}
	for _, candidate := range cases {
		if _, err := manager.Open(candidate, "go"); err == nil {
			t.Fatalf("unsafe path accepted: %s", candidate)
		} else if !IsCode(err, CodePathRejected) {
			t.Fatalf("%s error = %v, want path_rejected", candidate, err)
		}
	}
	if _, err := manager.Open("missing.go", "go"); !IsCode(err, CodePathRejected) {
		t.Fatalf("missing file error = %v", err)
	}
}

// Parity-exempt: symlinks that stay inside the workspace are allowed; no
// upstream analogue.
func TestDocumentAllowsInsideSymlink(t *testing.T) {
	manager, root := newTestDocumentManager(t, nil)
	target := filepath.Join(root, "real.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias.go")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	doc, err := manager.Open("alias.go", "go")
	if err != nil {
		t.Fatalf("Open inside symlink: %v", err)
	}
	realTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Path != realTarget {
		t.Fatalf("path = %s, want real target %s", doc.Path, realTarget)
	}
}

// Parity-exempt: large-file refusal is a new resource bound (R5/R8).
func TestDocumentRejectsOversizedFile(t *testing.T) {
	manager, root := newTestDocumentManager(t, nil)
	path := filepath.Join(root, "big.go")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxDocumentBytes + 1); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if _, err := manager.Open("big.go", "go"); !IsCode(err, CodePathRejected) {
		t.Fatalf("oversized error = %v", err)
	}
}
