package llm

// Antigravity provider tests: OAuth refresh, endpoint fallback, the Gemini
// request/response shape and the SSE stream parser. Every test runs against
// httptest servers — the real Cloud Code Assist gateway is never contacted.
//
// Parity anchors:
//   core/tests/test_antigravity_schema.py
//   core/framework/llm/antigravity.py (transport paths)

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// antigravityTestNow is the fixed clock every transport test runs on.
var antigravityTestNow = time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

type recordedAntigravityRequest struct {
	Headers http.Header
	Body    map[string]any
}

type antigravityRecorder struct {
	mu       sync.Mutex
	requests []recordedAntigravityRequest
}

func (r *antigravityRecorder) record(request *http.Request) map[string]any {
	body := map[string]any{}
	if request.Body != nil {
		_ = json.NewDecoder(request.Body).Decode(&body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, recordedAntigravityRequest{Headers: request.Header.Clone(), Body: body})
	return body
}

func (r *antigravityRecorder) all() []recordedAntigravityRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := make([]recordedAntigravityRequest, len(r.requests))
	copy(copied, r.requests)
	return copied
}

func writeAntigravityAccountsFile(t *testing.T, doc map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "antigravity-accounts.json")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal accounts file: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write accounts file: %v", err)
	}
	return path
}

func expiredAntigravityAccounts(t *testing.T, project string) string {
	t.Helper()
	return writeAntigravityAccountsFile(t, map[string]any{
		"schemaVersion": 4,
		"accounts": []any{map[string]any{
			"enabled": true,
			"refresh": "refresh-1|" + project,
			"access":  "stale-access",
			"expires": antigravityTestNow.Add(-time.Hour).UnixMilli(),
		}},
	})
}

// freshAntigravityAccounts holds a valid access token, so ensureToken returns
// it without a refresh call.
func freshAntigravityAccounts(t *testing.T, project string) string {
	t.Helper()
	return writeAntigravityAccountsFile(t, map[string]any{
		"schemaVersion": 4,
		"accounts": []any{map[string]any{
			"enabled": true,
			"refresh": "refresh-1|" + project,
			"access":  "valid-access",
			"expires": antigravityTestNow.Add(time.Hour).UnixMilli(),
		}},
	})
}

func testAntigravityProvider(t *testing.T, opts AntigravityOptions) *AntigravityProvider {
	t.Helper()
	if opts.AccountsPath == "" {
		opts.AccountsPath = filepath.Join(t.TempDir(), "missing-accounts.json")
	}
	if opts.IDEDBPaths == nil {
		opts.IDEDBPaths = []string{}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return antigravityTestNow }
	}
	if opts.ClientID == nil {
		opts.ClientID = func() string { return "client-1" }
	}
	if opts.ClientSecret == nil {
		opts.ClientSecret = func() string { return "secret-1" }
	}
	return NewAntigravityProvider(opts)
}

func antigravityTokenServer(t *testing.T, accessToken string) (*httptest.Server, *[]string) {
	t.Helper()
	forms := &[]string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			http.Error(writer, "parse form", http.StatusBadRequest)
			return
		}
		*forms = append(*forms, request.PostForm.Encode())
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"`+accessToken+`","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	return server, forms
}

