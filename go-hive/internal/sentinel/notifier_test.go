package sentinel

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type capturedRequest struct {
	method  string
	url     string
	body    map[string]any
	headers http.Header
}

type fakeResponse struct {
	status int
	body   string
	err    error
}

type fakeHTTPClient struct {
	responses []fakeResponse
	requests  []capturedRequest
}

func (c *fakeHTTPClient) Do(req *http.Request) (*http.Response, error) {
	decoded := map[string]any{}
	raw := []byte{}
	var err error
	if req.Body != nil {
		raw, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, err
		}
	}
	c.requests = append(c.requests, capturedRequest{method: req.Method, url: req.URL.String(), body: decoded, headers: req.Header.Clone()})
	if len(c.responses) == 0 {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	if response.err != nil {
		return nil, response.err
	}
	status := response.status
	if status == 0 {
		status = 200
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response.body))}, nil
}

func newTestNotifier(t *testing.T, env map[string]string, responses ...fakeResponse) (*HTTPNotifier, *fakeHTTPClient) {
	t.Helper()
	client := &fakeHTTPClient{responses: responses}
	notifier := NewHTTPNotifier(client)
	notifier.Getenv = func(name string) string { return env[name] }
	return notifier, client
}

// Parity: core/framework/sentinel/notifier.py, core/tests/test_sentinel_notifier.py::test_telegram_send_threads_reply
func TestTelegramSendThreadsReply(t *testing.T) {
	notifier, client := newTestNotifier(t, map[string]string{"TELEGRAM_BOT_TOKEN": "T"}, fakeResponse{body: `{"ok": true, "result": {"message_id": 77}}`})
	result := notifier.Send(context.Background(), "telegram", map[string]any{"chat_id": "9"}, "hello", map[string]any{"message_id": 5}, nil)
	if !result.OK || result.MessageID != "77" {
		t.Fatalf("result = %#v", result)
	}
	body := client.requests[0].body
	if PyStr(body["chat_id"]) != "9" {
		t.Fatalf("chat_id = %v", body["chat_id"])
	}
	if value, _ := ToFloat(body["reply_to_message_id"]); value != 5 {
		t.Fatalf("reply_to_message_id = %v", body["reply_to_message_id"])
	}
	if !strings.Contains(client.requests[0].url, "/botT/sendMessage") {
		t.Fatalf("url = %s", client.requests[0].url)
	}
}

// Parity: core/framework/sentinel/notifier.py, core/tests/test_sentinel_notifier.py::test_slack_send_threads_and_auths
func TestSlackSendThreadsAndAuths(t *testing.T) {
	notifier, client := newTestNotifier(t, map[string]string{"SLACK_BOT_TOKEN": "xoxb-1"}, fakeResponse{body: `{"ok": true, "ts": "123.45"}`})
	result := notifier.Send(context.Background(), "slack", map[string]any{"channel": "C1"}, "hi", map[string]any{"ts": "100.1"}, nil)
	if !result.OK || result.MessageID != "123.45" {
		t.Fatalf("result = %#v", result)
	}
	request := client.requests[0]
	if request.body["channel"] != "C1" || request.body["thread_ts"] != "100.1" {
		t.Fatalf("body = %#v", request.body)
	}
	if request.headers.Get("Authorization") != "Bearer xoxb-1" {
		t.Fatalf("authorization = %q", request.headers.Get("Authorization"))
	}
}

// Parity: core/framework/sentinel/notifier.py, core/tests/test_sentinel_notifier.py::test_telegram_error_surfaces
func TestTelegramErrorSurfaces(t *testing.T) {
	notifier, _ := newTestNotifier(t, map[string]string{"TELEGRAM_BOT_TOKEN": "T"}, fakeResponse{body: `{"ok": false, "description": "chat not found"}`})
	result := notifier.Send(context.Background(), "telegram", map[string]any{"chat_id": "9"}, "hi", nil, nil)
	if result.OK {
		t.Fatal("send should fail")
	}
	if !strings.Contains(result.Error, "chat not found") {
		t.Fatalf("error = %q", result.Error)
	}
}

// Parity: core/framework/sentinel/notifier.py, core/tests/test_sentinel_notifier.py::test_missing_token_fails_cleanly
func TestMissingTokenFailsCleanly(t *testing.T) {
	notifier, _ := newTestNotifier(t, map[string]string{})
	result := notifier.Send(context.Background(), "telegram", map[string]any{"chat_id": "9"}, "hi", nil, nil)
	if result.OK {
		t.Fatal("send should fail without a token")
	}
	if !strings.Contains(strings.ToLower(result.Error), "token") {
		t.Fatalf("error = %q", result.Error)
	}
}

// Parity: core/framework/sentinel/notifier.py, core/tests/test_sentinel_notifier.py::test_unknown_channel
func TestUnknownChannel(t *testing.T) {
	notifier, _ := newTestNotifier(t, map[string]string{})
	if result := notifier.Send(context.Background(), "carrier_pigeon", map[string]any{}, "hi", nil, nil); result.OK {
		t.Fatal("unknown channel should fail")
	}
}

// Parity-exempt: notifier.py::_send_hive has no Python test; this asserts the
// structured Hive ingest payload and account-JWT auth directly.
func TestHiveSendPostsStructuredMeta(t *testing.T) {
	notifier, client := newTestNotifier(t, map[string]string{
		"HIVE_CLOUD_BASE": "https://backend.example",
		"HIVE_CLOUD_JWT":  "jwt-1",
	}, fakeResponse{body: `{"id": "row-1"}`})
	result := notifier.Send(context.Background(), "hive", map[string]any{}, "body text", nil, map[string]any{
		"runtime_id":        "rt-1",
		"runtime_kind":      "local",
		"session_id":        "s1",
		"colony_id":         "c1",
		"kind":              "blocker",
		"title":             "Colony c1 needs you",
		"correlation_token": "tok",
	})
	if !result.OK || result.MessageID != "row-1" {
		t.Fatalf("result = %#v", result)
	}
	request := client.requests[0]
	if !strings.HasSuffix(request.url, "/v1/inbox/messages") {
		t.Fatalf("url = %s", request.url)
	}
	if request.headers.Get("Authorization") != "Bearer jwt-1" {
		t.Fatalf("authorization = %q", request.headers.Get("Authorization"))
	}
	if request.body["body"] != "body text" || request.body["runtime_id"] != "rt-1" || request.body["correlation_token"] != "tok" {
		t.Fatalf("body = %#v", request.body)
	}
}

// Parity-exempt: notifier.py::resolve_hive_session has no Python test.
func TestResolveHiveSession(t *testing.T) {
	notifier, client := newTestNotifier(t, map[string]string{
		"HIVE_CLOUD_BASE": "https://backend.example",
		"HIVE_CLOUD_JWT":  "jwt-1",
	}, fakeResponse{status: 204, body: `{}`})
	if !notifier.ResolveHiveSession(context.Background(), "s1", "local") {
		t.Fatal("resolve should succeed on 2xx")
	}
	if !strings.HasSuffix(client.requests[0].url, "/v1/inbox/resolve") {
		t.Fatalf("url = %s", client.requests[0].url)
	}
}
