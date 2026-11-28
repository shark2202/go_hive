package lsp

import (
	"testing"
	"unicode/utf8"
)

// Parity-exempt: UTF-16 position mapping is a Go-only LSP requirement
// (docs/lsp-design.json R4/A6) with no Python source counterpart.
func TestTextIndexUTF16Matrix(t *testing.T) {
	text := "hello\n你好world\na😀b\n\tTabs\nx\r\n"
	ix := NewTextIndex(text)
	if ix.LineCount() != 6 {
		t.Fatalf("line count = %d, want 6", ix.LineCount())
	}
	assertLine := func(line int, want string) {
		t.Helper()
		got, ok := ix.LineText(line)
		if !ok || got != want {
			t.Fatalf("line %d = %q (ok=%v), want %q", line, got, ok, want)
		}
	}
	assertLine(0, "hello")
	assertLine(1, "你好world")
	assertLine(2, "a😀b")
	assertLine(3, "\tTabs")
	assertLine(4, "x")

	// CJK: one UTF-16 unit and three UTF-8 bytes per rune.
	byteOffset, ok := ix.ByteOffset(1, 2)
	if !ok || byteOffset != 6 {
		t.Fatalf("CJK byte offset = %d (ok=%v), want 6", byteOffset, ok)
	}
	column, _ := ix.UTF16Column(1, 6)
	if column != 2 {
		t.Fatalf("CJK utf16 column = %d, want 2", column)
	}
	if got := ix.DisplayColumn(1, 2); got != 3 {
		t.Fatalf("CJK display column = %d, want 3", got)
	}

	// Emoji: two UTF-16 units for one four-byte rune. A column in the middle
	// of the surrogate pair maps to the rune start by policy.
	cases := []struct {
		col        int
		byteOffset int
	}{
		{0, 0},
		{1, 1},
		{2, 1}, // inside the surrogate pair
		{3, 5}, // after the emoji
	}
	for _, tc := range cases {
		got, ok := ix.ByteOffset(2, tc.col)
		if !ok || got != tc.byteOffset {
			t.Fatalf("emoji col %d -> byte %d (ok=%v), want %d", tc.col, got, ok, tc.byteOffset)
		}
	}
	if got := ix.DisplayColumn(2, 3); got != 3 {
		t.Fatalf("emoji display column = %d, want 3", got)
	}
	if got, _ := ix.UTF16Column(2, 5); got != 3 {
		t.Fatalf("emoji utf16 after rune = %d, want 3", got)
	}

	// Tabs count as one UTF-16 unit.
	if got, _ := ix.UTF16Column(3, 1); got != 1 {
		t.Fatalf("tab utf16 = %d, want 1", got)
	}

	// Round-trip every rune boundary on every line.
	for line := 0; line < ix.LineCount(); line++ {
		lineText, _ := ix.LineText(line)
		for offset := 0; offset <= len(lineText); {
			col, ok := ix.UTF16Column(line, offset)
			if !ok {
				t.Fatalf("UTF16Column(%d,%d) failed", line, offset)
			}
			back, ok := ix.ByteOffset(line, col)
			if !ok || back != offset {
				t.Fatalf("round trip line %d offset %d -> col %d -> %d (ok=%v)", line, offset, col, back, ok)
			}
			if offset == len(lineText) {
				break
			}
			_, size := utf8.DecodeRuneInString(lineText[offset:])
			offset += size
		}
	}
}