// TestAntigravityCompleteRefreshesExpiredToken runs the full non-streaming path
// against mock OAuth and gateway servers: the expired account file triggers a
// refresh_token grant, the request carries the fresh bearer token and the
// Gemini envelope, and the response parses into the runtime's Completion.
// Parity: core/framework/llm/antigravity.py::_do_token_refresh
func TestAntigravityCompleteRefreshesExpiredToken(t *testing.T) {
	tokenServer, forms := antigravityTokenServer(t, "fresh-token")

	recorder := &antigravityRecorder{}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1internal:generateContent" {
			http.Error(writer, "unexpected path "+request.URL.Path, http.StatusNotFound)
			return
		}
		recorder.record(request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"response":{"candidates":[{"content":{"parts":[{"text":"hel"},{"text":"ignored","thought":true},{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7},"modelVersion":"gemini-3-flash-001"},"traceId":"trace-1"}`)
	}))
	t.Cleanup(gateway.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "openai/gemini-3-flash",
		AccountsPath: expiredAntigravityAccounts(t, "proj-1"),
		TokenURL:     tokenServer.URL,
		Endpoints:    []string{gateway.URL},
	})
	if provider.Model != "gemini-3-flash" {
		t.Fatalf("model = %q, want the provider prefix stripped", provider.Model)
	}
	if !provider.HasCredentials() {
		t.Fatalf("HasCredentials = false, want the account file's credentials")
	}

	completion, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages:  []ChatMessage{{Role: "user", Content: "hi"}},
		MaxTokens: 99,
	})
	if err != nil {
		t.Fatalf("AComplete = %v", err)
	}
	if completion.Content != "hello" {
		t.Fatalf("content = %q, want the non-thought parts joined", completion.Content)
	}
	if completion.Model != "gemini-3-flash-001" {
		t.Fatalf("model = %q, want modelVersion", completion.Model)
	}
	if completion.StopReason != "stop" {
		t.Fatalf("stop reason = %q, want stop", completion.StopReason)
	}
	if completion.InputTokens != 11 || completion.OutputTokens != 7 {
		t.Fatalf("tokens = %d/%d, want 11/7", completion.InputTokens, completion.OutputTokens)
	}

	// The refresh grant carried the configured client credentials.
	if len(*forms) != 1 {
		t.Fatalf("token requests = %d, want 1", len(*forms))
	}
	form := (*forms)[0]
	for _, want := range []string{"grant_type=refresh_token", "refresh_token=refresh-1", "client_id=client-1", "client_secret=secret-1"} {
		if !strings.Contains(form, want) {
			t.Fatalf("token form %q missing %q", form, want)
		}
	}

	requests := recorder.all()
	if len(requests) != 1 {
		t.Fatalf("gateway requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if got := request.Headers.Get("Authorization"); got != "Bearer fresh-token" {
		t.Fatalf("authorization = %q, want the refreshed token", got)
	}
	if got := request.Headers.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") || !strings.Contains(got, "Antigravity/") {
		t.Fatalf("user-agent = %q, want the Antigravity app string", got)
	}
	if got := request.Headers.Get("X-Goog-Api-Client"); got != "google-cloud-sdk vscode_cloudshelleditor/0.1" {
		t.Fatalf("x-goog-api-client = %q", got)
	}

	body := request.Body
	if body["project"] != "proj-1" || body["model"] != "gemini-3-flash" {
		t.Fatalf("envelope project/model = %v/%v", body["project"], body["model"])
	}
	if body["requestType"] != "agent" || body["userAgent"] != "antigravity" {
		t.Fatalf("envelope type/agent = %v/%v", body["requestType"], body["userAgent"])
	}
	if id, _ := body["requestId"].(string); !strings.HasPrefix(id, "agent-") {
		t.Fatalf("requestId = %v, want an agent- prefix", body["requestId"])
	}
	inner, _ := body["request"].(map[string]any)
	contents, _ := inner["contents"].([]any)
	if len(contents) != 1 {
		t.Fatalf("contents = %#v, want one user turn", contents)
	}
	turn, _ := contents[0].(map[string]any)
	parts, _ := turn["parts"].([]any)
	first, _ := parts[0].(map[string]any)
	if turn["role"] != "user" || first["text"] != "hi" {
		t.Fatalf("first turn = %#v", contents[0])
	}
	generationConfig, _ := inner["generationConfig"].(map[string]any)
	if value, _ := antigravityInt(generationConfig["maxOutputTokens"]); value != 99 {
		t.Fatalf("generationConfig = %#v, want maxOutputTokens 99", generationConfig)
	}
}

// TestAntigravityRetriesOnceAfterUnauthorized pins the 401 → refresh → retry
// same endpoint path.
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._post
func TestAntigravityRetriesOnceAfterUnauthorized(t *testing.T) {
	tokenServer, forms := antigravityTokenServer(t, "fresh-2")

	var mu sync.Mutex
	calls := 0
	authorizations := []string{}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		calls++
		call := calls
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		mu.Unlock()
		if call == 1 {
			http.Error(writer, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(gateway.Close)

	accounts := writeAntigravityAccountsFile(t, map[string]any{
		"schemaVersion": 4,
		"accounts": []any{map[string]any{
			"refresh": "refresh-1|proj-1",
			"access":  "still-valid",
			"expires": antigravityTestNow.Add(time.Hour).UnixMilli(),
		}},
	})
	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: accounts,
		TokenURL:     tokenServer.URL,
		Endpoints:    []string{gateway.URL},
	})

	completion, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("AComplete = %v", err)
	}
	if completion.Content != "ok" {
		t.Fatalf("content = %q", completion.Content)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("gateway calls = %d, want the 401 and one retry", calls)
	}
	if len(authorizations) != 2 || authorizations[0] != "Bearer still-valid" || authorizations[1] != "Bearer fresh-2" {
		t.Fatalf("authorizations = %#v", authorizations)
	}
	if len(*forms) != 1 {
		t.Fatalf("token refresh calls = %d, want 1", len(*forms))
	}
}

// TestAntigravityFallsBackThroughEndpoints pins the 5xx → next endpoint path and
// the composite failure message when every endpoint fails.
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._post
func TestAntigravityFallsBackThroughEndpoints(t *testing.T) {
	recorder := &antigravityRecorder{}
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.record(request)
		http.Error(writer, "boom", http.StatusServiceUnavailable)
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.record(request)
		http.Error(writer, "boom", http.StatusServiceUnavailable)
	}))
	t.Cleanup(second.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: freshAntigravityAccounts(t, "proj-1"),
		Endpoints:    []string{first.URL, second.URL},
	})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "All Antigravity endpoints failed. Last error:") {
		t.Fatalf("error = %v, want the all-endpoints failure", err)
	}
	if len(recorder.all()) != 2 {
		t.Fatalf("endpoint attempts = %d, want both endpoints tried", len(recorder.all()))
	}
}

// TestAntigravityNonRetryableHTTPErrorCarriesBody pins the 4xx error text
// (status, URL and upstream body).
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._post
func TestAntigravityNonRetryableHTTPErrorCarriesBody(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":"bad schema"}`)
	}))
	t.Cleanup(gateway.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: freshAntigravityAccounts(t, "proj-1"),
		Endpoints:    []string{gateway.URL},
	})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("AComplete = nil error, want the gateway rejection")
	}
	want := "Antigravity HTTP 400 from " + gateway.URL + "/v1internal:generateContent: {\"error\":\"bad schema\"}"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// antigravityUnusedTokenServer answers the refresh grant with a 500. The
