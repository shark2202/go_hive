package excel

import (
	"bytes"
	"encoding/xml"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

type xlsxSheet struct {
	name  string
	rows  [][]any
	width int
}

func (s *xlsxSheet) ensureRow(index int) {
	for len(s.rows) <= index {
		s.rows = append(s.rows, nil)
	}
}

func (s *xlsxSheet) put(row, column int, value any) {
	if row < 0 || column < 0 {
		return
	}
	s.ensureRow(row)
	if len(s.rows[row]) <= column {
		s.rows[row] = append(s.rows[row], make([]any, column-len(s.rows[row])+1)...)
	}
	s.rows[row][column] = value
	if column+1 > s.width {
		s.width = column + 1
	}
}

func (s *xlsxSheet) value(row, column int) any {
	if row < 0 || row >= len(s.rows) {
		return nil
	}
	return excelRowValue(s.rows[row], column)
}

// padRows makes every row width long and drops trailing empty rows, so callers
// can index cells without bounds checks.
func (s *xlsxSheet) padRows() {
	for len(s.rows) > 0 {
		last := s.rows[len(s.rows)-1]
		empty := true
		for _, value := range last {
			if value != nil {
				empty = false
				break
			}
		}
		if !empty {
			break
		}
		s.rows = s.rows[:len(s.rows)-1]
	}
	for index, row := range s.rows {
		if len(row) < s.width {
			s.rows[index] = append(row, make([]any, s.width-len(row))...)
		}
	}
}

// xlsxRawCell is a cell as it appears in the XML, before value conversion.
type xlsxRawCell struct {
	kind   string
	style  int
	value  string
	inline string
}

// resolve converts a raw cell into the JSON-facing value: shared/inline text,
// booleans, the cached formula result, or a number (dates become ISO strings).
func (c xlsxRawCell) resolve(book *xlsxBook) any {
	switch c.kind {
	case "s":
		index, err := strconv.Atoi(strings.TrimSpace(c.value))
		if err != nil || index < 0 || index >= len(book.shared) {
			return nil
		}
		return book.shared[index]
	case "inlineStr":
		return c.inline
	case "str", "e", "d":
		return c.value
	case "b":
		return strings.TrimSpace(c.value) == "1"
	}
	raw := strings.TrimSpace(c.value)
	if raw == "" {
		return nil
	}
	if book.styles.isDate(c.style) {
		if serial, err := strconv.ParseFloat(raw, 64); err == nil {
			return xlsxDateString(serial, book.date1904)
		}
	}
	return xlsxNumberValue(raw)
}

// xlsxNumberValue keeps integer cells as int64 so JSON emits 30, not 30.0.
func xlsxNumberValue(raw string) any {
	if !strings.ContainsAny(raw, ".eE") {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return value
		}
	}
	if value, err := strconv.ParseFloat(raw, 64); err == nil {
		return value
	}
	return raw
}

// xlsxDateString converts an Excel serial date to an ISO-8601 timestamp.
func xlsxDateString(serial float64, date1904 bool) string {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	if date1904 {
		base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	milliseconds := int64(math.Round(serial * 86_400_000))
	stamp := base.Add(time.Duration(milliseconds) * time.Millisecond)
	if stamp.Nanosecond() == 0 {
		return stamp.Format("2006-01-02T15:04:05")
	}
	return stamp.Format("2006-01-02T15:04:05.000")
}

// parseSheetXML streams a worksheet part into a padded grid.
func parseSheetXML(data []byte, book *xlsxBook) (*xlsxSheet, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	sheet := &xlsxSheet{}
	row := -1
	column := -1
	cell := xlsxRawCell{}
	inValue, inInline, inInlineText, inPhonetic := false, false, false, false
	flush := func() {
		if column < 0 {
			return
		}
		sheet.put(row, column, cell.resolve(book))
		cell = xlsxRawCell{}
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "row":
				if raw := xmlAttr(typed, "r"); raw != "" {
					if index, err := strconv.Atoi(raw); err == nil {
						row = index - 1
					}
				} else {
					row++
				}
				column = -1
			case "c":
				if ref := xmlAttr(typed, "r"); ref != "" {
					if refColumn, refRow, ok := parseExcelCellRef(ref); ok {
						column = refColumn
						if refRow >= 0 {
							row = refRow
						}
					}
				} else {
					column++
				}
				cell = xlsxRawCell{kind: xmlAttr(typed, "t")}
				if raw := xmlAttr(typed, "s"); raw != "" {
					cell.style, _ = strconv.Atoi(raw)
				}
			case "v":
				inValue = true
			case "is":
				inInline = true
			case "rPh":
				inPhonetic = true
			case "t":
				if inInline && !inPhonetic {
					inInlineText = true
				}
			}
		case xml.CharData:
			switch {
			case inValue:
				cell.value += string(typed)
			case inInlineText && !inPhonetic:
				cell.inline += string(typed)
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "v":
				inValue = false
			case "t":
				inInlineText = false
			case "is":
				inInline = false
				inInlineText = false
			case "rPh":
				inPhonetic = false
			case "c":
				flush()
			}
		}
	}
	sheet.padRows()
	return sheet, nil
}

// parseExcelCellRef turns "AB12" into 0-based column 27 and row 11.
func parseExcelCellRef(ref string) (int, int, bool) {
	column := 0
	index := 0
	for ; index < len(ref); index++ {
		char := ref[index]
		if char >= 'a' && char <= 'z' {
			char -= 'a' - 'A'
		}
		if char < 'A' || char > 'Z' {
			break
		}
		column = column*26 + int(char-'A'+1)
	}
	if index == 0 || index == len(ref) {
		return 0, 0, false
	}
	row, err := strconv.Atoi(ref[index:])
	if err != nil || row < 1 {
		return 0, 0, false
	}
	return column - 1, row - 1, true
}

// excelColumnName renders a 0-based column index as spreadsheet letters.
func excelColumnName(index int) string {
	if index < 0 {
		return ""
	}
	name := make([]byte, 0, 4)
	for value := index + 1; value > 0; value = (value - 1) / 26 {
		name = append([]byte{byte('A' + (value-1)%26)}, name...)
	}
	return string(name)
}

// excelCellRef builds an A1 reference from 0-based coordinates.
func excelCellRef(column, row int) string {
	return excelColumnName(column) + strconv.Itoa(row+1)
}

// xmlAttr returns the attribute with the given local name, ignoring namespace.
func xmlAttr(element xml.StartElement, local string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == local {
			return attribute.Value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// OOXML package writer
// ---------------------------------------------------------------------------

// xlsxPart is one ZIP entry of a workbook package.
