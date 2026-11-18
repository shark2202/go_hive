package attachments

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: tools/src/aden_tools/utils/attachments.py has no Python tests;
func TestSanitizeBasenameStripsPathsAndForcesExtension(t *testing.T) {
	if got := SanitizeBasename("../../etc/passwd", "", "upload"); got != "passwd" {
		t.Fatalf("path traversal basename = %q, want passwd", got)
	}
	if got := SanitizeBasename(`C:\Users\x\evil.jpg`, ".png", "upload"); got != "evil.png" {
		t.Fatalf("windows path + forced ext = %q, want evil.png", got)
	}
	if got := SanitizeBasename(`a:b*c?.txt`, "", "upload"); got != "a_b_c_.txt" {
		t.Fatalf("reserved chars = %q, want a_b_c_.txt", got)
	}
	if got := SanitizeBasename("", "", "upload"); got != "upload" {
		t.Fatalf("empty name = %q, want upload", got)
	}
}

// Parity-exempt: tools/src/aden_tools/utils/attachments.py has no Python tests;
func TestSanitizeBasenameCollapsesWhitespaceAndCapsLength(t *testing.T) {
	if got := SanitizeBasename("  my   report  ", "", "upload"); got != "my report" {
		t.Fatalf("whitespace = %q, want %q", got, "my report")
	}
	long := strings.Repeat("a", 250) + ".txt"
	got := SanitizeBasename(long, "", "upload")
	if runes := []rune(got); len(runes) != MaxBasenameLen {
		t.Fatalf("capped length = %d, want %d", len(runes), MaxBasenameLen)
	}
	if !strings.HasSuffix(got, ".txt") {
		t.Fatalf("capped name dropped its suffix: %q", got)
	}
}

// Parity-exempt: tools/src/aden_tools/utils/attachments.py has no Python tests;
func TestDisambiguateFilenameSuffixesCollisions(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(first, []byte("x"), 0o644); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if got := DisambiguateFilename(dir, "photo.png"); got != "photo (2).png" {
		t.Fatalf("first collision = %q, want photo (2).png", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "photo (2).png"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write second: %v", err)
	}
	if got := DisambiguateFilename(dir, "photo.png"); got != "photo (3).png" {
		t.Fatalf("second collision = %q, want photo (3).png", got)
	}
	if got := DisambiguateFilename(dir, "fresh.png"); got != "fresh.png" {
		t.Fatalf("no collision = %q, want fresh.png", got)
	}
}

// Parity-exempt: tools/src/aden_tools/utils/attachments.py has no Python tests;
func TestLooksLikeTextRejectsBinaryAndInvalidUTF8(t *testing.T) {
	if !LooksLikeText([]byte("hello ✓\n")) {
		t.Fatal("valid UTF-8 should read as text")
	}
	if LooksLikeText([]byte{'a', 0, 'b'}) {
		t.Fatal("a NUL byte should read as binary")
	}
	if LooksLikeText([]byte{0xff, 0xfe, 0x00}) {
		t.Fatal("invalid UTF-8 should read as binary")
	}
}

// Parity-exempt: tools/src/aden_tools/utils/attachments.py has no Python tests;
func TestTextExtToMIMEKeepsTheSharedAllowlist(t *testing.T) {
	for ext, mime := range map[string]string{
		".md":   "text/markdown",
		".csv":  "text/csv",
		".json": "application/json",
		".go":   "text/x-go",
		".svg":  "image/svg+xml",
	} {
		if got := TextExtToMIME[ext]; got != mime {
			t.Fatalf("TextExtToMIME[%q] = %q, want %q", ext, got, mime)
		}
	}
}
