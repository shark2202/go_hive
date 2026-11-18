package agentloop

import "testing"

// Parity: core/tests/test_janitor_review_fixes.py::test_extractors_match_all_header_formats
func TestExtractorsMatchAllHeaderFormats(t *testing.T) {
	current := "Tool `terminal_exec` returned 50,893 characters (too large for context). " +
		"Full result at: /h/session/data/terminal_exec_15.txt\n" +
		"For targeted lookup, run `grep -nE \"<pattern>\" \"/h/session/data/terminal_exec_15.txt\"`."
	micro := "Old tool result (44,754 chars) at /h/session/data/web_scrape_3.txt. Use terminal_rg."
	prose := "Full result saved at: /h/session/data/pdf_read_2.txt"
	legacy := "Result too big [Saved to 'browser_html_9.txt']"

	cases := []struct{ content, want string }{
		{current, "/h/session/data/terminal_exec_15.txt"},
		{micro, "/h/session/data/web_scrape_3.txt"},
		{prose, "/h/session/data/pdf_read_2.txt"},
		{legacy, "browser_html_9.txt"},
	}
	extractors := []struct {
		name string
		fn   func(string) *string
	}{
		{"ExtractSpilloverFilename", ExtractSpilloverFilename},
		{"extractSpilloverFilenameInline", extractSpilloverFilenameInline},
	}
	for _, ex := range extractors {
		for _, c := range cases {
			got := ex.fn(c.content)
			if got == nil || *got != c.want {
				t.Fatalf("%s(%q) = %v, want %q", ex.name, c.content, got, c.want)
			}
		}
	}
}
