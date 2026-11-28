package lsp

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TextIndex maps one document's lines between Go byte offsets, rune columns
// and LSP UTF-16 columns. Every tool position passes through it; byte offsets
// are never used as LSP characters.
type TextIndex struct {
	text       string
	lineStarts []int
	lineEnds   []int
}

// NewTextIndex indexes text. Lines are separated by '\n'; a trailing '\r' is
// treated as part of the line terminator, not content.
func NewTextIndex(text string) *TextIndex {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	ends := make([]int, len(starts))
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1] - 1
		}
		if end > start && text[end-1] == '\r' {
			end--
		}
		ends[i] = end
	}
	return &TextIndex{text: text, lineStarts: starts, lineEnds: ends}
}

// LineCount returns the number of lines, including a final empty line after a
// trailing newline.
func (ix *TextIndex) LineCount() int { return len(ix.lineStarts) }

// LineText returns the line content without its terminator.
func (ix *TextIndex) LineText(line int) (string, bool) {
	start, end, ok := ix.lineBounds(line)
	if !ok {
		return "", false
	}
	return ix.text[start:end], true
}

func (ix *TextIndex) lineBounds(line int) (int, int, bool) {
	if line < 0 || line >= len(ix.lineStarts) {
		return 0, 0, false
	}
	return ix.lineStarts[line], ix.lineEnds[line], true
}

// ByteOffset converts a zero-based UTF-16 column to a byte offset relative to
// the start of line. Columns past the end clamp to the end of the line; a
// column inside a surrogate pair maps to the start of that rune.
func (ix *TextIndex) ByteOffset(line, utf16Column int) (int, bool) {
	start, end, ok := ix.lineBounds(line)
	if !ok {
		return 0, false
	}
	if utf16Column <= 0 {
		return 0, true
	}
	units := 0
	for i := start; i < end; {
		if units == utf16Column {
			return i - start, true
		}
		r, size := utf8.DecodeRuneInString(ix.text[i:end])
		if r == utf8.RuneError && size == 1 {
			size = 1
			units++
			i += size
			continue
		}
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > utf16Column {
			return i - start, true
		}
		units += width
		i += size
	}
	return end - start, true
}

// UTF16Column converts a byte offset relative to the start of line to a UTF-16
// column.
func (ix *TextIndex) UTF16Column(line, byteOffset int) (int, bool) {
	start, end, ok := ix.lineBounds(line)
	if !ok {
		return 0, false
	}
	if byteOffset < 0 {
		byteOffset = 0
	}
	if start+byteOffset > end {
		byteOffset = end - start
	}
	units := 0
	for i := start; i < start+byteOffset; {
		r, size := utf8.DecodeRuneInString(ix.text[i:end])
		if r == utf8.RuneError && size == 1 {
			units++
			i++
			continue
		}
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
		i += size
	}
	return units, true
}

// RuneColumn converts a byte offset relative to the start of line to a
// one-based display column.
func (ix *TextIndex) RuneColumn(line, byteOffset int) (int, bool) {
	start, end, ok := ix.lineBounds(line)
	if !ok {
		return 0, false
	}
	if byteOffset < 0 {
		byteOffset = 0
	}
	if start+byteOffset > end {
		byteOffset = end - start
	}
	return utf8.RuneCountInString(ix.text[start:start+byteOffset]) + 1, true
}

// DisplayColumn converts a UTF-16 column to a one-based rune column for
// human-facing output.
func (ix *TextIndex) DisplayColumn(line, utf16Column int) int {
	byteOffset, ok := ix.ByteOffset(line, utf16Column)
	if !ok {
		return 1
	}
	col, ok := ix.RuneColumn(line, byteOffset)
	if !ok {
		return 1
	}
	return col
}

