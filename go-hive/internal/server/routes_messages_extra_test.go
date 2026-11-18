package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

type fakeMessageLLM struct {
	response string
	err      error
	calls    []llm.CompletionRequest
}

func (f *fakeMessageLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return llm.Completion{}, f.err
	}
	return llm.Completion{Content: f.response}, nil
}

func newMessageTestMux(t *testing.T, provider *fakeMessageLLM) *http.ServeMux {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	mux := http.NewServeMux()
	RegisterMessageRoutes(mux, MessageRouteDeps{
		Home:     home,
		BuildLLM: func() llm.Completer { return provider },
	})
	return mux
}

func messageRequest(t *testing.T, mux *http.ServeMux, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	decoded := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder, decoded
}

// Parity: framework/server/tests/test_api.py::TestMessageBootstrap::test_classify_requires_non_empty_message
func TestClassifyRequiresNonEmptyMessage(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_technology","reason":"r"}`}
	mux := newMessageTestMux(t, provider)

	recorder, body := messageRequest(t, mux, "/api/messages/classify", `{"message":"   "}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "message is required" {
		t.Fatalf("blank = %d %#v", recorder.Code, body)
	}
	recorder, body = messageRequest(t, mux, "/api/messages/classify", `{"message":5}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "message is required" {
		t.Fatalf("non-string = %d %#v", recorder.Code, body)
	}
	if len(provider.calls) != 0 {
		t.Fatalf("LLM called %d times for rejected messages", len(provider.calls))
	}
}

// Parity: framework/server/tests/test_api.py::TestMessageBootstrap::test_classify_returns_queen_id_without_touching_sessions
func TestClassifyReturnsQueenIDWithoutTouchingSessions(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"reason":"scraper work","queen_id":"queen_technology"}`}
	mux := newMessageTestMux(t, provider)

	recorder, body := messageRequest(t, mux, "/api/messages/classify", `{"message":"  Build me a scraper  "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	if len(body) != 1 || body["queen_id"] != "queen_technology" {
		t.Fatalf("body = %#v", body)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("calls = %d", len(provider.calls))
	}
	call := provider.calls[0]
	if !call.JSONMode {
		t.Fatal("json mode not requested")
	}
	if call.Messages[0].Content != "Build me a scraper" || call.Messages[0].Role != "user" {
		t.Fatalf("message = %#v", call.Messages)
	}
	if call.MaxTokens != llm.ResolveAuxMaxTokens(0) {
		t.Fatalf("max tokens = %d", call.MaxTokens)
	}
	if !strings.Contains(call.System, "- queen_technology: ") || !strings.Contains(call.System, "- queen_sales: ") {
		t.Fatalf("system prompt missing queen catalog: %s", call.System)
	}
	if !strings.Contains(call.System, `"queen_id": "<one of the IDs above>"`) {
		t.Fatalf("system prompt missing JSON contract: %s", call.System)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住分类器异常时回退 queen_growth 且仍返回 200。
func TestClassifyFallsBackOnProviderError(t *testing.T) {
	provider := &fakeMessageLLM{err: errors.New("boom")}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify", `{"message":"hello"}`)
	if recorder.Code != http.StatusOK || body["queen_id"] != "queen_growth" {
		t.Fatalf("fallback = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住分类器返回非法 JSON 时回退 queen_growth。
func TestClassifyFallsBackOnInvalidJSON(t *testing.T) {
	provider := &fakeMessageLLM{response: "not json at all"}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify", `{"message":"hello"}`)
	if recorder.Code != http.StatusOK || body["queen_id"] != "queen_growth" {
		t.Fatalf("fallback = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住分类器返回未知 queen_id 时回退 queen_growth。
func TestClassifyFallsBackOnUnknownQueen(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_nope","reason":"weird"}`}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify", `{"message":"hello"}`)
	if recorder.Code != http.StatusOK || body["queen_id"] != "queen_growth" {
		t.Fatalf("fallback = %d %#v", recorder.Code, body)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住非法 JSON 请求体进入 500 分支且带错误文本。
func TestClassifyInvalidBodyReturns500(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_technology"}`}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify", `not json`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	if _, ok := body["error"].(string); !ok {
		t.Fatalf("error = %#v", body["error"])
	}
	if len(provider.calls) != 0 {
		t.Fatal("LLM called for invalid body")
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 classify-colony 的 JSON 契约、colony slug 清洗与提示词。
func TestClassifyColonyReturnsSanitizedSelection(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_growth","colony_name":"Fintech Research!!","reason":"best fit"}`}
	mux := newMessageTestMux(t, provider)

	recorder, body := messageRequest(t, mux, "/api/messages/classify-colony", `{"message":"research fintech"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %#v", recorder.Code, body)
	}
	if body["queen_id"] != "queen_growth" || body["colony_name"] != "fintech_research" || body["reason"] != "best fit" {
		t.Fatalf("body = %#v", body)
	}
	if !strings.Contains(provider.calls[0].System, "spinning up a dedicated colony") {
		t.Fatalf("colony prompt missing instructions: %s", provider.calls[0].System)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 active_queen_ids 限定可选集合，越界 queen 回退首个允许项。
func TestClassifyColonyHonorsActiveQueenIDs(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_growth","colony_name":"growth_lab","reason":"g"}`}
	mux := newMessageTestMux(t, provider)

	body := `{"message":"grow","active_queen_ids":["queen_technology","bogus",5]}`
	recorder, decoded := messageRequest(t, mux, "/api/messages/classify-colony", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body = %#v", recorder.Code, decoded)
	}
	if decoded["queen_id"] != "queen_technology" {
		t.Fatalf("queen_id = %#v, want fallback queen_technology", decoded["queen_id"])
	}
	if decoded["colony_name"] != "growth_lab" {
		t.Fatalf("colony_name = %#v", decoded["colony_name"])
	}
	prompt := provider.calls[0].System
	if strings.Contains(prompt, "- queen_growth:") || !strings.Contains(prompt, "- queen_technology:") {
		t.Fatalf("prompt was not restricted to active queens: %s", prompt)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 colony 选择器非法 JSON 时回退允许 queen 与 new_colony。
func TestClassifyColonyInvalidJSONFallback(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":`}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify-colony", `{"message":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d %#v", recorder.Code, body)
	}
	if body["queen_id"] != "queen_growth" || body["colony_name"] != "new_colony" {
		t.Fatalf("fallback = %#v", body)
	}
	if !strings.Contains(body["reason"].(string), "invalid JSON") {
		t.Fatalf("reason = %#v", body["reason"])
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 classify-colony 缺失/空白 message 返回 400。
func TestClassifyColonyRequiresMessage(t *testing.T) {
	provider := &fakeMessageLLM{response: `{"queen_id":"queen_growth"}`}
	mux := newMessageTestMux(t, provider)
	recorder, body := messageRequest(t, mux, "/api/messages/classify-colony", `{"message":" "}`)
	if recorder.Code != http.StatusBadRequest || body["error"] != "message is required" {
		t.Fatalf("blank = %d %#v", recorder.Code, body)
	}
	if len(provider.calls) != 0 {
		t.Fatal("LLM called for blank message")
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住 colony slug 的下划线化、截断与空值回退。
func TestSanitizeColonyName(t *testing.T) {
	cases := map[string]string{
		"Morning HN Digest":     "morning_hn_digest",
		"Fintech_Research":      "fintech_research",
		"a-b c":                 "a_b_c",
		"___":                   "new_colony",
		"":                      "new_colony",
		strings.Repeat("a", 70): strings.Repeat("a", 64),
	}
	for input, want := range cases {
		if got := sanitizeColonyName(input); got != want {
			t.Fatalf("sanitizeColonyName(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住默认 queen 全被移除时回退完整目录而不是空集合。
func TestAllowedQueenIDsFallsBackToCatalogWhenAllRemoved(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	if err := os.MkdirAll(home.QueensDir(), 0o755); err != nil {
		t.Fatalf("mkdir queens: %v", err)
	}
	ids := defaultQueenIDs()
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, fmt.Sprintf("%q", id))
	}
	catalog := `{"removed":[` + strings.Join(quoted, ",") + `]}`
	if err := os.WriteFile(filepath.Join(home.QueensDir(), "catalog.json"), []byte(catalog), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	allowed := allowedQueenIDs(nil, home.QueensDir())
	if len(allowed) != len(ids) {
		t.Fatalf("allowed = %d, want full catalog %d", len(allowed), len(ids))
	}
	selection := SelectQueenAndColony(context.Background(), "", nil, []string{"queen_technology"}, home.QueensDir(), 0)
	if selection.QueenID != "queen_technology" || selection.ColonyName != "new_colony" {
		t.Fatalf("empty message selection = %#v", selection)
	}
}

// Parity-exempt: 上游无 HTTP 用例；钉住空消息的直选函数回退允许集合首个 queen。
func TestSelectQueenAndColonyEmptyMessageUsesFallback(t *testing.T) {
	allowed := allowedQueenIDs([]string{"queen_legal"}, t.TempDir())
	selection := SelectQueenAndColony(context.Background(), "  ", nil, []string{"queen_legal"}, t.TempDir(), 0)
	if selection.QueenID != "queen_legal" || selection.ColonyName != "new_colony" {
		t.Fatalf("selection = %#v", selection)
	}
	if len(allowed) != 1 || allowed[0] != "queen_legal" {
		t.Fatalf("allowed = %#v", allowed)
	}
}
