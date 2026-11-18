package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/attachments"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

func uploadAttachment(t *testing.T, mux *http.ServeMux, sessionID, fieldName, filename, contentType string, content []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/upload-attachment", &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func attachmentSession(t *testing.T, home hivehome.Home, queen, sessionID string) (*SessionManager, string) {
	t.Helper()
	dir := home.QueenSessionDir(queen, sessionID)
	mkdirTestDir(t, dir)
	manager := NewSessionManager()
	session := NewSession(sessionID, nil)
	session.QueenDir = dir
	manager.AddSession(session)
	return manager, dir
}

// Parity-exempt: Go-only symlink hardening; the attachment route serves files
// from the session tree, and a planted symlink must not expose its target.
func TestSessionAttachmentDoesNotFollowSymlink(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sessionID := "session_20261118_090000_bbbb0002"
	manager, queenDir := attachmentSession(t, home, "queen_x", sessionID)
	attachmentsDir := filepath.Join(queenDir, "data", "attachments")
	mkdirTestDir(t, attachmentsDir)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("TOP_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(attachmentsDir, "leak.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/leak.txt", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "TOP_SECRET") {
		t.Fatal("attachment route served a symlink target")
	}
}

// Parity-exempt: Go-only symlink hardening; upload must not create through a
// pre-planted symlink in the attachments directory.
func TestUploadAttachmentDoesNotFollowSymlink(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sessionID := "session_20261118_090000_cccc0003"
	manager, queenDir := attachmentSession(t, home, "queen_x", sessionID)
	attachmentsDir := filepath.Join(queenDir, "data", "attachments")
	mkdirTestDir(t, attachmentsDir)
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.txt"), filepath.Join(attachmentsDir, "evil.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	recorder, body := uploadAttachment(t, mux, sessionID, "file", "evil.txt", "text/plain", []byte("payload"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["filename"] == "evil.txt" {
		t.Fatal("upload reused the symlink name")
	}
	if _, err := os.Lstat(filepath.Join(attachmentsDir, "evil.txt")); err != nil {
		t.Fatalf("symlink disappeared: %v", err)
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_upload_attachment
func TestUploadAttachmentStoresAndServesBack(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sessionID := "session_20261118_090000_aaaa0001"
	manager, queenDir := attachmentSession(t, home, "queen_x", sessionID)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	content := "# Notes\n\nhello attachment\n"
	recorder, body := uploadAttachment(t, mux, sessionID, "file", "notes.md", "text/markdown", []byte(content))
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["filename"] != "notes.md" {
		t.Fatalf("filename = %#v, want notes.md", body["filename"])
	}
	if body["path"] != "data/attachments/notes.md" {
		t.Fatalf("path = %#v, want the POSIX data/attachments form", body["path"])
	}
	if body["session_id"] != sessionID || body["original_name"] != "notes.md" {
		t.Fatalf("identity fields = %#v", body)
	}
	if body["content_type"] != "text/markdown" {
		t.Fatalf("content_type = %#v", body["content_type"])
	}
	if body["extracted_text"] != content {
		t.Fatalf("extracted_text = %#v, want the file body", body["extracted_text"])
	}
	if images, ok := body["page_images"].([]any); !ok || len(images) != 0 {
		t.Fatalf("page_images = %#v, want an empty list", body["page_images"])
	}

	stored := filepath.Join(queenDir, "data", "attachments", "notes.md")
	if saved, err := os.ReadFile(stored); err != nil || string(saved) != content {
		t.Fatalf("stored file = %q, %v", saved, err)
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/notes.md", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET attachment status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "text/markdown" {
		t.Fatalf("Content-Type = %q, want text/markdown", got)
	}
	if response.Body.String() != content {
		t.Fatalf("served body = %q", response.Body.String())
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_upload_attachment
func TestUploadAttachmentRejectsBadRequestsAndDisambiguates(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sessionID := "session_20261118_090000_bbbb0002"
	manager, queenDir := attachmentSession(t, home, "queen_x", sessionID)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	recorder, body := uploadAttachment(t, mux, "session_20261118_090000_ffff9999", "file", "x.txt", "text/plain", []byte("x"))
	if recorder.Code != http.StatusNotFound || body["error"] != "session not found" {
		t.Fatalf("unknown session = %d %#v, want 404 session not found", recorder.Code, body)
	}

	recorder, body = uploadAttachment(t, mux, sessionID, "attachment", "x.txt", "text/plain", []byte("x"))
	if recorder.Code != http.StatusBadRequest || body["error"] != "expected a 'file' field" {
		t.Fatalf("wrong field = %d %#v, want 400 expected a 'file' field", recorder.Code, body)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/upload-attachment", strings.NewReader("plain"))
	nonMultipart := httptest.NewRecorder()
	mux.ServeHTTP(nonMultipart, request)
	if nonMultipart.Code != http.StatusBadRequest {
		t.Fatalf("non-multipart status = %d, want 400", nonMultipart.Code)
	}

	_, first := uploadAttachment(t, mux, sessionID, "file", "photo.png", "image/png", []byte("png-1"))
	_, second := uploadAttachment(t, mux, sessionID, "file", "photo.png", "image/png", []byte("png-2"))
	if first["filename"] != "photo.png" || second["filename"] != "photo (2).png" {
		t.Fatalf("collision names = %#v / %#v", first["filename"], second["filename"])
	}
	for name, want := range map[string]string{"photo.png": "png-1", "photo (2).png": "png-2"} {
		stored := filepath.Join(queenDir, "data", "attachments", name)
		if saved, err := os.ReadFile(stored); err != nil || string(saved) != want {
			t.Fatalf("%s = %q, %v; want %q", name, saved, err, want)
		}
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_upload_attachment
func TestUploadAttachmentForcesExtensionFromContentType(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sessionID := "session_20261118_090000_cccc0003"
	manager, _ := attachmentSession(t, home, "queen_x", sessionID)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	_, body := uploadAttachment(t, mux, sessionID, "file", "photo.jpg", "image/png", []byte("\x89PNG"))
	if body["filename"] != "photo.png" {
		t.Fatalf("forced filename = %#v, want photo.png", body["filename"])
	}
	_, body = uploadAttachment(t, mux, sessionID, "file", "README", "application/octet-stream", []byte("hi"))
	if body["filename"] != "README.bin" {
		t.Fatalf("unknown type filename = %#v, want README.bin", body["filename"])
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_upload_attachment
func TestUploadAttachmentBuildsBoundedPreviews(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	sessionID := "session_20261118_090000_dddd0004"
	manager, _ := attachmentSession(t, home, "queen_x", sessionID)
	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	csvContent := "name,score\nalice,1\nbob,2\n"
	_, body := uploadAttachment(t, mux, sessionID, "file", "scores.csv", "text/csv", []byte(csvContent))
	preview, _ := body["extracted_text"].(string)
	wantCSV := "[CSV file: scores.csv, 2 rows, 2 columns]\nname | score\n--- | ---\nalice | 1\nbob | 2"
	if preview != wantCSV {
		t.Fatalf("csv preview = %q, want %q", preview, wantCSV)
	}

	long := strings.Repeat("a", textPreviewCapChars+1024)
	_, body = uploadAttachment(t, mux, sessionID, "file", "long.log", "text/plain", []byte(long))
	preview, _ = body["extracted_text"].(string)
	if !strings.HasSuffix(preview, "\n... (preview truncated)") {
		t.Fatalf("long preview tail = %q", preview)
	}
	if got := len([]rune(strings.TrimSuffix(preview, "\n... (preview truncated)"))); got != textPreviewCapChars {
		t.Fatalf("long preview length = %d, want %d", got, textPreviewCapChars)
	}

	_, body = uploadAttachment(t, mux, sessionID, "file", "icon.svg", "image/svg+xml", []byte("<svg/>"))
	if body["extracted_text"] != "" {
		t.Fatalf("svg preview = %#v, want empty", body["extracted_text"])
	}
}

// Parity-exempt: framework/server/routes_sessions.py::handle_session_attachment
func TestSessionAttachmentRejectsTraversalAndUnknownNames(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	mux := sessionRoutesMux(SessionRouteDeps{Home: home})
	sessionID := "session_20261118_090000_eeee0005"

	traversal := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/x", nil)
	traversal.SetPathValue("session_id", sessionID)
	traversal.SetPathValue("filename", "../secret")
	traversalResponse := httptest.NewRecorder()
	handleSessionAttachment(traversalResponse, traversal, SessionRouteDeps{Home: home})
	if traversalResponse.Code != http.StatusBadRequest || traversalResponse.Body.String() != "Invalid filename" {
		t.Fatalf("traversal = %d %q", traversalResponse.Code, traversalResponse.Body.String())
	}

	missing := httptest.NewRecorder()
	mux.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/nope.txt", nil))
	if missing.Code != http.StatusNotFound || missing.Body.String() != "Attachment not found" {
		t.Fatalf("missing = %d %q", missing.Code, missing.Body.String())
	}

	if attachmentMIMEMap[".ts"] != "text/plain" {
		t.Fatalf("attachmentMIMEMap[.ts] = %q, want text/plain", attachmentMIMEMap[".ts"])
	}
	if attachments.TextExtToMIME[".ts"] != "text/x-typescript" {
		t.Fatalf("TextExtToMIME[.ts] = %q, want text/x-typescript", attachments.TextExtToMIME[".ts"])
	}
	if attachmentMIMEMap[".unknown"] != "" {
		t.Fatalf("unknown ext should fall back to application/octet-stream")
	}
}

// Parity-exempt: framework/server/routes_sessions.py::_colony_root_for_session_dir
func TestAttachmentCandidateDirsFindsColonyRoot(t *testing.T) {
	colonySession := filepath.Join("/hive", "colonies", "acme", "queens", "q", "sessions", "s1")
	if got := colonyRootForSessionDir(colonySession); got != filepath.Join("/hive", "colonies", "acme") {
		t.Fatalf("colony root = %q", got)
	}
	dmSession := filepath.Join("/hive", "queens", "default", "sessions", "s1")
	if got := colonyRootForSessionDir(dmSession); got != "" {
		t.Fatalf("dm colony root = %q, want empty", got)
	}

	dirs := attachmentCandidateDirs(colonySession)
	want := []string{
		filepath.Join(colonySession, "data", "attachments"),
		filepath.Join(colonySession, "attachments"),
		colonySession,
		filepath.Join("/hive", "colonies", "acme"),
	}
	if len(dirs) != len(want) {
		t.Fatalf("candidate dirs = %#v", dirs)
	}
	for i := range want {
		if dirs[i] != want[i] {
			t.Fatalf("candidate dirs[%d] = %q, want %q", i, dirs[i], want[i])
		}
	}
}
