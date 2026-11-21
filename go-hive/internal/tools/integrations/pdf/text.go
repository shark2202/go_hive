package pdf

// Content-stream text interpreter: operators, operands and the state used to
// rebuild lines and spacing.

import "strings"

type pdfTextSink struct {
	lines   []string
	current strings.Builder
	font    *pdfFont
	leading float64
	lastY   float64
	haveY   bool
}

func (s *pdfTextSink) newline() {
	line := strings.Join(strings.Fields(s.current.String()), " ")
	if line != "" {
		s.lines = append(s.lines, line)
	}
	s.current.Reset()
}

func (s *pdfTextSink) write(text string) {
	s.current.WriteString(text)
}

func (s *pdfTextSink) space() {
	text := s.current.String()
	if text != "" && !strings.HasSuffix(text, " ") {
		s.current.WriteByte(' ')
	}
}

func pdfExtractText(content []byte, fonts map[string]*pdfFont) string {
	if len(content) == 0 {
		return ""
	}
	sink := &pdfTextSink{}
	lexer := &pdfLexer{data: content}
	operands := make([]any, 0, 8)
	pos := 0
	for pos < len(content) {
		value, next, err := lexer.parseValue(pos)
		if err != nil {
			break
		}
		pos = next
		if keyword, ok := value.(pdfKeyword); ok {
			pdfRunTextOperator(sink, fonts, string(keyword), operands)
			operands = operands[:0]
			continue
		}
		operands = append(operands, value)
	}
	sink.newline()
	return strings.Join(sink.lines, "\n")
}

func pdfRunTextOperator(sink *pdfTextSink, fonts map[string]*pdfFont, operator string, operands []any) {
	switch operator {
	case "Tf":
		if len(operands) >= 2 {
			if name, ok := operands[len(operands)-2].(pdfName); ok {
				sink.font = fonts[string(name)]
			}
		}
	case "Td", "TD":
		if len(operands) >= 2 {
			moveX := pdfNumberValue(operands[len(operands)-2])
			moveY := pdfNumberValue(operands[len(operands)-1])
			if moveY != 0 {
				sink.newline()
			} else if moveX != 0 {
				sink.space()
			}
			if operator == "TD" {
				sink.leading = -moveY
			}
		}
	case "Tm":
		if len(operands) >= 6 {
			y := pdfNumberValue(operands[len(operands)-1])
			if !sink.haveY || y != sink.lastY {
				sink.newline()
				sink.lastY = y
				sink.haveY = true
			}
		}
	case "T*":
		sink.newline()
	case "Tj":
		if len(operands) >= 1 {
			if text, ok := operands[len(operands)-1].(string); ok {
				sink.write(pdfDecodeFontString(sink.font, text))
			}
		}
	case "TJ":
		if len(operands) >= 1 {
			if array, ok := operands[len(operands)-1].([]any); ok {
				for _, item := range array {
					switch typed := item.(type) {
					case string:
						sink.write(pdfDecodeFontString(sink.font, typed))
					case int:
						if typed < -200 {
							sink.space()
						}
					case float64:
						if typed < -200 {
							sink.space()
						}
					}
				}
			}
		}
	case "'", "\"":
		sink.newline()
		for i := len(operands) - 1; i >= 0; i-- {
			if text, ok := operands[i].(string); ok {
				sink.write(pdfDecodeFontString(sink.font, text))
				break
			}
		}
	}
}

func pdfNumberValue(value any) float64 {
	switch typed := value.(type) {
	case int:
		return float64(typed)
	case float64:
		return typed
	}
	return 0
}