// provider keeps its stale access token, exactly as upstream does when
// _do_token_refresh returns None.
func antigravityUnusedTokenServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "refresh down", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// TestAntigravityFallsBackToStaleAccessToken covers the soft refresh failure:
// the stale token is still sent so a gateway that has not yet rejected it can
// answer.
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._ensure_token
func TestAntigravityFallsBackToStaleAccessToken(t *testing.T) {
	recorder := &antigravityRecorder{}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.record(request)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`)
	}))
	t.Cleanup(gateway.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model: "gemini-3-flash",
		AccountsPath: writeAntigravityAccountsFile(t, map[string]any{
			"schemaVersion": 4,
			"accounts": []any{map[string]any{
				"refresh": "refresh-1|proj-1",
				"access":  "stale-access",
				"expires": 0, // unknown expiry: refresh first, stale token as fallback
			}},
		}),
		TokenURL:  antigravityUnusedTokenServer(t),
		Endpoints: []string{gateway.URL},
	})
	completion, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("AComplete = %v", err)
	}
	if completion.Content != "ok" {
		t.Fatalf("content = %q", completion.Content)
	}
	if got := recorder.all()[0].Headers.Get("Authorization"); got != "Bearer stale-access" {
		t.Fatalf("authorization = %q, want the stale access token", got)
	}
}

// TestAntigravityWithoutCredentials pins upstream's loud failure text.
// Parity: core/framework/llm/antigravity.py::AntigravityProvider.has_credentials
func TestAntigravityWithoutCredentials(t *testing.T) {
	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:     "gemini-3-flash",
		Endpoints: []string{"http://127.0.0.1:1"},
	})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil || err.Error() != antigravityCredentialError {
		t.Fatalf("error = %v, want %q", err, antigravityCredentialError)
	}
}

// TestAntigravityRefreshWithoutClientIDFails pins the one hard refresh error:
// upstream's get_antigravity_client_id raises before its try/except.
// Parity: core/framework/llm/antigravity.py::_do_token_refresh
func TestAntigravityRefreshWithoutClientIDFails(t *testing.T) {
	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: expiredAntigravityAccounts(t, "proj-1"),
		ClientID:     func() string { return "" },
		Endpoints:    []string{"http://127.0.0.1:1"},
	})
	_, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	})
	if err == nil || err.Error() != "Could not obtain Antigravity OAuth client ID" {
		t.Fatalf("error = %v, want the missing client id error", err)
	}
}

// TestAntigravityStreamParsesGeminiSSE drives the SSE path end to end.
// Parity: core/framework/llm/antigravity.py::_parse_sse_stream
func TestAntigravityStreamParsesGeminiSSE(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1internal:streamGenerateContent" || request.URL.Query().Get("alt") != "sse" {
			http.Error(writer, "unexpected stream path "+request.URL.String(), http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hel\"}]}}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}},\"traceId\":\"t1\"}\n\n"+
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"lo\"}]}}]}}\n\n"+
				"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"secret\",\"thought\":true}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":9}}}\n\n")
	}))
	t.Cleanup(gateway.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: freshAntigravityAccounts(t, "proj-1"),
		Endpoints:    []string{gateway.URL},
	})

	events := []StreamEvent{}
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}, func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("ACompleteStream = %v", err)
	}
	if completion.Content != "Hello" {
		t.Fatalf("content = %q, want Hello (thought parts dropped)", completion.Content)
	}
	if completion.StopReason != "stop" || completion.InputTokens != 5 || completion.OutputTokens != 9 {
		t.Fatalf("completion = %#v", completion)
	}

	var deltas []string
	var snapshots []string
	var textEnd string
	var finish *FinishEvent
	for _, event := range events {
		switch typed := event.(type) {
		case TextDeltaEvent:
			deltas = append(deltas, typed.Content)
			snapshots = append(snapshots, typed.Snapshot)
		case TextEndEvent:
			textEnd = typed.FullText
		case FinishEvent:
			copied := typed
			finish = &copied
		}
	}
	if strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("deltas = %#v", deltas)
	}
	if strings.Join(snapshots, "|") != "Hel|Hello" {
		t.Fatalf("snapshots = %#v", snapshots)
	}
	if textEnd != "Hello" {
		t.Fatalf("text end = %q", textEnd)
	}
	if finish == nil || finish.StopReason != "stop" || finish.Model != "gemini-3-flash" {
		t.Fatalf("finish = %#v", finish)
	}
	if finish.InputTokens != 5 || finish.OutputTokens != 9 {
		t.Fatalf("finish tokens = %d/%d", finish.InputTokens, finish.OutputTokens)
	}
}

// TestAntigravityStreamToolCallCarriesThoughtSignature checks the functionCall
// parse and the thoughtSignature echo on the next request.
// Parity: core/framework/llm/antigravity.py::_parse_sse_stream
func TestAntigravityStreamToolCallCarriesThoughtSignature(t *testing.T) {
	recorder := &antigravityRecorder{}
	gateway := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := recorder.record(request)
		inner, _ := body["request"].(map[string]any)
		contents, _ := inner["contents"].([]any)
		if len(contents) > 1 {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"run\",\"args\":{\"x\":1},\"id\":\"call-1\"},\"thoughtSignature\":\"sig-abc\"}]},\"finishReason\":\"OTHER\"}]}}\n\n")
	}))
	t.Cleanup(gateway.Close)

	provider := testAntigravityProvider(t, AntigravityOptions{
		Model:        "gemini-3-flash",
		AccountsPath: freshAntigravityAccounts(t, "proj-1"),
		Endpoints:    []string{gateway.URL},
	})

	events := []StreamEvent{}
	completion, err := provider.ACompleteStream(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "go"}},
	}, func(event StreamEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("ACompleteStream = %v", err)
	}
	if len(completion.ToolCalls) != 1 {
		t.Fatalf("tool calls = %#v", completion.ToolCalls)
	}
	call := completion.ToolCalls[0]
	if call.ID != "call-1" || call.Name != "run" || call.Arguments != `{"x":1}` {
		t.Fatalf("tool call = %#v", call)
	}
	if completion.StopReason != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use (OTHER)", completion.StopReason)
	}
	var toolEvent *ToolCallEvent
	for _, event := range events {
		if typed, ok := event.(ToolCallEvent); ok {
			copied := typed
			toolEvent = &copied
		}
	}
	if toolEvent == nil || toolEvent.ToolUseID != "call-1" || toolEvent.ToolName != "run" {
		t.Fatalf("tool event = %#v", toolEvent)
	}
	if value, _ := antigravityInt(toolEvent.ToolInput["x"]); value != 1 {
		t.Fatalf("tool input = %#v", toolEvent.ToolInput)
	}

	// A follow-up request echoes the signature on the functionCall part and
	// names the tool result from its tool_call_id.
	_, err = provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: "go"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "call-1", Name: "run", Arguments: `{"x":1}`}}},
			{Role: "tool", ToolCallID: "call-1", Content: "result-text"},
		},
	})
	if err != nil {
		t.Fatalf("follow-up AComplete = %v", err)
	}
	requests := recorder.all()
	if len(requests) != 2 {
		t.Fatalf("gateway requests = %d, want 2", len(requests))
	}
	followUp, _ := requests[1].Body["request"].(map[string]any)
	contents, _ := followUp["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("follow-up contents = %#v, want user/model/user", contents)
	}
	modelTurn, _ := contents[1].(map[string]any)
	modelParts, _ := modelTurn["parts"].([]any)
	fcPart, _ := modelParts[0].(map[string]any)
	if fcPart["thoughtSignature"] != "sig-abc" {
		t.Fatalf("functionCall part = %#v, want the part-level thoughtSignature", fcPart)
	}
	toolTurn, _ := contents[2].(map[string]any)
	toolParts, _ := toolTurn["parts"].([]any)
	responsePart, _ := toolParts[0].(map[string]any)
	functionResponse, _ := responsePart["functionResponse"].(map[string]any)
	if functionResponse["name"] != "run" || functionResponse["id"] != "call-1" {
		t.Fatalf("functionResponse = %#v, want the tool_call_id → name mapping", functionResponse)
	}
	responseBody, _ := functionResponse["response"].(map[string]any)
	if responseBody["content"] != "result-text" {
		t.Fatalf("functionResponse response = %#v", responseBody)
	}
}

// TestAntigravityAccountsFileV4Schema pins the V4 parsing: refresh carries
// "token|project", expires is milliseconds and a near-expiry access token is
// dropped so a refresh runs.
// Parity: core/framework/llm/antigravity.py::_load_from_json_file
func TestAntigravityAccountsFileV4Schema(t *testing.T) {
	path := writeAntigravityAccountsFile(t, map[string]any{
		"schemaVersion": 4,
		"accounts": []any{
			map[string]any{"enabled": false, "refresh": "disabled-token|disabled-proj"},
			map[string]any{
				"refresh": "refresh-token|project-x",
				"access":  "ya29.access",
				"expires": antigravityTestNow.Add(30 * time.Second).UnixMilli(),
			},
		},
	})
	access, refresh, project, expires := loadAntigravityAccountsFile(path, antigravityTestNow)
	if access != "" {
		t.Fatalf("access = %q, want near-expiry dropped", access)
	}
	if refresh != "refresh-token" || project != "project-x" {
		t.Fatalf("refresh/project = %q/%q", refresh, project)
	}
	if !expires.IsZero() {
		t.Fatalf("expires = %v, want zero for a dropped token", expires)
	}

	freshPath := writeAntigravityAccountsFile(t, map[string]any{
		"schemaVersion": 4,
		"accounts": []any{map[string]any{
			"refresh": "refresh-token|project-x",
			"access":  "ya29.access",
			"expires": antigravityTestNow.Add(2 * time.Hour).UnixMilli(),
		}},
	})
	access, _, _, expires = loadAntigravityAccountsFile(freshPath, antigravityTestNow)
	if access != "ya29.access" || expires.IsZero() {
		t.Fatalf("fresh access/expires = %q/%v", access, expires)
	}
}

// TestAntigravityAccountsFileLegacySchema pins the V1–V3 last_refresh estimate.
// Parity: core/framework/llm/antigravity.py::_load_from_json_file
func TestAntigravityAccountsFileLegacySchema(t *testing.T) {
	path := writeAntigravityAccountsFile(t, map[string]any{
		"accounts":     []any{map[string]any{"accessToken": "legacy-access", "refreshToken": "legacy-refresh"}},
		"last_refresh": "2026-11-22T11:30:00Z",
	})
	access, refresh, project, expires := loadAntigravityAccountsFile(path, antigravityTestNow)
	if access != "legacy-access" || refresh != "legacy-refresh" {
		t.Fatalf("access/refresh = %q/%q", access, refresh)
	}
	if project != AntigravityDefaultProjectID {
		t.Fatalf("project = %q, want the default", project)
	}
	wantExpiry := time.Date(2026, 11, 22, 12, 30, 0, 0, time.UTC)
	if !expires.Equal(wantExpiry) {
		t.Fatalf("expires = %v, want %v", expires, wantExpiry)
	}

	oldPath := writeAntigravityAccountsFile(t, map[string]any{
		"accounts":     []any{map[string]any{"accessToken": "legacy-access", "refreshToken": "legacy-refresh"}},
		"last_refresh": "2026-11-22T09:00:00Z",
	})
	access, _, _, _ = loadAntigravityAccountsFile(oldPath, antigravityTestNow)
	if access != "" {
		t.Fatalf("access = %q, want the stale legacy token dropped", access)
	}
}

// TestAntigravityIDEStateDBExtractsTokens pins the SQLite fallback: the VS Code
// ItemTable value is double-base64 and the ya29./1// tokens are recovered from
// the nested payload.
// Parity: core/framework/llm/antigravity.py::_load_from_ide_db
func TestAntigravityIDEStateDBExtractsTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE ItemTable (key TEXT, value BLOB)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	inner := []byte("prefix ya29.ide-access_token-1.2 middle 1//ide-refresh_token-3.4 suffix")
	candidate := base64.StdEncoding.EncodeToString(inner)
	blob := []byte("wrapper " + candidate + " wrapper")
	value := base64.StdEncoding.EncodeToString(blob)
	if _, err := db.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", antigravityIDEStateKey, []byte(value)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	_ = db.Close()

	mtime := antigravityTestNow.Add(-10 * time.Minute)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	access, refresh, expires := loadAntigravityIDEDB([]string{path})
	if access != "ya29.ide-access_token-1.2" {
		t.Fatalf("access = %q", access)
	}
	if refresh != "1//ide-refresh_token-3.4" {
		t.Fatalf("refresh = %q", refresh)
	}
	if want := mtime.Add(time.Hour); !expires.Equal(want) {
		t.Fatalf("expires = %v, want the DB mtime + 1h (%v)", expires, want)
	}
}
