package agentloop

import (
	"reflect"
	"strings"
	"testing"
)

// Parity: core/tests/test_ask_user_sanitizer.py::test_clean_question_passes_through_unchanged
func TestSanitizeAskUserCleanQuestionPassesThrough(t *testing.T) {
	q, opts := SanitizeAskUserInputs("What's next?", nil)
	if q != "What's next?" {
		t.Fatalf("question = %q, want %q", q, "What's next?")
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_strips_trailing_close_question_tag
func TestSanitizeAskUserStripsTrailingCloseQuestionTag(t *testing.T) {
	q, opts := SanitizeAskUserInputs("What now?</question>", nil)
	if q != "What now?" {
		t.Fatalf("question = %q, want %q", q, "What now?")
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_strips_close_question_tag_case_insensitive_with_whitespace
func TestSanitizeAskUserStripsCloseTagCaseInsensitiveWithWhitespace(t *testing.T) {
	q, opts := SanitizeAskUserInputs("What now?  </QUESTION>  ", nil)
	if q != "What now?" {
		t.Fatalf("question = %q, want %q", q, "What now?")
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_recovers_inline_uppercase_options
func TestSanitizeAskUserRecoversInlineUppercaseOptions(t *testing.T) {
	raw := "What do you want to do from here?</question>\n_OPTIONS: [\"De-risk — trim PRLG\", \"Add to a position\", \"Open a short\"]"
	q, opts := SanitizeAskUserInputs(raw, nil)
	if q != "What do you want to do from here?" {
		t.Fatalf("question = %q, want %q", q, "What do you want to do from here?")
	}
	want := []string{"De-risk — trim PRLG", "Add to a position", "Open a short"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_recovers_inline_lowercase_options
func TestSanitizeAskUserRecoversInlineLowercaseOptions(t *testing.T) {
	q, opts := SanitizeAskUserInputs("Pick one\noptions: [\"A\", \"B\", \"C\"]", nil)
	if q != "Pick one" {
		t.Fatalf("question = %q, want %q", q, "Pick one")
	}
	want := []string{"A", "B", "C"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_recovers_inline_underscore_options
func TestSanitizeAskUserRecoversInlineUnderscoreOptions(t *testing.T) {
	q, opts := SanitizeAskUserInputs("Pick one\n_options: [\"A\", \"B\"]", nil)
	if q != "Pick one" {
		t.Fatalf("question = %q, want %q", q, "Pick one")
	}
	want := []string{"A", "B"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_recovered_options_dropped_when_not_a_list
func TestSanitizeAskUserRecoveredOptionsDroppedWhenNotList(t *testing.T) {
	raw := "Pick one\noptions: \"not-a-list\""
	q, opts := SanitizeAskUserInputs(raw, nil)
	if strings.Contains(strings.ToLower(q), "options") && !strings.Contains(q, "not-a-list") {
		t.Fatalf("question = %q, want options removed or the raw value kept", q)
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_recovered_options_dropped_when_too_many
func TestSanitizeAskUserRecoveredOptionsDroppedWhenTooMany(t *testing.T) {
	raw := "Pick\noptions: [\"a\",\"b\",\"c\",\"d\",\"e\",\"f\",\"g\",\"h\",\"i\",\"j\"]"
	_, opts := SanitizeAskUserInputs(raw, nil)
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_does_not_overwrite_real_options
func TestSanitizeAskUserDoesNotOverwriteRealOptions(t *testing.T) {
	realOptions := []string{"X", "Y"}
	q, opts := SanitizeAskUserInputs("Plain question?", realOptions)
	if q != "Plain question?" {
		t.Fatalf("question = %q, want %q", q, "Plain question?")
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_none_question_returns_empty
func TestSanitizeAskUserNoneQuestionReturnsEmpty(t *testing.T) {
	q, opts := SanitizeAskUserInputs(nil, nil)
	if q != "" {
		t.Fatalf("question = %q, want empty", q)
	}
	if opts != nil {
		t.Fatalf("options = %v, want nil", opts)
	}
}

// Parity: core/tests/test_ask_user_sanitizer.py::test_collapses_excess_blank_lines_after_removal
func TestSanitizeAskUserCollapsesExcessBlankLinesAfterRemoval(t *testing.T) {
	raw := "What?\n\n\n\noptions: [\"a\", \"b\"]"
	q, opts := SanitizeAskUserInputs(raw, nil)
	if q != "What?" {
		t.Fatalf("question = %q, want %q", q, "What?")
	}
	want := []string{"a", "b"}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("options = %v, want %v", opts, want)
	}
}
