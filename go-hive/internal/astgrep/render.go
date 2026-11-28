package astgrep

import (
	"fmt"
	"strings"
)

// Render turns a Result into the model-facing text. The shape is frozen in
// doc.go: path headers, *LINE:text anchors, continuation lines with a leading
// space, optional meta lines, a limit notice and deduplicated parse issues.
func Render(result *Result) string {
	if result == nil {
		return "No matches found"
	}
	var b strings.Builder
	if len(result.Matches) == 0 {
		b.WriteString("No matches found")
		if len(result.ParseErrors) > 0 {
			b.WriteString(". Parse issues mean the query may be mis-scoped; narrow `path` before concluding absence.")
			b.WriteString("\n\n")
			writeParseIssues(&b, result)
		}
		return strings.TrimRight(b.String(), "\n")
	}
	lastPath := ""
	for _, match := range result.Matches {
		if match.Path != lastPath {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(match.Path)
			b.WriteString(":\n")
			lastPath = match.Path
		}
		lines := strings.Split(match.Text, "\n")
		fmt.Fprintf(&b, "*%d:%s\n", match.Line, strings.TrimSuffix(lines[0], "\r"))
		for i := 1; i < len(lines); i++ {
			fmt.Fprintf(&b, " %d:%s\n", match.Line+i, strings.TrimSuffix(lines[i], "\r"))
		}
		if meta := metaLine(match.Captures); meta != "" {
			fmt.Fprintf(&b, "  meta: %s\n", meta)
		}
	}
	if result.LimitReached {
		b.WriteString("\nResult limit reached; narrow path or use skip to page through results.\n")
	}
	if len(result.ParseErrors) > 0 {
		b.WriteString("\n")
		writeParseIssues(&b, result)
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeParseIssues(b *strings.Builder, result *Result) {
	total := result.ParseErrorsTotal
	if total == 0 {
		total = len(result.ParseErrors)
	}
	switch {
	case total > len(result.ParseErrors):
		fmt.Fprintf(b, "Parse issues (%d of %d):\n", len(result.ParseErrors), total)
	default:
		fmt.Fprintf(b, "Parse issues (%d):\n", total)
	}
	for _, issue := range result.ParseErrors {
		if issue.Line > 0 {
			fmt.Fprintf(b, "  %s:%d: %s\n", issue.Path, issue.Line, issue.Message)
			continue
		}
		fmt.Fprintf(b, "  %s: %s\n", issue.Path, issue.Message)
	}
}

func metaLine(captures []Capture) string {
	parts := make([]string, 0, len(captures))
	for _, capture := range captures {
		if capture.Name == "" {
			continue
		}
		parts = append(parts, capture.Name+"="+normalizeMeta(capture.Text))
	}
	return strings.Join(parts, ", ")
}

func normalizeMeta(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return `""`
	}
	runes := []rune(text)
	if len(runes) > 80 {
		text = string(runes[:77]) + "..."
	}
	return text
}

// Details returns the JSON-friendly metadata contract. It intentionally does
// not include full match payloads: the model reads text, hosts read counts.
func Details(result *Result) map[string]any {
	if result == nil {
		return map[string]any{
			"matchCount":       0,
			"fileCount":        0,
			"filesSearched":    0,
			"limitReached":     false,
			"parseErrors":      []string{},
			"parseErrorsTotal": 0,
			"scopePath":        ".",
			"searchPath":       ".",
		}
	}
	issues := make([]string, 0, len(result.ParseErrors))
	for _, issue := range result.ParseErrors {
		if issue.Line > 0 {
			issues = append(issues, fmt.Sprintf("%s:%d: %s", issue.Path, issue.Line, issue.Message))
			continue
		}
		issues = append(issues, issue.Path+": "+issue.Message)
	}
	return map[string]any{
		"matchCount":       len(result.Matches),
		"fileCount":        result.FileCount,
		"filesSearched":    result.FilesSearched,
		"limitReached":     result.LimitReached,
		"parseErrors":      issues,
		"parseErrorsTotal": result.ParseErrorsTotal,
		"scopePath":        result.ScopePath,
		"searchPath":       result.SearchPath,
	}
}