// UTF16ColumnForRune converts a one-based rune column to a UTF-16 column.
func (ix *TextIndex) UTF16ColumnForRune(line, runeColumn int) (int, bool) {
	text, ok := ix.LineText(line)
	if !ok {
		return 0, false
	}
	if runeColumn < 1 {
		runeColumn = 1
	}
	runes := []rune(text)
	if runeColumn > len(runes)+1 {
		runeColumn = len(runes) + 1
	}
	prefix := string(runes[:runeColumn-1])
	units := 0
	for _, r := range prefix {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units, true
}

// RuneColumnForUTF16 converts a zero-based UTF-16 column to a one-based rune
// column.
func (ix *TextIndex) RuneColumnForUTF16(line, utf16Column int) int {
	return ix.DisplayColumn(line, utf16Column)
}

// PositionQuery resolves one model-facing location to an LSP position.
type PositionQuery struct {
	Line   int    // 1-based line
	Column int    // 1-based rune column used when Symbol is empty
	Symbol string // name or name#N
}

// ResolvePosition returns the LSP position and one-based rune column for a
// query. Symbol lookup is line-local, case-sensitive first, then
// case-insensitive; "#N" selects the Nth occurrence.
func ResolvePosition(text string, q PositionQuery) (Position, int, error) {
	ix := NewTextIndex(text)
	if q.Line < 1 || q.Line > ix.LineCount() {
		return Position{}, 0, fmt.Errorf("line %d is outside the document (1..%d)", q.Line, ix.LineCount())
	}
	line := q.Line - 1
	if strings.TrimSpace(q.Symbol) == "" {
		column := q.Column
		if column < 1 {
			column = 1
		}
		lineText, _ := ix.LineText(line)
		if column > utf8.RuneCountInString(lineText)+1 {
			return Position{}, 0, fmt.Errorf("column %d is outside line %d", q.Column, q.Line)
		}
		utf16Col, _ := ix.UTF16ColumnForRune(line, column)
		return Position{Line: line, Character: utf16Col}, column, nil
	}
	lineText, _ := ix.LineText(line)
	base, occurrence := splitOccurrence(q.Symbol)
	runes := []rune(lineText)
	matches := symbolMatches(runes, base, true)
	if len(matches) == 0 {
		matches = symbolMatches(runes, base, false)
	}
	if len(matches) == 0 {
		return Position{}, 0, fmt.Errorf("symbol %q not found on line %d", base, q.Line)
	}
	if occurrence > len(matches) {
		return Position{}, 0, fmt.Errorf("symbol %q occurs %d time(s) on line %d; #%d is out of range", base, len(matches), q.Line, occurrence)
	}
	runeIndex := matches[occurrence-1]
	runeColumn := runeIndex + 1
	utf16Col, _ := ix.UTF16ColumnForRune(line, runeColumn)
	return Position{Line: line, Character: utf16Col}, runeColumn, nil
}

// splitOccurrence parses "name#N". A trailing #N is a selector only when N is
// a positive integer and the name is non-empty.
func splitOccurrence(symbol string) (string, int) {
	idx := strings.LastIndex(symbol, "#")
	if idx <= 0 || idx == len(symbol)-1 {
		return symbol, 1
	}
	suffix := symbol[idx+1:]
	n := 0
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return symbol, 1
		}
		n = n*10 + int(r-'0')
		if n > 100000 {
			return symbol, 1
		}
	}
	if n < 1 {
		return symbol, 1
	}
	return symbol[:idx], n
}

func isIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// symbolMatches returns rune indexes where base starts on an identifier
// boundary. exact selects case-sensitive matching.
func symbolMatches(line []rune, base string, exact bool) []int {
	baseRunes := []rune(base)
	if len(baseRunes) == 0 {
		return nil
	}
	var out []int
	for i := 0; i+len(baseRunes) <= len(line); i++ {
		if i > 0 && isIdentifierRune(line[i-1]) {
			continue
		}
		if i+len(baseRunes) < len(line) && isIdentifierRune(line[i+len(baseRunes)]) {
			continue
		}
		matched := true
		for j, r := range baseRunes {
			candidate := line[i+j]
			if exact {
				if candidate != r {
					matched = false
					break
				}
				continue
			}
			if !strings.EqualFold(string(candidate), string(r)) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, i)
		}
	}
	return out
}
