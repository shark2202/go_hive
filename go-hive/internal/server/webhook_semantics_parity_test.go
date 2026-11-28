package server

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
//
// Measured against real aiohttp 3.13.5 (raw-socket probe, evidence in
// docs/e2e-assets/webhook-semantics/):
//   - headers=dict(request.headers): duplicate header names keep the FIRST
//     value (multi-dict __getitem__); Host is present as a regular key.
//   - query_params=dict(request.query): duplicate names keep the FIRST value;
//     empty values stay "".
//   - body: aiohttp's default client_max_size (1 MiB) raises
//     HTTPRequestEntityTooLarge once the accumulated size reaches the limit,
//     which the handler's `except Exception` converts to
//     400 {"error": "Failed to read request body"} — there is no 413.

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// doRawWebhookRequest sends the exact request bytes (duplicate header lines
// preserved, Host set like the measurement probe) and returns the response.
func doRawWebhookRequest(t *testing.T, addr, target string, headerLines []string, body []byte) (int, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer conn.Close()

	var raw strings.Builder
	raw.WriteString("POST " + target + " HTTP/1.1\r\n")
	raw.WriteString("Host: " + addr + "\r\n")
	for _, line := range headerLines {
		raw.WriteString(line + "\r\n")
	}
	fmt.Fprintf(&raw, "Content-Length: %d\r\n", len(body))
	raw.WriteString("Connection: close\r\n\r\n")

	if _, err := io.WriteString(conn, raw.String()); err != nil {
		t.Fatalf("write head: %v", err)
	}
	if _, err := conn.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return resp.StatusCode, string(respBody)
}

