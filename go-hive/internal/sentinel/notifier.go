package sentinel

// Outbound messaging for Sentinel — Telegram, Slack and the first-party Hive
// Inbox.
//
// A deliberately thin sender: the MCP tools live above core in the dependency
// graph, so core cannot import them. Tokens are never logged and all I/O is
// context-bound so it never blocks the caller indefinitely.
//
// The transport is an injectable HTTPDoer so tests never touch the network.
//
// Parity: core/framework/sentinel/notifier.py

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	ChannelTelegram = "telegram"
	ChannelSlack    = "slack"
	// ChannelHive is the first-party, account-bound channel served by
	// hive-backend. It needs no per-channel token: it rides the account's
	// cloud JWT (HIVE_CLOUD_JWT / HIVE_CLOUD_BASE).
	ChannelHive = "hive"

	defaultTelegramAPI = "https://api.telegram.org"
	defaultSlackAPI    = "https://slack.com/api"
)

// HTTPDoer is the subset of *http.Client the notifier and listeners use.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

func defaultHTTPDoer() HTTPDoer {
	return &http.Client{Timeout: 30 * time.Second}
}

// NotifierResult is the outcome of one outbound send.
type NotifierResult struct {
	OK        bool
	MessageID string
	Error     string
}

// Notifier delivers one escalation message.
type Notifier interface {
	Send(ctx context.Context, channel string, target map[string]any, text string, threadRef map[string]any, meta map[string]any) NotifierResult
}

// ChannelTokenProvider reports which inbound channels are configured.
type ChannelTokenProvider interface {
	TelegramToken() string
	SlackAppToken() string
	HiveConnected() bool
}

// HTTPNotifier is the default Notifier over the Telegram/Slack/Hive REST APIs.
type HTTPNotifier struct {
	Client HTTPDoer
	Getenv func(string) string
	// ResolveToken optionally reads a credential store before the env var.
	// Nil means env-only; hosts that own a credential store inject it here.
	ResolveToken    func(credentialID, envVar string) (string, bool)
	TelegramAPIBase string
	SlackAPIBase    string
}

// NewHTTPNotifier returns a notifier over client (nil → http.DefaultClient)
// reading environment variables via os.Getenv.
func NewHTTPNotifier(client HTTPDoer) *HTTPNotifier {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &HTTPNotifier{
		Client:          client,
		Getenv:          os.Getenv,
		TelegramAPIBase: defaultTelegramAPI,
		SlackAPIBase:    defaultSlackAPI,
	}
}

func (n *HTTPNotifier) client() HTTPDoer {
	if n.Client != nil {
		return n.Client
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (n *HTTPNotifier) getenv(name string) string {
	if n.Getenv != nil {
		return n.Getenv(name)
	}
	return os.Getenv(name)
}

func (n *HTTPNotifier) credential(credentialID, envVar string) string {
	if n.ResolveToken != nil {
		if value, ok := n.ResolveToken(credentialID, envVar); ok && value != "" {
			return value
		}
	}
	return n.getenv(envVar)
}

// TelegramToken returns the bot token from the credential resolver or env.
func (n *HTTPNotifier) TelegramToken() string { return n.credential("telegram", "TELEGRAM_BOT_TOKEN") }

// SlackBotToken returns the bot token from the credential resolver or env.
func (n *HTTPNotifier) SlackBotToken() string { return n.credential("slack", "SLACK_BOT_TOKEN") }

// SlackAppToken returns the Socket-Mode app-level token (xapp-…).
func (n *HTTPNotifier) SlackAppToken() string { return n.credential("slack_app", "SLACK_APP_TOKEN") }

// HiveBackendBase returns the hive-backend base URL, or "".
func (n *HTTPNotifier) HiveBackendBase() string {
	return strings.TrimSpace(n.getenv("HIVE_CLOUD_BASE"))
}

// HiveJWT returns the account cloud JWT, or "".
func (n *HTTPNotifier) HiveJWT() string { return strings.TrimSpace(n.getenv("HIVE_CLOUD_JWT")) }

// HiveConnected reports whether the runtime is signed in, i.e. the Hive Inbox
// channel is usable without any per-channel token.
func (n *HTTPNotifier) HiveConnected() bool {
	return n.HiveBackendBase() != "" && n.HiveJWT() != ""
}

func (n *HTTPNotifier) telegramBase() string {
	if n.TelegramAPIBase != "" {
		return n.TelegramAPIBase
	}
	return defaultTelegramAPI
}

func (n *HTTPNotifier) slackBase() string {
	if n.SlackAPIBase != "" {
		return n.SlackAPIBase
	}
	return defaultSlackAPI
}

// Send delivers text to channel, optionally threaded under threadRef. target
// carries the destination; meta carries the structured Hive fields (ignored by
// telegram/slack).
func (n *HTTPNotifier) Send(ctx context.Context, channel string, target map[string]any, text string, threadRef map[string]any, meta map[string]any) NotifierResult {
	switch channel {
	case ChannelTelegram:
		return n.sendTelegram(ctx, target, text, threadRef)
	case ChannelSlack:
		return n.sendSlack(ctx, target, text, threadRef)
	case ChannelHive:
		return n.sendHive(ctx, text, meta)
	default:
		return NotifierResult{Error: fmt.Sprintf("unknown channel %q", channel)}
	}
}

type httpJSONResponse struct {
	Status int
	Data   map[string]any
}

func (n *HTTPNotifier) postJSON(ctx context.Context, url string, payload any, headers map[string]string) (httpJSONResponse, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return httpJSONResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return httpJSONResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := n.client().Do(req)
	if err != nil {
		return httpJSONResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpJSONResponse{Status: resp.StatusCode}, err
	}
	data := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &data); err != nil {
			return httpJSONResponse{Status: resp.StatusCode}, err
		}
	}
	return httpJSONResponse{Status: resp.StatusCode, Data: data}, nil
}

