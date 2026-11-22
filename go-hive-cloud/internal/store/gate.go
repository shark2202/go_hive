package store

import (
	"fmt"
	"strings"
)

// GateError is a client-correctable rejection of a free-form SQL statement.
type GateError struct {
	Code    string
	Message string
}

func (e *GateError) Error() string { return e.Message }

const (
	GateEmpty       = "empty_statement"
	GateMultiple    = "multiple_statements"
	GateForbidden   = "forbidden_statement"
	GateInternal    = "internal_table"
	GateReadOnly    = "read_only"
	GateTooLarge    = "statement_too_large"
	GateBadEncoding = "bad_statement"
)

// MaxStatementBytes bounds request SQL size (mirrors the 8MB body cap with
// headroom for whitespace/quoting).
const MaxStatementBytes = 4 << 20

// forbiddenKeywords mirror the cloud's statement denylist. load_extension is
// handled separately because it is a function call, not a statement keyword.
var forbiddenKeywords = map[string]bool{
	"attach":  true,
	"detach":  true,
	"pragma":  true,
	"vacuum":  true,
	"reindex": true,
}

// CheckStatement validates a statement before execution.
//
//   - exactly one statement
//   - no ATTACH/DETACH/PRAGMA/VACUUM/REINDEX/load_extension
//   - no references to identifiers beginning with "_" (internal tables) or
//     "sqlite_" (engine internals)
//   - readOnly requires the statement to start with SELECT/WITH/VALUES/EXPLAIN
//
// The check runs on the raw text; translation happens afterwards.
func CheckStatement(sqlText string, readOnly bool) error {
	if len(sqlText) > MaxStatementBytes {
		return &GateError{Code: GateTooLarge, Message: fmt.Sprintf("statement exceeds %d bytes", MaxStatementBytes)}
	}
	segments := tokenizeSQL(sqlText)

	// Trim a trailing semicolon (and whitespace/comments after it) before
	// counting statements.
	end := len(segments)
	for end > 0 {
		seg := segments[end-1]
		if seg.kind == segSpace || seg.kind == segComment {
			end--
			continue
		}
		if seg.kind == segPunct && seg.text == ";" {
			end--
			continue
		}
		break
	}
	haveWord := false
	for i := 0; i < end; i++ {
		seg := segments[i]
		if seg.kind == segWord {
			haveWord = true
			break
		}
	}
	if !haveWord {
		return &GateError{Code: GateEmpty, Message: "empty statement"}
	}
	for i := 0; i < end; i++ {
		seg := segments[i]
		if seg.kind == segPunct && seg.text == ";" {
			return &GateError{Code: GateMultiple, Message: "only a single SQL statement is allowed"}
		}
		if seg.kind == segComment || seg.kind == segSpace {
			continue
		}
		if seg.kind == segWord {
			word := strings.ToLower(seg.text)
			if forbiddenKeywords[word] {
				return &GateError{Code: GateForbidden, Message: fmt.Sprintf("%s is not allowed", strings.ToUpper(word))}
			}
			if word == "load_extension" && nextPunct(segments, i) == "(" {
				return &GateError{Code: GateForbidden, Message: "load_extension is not allowed"}
			}
		}
		if seg.kind == segQuotedIdent || seg.kind == segWord {
			name := strings.Trim(seg.text, "\"")
			if strings.HasPrefix(name, "_") || strings.HasPrefix(strings.ToLower(name), "sqlite_") {
				return &GateError{Code: GateInternal, Message: fmt.Sprintf("access to internal object %q is not allowed", name)}
			}
		}
	}

	if readOnly {
		first := firstKeyword(segments)
		switch first {
		case "select", "with", "values", "explain":
		default:
			return &GateError{Code: GateReadOnly, Message: "only read-only statements are allowed on /query"}
		}
		if first == "with" && topLevelWriteWordIndex(segments) >= 0 {
			// Data-modifying CTE (WITH ... DELETE/INSERT/UPDATE): still a write.
			return &GateError{Code: GateReadOnly, Message: "only read-only statements are allowed on /query"}
		}
	}
	return nil
}

func firstKeyword(segments []segment) string {
	for _, seg := range segments {
		if seg.kind == segWord {
			return strings.ToLower(seg.text)
		}
	}
	return ""
}

// StatementKind classifies a statement for the change feed and result shape.
type StatementKind int

const (
	KindRead StatementKind = iota
	KindInsert
	KindUpdate
	KindDelete
	KindSchema
	KindOtherWrite
)

// ClassifyStatement reports whether the statement writes and, when it targets a
// single table, which one. Used to append _cloud_changes rows for /sql writes.
func ClassifyStatement(sqlText string) (StatementKind, string) {
	segments := tokenizeSQL(sqlText)
	words := make([]string, 0, 8)
	for _, seg := range segments {
		if seg.kind == segWord {
			words = append(words, strings.ToLower(seg.text))
		}
	}
	if len(words) == 0 {
		return KindOtherWrite, ""
	}
	start := 0
	if words[0] == "with" {
		idx := topLevelWriteWordIndex(segments)
		if idx < 0 {
			return KindRead, ""
		}
		start = idx
	}
	rest := words[start:]
	if len(rest) == 0 {
		return KindOtherWrite, ""
	}
	switch rest[0] {
	case "select", "values", "explain":
		return KindRead, ""
	case "insert", "replace":
		return KindInsert, tableAfter(rest, "into")
	case "update":
		if len(rest) > 1 {
			return KindUpdate, rest[1]
		}
		return KindUpdate, ""
	case "delete":
		return KindDelete, tableAfter(rest, "from")
	case "create", "drop", "alter":
		kind := KindSchema
		table := ""
		if len(rest) > 1 {
			switch rest[1] {
			case "table":
				table = tableAfter(rest, "table")
			case "view":
				table = tableAfter(rest, "view")
			case "index":
				table = tableAfter(rest, "on")
			}
		}
		return kind, table
	default:
		return KindOtherWrite, ""
	}
}

// topLevelWriteWordIndex returns the index (within the word sequence) of the
// first INSERT/UPDATE/DELETE/REPLACE keyword at paren depth 0, or -1. It is how
// a data-modifying CTE is told apart from a read.
func topLevelWriteWordIndex(segments []segment) int {
	depth := 0
	wordIndex := 0
	for _, seg := range segments {
		if seg.kind == segPunct {
			switch seg.text {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			}
			continue
		}
		if seg.kind != segWord {
			continue
		}
		if depth == 0 {
			switch strings.ToLower(seg.text) {
			case "insert", "update", "delete", "replace":
				return wordIndex
			}
		}
		wordIndex++
	}
	return -1
}

func tableAfter(words []string, keyword string) string {
	for i := 0; i < len(words)-1; i++ {
		if words[i] != keyword {
			continue
		}
		// Skip modifiers: IF NOT EXISTS / IF EXISTS / OR REPLACE / OR IGNORE
		j := i + 1
		for j < len(words) {
			switch words[j] {
			case "if", "not", "exists", "or", "replace", "ignore":
				j++
				continue
			}
			break
		}
		if j < len(words) {
			return words[j]
		}
	}
	return ""
}