func startWebhookSemanticsServer(t *testing.T) (*eventCapture, string) {
	t.Helper()
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventWebhookReceived)
	server := newWebhookServer(bus, "127.0.0.1", 0).(*webhookServer)
	server.AddRoute("src", "/hook", []string{"POST"})
	if err := server.Start(); err != nil {
		t.Fatalf("start webhook server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	if server.Port() == 0 {
		t.Fatalf("bound port is 0")
	}
	return capture, fmt.Sprintf("127.0.0.1:%d", server.Port())
}

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
// (headers=dict(request.headers)).
func TestWebhookDuplicateHeadersKeepFirstValueParity(t *testing.T) {
	capture, addr := startWebhookSemanticsServer(t)

	cases := []struct {
		name  string
		lines []string
		key   string
		want  string
	}{
		{
			name:  "two same-name headers keep first",
			lines: []string{"X-Test: alpha", "X-Test: beta"},
			key:   "X-Test",
			want:  "alpha",
		},
		{
			name:  "three same-name headers keep first",
			lines: []string{"X-Triple: one", "X-Triple: two", "X-Triple: three"},
			key:   "X-Triple",
			want:  "one",
		},
		{
			// aiohttp also keeps both wire spellings as separate dict keys
			// (multi-dict keys() yields each inserted spelling); net/http
			// canonicalizes names, so Go exposes the canonical key only. The
			// value rule (first) is what this asserts.
			name:  "case-variant duplicates keep first value under canonical name",
			lines: []string{"X-Mix-Case: FirstCase", "x-mix-case: secondcase"},
			key:   "X-Mix-Case",
			want:  "FirstCase",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := doRawWebhookRequest(t, addr, "/hook",
				append([]string{"Content-Type: application/json"}, tc.lines...), []byte("{}"))
			if status != http.StatusAccepted {
				t.Fatalf("status = %d (body %q), want 202", status, body)
			}
			events := capture.eventsOf(eventbus.EventWebhookReceived)
			if len(events) == 0 {
				t.Fatalf("no webhook event captured")
			}
			headers, ok := events[len(events)-1].Data["headers"].(map[string]string)
			if !ok {
				t.Fatalf("event headers = %#v (%T), want map[string]string", events[len(events)-1].Data["headers"], events[len(events)-1].Data["headers"])
			}
			if headers[tc.key] != tc.want {
				t.Fatalf("headers[%q] = %q, want %q (first value; all headers: %#v)", tc.key, headers[tc.key], tc.want, headers)
			}
			if headers["Host"] != addr {
				t.Fatalf("headers[Host] = %q, want %q", headers["Host"], addr)
			}
			if headers["Content-Length"] != "2" {
				t.Fatalf("headers[Content-Length] = %q, want \"2\"", headers["Content-Length"])
			}
		})
	}
}

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
// (query_params=dict(request.query)).
func TestWebhookDuplicateQueryParamsKeepFirstValueParity(t *testing.T) {
	capture, addr := startWebhookSemanticsServer(t)

	cases := []struct {
		name   string
		target string
		want   map[string]string
	}{
		{
			name:   "two duplicate params keep first",
			target: "/hook?a=1&a=2",
			want:   map[string]string{"a": "1"},
		},
		{
			name:   "mixed duplicates keep first, empty value stays empty",
			target: "/hook?a=1&a=2&b=3&empty=&a=4",
			want:   map[string]string{"a": "1", "b": "3", "empty": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := doRawWebhookRequest(t, addr, tc.target,
				[]string{"Content-Type: application/json"}, []byte("{}"))
			if status != http.StatusAccepted {
				t.Fatalf("status = %d (body %q), want 202", status, body)
			}
			events := capture.eventsOf(eventbus.EventWebhookReceived)
			if len(events) == 0 {
				t.Fatalf("no webhook event captured")
			}
			got, ok := events[len(events)-1].Data["query_params"].(map[string]string)
			if !ok {
				t.Fatalf("event query_params = %#v (%T), want map[string]string", events[len(events)-1].Data["query_params"], events[len(events)-1].Data["query_params"])
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("query_params = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
// (`body = await request.read()`; aiohttp web.Application default
// client_max_size = 1 MiB; the raise is caught and answered 400).
func TestWebhookBodyLimitAiohttpDefaultParity(t *testing.T) {
	capture, addr := startWebhookSemanticsServer(t)
	url := "http://" + addr + "/hook"
	client := &http.Client{}
	t.Cleanup(client.CloseIdleConnections)

	exactJSONBody := func(total int) []byte {
		t.Helper()
		const prefix, suffix = `{"pad":"`, `"}`
		if total < len(prefix)+len(suffix) {
			t.Fatalf("body size %d too small", total)
		}
		return []byte(prefix + strings.Repeat("x", total-len(prefix)-len(suffix)) + suffix)
	}

	cases := []struct {
		name       string
		size       int
		wantStatus int
		wantEvent  bool
	}{
		{"1MiB minus 1 is accepted", 1<<20 - 1, http.StatusAccepted, true},
		{"exactly 1MiB is rejected", 1 << 20, http.StatusBadRequest, false},
		{"1MiB plus 1 is rejected", 1<<20 + 1, http.StatusBadRequest, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := exactJSONBody(tc.size)
			if len(body) != tc.size {
				t.Fatalf("built body size = %d, want %d", len(body), tc.size)
			}
			before := len(capture.eventsOf(eventbus.EventWebhookReceived))
			req, err := http.NewRequest("POST", url, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("post %d bytes: %v", tc.size, err)
			}
			defer resp.Body.Close()
			respBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d (body %q), want %d", resp.StatusCode, respBody, tc.wantStatus)
			}
			after := len(capture.eventsOf(eventbus.EventWebhookReceived))
			if got := after > before; got != tc.wantEvent {
				t.Fatalf("event emitted = %v, want %v", got, tc.wantEvent)
			}
			var wantBody string
			if tc.wantStatus == http.StatusBadRequest {
				// Same message as the generic read-failure branch, exactly as
				// upstream (no 413 on this path).
				wantBody = webhookJSONReadFailed
			} else {
				wantBody = webhookJSONAccepted
			}
			if string(respBody) != wantBody {
				t.Fatalf("body bytes = %q (%d bytes), want %q (%d bytes)", respBody, len(respBody), wantBody, len(wantBody))
			}
		})
	}
}

// Parity: framework/host/webhook_server.py::WebhookServer.start registers one
// aiohttp route per configured method; aiohttp/web_exceptions.py::
// HTTPMethodNotAllowed renders Allow as ",".join(sorted(allowed_methods)) and
// the default "405: Method Not Allowed" text. Measured against real aiohttp
// 3.13.5: methods ["PUT","POST","GET"] -> PATCH 405 with
// `Allow: GET,POST,PUT`; methods: [] -> no aiohttp route -> 404.
func TestWebhookMethodMismatchAllowAndEmptyMethodsParity(t *testing.T) {
	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventWebhookReceived)
	server := newWebhookServer(bus, "127.0.0.1", 0).(*webhookServer)
	server.AddRoute("src", "/hook", []string{"POST"})
	// Lowercase on purpose: aiohttp upper-cases methods at registration.
	server.AddRoute("src", "/multi", []string{"put", "post", "GET"})
	server.AddRoute("src", "/empty", []string{})
	if err := server.Start(); err != nil {
		t.Fatalf("start webhook server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	client := &http.Client{}
	t.Cleanup(client.CloseIdleConnections)
	base := fmt.Sprintf("http://127.0.0.1:%d", server.Port())

	request := func(method, path string) (int, string, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read response body: %v", err)
		}
		return resp.StatusCode, string(body), resp.Header.Get("Allow")
	}

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		status, body, allow := request(method, "/hook")
		if status != http.StatusMethodNotAllowed {
			t.Fatalf("%s /hook status = %d, want 405", method, status)
		}
		if body != "405: Method Not Allowed" {
			t.Fatalf("%s /hook body = %q, want %q", method, body, "405: Method Not Allowed")
		}
		if allow != "POST" {
			t.Fatalf("%s /hook Allow = %q, want %q", method, allow, "POST")
		}
	}

	// Sorted, comma-separated, uppercase — regardless of configured
	// order/case. This is the exact aiohttp 3.13.5 shape.
	status, body, allow := request(http.MethodPatch, "/multi")
	if status != http.StatusMethodNotAllowed || body != "405: Method Not Allowed" {
		t.Fatalf("PATCH /multi = %d %q, want 405 %q", status, body, "405: Method Not Allowed")
	}
	if allow != "GET,POST,PUT" {
		t.Fatalf("PATCH /multi Allow = %q, want %q", allow, "GET,POST,PUT")
	}

	// A lowercase configured method still serves its uppercase wire method.
	if status, body, _ := request(http.MethodGet, "/multi"); status != http.StatusAccepted || body != webhookJSONAccepted {
		t.Fatalf("GET /multi = %d %q, want 202 %q", status, body, webhookJSONAccepted)
	}

	// methods: [] has no aiohttp route, so every method is a plain 404 —
	// never 405, and never an Allow header.
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		status, body, allow := request(method, "/empty")
		if status != http.StatusNotFound {
			t.Fatalf("%s /empty status = %d, want 404", method, status)
		}
		if body != webhookJSONNotFound {
			t.Fatalf("%s /empty body = %q, want %q", method, body, webhookJSONNotFound)
		}
		if allow != "" {
			t.Fatalf("%s /empty Allow = %q, want none", method, allow)
		}
	}

	// Normal POST is untouched and still emits the webhook event.
	status, body, allow = request(http.MethodPost, "/hook")
	if status != http.StatusAccepted || body != webhookJSONAccepted {
		t.Fatalf("POST /hook = %d %q, want 202 %q", status, body, webhookJSONAccepted)
	}
	if allow != "" {
		t.Fatalf("POST /hook Allow = %q, want none", allow)
	}
	if event := capture.lastOf(eventbus.EventWebhookReceived); event == nil || event.StreamID != "src" {
		t.Fatalf("webhook event = %#v", event)
	}
}

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
// (web.json_response). aiohttp emits json.dumps' default separators, no
// trailing newline, and "application/json; charset=utf-8" for all four fixed
// responses; the 202 body is 22 bytes (measured with a raw-socket probe,
// docs/e2e-assets/webhook-semantics/). Body bytes are compared exactly, so an
// accidental re-encoding through the shared writeJSON (compact JSON + "\n")
// fails this test.
func TestWebhookResponseBytesAiohttpParity(t *testing.T) {
	bus := eventbus.NewEventBus()
	server := newWebhookServer(bus, "127.0.0.1", 0).(*webhookServer)
	server.AddRoute("src", "/hook", []string{"POST"})
	server.AddRouteWithSecret("src", "/secure", "topsecret", []string{"POST"})
	if err := server.Start(); err != nil {
		t.Fatalf("start webhook server: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	client := &http.Client{}
	t.Cleanup(client.CloseIdleConnections)
	url := fmt.Sprintf("http://127.0.0.1:%d", server.Port())

	oversized := []byte(`{"pad":"` + strings.Repeat("x", (1<<20)-len(`{"pad":"`)-len(`"}`)) + `"}`)
	if len(oversized) != 1<<20 {
		t.Fatalf("built body size = %d, want %d", len(oversized), 1<<20)
	}

	cases := []struct {
		name       string
		method     string
		path       string
		body       []byte
		wantStatus int
		wantBody   string
	}{
		{"202 accepted", http.MethodPost, "/hook", []byte("{}"), http.StatusAccepted, `{"status": "accepted"}`},
		{"400 read failure", http.MethodPost, "/hook", oversized, http.StatusBadRequest, `{"error": "Failed to read request body"}`},
		{"401 invalid signature", http.MethodPost, "/secure", []byte("{}"), http.StatusUnauthorized, `{"error": "Invalid signature"}`},
		{"404 unknown route", http.MethodGet, "/missing", nil, http.StatusNotFound, `{"error": "Not found"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, url+tc.path, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			defer resp.Body.Close()
			gotBody, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read response body: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d (body %q), want %d", resp.StatusCode, gotBody, tc.wantStatus)
			}
			if string(gotBody) != tc.wantBody {
				t.Fatalf("body bytes = %q (%d bytes), want %q (%d bytes)", gotBody, len(gotBody), tc.wantBody, len(tc.wantBody))
			}
			if contentType := resp.Header.Get("Content-Type"); contentType != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q, want %q", contentType, "application/json; charset=utf-8")
			}
			if contentLength := resp.Header.Get("Content-Length"); contentLength != fmt.Sprintf("%d", len(tc.wantBody)) {
				t.Fatalf("Content-Length = %q, want %d", contentLength, len(tc.wantBody))
			}
		})
	}
}
