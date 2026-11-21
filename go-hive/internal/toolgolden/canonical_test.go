package toolgolden

import (
	"encoding/json"
	"testing"
)

// Parity-exempt: Go-side golden harness; upstream has no canonicalizer.
//
// TestCanonicalizeEnvelopeShapes pins the documented rules: the is_error
// marker, the JSON/text forms, the attach_file trailer and the reminder strip.
func TestCanonicalizeEnvelopeShapes(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    Envelope
	}{
		{
			name:    "plain json",
			content: `{"b": 2, "a": "x"}`,
			want: Envelope{
				Form:  "json",
				Value: map[string]any{"a": "x", "b": json.Number("2")},
			},
		},
		{
			name:    "error marker is part of the envelope",
			content: `ERROR: {"error":"boom"}`,
			want: Envelope{
				IsError: true,
				Form:    "json",
				Value:   map[string]any{"error": "boom"},
			},
		},
		{
			name:    "python's double marker collapses",
			content: `ERROR: ERROR: unknown credentials action 'list'. Call credentials() with no arguments for usage.`,
			want: Envelope{
				IsError: true,
				Form:    "text",
				Text:    "unknown credentials action 'list'. Call credentials() with no arguments for usage.",
			},
		},
		{
			name:    "plain text without marker",
			content: "Error: File not found: /tmp/x/missing.txt",
			want: Envelope{
				Form: "text",
				Text: "Error: File not found: /tmp/x/missing.txt",
			},
		},
		{
			name:    "json with a text trailer",
			content: "{\"attached\": [], \"errors\": []}\n[attached probe.txt (text/plain)]\n\nprobe file content\nsecond line\n",
			want: Envelope{
				Form:    "json",
				Value:   map[string]any{"attached": []any{}, "errors": []any{}},
				Trailer: "[attached probe.txt (text/plain)]\n\nprobe file content\nsecond line",
			},
		},
		{
			name:    "trailing reminder is recorded, not compared",
			content: "{\"success\": true}\n\n<system-reminder>\ntask stuff\n</system-reminder>\n",
			want: Envelope{
				Form:     "json",
				Value:    map[string]any{"success": true},
				Reminder: true,
			},
		},
		{
			name:    "empty body",
			content: "",
			want:    Envelope{Form: "text"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := Canonicalize(testCase.content, DefaultOptions())
			if !got.Equal(testCase.want) {
				t.Fatalf("Canonicalize(%q) = %#v, want %#v", testCase.content, got, testCase.want)
			}
		})
	}
}

// Parity-exempt: Go-side golden harness; upstream has no canonicalizer.
//
// TestCanonicalizeValueMasks pins the volatile-value rules: a masked key is a
// placeholder regardless of its value, and text masks survive inside strings.
func TestCanonicalizeValueMasks(t *testing.T) {
	content := `{"session_id": "session_20261121_111529_b5f2aaaf", "created_at": 1795230918.884984, ` +
		`"datetime": "2026-11-21T03:15:18.717088+00:00", "next_step": "python only advice", ` +
		`"note": "session_20261121_111529_b5f2aaaf ran at 2026-11-21T03:15:18+00:00"}`
	got := Canonicalize(content, DefaultOptions())
	value, ok := got.Value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v", got.Value)
	}
	want := map[string]any{
		"session_id": "<SESSION_ID>",
		"created_at": "<CREATED_AT>",
		"datetime":   "<DATETIME>",
		"next_step":  "<ADVISORY>",
		"note":       "session_<ID> ran at <TIMESTAMP>",
	}
	if got, wantJSON := mustJSON(value), mustJSON(want); got != wantJSON {
		t.Fatalf("masks = %s, want %s", got, wantJSON)
	}
}

// Parity-exempt: Go-side golden harness; upstream has no canonicalizer.
//
// TestCanonicalizePathMasks pins the capture-layout and explicit path masks.
func TestCanonicalizePathMasks(t *testing.T) {
	options := DefaultOptions().WithPath("/var/folders/xx/T/hive-test", "<WS>")
	content := `{"/private/tmp/tool-output-capture/go-home/queens/q/sessions/s/data/search_messages_1.txt": 1}`
	got := Canonicalize(content, options)
	if !got.Equal(Envelope{
		Form: "json",
		Value: map[string]any{
			"<HOME>/queens/q/sessions/s/data/search_messages_1.txt": json.Number("1"),
		},
	}) {
		t.Fatalf("json body = %#v", got)
	}
	text := Canonicalize("Error: File not found: /var/folders/xx/T/hive-test/missing.txt", options)
	if text.Text != "Error: File not found: <WS>/missing.txt" {
		t.Fatalf("text = %q", text.Text)
	}
}

// Parity-exempt: Go-side golden harness; upstream has no canonicalizer.
//
// TestFromGoldenPayloadRebuildsEnvelope proves the loader and the canonicalizer
// agree, which is what makes a golden comparison meaningful.
func TestFromGoldenPayloadRebuildsEnvelope(t *testing.T) {
	content := `{"success": false, "task_id": 999, "message": "Task #999 not found."}` + "\n\n<system-reminder>\nnoise\n</system-reminder>\n"
	direct := Canonicalize(content, DefaultOptions())
	rebuilt := FromGoldenPayload(direct.IsError, direct.Form, mustJSON(direct.Value), direct.Trailer, direct.Reminder)
	if !direct.Equal(rebuilt) {
		t.Fatalf("rebuilt = %#v, want %#v", rebuilt, direct)
	}
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
