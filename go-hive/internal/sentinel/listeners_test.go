package sentinel

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
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

// ----- Socket Mode run loop ------------------------------------------------

type fakeRead struct {
	data []byte
	err  error
}

// fakeSocketConn is a scripted SocketConn. Reads come from the script; once it
// is exhausted ReadMessage blocks until Close, so Run's reconnect and shutdown
// paths see a real socket's behavior.
type fakeSocketConn struct {
	mu        sync.Mutex
	script    []fakeRead
	acked     []map[string]any
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakeSocketConn(script ...fakeRead) *fakeSocketConn {
	return &fakeSocketConn{script: script, closed: make(chan struct{})}
}

func (c *fakeSocketConn) ReadMessage() ([]byte, error) {
	c.mu.Lock()
	if len(c.script) > 0 {
		next := c.script[0]
		c.script = c.script[1:]
		c.mu.Unlock()
		if next.err != nil {
			return nil, next.err
		}
		return next.data, nil
	}
	c.mu.Unlock()
	<-c.closed
	return nil, io.EOF
}

func (c *fakeSocketConn) WriteJSON(value any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if decoded, ok := value.(map[string]any); ok {
		c.acked = append(c.acked, decoded)
	}
	return nil
}

func (c *fakeSocketConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeSocketConn) ackCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.acked)
}

type fakeDialer struct {
	mu    sync.Mutex
	dial  func(call int, url string) (SocketConn, error)
	calls int
	urls  []string
}

func (d *fakeDialer) Dial(ctx context.Context, url string) (SocketConn, error) {
	d.mu.Lock()
	call := d.calls
	d.calls++
	d.urls = append(d.urls, url)
	d.mu.Unlock()
	if d.dial == nil {
		return newFakeSocketConn(), nil
	}
	conn, err := d.dial(call, url)
	if err != nil {
		return nil, err
	}
	if conn != nil {
		// A real dialer closes the socket on cancellation; the fake mirrors
		// that so a blocking ReadMessage cannot outlive the Run context.
		go func() {
			<-ctx.Done()
			_ = conn.Close()
		}()
	}
	return conn, nil
}

func (d *fakeDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

type cancelClock struct {
	*fakeClock
	after  int
	sleeps int
	cancel context.CancelFunc
}

func (c *cancelClock) Sleep(ctx context.Context, d time.Duration) error {
	c.sleeps++
	if c.sleeps >= c.after {
		c.cancel()
	}
	return ctx.Err()
}

// Parity-exempt: listeners.py has no Python unit tests; this drives the real
// Socket Mode Run loop (open → dial → ack/dispatch → reconnect) with fakes.
func TestSlackRunDispatchesInboundAndReconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &noopSink{}
	client := &fakeHTTPClient{responses: []fakeResponse{
		{body: `{"ok": true, "url": "wss://one"}`},
		{body: `{"ok": true, "url": "wss://two"}`},
	}}
	envelope := `{"envelope_id":"env-1","type":"events_api","payload":{"event":{"type":"message","user":"U1","channel":"C1","text":"go","ts":"100.1"}}}`
	first := newFakeSocketConn(fakeRead{data: []byte(envelope)}, fakeRead{err: io.EOF})
	second := newFakeSocketConn()
	dialer := &fakeDialer{dial: func(call int, url string) (SocketConn, error) {
		switch call {
		case 0:
			if url != "wss://one" {
				t.Errorf("first dial url = %q", url)
			}
			return first, nil
		case 1:
			if url != "wss://two" {
				t.Errorf("second dial url = %q", url)
			}
			return second, nil
		}
		return newFakeSocketConn(), nil
	}}
	listener := NewSlackSocketListener(sink, client, newFakeClock(), func() string { return "xapp-1" }, "", dialer)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()

	waitFor(t, 2*time.Second, "inbound dispatch and reconnect", func() bool {
		return sink.inboundCount() == 1 && dialer.callCount() >= 2
	})
	calls := sink.inboundSnapshot()
	if calls[0].channel != "slack" || calls[0].senderID != "U1" || calls[0].text != "go" || calls[0].source != "C1" {
		t.Fatalf("inbound = %#v", calls[0])
	}
	if first.ackCount() != 1 {
		t.Fatalf("ack count = %d, want 1", first.ackCount())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slack Run did not stop after cancellation")
	}
}

