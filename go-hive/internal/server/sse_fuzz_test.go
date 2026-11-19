package server

// Fuzz target for SSE event framing. Both writers put arbitrary event payload
// strings into one JSON data line; the framing must never split a payload into
// extra frames or lose bytes through escaping.

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzSSEFrameAndPayload asserts that a frame produced from arbitrary payload
// strings contains exactly one data line whose bytes are json.Marshal's output.
func FuzzSSEFrameAndPayload(f *testing.F) {
	f.Add(false, "key", "line one\nline two", "plain")
	f.Add(true, "k", "\r\n\r\n", "🐝")
	f.Add(false, "", "", "")
	f.Add(true, "quote", `" \ backslash`, "\x00")

	f.Fuzz(func(t *testing.T, named bool, key, value, extra string) {
		payload := map[string]any{
			"key":   key,
			"value": value,
			"extra": extra,
		}
		wantJSON, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		recorder := httptest.NewRecorder()
		if named {
			writeNamedSSEEvent(recorder, recorder, "snapshot", payload)
		} else {
			writeSSEData(recorder, recorder, payload)
		}

		body := recorder.Body.String()
		if !strings.HasSuffix(body, "\n\n") {
			t.Fatalf("frame does not end with a blank line: %q", body)
		}
		if strings.Count(body, "\n\n") != 1 {
			t.Fatalf("payload split the frame into %d frames: %q", strings.Count(body, "\n\n"), body)
		}
		lines := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n")
		if named {
			if len(lines) != 2 || lines[0] != "event: snapshot" {
				t.Fatalf("named frame lines = %#v", lines)
			}
		} else if len(lines) != 1 {
			t.Fatalf("data-only frame lines = %#v", lines)
		}
		dataLine := lines[len(lines)-1]
		if !strings.HasPrefix(dataLine, "data: ") {
			t.Fatalf("missing data prefix: %q", dataLine)
		}
		data := strings.TrimPrefix(dataLine, "data: ")
		if !bytes.Equal([]byte(data), wantJSON) {
			t.Fatalf("data line = %q, want %q", data, wantJSON)
		}
		var roundTripped map[string]any
		if err := json.Unmarshal([]byte(data), &roundTripped); err != nil {
			t.Fatalf("data line does not decode: %v", err)
		}
	})
}
