// Package arghelp renders command help pages in argparse's layout.
//
// The Python side of the parity contract (core/framework/cli.py and siblings,
// tools/src/gcu/cli.py) renders help with argparse.HelpFormatter. To keep the
// two CLIs' help surfaces readable side by side — same wrapping width, same
// label column, same section names — this package ports that formatter:
//
//   - terminal width comes from COLUMNS, else the stdout tty, else 80, and the
//     usable width is that minus 2 (argparse's shell-wrap compensation);
//   - the usage line wraps over indented continuation lines;
//   - the label column is helpPos = min(action_max_length + 2, min(24, ...)),
//     not a hard-coded 22, so short option blocks stay tight;
//   - help text wraps with continuation lines at helpPos;
//   - subcommands are rendered the way argparse renders a subparsers action:
//     a `{a,b,c}` entry in `positional arguments` followed by one indented
//     line per choice.
package arghelp

import (
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	indentIncrement = 2
	maxHelpPosition = 24
	// defaultTerminalWidth mirrors the (80, 24) fallback of
	// shutil.get_terminal_size(), which argparse uses when COLUMNS is unset
	// and stdout is not a tty.
	defaultTerminalWidth = 80
)

// Option is one `options:` entry. Label is rendered verbatim (`--json`,
// `-h, --help`, `--coordinate x,y`, ...); an empty Help renders a bare label
// line, exactly like argparse.
type Option struct {
	Label string
	Help  string
}

// Positional is one `positional arguments:` entry (argv slot, not a choice).
type Positional struct {
	Label string
	Help  string
}

// Choice is one subcommand line under the `{a,b,c}` entry.
type Choice struct {
	Name string
	Help string
}

// Spec is everything one help page needs. Usage parts are split the way
// argparse's `_format_usage` splits them: bracketed groups stay together
// (`[--wait-until {commit,load}]`), while an unbracketed `--action {a,b}`
// yields two parts (`--action`, `{a,b}`) that may wrap onto separate lines.
type Spec struct {
	Prog             string
	UsageOptionals   []string
	UsagePositionals []string
	UsageParts       []string // fallback used only when the two lists above are nil
	Description      string
	Epilog           string
	// RawDescription mirrors argparse.RawDescriptionHelpFormatter: the
	// description (and epilog) keep their line breaks instead of wrapping.
	RawDescription bool
	// NoUsage suppresses the usage block (used by tests).
	NoUsage     bool
	Options     []Option
	Positionals []Positional
	Choices     []Choice
}

// Text renders the page at the detected terminal width.
func (s Spec) Text() string {
	return s.TextAt(TerminalWidth())
}

// UsageText renders only the usage block, with argparse's wrapping. Callers
// print it ahead of an error line (e.g. a missing required positional).
func (s Spec) UsageText() string {
	termWidth := TerminalWidth()
	if termWidth <= 2 {
		termWidth = defaultTerminalWidth
	}
	return strings.TrimRight(usageBlock(s, termWidth-2), "\n")
}

// TextAt renders the page for a terminal that is termWidth columns wide. The
// effective formatting width is termWidth-2, as in argparse.
func (s Spec) TextAt(termWidth int) string {
	if termWidth <= 2 {
		termWidth = defaultTerminalWidth
	}
	width := termWidth - 2
	limitHelp := maxInt(width-20, indentIncrement*2)
	if limitHelp > maxHelpPosition {
		limitHelp = maxHelpPosition
	}

	// action_max_length: every rendered invocation plus its indent; subcommand
	// choices are formatted one indent level deeper, exactly like argparse's
	// _iter_indented_subactions.
	actionMax := 0
	for _, option := range s.Options {
		actionMax = maxInt(actionMax, runeLen(option.Label)+indentIncrement)
	}
	for _, positional := range s.Positionals {
		actionMax = maxInt(actionMax, runeLen(positional.Label)+indentIncrement)
	}
	if len(s.Choices) > 0 {
		actionMax = maxInt(actionMax, runeLen(choiceSet(s.Choices))+indentIncrement)
		for _, choice := range s.Choices {
			actionMax = maxInt(actionMax, runeLen(choice.Name)+2*indentIncrement)
		}
	}
	helpPos := actionMax + 2
	if helpPos > limitHelp {
		helpPos = limitHelp
	}

	var out strings.Builder
	if !s.NoUsage {
		out.WriteString(usageBlock(s, width))
	}
	if s.Description != "" {
		out.WriteString(textBlock(s.Description, width, 0, s.RawDescription))
	}
	if len(s.Positionals) > 0 || len(s.Choices) > 0 {
		lines := []string{}
		for _, positional := range s.Positionals {
			lines = append(lines, formatAction(indentIncrement, helpPos, width, positional.Label, positional.Help)...)
		}
		if len(s.Choices) > 0 {
			lines = append(lines, formatAction(indentIncrement, helpPos, width, choiceSet(s.Choices), "")...)
			for _, choice := range s.Choices {
				lines = append(lines, formatAction(2*indentIncrement, helpPos, width, choice.Name, choice.Help)...)
			}
		}
		out.WriteString(section("positional arguments", lines, width))
	}
	if len(s.Options) > 0 {
		lines := []string{}
		for _, option := range s.Options {
			lines = append(lines, formatAction(indentIncrement, helpPos, width, option.Label, option.Help)...)
		}
		out.WriteString(section("options", lines, width))
	}
	if s.Epilog != "" {
		out.WriteString(textBlock(s.Epilog, width, 0, s.RawDescription))
	}
	return normalize(out.String())
}

