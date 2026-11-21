package excel

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

type xlsxPackage struct {
	reader *zip.ReadCloser
	files  map[string]*zip.File
	order  []string
}

func openXLSXPackage(target string) (*xlsxPackage, error) {
	reader, err := zip.OpenReader(target)
	if err != nil {
		return nil, err
	}
	pkg := &xlsxPackage{reader: reader, files: map[string]*zip.File{}}
	for _, file := range reader.File {
		pkg.files[file.Name] = file
		pkg.order = append(pkg.order, file.Name)
	}
	return pkg, nil
}

func (p *xlsxPackage) read(name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("empty part name")
	}
	file, ok := p.files[name]
	if !ok {
		return nil, fmt.Errorf("missing part %q", name)
	}
	handle, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer handle.Close()
	return io.ReadAll(handle)
}

func (p *xlsxPackage) readOptional(name string) []byte {
	data, err := p.read(name)
	if err != nil {
		return nil
	}
	return data
}

func (p *xlsxPackage) close() error {
	if p.reader == nil {
		return nil
	}
	err := p.reader.Close()
	p.reader = nil
	return err
}

// xlsxSheetRef ties a workbook sheet name to its worksheet part.
type xlsxSheetRef struct {
	Name string
	Part string
}

// xlsxBook is a parsed workbook: sheet index, shared strings and styles.
type xlsxBook struct {
	pkg      *xlsxPackage
	sheets   []xlsxSheetRef
	shared   []string
	styles   xlsxStyles
	date1904 bool
}

func openXLSX(target string) (*xlsxBook, error) {
	pkg, err := openXLSXPackage(target)
	if err != nil {
		return nil, err
	}
	book := &xlsxBook{pkg: pkg}
	if err := book.loadMetadata(); err != nil {
		_ = pkg.close()
		return nil, err
	}
	return book, nil
}

// loadMetadata reads the workbook index, shared strings and style table.
func (b *xlsxBook) loadMetadata() error {
	workbookPart := "xl/workbook.xml"
	if rootRels := parseRelationshipsXML(b.pkg.readOptional("_rels/.rels"), ""); rootRels.byType["officeDocument"] != "" {
		workbookPart = rootRels.byType["officeDocument"]
	}
	workbookXML, err := b.pkg.read(workbookPart)
	if err != nil {
		return err
	}
	relationships := parseRelationshipsXML(b.pkg.readOptional(xlsxRelsPart(workbookPart)), path.Dir(workbookPart))
	b.sheets, b.date1904 = parseWorkbookXML(workbookXML, relationships)
	if data := b.pkg.readOptional(relationships.byType["sharedStrings"]); len(data) > 0 {
		b.shared = parseSharedStringsXML(data)
	}
	if data := b.pkg.readOptional(relationships.byType["styles"]); len(data) > 0 {
		b.styles = parseStylesXML(data)
	}
	return nil
}

func (b *xlsxBook) Close() error { return b.pkg.close() }

func (b *xlsxBook) sheetNames() []string {
	names := make([]string, 0, len(b.sheets))
	for _, sheet := range b.sheets {
		names = append(names, sheet.Name)
	}
	return names
}

func (b *xlsxBook) sheetRef(name string) (xlsxSheetRef, bool) {
	for _, sheet := range b.sheets {
		if sheet.Name == name {
			return sheet, true
		}
	}
	return xlsxSheetRef{}, false
}

// sheet loads one worksheet; an empty name selects the first sheet, matching
// the upstream "active sheet" default for the single-sheet files we write.
func (b *xlsxBook) sheet(name string) (*xlsxSheet, error) {
	if len(b.sheets) == 0 {
		return nil, &xlsxMessageError{message: "Workbook has no active sheet"}
	}
	if name == "" {
		name = b.sheets[0].Name
	}
	ref, ok := b.sheetRef(name)
	if !ok {
		return nil, &xlsxMessageError{message: "Sheet '" + name + "' not found. Available sheets: " + strings.Join(b.sheetNames(), ", ")}
	}
	data, err := b.pkg.read(ref.Part)
	if err != nil {
		return nil, err
	}
	sheet, err := parseSheetXML(data, b)
	if err != nil {
		return nil, err
	}
	sheet.name = ref.Name
	return sheet, nil
}

// xlsxMessageError marks an error whose message is meant for the caller as-is.
type xlsxMessageError struct{ message string }

func (e *xlsxMessageError) Error() string { return e.message }

// xlsxRelsPart maps "xl/workbook.xml" to "xl/_rels/workbook.xml.rels".
func xlsxRelsPart(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

// xlsxRelationships indexes relationship targets by id and by type suffix.
type xlsxRelationships struct {
	byID   map[string]string
	byType map[string]string
}

func parseRelationshipsXML(data []byte, baseDir string) xlsxRelationships {
	relationships := xlsxRelationships{byID: map[string]string{}, byType: map[string]string{}}
	if len(data) == 0 {
		return relationships
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		if xmlAttr(start, "TargetMode") == "External" {
			continue
		}
		id := xmlAttr(start, "Id")
		target := xlsxNormalizePart(baseDir, xmlAttr(start, "Target"))
		if id == "" || target == "" {
			continue
		}
		relationships.byID[id] = target
		if index := strings.LastIndex(xmlAttr(start, "Type"), "/"); index >= 0 {
			relationships.byType[xmlAttr(start, "Type")[index+1:]] = target
		}
	}
	return relationships
}

// xlsxNormalizePart resolves a relationship target against its source folder.
func xlsxNormalizePart(baseDir, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	if baseDir == "" {
		return path.Clean(target)
	}
	return path.Clean(path.Join(baseDir, target))
}

// parseWorkbookXML returns the sheet index and the 1904 date-system flag.
func parseWorkbookXML(data []byte, relationships xlsxRelationships) ([]xlsxSheetRef, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	sheets := make([]xlsxSheetRef, 0, 4)
	date1904 := false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "workbookPr":
			switch xmlAttr(start, "date1904") {
			case "1", "true":
				date1904 = true
			}
		case "sheet":
			name := xmlAttr(start, "name")
			if name == "" {
				continue
			}
			part := relationships.byID[xmlAttr(start, "id")]
			if part == "" {
				part = fmt.Sprintf("xl/worksheets/sheet%s.xml", xmlAttr(start, "sheetId"))
			}
			sheets = append(sheets, xlsxSheetRef{Name: name, Part: part})
		}
	}
	return sheets, date1904
}

// parseSharedStringsXML concatenates every text run of each <si>, skipping
// phonetic (<rPh>) runs.
func parseSharedStringsXML(data []byte) []string {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	shared := make([]string, 0, 64)
	var buffer strings.Builder
	inItem, inText, inPhonetic := false, false, false
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch typed := token.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "si":
				inItem = true
				buffer.Reset()
			case "rPh":
				inPhonetic = true
			case "t":
				if inItem && !inPhonetic {
					inText = true
				}
			}
		case xml.CharData:
			if inItem && inText && !inPhonetic {
				buffer.Write([]byte(typed))
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "rPh":
				inPhonetic = false
			case "si":
				shared = append(shared, buffer.String())
				buffer.Reset()
				inItem = false
			}
		}
	}
	return shared
}

// xlsxStyles records which cellXfs entries render as dates.
