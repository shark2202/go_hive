package senders

// Ports tools/tests/test_senders_body_html.py. The body is TWO fields and both
// must go out right: on 2026-07-13, 652 plain-text bodies were sent as
// text/html and arrived as one jammed line, with a successful send log the
// whole time. These tests exist to keep that from being "simplified" back.

import (
	"strings"
	"testing"
)

// TRACKER_BODY is the exact shape stored in a colony tracker: paragraphs
// separated by a blank line, and a signature block whose lines are single
// newlines.
const trackerBody = "Hi Sarah,\n" +
	"\n" +
	"I noticed Acme is hiring 12 SDRs this quarter.\n" +
	"That usually means ramp time is the bottleneck.\n" +
	"\n" +
	"Best,\n" +
	"Chris Voss\n" +
	"Head of Outbound"

func strPtr(value string) *string { return &value }

// Parity: tools/tests/test_senders_body_html.py::test_text_only_generates_html_with_real_line_breaks
func TestTextOnlyGeneratesHTMLWithRealLineBreaks(t *testing.T) {
	htmlBody, textBody := ResolveBody("", strPtr(trackerBody), "")

	if want := "Best,<br>Chris Voss<br>Head of Outbound"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want it to contain %q", htmlBody, want)
	}
	if want := "this quarter.<br>That usually means"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want it to contain %q", htmlBody, want)
	}
	if want := "<p>Hi Sarah,</p>"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want it to contain %q", htmlBody, want)
	}
	if contains(htmlBody, "Chris Voss Head of Outbound") {
		t.Fatalf("regression: lines were jammed into one: %q", htmlBody)
	}
	if textBody != trackerBody {
		t.Fatalf("text = %q, want the caller's plain text verbatim", textBody)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_html_only_generates_a_readable_text_alternative
func TestHTMLOnlyGeneratesReadableTextAlternative(t *testing.T) {
	htmlBody, textBody := ResolveBody(`<p>Hi Sarah,</p><p>We ship <strong>fast</strong>.<br>Worth a chat?</p>`, nil, "")

	if want := `<p>Hi Sarah,</p>`; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want it untouched", htmlBody)
	}
	if want := "Hi Sarah,\n\nWe ship fast.\nWorth a chat?"; textBody != want {
		t.Fatalf("text = %q, want %q", textBody, want)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_both_provided_are_used_verbatim
func TestBothBodiesUsedVerbatim(t *testing.T) {
	htmlBody, textBody := ResolveBody("<p>Rich</p>", strPtr("Plain"), "")
	if htmlBody != "<p>Rich</p>" || textBody != "Plain" {
		t.Fatalf("got (%q, %q), want verbatim pair", htmlBody, textBody)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_empty_text_opts_out_of_the_plain_part
func TestEmptyTextOptsOutOfPlainPart(t *testing.T) {
	htmlBody, textBody := ResolveBody("<p>HTML only</p>", strPtr(""), "")
	if htmlBody != "<p>HTML only</p>" {
		t.Fatalf("html = %q", htmlBody)
	}
	if textBody != "" {
		t.Fatalf("text = %q, want an explicit opt-out", textBody)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_legacy_html_content_holding_plain_text_is_still_safe
func TestLegacyHTMLContentHoldingPlainTextIsStillSafe(t *testing.T) {
	htmlBody, textBody := ResolveBody("", nil, trackerBody)
	if want := "Best,<br>Chris Voss<br>Head of Outbound"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want %q", htmlBody, want)
	}
	if textBody != trackerBody {
		t.Fatalf("text = %q, want the original body", textBody)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_legacy_html_content_holding_real_html_still_works
func TestLegacyHTMLContentHoldingRealHTMLStillWorks(t *testing.T) {
	htmlBody, textBody := ResolveBody("", nil, "<p>Hi</p><p>Real markup.</p>")
	if htmlBody != "<p>Hi</p><p>Real markup.</p>" {
		t.Fatalf("html = %q", htmlBody)
	}
	if want := "Hi\n\nReal markup."; textBody != want {
		t.Fatalf("text = %q, want %q", textBody, want)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_special_characters_are_escaped_not_broken
func TestSpecialCharactersEscapedNotBroken(t *testing.T) {
	htmlBody, _ := ResolveBody("", strPtr("R&D team <3\nTerms & conditions"), "")
	if want := "R&amp;D team &lt;3"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want %q", htmlBody, want)
	}
	if want := "Terms &amp; conditions"; !contains(htmlBody, want) {
		t.Fatalf("html = %q, want %q", htmlBody, want)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_html_to_text_unescapes_entities
func TestHTMLToTextUnescapesEntities(t *testing.T) {
	if got, want := HTMLToText("<p>R&amp;D team &lt;3</p>"), "R&D team <3"; got != want {
		t.Fatalf("HTMLToText = %q, want %q", got, want)
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_empty_bodies_do_not_become_markup
func TestEmptyBodiesDoNotBecomeMarkup(t *testing.T) {
	for _, body := range []string{"", "   ", "\n\n"} {
		if got := TextToHTML(body); got != "" {
			t.Fatalf("TextToHTML(%q) = %q, want empty", body, got)
		}
	}
}

// Parity: tools/tests/test_senders_body_html.py::test_no_body_at_all_resolves_to_nothing
func TestNoBodyAtAllResolvesToNothing(t *testing.T) {
	htmlBody, textBody := ResolveBody("", nil, "")
	if htmlBody != "" || textBody != "" {
		t.Fatalf("got (%q, %q), want both empty", htmlBody, textBody)
	}
}

// Parity-exempt: _parse_recipients is private in senders_tool.py and has no
// upstream unit test; the JSON-array and comma/newline forms are exercised here.
func TestParseRecipientsAcceptsJSONAndSeparatedLists(t *testing.T) {
	fromJSON := ParseRecipients(`["a@example.com", "b@example.com"]`)
	if len(fromJSON) != 2 || fromJSON[0] != "a@example.com" || fromJSON[1] != "b@example.com" {
		t.Fatalf("JSON recipients = %#v", fromJSON)
	}
	fromList := ParseRecipients("a@example.com, b@example.com\nc@example.com")
	if len(fromList) != 3 {
		t.Fatalf("list recipients = %#v", fromList)
	}
	if got := ParseRecipients("  "); got != nil {
		t.Fatalf("blank recipients = %#v, want nil", got)
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
