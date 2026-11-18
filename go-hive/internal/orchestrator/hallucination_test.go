package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

func expectDataBufferWriteError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected DataBufferWriteError, got nil")
	}
	var writeErr *DataBufferWriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("expected DataBufferWriteError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "hallucinated code") {
		t.Fatalf("error text %q does not mention hallucinated code", err.Error())
	}
}

func mustRead(t *testing.T, buffer *DataBuffer, key string) any {
	t.Helper()
	value, err := buffer.Read(key)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	return value
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_code_at_start
func TestDataBufferDetectsCodeAtStart(t *testing.T) {
	buffer := NewDataBuffer()
	content := "```python\nimport os\ndef hack(): pass\n```" + strings.Repeat("A", 6000)
	expectDataBufferWriteError(t, buffer.Write("output", content))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_code_in_middle
func TestDataBufferDetectsCodeInMiddle(t *testing.T) {
	buffer := NewDataBuffer()
	paddingStart := strings.Repeat("A", 600)
	code := "\n```python\nimport os\ndef malicious(): pass\n```\n"
	paddingEnd := strings.Repeat("B", 5000)
	expectDataBufferWriteError(t, buffer.Write("output", paddingStart+code+paddingEnd))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_code_at_end
func TestDataBufferDetectsCodeAtEnd(t *testing.T) {
	buffer := NewDataBuffer()
	padding := strings.Repeat("A", 5500)
	code := "\n```python\nclass Exploit:\n    pass\n```"
	expectDataBufferWriteError(t, buffer.Write("output", padding+code))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_javascript_code
func TestDataBufferDetectsJavaScriptCode(t *testing.T) {
	buffer := NewDataBuffer()
	padding := strings.Repeat("A", 600)
	code := "\nfunction malicious() { require('child_process'); }\n"
	paddingEnd := strings.Repeat("B", 5000)
	expectDataBufferWriteError(t, buffer.Write("output", padding+code+paddingEnd))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_sql_injection
func TestDataBufferDetectsSQLInjection(t *testing.T) {
	buffer := NewDataBuffer()
	padding := strings.Repeat("A", 600)
	code := "\nDROP TABLE users; SELECT * FROM passwords;\n"
	paddingEnd := strings.Repeat("B", 5000)
	expectDataBufferWriteError(t, buffer.Write("output", padding+code+paddingEnd))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_detects_script_injection
func TestDataBufferDetectsScriptInjection(t *testing.T) {
	buffer := NewDataBuffer()
	padding := strings.Repeat("A", 600)
	code := "\n<script>alert('xss')</script>\n"
	paddingEnd := strings.Repeat("B", 5000)
	expectDataBufferWriteError(t, buffer.Write("output", padding+code+paddingEnd))
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_allows_short_strings_without_validation
func TestDataBufferAllowsShortStrings(t *testing.T) {
	buffer := NewDataBuffer()
	content := "def hello(): pass"
	if err := buffer.Write("output", content); err != nil {
		t.Fatalf("short string rejected: %v", err)
	}
	if got := mustRead(t, buffer, "output"); got != content {
		t.Fatalf("read = %#v, want %q", got, content)
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_allows_long_strings_without_code
func TestDataBufferAllowsLongStringWithoutCode(t *testing.T) {
	buffer := NewDataBuffer()
	content := strings.Repeat("This is a long text document. ", 500)
	if err := buffer.Write("output", content); err != nil {
		t.Fatalf("clean long string rejected: %v", err)
	}
	if got := mustRead(t, buffer, "output"); got != content {
		t.Fatalf("read = %#v, want the written string", got)
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_validate_false_bypasses_check
func TestDataBufferValidateFalseBypassesCheck(t *testing.T) {
	buffer := NewDataBuffer()
	content := "```python\nimport os\n```" + strings.Repeat("A", 6000)
	if err := buffer.Write("output", content, false); err != nil {
		t.Fatalf("validate=false should bypass the check, got %v", err)
	}
	if got := mustRead(t, buffer, "output"); got != content {
		t.Fatalf("read = %#v, want the written string", got)
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestDataBufferHallucinationDetection::test_sampling_for_very_long_strings
func TestDataBufferSamplesVeryLongStrings(t *testing.T) {
	buffer := NewDataBuffer()
	size := 50000
	codePosition := int(float64(size) * 0.75)
	content := strings.Repeat("A", codePosition) +
		"def hidden_code(): pass" +
		strings.Repeat("B", size-codePosition-25)
	expectDataBufferWriteError(t, buffer.Write("output", content))
}

// Parity: core/tests/test_hallucination_detection.py::TestOutputValidatorHallucinationDetection::test_detects_code_anywhere_in_output
func TestOutputValidatorDetectsCodeAnywhere(t *testing.T) {
	validator := &OutputValidator{}
	padding := strings.Repeat("Normal text content. ", 50)
	code := "\ndef suspicious_function():\n    pass\n"
	result := validator.ValidateNoHallucination(map[string]any{"result": padding + code})
	if !result.Success {
		t.Fatalf("code detection only warns upstream, got errors %v", result.Errors)
	}
	if result.ErrorText() != "" {
		t.Fatalf("error text = %q, want empty", result.ErrorText())
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestOutputValidatorHallucinationDetection::test_contains_code_indicators_full_check
func TestOutputValidatorContainsCodeIndicatorsFullCheck(t *testing.T) {
	validator := &OutputValidator{}
	content := strings.Repeat("A", 600) + "import os"
	if !validator.containsCodeIndicators(content) {
		t.Fatal("code at position 600 was not detected")
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestOutputValidatorHallucinationDetection::test_contains_code_indicators_sampling
func TestOutputValidatorContainsCodeIndicatorsSampling(t *testing.T) {
	validator := &OutputValidator{}
	size := 50000
	codePosition := int(float64(size) * 0.75)
	content := strings.Repeat("A", codePosition) +
		"class HiddenClass:" +
		strings.Repeat("B", size-codePosition-18)
	if !validator.containsCodeIndicators(content) {
		t.Fatal("code at the 75% mark was not detected")
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestOutputValidatorHallucinationDetection::test_no_false_positive_for_clean_text
func TestOutputValidatorNoFalsePositiveCleanText(t *testing.T) {
	validator := &OutputValidator{}
	content := strings.Repeat("This is a perfectly normal document. ", 300)
	if validator.containsCodeIndicators(content) {
		t.Fatal("clean text flagged as code")
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestOutputValidatorHallucinationDetection::test_detects_multiple_languages
func TestOutputValidatorDetectsMultipleLanguages(t *testing.T) {
	validator := &OutputValidator{}
	cases := []string{
		"function test() {}",
		"const x = 5;",
		"SELECT * FROM users",
		"DROP TABLE data",
		"<script>",
		"<?php",
	}
	for _, code := range cases {
		if !validator.containsCodeIndicators(code) {
			t.Fatalf("failed to detect: %q", code)
		}
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestEdgeCases::test_empty_string
func TestHallucinationEdgeEmptyString(t *testing.T) {
	buffer := NewDataBuffer()
	if err := buffer.Write("output", ""); err != nil {
		t.Fatalf("empty string rejected: %v", err)
	}
	if got := mustRead(t, buffer, "output"); got != "" {
		t.Fatalf("read = %#v, want empty string", got)
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestEdgeCases::test_non_string_values
func TestHallucinationEdgeNonStringValues(t *testing.T) {
	buffer := NewDataBuffer()
	if err := buffer.Write("number", 12345); err != nil {
		t.Fatalf("number rejected: %v", err)
	}
	if err := buffer.Write("list", []any{1, 2, 3}); err != nil {
		t.Fatalf("list rejected: %v", err)
	}
	if err := buffer.Write("dict", map[string]any{"key": "value"}); err != nil {
		t.Fatalf("dict rejected: %v", err)
	}
	if err := buffer.Write("bool", true); err != nil {
		t.Fatalf("bool rejected: %v", err)
	}
	if got := mustRead(t, buffer, "number"); got != 12345 {
		t.Fatalf("number = %#v", got)
	}
	list, ok := mustRead(t, buffer, "list").([]any)
	if !ok || len(list) != 3 || list[0] != 1 || list[1] != 2 || list[2] != 3 {
		t.Fatalf("list = %#v", list)
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestEdgeCases::test_exactly_5000_chars
func TestHallucinationEdgeExactly5000Chars(t *testing.T) {
	buffer := NewDataBuffer()
	content := "def code(): pass" + strings.Repeat("A", 5000-16)
	if len(content) != 5000 {
		t.Fatalf("fixture length = %d, want 5000", len(content))
	}
	if err := buffer.Write("output", content); err != nil {
		t.Fatalf("exactly 5000 chars rejected: %v", err)
	}
	if got := mustRead(t, buffer, "output"); got != content {
		t.Fatalf("read length = %d", len(got.(string)))
	}
}

// Parity: core/tests/test_hallucination_detection.py::TestEdgeCases::test_5001_chars_triggers_validation
func TestHallucinationEdge5001CharsTriggers(t *testing.T) {
	buffer := NewDataBuffer()
	content := "def code(): pass" + strings.Repeat("A", 5001-16)
	if len(content) != 5001 {
		t.Fatalf("fixture length = %d, want 5001", len(content))
	}
	expectDataBufferWriteError(t, buffer.Write("output", content))
}

// Parity-exempt: 上游没有 DataBuffer 权限视图的 pytest；语义取自
func TestDataBufferPermissions(t *testing.T) {
	parent := NewDataBuffer()
	if err := parent.Write("shared", "value"); err != nil {
		t.Fatalf("parent write: %v", err)
	}

	view := parent.WithPermissions([]string{"shared"}, []string{"scoped"})
	value, err := view.Read("shared")
	if err != nil || value != "value" {
		t.Fatalf("view read shared = %#v, %v", value, err)
	}

	if _, err := view.Read("missing"); err == nil {
		t.Fatal("view should not read keys outside its allow-list")
	} else {
		var permissionErr *PermissionError
		if !errors.As(err, &permissionErr) {
			t.Fatalf("read error = %T, want PermissionError", err)
		}
	}

	if err := view.Write("scoped", "ok"); err != nil {
		t.Fatalf("view write scoped: %v", err)
	}
	if got := mustRead(t, parent, "scoped"); got != "ok" {
		t.Fatalf("parent cannot see scoped write: %#v", got)
	}
	if err := view.Write("shared", "nope"); err == nil {
		t.Fatal("view should not write keys outside its allow-list")
	}

	all := view.ReadAll()
	if len(all) != 1 || all["shared"] != "value" {
		t.Fatalf("view ReadAll = %#v", all)
	}
}

// Parity-exempt: 上游没有 validate_output_keys 的 pytest；语义取自
func TestOutputValidatorValidateOutputKeys(t *testing.T) {
	validator := &OutputValidator{}

	result := validator.ValidateOutputKeys(
		map[string]any{"name": "ok"},
		[]string{"name", "missing"},
		false,
		nil,
	)
	if result.Success {
		t.Fatal("missing key should fail validation")
	}
	want := "Missing required output key: 'missing'"
	if result.ErrorText() != want {
		t.Fatalf("error = %q, want %q", result.ErrorText(), want)
	}

	result = validator.ValidateOutputKeys(
		map[string]any{"none": nil, "blank": "   "},
		[]string{"none", "blank"},
		false,
		nil,
	)
	if result.Success {
		t.Fatal("nil and blank values should fail validation")
	}
	if result.ErrorText() != "Output key 'none' is None; Output key 'blank' is empty string" {
		t.Fatalf("error = %q", result.ErrorText())
	}

	result = validator.ValidateOutputKeys(
		map[string]any{"none": nil, "blank": "   ", "text": ""},
		[]string{"none", "blank"},
		false,
		[]string{"none", "blank"},
	)
	if !result.Success {
		t.Fatalf("nullable keys should pass, got %v", result.Errors)
	}

	result = validator.ValidateOutputKeys(
		map[string]any{"text": ""},
		[]string{"text"},
		true,
		nil,
	)
	if !result.Success {
		t.Fatalf("allowEmpty should pass, got %v", result.Errors)
	}

	result = validator.ValidateOutputKeys(nil, []string{"anything"}, false, nil)
	if result.ErrorText() != "Output is not a dict, got NoneType" {
		t.Fatalf("nil output error = %q", result.ErrorText())
	}
}

// Parity-exempt: 上游没有 validate_no_hallucination 长度上限的 pytest；
func TestOutputValidatorMaxLength(t *testing.T) {
	validator := &OutputValidator{}

	allowed := map[string]any{"text": strings.Repeat("x", 50000)}
	if result := validator.ValidateNoHallucination(allowed); !result.Success {
		t.Fatalf("50000 chars should be allowed, got %v", result.Errors)
	}

	tooLong := map[string]any{"text": strings.Repeat("x", 50001), "other": 1}
	result := validator.ValidateNoHallucination(tooLong)
	if result.Success {
		t.Fatal("50001 chars should fail")
	}
	want := "Output key 'text' exceeds max length (50001 > 50000)"
	if result.ErrorText() != want {
		t.Fatalf("error = %q, want %q", result.ErrorText(), want)
	}

	result = validator.ValidateNoHallucination(tooLong, 10)
	if !strings.Contains(result.ErrorText(), "50001 > 10") {
		t.Fatalf("custom max length error = %q", result.ErrorText())
	}
}

// Parity-exempt: 上游 validate_schema 依赖可选 jsonschema 包且没有 pytest；
func TestOutputValidatorValidateSchemaSubset(t *testing.T) {
	validator := &OutputValidator{}
	schema := map[string]any{
		"type":     "object",
		"required": []any{"name", "tags"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "minLength": 2, "pattern": "^[a-z]+$"},
			"tags": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"score": map[string]any{"type": "number", "minimum": 0, "maximum": 100},
		},
		"additionalProperties": false,
	}

	valid := map[string]any{
		"name":  "hive",
		"tags":  []any{"a", "b"},
		"score": 42,
	}
	if result := validator.ValidateSchema(valid, schema); !result.Success {
		t.Fatalf("valid output rejected: %v", result.Errors)
	}

	invalid := map[string]any{
		"name":  "H",
		"tags":  []any{"a", 7},
		"score": 101,
		"extra": true,
	}
	result := validator.ValidateSchema(invalid, schema)
	if result.Success {
		t.Fatal("invalid output passed schema validation")
	}
	errorsText := result.ErrorText()
	for _, fragment := range []string{
		"name: \"H\" is too short",
		"name: \"H\" does not match \"^[a-z]+$\"",
		"tags.1: 7 is not of type 'string'",
		"score: 101 is greater than the maximum of 100",
		"root: Additional properties are not allowed ('extra' was unexpected)",
	} {
		if !strings.Contains(errorsText, fragment) {
			t.Fatalf("schema errors %q missing %q", errorsText, fragment)
		}
	}

	missing := validator.ValidateSchema(map[string]any{}, schema)
	if !strings.Contains(missing.ErrorText(), "root: 'name' is a required property") {
		t.Fatalf("required error = %q", missing.ErrorText())
	}
}

// Parity-exempt: 上游没有 validate_all 组合顺序的 pytest；语义取自
func TestOutputValidatorValidateAll(t *testing.T) {
	validator := &OutputValidator{}
	output := map[string]any{"text": strings.Repeat("x", 50001)}

	result := validator.ValidateAll(
		output,
		[]string{"text", "missing"},
		map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		true,
		nil,
	)
	if result.Success {
		t.Fatal("validate_all should fail")
	}
	want := "Missing required output key: 'missing'; Output key 'text' exceeds max length (50001 > 50000)"
	if result.ErrorText() != want {
		t.Fatalf("error = %q, want %q", result.ErrorText(), want)
	}

	ok := validator.ValidateAll(map[string]any{"text": "hello"}, nil, nil, true, nil)
	if !ok.Success || ok.ErrorText() != "" {
		t.Fatalf("clean output failed: %#v", ok)
	}
}
