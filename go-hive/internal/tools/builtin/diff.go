package builtin

// Unified diff rendering for edit_file: Python difflib-compatible headers and
// hunk ranges, three context lines, truncated at 2000 characters the way
// file_ops.py's _compute_diff does.

import (
	"fmt"
	"strings"
)

// ── Unified diff ─────────────────────────────────────────────────────────

type diffLine struct {
	tag  byte // ' ', '-', '+'
	text string
}

// unifiedDiff renders a Python-compatible unified diff (3 context lines) and
// truncates it at 2000 characters, like file_ops.py's _compute_diff.
func unifiedDiff(oldText, newText, path string) string {
	if oldText == newText {
		return ""
	}
	oldLines := splitKeepEnds(oldText)
	newLines := splitKeepEnds(newText)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	oldMiddle := oldLines[prefix : len(oldLines)-suffix]
	newMiddle := newLines[prefix : len(newLines)-suffix]

	ops := make([]diffLine, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines[:prefix] {
		ops = append(ops, diffLine{tag: ' ', text: line})
	}
	ops = append(ops, lcsDiff(oldMiddle, newMiddle)...)
	for _, line := range oldLines[len(oldLines)-suffix:] {
		ops = append(ops, diffLine{tag: ' ', text: line})
	}

	const contextLines = 3
	var changes []int
	for i, op := range ops {
		if op.tag != ' ' {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return ""
	}
	type span struct{ start, end int }
	var spans []span
	start, end := changes[0], changes[0]
	for _, idx := range changes[1:] {
		if idx-end <= 2*contextLines {
			end = idx
			continue
		}
		spans = append(spans, span{start: start, end: end})
		start, end = idx, idx
	}
	spans = append(spans, span{start: start, end: end})

	// prefixOld[i] is the number of old-side lines consumed by ops[:i].
	prefixOld := make([]int, len(ops)+1)
	for i, op := range ops {
		prefixOld[i+1] = prefixOld[i]
		if op.tag == ' ' || op.tag == '-' {
			prefixOld[i+1]++
		}
	}

	var b strings.Builder
	b.WriteString("--- " + path + "\n")
	b.WriteString("+++ " + path + "\n")
	for _, sp := range spans {
		hunkStart := max(0, sp.start-contextLines)
		hunkEnd := min(len(ops), sp.end+contextLines+1)
		oldCount, newCount := 0, 0
		for _, op := range ops[hunkStart:hunkEnd] {
			if op.tag == ' ' || op.tag == '-' {
				oldCount++
			}
			if op.tag == ' ' || op.tag == '+' {
				newCount++
			}
		}
		oldStart := prefixOld[hunkStart]
		newStart := 0
		for _, op := range ops[:hunkStart] {
			if op.tag == ' ' || op.tag == '+' {
				newStart++
			}
		}
		b.WriteString("@@ -" + formatDiffRange(oldStart, oldCount) + " +" + formatDiffRange(newStart, newCount) + " @@\n")
		for _, op := range ops[hunkStart:hunkEnd] {
			b.WriteByte(op.tag)
			b.WriteString(op.text)
		}
	}

	result := b.String()
	if runes := []rune(result); len(runes) > 2000 {
		result = string(runes[:2000]) + "\n... (diff truncated)"
	}
	return result
}

// formatDiffRange matches difflib._format_range_unified.
func formatDiffRange(start, length int) string {
	beginning := start + 1
	switch length {
	case 1:
		return fmt.Sprintf("%d", beginning)
	case 0:
		return fmt.Sprintf("%d,0", beginning-1)
	default:
		return fmt.Sprintf("%d,%d", beginning, length)
	}
}

// lcsDiff produces a minimal line diff, falling back to whole-block
// replacement when the quadratic table would be too large.
func lcsDiff(oldLines, newLines []string) []diffLine {
	n, m := len(oldLines), len(newLines)
	if n == 0 && m == 0 {
		return nil
	}
	if n*m > 1_000_000 {
		ops := make([]diffLine, 0, n+m)
		for _, line := range oldLines {
			ops = append(ops, diffLine{tag: '-', text: line})
		}
		for _, line := range newLines {
			ops = append(ops, diffLine{tag: '+', text: line})
		}
		return ops
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	ops := make([]diffLine, 0, n+m)
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			ops = append(ops, diffLine{tag: ' ', text: oldLines[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffLine{tag: '-', text: oldLines[i]})
			i++
		default:
			ops = append(ops, diffLine{tag: '+', text: newLines[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffLine{tag: '-', text: oldLines[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffLine{tag: '+', text: newLines[j]})
	}
	return ops
}

// splitKeepEnds splits s into lines that keep their trailing "\n".
func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for len(s) > 0 {
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:idx+1])
		s = s[idx+1:]
	}
	return out
}
