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
	"encoding/json"
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
			var decoded map[string]any
			if err := json.Unmarshal(respBody, &decoded); err != nil {
				t.Fatalf("response body %q is not JSON: %v", respBody, err)
			}
			if tc.wantStatus == http.StatusBadRequest {
				// Same message as the generic read-failure branch, exactly as
				// upstream (no 413 on this path).
				if decoded["error"] != "Failed to read request body" {
					t.Fatalf("error = %#v, want %q", decoded["error"], "Failed to read request body")
				}
			} else if decoded["status"] != "accepted" {
				t.Fatalf("status field = %#v, want %q", decoded["status"], "accepted")
			}
		})
	}
}
