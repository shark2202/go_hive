package agentloop

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func makeJSONPayload(t *testing.T, sizeBytes int) string {
	t.Helper()
	pad := sizeBytes - 12
	if pad < 0 {
		pad = 0
	}
	return `{"data": "` + strings.Repeat("x", pad) + `"}`
}

func makeNestedJSON(depth int) string {
	core := `"leaf"`
	for i := 0; i < depth; i++ {
		core = `{"a":` + core + `}`
	}
	return core
}

func mustParseJSON(t *testing.T, raw string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("result is not valid JSON: %v (raw=%q)", err, raw)
	}
	return value
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_simple_json_only
func TestFindJSONBasicSimpleJSONOnly(t *testing.T) {
	if got := findJSONObject(`{"foo": 1}`); got != `{"foo": 1}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_json_with_surrounding_text
func TestFindJSONBasicSurroundingText(t *testing.T) {
	raw := `Here is the answer: {"foo": 1} Hope that helps!`
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, map[string]any{"foo": float64(1)}) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_json_in_markdown_fence
func TestFindJSONBasicMarkdownFence(t *testing.T) {
	raw := "```json\n{\"foo\": 1}\n```"
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, map[string]any{"foo": float64(1)}) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_multiple_json_first_wins
func TestFindJSONBasicMultipleFirstWins(t *testing.T) {
	raw := `{"first": 1} and then {"second": 2}`
	if got := findJSONObject(raw); got != `{"first": 1}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_missing_closing_brace
func TestFindJSONBasicMissingClosingBrace(t *testing.T) {
	if got := findJSONObject(`{"foo": 1`); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_trailing_comma_returns_balanced_candidate
func TestFindJSONBasicTrailingComma(t *testing.T) {
	if got := findJSONObject(`{"a": 1,}`); got != `{"a": 1,}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_truncated_payload
func TestFindJSONBasicTruncatedPayload(t *testing.T) {
	if got := findJSONObject(`{"key": "val`); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_empty_string
func TestFindJSONBasicEmptyString(t *testing.T) {
	if got := findJSONObject(""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_whitespace_only
func TestFindJSONBasicWhitespaceOnly(t *testing.T) {
	if got := findJSONObject("   \n\t  "); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_no_braces
func TestFindJSONBasicNoBraces(t *testing.T) {
	if got := findJSONObject("hello world"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_braces_inside_string_value
func TestFindJSONBasicBracesInsideStringValue(t *testing.T) {
	raw := `{"msg": "a {b} c"}`
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, map[string]any{"msg": "a {b} c"}) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_escaped_quotes
func TestFindJSONBasicEscapedQuotes(t *testing.T) {
	raw := `{"k": "say \"hi\""}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	if parsed["k"] != `say "hi"` {
		t.Fatalf("k = %#v", parsed["k"])
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_escaped_backslash_at_end_of_value
func TestFindJSONBasicEscapedBackslashAtEnd(t *testing.T) {
	raw := `{"p": "C:\\"}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	if parsed["p"] != `C:\` {
		t.Fatalf("p = %#v", parsed["p"])
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_nested_arrays
func TestFindJSONBasicNestedArrays(t *testing.T) {
	raw := `{"a": [[1], [2]]}`
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, map[string]any{"a": []any{[]any{float64(1)}, []any{float64(2)}}}) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_unicode_emoji
func TestFindJSONBasicUnicodeEmoji(t *testing.T) {
	raw := `{"emoji": "😀🎉"}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	if parsed["emoji"] != "😀🎉" {
		t.Fatalf("emoji = %#v", parsed["emoji"])
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_boolean_and_null
func TestFindJSONBasicBooleanAndNull(t *testing.T) {
	raw := `{"a": true, "b": false, "c": null}`
	want := map[string]any{"a": true, "b": false, "c": nil}
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_numeric_values
func TestFindJSONBasicNumericValues(t *testing.T) {
	raw := `{"int": 42, "float": 3.14, "neg": -1, "exp": 1e10}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	if parsed["int"] != float64(42) || parsed["neg"] != float64(-1) {
		t.Fatalf("parsed = %#v", parsed)
	}
	if parsed["float"].(float64) < 3.139 || parsed["float"].(float64) > 3.141 {
		t.Fatalf("float = %#v", parsed["float"])
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_empty_object
func TestFindJSONBasicEmptyObject(t *testing.T) {
	if got := findJSONObject("{}"); got != "{}" {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBasicCorrectness::test_deeply_nested_objects
func TestFindJSONBasicDeeplyNestedObjects(t *testing.T) {
	raw := `{"a": {"b": {"c": {"d": "deep"}}}}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	node := parsed
	for _, key := range []string{"a", "b", "c"} {
		node = node[key].(map[string]any)
	}
	if node["d"] != "deep" {
		t.Fatalf("d = %#v", node["d"])
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_100kb_json_correctness_and_perf
func TestFindJSONLarge100KB(t *testing.T) {
	payload := makeJSONPayload(t, 100_000)
	raw := "Prefix text. " + payload + " Suffix text."
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if got == "" {
		t.Fatal("no object found")
	}
	if !reflect.DeepEqual(mustParseJSON(t, got), mustParseJSON(t, payload)) {
		t.Fatal("payload mismatch")
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("100KB took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_1mb_json_correctness_and_perf
func TestFindJSONLarge1MB(t *testing.T) {
	payload := makeJSONPayload(t, 1_000_000)
	raw := "Prefix text. " + payload + " Suffix text."
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if got == "" {
		t.Fatal("no object found")
	}
	if !reflect.DeepEqual(mustParseJSON(t, got), mustParseJSON(t, payload)) {
		t.Fatal("payload mismatch")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("1MB took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_2mb_json_exceeds_old_threshold
func TestFindJSONLarge2MB(t *testing.T) {
	payload := makeJSONPayload(t, 2_000_000)
	raw := "Here is the data: " + payload
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if got == "" {
		t.Fatal("no object found")
	}
	if !reflect.DeepEqual(mustParseJSON(t, got), mustParseJSON(t, payload)) {
		t.Fatal("payload mismatch")
	}
	if elapsed > time.Second {
		t.Fatalf("2MB took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_1mb_no_json_early_exit
func TestFindJSONLarge1MBNoBraceEarlyExit(t *testing.T) {
	raw := strings.Repeat("x", 1_000_000)
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
	if elapsed > 10*time.Millisecond {
		t.Fatalf("no-brace scan took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_json_at_end_of_1mb_text
func TestFindJSONLargeJSONAtEndOf1MB(t *testing.T) {
	raw := strings.Repeat("a", 1_000_000) + `{"found": true}`
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if !reflect.DeepEqual(mustParseJSON(t, got), map[string]any{"found": true}) {
		t.Fatalf("got %q", got)
	}
	if elapsed > time.Second {
		t.Fatalf("end-of-1MB took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_100kb_template_braces_performance
func TestFindJSONLarge100KBTemplateBraces(t *testing.T) {
	chunk := "Hello {{name}}, balance: {{bal}}. "
	raw := strings.Repeat(chunk, 100_000/len(chunk))
	start := time.Now()
	_ = findJSONObject(raw)
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("template-brace scan took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_deeply_nested_valid_json_500_levels
func TestFindJSONLarge500Levels(t *testing.T) {
	raw := makeNestedJSON(500)
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if got == "" {
		t.Fatal("no object found")
	}
	node := mustParseJSON(t, got).(map[string]any)
	for i := 0; i < 499; i++ {
		node = node["a"].(map[string]any)
	}
	if node["a"] != "leaf" {
		t.Fatalf("leaf = %#v", node["a"])
	}
	if elapsed > time.Second {
		t.Fatalf("500-deep took %v", elapsed)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestLargeOutputRegression::test_deep_nesting_does_not_hang
func TestFindJSONLargeDeepNestingDoesNotHang(t *testing.T) {
	tooDeep := strings.Repeat("{", 1010) + strings.Repeat("}", 1010)
	raw := tooDeep + " " + `{"found": "after_deep"}`
	start := time.Now()
	got := findJSONObject(raw)
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("deep nesting scan took %v", elapsed)
	}
	if got != tooDeep {
		t.Fatalf("got %q, want the balanced brace prefix", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_only_opening_braces
func TestFindJSONAdversarialOnlyOpeningBraces(t *testing.T) {
	if got := findJSONObject(strings.Repeat("{", 5000)); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_only_closing_braces
func TestFindJSONAdversarialOnlyClosingBraces(t *testing.T) {
	if got := findJSONObject(strings.Repeat("}", 5000)); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_alternating_open_close
func TestFindJSONAdversarialAlternatingOpenClose(t *testing.T) {
	if got := findJSONObject(strings.Repeat("{}", 100)); got != "{}" {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_mismatched_brackets
func TestFindJSONAdversarialMismatchedBrackets(t *testing.T) {
	if got := findJSONObject("{]"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_mismatched_then_valid
func TestFindJSONAdversarialMismatchedThenValid(t *testing.T) {
	raw := `{] then [} but finally {"valid": 1}`
	if got := findJSONObject(raw); got != `{] then [}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_invalid_json_then_valid
func TestFindJSONAdversarialInvalidJSONThenValid(t *testing.T) {
	raw := `{bad content no quotes} {"good": 1}`
	if got := findJSONObject(raw); got != `{bad content no quotes}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_jinja_template_braces
func TestFindJSONAdversarialJinjaTemplateBraces(t *testing.T) {
	raw := "Hello {{name}}, your balance is {{bal}}"
	if got := findJSONObject(raw); got != "{{name}}" {
		t.Fatalf("got %q, want {{name}}", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_cjk_content
func TestFindJSONAdversarialCJKContent(t *testing.T) {
	raw := `{"名前": "太郎", "都市": "東京"}`
	want := map[string]any{"名前": "太郎", "都市": "東京"}
	if got := mustParseJSON(t, findJSONObject(raw)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed = %#v", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_enormous_string_value
func TestFindJSONAdversarialEnormousStringValue(t *testing.T) {
	bigVal := strings.Repeat("a", 500_000)
	raw := `{"data": "` + bigVal + `"}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	if parsed["data"] != bigVal {
		t.Fatalf("data length = %d", len(parsed["data"].(string)))
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_null_byte_in_text
func TestFindJSONAdversarialNullByteInText(t *testing.T) {
	raw := "some\x00text before {\"key\": \"val\"}"
	got := findJSONObject(raw)
	if got != `{"key": "val"}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_negative_depth_then_valid
func TestFindJSONAdversarialNegativeDepthThenValid(t *testing.T) {
	raw := `}} {"result": 42}`
	got := findJSONObject(raw)
	if !reflect.DeepEqual(mustParseJSON(t, got), map[string]any{"result": float64(42)}) {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_json_array_ignored
func TestFindJSONAdversarialJSONArrayIgnored(t *testing.T) {
	raw := `[1, 2, 3] {"obj": true}`
	if got := findJSONObject(raw); got != `{"obj": true}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestAdversarial::test_parametrized_edge_cases
func TestFindJSONAdversarialParametrizedEdgeCases(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"space", " ", ""},
		{"empty_obj", "{}", "{}"},
		{"simple", `{"a":1}`, `{"a":1}`},
		{"no_json", "no json here", ""},
		{"unclosed", "{unclosed", ""},
		{"embedded", `prefix {"k":"v"} suffix`, `{"k":"v"}`},
		{"nested_braces_invalid", "{{{}}}", "{{{}}}"},
		{"unterminated_string", `{"incomplete": "value`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findJSONObject(tc.input); got != tc.want {
				t.Fatalf("findJSONObject(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBehaviourParity::test_returns_string_not_dict
func TestFindJSONParityReturnsStringNotDict(t *testing.T) {
	if got := findJSONObject(`{"a": 1}`); got != `{"a": 1}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBehaviourParity::test_returns_none_not_raises
func TestFindJSONParityReturnsEmptyNotPanics(t *testing.T) {
	if got := findJSONObject("garbage {{ }} badness"); got != "{{ }}" {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBehaviourParity::test_first_valid_object_wins
func TestFindJSONParityFirstValidObjectWins(t *testing.T) {
	raw := `{"a": 1} {"b": 2}`
	if got := findJSONObject(raw); got != `{"a": 1}` {
		t.Fatalf("got %q", got)
	}
}

// Parity: core/tests/test_find_json_hardened.py::TestBehaviourParity::test_string_containing_json_not_parsed
func TestFindJSONParityStringContainingJSONNotParsed(t *testing.T) {
	raw := `{"outer": "{\"inner\": 1}"}`
	parsed := mustParseJSON(t, findJSONObject(raw)).(map[string]any)
	inner, ok := parsed["outer"].(string)
	if !ok {
		t.Fatalf("outer should stay a string, got %#v", parsed["outer"])
	}
	if inner != `{"inner": 1}` {
		t.Fatalf("inner = %#v", inner)
	}
}
