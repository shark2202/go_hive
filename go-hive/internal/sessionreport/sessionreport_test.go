package sessionreport

// Parity-exempt: framework/server/_session_report.py has no Python tests; these
// Go tests pin the redaction regexes, marker format and bundle layout.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Parity-exempt: upstream _session_report.py has no tests; pins all eight
// credential regexes and the debug-context passthrough.
func TestScrubCredentialsStripsTokensAndKeepsDebugContext(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "jwt",
			in:   "auth eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			want: "auth [REDACTED_JWT]",
		},
		{
			name: "bearer",
			in:   "Authorization: Bearer abc.def-123=xyz",
			want: "Authorization: Bearer [REDACTED]",
		},
		{
			name: "api key assignment",
			in:   `api_key = "abcdefghijklmnop1234"`,
			want: `api_key = "[REDACTED_SECRET]"`,
		},
		{
			name: "secret key with colon",
			in:   "client_secret:abcdefghijklmnop12",
			want: "client_secret:[REDACTED_SECRET]",
		},
		{
			name: "oauth callback",
			in:   "see https://example.com/callback?code=abc123&state=xyz now",
			want: "see [REDACTED_AUTH_URL] now",
		},
		{
			name: "openai style key",
			in:   "key sk-abcdefghijklmnop1234",
			want: "key [REDACTED_KEY]",
		},
		{
			name: "github token",
			in:   "key ghp_abcdefghijklmnop1234",
			want: "key [REDACTED_KEY]",
		},
		{
			name: "slack token",
			in:   "key xoxb-1234567890-abc",
			want: "key [REDACTED_KEY]",
		},
		{
			name: "google api key",
			in:   "key AIzaSyA1234567890abcdefghijklmnopqrst",
			want: "key [REDACTED_KEY]",
		},
		{
			name: "debug context kept",
			in:   "Alice <alice@example.com> opened /Users/alice/report.png",
			want: "Alice <alice@example.com> opened /Users/alice/report.png",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ScrubCredentials(tc.in); got != tc.want {
				t.Fatalf("ScrubCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := ScrubCredentials(""); got != "" {
		t.Fatalf("empty text = %q, want empty", got)
	}
}

// Parity-exempt: upstream has no marker tests; pins label/label_source/reporter
// fields, the medium default for invalid severities and overwrite semantics.
func TestWriteUserReportMarkerDefaultsSeverityAndOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions", "session_x")
	marker, err := WriteUserReportMarker(dir, "first", "bogus")
	if err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if marker.Severity != "medium" {
		t.Fatalf("invalid severity = %q, want medium", marker.Severity)
	}

	data, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode marker: %v", err)
	}
	if stored["label"] != "bad" || stored["label_source"] != "user_reported" {
		t.Fatalf("marker labels = %#v", stored)
	}
	if stored["reporter"] != "desktop" || stored["severity"] != "medium" {
		t.Fatalf("marker reporter/severity = %#v", stored)
	}
	if reported, _ := stored["reported_at"].(string); reported == "" {
		t.Fatalf("reported_at missing: %#v", stored)
	}

	second, err := WriteUserReportMarker(dir, "second", "high")
	if err != nil {
		t.Fatalf("overwrite marker: %v", err)
	}
	if second.Description != "second" || second.Severity != "high" {
		t.Fatalf("second marker = %#v", second)
	}
	data, err = os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		t.Fatalf("read second marker: %v", err)
	}
	if !bytes.Contains(data, []byte(`"second"`)) {
		t.Fatalf("marker was not overwritten: %s", data)
	}
}

