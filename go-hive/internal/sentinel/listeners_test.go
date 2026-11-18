package sentinel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Parity-exempt: listeners.py has no Python unit tests; the Telegram parsing
// surface is covered here directly.
func TestTelegramHandleUpdateRecordsChatAndForwards(t *testing.T) {
	sink := &noopSink{}
	listener := NewTelegramListener(sink, nil, newFakeClock(), func() string { return "T" }, "")
	listener.HandleUpdate(context.Background(), map[string]any{
		"update_id": 10,
		"message": map[string]any{
			"message_id": 3,
			"text":       "yes go ahead",
			"chat":       map[string]any{"id": 99, "first_name": "Ada"},
			"from":       map[string]any{"id": 42, "username": "ada"},
			"reply_to_message": map[string]any{
				"message_id": 5,
			},
		},
	})
	if len(sink.inbound) != 1 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
	call := sink.inbound[0]
	if call.channel != "telegram" || call.senderID != "42" || call.text != "yes go ahead" || call.source != "99" {
		t.Fatalf("call = %#v", call)
	}
	if value, _ := ToFloat(call.threadRef["message_id"]); value != 5 {
		t.Fatalf("threadRef = %#v", call.threadRef)
	}
	chat := sink.chats["telegram"]
	if chat["chat_title"] != "Ada" || chat["sender_name"] != "ada" {
		t.Fatalf("chat = %#v", chat)
	}
}

// Parity-exempt: listeners.py has no Python unit tests.
func TestTelegramHandleUpdateIgnoresNonText(t *testing.T) {
	sink := &noopSink{}
	listener := NewTelegramListener(sink, nil, newFakeClock(), func() string { return "T" }, "")
	listener.HandleUpdate(context.Background(), map[string]any{
		"message": map[string]any{
			"chat": map[string]any{"id": 99},
			"from": map[string]any{"id": 42},
		},
	})
	if len(sink.inbound) != 0 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
	// The chat is still recorded (a Start tap identifies the chat).
	if sink.chats["telegram"] == nil {
		t.Fatal("chat should be recorded before the text gate")
	}
}

type fakeSocket struct {
	acknowledged []map[string]any
}

func (s *fakeSocket) ReadMessage() ([]byte, error) { return nil, nil }

func (s *fakeSocket) WriteJSON(value any) error {
	if decoded, ok := value.(map[string]any); ok {
		s.acknowledged = append(s.acknowledged, decoded)
	}
	return nil
}

func (s *fakeSocket) Close() error { return nil }

// Parity-exempt: listeners.py has no Python unit tests.
func TestSlackHandleWSMessageAcksAndForwards(t *testing.T) {
	sink := &noopSink{}
	socket := &fakeSocket{}
	listener := NewSlackSocketListener(sink, nil, newFakeClock(), func() string { return "xapp-1" }, "", nil)
	listener.HandleWSMessage(context.Background(), socket, `{
		"envelope_id": "env-1",
		"type": "events_api",
		"payload": {"event": {"type": "message", "user": "U1", "channel": "C1", "text": "go", "ts": "100.1", "thread_ts": "99.9"}}
	}`)
	if len(socket.acknowledged) != 1 || socket.acknowledged[0]["envelope_id"] != "env-1" {
		t.Fatalf("ack = %#v", socket.acknowledged)
	}
	if len(sink.inbound) != 1 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
	call := sink.inbound[0]
	if call.channel != "slack" || call.senderID != "U1" || call.text != "go" || call.source != "C1" {
		t.Fatalf("call = %#v", call)
	}
	if call.threadRef["ts"] != "99.9" {
		t.Fatalf("threadRef = %#v", call.threadRef)
	}
}

// Parity-exempt: listeners.py has no Python unit tests.
func TestSlackHandleWSMessageIgnoresBotEcho(t *testing.T) {
	sink := &noopSink{}
	listener := NewSlackSocketListener(sink, nil, newFakeClock(), func() string { return "xapp-1" }, "", nil)
	listener.HandleWSMessage(context.Background(), &fakeSocket{}, `{
		"type": "events_api",
		"payload": {"event": {"type": "message", "bot_id": "B1", "text": "our own echo"}}
	}`)
	if len(sink.inbound) != 0 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
}

