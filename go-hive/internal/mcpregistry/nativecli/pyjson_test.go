package nativecli

import (
	"encoding/json"
	"testing"
)

// Parity-exempt: Go-only formatting layer; it exists so the native CLI can emit
// byte-identical Python json.dumps output (verified by the parity suite).
func TestMarshalPyMatchesPythonSeparators(t *testing.T) {
	value := Ordered{
		{Key: "name", Value: "echo"},
		{Key: "count", Value: json.Number("2")},
		{Key: "ok", Value: true},
		{Key: "missing", Value: nil},
		{Key: "items", Value: []any{"a", json.Number("1.5")}},
	}
	want := `{"name": "echo", "count": 2, "ok": true, "missing": null, "items": ["a", 1.5]}`
	if got := MarshalPy(value); got != want {
		t.Fatalf("MarshalPy = %s, want %s", got, want)
	}
}

// Parity-exempt: Go-only formatting layer.
func TestMarshalPyPrettyUsesTwoSpaceIndent(t *testing.T) {
	value := Ordered{{Key: "content", Value: []any{Ordered{{Key: "type", Value: "text"}, {Key: "text", Value: "hi"}}}}}
	want := "{\n  \"content\": [\n    {\n      \"type\": \"text\",\n      \"text\": \"hi\"\n    }\n  ]\n}"
	if got := MarshalPyPretty(value); got != want {
		t.Fatalf("MarshalPyPretty =\n%s\nwant\n%s", got, want)
	}
	if got := MarshalPyPretty([]any{}); got != "[]" {
		t.Fatalf("empty array = %q", got)
	}
	if got := MarshalPyPretty(Ordered{}); got != "{}" {
		t.Fatalf("empty object = %q", got)
	}
}

// Parity-exempt: Go-only formatting layer.
func TestMarshalPyEscapesLikePython(t *testing.T) {
	value := "quote\" backslash\\ newline\n tab\t ctrl\x01 unicode-é"
	want := `"quote\" backslash\\ newline\n tab\t ctrl\u0001 unicode-é"`
	if got := MarshalPy(value); got != want {
		t.Fatalf("MarshalPy = %s, want %s", got, want)
	}
}

// Parity-exempt: Go-only formatting layer.
func TestFormatPyFloatMatchesPythonRepr(t *testing.T) {
	cases := map[float64]string{
		1:      "1.0",
		3.5:    "3.5",
		0:      "0.0",
		-2.25:  "-2.25",
		1e6:    "1000000.0",
		1e16:   "1e+16",
		1e-7:   "1e-07",
		1234.5: "1234.5",
	}
	for value, want := range cases {
		if got := formatPyFloat(value); got != want {
			t.Fatalf("formatPyFloat(%v) = %q, want %q", value, got, want)
		}
	}
}

// Parity-exempt: Go-only formatting layer.
func TestParseJSONPreservesOrderAndNumberLiterals(t *testing.T) {
	parsed, err := ParseJSON([]byte(`{"z": 1, "a": 2.50, "list": [true, null, "x"]}`))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	object, ok := parsed.(Ordered)
	if !ok {
		t.Fatalf("parsed type = %T", parsed)
	}
	if object[0].Key != "z" || object[1].Key != "a" || object[2].Key != "list" {
		t.Fatalf("key order = %v", []string{object[0].Key, object[1].Key, object[2].Key})
	}
	if got := object[1].Value.(json.Number); string(got) != "2.50" {
		t.Fatalf("number literal = %q, want 2.50", got)
	}
	if got := MarshalPy(parsed); got != `{"z": 1, "a": 2.50, "list": [true, null, "x"]}` {
		t.Fatalf("round trip = %s", got)
	}
}

// Parity-exempt: Go-only formatting layer.
func TestParseJSONRejectsTrailingData(t *testing.T) {
	if _, err := ParseJSON([]byte(`{"a": 1} trailing`)); err == nil {
		t.Fatal("expected an error for trailing data")
	}
	if _, err := ParseJSON([]byte(`not json`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}
