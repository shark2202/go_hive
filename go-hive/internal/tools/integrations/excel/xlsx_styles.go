package excel

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

type xlsxStyles struct {
	dates map[int]bool
}

func (s xlsxStyles) isDate(index int) bool {
	if s.dates == nil {
		return false
	}
	return s.dates[index]
}

func parseStylesXML(data []byte) xlsxStyles {
	styles := xlsxStyles{dates: map[int]bool{}}
	formats := map[int]bool{}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	inCellXfs := false
	xfIndex := -1
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "numFmt":
				id, err := strconv.Atoi(xmlAttr(typed, "numFmtId"))
				if err == nil {
					formats[id] = xlsxIsDateFormat(xmlAttr(typed, "formatCode"))
				}
			case "cellXfs":
				inCellXfs = true
			case "xf":
				if !inCellXfs {
					continue
				}
				xfIndex++
				id, err := strconv.Atoi(xmlAttr(typed, "numFmtId"))
				if err != nil {
					continue
				}
				if formats[id] || xlsxBuiltinDateFormat(id) {
					styles.dates[xfIndex] = true
				}
			}
		case xml.EndElement:
			if typed.Name.Local == "cellXfs" {
				inCellXfs = false
			}
		}
	}
	return styles
}

// xlsxBuiltinDateFormat covers the built-in date/time number formats.
func xlsxBuiltinDateFormat(id int) bool {
	switch {
	case id >= 14 && id <= 22:
		return true
	case id >= 27 && id <= 36:
		return true
	case id >= 45 && id <= 47:
		return true
	case id >= 50 && id <= 58:
		return true
	default:
		return false
	}
}

// xlsxIsDateFormat is a heuristic over a custom format code: strip quoted
// literals, bracketed sections and escaped characters, then look for date
// tokens.
func xlsxIsDateFormat(code string) bool {
	var cleaned strings.Builder
	quoted, bracket, escaped := false, false, false
	for _, char := range code {
		switch {
		case escaped:
			escaped = false
			continue
		case quoted:
			if char == '"' {
				quoted = false
			}
			continue
		case bracket:
			if char == ']' {
				bracket = false
			}
			continue
		}
		switch char {
		case '"':
			quoted = true
		case '[':
			bracket = true
		case '\\', '_', '*':
			escaped = true
		default:
			cleaned.WriteRune(char)
		}
	}
	text := strings.ToLower(cleaned.String())
	if strings.ContainsAny(text, "ydhs") {
		return true
	}
	return strings.Contains(text, "m") && strings.ContainsAny(text, "/-.年")
}

// xlsxSheet is a parsed worksheet: rows padded to width, 0-based.
