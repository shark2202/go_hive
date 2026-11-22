package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type colonyTarEntry struct {
	Name    string
	Body    []byte
	Dir     bool
	Symlink string
}

func buildColonyTar(t *testing.T, entries []colonyTarEntry, gzipMode bool) []byte {
	t.Helper()
	buffer := &bytes.Buffer{}
	var writer *tar.Writer
	var gz *gzip.Writer
	if gzipMode {
		gz = gzip.NewWriter(buffer)
		writer = tar.NewWriter(gz)
	} else {
		writer = tar.NewWriter(buffer)
	}
	for _, entry := range entries {
		header := &tar.Header{Name: entry.Name, Mode: 0o644}
		switch {
		case entry.Symlink != "":
			header.Typeflag = tar.TypeSymlink
			header.Linkname = entry.Symlink
			header.Mode = 0o777
			if err := writer.WriteHeader(header); err != nil {
				t.Fatalf("write symlink header: %v", err)
			}
		case entry.Dir:
			header.Typeflag = tar.TypeDir
			header.Mode = 0o755
			if err := writer.WriteHeader(header); err != nil {
				t.Fatalf("write dir header: %v", err)
			}
		default:
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(entry.Body))
			if err := writer.WriteHeader(header); err != nil {
				t.Fatalf("write header: %v", err)
			}
			if _, err := writer.Write(entry.Body); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			t.Fatalf("close gzip: %v", err)
		}
	}
	return buffer.Bytes()
}

func colonyTestDeps(t *testing.T) (ColonyRouteDeps, hivehome.Home) {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return ColonyRouteDeps{Home: home, StagingDir: filepath.Join(home.Root, "uploads")}, home
}

func colonyMux(deps ColonyRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterColonyRoutes(mux, deps)
	return mux
}

func jsonBody(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(data)
}

func colonyRequest(t *testing.T, method, target string, body any) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = jsonBody(t, body)
	}
	return httptest.NewRequest(method, target, reader)
}