// section renders `heading:` plus its action lines, argparse-style: the block
// is bracketed by blank lines (the heading is preceded by one, and the section
// itself ends with another — the normalize() pass squashes any triple).
func section(heading string, lines []string, width int) string {
	var out strings.Builder
	out.WriteString("\n")
	out.WriteString(heading)
	out.WriteString(":\n")
	for _, line := range lines {
		out.WriteString(line)
		out.WriteString("\n")
	}
	out.WriteString("\n")
	return out.String()
}

// usageBlock ports _format_usage: single line when it fits, else wrapped with
// continuation lines indented under the prog (or under `usage: ` when the prog
// alone is too long for the short-indent branch).
func usageBlock(s Spec, width int) string {
	const prefix = "usage: "
	optionals, positionals := s.usageParts()
	optUsage := strings.Join(optionals, " ")
	posUsage := strings.Join(positionals, " ")
	usage := strings.TrimSpace(strings.Join(nonEmpty([]string{s.Prog, optUsage, posUsage}), " "))
	if runeLen(prefix)+runeLen(usage) <= width {
		return prefix + usage + "\n\n"
	}

	textWidth := width
	optParts := splitUsageParts(optUsage)
	posParts := splitUsageParts(posUsage)

	var lines []string
	if runeLen(prefix)+runeLen(s.Prog) <= int(0.75*float64(textWidth)) {
		indent := strings.Repeat(" ", runeLen(prefix)+runeLen(s.Prog)+1)
		if len(optParts) > 0 {
			lines = getLines(append([]string{s.Prog}, optParts...), indent, prefix, textWidth)
			lines = append(lines, getLines(posParts, indent, "", textWidth)...)
		} else if len(posParts) > 0 {
			lines = getLines(append([]string{s.Prog}, posParts...), indent, prefix, textWidth)
		} else {
			lines = []string{s.Prog}
		}
	} else {
		indent := strings.Repeat(" ", runeLen(prefix))
		lines = getLines(append(append([]string{}, optParts...), posParts...), indent, "", textWidth)
		if len(lines) > 1 {
			lines = getLines(optParts, indent, "", textWidth)
			lines = append(lines, getLines(posParts, indent, "", textWidth)...)
		}
		lines = append([]string{s.Prog}, lines...)
	}
	return prefix + strings.Join(lines, "\n") + "\n\n"
}

func (s Spec) usageParts() ([]string, []string) {
	if s.UsageOptionals == nil && s.UsagePositionals == nil {
		return nil, splitUsageParts(strings.Join(s.UsageParts, " "))
	}
	return s.UsageOptionals, s.UsagePositionals
}

// getLines ports the nested get_lines() helper of _format_usage.
func getLines(parts []string, indent, prefix string, textWidth int) []string {
	var lines []string
	var line []string
	lineLen := runeLen(indent) - 1
	if prefix != "" {
		lineLen = runeLen(prefix) - 1
	}
	for _, part := range parts {
		if lineLen+1+runeLen(part) > textWidth && len(line) > 0 {
			lines = append(lines, indent+strings.Join(line, " "))
			line = nil
			lineLen = runeLen(indent) - 1
		}
		line = append(line, part)
		lineLen += runeLen(part) + 1
	}
	if len(line) > 0 {
		lines = append(lines, indent+strings.Join(line, " "))
	}
	if prefix != "" && len(lines) > 0 {
		lines[0] = lines[0][runeLen(indent):]
	}
	return lines
}

// formatAction ports HelpFormatter._format_action for one entry.
func formatAction(indent, helpPos, width int, header, help string) []string {
	actionWidth := helpPos - indent - 2
	lines := []string{}
	if help == "" {
		return append(lines, strings.Repeat(" ", indent)+header)
	}
	helpWidth := width - helpPos
	if helpWidth < 11 {
		helpWidth = 11
	}
	helpLines := wrapText(help, helpWidth)
	if len(helpLines) == 0 {
		return append(lines, strings.Repeat(" ", indent)+header)
	}
	if runeLen(header) <= actionWidth {
		first := strings.Repeat(" ", indent) + header + strings.Repeat(" ", actionWidth-runeLen(header)+2) + helpLines[0]
		lines = append(lines, first)
	} else {
		lines = append(lines, strings.Repeat(" ", indent)+header)
		lines = append(lines, strings.Repeat(" ", helpPos)+helpLines[0])
	}
	for _, continuation := range helpLines[1:] {
		lines = append(lines, strings.Repeat(" ", helpPos)+continuation)
	}
	return lines
}

