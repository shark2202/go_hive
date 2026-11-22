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

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

func newPromptTestMux(home hivehome.Home) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterPromptRoutes(mux, PromptRouteDeps{
		Home: home,
		Now:  func() time.Time { return time.UnixMilli(1700000000123) },
	})
	return mux
}

func promptRequest(t *testing.T, mux *http.ServeMux, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

func seedPromptFile(t *testing.T, home hivehome.Home, content string) string {
	t.Helper()
	path := filepath.Join(home.Root, customPromptsFilename)
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write prompts: %v", err)
	}
	return path
}

// Parity-exempt: 上游 routes_prompts.py 无 HTTP 用例；钉住缺失/损坏文件返回空数组而非 null。
func TestListPromptsEmptyAndCorruptFile(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newPromptTestMux(home)

	recorder, body := promptRequest(t, mux, http.MethodGet, "/api/prompts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("missing file status = %d", recorder.Code)
	}
	prompts, ok := body["prompts"].([]any)
	if !ok || len(prompts) != 0 {
		t.Fatalf("prompts = %#v, want empty array", body["prompts"])
	}

	seedPromptFile(t, home, `{"not":"a list"}`)
	recorder, body = promptRequest(t, mux, http.MethodGet, "/api/prompts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("non-list status = %d", recorder.Code)
	}
	if prompts, ok := body["prompts"].([]any); !ok || len(prompts) != 0 {
		t.Fatalf("non-list prompts = %#v, want empty array", body["prompts"])
	}

	seedPromptFile(t, home, `not json`)
	recorder, body = promptRequest(t, mux, http.MethodGet, "/api/prompts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("corrupt status = %d", recorder.Code)
	}
	if prompts, ok := body["prompts"].([]any); !ok || len(prompts) != 0 {
		t.Fatalf("corrupt prompts = %#v, want empty array", body["prompts"])
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住创建接口的 JSON、必填字段与标题空白错误分支。
func TestCreatePromptValidation(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newPromptTestMux(home)

	recorder, _ := promptRequest(t, mux, http.MethodPost, "/api/prompts", `not json`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON status = %d, want 400", recorder.Code)
	}

	recorder, body := promptRequest(t, mux, http.MethodPost, "/api/prompts", `["list"]`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("non-object status = %d, want 500", recorder.Code)
	}
	if body["error"] != internalServerError {
		t.Fatalf("non-object error = %#v", body["error"])
	}

	recorder, body = promptRequest(t, mux, http.MethodPost, "/api/prompts", `{"title":"  ","content":"x"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Title and content are required" {
		t.Fatalf("missing title = %d %#v", recorder.Code, body)
	}

	recorder, body = promptRequest(t, mux, http.MethodPost, "/api/prompts", `{"title":"x","content":"   "}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Title and content are required" {
		t.Fatalf("missing content = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住创建成功后的默认分类、规范化列表、时间戳 id 与落盘字节。
func TestCreatePromptPersistsNormalizedLists(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newPromptTestMux(home)

	longSkill := strings.Repeat("x", 80)
	skills := `[" Growth ","growth","PLG","` + longSkill + `","a","b","c","d","e","f","g","h"]`
	payload := `{"title":"  My Prompt  ","content":"  Hello 繁體 <tag>  ","skills":` + skills + `,"tags":["One","one","2"]}`
	recorder, body := promptRequest(t, mux, http.MethodPost, "/api/prompts", payload)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if body["id"] != "custom_1700000000123" {
		t.Fatalf("id = %#v", body["id"])
	}
	if body["title"] != "My Prompt" || body["content"] != "Hello 繁體 <tag>" || body["category"] != "custom" {
		t.Fatalf("fields = %#v", body)
	}
	if body["custom"] != true {
		t.Fatalf("custom = %#v", body["custom"])
	}
	gotSkills := body["skills"].([]any)
	if len(gotSkills) != 10 {
		t.Fatalf("skills = %#v, want 10 capped items", gotSkills)
	}
	if gotSkills[0] != "growth" || gotSkills[1] != "plg" {
		t.Fatalf("skills head = %#v", gotSkills[:2])
	}
	if gotSkills[2] != strings.Repeat("x", 64) {
		t.Fatalf("truncated skill = %q", gotSkills[2])
	}
	gotTags := body["tags"].([]any)
	if len(gotTags) != 2 || gotTags[0] != "one" || gotTags[1] != "2" {
		t.Fatalf("tags = %#v", gotTags)
	}

	raw, err := os.ReadFile(filepath.Join(home.Root, customPromptsFilename))
	if err != nil {
		t.Fatalf("read prompts file: %v", err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
		t.Fatalf("file newline = %q", text)
	}
	if !strings.Contains(text, "繁體 <tag>") {
		t.Fatalf("unicode/html escaping in %s", text)
	}
	if !strings.Contains(text, "\"id\": \"custom_1700000000123\"") {
		t.Fatalf("pretty JSON missing id: %s", text)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 Python `(value or "").strip()` 对真值非字符串抛 500。
func TestCreatePromptNonStringTitleReturns500(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := newPromptTestMux(home)
	recorder, body := promptRequest(t, mux, http.MethodPost, "/api/prompts", `{"title":5,"content":"x"}`)
	if recorder.Code != http.StatusInternalServerError || body["error"] != internalServerError {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	recorder, body = promptRequest(t, mux, http.MethodPost, "/api/prompts", `{"title":false,"content":"x"}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "Title and content are required" {
		t.Fatalf("falsy title = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 PATCH 只改可编辑字段且 id/custom 不可变。
func TestUpdatePromptEditsOnlyEditableFields(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedPromptFile(t, home, `[{"id":"custom_1","title":"Old","category":"custom","content":"Body","skills":[],"tags":[],"custom":true}]`)
	mux := newPromptTestMux(home)

	payload := `{"title":" New ","category":" ","content":" New Body ","skills":["X"],"tags":[],"id":"hacked","custom":false}`
	recorder, body := promptRequest(t, mux, http.MethodPatch, "/api/prompts/custom_1", payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	if body["id"] != "custom_1" || body["custom"] != true {
		t.Fatalf("immutable fields = %#v", body)
	}
	if body["title"] != "New" || body["content"] != "New Body" || body["category"] != "custom" {
		t.Fatalf("edited fields = %#v", body)
	}
	if skills := body["skills"].([]any); len(skills) != 1 || skills[0] != "x" {
		t.Fatalf("skills = %#v", body["skills"])
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 PATCH 的空标题、空正文、无可编辑字段与 404 分支。
func TestUpdatePromptRejections(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedPromptFile(t, home, `[{"id":"custom_1","title":"Old","category":"custom","content":"Body"}]`)
	mux := newPromptTestMux(home)

	cases := []struct {
		payload string
		status  int
		message string
	}{
		{`{"title":"  "}`, http.StatusBadRequest, "Title cannot be empty"},
		{`{"content":""}`, http.StatusBadRequest, "Content cannot be empty"},
		{`{"unknown":"x"}`, http.StatusBadRequest, "No editable fields provided"},
		{`not json`, http.StatusBadRequest, "Invalid JSON body"},
	}
	for _, testCase := range cases {
		recorder, body := promptRequest(t, mux, http.MethodPatch, "/api/prompts/custom_1", testCase.payload)
		if recorder.Code != testCase.status || body["error"] != testCase.message {
			t.Fatalf("%s = %d %#v", testCase.payload, recorder.Code, body)
		}
	}

	recorder, body := promptRequest(t, mux, http.MethodPatch, "/api/prompts/missing", `{"title":"x"}`)
	if recorder.Code != http.StatusNotFound || body["error"] != "Prompt not found" {
		t.Fatalf("missing = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 DELETE 删除并返回 id，重复删除 404。
func TestDeletePrompt(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	seedPromptFile(t, home, `[{"id":"custom_1","title":"A"},{"id":"custom_2","title":"B"}]`)
	mux := newPromptTestMux(home)

	recorder, body := promptRequest(t, mux, http.MethodDelete, "/api/prompts/custom_1", "")
	if recorder.Code != http.StatusOK || body["deleted"] != "custom_1" {
		t.Fatalf("delete = %d %#v", recorder.Code, body)
	}
	raw, err := os.ReadFile(filepath.Join(home.Root, customPromptsFilename))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if strings.Contains(string(raw), "custom_1") || !strings.Contains(string(raw), "custom_2") {
		t.Fatalf("file after delete = %s", raw)
	}

	recorder, body = promptRequest(t, mux, http.MethodDelete, "/api/prompts/custom_1", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "Prompt not found" {
		t.Fatalf("repeat delete = %d %#v", recorder.Code, body)
	}
}
