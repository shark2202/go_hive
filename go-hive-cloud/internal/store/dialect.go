package store

import (
	"strings"
	"unicode"
)

// This file is the beginning of the SQLite dialect layer. The desktop proxy's
// grid_query module (core/framework/global_db/grid_query.py) emits Postgres
// SQL; SQLite accepts most of it, but not ILIKE and a few literals. Translate
// is deliberately token-aware: it never rewrites text inside string literals,
// quoted identifiers or comments.
//
// Rules implemented here (see docs/m1-notes.md for the rationale + limits):
//   - ILIKE  -> LIKE   (SQLite LIKE is ASCII-case-insensitive by default)
//   - TRUE/FALSE -> 1/0
//   - NOW() -> CURRENT_TIMESTAMP
//   - ::text / ::varchar / ::uuid / ::json / ::jsonb / ::timestamptz -> removed
//     (SQLite has no static types; these casts are no-ops for our tables)
//
// Everything else passes through untouched. Unsupported Postgres constructs
// (e.g. ::int casts, window functions outside SQLite's support) surface as a
// 400 from the endpoint rather than being silently guessed at.

// Translate rewrites Postgres-flavoured SQL into SQLite-compatible SQL.
func Translate(sqlText string) string {
	segments := tokenizeSQL(sqlText)
	var b strings.Builder
	b.Grow(len(sqlText))
	for i := 0; i < len(segments); i++ {
		seg := segments[i]
		switch seg.kind {
		case segWord:
			lower := strings.ToLower(seg.text)
			switch lower {
			case "ilike":
				b.WriteString("LIKE")
				continue
			case "true":
				b.WriteString("1")
				continue
			case "false":
				b.WriteString("0")
				continue
			case "now":
				if nextPunct(segments, i) == "(" {
					// NOW() -> CURRENT_TIMESTAMP (the empty call must go too).
					b.WriteString("CURRENT_TIMESTAMP")
					consumed := consumeEmptyParens(segments, i+1)
					i += consumed
					continue
				}
			}
		case segPunct:
			if seg.text == ":" && i+1 < len(segments) && segments[i+1].kind == segPunct && segments[i+1].text == ":" {
				// Postgres cast `expr::type` — drop known no-op casts.
				typeName, consumed := castTypeAfter(segments, i+2)
				if typeName != "" && noOpCast(typeName) {
					i += 1 + consumed
					continue
				}
			}
		}
		b.WriteString(seg.text)
	}
	return b.String()
}

// castTypeAfter reads the type word after `::` and returns how many segments it
// consumed (type word plus an optional `(n)` length specification).
func castTypeAfter(segments []segment, idx int) (string, int) {
	skip := 0
	for idx+skip < len(segments) && segments[idx+skip].kind == segSpace {
		skip++
	}
	if idx+skip >= len(segments) || segments[idx+skip].kind != segWord {
		return "", 0
	}
	name := strings.ToLower(segments[idx+skip].text)
	consumed := skip + 1
	if idx+consumed < len(segments) && segments[idx+consumed].kind == segPunct && segments[idx+consumed].text == "(" {
		depth := 0
		for j := idx + consumed; j < len(segments); j++ {
			if segments[j].kind != segPunct {
				continue
			}
			if segments[j].text == "(" {
				depth++
			} else if segments[j].text == ")" {
				depth--
				if depth == 0 {
					consumed = j - idx + 1
					break
				}
			}
		}
	}
	return name, consumed
}

func noOpCast(name string) bool {
	switch name {
	case "text", "varchar", "char", "uuid", "json", "jsonb", "citext", "timestamptz", "timestamp":
		return true
	}
	return false
}

func nextPunct(segments []segment, idx int) string {
	for j := idx + 1; j < len(segments); j++ {
		switch segments[j].kind {
		case segSpace, segComment:
			continue
		case segPunct:
			return segments[j].text
		default:
			return ""
		}
	}
	return ""
}

// consumeEmptyParens returns how many segments make up an empty "()" pair
// starting at or after idx (whitespace/comments between the parens allowed).
func consumeEmptyParens(segments []segment, idx int) int {
	j := idx
	for j < len(segments) && (segments[j].kind == segSpace || segments[j].kind == segComment) {
		j++
	}
	if j >= len(segments) || segments[j].kind != segPunct || segments[j].text != "(" {
		return 0
	}
	consumed := j - idx + 1
	j++
	for j < len(segments) && (segments[j].kind == segSpace || segments[j].kind == segComment) {
		j++
	}
	if j < len(segments) && segments[j].kind == segPunct && segments[j].text == ")" {
		return j - idx + 1
	}
	return consumed
}

type segKind int

const (
	segSpace segKind = iota
	segWord
	segString
	segQuotedIdent
	segComment
	segPunct
)

type segment struct {
	text string
	kind segKind
}

func isWordStart(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isWordPart(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || r == '$'
}

// tokenizeSQL splits SQL into spaces, words, string literals, quoted
// identifiers, comments and single-rune punctuation. Adjacent spaces collapse
// into one segment, which keeps copies byte-identical.
func tokenizeSQL(s string) []segment {
	runes := []rune(s)
	out := make([]segment, 0, len(runes)/2+1)
	flushSpace := func(from, to int) {
		if to > from {
			out = append(out, segment{text: string(runes[from:to]), kind: segSpace})
		}
	}
	i := 0
	spaceStart := -1
	for i < len(runes) {
		r := runes[i]
		if unicode.IsSpace(r) {
			if spaceStart < 0 {
				spaceStart = i
			}
			i++
			continue
		}
		if spaceStart >= 0 {
			flushSpace(spaceStart, i)
			spaceStart = -1
		}
		switch {
		case r == '-' && i+1 < len(runes) && runes[i+1] == '-':
			j := i
			for j < len(runes) && runes[j] != '\n' {
				j++
			}
			out = append(out, segment{text: string(runes[i:j]), kind: segComment})
			i = j
		case r == '/' && i+1 < len(runes) && runes[i+1] == '*':
			j := i + 2
			for j < len(runes) {
				if runes[j] == '*' && j+1 < len(runes) && runes[j+1] == '/' {
					j += 2
					break
				}
				j++
			}
			out = append(out, segment{text: string(runes[i:j]), kind: segComment})
			i = j
		case r == '\'':
			j := i + 1
			for j < len(runes) {
				if runes[j] == '\'' {
					if j+1 < len(runes) && runes[j+1] == '\'' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			out = append(out, segment{text: string(runes[i:j]), kind: segString})
			i = j
		case r == '"':
			j := i + 1
			for j < len(runes) {
				if runes[j] == '"' {
					if j+1 < len(runes) && runes[j+1] == '"' {
						j += 2
						continue
					}
					j++
					break
				}
				j++
			}
			out = append(out, segment{text: string(runes[i:j]), kind: segQuotedIdent})
			i = j
		case isWordStart(r):
			j := i + 1
			for j < len(runes) && isWordPart(runes[j]) {
				j++
			}
			out = append(out, segment{text: string(runes[i:j]), kind: segWord})
			i = j
		default:
			out = append(out, segment{text: string(r), kind: segPunct})
			i++
		}
	}
	if spaceStart >= 0 {
		flushSpace(spaceStart, len(runes))
	}
	return out
}
