package sentinel

// Inbound listeners — receive user replies and hand them to the manager.
//
// Every channel uses an *outbound* connection, which works behind NAT (the
// runtime is on localhost): Telegram via getUpdates long-poll, Slack via
// Socket Mode, Hive Inbox via an SSE subscription to hive-backend. Each runs
// as a single process-global task — both Telegram and Slack are
// single-connection per token, so two pollers would conflict.
//
// These never raise out: on any error they log and back off, so a flaky
// network cannot take down the manager. They simply forward parsed messages to
// the manager's OnInbound; all auth/routing/resume lives there.
//
// Parity: core/framework/sentinel/listeners.py

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTelegramPollTimeoutSeconds = 30
	defaultTelegramBackoff            = 3 * time.Second
	defaultSlackBackoff               = 5 * time.Second
)

// InboundSink is the manager surface the listeners need.
type InboundSink interface {
	RecordRecentChat(channel string, info map[string]any)
	OnInbound(ctx context.Context, channel, senderID, text string, threadRef map[string]any, source string)
}

// Listener is one long-lived inbound subscription.
type Listener interface {
	Run(ctx context.Context) error
}

func sleepOrDone(ctx context.Context, clock Clock, d time.Duration) bool {
	if clock == nil {
		clock = RealClock{}
	}
	if err := clock.Sleep(ctx, d); err != nil {
		return false
	}
	return true
}

// ----- Telegram -----------------------------------------------------------

// TelegramListener long-polls getUpdates and forwards text messages.
type TelegramListener struct {
	Sink    InboundSink
	Client  HTTPDoer
	Clock   Clock
	Token   func() string
	BaseURL string
}

// NewTelegramListener returns a Telegram listener. An empty token or base URL
// disables it at Run time.
func NewTelegramListener(sink InboundSink, client HTTPDoer, clock Clock, token func() string, baseURL string) *TelegramListener {
	return &TelegramListener{Sink: sink, Client: client, Clock: clock, Token: token, BaseURL: baseURL}
}

func (l *TelegramListener) baseURL() string {
	if l.BaseURL != "" {
		return l.BaseURL
	}
	return defaultTelegramAPI
}

func (l *TelegramListener) client() HTTPDoer {
	if l.Client != nil {
		return l.Client
	}
	return defaultHTTPDoer()
}

