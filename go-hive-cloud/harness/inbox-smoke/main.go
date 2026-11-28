// Command inbox-smoke drives go-hive-cloud with the REAL go_hive Sentinel
// client — the L2b evidence layer for M2. It is not a reimplementation of the
// client: the escalation goes through sentinel.HTTPNotifier.sendHive (the
// production `POST /v1/inbox/messages` path) and the reply arrives through
// sentinel.HiveInboxListener (the production `GET /v1/inbox/runtime/stream`
// SSE consumer), so a pass proves the closed loop against the code the runtime
// ships.
//
// Checks:
//   - cron: operator JWT + streamToken auth semantics on the observed endpoints
//   - escalate: real notifier Send(ChannelHive, …) -> 2xx + message id
//   - resolve: real notifier ResolveHiveSession -> 2xx
//   - reply: private-deploy POST /internal/inbox/reply injects the answer
//   - sse: real listener parses `event: reply` and calls OnInbound with the
//     (ref: <correlation_token>) envelope
//   - unknown: GET /v1/inbox/stream stays 404 (no guessed desktop SSE)
//
// Module note: the import path is nested under
// github.com/shark2202/go_hive/go-hive/ so Go's internal-package rule allows
// importing the main module's internal/sentinel through the replace directive.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
)

type check struct {
	Name   string         `json:"name"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
	Error  string         `json:"error,omitempty"`
}

type evidence struct {
	Harness    string         `json:"harness"`
	BaseURL    string         `json:"base_url"`
	RuntimeID  string         `json:"runtime_id"`
	SessionID  string         `json:"session_id"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	OK         bool           `json:"ok"`
	Checks     []check        `json:"checks"`
	Env        map[string]any `json:"env,omitempty"`
}

type inboundCall struct {
	Channel  string
	SenderID string
	Text     string
	Source   string
}

// captureSink implements sentinel.InboundSink and records what the real
// listener dispatches.
type captureSink struct {
	mu      sync.Mutex
	inbound []inboundCall
	ch      chan inboundCall
}

func newCaptureSink() *captureSink {
	return &captureSink{ch: make(chan inboundCall, 8)}
}

func (s *captureSink) RecordRecentChat(channel string, info map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = channel
	_ = info
}

func (s *captureSink) OnInbound(ctx context.Context, channel, senderID, text string, threadRef map[string]any, source string) {
	_ = ctx
	_ = threadRef
	call := inboundCall{Channel: channel, SenderID: senderID, Text: text, Source: source}
	s.mu.Lock()
	s.inbound = append(s.inbound, call)
	s.mu.Unlock()
	select {
	case s.ch <- call:
	default:
	}
}

func (s *captureSink) wait(d time.Duration) (inboundCall, bool) {
	select {
	case call := <-s.ch:
		return call, true
	case <-time.After(d):
		return inboundCall{}, false
	}
}

type runner struct {
	base   string
	jwt    string
	http   *http.Client
	checks []check
}

func (r *runner) add(name string, ok bool, detail map[string]any, err error) {
	item := check{Name: name, OK: ok, Detail: detail}
	if err != nil {
		item.Error = err.Error()
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "FAIL %s: %v (detail %v)\n", name, err, detail)
	} else {
		fmt.Fprintf(os.Stderr, "ok   %s %v\n", name, detail)
	}
	r.checks = append(r.checks, item)
}

func (r *runner) doJSON(method, target string, body any, bearer string) (int, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, strings.TrimRight(r.base, "/")+target, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	out := map[string]any{}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 {
		// The router's 404/405 pages are plain text, not JSON.
		if trimmed[0] == '{' || trimmed[0] == '[' {
			if err := json.Unmarshal(trimmed, &out); err != nil {
				return resp.StatusCode, nil, fmt.Errorf("decode %s: %w (body %s)", target, err, raw)
			}
		} else {
			out["raw"] = string(trimmed)
		}
	}
	return resp.StatusCode, out, nil
}

