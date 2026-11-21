package excel

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type xlsxPart struct {
	name string
	data []byte
}

// xlsxSharedStrings dedupes strings and keeps insertion order.
type xlsxSharedStrings struct {
	order []string
	index map[string]int
}

func newXLSXSharedStrings() *xlsxSharedStrings {
	return &xlsxSharedStrings{index: map[string]int{}}
}

func (s *xlsxSharedStrings) add(value string) int {
	if index, ok := s.index[value]; ok {
		return index
	}
	index := len(s.order)
	s.index[value] = index
	s.order = append(s.order, value)
	return index
}

// writeXLSXFile writes a single-sheet workbook. rows[0] is the header.
func writeXLSXFile(target, sheetName string, rows [][]any) error {
	shared := newXLSXSharedStrings()
	sheetXML, dimension, err := serializeWorksheet(rows, shared)
	if err != nil {
		return err
	}
	parts := []xlsxPart{
		{name: "[Content_Types].xml", data: xlsxContentTypesXML()},
		{name: "_rels/.rels", data: xlsxRootRelsXML()},
		{name: "xl/workbook.xml", data: xlsxWorkbookXML(sheetName)},
		{name: "xl/_rels/workbook.xml.rels", data: xlsxWorkbookRelsXML()},
		{name: "xl/styles.xml", data: []byte(xlsxStylesXML)},
		{name: "xl/sharedStrings.xml", data: xlsxSharedStringsXML(shared)},
		{name: "xl/worksheets/sheet1.xml", data: xlsxWorksheetXML(dimension, sheetXML)},
	}
	return writeXLSXPackage(target, parts)
}

