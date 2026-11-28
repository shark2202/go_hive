package lsp

import (
	"context"
	"testing"
	"time"
)

func diagAt(line int, message string) Diagnostic {
	return Diagnostic{
		Range:    Range{Start: Position{Line: line}, End: Position{Line: line, Character: 1}},
		Severity: SeverityError,
		Message:  message,
	}
}

// Parity-exempt: diagnostics version filtering and freshness are the frozen
// D7/A8 rules for the Go-only LSP client; no Python source test exists.
func TestDiagnosticStoreIgnoresStaleVersions(t *testing.T) {
	store := newDiagnosticStore()
	v2 := 2
	store.Publish("file:///a.go", &v2, []Diagnostic{diagAt(0, "current")})
	v1 := 1
	store.Publish("file:///a.go", &v1, []Diagnostic{diagAt(0, "stale")})
	latest, ok := store.Latest("file:///a.go")
	if !ok {
		t.Fatal("missing entry")
	}
	if latest.Version != 2 || len(latest.Items) != 1 || latest.Items[0].Message != "current" {
		t.Fatalf("latest = %+v", latest)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	snapshot := store.Wait(ctx, "file:///a.go", 2, 0)
	if !snapshot.Fresh {
		t.Fatalf("snapshot not fresh: %+v", snapshot)
	}
}

// Parity-exempt: a publish from before the current document version was sent
// must never be reported current (docs/lsp-design.json review focus); no
// upstream test.
func TestDiagnosticStoreRejectsPublishBeforeDocumentSync(t *testing.T) {
	store := newDiagnosticStore()
	version := 1
	store.Publish("file:///a.go", &version, []Diagnostic{diagAt(0, "pre-open")})
	since := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	snapshot := store.WaitSince(ctx, "file:///a.go", 1, 0, since)
	if snapshot.Fresh {
		t.Fatalf("pre-open publish reported current: %+v", snapshot)
	}
}

// Parity-exempt: version-less publish handling protects against a stale empty
// set being reported as clean (D7); no upstream test.
func TestDiagnosticStoreWaitSinceVersionlessPublish(t *testing.T) {
	store := newDiagnosticStore()
	store.Publish("file:///a.go", nil, nil)
	since := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	snapshot := store.WaitSince(ctx, "file:///a.go", 1, 0, since)
	if snapshot.Fresh {
		t.Fatalf("pre-open versionless publish reported fresh: %+v", snapshot)
	}
	if snapshot.Note == "" {
		t.Fatal("stale wait did not explain itself")
	}

	time.Sleep(2 * time.Millisecond)
	store.Publish("file:///a.go", nil, []Diagnostic{diagAt(0, "fresh")})
	wctx, wcancel := context.WithTimeout(context.Background(), time.Second)
	defer wcancel()
	snapshot = store.WaitSince(wctx, "file:///a.go", 1, 0, since)
	if !snapshot.Fresh || len(snapshot.Items) != 1 {
		t.Fatalf("post-open versionless publish = %+v", snapshot)
	}
}
