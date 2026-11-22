package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/memory"
)

func newMemoryTestMux(home hivehome.Home) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterMemoryRoutes(mux, MemoryRouteDeps{Home: home})
	return mux
}

func memoryRequest(t *testing.T, mux *http.ServeMux, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func writeMemoryFixture(t *testing.T, home hivehome.Home, rel, content string, mtime time.Time) string {
	t.Helper()
	path := filepath.Join(home.MemoriesDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write memory: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// Parity-exempt: 上游 routes_memories.py 无 HTTP 用例；钉住缺失目录返回空数组。
func TestListMemoriesMissingDirectory(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)
	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	entries, ok := body["memories"].([]any)
	if !ok || len(entries) != 0 {
		t.Fatalf("memories = %#v, want empty array", body["memories"])
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住按 scope/scope_name/新到旧 排序、字段与点文件/非 md 过滤。
func TestListMemoriesGroupsAndSorts(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	base := time.Unix(1000, 0)
	writeMemoryFixture(t, home, "root.md", "---\nname: Root\n---\nbody", base)
	writeMemoryFixture(t, home, "global/old.md", "---\nname: Old\ntype: profile\ndescription: old one\n---\nbody", base)
	writeMemoryFixture(t, home, "global/new.md", "---\nname: New\n---\nbody", base.Add(10*time.Second))
	writeMemoryFixture(t, home, "agents/queens/q1/memory.md", "---\nname: Queen\n---\nbody", base)
	writeMemoryFixture(t, home, "colonies/c1/notes.md", "---\nname: Colony\n---\nbody", base)
	writeMemoryFixture(t, home, "agents/worker/w.md", "---\nname: Worker\n---\nbody", base)
	writeMemoryFixture(t, home, "global/.hidden.md", "---\nname: Hidden\n---\nbody", base)
	writeMemoryFixture(t, home, "global/skip.txt", "not a memory", base)

	mux := newMemoryTestMux(home)
	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	entries := body["memories"].([]any)
	if len(entries) != 6 {
		t.Fatalf("entries = %d, want 6: %#v", len(entries), entries)
	}

	order := make([]string, 0, len(entries))
	for _, entry := range entries {
		order = append(order, entry.(map[string]any)["path"].(string))
	}
	want := []string{
		"agents/worker/w.md",
		"colonies/c1/notes.md",
		"global/new.md",
		"global/old.md",
		"agents/queens/q1/memory.md",
		"root.md",
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order = %#v, want %#v", order, want)
		}
	}

	globalOld := entries[3].(map[string]any)
	if globalOld["scope"] != "global" || globalOld["scope_name"] != "" {
		t.Fatalf("scope = %#v", globalOld)
	}
	if globalOld["name"] != "Old" || globalOld["type"] != "profile" || globalOld["description"] != "old one" {
		t.Fatalf("frontmatter = %#v", globalOld)
	}
	if globalOld["filename"] != "old.md" {
		t.Fatalf("filename = %#v", globalOld["filename"])
	}
	if _, ok := globalOld["mtime"].(float64); !ok {
		t.Fatalf("mtime = %#v", globalOld["mtime"])
	}
	queenEntry := entries[4].(map[string]any)
	if queenEntry["scope"] != "queen" || queenEntry["scope_name"] != "q1" {
		t.Fatalf("queen scope = %#v", queenEntry)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住遍历/绝对路径/非 md 与空路径统一返回 400。
func TestMemoryFileRejectsUnsafePaths(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeMemoryFixture(t, home, "global/ok.md", "---\nname: Ok\n---\nbody", time.Now())
	mux := newMemoryTestMux(home)

	targets := []string{
		"/api/memories/file",
		"/api/memories/file?path=",
		"/api/memories/file?path=%2Fetc%2Fpasswd.md",
		"/api/memories/file?path=..%2Fsecret.md",
		"/api/memories/file?path=global%2F..%2Fsecret.md",
		"/api/memories/file?path=global%2F%2Fok.md",
		"/api/memories/file?path=.%2Fglobal%2Fok.md",
		"/api/memories/file?path=global%2Fok.txt",
	}
	for _, target := range targets {
		recorder, body := memoryRequest(t, mux, http.MethodGet, target, "")
		if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid path" {
			t.Fatalf("%s = %d %#v", target, recorder.Code, body)
		}
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住读取响应的 frontmatter、content 与 404。
func TestGetMemoryContentAndNotFound(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeMemoryFixture(t, home, "global/ok.md", "---\nname: Ok\ndescription: fine\ntype: preference\n---\n\nhello\n", time.Now())
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/file?path=global%2Fok.md", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if body["name"] != "Ok" || body["type"] != "preference" || body["description"] != "fine" {
		t.Fatalf("frontmatter = %#v", body)
	}
	if body["content"] != "---\nname: Ok\ndescription: fine\ntype: preference\n---\n\nhello\n" {
		t.Fatalf("content = %#v", body["content"])
	}
	if body["path"] != "global/ok.md" || body["scope"] != "global" {
		t.Fatalf("path/scope = %#v", body)
	}

	recorder, body = memoryRequest(t, mux, http.MethodGet, "/api/memories/file?path=global%2Fmissing.md", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "Memory not found" {
		t.Fatalf("missing = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住创建文档格式、默认 type、memory_log 盖章（seeded=无 author）、
// content 回显与幂等 created:false。
func TestCreateMemoryDocumentAndIdempotency(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)

	payload := `{"name":" My Memory ","body":" Hello body ","description":" Desc "}`
	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fnew.md", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	if body["created"] != true || body["path"] != "global/new.md" {
		t.Fatalf("create payload = %#v", body)
	}
	content, _ := body["content"].(string)
	fields := memory.ParseFrontmatter(content)
	if fields["name"] != "My Memory" || fields["type"] != "profile" || fields["description"] != "Desc" {
		t.Fatalf("frontmatter = %#v", fields)
	}
	if _, hasAuthor := fields["author"]; hasAuthor {
		t.Fatalf("seeded memory claims an author: %#v", fields)
	}
	if fields["learned"] != fields["confirmed"] || fields["learned"] == "" {
		t.Fatalf("seeded memory dates = %#v", fields)
	}
	if !strings.HasSuffix(content, "\n\nHello body\n") {
		t.Fatalf("content = %#v", content)
	}
	if body["name"] != "My Memory" || body["type"] != "profile" || body["author"] != nil {
		t.Fatalf("parsed = %#v", body)
	}
	raw, err := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "new.md"))
	if err != nil || string(raw) != content {
		t.Fatalf("file = %q err = %v", raw, err)
	}

	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fnew.md", payload)
	if recorder.Code != http.StatusOK || body["created"] != false || body["path"] != "global/new.md" {
		t.Fatalf("idempotent = %d %#v", recorder.Code, body)
	}
	rawAgain, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "new.md"))
	if string(rawAgain) != content {
		t.Fatalf("existing file was clobbered: %q", rawAgain)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住创建接口的 JSON、name/body 校验、自定义 type 与 4096 字节上限。
func TestCreateMemoryValidationAndType(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newMemoryTestMux(home)

	recorder, _ := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fx.md", `not json`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON = %d", recorder.Code)
	}

	recorder, body := memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fx.md", `{"name":"x"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "body must be a non-empty string" {
		t.Fatalf("missing body = %d %#v", recorder.Code, body)
	}

	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fx.md", `{"name":" ","body":"x"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "name must be a non-empty string" {
		t.Fatalf("blank name = %d %#v", recorder.Code, body)
	}

	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fx.md", `["x"]`)
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("non-object = %d %#v", recorder.Code, body)
	}

	payload := `{"name":"Typed","body":"body","type":"bogus","description":5}`
	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Ftyped.md", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("typed status = %d %#v", recorder.Code, body)
	}
	if body["type"] != nil {
		t.Fatalf("unknown type should parse to null, got %#v", body["type"])
	}
	raw, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "typed.md"))
	if !strings.Contains(string(raw), "type: bogus") || !strings.Contains(string(raw), "description: \n") {
		t.Fatalf("typed document = %q", raw)
	}

	oversized := `{"name":"Big","body":"` + strings.Repeat("a", 4096+1) + `"}`
	recorder, body = memoryRequest(t, mux, http.MethodPost, "/api/memories/file?path=global%2Fbig.md", oversized)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized = %d %#v", recorder.Code, body)
	}
	if body["error"] != "Memory exceeds the 4096-byte limit" {
		t.Fatalf("oversized error = %#v", body["error"])
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 PUT 经 memory_log 盖章（author=owner）、404、空内容与上限。
func TestUpdateMemoryOverwritesContent(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	writeMemoryFixture(t, home, "global/ok.md", "---\nname: Ok\n---\nold\n", time.Now())
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fok.md", `{"content":"new body"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %#v", recorder.Code, body)
	}
	content, _ := body["content"].(string)
	fields := memory.ParseFrontmatter(content)
	if fields["author"] != "owner" {
		t.Fatalf("author = %#v", fields)
	}
	if fields["learned"] == "" || fields["confirmed"] == "" {
		t.Fatalf("dates = %#v", fields)
	}
	if !strings.HasSuffix(content, "\n\nnew body\n") {
		t.Fatalf("content = %#v", content)
	}
	raw, _ := os.ReadFile(filepath.Join(home.MemoriesDir(), "global", "ok.md"))
	if string(raw) != content {
		t.Fatalf("file = %q", raw)
	}

	recorder, body = memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fok.md", `{"content":"   "}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Content must be a non-empty string" {
		t.Fatalf("blank = %d %#v", recorder.Code, body)
	}

	oversized := `{"content":"` + strings.Repeat("a", 4096+1) + `"}`
	recorder, body = memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fok.md", oversized)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Memory exceeds the 4096-byte limit" {
		t.Fatalf("oversized = %d %#v", recorder.Code, body)
	}

	recorder, body = memoryRequest(t, mux, http.MethodPut, "/api/memories/file?path=global%2Fmissing.md", `{"content":"x"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "Memory not found" {
		t.Fatalf("missing = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 DELETE 删文件、返回相对路径与重复删除 404。
func TestDeleteMemoryFile(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	path := writeMemoryFixture(t, home, "global/ok.md", "---\nname: Ok\n---\nbody", time.Now())
	mux := newMemoryTestMux(home)

	recorder, body := memoryRequest(t, mux, http.MethodDelete, "/api/memories/file?path=global%2Fok.md", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "global/ok.md" {
		t.Fatalf("delete = %d %#v", recorder.Code, body)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}

	recorder, body = memoryRequest(t, mux, http.MethodDelete, "/api/memories/file?path=global%2Fok.md", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "Memory not found" {
		t.Fatalf("repeat delete = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住指向 MEMORIES_DIR 之外的符号链接被拒绝。
func TestMemoryFileRejectsSymlinkEscape(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(home.MemoriesDir(), 0o755); err != nil {
		t.Fatalf("mkdir memories: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(home.MemoriesDir(), "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	mux := newMemoryTestMux(home)
	recorder, body := memoryRequest(t, mux, http.MethodGet, "/api/memories/file?path=escape.md", "")
	if recorder.Code != http.StatusBadRequest || body["error"] != "Invalid path" {
		t.Fatalf("symlink escape = %d %#v", recorder.Code, body)
	}
}
