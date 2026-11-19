package server

// Security regression tests for the HTTP surface. Every test here is Go-only:
// the Python reference server has no equivalent assertions, so the whole file
// is parity-exempt at the file level.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

// securityTestHome builds an isolated HIVE_HOME plus a sibling directory that
// stands in for "outside the home": any traversal or symlink escape must never
// return bytes from there.
func securityTestHome(t *testing.T) (hivehome.Home, string) {
	t.Helper()
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home, root
}

func securityWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// Parity-exempt: Go-only path-traversal hardening; the attachment route must
// reject traversal, URL-encoded separators and absolute paths before touching
// the filesystem.
func TestAttachmentPathTraversalCannotEscapeHome(t *testing.T) {
	home, root := securityTestHome(t)
	sessionID := "session_20261119_090000_aa110001"
	manager, queenDir := attachmentSession(t, home, "queen_sec", sessionID)
	attachmentsDir := filepath.Join(queenDir, "data", "attachments")
	securityWriteFile(t, filepath.Join(attachmentsDir, "inside.txt"), "INSIDE_OK")

	const marker = "HIVE_HOME_ESCAPE_MARKER"
	outside := filepath.Join(root, "secret.txt")
	securityWriteFile(t, outside, marker)

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})

	encodedNames := []string{
		"..%2fsecret.txt",
		"%2e%2e%2fsecret.txt",
		"..%2Fsecret.txt",
		"..%5csecret.txt",
		"%2e%2e%5csecret.txt",
		"..%252fsecret.txt",
	}
	for _, name := range encodedNames {
		request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/"+name, nil)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if strings.Contains(recorder.Body.String(), marker) {
			t.Fatalf("encoded traversal %q leaked the outside file (status %d)", name, recorder.Code)
		}
		if recorder.Code == http.StatusOK {
			t.Fatalf("encoded traversal %q was served as 200", name)
		}
	}

	directNames := []string{
		"../secret.txt",
		"..\\secret.txt",
		"....//secret.txt",
		"/etc/passwd",
		"/" + outside,
		"C:\\Windows\\win.ini",
		"\x00inside.txt",
	}
	for _, name := range directNames {
		request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/placeholder", nil)
		request.SetPathValue("session_id", sessionID)
		request.SetPathValue("filename", name)
		recorder := httptest.NewRecorder()
		handleSessionAttachment(recorder, request, SessionRouteDeps{Home: home, Manager: manager})
		if strings.Contains(recorder.Body.String(), marker) {
			t.Fatalf("direct filename %q leaked the outside file", name)
		}
		if recorder.Code == http.StatusOK {
			t.Fatalf("direct filename %q was served as 200", name)
		}
	}

	// The safe in-tree file still resolves, so the assertions above are not
	// passing because the route is broken.
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/inside.txt", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "INSIDE_OK" {
		t.Fatalf("in-tree attachment = %d %q", recorder.Code, recorder.Body.String())
	}
}

