package pdf

// PDF lexer: whitespace/delimiter scanning, numbers, dictionaries, arrays,
// literal/hex strings, names and indirect references.

import (
	"fmt"
	"io"
	"strconv"
)

type pdfLexer struct {
	data []byte
}

func pdfIsSpace(char byte) bool {
	switch char {
	case 0x00, 0x09, 0x0A, 0x0C, 0x0D, 0x20:
		return true
	}
	return false
}

func pdfIsDelimiter(char byte) bool {
	switch char {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return pdfIsSpace(char)
}

func pdfIsDigit(char byte) bool { return char >= '0' && char <= '9' }

func (l *pdfLexer) skip(pos int) int {
	for pos < len(l.data) {
		switch {
		case l.data[pos] == '%':
			for pos < len(l.data) && l.data[pos] != '\n' && l.data[pos] != '\r' {
				pos++
			}
		case pdfIsSpace(l.data[pos]):
			pos++
		default:
			return pos
		}
	}
	return pos
}

func (l *pdfLexer) readInteger(pos int) (int, int, bool) {
	pos = l.skip(pos)
	start := pos
	if pos < len(l.data) && (l.data[pos] == '+' || l.data[pos] == '-') {
		pos++
	}
	digits := pos
	for pos < len(l.data) && pdfIsDigit(l.data[pos]) {
		pos++
	}
	if pos == digits {
		return 0, start, false
	}
	value, err := strconv.Atoi(string(l.data[start:pos]))
	if err != nil {
		return 0, pos, false
	}
	return value, pos, true
}

func (l *pdfLexer) parseValue(pos int) (any, int, error) {
	pos = l.skip(pos)
	if pos >= len(l.data) {
		return nil, pos, io.EOF
	}
	switch char := l.data[pos]; {
	case char == '<':
		if pos+1 < len(l.data) && l.data[pos+1] == '<' {
			return l.parseDict(pos + 2)
		}
		return l.parseHexString(pos)
	case char == '[':
		return l.parseArray(pos + 1)
	case char == '(':
		return l.parseLiteralString(pos)
	case char == '/':
		return l.parseName(pos)
	case char == ']' || char == '>' || char == ')' || char == '}' || char == '{':
		return pdfKeyword(string(char)), pos + 1, nil
	default:
		start := pos
		for pos < len(l.data) && !pdfIsDelimiter(l.data[pos]) {
			pos++
		}
		if pos == start {
			return pdfKeyword(string(l.data[pos : pos+1])), pos + 1, nil
		}
		token := string(l.data[start:pos])
		switch token {
		case "true":
			return true, pos, nil
		case "false":
			return false, pos, nil
		case "null":
			return nil, pos, nil
		}
		if number, err := strconv.ParseInt(token, 10, 64); err == nil {
			// Possible indirect reference "N G R".
			after := l.skip(pos)
			if gen, next, ok := l.readInteger(after); ok && gen >= 0 {
				after = l.skip(next)
				if after < len(l.data) && l.data[after] == 'R' && (after+1 == len(l.data) || pdfIsDelimiter(l.data[after+1])) {
					return pdfRef{num: int(number), gen: gen}, after + 1, nil
				}
			}
			return int(number), pos, nil
		}
		if float, err := strconv.ParseFloat(token, 64); err == nil {
			return float, pos, nil
		}
		return pdfKeyword(token), pos, nil
	}
}

func (l *pdfLexer) parseDict(pos int) (map[string]any, int, error) {
	dict := map[string]any{}
	for {
		pos = l.skip(pos)
		if pos+1 < len(l.data) && l.data[pos] == '>' && l.data[pos+1] == '>' {
			return dict, pos + 2, nil
		}
		keyValue, next, err := l.parseValue(pos)
		if err != nil {
			return nil, pos, err
		}
		key, ok := keyValue.(pdfName)
		if !ok {
			return nil, pos, fmt.Errorf("%w: dictionary key", errPDFInvalid)
		}
		value, after, err := l.parseValue(next)
		if err != nil {
			return nil, pos, err
		}
		dict[string(key)] = value
		pos = after
	}
}

func (l *pdfLexer) parseArray(pos int) ([]any, int, error) {
	array := []any{}
	for {
		pos = l.skip(pos)
		if pos >= len(l.data) {
			return array, pos, nil
		}
		if l.data[pos] == ']' {
			return array, pos + 1, nil
		}
		value, next, err := l.parseValue(pos)
		if err != nil {
			return array, pos, err
		}
		array = append(array, value)
		pos = next
	}
}

func (l *pdfLexer) parseLiteralString(pos int) (string, int, error) {
	pos++ // opening paren
	depth := 1
	out := make([]byte, 0, 32)
	for pos < len(l.data) {
		char := l.data[pos]
		switch char {
		case '\\':
			pos++
			if pos >= len(l.data) {
				return string(out), pos, nil
			}
			switch escaped := l.data[pos]; escaped {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, escaped)
			case '\r':
				if pos+1 < len(l.data) && l.data[pos+1] == '\n' {
					pos++
				}
			case '\n':
				// line continuation
			default:
				if escaped >= '0' && escaped <= '7' {
					value := 0
					for count := 0; count < 3 && pos < len(l.data) && l.data[pos] >= '0' && l.data[pos] <= '7'; count++ {
						value = value*8 + int(l.data[pos]-'0')
						pos++
					}
					out = append(out, byte(value))
					continue
				}
				out = append(out, escaped)
			}
			pos++
		case '(':
			depth++
			out = append(out, char)
			pos++
		case ')':
			depth--
			if depth == 0 {
				return string(out), pos + 1, nil
			}
			out = append(out, char)
			pos++
		default:
			out = append(out, char)
			pos++
		}
	}
	return string(out), pos, nil
}

func (l *pdfLexer) parseHexString(pos int) (string, int, error) {
	pos++ // opening <
	high := -1
	out := make([]byte, 0, 16)
	for pos < len(l.data) {
		char := l.data[pos]
		pos++
		if char == '>' {
			break
		}
		value := -1
		switch {
		case char >= '0' && char <= '9':
			value = int(char - '0')
		case char >= 'a' && char <= 'f':
			value = int(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value = int(char-'A') + 10
		}
		if value < 0 {
			continue
		}
		if high < 0 {
			high = value
			continue
		}
		out = append(out, byte(high<<4|value))
		high = -1
	}
	if high >= 0 {
		out = append(out, byte(high<<4))
	}
	return string(out), pos, nil
}

func (l *pdfLexer) parseName(pos int) (pdfName, int, error) {
	pos++ // slash
	out := make([]byte, 0, 16)
	for pos < len(l.data) && !pdfIsDelimiter(l.data[pos]) {
		if l.data[pos] == '#' && pos+2 < len(l.data) {
			if value, err := strconv.ParseUint(string(l.data[pos+1:pos+3]), 16, 8); err == nil {
				out = append(out, byte(value))
				pos += 3
				continue
			}
		}
		out = append(out, l.data[pos])
		pos++
	}
	return pdfName(out), pos, nil
}