// redactSecret keeps a provider token out of logs and user-visible errors.
// net/http wraps failures in *url.Error, whose Error() includes the request
// URL — and Telegram puts the bot token in the URL path.
func redactSecret(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}

func (n *HTTPNotifier) sendTelegram(ctx context.Context, target map[string]any, text string, threadRef map[string]any) NotifierResult {
	token := n.TelegramToken()
	if token == "" {
		return NotifierResult{Error: "telegram token not configured"}
	}
	chatID, ok := target["chat_id"]
	if !ok || !PyTruthy(chatID) {
		return NotifierResult{Error: "telegram target missing chat_id"}
	}
	payload := map[string]any{"chat_id": chatID, "text": text}
	if threadRef != nil {
		if messageID, ok := threadRef["message_id"]; ok && PyTruthy(messageID) {
			payload["reply_to_message_id"] = messageID
		}
	}
	response, err := n.postJSON(ctx, fmt.Sprintf("%s/bot%s/sendMessage", n.telegramBase(), token), payload, nil)
	if err != nil {
		message := redactSecret(err.Error(), token)
		log.Printf("sentinel: telegram send failed: %s", message)
		return NotifierResult{Error: message}
	}
	if ok, _ := response.Data["ok"].(bool); !ok {
		description := "telegram error"
		if value, ok := response.Data["description"]; ok {
			description = PyStr(value)
		}
		return NotifierResult{Error: description}
	}
	messageID := ""
	if result, ok := response.Data["result"].(map[string]any); ok {
		if value, ok := result["message_id"]; ok && value != nil {
			messageID = PyStr(value)
		}
	}
	return NotifierResult{OK: true, MessageID: messageID}
}

func (n *HTTPNotifier) sendSlack(ctx context.Context, target map[string]any, text string, threadRef map[string]any) NotifierResult {
	token := n.SlackBotToken()
	if token == "" {
		return NotifierResult{Error: "slack token not configured"}
	}
	channel, ok := target["channel"]
	if !ok || !PyTruthy(channel) {
		return NotifierResult{Error: "slack target missing channel"}
	}
	payload := map[string]any{"channel": channel, "text": text}
	if threadRef != nil {
		if ts, ok := threadRef["ts"]; ok && PyTruthy(ts) {
			payload["thread_ts"] = ts
		}
	}
	response, err := n.postJSON(ctx, n.slackBase()+"/chat.postMessage", payload, map[string]string{
		"Authorization": "Bearer " + token,
	})
	if err != nil {
		message := redactSecret(err.Error(), token)
		log.Printf("sentinel: slack send failed: %s", message)
		return NotifierResult{Error: message}
	}
	if ok, _ := response.Data["ok"].(bool); !ok {
		message := "slack error"
		if value, ok := response.Data["error"]; ok {
			message = PyStr(value)
		}
		return NotifierResult{Error: message}
	}
	messageID := ""
	if value, ok := response.Data["ts"]; ok && value != nil {
		messageID = PyStr(value)
	}
	return NotifierResult{OK: true, MessageID: messageID}
}

func (n *HTTPNotifier) sendHive(ctx context.Context, body string, meta map[string]any) NotifierResult {
	base := n.HiveBackendBase()
	jwt := n.HiveJWT()
	if base == "" || jwt == "" {
		return NotifierResult{Error: "hive not connected (no cloud JWT/base)"}
	}
	if meta == nil {
		meta = map[string]any{}
	}
	kind := "blocker"
	if value, ok := meta["kind"]; ok && value != nil && PyStr(value) != "" {
		kind = PyStr(value)
	}
	title := ""
	if value, ok := meta["title"]; ok && value != nil {
		title = PyStr(value)
	}
	payload := map[string]any{
		"runtime_id":        meta["runtime_id"],
		"session_id":        meta["session_id"],
		"colony_id":         meta["colony_id"],
		"kind":              kind,
		"title":             title,
		"body":              body,
		"correlation_token": meta["correlation_token"],
		"deep_link":         meta["deep_link"],
	}
	response, err := n.postJSON(ctx, base+"/v1/inbox/messages", payload, map[string]string{
		"Authorization": "Bearer " + jwt,
	})
	if err != nil {
		message := redactSecret(err.Error(), jwt)
		log.Printf("sentinel: hive send failed: %s", message)
		return NotifierResult{Error: message}
	}
	if response.Status >= 300 {
		return NotifierResult{Error: fmt.Sprintf("hive ingest HTTP %d", response.Status)}
	}
	messageID := ""
	if value, ok := response.Data["id"]; ok && value != nil {
		messageID = PyStr(value)
	}
	return NotifierResult{OK: true, MessageID: messageID}
}

// ResolveHiveSession closes any open Hive Inbox message for sessionID — used
// when the colony was answered out of band so the Inbox does not keep showing
// a stale escalation. Best-effort; no-ops when signed out.
func (n *HTTPNotifier) ResolveHiveSession(ctx context.Context, sessionID, resolvedBy string) bool {
	base := n.HiveBackendBase()
	jwt := n.HiveJWT()
	if base == "" || jwt == "" {
		return false
	}
	response, err := n.postJSON(ctx, base+"/v1/inbox/resolve", map[string]any{
		"session_id":  sessionID,
		"resolved_by": resolvedBy,
	}, map[string]string{"Authorization": "Bearer " + jwt})
	if err != nil {
		log.Printf("sentinel: hive resolve failed: %v", err)
		return false
	}
	return response.Status < 300
}