// Parity-exempt: upstream has no bundle tests; pins the data/reports exclusion
// before counting, text scrubbing, binary passthrough and manifest stats.
func TestBuildSessionReportBundleScrubsAndKeepsTree(t *testing.T) {
	sessionDir := t.TempDir()
	writeTestFile(t, sessionDir, "conversations/parts/0000000000.json",
		`{"content":"Bearer abcdef1234567890"}`)
	writeTestFile(t, sessionDir, "notes.md", "plain notes")
	writeTestFile(t, sessionDir, "data/attachments/photo.png", "\x89PNG\r\n")
	writeTestFile(t, sessionDir, "data/reports/old.tar.gz", "old bundle")

	marker := &UserReportMarker{Label: "bad", LabelSource: "user_reported", Severity: "high"}
	bundle, stats, err := BuildSessionReportBundle(
		sessionDir, "session_abc", "boom", "high", marker)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if stats.Files != 3 {
		t.Fatalf("files = %d, want 3", stats.Files)
	}
	if stats.TextFiles != 2 || stats.BinaryFiles != 1 {
		t.Fatalf("text/binary = %d/%d, want 2/1", stats.TextFiles, stats.BinaryFiles)
	}
	if len(Sha256Hex(bundle)) != 64 {
		t.Fatalf("sha256 length = %d", len(Sha256Hex(bundle)))
	}

	entries := readTarGz(t, bundle)
	if _, ok := entries["session_session_abc/data/reports/old.tar.gz"]; ok {
		t.Fatal("previously generated report bundle was re-packed")
	}
	part := string(entries["session_session_abc/conversations/parts/0000000000.json"])
	if strings.Contains(part, "abcdef1234567890") || !strings.Contains(part, "Bearer [REDACTED]") {
		t.Fatalf("part was not scrubbed: %q", part)
	}
	if string(entries["session_session_abc/data/attachments/photo.png"]) != "\x89PNG\r\n" {
		t.Fatal("binary attachment was not preserved verbatim")
	}

	manifestRaw, ok := entries["session_session_abc/report_manifest.json"]
	if !ok {
		t.Fatal("manifest missing from bundle")
	}
	var document map[string]any
	if err := json.Unmarshal(manifestRaw, &document); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if document["session_id"] != "session_abc" || document["severity"] != "high" {
		t.Fatalf("manifest header = %#v", document)
	}
	if document["redaction"] != "credentials_only" {
		t.Fatalf("redaction = %#v", document["redaction"])
	}
	manifestMarker, _ := document["marker"].(map[string]any)
	if manifestMarker["label"] != "bad" {
		t.Fatalf("manifest marker = %#v", document["marker"])
	}
	manifestStats, _ := document["stats"].(map[string]any)
	if manifestStats["files"] != float64(3) || manifestStats["text_files"] != float64(2) {
		t.Fatalf("manifest stats = %#v", manifestStats)
	}
	if omitted, ok := manifestStats["omitted_paths"].([]any); !ok || len(omitted) != 0 {
		t.Fatalf("omitted_paths should be a present empty list: %#v", manifestStats["omitted_paths"])
	}
}

// Parity-exempt: upstream read uses errors="replace"; this pins the U+FFFD
// substitution for invalid bytes inside text files.
func TestBuildSessionReportBundleReplacesInvalidUTF8(t *testing.T) {
	sessionDir := t.TempDir()
	writeTestFile(t, sessionDir, "log.txt", "ok\xffbad")
	bundle, _, err := BuildSessionReportBundle(sessionDir, "s1", "", "low", nil)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	entries := readTarGz(t, bundle)
	text := string(entries["session_s1/log.txt"])
	if !strings.Contains(text, "\uFFFD") {
		t.Fatalf("invalid byte was not replaced: %q", text)
	}
}

// Parity-exempt: upstream SEVERITIES has no dedicated test; pins the exact
// accepted set and the case-sensitive rejection of everything else.
func TestSeverityValid(t *testing.T) {
	for _, severity := range []string{"low", "medium", "high", "critical"} {
		if !SeverityValid(severity) {
			t.Fatalf("%q should be accepted", severity)
		}
	}
	for _, severity := range []string{"", "LOW", "urgent"} {
		if SeverityValid(severity) {
			t.Fatalf("%q should be rejected", severity)
		}
	}
}