func main() {
	base := strings.TrimSpace(os.Getenv("HIVE_CLOUD_BASE"))
	jwt := strings.TrimSpace(os.Getenv("HIVE_CLOUD_JWT"))
	streamToken := strings.TrimSpace(os.Getenv("HIVE_CLOUD_STREAM_TOKEN"))
	runtimeID := envOr("HIVE_CLOUD_SMOKE_RUNTIME_ID", "rt-inbox-smoke")
	sessionID := envOr("HIVE_CLOUD_SMOKE_SESSION_ID", "s-inbox-smoke")
	correlationToken := envOr("HIVE_CLOUD_SMOKE_CORRELATION", "corr-inbox-smoke")
	replyText := "Approved by the smoke run — ship it."

	started := time.Now().UTC()
	report := evidence{
		Harness:   "inbox-smoke",
		BaseURL:   base,
		RuntimeID: runtimeID,
		SessionID: sessionID,
		StartedAt: started.Format(time.RFC3339Nano),
		Env: map[string]any{
			"go_hive_sentinel": "github.com/shark2202/go_hive/go-hive/internal/sentinel",
			"reply_endpoint":   "/internal/inbox/reply (private-deploy extension)",
		},
	}
	finish := func() {
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
		report.OK = true
		for _, item := range report.Checks {
			if !item.OK {
				report.OK = false
			}
		}
		emit(report)
		if !report.OK {
			fmt.Fprintln(os.Stderr, "harness: FAILED")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "harness: all %d checks passed against %s\n", len(report.Checks), base)
	}
	if base == "" || jwt == "" {
		report.Checks = []check{{Name: "environment", OK: false, Error: "HIVE_CLOUD_BASE and HIVE_CLOUD_JWT are required"}}
		finish()
		return
	}

	r := &runner{base: base, jwt: jwt, http: &http.Client{Timeout: 15 * time.Second}}

	// 0. Operator JWT works; a streamToken is not an operator session; the
	//    desktop SSE endpoint is a documented-but-unshaped 404.
	status, body, err := r.doJSON(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id": runtimeID,
		"session_id": sessionID + "-auth",
		"kind":       "heartbeat",
	}, jwt)
	r.add("operator_jwt_accepted", err == nil && status == http.StatusOK && body["id"] != nil,
		map[string]any{"status": status}, err)

	if streamToken != "" {
		status, body, err = r.doJSON(http.MethodPost, "/v1/inbox/messages", map[string]any{
			"runtime_id": runtimeID,
			"session_id": sessionID + "-auth",
		}, streamToken)
		r.add("stream_token_rejected", err == nil && status == http.StatusUnauthorized &&
			strings.Contains(fmt.Sprint(body["error"]), "Sign in to use the shared global DB"),
			map[string]any{"status": status, "error": body["error"]}, err)
	}

	status, body, err = r.doJSON(http.MethodGet, "/v1/inbox/stream", nil, jwt)
	r.add("desktop_stream_unknown_404", err == nil && status == http.StatusNotFound,
		map[string]any{"status": status, "error": body["error"]}, err)

	// 1. Start the real Sentinel Hive Inbox listener (SSE consumer).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := newCaptureSink()
	listener := sentinel.NewHiveInboxListener(sink, &http.Client{}, nil, func() sentinel.HiveStreamConfig {
		return sentinel.HiveStreamConfig{
			BaseURL:     base,
			JWT:         jwt,
			RuntimeID:   runtimeID,
			RuntimeKind: "smoke",
		}
	})
	listenerDone := make(chan error, 1)
	go func() { listenerDone <- listener.Run(ctx) }()

	// 2. Escalate through the real notifier: this is the same code path the
	//    runtime uses for `POST /v1/inbox/messages`.
	notifier := sentinel.NewHTTPNotifier(&http.Client{Timeout: 10 * time.Second})
	escalation := "Smoke run requests approval to deploy the private cloud."
	result := notifier.Send(ctx, sentinel.ChannelHive, nil, escalation, nil, map[string]any{
		"runtime_id":        runtimeID,
		"session_id":        sessionID,
		"colony_id":         "colony-smoke",
		"kind":              "blocker",
		"title":             "Deploy approval",
		"correlation_token": correlationToken,
		"deep_link":         "hive://colony/colony-smoke/session/" + sessionID,
	})
	r.add("escalate_via_sentinel", result.OK && result.MessageID != "",
		map[string]any{"ok": result.OK, "message_id": result.MessageID}, errFrom(result.Error))

	if !result.OK {
		report.Checks = r.checks
		cancel()
		<-listenerDone
		finish()
		return
	}

	// 3. Reply through the private-deploy extension (the real Inbox UI is
	//    closed-source; its reply endpoint is UNKNOWN).
	status, body, err = r.doJSON(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": correlationToken,
		"text":              replyText,
	}, jwt)
	delivered, _ := body["delivered"].(float64)
	r.add("reply_injected", err == nil && status == http.StatusOK && body["ok"] == true && body["reply_id"] != nil,
		map[string]any{"status": status, "ok": body["ok"], "delivered": delivered, "reply_id": body["reply_id"]}, err)

	// 4. The real SSE listener must dispatch the reply with the ref envelope.
	call, got := sink.wait(10 * time.Second)
	wantText := sentinel.FormatRef(correlationToken) + " " + replyText
	r.add("sse_reply_delivered", got && call.Channel == "hive" && call.SenderID == "hive" &&
		call.Text == wantText && call.Source == "colony-smoke",
		map[string]any{
			"channel": call.Channel, "sender_id": call.SenderID, "source": call.Source,
			"text": call.Text, "want_text": wantText,
		}, nil)

	// 5. Resolve through the real notifier (`POST /v1/inbox/resolve`).
	resolved := notifier.ResolveHiveSession(ctx, sessionID, "smoke")
	r.add("resolve_via_sentinel", resolved, map[string]any{"session_id": sessionID}, nil)

	cancel()
	select {
	case <-listenerDone:
	case <-time.After(3 * time.Second):
		r.add("listener_stopped", false, nil, fmt.Errorf("listener did not stop within 3s"))
	}
	report.Checks = r.checks
	finish()
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func errFrom(message string) error {
	if message == "" {
		return nil
	}
	return fmt.Errorf("%s", message)
}

func emit(report evidence) {
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness: marshal evidence: %v\n", err)
		os.Exit(1)
	}
	raw = append(raw, '\n')
	out := os.Getenv("EVIDENCE_OUT")
	if out == "" {
		os.Stdout.Write(raw)
		return
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "harness: mkdir evidence dir: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "harness: write evidence: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "harness: evidence -> %s\n", out)
}
