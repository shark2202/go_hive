package server

// Parity: framework/host/webhook_server.py::WebhookServer
// Parity: framework/host/webhook_server.py::WebhookRoute
// Parity: framework/host/webhook_server.py::WebhookServerConfig

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"sort"
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

// Fixed webhook responses carry the exact bytes aiohttp's web.json_response
// produces for the same dicts: Python json.dumps default separators (", " /
// ": ") and no trailing newline. They stay literals because these bytes are a
// wire contract; the shared writeJSON emits compact JSON plus a newline and
// must not move for unrelated endpoints.
const (
	webhookJSONAccepted     = `{"status": "accepted"}`
	webhookJSONReadFailed   = `{"error": "Failed to read request body"}`
	webhookJSONBadSignature = `{"error": "Invalid signature"}`
	webhookJSONNotFound     = `{"error": "Not found"}`
)

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
	// Parity: aiohttp upper-cases every method at registration
	// (aiohttp/web_urldispatcher.py::AbstractRoute.__init__), so a lowercase
	// configured method still matches an uppercase HTTP request. An empty
	// list stays empty: WebhookServer.start registers one aiohttp route per
	// method, so such a path has no route at all.
	normalized := make([]string, len(methods))
	for index, method := range methods {
		normalized[index] = strings.ToUpper(method)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[path] = &webhookRoute{
		sourceID: sourceID,
		path:     path,
		methods:  normalized,
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
		writeWebhookJSON(w, http.StatusNotFound, webhookJSONNotFound)
		return
	}
	// Parity: upstream registers one aiohttp route per configured method
	// (framework/host/webhook_server.py::WebhookServer.start), so methods: []
	// leaves the path with no HTTP route and aiohttp answers 404. The route
	// stays in this table so Start()/replacement semantics do not move.
	if len(route.methods) == 0 {
		writeWebhookJSON(w, http.StatusNotFound, webhookJSONNotFound)
		return
	}
	if !route.allowsMethod(r.Method) {
		// Parity: aiohttp's HTTPMethodNotAllowed sets
		// Allow: ",".join(sorted(allowed_methods)) — upper-cased, sorted,
		// comma-separated with no spaces. Measured against real aiohttp
		// 3.13.5: methods ["PUT","POST","GET"] -> `Allow: GET,POST,PUT`.
		// The 405 body bytes stay exactly as before.
		w.Header().Set("Allow", route.allowedMethodsHeader())
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
		writeWebhookJSON(w, http.StatusBadRequest, webhookJSONReadFailed)
		return
	}

	if route.secret != "" && !verifyWebhookSignature(r.Header.Get("X-Hub-Signature-256"), route.secret, body) {
		writeWebhookJSON(w, http.StatusUnauthorized, webhookJSONBadSignature)
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

	writeWebhookJSON(w, http.StatusAccepted, webhookJSONAccepted)
}

// writeWebhookJSON emits one fixed webhook response with aiohttp's exact body
// bytes and content type. It is webhook-local on purpose: the shared writeJSON
// appends a newline and omits the charset, and unrelated endpoints must not
// move.
func writeWebhookJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (r *webhookRoute) allowsMethod(method string) bool {
	for _, candidate := range r.methods {
		if strings.EqualFold(candidate, method) {
			return true
		}
	}
	return false
}

// allowedMethodsHeader renders aiohttp's HTTPMethodNotAllowed Allow value:
// ",".join(sorted(allowed_methods)) (aiohttp/web_exceptions.py), where
// registration upper-cases each method. Duplicates collapse like aiohttp's
// set-backed allowed_methods.
func (r *webhookRoute) allowedMethodsHeader() string {
	seen := make(map[string]struct{}, len(r.methods))
	allowed := make([]string, 0, len(r.methods))
	for _, method := range r.methods {
		upper := strings.ToUpper(method)
		if _, duplicate := seen[upper]; duplicate {
			continue
		}
		seen[upper] = struct{}{}
		allowed = append(allowed, upper)
	}
	sort.Strings(allowed)
	return strings.Join(allowed, ",")
}

// parseWebhookBody mirrors upstream's `payload = json.loads(body) if body else {}`:
// every valid JSON type is kept verbatim (including null -> nil); only a decode
// failure falls back to {"raw_body": ...}. Numbers follow Python's split:
// integer literals stay integers, while fractions and exponents become float64.
func parseWebhookBody(body []byte) any {
	if len(body) == 0 {
		return map[string]any{}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return webhookRawBody(body)
	}
	// json.loads rejects trailing non-whitespace after the first value, while
	// Decoder.Decode would silently stop there; the second Decode must hit EOF.
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return webhookRawBody(body)
	}
	return webhookJSONValue(decoded)
}

func webhookRawBody(body []byte) map[string]any {
	return map[string]any{"raw_body": strings.ToValidUTF8(string(body), "\uFFFD")}
}

// webhookJSONValue re-types the json.Number leaves Decoder produced, matching
// json.loads' parse_int/parse_float split: an integer literal becomes int64
// (or *big.Int past int64, like Python's arbitrary-precision int) and a
// fraction/exponent literal becomes float64.
func webhookJSONValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		text := typed.String()
		if !strings.ContainsAny(text, ".eE") {
			if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
				return integer
			}
			if integer, ok := new(big.Int).SetString(text, 10); ok {
				return integer
			}
		}
		// ParseFloat can only report a range error for decoder-vetted JSON
		// numbers; its ±Inf / 0 result still matches Python's float parsing.
		number, _ := strconv.ParseFloat(text, 64)
		return number
	case map[string]any:
		for key, entry := range typed {
			typed[key] = webhookJSONValue(entry)
		}
		return typed
	case []any:
		for index, entry := range typed {
			typed[index] = webhookJSONValue(entry)
		}
		return typed
	default:
		return value
	}
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