// Run long-polls Telegram until the context is cancelled. Any error backs off
// and retries; it never returns a transient failure.
func (l *TelegramListener) Run(ctx context.Context) error {
	token := ""
	if l.Token != nil {
		token = l.Token()
	}
	if token == "" {
		return nil
	}
	var offset *int64
	log.Printf("[sentinel] telegram listener started")
	for {
		query := url.Values{}
		query.Set("timeout", strconv.Itoa(defaultTelegramPollTimeoutSeconds))
		query.Set("allowed_updates", `["message"]`)
		if offset != nil {
			query.Set("offset", strconv.FormatInt(*offset, 10))
		}
		endpoint := fmt.Sprintf("%s/bot%s/getUpdates?%s", l.baseURL(), token, query.Encode())
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		response, err := l.client().Do(request)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("[sentinel] telegram listener stopped")
				return nil
			}
			log.Printf("[sentinel] telegram poll error: %v", err)
			if !sleepOrDone(ctx, l.Clock, defaultTelegramBackoff) {
				log.Printf("[sentinel] telegram listener stopped")
				return nil
			}
			continue
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[sentinel] telegram poll read error: %v", readErr)
			if !sleepOrDone(ctx, l.Clock, defaultTelegramBackoff) {
				return nil
			}
			continue
		}
		var decoded struct {
			Result []map[string]any `json:"result"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			log.Printf("[sentinel] telegram poll decode error: %v", err)
			if !sleepOrDone(ctx, l.Clock, defaultTelegramBackoff) {
				return nil
			}
			continue
		}
		for _, update := range decoded.Result {
			if value, ok := ToInt(update["update_id"]); ok {
				next := int64(value) + 1
				offset = &next
			}
			l.HandleUpdate(ctx, update)
		}
	}
}

// HandleUpdate parses one Telegram update and forwards it.
func (l *TelegramListener) HandleUpdate(ctx context.Context, update map[string]any) {
	message, _ := update["message"].(map[string]any)
	if message == nil {
		return
	}
	chat, _ := message["chat"].(map[string]any)
	sender, _ := message["from"].(map[string]any)

	senderID := ""
	if value, ok := sender["id"]; ok && value != nil {
		senderID = PyStr(value)
	}
	chatID := ""
	if value, ok := chat["id"]; ok && value != nil {
		chatID = PyStr(value)
	}
	// Record the chat for sentinel_setup detect_chat BEFORE the text gate
	// (a "Start" tap or any message is enough to identify the chat).
	if chatID != "" && l.Sink != nil {
		title := ""
		for _, key := range []string{"title", "first_name", "username"} {
			if value, ok := chat[key].(string); ok && value != "" {
				title = value
				break
			}
		}
		senderName := ""
		for _, key := range []string{"username", "first_name"} {
			if value, ok := sender[key].(string); ok && value != "" {
				senderName = value
				break
			}
		}
		l.Sink.RecordRecentChat("telegram", map[string]any{
			"chat_id":     chatID,
			"sender_id":   senderID,
			"chat_title":  title,
			"sender_name": senderName,
		})
	}

	text, _ := message["text"].(string)
	if text == "" {
		return
	}
	threadRef := map[string]any{}
	if replyTo, ok := message["reply_to_message"].(map[string]any); ok {
		if messageID, ok := replyTo["message_id"]; ok && messageID != nil {
			threadRef = map[string]any{"message_id": messageID}
		}
	}
	if l.Sink != nil {
		l.Sink.OnInbound(ctx, "telegram", senderID, text, threadRef, chatID)
	}
}

// ----- Slack Socket Mode --------------------------------------------------

// SocketConn is the minimal websocket surface Slack Socket Mode needs, so the
// transport can be injected without a third-party websocket dependency.
type SocketConn interface {
	ReadMessage() ([]byte, error)
	WriteJSON(value any) error
	Close() error
}

// SocketDialer opens a websocket connection to a Socket Mode URL.
type SocketDialer interface {
	Dial(ctx context.Context, url string) (SocketConn, error)
}

// SlackSocketListener receives Slack events over Socket Mode.
type SlackSocketListener struct {
	Sink     InboundSink
	Client   HTTPDoer
	Clock    Clock
	AppToken func() string
	BaseURL  string
	Dialer   SocketDialer
}

// NewSlackSocketListener returns a Slack listener. A nil dialer makes Run back
// off with a clear log until one is supplied; the parsing surface remains
// fully testable.
func NewSlackSocketListener(sink InboundSink, client HTTPDoer, clock Clock, appToken func() string, baseURL string, dialer SocketDialer) *SlackSocketListener {
	return &SlackSocketListener{Sink: sink, Client: client, Clock: clock, AppToken: appToken, BaseURL: baseURL, Dialer: dialer}
}

func (l *SlackSocketListener) baseURL() string {
	if l.BaseURL != "" {
		return l.BaseURL
	}
	return defaultSlackAPI
}

func (l *SlackSocketListener) client() HTTPDoer {
	if l.Client != nil {
		return l.Client
	}
	return defaultHTTPDoer()
}

// Run opens and consumes Socket Mode connections until cancelled.
func (l *SlackSocketListener) Run(ctx context.Context) error {
	appToken := ""
	if l.AppToken != nil {
		appToken = l.AppToken()
	}
	if appToken == "" {
		return nil
	}
	log.Printf("[sentinel] slack socket-mode listener started")
	for {
		url, err := l.OpenSocket(ctx, appToken)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[sentinel] slack apps.connections.open error: %v", err)
			if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
				return nil
			}
			continue
		}
		if url == "" {
			if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
				return nil
			}
			continue
		}
		if l.Dialer == nil {
			log.Printf("[sentinel] slack socket dialer not configured")
			if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
				return nil
			}
			continue
		}
		connection, err := l.Dialer.Dial(ctx, url)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[sentinel] slack socket error: %v", err)
			if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
				return nil
			}
			continue
		}
		for {
			raw, err := connection.ReadMessage()
			if err != nil {
				connection.Close()
				if ctx.Err() != nil {
					log.Printf("[sentinel] slack listener stopped")
					return nil
				}
				log.Printf("[sentinel] slack socket error: %v", err)
				if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
					return nil
				}
				break
			}
			l.HandleWSMessage(ctx, connection, string(raw))
		}
	}
}

// OpenSocket calls apps.connections.open and returns the websocket URL ("" on
// a logical failure).
func (l *SlackSocketListener) OpenSocket(ctx context.Context, appToken string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, l.baseURL()+"/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+appToken)
	response, err := l.client().Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	if ok, _ := decoded["ok"].(bool); !ok {
		log.Printf("[sentinel] slack apps.connections.open failed: %v", decoded["error"])
		return "", nil
	}
	value, _ := decoded["url"].(string)
	return value, nil
}

// HandleWSMessage parses one Socket Mode envelope, acks it, and forwards
// message events. Bot echoes (including our own outbound messages) are
// ignored.
func (l *SlackSocketListener) HandleWSMessage(ctx context.Context, connection SocketConn, raw string) {
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return
	}
	if envelopeID, ok := decoded["envelope_id"]; ok && PyTruthy(envelopeID) {
		if connection != nil {
			_ = connection.WriteJSON(map[string]any{"envelope_id": envelopeID})
		}
	}
	if kind, _ := decoded["type"].(string); kind != "events_api" {
		return
	}
	payload, _ := decoded["payload"].(map[string]any)
	event, _ := payload["event"].(map[string]any)
	if event == nil {
		return
	}
	if kind, _ := event["type"].(string); kind != "message" {
		return
	}
	if PyTruthy(event["bot_id"]) || PyTruthy(event["subtype"]) {
		return
	}
	text, _ := event["text"].(string)
	if text == "" {
		return
	}
	senderID := ""
	if value, ok := event["user"]; ok && value != nil {
		senderID = PyStr(value)
	}
	channel := ""
	if value, ok := event["channel"]; ok && value != nil {
		channel = PyStr(value)
	}
	ts := event["thread_ts"]
	if !PyTruthy(ts) {
		ts = event["ts"]
	}
	threadRef := map[string]any{"ts": ts}
	if l.Sink != nil {
		l.Sink.OnInbound(ctx, "slack", senderID, text, threadRef, channel)
	}
}

// ----- Hive Inbox (SSE) ---------------------------------------------------

// HiveStreamConfig is the resolved Hive Inbox connection state.
type HiveStreamConfig struct {
	BaseURL     string
	JWT         string
	RuntimeID   string
	RuntimeKind string
}

// HiveInboxListener subscribes to hive-backend's runtime reply stream. Each
// reply is wrapped with the escalation's (ref: …) token so it routes through
// OnInbound exactly like a quoted Telegram/Slack reply.
type HiveInboxListener struct {
	Sink   InboundSink
	Client HTTPDoer
	Clock  Clock
	Config func() HiveStreamConfig
}

// NewHiveInboxListener returns a Hive Inbox listener.
func NewHiveInboxListener(sink InboundSink, client HTTPDoer, clock Clock, config func() HiveStreamConfig) *HiveInboxListener {
	return &HiveInboxListener{Sink: sink, Client: client, Clock: clock, Config: config}
}

func (l *HiveInboxListener) client() HTTPDoer {
	if l.Client != nil {
		return l.Client
	}
	return defaultHTTPDoer()
}

// Run keeps one SSE subscription open, reconnecting with backoff.
func (l *HiveInboxListener) Run(ctx context.Context) error {
	if l.Config == nil {
		return nil
	}
	cfg := l.Config()
	if cfg.BaseURL == "" || cfg.JWT == "" {
		return nil
	}
	endpoint := cfg.BaseURL + "/v1/inbox/runtime/stream"
	query := url.Values{}
	query.Set("runtime_id", cfg.RuntimeID)
	query.Set("kind", cfg.RuntimeKind)
	endpoint += "?" + query.Encode()
	log.Printf("[sentinel] hive inbox listener started (runtime_id=%s kind=%s)", cfg.RuntimeID, cfg.RuntimeKind)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+cfg.JWT)
		request.Header.Set("Accept", "text/event-stream")
		response, err := l.client().Do(request)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("[sentinel] hive inbox listener stopped")
				return nil
			}
			log.Printf("[sentinel] hive stream error: %v", err)
			if !sleepOrDone(ctx, l.Clock, defaultTelegramBackoff) {
				return nil
			}
			continue
		}
		if response.StatusCode != http.StatusOK {
			log.Printf("[sentinel] hive stream HTTP %d", response.StatusCode)
			response.Body.Close()
			if !sleepOrDone(ctx, l.Clock, defaultSlackBackoff) {
				return nil
			}
			continue
		}
		err = l.ConsumeSSE(ctx, response.Body)
		response.Body.Close()
		if ctx.Err() != nil {
			log.Printf("[sentinel] hive inbox listener stopped")
			return nil
		}
		if err != nil {
			log.Printf("[sentinel] hive stream error: %v", err)
		}
		if !sleepOrDone(ctx, l.Clock, defaultTelegramBackoff) {
			return nil
		}
	}
}

// ConsumeSSE parses SSE frames on an open stream, dispatching reply events.
func (l *HiveInboxListener) ConsumeSSE(ctx context.Context, body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	event := "message"
	dataLines := []string{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" { // frame boundary — dispatch what we accumulated
			if len(dataLines) > 0 {
				l.HandleEvent(ctx, event, strings.Join(dataLines, "\n"))
			}
			event = "message"
			dataLines = nil
			continue
		}
		if strings.HasPrefix(line, ":") { // comment / keepalive
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// A final frame without a trailing blank line still dispatches, matching
	// the reference parser's end-of-stream flush expectations.
	if len(dataLines) > 0 {
		l.HandleEvent(ctx, event, strings.Join(dataLines, "\n"))
	}
	return nil
}

// HandleEvent dispatches one parsed SSE frame.
func (l *HiveInboxListener) HandleEvent(ctx context.Context, event, raw string) {
	if event != "reply" {
		return
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return
	}
	token, _ := decoded["correlation_token"].(string)
	reply, _ := decoded["text"].(string)
	if token == "" || reply == "" {
		return
	}
	// Wrap with the (ref: token) footer so OnInbound resolves by token, then
	// strips it before injecting the reply into the queen.
	envelope := FormatRef(token) + " " + reply
	colonyID := ""
	if value, ok := decoded["colony_id"]; ok && value != nil {
		colonyID = PyStr(value)
	}
	if l.Sink != nil {
		l.Sink.OnInbound(ctx, "hive", "hive", envelope, nil, colonyID)
	}
}
