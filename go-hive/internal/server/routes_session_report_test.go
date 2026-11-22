package server

// Parity-exempt: framework/server/_session_report.py and its route have no
// Python tests; these Go tests pin the 404 branch, body defaults, marker,
// credential scrubbing and archive layout.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity-exempt: upstream has no handler test; pins the 404 branch and error
// text when the resolved session folder does not exist.
func TestReportBundleRouteRejectsMissingSessionFolder(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/session_missing/report-bundle", `{}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["error"] != "session folder not found" {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity-exempt: upstream has no handler test; pins the response envelope,
// temp-dir placement, marker write, stats and scrubbed archive layout.
func TestReportBundleRouteWritesMarkerAndScrubbedArchive(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	sessID := "session_20261118_101010_abcd1234"
	sessionDir := home.QueenSessionDir("default", sessID)
	writeReportFixture(t, sessionDir, "conversations/parts/0000000000.json",
		`{"content":"Authorization: Bearer abcdef1234567890"}`)
	writeReportFixture(t, sessionDir, "data/attachments/photo.png", "\x89PNG\r\n")

	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/"+sessID+"/report-bundle",
		`{"description":"it broke","severity":"high"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["ok"] != true || body["content_type"] != "application/gzip" {
		t.Fatalf("response = %#v", body)
	}
	filename, _ := body["filename"].(string)
	if !regexp.MustCompile(`^report_\d{8}_\d{6}_` + regexp.QuoteMeta(sessID) + `\.tar\.gz$`).MatchString(filename) {
		t.Fatalf("filename = %q", filename)
	}
	bundlePath, _ := body["bundle_path"].(string)
	stored, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if info, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("stat bundle: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("bundle mode = %o, want 600", perm)
	}
	if info, err := os.Stat(filepath.Dir(bundlePath)); err != nil {
		t.Fatalf("stat bundle dir: %v", err)
	} else if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("bundle dir mode = %o, want 700", perm)
	}
	if body["size"] != float64(len(stored)) {
		t.Fatalf("size = %#v, file = %d", body["size"], len(stored))
	}
	sum := sha256.Sum256(stored)
	if body["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 = %#v", body["sha256"])
	}

	marker, _ := body["marker"].(map[string]any)
	if marker["label"] != "bad" || marker["label_source"] != "user_reported" {
		t.Fatalf("marker = %#v", marker)
	}
	if marker["severity"] != "high" || marker["description"] != "it broke" {
		t.Fatalf("marker = %#v", marker)
	}
	storedMarker, err := os.ReadFile(filepath.Join(sessionDir, "user_report.json"))
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	if !bytes.Contains(storedMarker, []byte(`"high"`)) {
		t.Fatalf("marker file = %s", storedMarker)
	}

	stats, _ := body["stats"].(map[string]any)
	if stats["files"] != float64(3) || stats["text_files"] != float64(2) {
		t.Fatalf("stats = %#v", stats)
	}
	if stats["binary_files"] != float64(1) || stats["binary_omitted"] != float64(0) {
		t.Fatalf("stats = %#v", stats)
	}

	entries := readReportTarGz(t, stored)
	part := string(entries["session_"+sessID+"/conversations/parts/0000000000.json"])
	if strings.Contains(part, "abcdef1234567890") || !strings.Contains(part, "Bearer [REDACTED]") {
		t.Fatalf("part was not scrubbed: %q", part)
	}
	if _, ok := entries["session_"+sessID+"/user_report.json"]; !ok {
		t.Fatal("marker missing from bundle")
	}
	if _, ok := entries["session_"+sessID+"/report_manifest.json"]; !ok {
		t.Fatal("manifest missing from bundle")
	}
}

// Parity-exempt: upstream has no handler test; pins queen_resume_from storage-id
// precedence, the archive prefix and marker placement in the live queen_dir.
func TestReportBundleRouteUsesLiveSessionResumeIDAndQueenDir(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	queenDir := filepath.Join(t.TempDir(), "live-session")
	writeReportFixture(t, queenDir, "meta.json", `{"queen_id":"queen_x"}`)

	manager := NewSessionManager()
	manager.AddSession(&Session{
		ID:              "session_live",
		QueenDir:        queenDir,
		QueenResumeFrom: "session_original",
	})
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/session_live/report-bundle", `{"severity":"critical"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	filename, _ := body["filename"].(string)
	if !strings.HasSuffix(filename, "_session_original.tar.gz") {
		t.Fatalf("filename = %q, want resume id suffix", filename)
	}
	marker, _ := body["marker"].(map[string]any)
	if marker["description"] != "" || marker["severity"] != "critical" {
		t.Fatalf("marker = %#v", marker)
	}

	stored, err := os.ReadFile(body["bundle_path"].(string))
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	entries := readReportTarGz(t, stored)
	if _, ok := entries["session_session_original/meta.json"]; !ok {
		t.Fatalf("archive prefix did not use the resume id: %#v", keysOf(entries))
	}
	if _, err := os.Stat(filepath.Join(queenDir, "user_report.json")); err != nil {
		t.Fatalf("marker was not written into queen_dir: %v", err)
	}
}

// Parity-exempt: upstream has no handler test; pins the empty-body fallback and
// the medium substitution for unknown severities.
func TestReportBundleRouteDefaultsMalformedBodyAndBadSeverity(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	sessID := "session_20261118_111111_ffff0000"
	os.MkdirAll(home.QueenSessionDir("default", sessID), 0o755)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/"+sessID+"/report-bundle", `not json`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	marker, _ := body["marker"].(map[string]any)
	if marker["description"] != "" || marker["severity"] != "medium" {
		t.Fatalf("marker = %#v", marker)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/sessions/"+sessID+"/report-bundle", `{"description":"x","severity":"urgent"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	marker, _ = body["marker"].(map[string]any)
	if marker["severity"] != "medium" {
		t.Fatalf("invalid severity = %#v, want medium", marker["severity"])
	}
	if marker["description"] != "x" {
		t.Fatalf("description = %#v", marker["description"])
	}
}

func writeReportFixture(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readReportTarGz(t *testing.T, data []byte) map[string][]byte {
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

func keysOf(entries map[string][]byte) []string {
	out := make([]string, 0, len(entries))
	for name := range entries {
		out = append(out, name)
	}
	return out
}