// Parity-exempt: listeners.py has no Python unit tests; this pins the two
// retry seams (apps.connections.open failure, dial failure) before a good
// connection is consumed.
func TestSlackRunRetriesOpenAndDialFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &noopSink{}
	client := &fakeHTTPClient{responses: []fakeResponse{
		{body: `{"ok": false, "error": "ratelimited"}`},
		{body: `{"ok": true, "url": "wss://one"}`},
		{body: `{"ok": true, "url": "wss://two"}`},
	}}
	envelope := `{"envelope_id":"env-2","type":"events_api","payload":{"event":{"type":"message","user":"U2","channel":"C2","text":"resume","ts":"200.1"}}}`
	good := newFakeSocketConn(fakeRead{data: []byte(envelope)})
	dialer := &fakeDialer{dial: func(call int, _ string) (SocketConn, error) {
		if call == 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return good, nil
	}}
	listener := NewSlackSocketListener(sink, client, newFakeClock(), func() string { return "xapp-1" }, "", dialer)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()

	waitFor(t, 2*time.Second, "inbound dispatch after retries", func() bool {
		return sink.inboundCount() == 1
	})
	calls := sink.inboundSnapshot()
	if calls[0].text != "resume" {
		t.Fatalf("inbound = %#v", calls[0])
	}
	if len(client.requests) < 3 {
		t.Fatalf("apps.connections.open calls = %d, want at least 3", len(client.requests))
	}
	if dialer.callCount() < 2 {
		t.Fatalf("dial calls = %d, want at least 2", dialer.callCount())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slack Run did not stop after cancellation")
	}
}

// Parity-exempt: negative regression for the wiring gap this lane closes — a
// listener without a dialer must never report or dispatch an inbound event.
func TestSlackRunWithoutDialerNeverDispatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := &cancelClock{fakeClock: newFakeClock(), after: 2, cancel: cancel}
	sink := &noopSink{}
	client := &fakeHTTPClient{responses: []fakeResponse{{body: `{"ok": true, "url": "wss://one"}`}}}
	listener := NewSlackSocketListener(sink, client, clock, func() string { return "xapp-1" }, "", nil)
	done := make(chan error, 1)
	go func() { done <- listener.Run(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slack Run without a dialer did not stop after cancellation")
	}
	if sink.inboundCount() != 0 {
		t.Fatalf("inbound = %#v, want none", sink.inboundSnapshot())
	}
	if clock.sleeps < 2 {
		t.Fatalf("backoff sleeps = %d, want at least 2", clock.sleeps)
	}
}

// Parity-exempt: the production listener factory must hand the Slack listener
// a real dialer; a nil one is the exact gap that left Socket Mode inbound dead.
func TestDefaultListenerFactoryWiresSlackSocketDialer(t *testing.T) {
	store := NewStore(t.TempDir())
	manager := NewManager(ManagerOptions{
		Store:    store,
		Tokens:   fixedSecretTokens(t, store),
		Notifier: &refreshTokenNotifier{slackApp: "xapp-1"},
		Clock:    newFakeClock(),
	})
	listener := DefaultListenerFactory(ChannelSlack, manager)
	slack, ok := listener.(*SlackSocketListener)
	if !ok || slack == nil {
		t.Fatalf("factory listener = %#v, want *SlackSocketListener", listener)
	}
	if slack.Dialer == nil {
		t.Fatal("Slack Socket Mode listener has no dialer; a configured app token would back off forever")
	}
	if got := slack.AppToken(); got != "xapp-1" {
		t.Fatalf("app token = %q", got)
	}
}