// Parity-exempt: symbol occurrence selection name/#N is frozen by
// docs/lsp-design.json R4/D5; no upstream test.
func TestResolvePositionSymbolSelectors(t *testing.T) {
	text := "package main\n\nfunc Foo() int { return 1 }\n\nfunc main() { Foo(); Foo(); foo() }\n"
	pos, column, err := ResolvePosition(text, PositionQuery{Line: 5, Symbol: "Foo"})
	if err != nil {
		t.Fatalf("Foo: %v", err)
	}
	if pos.Line != 4 || pos.Character != 14 {
		t.Fatalf("Foo pos = %+v, want line 4 char 14", pos)
	}
	if column != 15 {
		t.Fatalf("Foo rune column = %d, want 15", column)
	}

	pos, _, err = ResolvePosition(text, PositionQuery{Line: 5, Symbol: "Foo#2"})
	if err != nil {
		t.Fatalf("Foo#2: %v", err)
	}
	if pos.Character != 21 {
		t.Fatalf("Foo#2 char = %d, want 21", pos.Character)
	}

	// exact lowercase match wins before case-insensitive matching.
	pos, _, err = ResolvePosition(text, PositionQuery{Line: 5, Symbol: "foo"})
	if err != nil {
		t.Fatalf("foo: %v", err)
	}
	if pos.Character != 28 {
		t.Fatalf("foo char = %d, want 28", pos.Character)
	}

	// No exact match, so case-insensitive sees all three Foo/foo occurrences.
	if _, _, err := ResolvePosition(text, PositionQuery{Line: 5, Symbol: "FOO#3"}); err != nil {
		t.Fatalf("FOO#3: %v", err)
	}
	if _, _, err := ResolvePosition(text, PositionQuery{Line: 5, Symbol: "FOO#4"}); err == nil {
		t.Fatal("out-of-range occurrence was accepted")
	}
	if _, _, err := ResolvePosition(text, PositionQuery{Line: 5, Symbol: "Fo"}); err == nil {
		t.Fatal("partial identifier was accepted")
	}
	if _, _, err := ResolvePosition(text, PositionQuery{Line: 5, Symbol: "Missing"}); err == nil {
		t.Fatal("missing symbol was accepted")
	}
}

// Parity-exempt: CJK/emoji column resolution is the review-focus case in
// docs/lsp-design.json (CJK/emoji and CRLF in position mapping).
func TestResolvePositionCJKEmojiAndComments(t *testing.T) {
	text := "// 你好😀\n 你好😀 Foo\n"
	pos, column, err := ResolvePosition(text, PositionQuery{Line: 2, Symbol: "Foo"})
	if err != nil {
		t.Fatalf("Foo: %v", err)
	}
	if pos.Character != 6 {
		t.Fatalf("utf16 column = %d, want 6", pos.Character)
	}
	if column != 6 {
		t.Fatalf("rune column = %d, want 6", column)
	}
	pos, _, err = ResolvePosition(text, PositionQuery{Line: 2, Column: 6})
	if err != nil {
		t.Fatalf("column: %v", err)
	}
	if pos.Character != 6 {
		t.Fatalf("column position = %+v", pos)
	}
}

// Parity-exempt: line/column rejection is a new positional-input guard (A6);
// no Python counterpart.
func TestResolvePositionRejectsBadLineAndColumn(t *testing.T) {
	text := "one\ntwo\n"
	for _, query := range []PositionQuery{
		{Line: 0, Column: 1},
		{Line: 4, Column: 1},
		{Line: 1, Column: 99},
	} {
		if _, _, err := ResolvePosition(text, query); err == nil {
			t.Fatalf("query %+v was accepted", query)
		}
	}
	_, _, err := ResolvePosition(text, PositionQuery{Line: 1, Column: 1, Symbol: "one"})
	if err != nil {
		t.Fatalf("column+symbol should prefer symbol: %v", err)
	}
}

// Parity-exempt: CRLF handling is a GFM-independent LSP mapping case from A6.
func TestTextIndexCRLFEndsBeforeTerminator(t *testing.T) {
	ix := NewTextIndex("foo\r\nbar\r\n")
	line, _ := ix.LineText(0)
	if line != "foo" {
		t.Fatalf("line 0 = %q", line)
	}
	col, _ := ix.UTF16Column(0, 3)
	if col != 3 {
		t.Fatalf("col = %d, want 3", col)
	}
	byteOffset, _ := ix.ByteOffset(0, 3)
	if byteOffset != 3 {
		t.Fatalf("byte offset = %d, want 3", byteOffset)
	}
	if _, ok := ix.ByteOffset(0, 4); !ok {
		t.Fatal("past-end column should clamp")
	}
}

// Parity-exempt: no upstream analogue; guards the identifier-boundary helper.
func TestSymbolMatchesRespectsBoundaries(t *testing.T) {
	line := []rune("foobar foo _foo foo_bar")
	if matches := symbolMatches(line, "foo", true); len(matches) != 1 {
		t.Fatalf("matches = %v, want 1", matches)
	}
}