// writeXLSXPackage zips parts and replaces target atomically.
func writeXLSXPackage(target string, parts []xlsxPart) error {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, part := range parts {
		entry, err := writer.Create(part.name)
		if err != nil {
			return err
		}
		if _, err := entry.Write(part.data); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return replaceFileAtomically(target, buffer.Bytes())
}

// replaceFileAtomically writes bytes to a sibling temp file and renames it, so
// a failed write never leaves a truncated workbook behind.
func replaceFileAtomically(target string, data []byte) error {
	dir := filepath.Dir(target)
	mode := os.FileMode(0o644)
	if info, err := os.Stat(target); err == nil {
		mode = info.Mode().Perm()
	}
	temp, err := os.CreateTemp(dir, ".hive-xlsx-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempName)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Chmod(tempName, mode); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, target); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

// serializeWorksheet renders rows and returns the XML plus its dimension ref.
func serializeWorksheet(rows [][]any, shared *xlsxSharedStrings) ([]byte, string, error) {
	width := 0
	var body strings.Builder
	for rowIndex, row := range rows {
		body.WriteString(`<row r="`)
		body.WriteString(strconv.Itoa(rowIndex + 1))
		body.WriteString(`">`)
		for column, value := range row {
			reference := excelCellRef(column, rowIndex)
			switch typed := value.(type) {
			case nil:
				continue
			case string:
				index := shared.add(typed)
				body.WriteString(`<c r="`)
				body.WriteString(reference)
				body.WriteString(`" t="s"><v>`)
				body.WriteString(strconv.Itoa(index))
				body.WriteString(`</v></c>`)
			case bool:
				flag := "0"
				if typed {
					flag = "1"
				}
				body.WriteString(`<c r="`)
				body.WriteString(reference)
				body.WriteString(`" t="b"><v>`)
				body.WriteString(flag)
				body.WriteString(`</v></c>`)
			case int:
				body.WriteString(xlsxNumberCellXML(reference, strconv.Itoa(typed)))
			case int64:
				body.WriteString(xlsxNumberCellXML(reference, strconv.FormatInt(typed, 10)))
			case float64:
				body.WriteString(xlsxNumberCellXML(reference, strconv.FormatFloat(typed, 'f', -1, 64)))
			case time.Time:
				body.WriteString(`<c r="`)
				body.WriteString(reference)
				body.WriteString(`" s="1"><v>`)
				body.WriteString(strconv.FormatFloat(xlsxSerialDate(typed), 'f', -1, 64))
				body.WriteString(`</v></c>`)
			default:
				return nil, "", fmt.Errorf("unsupported cell value type %T", value)
			}
			if column+1 > width {
				width = column + 1
			}
		}
		body.WriteString(`</row>`)
	}
	dimension := "A1"
	if width > 0 && len(rows) > 0 {
		dimension = "A1:" + excelCellRef(width-1, len(rows)-1)
	}
	return []byte(body.String()), dimension, nil
}

func xlsxNumberCellXML(reference, raw string) string {
	return `<c r="` + reference + `"><v>` + raw + `</v></c>`
}

// xlsxSerialDate converts a timestamp to the Excel 1900-system serial number.
func xlsxSerialDate(stamp time.Time) float64 {
	base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
	return stamp.UTC().Sub(base).Hours() / 24
}

func xlsxWorksheetXML(dimension string, rowsXML []byte) []byte {
	var buffer bytes.Buffer
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	buffer.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="`)
	buffer.WriteString(dimension)
	buffer.WriteString(`"/><sheetData>`)
	buffer.Write(rowsXML)
	buffer.WriteString(`</sheetData></worksheet>`)
	return buffer.Bytes()
}

func xlsxSharedStringsXML(shared *xlsxSharedStrings) []byte {
	var buffer bytes.Buffer
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	buffer.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="`)
	buffer.WriteString(strconv.Itoa(len(shared.order)))
	buffer.WriteString(`" uniqueCount="`)
	buffer.WriteString(strconv.Itoa(len(shared.order)))
	buffer.WriteString(`">`)
	for _, value := range shared.order {
		buffer.WriteString(`<si><t xml:space="preserve">`)
		escapeXMLText(&buffer, value)
		buffer.WriteString(`</t></si>`)
	}
	buffer.WriteString(`</sst>`)
	return buffer.Bytes()
}

func xlsxWorkbookXML(sheetName string) []byte {
	var buffer bytes.Buffer
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	buffer.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="`)
	escapeXMLText(&buffer, sheetName)
	buffer.WriteString(`" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	return buffer.Bytes()
}

func xlsxRootRelsXML() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)
}

func xlsxWorkbookRelsXML() []byte {
	base := "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rId1" Type="` + base + `worksheet" Target="worksheets/sheet1.xml"/>` +
		`<Relationship Id="rId2" Type="` + base + `styles" Target="styles.xml"/>` +
		`<Relationship Id="rId3" Type="` + base + `sharedStrings" Target="sharedStrings.xml"/>` +
		`</Relationships>`)
}

// xlsxStylesXML declares one style: index 1 applies the date number format.
const xlsxStylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy-mm-dd hh:mm:ss"/></numFmts><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`

func xlsxContentTypesXML() []byte {
	base := "application/vnd.openxmlformats-officedocument."
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n" +
		`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="` + base + `spreadsheetml.sheet.main+xml"/>` +
		`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="` + base + `spreadsheetml.worksheet+xml"/>` +
		`<Override PartName="/xl/styles.xml" ContentType="` + base + `spreadsheetml.styles+xml"/>` +
		`<Override PartName="/xl/sharedStrings.xml" ContentType="` + base + `spreadsheetml.sharedStrings+xml"/>` +
		`</Types>`)
}

// escapeXMLText writes escaped character data (encoding/xml handles entities).
func escapeXMLText(buffer *bytes.Buffer, value string) {
	_ = xml.EscapeText(buffer, []byte(value))
}

// ---------------------------------------------------------------------------
// Append (surgical worksheet edit)
// ---------------------------------------------------------------------------

// appendXLSXRows appends row objects to an existing sheet, preserving every
// other part of the package. Only inline strings and plain numbers are added,
// so the shared-string table does not need to change.
func appendXLSXRows(target, sheetName string, rows []map[string]any) (string, int, int, error) {
	pkg, err := openXLSXPackage(target)
	if err != nil {
		return "", 0, 0, err
	}
	defer pkg.close()

	book := &xlsxBook{pkg: pkg}
	if err := book.loadMetadata(); err != nil {
		return "", 0, 0, err
	}
	if len(book.sheets) == 0 {
		return "", 0, 0, &xlsxMessageError{message: "Workbook has no active sheet"}
	}
	name := sheetName
	if name == "" {
		name = book.sheets[0].Name
	}
	ref, ok := book.sheetRef(name)
	if !ok {
		return "", 0, 0, &xlsxMessageError{message: "Sheet '" + name + "' not found. Available sheets: " + strings.Join(book.sheetNames(), ", ")}
	}
	sheetData, err := pkg.read(ref.Part)
	if err != nil {
		return "", 0, 0, err
	}
	sheet, err := parseSheetXML(sheetData, book)
	if err != nil {
		return "", 0, 0, err
	}

	headers := make([]string, sheet.width)
	hasHeader := false
	for index := 0; index < sheet.width; index++ {
		headers[index] = excelCellText(sheet.value(0, index))
		if headers[index] != "" {
			hasHeader = true
		}
	}
	if !hasHeader {
		return "", 0, 0, &xlsxMessageError{message: "Excel file has no headers in the first row"}
	}

	firstRow := len(sheet.rows) + 1
	if firstRow < 2 {
		firstRow = 2
	}
	var inserted strings.Builder
	for offset, row := range rows {
		rowIndex := firstRow + offset
		inserted.WriteString(`<row r="`)
		inserted.WriteString(strconv.Itoa(rowIndex))
		inserted.WriteString(`">`)
		for column, header := range headers {
			value, present := row[header]
			if !present || value == nil {
				value = ""
			}
			cellXML, err := xlsxInlineCellXML(excelCellRef(column, rowIndex-1), value)
			if err != nil {
				return "", 0, 0, err
			}
			inserted.WriteString(cellXML)
		}
		inserted.WriteString(`</row>`)
	}
	lastRow := firstRow + len(rows) - 1
	lastColumn := sheet.width
	if lastColumn < 1 {
		lastColumn = 1
	}
	updated, err := insertWorksheetRows(sheetData, inserted.String(), lastColumn, lastRow)
	if err != nil {
		return "", 0, 0, err
	}
	replacements := map[string][]byte{ref.Part: updated}
	if err := rewriteXLSXPackage(target, pkg, replacements); err != nil {
		return "", 0, 0, err
	}
	totalRows := firstRow - 2 + len(rows)
	return name, len(rows), totalRows, nil
}

// xlsxInlineCellXML renders one appended cell. Inline strings avoid touching
// sharedStrings.xml; datetimes become ISO-8601 text because append does not
// extend the style table.
func xlsxInlineCellXML(reference string, value any) (string, error) {
	switch typed := value.(type) {
	case string:
		var buffer bytes.Buffer
		buffer.WriteString(`<c r="`)
		buffer.WriteString(reference)
		buffer.WriteString(`" t="inlineStr"><is><t xml:space="preserve">`)
		escapeXMLText(&buffer, typed)
		buffer.WriteString(`</t></is></c>`)
		return buffer.String(), nil
	case bool:
		flag := "0"
		if typed {
			flag = "1"
		}
		return `<c r="` + reference + `" t="b"><v>` + flag + `</v></c>`, nil
	case int:
		return xlsxNumberCellXML(reference, strconv.Itoa(typed)), nil
	case int64:
		return xlsxNumberCellXML(reference, strconv.FormatInt(typed, 10)), nil
	case float64:
		return xlsxNumberCellXML(reference, strconv.FormatFloat(typed, 'f', -1, 64)), nil
	case time.Time:
		return xlsxInlineCellXML(reference, typed.Format("2006-01-02T15:04:05"))
	default:
		return "", fmt.Errorf("unsupported cell value type %T", value)
	}
}

// insertWorksheetRows inserts row XML before </sheetData> and refreshes the
// worksheet dimension.
func insertWorksheetRows(sheet []byte, rowsXML string, lastColumn, lastRow int) ([]byte, error) {
	start := bytes.Index(sheet, []byte("<sheetData"))
	if start < 0 {
		return nil, errors.New("worksheet has no sheetData element")
	}
	tagEnd := bytes.IndexByte(sheet[start:], '>')
	if tagEnd < 0 {
		return nil, errors.New("malformed sheetData element")
	}
	tagEnd += start
	dimension := "A1:" + excelCellRef(lastColumn-1, lastRow-1)

	var buffer bytes.Buffer
	if sheet[tagEnd-1] == '/' {
		// <sheetData/>: open it and supply the closing tag ourselves.
		buffer.Write(sheet[:tagEnd-1])
		buffer.WriteByte('>')
		buffer.WriteString(rowsXML)
		buffer.WriteString(`</sheetData>`)
		buffer.Write(sheet[tagEnd+1:])
		return rewriteDimension(buffer.Bytes(), dimension), nil
	}
	end := bytes.Index(sheet[tagEnd:], []byte("</sheetData>"))
	if end < 0 {
		return nil, errors.New("malformed sheetData element")
	}
	end += tagEnd
	buffer.Write(sheet[:end])
	buffer.WriteString(rowsXML)
	buffer.Write(sheet[end:])
	return rewriteDimension(buffer.Bytes(), dimension), nil
}

// rewriteDimension replaces the worksheet's dimension ref when present.
func rewriteDimension(sheet []byte, reference string) []byte {
	start := bytes.Index(sheet, []byte("<dimension"))
	if start < 0 {
		return sheet
	}
	end := bytes.IndexByte(sheet[start:], '>')
	if end < 0 {
		return sheet
	}
	end += start
	var buffer bytes.Buffer
	buffer.Write(sheet[:start])
	buffer.WriteString(`<dimension ref="`)
	buffer.WriteString(reference)
	buffer.WriteString(`"/>`)
	buffer.Write(sheet[end+1:])
	return buffer.Bytes()
}

// rewriteXLSXPackage copies every entry of the open package into a fresh ZIP,
// substituting the given parts, then replaces the file in one rename.
func rewriteXLSXPackage(target string, pkg *xlsxPackage, replacements map[string][]byte) error {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range pkg.order {
		file := pkg.files[name]
		header := file.FileHeader
		entry, err := writer.CreateHeader(&header)
		if err != nil {
			return err
		}
		if data, ok := replacements[name]; ok {
			if _, err := entry.Write(data); err != nil {
				return err
			}
			continue
		}
		handle, err := file.Open()
		if err != nil {
			return err
		}
		if _, err := io.Copy(entry, handle); err != nil {
			handle.Close()
			return err
		}
		handle.Close()
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := pkg.close(); err != nil {
		return err
	}
	return replaceFileAtomically(target, buffer.Bytes())
}
