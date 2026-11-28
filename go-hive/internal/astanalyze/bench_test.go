package astanalyze

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkAnalyzeSynthetic measures a small synthetic repository. It is a
// harness for future large-repo tuning, not a release gate.
func BenchmarkAnalyzeSynthetic(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 200; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		body := fmt.Sprintf("package pkg%d\n\nfunc Fn%d() {}\n", i, i)
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	cfg := DefaultConfig()
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Analyze(context.Background(), cfg, root, Request{Path: root}); err != nil {
			b.Fatal(err)
		}
	}
}