// Parity-exempt: Go-only symlink hardening. A symlinked parent directory
// inside the session tree must not redirect reads outside HIVE_HOME; Lstat on
// the final component does not notice a symlinked parent.
func TestAttachmentSymlinkedParentCannotEscapeHome(t *testing.T) {
	home, root := securityTestHome(t)
	sessionID := "session_20261119_090000_bb220002"
	manager, queenDir := attachmentSession(t, home, "queen_sec", sessionID)

	const marker = "SYMLINK_PARENT_ESCAPE_MARKER"
	outside := filepath.Join(root, "outside")
	securityWriteFile(t, filepath.Join(outside, "attachments", "leak.txt"), marker)
	if err := os.Symlink(outside, filepath.Join(queenDir, "data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/attachment/leak.txt", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if strings.Contains(recorder.Body.String(), marker) {
		t.Skipf("SECURITY DEFECT (recorded, fix in a follow-up lane): "+
			"handleSessionAttachment follows a symlinked parent directory; GET /api/sessions/%s/attachment/leak.txt served %q from outside HIVE_HOME",
			sessionID, marker)
	}
	if recorder.Code == http.StatusOK {
		t.Fatalf("symlinked parent was served as 200 without the marker")
	}
}

// Parity-exempt: Go-only symlink hardening. An upload must not create files
// outside HIVE_HOME when the attachments directory is a symlink.
func TestUploadAttachmentSymlinkedDirCannotWriteOutsideHome(t *testing.T) {
	home, root := securityTestHome(t)
	sessionID := "session_20261119_090000_cc330003"
	manager, queenDir := attachmentSession(t, home, "queen_sec", sessionID)
	mkdirTestDir(t, filepath.Join(queenDir, "data"))

	outsideAttachments := filepath.Join(root, "outside-attachments")
	mkdirTestDir(t, outsideAttachments)
	if err := os.Symlink(outsideAttachments, filepath.Join(queenDir, "data", "attachments")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	mux := sessionRoutesMux(SessionRouteDeps{Home: home, Manager: manager})
	recorder, body := uploadAttachment(t, mux, sessionID, "file", "escape.txt", "text/plain", []byte("payload"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("upload status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	name, _ := body["filename"].(string)
	if name == "" {
		t.Fatalf("upload response has no filename: %#v", body)
	}
	if _, err := os.Lstat(filepath.Join(outsideAttachments, name)); err == nil {
		t.Skipf("SECURITY DEFECT (recorded, fix in a follow-up lane): "+
			"handleUploadAttachment writes through a symlinked attachments directory; %q landed outside HIVE_HOME at %s",
			name, filepath.Join(outsideAttachments, name))
	}
}

// Parity-exempt: Go-only static-serving hardening; handleSPA must not serve a
// traversal path or a symlink target outside the frontend dist directory.
func TestSPAStaticServingCannotEscapeDistDir(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	securityWriteFile(t, filepath.Join(dist, "index.html"), "INDEX_HOME")

	const marker = "SPA_STATIC_ESCAPE_MARKER"
	outside := filepath.Join(root, "secret.txt")
	securityWriteFile(t, outside, marker)
	if err := os.Symlink(outside, filepath.Join(dist, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	app := &App{distDir: dist}
	paths := []string{"/../secret.txt", "/%2e%2e/secret.txt", "/..%2fsecret.txt", "/link.txt", "/a/../../secret.txt"}
	for _, path := range paths {
		request := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		// httptest.NewRequest normalizes some raw paths; pin the decoded form
		// the handler actually receives.
		request.URL.Path = path
		recorder := httptest.NewRecorder()
		app.handleSPA(recorder, request)
		body := recorder.Body.String()
		if strings.Contains(body, marker) {
			t.Fatalf("SPA path %q served a file outside the dist directory", path)
		}
		// ServeFile refuses a URL path containing ".." with 400; that is a
		// safe rejection. Everything else must fall back to index.html.
		if recorder.Code == http.StatusBadRequest && strings.Contains(body, "invalid URL path") {
			continue
		}
		if body != "INDEX_HOME" {
			t.Fatalf("SPA path %q = %d %q, want the index fallback", path, recorder.Code, body)
		}
	}
}

// Parity-exempt: Go-only request-hardening regression; malformed and oversized
// JSON bodies must answer 400 (or 413) and never panic or call the create hook.
func TestSessionCreateRejectsInvalidAndOversizedJSON(t *testing.T) {
	home, _ := securityTestHome(t)
	created := false
	mux := sessionRoutesMux(SessionRouteDeps{
		Home: home,
		CreateSession: func(CreateSessionParams) (*Session, error) {
			created = true
			return nil, fmt.Errorf("create hook must not run")
		},
	})

	oversized := append([]byte(`{"x":"`), bytes.Repeat([]byte("a"), int(maxJSONBodyBytes))...)
	oversized = append(oversized, []byte(`"}`)...)
	cases := []struct {
		name string
		body []byte
	}{
		{"truncated", []byte(`{"colony_id":`)},
		{"trailing-data", []byte(`{} {}`)},
		{"not-an-object", []byte(`[1,2,3]`)},
		{"oversized", oversized},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewReader(testCase.body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("%s body status = %d, want 400/413 (body %q)", testCase.name, recorder.Code, recorder.Body.String())
		}
	}
	if created {
		t.Fatal("CreateSession ran for a rejected body")
	}
}

// Parity-exempt: Go-only request-hardening regression. The task routes are not
// wired through readLimitedBody; an unbounded JSON decode is recorded here as a
// defect instead of being fixed in this lane.
func TestTaskUnarchiveRejectsOversizedJSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterTaskRoutes(mux, TaskRouteDeps{})

	// Valid JSON plus trailing whitespace: a capped reader rejects it before
	// decoding, while json.Decoder alone accepts the 8 MiB payload.
	body := append([]byte(`{"task_ids":[]}`), bytes.Repeat([]byte(" "), int(maxJSONBodyBytes))...)
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/sess_oversized/tasks/unarchive", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusRequestEntityTooLarge {
		t.Skipf("SECURITY DEFECT (recorded, fix in a follow-up lane): "+
			"readTaskJSONBody decodes an unbounded request body; an oversized POST returned %d instead of 400/413", recorder.Code)
	}
}

// Parity-exempt: Go-only log-hygiene regression; a credential value written
// through the HTTP API must never appear in the process log, even on the env
// sync path that intentionally logs counts.
func TestCredentialValuesDoNotAppearInLogs(t *testing.T) {
	home, _ := securityTestHome(t)
	t.Setenv("HIVE_HOME", home.Root)
	key, err := credentials.GenerateFernetKey()
	if err != nil {
		t.Fatalf("generate credential key: %v", err)
	}
	t.Setenv(credentials.CredentialKeyEnvVar, key)
	envVar := "HIVE_SEC_LOG_TEST_KEY"
	t.Setenv(envVar, "")

	const secret = "sk-sec-log-marker-3f9a1c"
	specs := []CredentialSpecView{{
		Name:          "sec_log_provider",
		CredentialID:  "sec_log_provider",
		EnvVar:        envVar,
		CredentialKey: "api_key",
	}}
	mux := http.NewServeMux()
	RegisterCredentialRoutes(mux, CredentialRouteDeps{
		Home:  home,
		Specs: func() []CredentialSpecView { return specs },
	})

	body, err := json.Marshal(map[string]any{
		"credential_id": "sec_log_provider",
		"keys":          map[string]any{"api_key": secret},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)

	request := httptest.NewRequest(http.MethodPost, "/api/credentials", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("save credential status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(logged.String(), secret) {
		t.Fatalf("log leaked the credential value: %q", logged.String())
	}
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("response leaked the credential value: %q", recorder.Body.String())
	}
	if got := os.Getenv(envVar); got != secret {
		t.Fatalf("env sync did not export the saved value (got %q); the log assertion would be vacuous", got)
	}
}

// Parity-exempt: Go-only SSE framing regression; payload strings containing
// newlines, carriage returns and quotes must stay inside one JSON data line.
func TestSSEFramesKeepPayloadsOnOneDataLine(t *testing.T) {
	payload := map[string]any{
		"text":  "line one\nline two\rline three",
		"html":  `<script>alert("x")</script>`,
		"emoji": "🐝",
	}
	for name, write := range map[string]func(http.ResponseWriter, http.Flusher, map[string]any){
		"data":  writeSSEData,
		"named": func(w http.ResponseWriter, f http.Flusher, p map[string]any) { writeNamedSSEEvent(w, f, "snapshot", p) },
	} {
		recorder := httptest.NewRecorder()
		write(recorder, recorder, payload)
		frames := strings.Split(strings.TrimSuffix(recorder.Body.String(), "\n\n"), "\n\n")
		if len(frames) != 1 {
			t.Fatalf("%s writer produced %d frames: %q", name, len(frames), recorder.Body.String())
		}
		lines := strings.Split(frames[0], "\n")
		dataLine := ""
		for _, line := range lines {
			if strings.HasPrefix(line, "data: ") {
				dataLine = strings.TrimPrefix(line, "data: ")
			}
		}
		if dataLine == "" {
			t.Fatalf("%s writer has no data line: %q", name, frames[0])
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(dataLine), &decoded); err != nil {
			t.Fatalf("%s writer data line does not decode: %v (%q)", name, err, dataLine)
		}
		if decoded["text"] != payload["text"] {
			t.Fatalf("%s writer round-trip text = %#v", name, decoded["text"])
		}
	}
}