// Parity-exempt: upstream has no budget test; pins the single-binary cap, the
// omitted_paths reporting and the absence of the file from the archive.
func TestBuildSessionReportBundleOmitsOversizedBinary(t *testing.T) {
	sessionDir := t.TempDir()
	writeTestFile(t, sessionDir, "small.bin", "ok")
	writeTestFile(t, sessionDir, "big.bin", "0123456789")

	previous := maxSingleBinary
	maxSingleBinary = 4
	defer func() { maxSingleBinary = previous }()

	bundle, stats, err := BuildSessionReportBundle(sessionDir, "s2", "", "low", nil)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if stats.BinaryOmitted != 1 || len(stats.OmittedPaths) != 1 || stats.OmittedPaths[0] != "big.bin" {
		t.Fatalf("omitted = %d %v", stats.BinaryOmitted, stats.OmittedPaths)
	}
	if stats.BinaryFiles != 1 {
		t.Fatalf("binary files = %d, want 1", stats.BinaryFiles)
	}
	entries := readTarGz(t, bundle)
	if _, ok := entries["session_s2/big.bin"]; ok {
		t.Fatal("oversized binary was packed")
	}
	if _, ok := entries["session_s2/small.bin"]; !ok {
		t.Fatal("small binary was not packed")
	}
	var manifest map[string]any
	if err := json.Unmarshal(entries["session_s2/report_manifest.json"], &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	manifestStats := manifest["stats"].(map[string]any)
	omitted := manifestStats["omitted_paths"].([]any)
	if len(omitted) != 1 || omitted[0] != "big.bin" {
		t.Fatalf("manifest omitted_paths = %#v", omitted)
	}
}

// Parity-exempt: upstream json.dumps defaults to ensure_ascii=True; this pins
// the \uXXXX escaping, surrogate pairs and the disabled HTML escaping.
func TestWriteUserReportMarkerEscapesNonASCIIAndKeepsHTML(t *testing.T) {
	dir := t.TempDir()
	if _, err := WriteUserReportMarker(dir, "café 😀 <b>&", "low"); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	text := string(data)
	for _, want := range []string{`\u00e9`, `\ud83d\ude00`, `<b>&`} {
		if !strings.Contains(text, want) {
			t.Fatalf("marker missing %q: %s", want, text)
		}
	}
	for _, unwanted := range []string{"é", "😀", `\u003c`, `\u0026`} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("marker contains %q: %s", unwanted, text)
		}
	}
}

// Parity-exempt: pure helper with no upstream test; pins a published SHA-256
// vector so the hex encoding cannot drift.
func TestSha256HexMatchesKnownVector(t *testing.T) {
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Sha256Hex([]byte("abc")); got != want {
		t.Fatalf("Sha256Hex(abc) = %s, want %s", got, want)
	}
}

// Parity-exempt: no upstream test; a symlink inside the session dir must not
// pull an outside file into a bundle the user is about to upload.
func TestBuildSessionReportBundleSkipsSymlinkedFiles(t *testing.T) {
	sessionDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	writeTestFile(t, sessionDir, "notes.md", "inside")
	if err := os.Symlink(outside, filepath.Join(sessionDir, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	bundle, stats, err := BuildSessionReportBundle(sessionDir, "session_sym", "", "low", nil)
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	if stats.Files != 1 || stats.TextFiles != 1 {
		t.Fatalf("stats = %+v, want only the regular file", stats)
	}
	entries := readTarGz(t, bundle)
	if _, ok := entries["session_session_sym/leak.txt"]; ok {
		t.Fatal("symlink target was packed into the bundle")
	}
	if _, ok := entries["session_session_sym/notes.md"]; !ok {
		t.Fatal("regular file missing from bundle")
	}
}

// Parity-exempt: no upstream test; the id prefixes every archive member, so a
// value that can step out of the archive must be refused.
func TestBuildSessionReportBundleRejectsUnsafeSessionID(t *testing.T) {
	sessionDir := t.TempDir()
	writeTestFile(t, sessionDir, "notes.md", "inside")
	for _, bad := range []string{"", ".", "..", "../escape", "a/b", "a\\b", "a\x00b"} {
		if _, _, err := BuildSessionReportBundle(sessionDir, bad, "", "low", nil); err == nil {
			t.Fatalf("BuildSessionReportBundle accepted session id %q", bad)
		}
	}
}

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readTarGz(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	entries := map[string][]byte{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		entries[header.Name] = content
	}
	return entries
}