// Parity-exempt: listeners.py has no Python unit tests.
func TestSlackOpenSocketSendsAppToken(t *testing.T) {
	client := &fakeHTTPClient{responses: []fakeResponse{{body: `{"ok": true, "url": "wss://socket.example"}`}}}
	listener := NewSlackSocketListener(nil, client, newFakeClock(), func() string { return "xapp-1" }, "", nil)
	url, err := listener.OpenSocket(context.Background(), "xapp-1")
	if err != nil || url != "wss://socket.example" {
		t.Fatalf("url = %q, err = %v", url, err)
	}
	if client.requests[0].headers.Get("Authorization") != "Bearer xapp-1" {
		t.Fatalf("authorization = %q", client.requests[0].headers.Get("Authorization"))
	}
	if !strings.HasSuffix(client.requests[0].url, "/apps.connections.open") {
		t.Fatalf("url = %s", client.requests[0].url)
	}
}

// Parity-exempt: listeners.py has no Python unit tests; this covers the SSE
// frame parser and the (ref:) envelope wrapping.
func TestHiveConsumeSSEDispatchesReply(t *testing.T) {
	sink := &noopSink{}
	listener := NewHiveInboxListener(sink, nil, newFakeClock(), nil)
	frames := ": ping\n\n" +
		"event: reply\n" +
		"data: {\"correlation_token\": \"tok\", \"text\": \"hello\", \"colony_id\": \"c1\"}\n\n" +
		"event: other\n" +
		"data: {\"correlation_token\": \"tok2\", \"text\": \"nope\"}\n\n"
	if err := listener.ConsumeSSE(context.Background(), strings.NewReader(frames)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(sink.inbound) != 1 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
	call := sink.inbound[0]
	if call.channel != "hive" || call.senderID != "hive" || call.source != "c1" {
		t.Fatalf("call = %#v", call)
	}
	if call.text != FormatRef("tok")+" hello" {
		t.Fatalf("envelope = %q", call.text)
	}
}

// Parity-exempt: listeners.py has no Python unit tests.
func TestHiveConsumeSSESkipsMalformedAndEmptyReplies(t *testing.T) {
	sink := &noopSink{}
	listener := NewHiveInboxListener(sink, nil, newFakeClock(), nil)
	frames := "event: reply\ndata: not-json\n\n" +
		"event: reply\ndata: {\"correlation_token\": \"tok\"}\n\n"
	if err := listener.ConsumeSSE(context.Background(), strings.NewReader(frames)); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if len(sink.inbound) != 0 {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
}

type cancelAfterFirstClient struct {
	cancel context.CancelFunc
	urls   []string
	calls  int
}

func (c *cancelAfterFirstClient) Do(request *http.Request) (*http.Response, error) {
	c.calls++
	c.urls = append(c.urls, request.URL.String())
	if c.calls == 1 {
		body := `{"ok": true, "result": [{"update_id": 10, "message": {"text": "hi", "chat": {"id": 9}, "from": {"id": 1}}}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	c.cancel()
	return nil, context.Canceled
}

// Parity-exempt: listeners.py has no Python unit tests.
func TestTelegramRunAdvancesOffsetAndStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &noopSink{}
	client := &cancelAfterFirstClient{cancel: cancel}
	listener := NewTelegramListener(sink, client, newFakeClock(), func() string { return "T" }, "")
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("telegram Run did not stop after cancellation")
	}
	if len(client.urls) < 2 {
		t.Fatalf("urls = %#v", client.urls)
	}
	if !strings.Contains(client.urls[0], "allowed_updates") {
		t.Fatalf("first url = %s", client.urls[0])
	}
	if !strings.Contains(client.urls[1], "offset=11") {
		t.Fatalf("second url should advance the offset: %s", client.urls[1])
	}
	if len(sink.inbound) != 1 || sink.inbound[0].text != "hi" {
		t.Fatalf("inbound = %#v", sink.inbound)
	}
}