// textBlock renders a description/epilog paragraph. Raw text keeps its lines;
// otherwise it is collapsed and wrapped at width.
func textBlock(text string, width, indent int, raw bool) string {
	if raw {
		var out strings.Builder
		for _, line := range strings.Split(text, "\n") {
			out.WriteString(strings.Repeat(" ", indent))
			out.WriteString(line)
			out.WriteString("\n")
		}
		return out.String() + "\n"
	}
	wrapped := wrapText(collapse(text), width-indent)
	if len(wrapped) == 0 {
		return ""
	}
	var out strings.Builder
	for _, line := range wrapped {
		out.WriteString(strings.Repeat(" ", indent))
		out.WriteString(line)
		out.WriteString("\n")
	}
	return out.String() + "\n"
}

// wrapText is textwrap.wrap(text, width) for text whose whitespace is already
// collapsed: greedy fill, breaking over-long words like break_long_words does.
func wrapText(text string, width int) []string {
	if width < 1 {
		width = 1
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	current := ""
	for _, word := range words {
		if current == "" {
			for runeLen(word) > width {
				lines = append(lines, cutRunes(word, width))
				word = word[runeCutBytes(word, width):]
			}
			current = word
			continue
		}
		if runeLen(current)+1+runeLen(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = ""
		for runeLen(word) > width {
			lines = append(lines, cutRunes(word, width))
			word = word[runeCutBytes(word, width):]
		}
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func cutRunes(text string, n int) string {
	count := 0
	for index := range text {
		if count == n {
			return text[:index]
		}
		count++
	}
	return text
}

func runeCutBytes(text string, n int) int {
	count := 0
	for index := range text {
		if count == n {
			return index
		}
		count++
	}
	return len(text)
}

// splitUsageParts ports argparse's part_regexp
// `\(.*?\)+(?=\s|$)|\[.*?\]+(?=\s|$)|\S+` for usage strings this package
// generates (no nested brackets): bracketed runs stay one part, everything
// else splits on whitespace.
func splitUsageParts(text string) []string {
	var parts []string
	for index := 0; index < len(text); {
		switch text[index] {
		case ' ', '\t':
			index++
		case '[':
			end := strings.IndexByte(text[index:], ']')
			if end < 0 {
				parts = append(parts, text[index:])
				return parts
			}
			// Consume any immediate extra `]` runs the way `\]+` does.
			stop := index + end + 1
			for stop < len(text) && text[stop] == ']' {
				stop++
			}
			parts = append(parts, text[index:stop])
			index = stop
		case '(':
			end := strings.IndexByte(text[index:], ')')
			if end < 0 {
				parts = append(parts, text[index:])
				return parts
			}
			stop := index + end + 1
			parts = append(parts, text[index:stop])
			index = stop
		default:
			stop := index
			for stop < len(text) && text[stop] != ' ' && text[stop] != '\t' {
				stop++
			}
			parts = append(parts, text[index:stop])
			index = stop
		}
	}
	return parts
}

// normalize collapses runs of 3+ newlines into a blank line and trims the
// leading/trailing newlines, then ends with exactly one — the same shape
// HelpFormatter.format_help produces.
func normalize(text string) string {
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}
	text = strings.Trim(text, "\n")
	if text == "" {
		return ""
	}
	return strings.ReplaceAll(text, "\r\n", "\n") + "\n"
}

func choiceSet(choices []Choice) string {
	names := make([]string, 0, len(choices))
	for _, choice := range choices {
		names = append(names, choice.Name)
	}
	return "{" + strings.Join(names, ",") + "}"
}

// collapse mirrors the `re.sub(r'\s+', ' ', text)` normalization argparse
// applies before wrapping.
func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func nonEmpty(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func runeLen(text string) int { return utf8.RuneCountInString(text) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// TerminalWidth returns the width argparse would use: COLUMNS when set to a
// positive integer, else the stdout tty size, else 80.
func TerminalWidth() int {
	if columns := strings.TrimSpace(os.Getenv("COLUMNS")); columns != "" {
		if value, err := strconv.Atoi(columns); err == nil && value > 0 {
			return value
		}
	}
	if width := terminalColumns(); width > 0 {
		return width
	}
	return defaultTerminalWidth
}

// Format renders a spec at an explicit terminal width; it exists so callers
// (and tests) can pin the layout without touching COLUMNS.
func (s Spec) Format(termWidth int) string { return s.TextAt(termWidth) }
