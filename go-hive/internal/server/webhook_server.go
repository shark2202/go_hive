package server

// Parity: framework/host/webhook_server.py::WebhookServer
// Parity: framework/host/webhook_server.py::WebhookRoute
// Parity: framework/host/webhook_server.py::WebhookServerConfig

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// webhookBodyLimitBytes mirrors aiohttp's default web.Application
// client_max_size (1 MiB), which upstream never overrides. Its request.read()
// raises HTTPRequestEntityTooLarge as soon as the accumulated body reaches the
// limit (>=), and _handle_request's `except Exception` turns that into
// 400 {"error": "Failed to read request body"} — there is no 413 on this path.
// Measured against real aiohttp 3.13.5: 1 MiB-1 -> 202, 1 MiB -> 400,
// 1 MiB+1 -> 400. Evidence: docs/e2e-assets/webhook-semantics/.
const webhookBodyLimitBytes int64 = 1 << 20

type webhookRoute struct {
	sourceID string
	path     string
	methods  []string
	secret   string
}

type webhookServer struct {
	bus  *eventbus.EventBus
	host string
	port int

	mu         sync.Mutex
	routes     map[string]*webhookRoute
	server     *http.Server
	actualPort int
	running    bool
}

func newWebhookServer(bus *eventbus.EventBus, host string, port int) TriggerWebhookServer {
	return &webhookServer{
		bus:    bus,
		host:   host,
		port:   port,
		routes: map[string]*webhookRoute{},
	}
}

func (s *webhookServer) AddRoute(sourceID, path string, methods []string) {
	s.AddRouteWithSecret(sourceID, path, "", methods)
}

func (s *webhookServer) AddRouteWithSecret(sourceID, path, secret string, methods []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = &webhookRoute{
		sourceID: sourceID,
		path:     path,
		methods:  append([]string(nil), methods...),
		secret:   secret,
	}
}

func (s *webhookServer) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return nil
	}
	if len(s.routes) == 0 {
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	listener, err := net.Listen("tcp", net.JoinHostPort(s.host, strconv.Itoa(s.port)))
	if err != nil {
		return err
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); ok {
		s.actualPort = tcp.Port
	} else {
		s.actualPort = s.port
	}
	s.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	s.running = true
	go func() {
		_ = s.server.Serve(listener)
	}()
	return nil
}

func (s *webhookServer) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *webhookServer) Stop() error {
	s.mu.Lock()
	server := s.server
	s.server = nil
	s.running = false
	s.mu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func (s *webhookServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.actualPort
}

func (s *webhookServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	s.mu.Lock()
	route := s.routes[path]
	s.mu.Unlock()

	if route == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Not found"})
		return
	}
	if !route.allowsMethod(r.Method) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405: Method Not Allowed")
		return
	}

	body, err := readWebhookBody(r, webhookBodyLimitBytes)
	if err != nil {
		// Parity: upstream catches the aiohttp HTTPRequestEntityTooLarge raised
		// by request.read() with a bare `except Exception` and answers 400 with
		// this exact message — oversized bodies never surface as 413.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Failed to read request body"})
		return
	}

	if route.secret != "" && !verifyWebhookSignature(r.Header.Get("X-Hub-Signature-256"), route.secret, body) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Invalid signature"})
		return
	}

	payload := parseWebhookBody(body)

	s.bus.EmitWebhookReceived(
		route.sourceID,
		path,
		r.Method,
		webhookHeaders(r),
		payload,
		webhookQueryParams(r),
	)

	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

func (r *webhookRoute) allowsMethod(method string) bool {
	for _, candidate := range r.methods {
		if candidate == method {
			return true
		}
	}
	return false
}

// parseWebhookBody mirrors upstream's `payload = json.loads(body) if body else {}`:
// every valid JSON type is kept verbatim (including null -> nil); only a decode
// failure falls back to {"raw_body": ...}.
func parseWebhookBody(body []byte) any {
	if len(body) == 0 {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return map[string]any{"raw_body": strings.ToValidUTF8(string(body), "\uFFFD")}
	}
	return decoded
}

// readWebhookBody mirrors upstream `await request.read()`: aiohttp rejects a
// body as soon as its accumulated size reaches client_max_size (>=), so both
// "exactly the limit" and "over the limit" are refused. Reading at most limit
// bytes is sufficient to decide; the caller answers 400 for both.
func readWebhookBody(r *http.Request, limit int64) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) >= limit {
		return nil, errBodyTooLarge
	}
	return data, nil
}

func verifyWebhookSignature(headerValue, secret string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(headerValue, prefix) {
		return false
	}
	expected := headerValue[len(prefix):]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	computed := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(computed))
}

// webhookHeaders mirrors upstream `dict(request.headers)` for duplicate header
// lines. aiohttp's multi-dict __getitem__ returns the FIRST value of a key, so
// dict() keeps the first value (measured: X-Test: alpha + X-Test: beta ->
// "alpha"). net/http canonicalizes header names while aiohttp preserves the
// wire spelling, and Host is not part of r.Header, so Host is re-added the way
// aiohttp keeps it.
func webhookHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string, len(r.Header)+1)
	for name, values := range r.Header {
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}
	if r.Host != "" {
		headers["Host"] = r.Host
	}
	return headers
}

// webhookQueryParams mirrors upstream `dict(request.query)`: the multi-dict
// keeps the FIRST value for a repeated parameter (measured: ?a=1&a=2 -> "1"),
// and an empty value stays "".
func webhookQueryParams(r *http.Request) map[string]string {
	query := r.URL.Query()
	params := make(map[string]string, len(query))
	for name, values := range query {
		if len(values) > 0 {
			params[name] = values[0]
		}
	}
	return params
}
