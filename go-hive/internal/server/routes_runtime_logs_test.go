package server

// Parity: framework/server/tests/test_runtime_logs.py::test_the_drawer_gets_the_lines_after_its_cursor

import (
	"net/http"
	"strings"
	"testing"
)

func runtimeLogTestMux(t *testing.T, buffer *RuntimeLogBuffer) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRuntimeLogRoutes(mux, RuntimeLogRouteDeps{Buffer: buffer})
	return mux
}

// Parity: framework/server/tests/test_runtime_logs.py::test_the_drawer_gets_the_lines_after_its_cursor
func TestRuntimeLogRoutesCursorAndBoot(t *testing.T) {
	buffer := NewRuntimeLogBuffer("boot-a")
	buffer.Append("stdout", "one")
	buffer.Append("stderr", "two")
	mux := runtimeLogTestMux(t, buffer)

	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=0&boot=boot-a", "")
	if recorder.Code != http.StatusOK || body["boot"] != "boot-a" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	entries := credentialRouteJSONList(t, body["entries"])
	if len(entries) != 2 {
		t.Fatalf("entries = %#v", body["entries"])
	}
	first := credentialRouteJSONMap(t, entries[0])
	second := credentialRouteJSONMap(t, entries[1])
	if soulTestInt(t, first["seq"]) != 1 || first["stream"] != "stdout" || first["text"] != "one" {
		t.Fatalf("first = %#v", first)
	}
	if soulTestInt(t, second["seq"]) != 2 || second["stream"] != "stderr" || second["text"] != "two" {
		t.Fatalf("second = %#v", second)
	}

	recorder, body = doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=2&boot=boot-a", "")
	entries = credentialRouteJSONList(t, body["entries"])
	if recorder.Code != http.StatusOK || len(entries) != 0 {
		t.Fatalf("incremental status = %d, entries = %#v", recorder.Code, entries)
	}

	// A drawer from an earlier process gets the whole buffer: its cursor
	// belongs to sequence numbers this process never issued.
	recorder, body = doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=1&boot=earlier", "")
	if recorder.Code != http.StatusOK || len(credentialRouteJSONList(t, body["entries"])) != 2 {
		t.Fatalf("stale boot status = %d, body = %#v", recorder.Code, body)
	}

	// Invalid or negative cursors fall back to zero rather than failing.
	for _, after := range []string{"abc", "-5", "1.5"} {
		_, body = doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after="+after+"&boot=boot-a", "")
		if len(credentialRouteJSONList(t, body["entries"])) != 2 {
			t.Fatalf("after=%s entries = %#v", after, body["entries"])
		}
	}
	// A cursor past int64 is still a cursor: nothing newer, empty array.
	_, body = doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=999999999999999999999&boot=boot-a", "")
	if entries := credentialRouteJSONList(t, body["entries"]); len(entries) != 0 {
		t.Fatalf("huge cursor entries = %#v", entries)
	}
}

// Parity: framework/server/routes_logs.py::handle_runtime_logs
func TestRuntimeLogRoutesEmptyBufferIsArray(t *testing.T) {
	mux := runtimeLogTestMux(t, NewRuntimeLogBuffer("boot-empty"))
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=0&boot=boot-empty", "")
	if recorder.Code != http.StatusOK || body["boot"] != "boot-empty" {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	if _, ok := body["entries"].([]any); !ok {
		t.Fatalf("entries = %#v, want an array", body["entries"])
	}
	if len(credentialRouteJSONList(t, body["entries"])) != 0 {
		t.Fatalf("entries = %#v, want empty", body["entries"])
	}
}

// Parity: framework/server/tests/test_runtime_logs.py::test_a_drawer_from_an_earlier_server_gets_the_whole_buffer
func TestRuntimeLogRoutesPathQueriesStayInMemory(t *testing.T) {
	buffer := NewRuntimeLogBuffer("boot-safe")
	buffer.Append("stdout", "only line")
	mux := runtimeLogTestMux(t, buffer)

	// No query value is a path: a traversal-shaped cursor and boot merely
	// fail to parse or mismatch, and the response still contains only the
	// in-memory ring.
	recorder, body := doSessionJSON(t, mux, http.MethodGet,
		"/api/runtime-logs?after=../../etc/passwd&boot=../../etc/passwd&path=../../etc/passwd", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if body["boot"] != "boot-safe" || strings.Contains(recorder.Body.String(), "root:") {
		t.Fatalf("traversal leaked: %s", recorder.Body.String())
	}
	entries := credentialRouteJSONList(t, body["entries"])
	if len(entries) != 1 || credentialRouteJSONMap(t, entries[0])["text"] != "only line" {
		t.Fatalf("entries = %#v", body["entries"])
	}
}

// Parity: framework/server/routes_logs.py::handle_runtime_logs
func TestRuntimeLogRoutesBoundsTextAndBuffer(t *testing.T) {
	buffer := NewRuntimeLogBuffer("boot-bounded")
	buffer.Append("stdout", strings.Repeat("x", maxRuntimeLogTextRunes+500))
	buffer.Append("stderr", "\x1b[31mred\x1b[0m")

	if runes := []rune(buffer.Since(0)[0].Text); len(runes) != maxRuntimeLogTextRunes {
		t.Fatalf("text runes = %d, want %d", len(runes), maxRuntimeLogTextRunes)
	}
	if text := buffer.Since(0)[1].Text; text != "red" {
		t.Fatalf("ansi text = %q, want red", text)
	}

	for i := 0; i < RuntimeLogCapacity+120; i++ {
		buffer.Append("stdout", "line")
	}
	all := buffer.Since(0)
	if len(all) != RuntimeLogCapacity {
		t.Fatalf("buffer = %d entries, want %d", len(all), RuntimeLogCapacity)
	}
	if all[0].Seq != int64(120+3) {
		t.Fatalf("oldest seq = %d, want %d", all[0].Seq, 123)
	}

	// The response budget trims the oldest entries, never the newest.
	big := NewRuntimeLogBuffer("boot-big")
	for i := 0; i < 300; i++ {
		big.Append("stdout", strings.Repeat("y", maxRuntimeLogTextRunes))
	}
	mux := runtimeLogTestMux(t, big)
	recorder, body := doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=0&boot=boot-big", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	entries := credentialRouteJSONList(t, body["entries"])
	total := 0
	for _, entry := range entries {
		total += len(credentialRouteJSONMap(t, entry)["text"].(string)) + 64
	}
	if total > maxRuntimeLogPayloadBytes {
		t.Fatalf("payload = %d bytes, cap %d", total, maxRuntimeLogPayloadBytes)
	}
	if len(entries) == 0 || credentialRouteJSONMap(t, entries[len(entries)-1])["seq"] != float64(300) {
		t.Fatalf("newest entry was dropped: %#v", entries[len(entries)-1])
	}
}

// Parity: framework/server/tests/test_runtime_logs.py::test_polling_the_log_does_not_write_to_it
func TestRuntimeLogRoutesPollingDoesNotAppend(t *testing.T) {
	buffer := NewRuntimeLogBuffer("boot-poll")
	buffer.Append("stdout", "server line")
	mux := runtimeLogTestMux(t, buffer)

	before := len(buffer.Since(0))
	doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=0&boot=boot-poll", "")
	doSessionJSON(t, mux, http.MethodGet, "/api/runtime-logs?after=1&boot=boot-poll", "")
	if after := len(buffer.Since(0)); after != before {
		t.Fatalf("entries = %d, want %d; polling wrote to the buffer", after, before)
	}
}