func multipartImportRequest(t *testing.T, target, filename string, data []byte, fields map[string]string) *http.Request {
	t.Helper()
	buffer := &bytes.Buffer{}
	writer := multipart.NewWriter(buffer)
	if filename != "" {
		part, err := writer.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	for _, key := range keys {
		if err := writer.WriteField(key, fields[key]); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(buffer.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func serveColonyRequest(mux *http.ServeMux, request *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func postColonyTar(t *testing.T, mux *http.ServeMux, archive []byte, filename string, fields map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return serveColonyRequest(mux, multipartImportRequest(t, "/api/colonies/import", filename, archive, fields))
}

func nestedFiles(t *testing.T, body map[string]any, root string) int {
	t.Helper()
	byRoot, ok := body["by_root"].(map[string]any)
	if !ok {
		t.Fatalf("by_root missing: %v", body)
	}
	entry, ok := byRoot[root].(map[string]any)
	if !ok {
		t.Fatalf("by_root[%q] missing: %v", root, byRoot)
	}
	value, ok := entry["files"].(float64)
	if !ok {
		t.Fatalf("by_root[%q].files not a number: %v", root, entry)
	}
	return int(value)
}

// Parity: framework/server/tests/test_colonies_import.py::test_happy_path_imports_colony
func TestHappyPathImportsColony(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/metadata.json", Body: []byte(`{"colony_id":"x_daily"}`)},
		{Name: "x_daily/scripts/run.sh", Body: []byte("#!/bin/sh\necho hi\n")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if body["name"] != "x_daily" || body["files_imported"] != float64(2) {
		t.Fatalf("body = %v", body)
	}
	data, err := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "metadata.json"))
	if err != nil || string(data) != `{"colony_id":"x_daily"}` {
		t.Fatalf("metadata = %q, err %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "x_daily", "scripts", "run.sh")); err != nil {
		t.Fatalf("run.sh missing: %v", err)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_name_override
func TestNameOverride(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/file.txt", Body: []byte("hi")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", map[string]string{"name": "other_name"})
	if recorder.Code != http.StatusCreated || body["name"] != "other_name" {
		t.Fatalf("status = %d body = %v", recorder.Code, body)
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "other_name", "file.txt")); string(data) != "hi" {
		t.Fatalf("file = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "x_daily")); !os.IsNotExist(err) {
		t.Fatalf("x_daily should not exist")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_existing_without_replace_flag
func TestRejectsExistingWithoutReplaceFlag(t *testing.T) {
	deps, home := colonyTestDeps(t)
	existing := filepath.Join(home.ColoniesDir(), "x_daily")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "old.txt"), []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/new.txt", Body: []byte("new")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "old.txt")); string(data) != "preserved" {
		t.Fatalf("old content = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_replace_existing_overwrites
func TestReplaceExistingOverwrites(t *testing.T) {
	deps, home := colonyTestDeps(t)
	existing := filepath.Join(home.ColoniesDir(), "x_daily")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "old.txt"), []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/new.txt", Body: []byte("new")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", map[string]string{"replace_existing": "true"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(existing, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt should be gone")
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "new.txt")); string(data) != "new" {
		t.Fatalf("new.txt = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_path_traversal
func TestRejectsPathTraversal(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/../escape.txt", Body: []byte("oops")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	errText, _ := body["error"].(string)
	if !strings.Contains(strings.ToLower(errText), "traversal") && !strings.Contains(strings.ToLower(errText), "outside") {
		t.Fatalf("error = %q", errText)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "x_daily")); !os.IsNotExist(err) {
		t.Fatalf("x_daily should not exist")
	}
	if _, err := os.Stat(filepath.Join(home.Root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape.txt should not exist")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_absolute_member
func TestRejectsAbsoluteMember(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "/etc/passwd", Body: []byte("oops")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_symlinks
func TestRejectsSymlinks(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily", Dir: true},
		{Name: "x_daily/evil", Symlink: "/etc/passwd"},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if errText, _ := body["error"].(string); !strings.Contains(strings.ToLower(errText), "symlink") {
		t.Fatalf("error = %q", errText)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_multiple_top_level_dirs
func TestRejectsMultipleTopLevelDirs(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "a/", Dir: true},
		{Name: "a/x.txt", Body: []byte("a")},
		{Name: "b/", Dir: true},
		{Name: "b/y.txt", Body: []byte("b")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if errText, _ := body["error"].(string); !strings.Contains(strings.ToLower(errText), "top-level") {
		t.Fatalf("error = %q", errText)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_invalid_colony_id
func TestRejectsInvalidColonyID(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "Bad-Name/", Dir: true},
		{Name: "Bad-Name/x.txt", Body: []byte("x")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_non_multipart
func TestRejectsNonMultipart(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	request := httptest.NewRequest(http.MethodPost, "/api/colonies/import", strings.NewReader("not multipart"))
	request.Header.Set("Content-Type", "application/octet-stream")
	recorder, _ := serveColonyRequest(colonyMux(deps), request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_corrupt_tar
func TestRejectsCorruptTar(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	recorder, _ := postColonyTar(t, colonyMux(deps), []byte("not a real tar"), "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_rejects_missing_file_part
func TestRejectsMissingFilePart(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	recorder, _ := postColonyTar(t, colonyMux(deps), nil, "", map[string]string{"name": "anything"})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_accepts_uncompressed_tar
func TestAcceptsUncompressedTar(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/file.txt", Body: []byte("plain")},
	}, false)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "file.txt")); string(data) != "plain" {
		t.Fatalf("file = %q", data)
	}
}

// Parity-exempt: 上游用 150 MiB 载荷证明流式上传突破了旧的 100 MiB 上限；
// Go 侧把载荷缩到 3 MiB 并把上限压到 2 MiB，走同一条流式路径，避免测试耗时。
func TestUploadAboveConfiguredCapStreams(t *testing.T) {
	deps, home := colonyTestDeps(t)
	payload := bytes.Repeat([]byte{0}, 3*1024*1024)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "big_colony", Dir: true},
		{Name: "big_colony/big.bin", Body: payload},
	}, false)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "big_colony.tar", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	info, err := os.Stat(filepath.Join(home.ColoniesDir(), "big_colony", "big.bin"))
	if err != nil || info.Size() != int64(len(payload)) {
		t.Fatalf("big.bin size = %v, err %v", info, err)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_upload_over_cap_returns_413
func TestUploadOverCapReturns413(t *testing.T) {
	deps, home := colonyTestDeps(t)
	deps.MaxUpload = 1024 * 1024
	payload := bytes.Repeat([]byte{0}, 2*1024*1024)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "big_colony", Dir: true},
		{Name: "big_colony/big.bin", Body: payload},
	}, false)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "big_colony.tar", nil)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	if errText, _ := body["error"].(string); !strings.Contains(errText, "exceeds") {
		t.Fatalf("error = %q", errText)
	}
	if _, err := os.Stat(filepath.Join(home.ColoniesDir(), "big_colony")); !os.IsNotExist(err) {
		t.Fatalf("partial colony should not exist")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_unpacks_three_subtrees
func TestMultiRootUnpacksThreeSubtrees(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/metadata.json", Body: []byte(`{"queen_session_id":"session_x"}`)},
		{Name: "colonies/x_daily/data/tracker.db", Body: []byte("sqlite")},
		{Name: "agents/x_daily/worker/", Dir: true},
		{Name: "agents/x_daily/worker/conversations/", Dir: true},
		{Name: "agents/x_daily/worker/conversations/0001.json", Body: []byte(`{"role":"user"}`)},
		{Name: "agents/x_daily/worker/conversations/0002.json", Body: []byte(`{"role":"assistant"}`)},
		{Name: "agents/queens/queen_alpha/sessions/session_x/", Dir: true},
		{Name: "agents/queens/queen_alpha/sessions/session_x/queen.json", Body: []byte(`{"id":"x"}`)},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "data", "tracker.db")); string(data) != "sqlite" {
		t.Fatalf("tracker = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home.Root, "agents", "x_daily", "worker", "conversations", "0001.json")); string(data) != `{"role":"user"}` {
		t.Fatalf("worker conversation = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "queens", "queen_alpha", "sessions", "session_x", "queen.json")); err != nil {
		t.Fatalf("queen session missing: %v", err)
	}
	if body["name"] != "x_daily" || body["files_imported"] != float64(5) {
		t.Fatalf("body = %v", body)
	}
	if nestedFiles(t, body, "colonies") != 2 || nestedFiles(t, body, "agents_worker") != 2 || nestedFiles(t, body, "agents_queen") != 1 {
		t.Fatalf("by_root = %v", body["by_root"])
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_colonies_only_succeeds
func TestMultiRootColoniesOnlySucceeds(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/metadata.json", Body: []byte("{}")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated || body["files_imported"] != float64(1) {
		t.Fatalf("status = %d body = %v", recorder.Code, body)
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "metadata.json")); string(data) != "{}" {
		t.Fatalf("metadata = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_missing_colonies_root
func TestMultiRootRejectsMissingColoniesRoot(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "agents/x_daily/worker/", Dir: true},
		{Name: "agents/x_daily/worker/log.json", Body: []byte("{}")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if errText, _ := body["error"].(string); !strings.Contains(errText, "colonies/") {
		t.Fatalf("error = %q", errText)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_replace_existing_colony
func TestMultiRootReplaceExistingColony(t *testing.T) {
	deps, home := colonyTestDeps(t)
	existing := filepath.Join(home.ColoniesDir(), "x_daily")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "old.txt"), []byte("preserved"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/new.txt", Body: []byte("new")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
	recorder, _ = postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", map[string]string{"replace_existing": "true"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(existing, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt should be removed")
	}
	if data, _ := os.ReadFile(filepath.Join(existing, "new.txt")); string(data) != "new" {
		t.Fatalf("new.txt = %q", data)
	}
}

// Parity-exempt: 上游测试未覆盖「目标目录内的符号链接指向外部」；此用例钉住
// extraction 的符号链接逃逸拒绝（Python 的 resolve()+relative_to 同样拒绝）。
func TestMultiRootRejectsSymlinkedEscapeInsideDestination(t *testing.T) {
	deps, home := colonyTestDeps(t)
	workerDir := filepath.Join(home.Root, "agents", "x_daily", "worker")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(workerDir, "link")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "agents/x_daily/worker/link/evil.txt", Body: []byte("oops")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(outside, "evil.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape wrote outside the destination")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_traversal_in_worker_subtree
func TestMultiRootRejectsTraversalInWorkerSubtree(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "agents/x_daily/worker/", Dir: true},
		{Name: "agents/x_daily/worker/../escape.txt", Body: []byte("oops")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "agents", "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape.txt should not exist")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_unknown_prefix
func TestMultiRootRejectsUnknownPrefix(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "etc/passwd", Body: []byte("oops")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home.Root, "etc", "passwd")); !os.IsNotExist(err) {
		t.Fatalf("etc/passwd should not exist")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_invalid_segment
func TestMultiRootRejectsInvalidSegment(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "agents/queens/Bad-Queen/sessions/sess_1/", Dir: true},
		{Name: "agents/queens/Bad-Queen/sessions/sess_1/x.json", Body: []byte("{}")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_overwrites_agents_subtree_in_place
func TestMultiRootOverwritesAgentsSubtreeInPlace(t *testing.T) {
	deps, home := colonyTestDeps(t)
	workerDir := filepath.Join(home.Root, "agents", "x_daily", "worker", "conversations")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workerDir, "0000_old.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "agents/x_daily/worker/", Dir: true},
		{Name: "agents/x_daily/worker/conversations/", Dir: true},
		{Name: "agents/x_daily/worker/conversations/0001_new.json", Body: []byte("new")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", map[string]string{"replace_existing": "true"})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(workerDir, "0000_old.json")); string(data) != "old" {
		t.Fatalf("old conversation = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(workerDir, "0001_new.json")); string(data) != "new" {
		t.Fatalf("new conversation = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_unpacks_queens_and_memories
func TestMultiRootUnpacksQueensAndMemories(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/metadata.json", Body: []byte("{}")},
		{Name: "queens/queen_alpha/", Dir: true},
		{Name: "queens/queen_alpha/profile.yaml", Body: []byte("name: Alpha\n")},
		{Name: "queens/queen_alpha/tools.json", Body: []byte(`{"enabled_mcp_tools": null}`)},
		{Name: "queens/queen_alpha/skills/research/SKILL.md", Body: []byte("# skill")},
		{Name: "memories/global/note.md", Body: []byte("---\nname: note\n---\nbody\n")},
		{Name: "memories/colonies/x_daily/fact.md", Body: []byte("fact")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.Root, "queens", "queen_alpha", "profile.yaml")); string(data) != "name: Alpha\n" {
		t.Fatalf("profile = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "colonies", "x_daily", "fact.md")); string(data) != "fact" {
		t.Fatalf("memory = %q", data)
	}
	if nestedFiles(t, body, "queens") != 3 || nestedFiles(t, body, "memories") != 2 {
		t.Fatalf("by_root = %v", body["by_root"])
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_queens_sessions_are_skipped
func TestMultiRootQueensSessionsAreSkipped(t *testing.T) {
	deps, home := colonyTestDeps(t)
	sessionDir := filepath.Join(home.Root, "queens", "queen_alpha", "sessions", "session_vm")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "events.jsonl"), []byte("vm-side"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "queens/queen_alpha/profile.yaml", Body: []byte("name: Alpha\n")},
		{Name: "queens/queen_alpha/sessions/session_vm/events.jsonl", Body: []byte("pushed")},
		{Name: "queens/queen_alpha/sessions/session_other/events.jsonl", Body: []byte("pushed")},
	}, true)
	recorder, body := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(sessionDir, "events.jsonl")); string(data) != "vm-side" {
		t.Fatalf("events = %q", data)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "queens", "queen_alpha", "sessions", "session_other")); !os.IsNotExist(err) {
		t.Fatalf("session_other should not exist")
	}
	if nestedFiles(t, body, "queens") != 1 {
		t.Fatalf("by_root = %v", body["by_root"])
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_queens_overwrite_in_place
func TestMultiRootQueensOverwriteInPlace(t *testing.T) {
	deps, home := colonyTestDeps(t)
	queenDir := filepath.Join(home.Root, "queens", "queen_alpha")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "profile.yaml"), []byte("name: Old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "untouched.json"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "queens/queen_alpha/profile.yaml", Body: []byte("name: New\n")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(queenDir, "profile.yaml")); string(data) != "name: New\n" {
		t.Fatalf("profile = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(queenDir, "untouched.json")); string(data) != "keep" {
		t.Fatalf("untouched = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_avatar_replaces_stale_extension
func TestMultiRootAvatarReplacesStaleExtension(t *testing.T) {
	deps, home := colonyTestDeps(t)
	queenDir := filepath.Join(home.Root, "queens", "queen_alpha")
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(queenDir, "avatar.png"), []byte("old-png"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "queens/queen_alpha/avatar.jpg", Body: []byte("new-jpg")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(queenDir, "avatar.png")); !os.IsNotExist(err) {
		t.Fatalf("avatar.png should be gone")
	}
	if data, _ := os.ReadFile(filepath.Join(queenDir, "avatar.jpg")); string(data) != "new-jpg" {
		t.Fatalf("avatar = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_invalid_queen_id
func TestMultiRootRejectsInvalidQueenID(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "queens/Bad-Queen/profile.yaml", Body: []byte("name: x\n")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_multi_root_rejects_traversal_in_memories
func TestMultiRootRejectsTraversalInMemories(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "colonies/x_daily/", Dir: true},
		{Name: "colonies/x_daily/m.json", Body: []byte("{}")},
		{Name: "memories/../escape.md", Body: []byte("oops")},
	}, true)
	recorder, _ := postColonyTar(t, colonyMux(deps), archive, "colony.tar.gz", nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(home.Root, "escape.md")); !os.IsNotExist(err) {
		t.Fatalf("escape.md should not exist")
	}
}

func initColonyUpload(t *testing.T, mux *http.ServeMux, payload map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	recorder, body := serveColonyRequest(mux, colonyRequest(t, http.MethodPost, "/api/colonies/import/init", payload))
	return recorder, body
}

func putColonyChunk(t *testing.T, mux *http.ServeMux, uploadID string, offset int, chunk []byte) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/colonies/import/chunk/" + uploadID + "?offset=" + strconv.Itoa(offset)
	request := httptest.NewRequest(http.MethodPut, target, bytes.NewReader(chunk))
	request.Header.Set("Content-Type", "application/octet-stream")
	recorder, _ := serveColonyRequest(mux, request)
	return recorder
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_happy_path
func TestChunkedUploadHappyPath(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/metadata.json", Body: []byte(`{"colony_id":"x_daily"}`)},
		{Name: "x_daily/file.txt", Body: []byte("chunked")},
	}, true)
	digest := sha256.Sum256(archive)
	mux := colonyMux(deps)
	recorder, body := initColonyUpload(t, mux, map[string]any{
		"filename":         "x_daily.tar.gz",
		"total_bytes":      len(archive),
		"sha256":           hex.EncodeToString(digest[:]),
		"replace_existing": true,
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("init status = %d, body %s", recorder.Code, recorder.Body.String())
	}
	uploadID, _ := body["upload_id"].(string)
	if len(uploadID) != 16 {
		t.Fatalf("upload_id = %q", uploadID)
	}

	cuts := []int{len(archive) / 3, (2 * len(archive)) / 3, len(archive)}
	offset := 0
	for _, end := range cuts {
		response := putColonyChunk(t, mux, uploadID, offset, archive[offset:end])
		if response.Code != http.StatusOK {
			t.Fatalf("chunk status = %d, body %s", response.Code, response.Body.String())
		}
		var chunkBody map[string]any
		_ = json.Unmarshal(response.Body.Bytes(), &chunkBody)
		if chunkBody["received"] != float64(end) {
			t.Fatalf("received = %v, want %d", chunkBody["received"], end)
		}
		offset = end
	}

	statusRecorder, statusBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/import/status/"+uploadID, nil))
	if statusRecorder.Code != http.StatusOK || statusBody["received"] != float64(len(archive)) {
		t.Fatalf("status = %d %v", statusRecorder.Code, statusBody)
	}

	finalRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+uploadID, nil))
	if finalRecorder.Code != http.StatusCreated {
		t.Fatalf("finalize status = %d, body %s", finalRecorder.Code, finalRecorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "file.txt")); string(data) != "chunked" {
		t.Fatalf("file = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_sha256_mismatch_400
func TestChunkedUploadSHA256Mismatch400(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/x.txt", Body: []byte("real")},
	}, true)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"filename":         "x.tar.gz",
		"total_bytes":      len(archive),
		"sha256":           strings.Repeat("0", 64),
		"replace_existing": true,
	})
	uploadID, _ := body["upload_id"].(string)
	if response := putColonyChunk(t, mux, uploadID, 0, archive); response.Code != http.StatusOK {
		t.Fatalf("chunk status = %d", response.Code)
	}
	finalRecorder, finalBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+uploadID, nil))
	if finalRecorder.Code != http.StatusBadRequest || finalBody["error"] != "sha256 mismatch" {
		t.Fatalf("finalize = %d %v", finalRecorder.Code, finalBody)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_offset_mismatch_409_then_resync
func TestChunkedUploadOffsetMismatch409ThenResync(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/x.txt", Body: []byte("resync")},
	}, true)
	digest := sha256.Sum256(archive)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes":      len(archive),
		"sha256":           hex.EncodeToString(digest[:]),
		"replace_existing": true,
	})
	uploadID, _ := body["upload_id"].(string)
	half := len(archive) / 2
	if response := putColonyChunk(t, mux, uploadID, 0, archive[:half]); response.Code != http.StatusOK {
		t.Fatalf("first chunk = %d", response.Code)
	}
	conflict := putColonyChunk(t, mux, uploadID, 0, archive[:half])
	if conflict.Code != http.StatusConflict {
		t.Fatalf("mismatch status = %d, want 409", conflict.Code)
	}
	var conflictBody map[string]any
	_ = json.Unmarshal(conflict.Body.Bytes(), &conflictBody)
	if conflictBody["received"] != float64(half) {
		t.Fatalf("conflict received = %v", conflictBody["received"])
	}
	_, statusBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/import/status/"+uploadID, nil))
	received := int(statusBody["received"].(float64))
	if response := putColonyChunk(t, mux, uploadID, received, archive[received:]); response.Code != http.StatusOK {
		t.Fatalf("resync chunk = %d", response.Code)
	}
	finalRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+uploadID, nil))
	if finalRecorder.Code != http.StatusCreated {
		t.Fatalf("finalize = %d, body %s", finalRecorder.Code, finalRecorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "x.txt")); string(data) != "resync" {
		t.Fatalf("file = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_overflow_413
func TestChunkedUploadOverflow413(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes": 10,
		"sha256":      strings.Repeat("a", 64),
	})
	uploadID, _ := body["upload_id"].(string)
	response := putColonyChunk(t, mux, uploadID, 0, bytes.Repeat([]byte("x"), 11))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
	_, statusBody := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/import/status/"+uploadID, nil))
	if statusBody["received"] != float64(0) {
		t.Fatalf("received = %v, want 0", statusBody["received"])
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_finalize_incomplete_409
func TestChunkedUploadFinalizeIncomplete409(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes": 100,
		"sha256":      strings.Repeat("a", 64),
	})
	uploadID, _ := body["upload_id"].(string)
	if response := putColonyChunk(t, mux, uploadID, 0, bytes.Repeat([]byte("x"), 50)); response.Code != http.StatusOK {
		t.Fatalf("chunk = %d", response.Code)
	}
	finalRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+uploadID, nil))
	if finalRecorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", finalRecorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_delete_is_idempotent
func TestChunkedUploadDeleteIsIdempotent(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes": 10,
		"sha256":      strings.Repeat("b", 64),
	})
	uploadID, _ := body["upload_id"].(string)
	for attempt := 0; attempt < 2; attempt++ {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/colonies/import/"+uploadID, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("delete attempt %d = %d, want 204", attempt, response.Code)
		}
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_unknown_id_404
func TestChunkedUploadUnknownID404(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	unknown := "0123456789abcdef"
	statusRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/import/status/"+unknown, nil))
	if statusRecorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", statusRecorder.Code)
	}
	if response := putColonyChunk(t, mux, unknown, 0, []byte("x")); response.Code != http.StatusNotFound {
		t.Fatalf("chunk = %d, want 404", response.Code)
	}
	finalRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+unknown, nil))
	if finalRecorder.Code != http.StatusNotFound {
		t.Fatalf("finalize = %d, want 404", finalRecorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_init_rejects_oversize_413
func TestChunkedUploadInitRejectsOversize413(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	deps.MaxUpload = 1024 * 1024
	recorder, _ := initColonyUpload(t, colonyMux(deps), map[string]any{
		"total_bytes": 2 * 1024 * 1024,
		"sha256":      strings.Repeat("c", 64),
	})
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_invalid_id_400
func TestChunkedUploadInvalidID400(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	statusRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodGet, "/api/colonies/import/status/not-an-id", nil))
	if statusRecorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", statusRecorder.Code)
	}
	request := httptest.NewRequest(http.MethodPut, "/api/colonies/import/chunk/not-an-id?offset=0", strings.NewReader("x"))
	recorder, _ := serveColonyRequest(mux, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("chunk = %d, want 400", recorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_gc_of_stale_uploads
func TestGCOfStaleUploads(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	deps.UploadTTL = time.Second
	if err := os.MkdirAll(deps.StagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staleID := strings.Repeat("0", 16)
	if err := os.WriteFile(filepath.Join(deps.StagingDir, staleID+".data"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"created_at": 0, "total_bytes": 4, "received_bytes": 4, "sha256": strings.Repeat("d", 64)}
	metaData, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(deps.StagingDir, staleID+".meta.json"), metaData, 0o644); err != nil {
		t.Fatal(err)
	}
	recorder, _ := initColonyUpload(t, colonyMux(deps), map[string]any{
		"total_bytes":      8,
		"sha256":           strings.Repeat("e", 64),
		"replace_existing": false,
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("init = %d", recorder.Code)
	}
	if _, err := os.Stat(filepath.Join(deps.StagingDir, staleID+".data")); !os.IsNotExist(err) {
		t.Fatalf("stale data should be swept")
	}
	if _, err := os.Stat(filepath.Join(deps.StagingDir, staleID+".meta.json")); !os.IsNotExist(err) {
		t.Fatalf("stale meta should be swept")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_gc_sweeps_orphan_data_file
func TestGCSweepsOrphanDataFile(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	if err := os.MkdirAll(deps.StagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(deps.StagingDir, strings.Repeat("a", 16)+".data")
	if err := os.WriteFile(orphan, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder, _ := initColonyUpload(t, colonyMux(deps), map[string]any{
		"total_bytes":      8,
		"sha256":           strings.Repeat("f", 64),
		"replace_existing": false,
	})
	if recorder.Code != http.StatusCreated {
		t.Fatalf("init = %d", recorder.Code)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan data should be swept")
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_truncates_torn_file_before_append
func TestChunkedUploadTruncatesTornFileBeforeAppend(t *testing.T) {
	deps, home := colonyTestDeps(t)
	archive := buildColonyTar(t, []colonyTarEntry{
		{Name: "x_daily/", Dir: true},
		{Name: "x_daily/x.txt", Body: []byte("realign")},
	}, true)
	digest := sha256.Sum256(archive)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes":      len(archive),
		"sha256":           hex.EncodeToString(digest[:]),
		"replace_existing": true,
	})
	uploadID, _ := body["upload_id"].(string)
	half := len(archive) / 2
	if response := putColonyChunk(t, mux, uploadID, 0, archive[:half]); response.Code != http.StatusOK {
		t.Fatalf("first chunk = %d", response.Code)
	}
	dataPath := filepath.Join(deps.StagingDir, uploadID+".data")
	file, err := os.OpenFile(dataPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte{0xff}, 4096)); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if info, _ := os.Stat(dataPath); info.Size() != int64(half+4096) {
		t.Fatalf("torn size = %d", info.Size())
	}
	if response := putColonyChunk(t, mux, uploadID, half, archive[half:]); response.Code != http.StatusOK {
		t.Fatalf("retry chunk = %d", response.Code)
	}
	if info, _ := os.Stat(dataPath); info.Size() != int64(len(archive)) {
		t.Fatalf("realigned size = %d, want %d", info.Size(), len(archive))
	}
	finalRecorder, _ := serveColonyRequest(mux, httptest.NewRequest(http.MethodPost, "/api/colonies/import/finalize/"+uploadID, nil))
	if finalRecorder.Code != http.StatusCreated {
		t.Fatalf("finalize = %d, body %s", finalRecorder.Code, finalRecorder.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(home.ColoniesDir(), "x_daily", "x.txt")); string(data) != "realign" {
		t.Fatalf("file = %q", data)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_chunked_upload_shorter_file_returns_410
func TestChunkedUploadShorterFileReturns410(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	mux := colonyMux(deps)
	_, body := initColonyUpload(t, mux, map[string]any{
		"total_bytes": 100,
		"sha256":      strings.Repeat("a", 64),
	})
	uploadID, _ := body["upload_id"].(string)
	if response := putColonyChunk(t, mux, uploadID, 0, bytes.Repeat([]byte("x"), 50)); response.Code != http.StatusOK {
		t.Fatalf("chunk = %d", response.Code)
	}
	dataPath := filepath.Join(deps.StagingDir, uploadID+".data")
	if err := os.Truncate(dataPath, 20); err != nil {
		t.Fatal(err)
	}
	if response := putColonyChunk(t, mux, uploadID, 50, bytes.Repeat([]byte("y"), 50)); response.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", response.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_concurrent_upload_cap_returns_429
func TestConcurrentUploadCapReturns429(t *testing.T) {
	deps, _ := colonyTestDeps(t)
	deps.MaxUploads = 2
	mux := colonyMux(deps)
	firstRecorder, firstBody := initColonyUpload(t, mux, map[string]any{"total_bytes": 10, "sha256": strings.Repeat("1", 64)})
	secondRecorder, _ := initColonyUpload(t, mux, map[string]any{"total_bytes": 10, "sha256": strings.Repeat("2", 64)})
	thirdRecorder, _ := initColonyUpload(t, mux, map[string]any{"total_bytes": 10, "sha256": strings.Repeat("3", 64)})
	if firstRecorder.Code != http.StatusCreated || secondRecorder.Code != http.StatusCreated || thirdRecorder.Code != http.StatusTooManyRequests {
		t.Fatalf("statuses = %d %d %d", firstRecorder.Code, secondRecorder.Code, thirdRecorder.Code)
	}
	uploadID, _ := firstBody["upload_id"].(string)
	deleteRecorder := httptest.NewRecorder()
	mux.ServeHTTP(deleteRecorder, httptest.NewRequest(http.MethodDelete, "/api/colonies/import/"+uploadID, nil))
	if deleteRecorder.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", deleteRecorder.Code)
	}
	fourthRecorder, _ := initColonyUpload(t, mux, map[string]any{"total_bytes": 10, "sha256": strings.Repeat("4", 64)})
	if fourthRecorder.Code != http.StatusCreated {
		t.Fatalf("post-cancel init = %d, want 201", fourthRecorder.Code)
	}
}

// Parity: framework/server/tests/test_colonies_import.py::test_staging_dir_is_on_a_real_disk
func TestStagingDirIsOnARealDisk(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deps := ColonyRouteDeps{Home: home}
	var expected string
	if info, statErr := os.Stat("/var/tmp"); statErr == nil && info.IsDir() {
		expected = filepath.Join("/var/tmp", "hive-colony-uploads")
	} else {
		expected = filepath.Join(home.Root, "tmp", "colony_uploads")
	}
	if actual := deps.stagingDir(); actual != expected {
		t.Fatalf("staging dir = %q, want %q", actual, expected)
	}
}
