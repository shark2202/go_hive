package hiveconf

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: framework/config.py's numeric readers return the JSON value as-is.
// Python accepts 8192.0 and bool (bool is an int subclass); the Go reader must
// not silently fall back to the catalog/default for those spellings.
func TestJSONIntAcceptsPythonNumberSpellings(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
		ok    bool
	}{
		{"integer", `7`, 7, true},
		{"float with zero fraction", `8192.0`, 8192, true},
		{"exponent", `8.192e3`, 8192, true},
		{"true", `true`, 1, true},
		{"false", `false`, 0, true},
		{"fractional float", `8.5`, 0, false},
		{"string", `"7"`, 0, false},
		{"null", `null`, 0, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var value any
			decoder := json.NewDecoder(strings.NewReader(testCase.input))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				t.Fatalf("decode %s: %v", testCase.input, err)
			}
			got, ok := JSONInt(value)
			if ok != testCase.ok || (ok && got != testCase.want) {
				t.Fatalf("JSONInt(%s) = (%d, %v), want (%d, %v)", testCase.input, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

// Parity: framework/config.py::get_max_tool_result_chars (the value Python
// hands to a LoopConfig int field: numeric text is coerced, empty/null is not;
// a missing or empty value therefore keeps the call site's fallback instead of
// reading as 0, which would disable the spillover cap).
func TestMaxToolResultCharsAcceptsPythonIntSpellings(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  int
	}{
		{"missing file", ``, 30_000},
		{"empty document", `{}`, 30_000},
		{"loop section without the key", `{"loop": {}}`, 30_000},
		{"null value", `{"loop": {"max_tool_result_chars": null}}`, 30_000},
		{"empty string", `{"loop": {"max_tool_result_chars": ""}}`, 30_000},
		{"whitespace string", `{"loop": {"max_tool_result_chars": "   "}}`, 30_000},
		{"integer", `{"loop": {"max_tool_result_chars": 4096}}`, 4096},
		{"integral float", `{"loop": {"max_tool_result_chars": 4096.0}}`, 4096},
		{"numeric string", `{"loop": {"max_tool_result_chars": "4096"}}`, 4096},
		{"padded numeric string", `{"loop": {"max_tool_result_chars": " 4096 "}}`, 4096},
		{"zero disables the cap", `{"loop": {"max_tool_result_chars": 0}}`, 0},
		{"boolean true", `{"loop": {"max_tool_result_chars": true}}`, 1},
		{"fractional float", `{"loop": {"max_tool_result_chars": 4096.5}}`, 30_000},
		{"non-numeric string", `{"loop": {"max_tool_result_chars": "lots"}}`, 30_000},
		{"object value", `{"loop": {"max_tool_result_chars": {}}}`, 30_000},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			if testCase.value != "" {
				if err := os.WriteFile(filepath.Join(root, "configuration.json"), []byte(testCase.value), 0o644); err != nil {
					t.Fatalf("write configuration: %v", err)
				}
			}
			home, err := hivehome.ResolveAt(root)
			if err != nil {
				t.Fatalf("resolve home: %v", err)
			}
			if got := MaxToolResultChars(home, 30_000); got != testCase.want {
				t.Fatalf("MaxToolResultChars = %d, want %d", got, testCase.want)
			}
		})
	}
}
