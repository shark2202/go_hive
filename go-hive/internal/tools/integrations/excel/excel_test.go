package excel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// newExcelTestRegistry builds an Excel registry sandboxed to a temp directory.
func newExcelTestRegistry(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(WithRoots(dir)), dir
}

// excelAction executes one Excel action and requires a map result.
func excelAction(t *testing.T, registry *Registry, action string, input map[string]any) map[string]any {
	t.Helper()
	input["action"] = action
	result, err := registry.Execute(context.Background(), "excel", input)
	if err != nil {
		t.Fatalf("excel.%s: %v", action, err)
	}
	object, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("excel.%s returned %T, want map", action, result)
	}
	return object
}

func excelWantInt(t *testing.T, object map[string]any, key string, want int) {
	t.Helper()
	got, ok := object[key].(int)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %d", key, object[key], want)
	}
}

func excelWantString(t *testing.T, object map[string]any, key, want string) {
	t.Helper()
	got, _ := object[key].(string)
	if got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

// excelWantError requires an error dict mentioning every fragment.
func excelWantError(t *testing.T, object map[string]any, fragments ...string) {
	t.Helper()
	message, ok := object["error"].(string)
	if !ok {
		t.Fatalf("expected error, got %#v", object)
	}
	for _, fragment := range fragments {
		if !strings.Contains(strings.ToLower(message), strings.ToLower(fragment)) {
			t.Fatalf("error %q does not mention %q", message, fragment)
		}
	}
}

// excelRows reads a row list out of an action result.
func excelRows(t *testing.T, object map[string]any) []map[string]any {
	t.Helper()
	rows, ok := object["rows"].([]map[string]any)
	if !ok {
		t.Fatalf("rows = %#v", object["rows"])
	}
	return rows
}

func excelStrings(t *testing.T, object map[string]any, key string) []string {
	t.Helper()
	values, ok := object[key].([]string)
	if !ok {
		t.Fatalf("%s = %#v", key, object[key])
	}
	return values
}

// ---------------------------------------------------------------------------
// Fixture builder: writes a real .xlsx package from static XML, independent of
// the production writer, so reader tests cannot pass by agreeing with it.
// ---------------------------------------------------------------------------

type fixtureCell struct {
	kind   string
	text   string
	number float64
	flag   bool
}

func fxText(value string) fixtureCell     { return fixtureCell{kind: "text", text: value} }
func fxRich(value string) fixtureCell     { return fixtureCell{kind: "rich", text: value} }
func fxInline(value string) fixtureCell   { return fixtureCell{kind: "inline", text: value} }
func fxNumber(value float64) fixtureCell  { return fixtureCell{kind: "number", number: value} }
func fxBool(value bool) fixtureCell       { return fixtureCell{kind: "bool", flag: value} }
func fxDate(serial float64) fixtureCell   { return fixtureCell{kind: "date", number: serial} }
func fxFormula(cached string) fixtureCell { return fixtureCell{kind: "formula", text: cached} }
func fxFormulaNoCache() fixtureCell       { return fixtureCell{kind: "formula-nocache"} }
func fxEmpty() fixtureCell                { return fixtureCell{kind: "empty"} }

type fixtureSheet struct {
	name string
	rows [][]fixtureCell
}

type fixtureSharedStrings struct {
	order []string
	index map[string]int
	rich  map[int]bool
}

func newFixtureSharedStrings() *fixtureSharedStrings {
	return &fixtureSharedStrings{index: map[string]int{}, rich: map[int]bool{}}
}

func (s *fixtureSharedStrings) add(value string, rich bool) int {
	if index, ok := s.index[value]; ok {
		return index
	}
	index := len(s.order)
	s.index[value] = index
	s.order = append(s.order, value)
	if rich {
		s.rich[index] = true
	}
	return index
}

func fixtureSerial(stamp time.Time) float64 {
	return stamp.Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours() / 24
}

func fixtureEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func fixtureSheetXML(sheet fixtureSheet, shared *fixtureSharedStrings) string {
	var buffer strings.Builder
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	buffer.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rowIndex, row := range sheet.rows {
		buffer.WriteString(`<row r="`)
		buffer.WriteString(strconv.Itoa(rowIndex + 1))
		buffer.WriteString(`">`)
		for column, cell := range row {
			reference := excelCellRef(column, rowIndex)
			switch cell.kind {
			case "":
				continue
			case "text":
				fmt.Fprintf(&buffer, `<c r="%s" t="s"><v>%d</v></c>`, reference, shared.add(cell.text, false))
			case "rich":
				fmt.Fprintf(&buffer, `<c r="%s" t="s"><v>%d</v></c>`, reference, shared.add(cell.text, true))
			case "inline":
				fmt.Fprintf(&buffer, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, reference, fixtureEscape(cell.text))
			case "number":
				fmt.Fprintf(&buffer, `<c r="%s"><v>%s</v></c>`, reference, strconv.FormatFloat(cell.number, 'f', -1, 64))
			case "bool":
				flag := "0"
				if cell.flag {
					flag = "1"
				}
				fmt.Fprintf(&buffer, `<c r="%s" t="b"><v>%s</v></c>`, reference, flag)
			case "date":
				fmt.Fprintf(&buffer, `<c r="%s" s="1"><v>%s</v></c>`, reference, strconv.FormatFloat(cell.number, 'f', -1, 64))
			case "formula":
				fmt.Fprintf(&buffer, `<c r="%s"><f>SUM(A2:A3)</f><v>%s</v></c>`, reference, cell.text)
			case "formula-nocache":
				fmt.Fprintf(&buffer, `<c r="%s"><f>SUM(A2:A3)</f></c>`, reference)
			case "empty":
				fmt.Fprintf(&buffer, `<c r="%s"/>`, reference)
			}
		}
		buffer.WriteString(`</row>`)
	}
	buffer.WriteString(`</sheetData></worksheet>`)
	return buffer.String()
}

func fixtureSharedStringsXML(shared *fixtureSharedStrings) string {
	var buffer strings.Builder
	buffer.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	buffer.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="`)
	buffer.WriteString(strconv.Itoa(len(shared.order)))
	buffer.WriteString(`" uniqueCount="`)
	buffer.WriteString(strconv.Itoa(len(shared.order)))
	buffer.WriteString(`">`)
	for index, value := range shared.order {
		if shared.rich[index] {
			middle := len(value) / 2
			buffer.WriteString(`<si><r><rPr><b/></rPr><t xml:space="preserve">`)
			buffer.WriteString(fixtureEscape(value[:middle]))
			buffer.WriteString(`</t></r><r><t xml:space="preserve">`)
			buffer.WriteString(fixtureEscape(value[middle:]))
			buffer.WriteString(`</t></r></si>`)
			continue
		}
		buffer.WriteString(`<si><t xml:space="preserve">`)
		buffer.WriteString(fixtureEscape(value))
		buffer.WriteString(`</t></si>`)
	}
	buffer.WriteString(`</sst>`)
	return buffer.String()
}

const fixtureStylesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="0"/><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="14" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`

// buildFixtureWorkbook writes a multi-sheet workbook plus optional extra parts.
func buildFixtureWorkbook(t *testing.T, target string, sheets []fixtureSheet, extras map[string][]byte) {
	t.Helper()
	shared := newFixtureSharedStrings()
	sheetXML := make([]string, len(sheets))
	for index, sheet := range sheets {
		sheetXML[index] = fixtureSheetXML(sheet, shared)
	}

	var workbook strings.Builder
	workbook.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	workbook.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for index, sheet := range sheets {
		fmt.Fprintf(&workbook, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, fixtureEscape(sheet.name), index+1, index+1)
	}
	workbook.WriteString(`</sheets></workbook>`)

	base := "http://schemas.openxmlformats.org/officeDocument/2006/relationships/"
	var rels strings.Builder
	rels.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for index := range sheets {
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="%sworksheet" Target="worksheets/sheet%d.xml"/>`, index+1, base, index+1)
	}
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="%sstyles" Target="styles.xml"/>`, len(sheets)+1, base)
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="%ssharedStrings" Target="sharedStrings.xml"/>`, len(sheets)+2, base)
	rels.WriteString(`</Relationships>`)

	content := "application/vnd.openxmlformats-officedocument."
	var types strings.Builder
	types.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	types.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	types.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`)
	types.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="` + content + `spreadsheetml.sheet.main+xml"/>`)
	for index := range sheets {
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="%sspreadsheetml.worksheet+xml"/>`, index+1, content)
	}
	types.WriteString(`<Override PartName="/xl/styles.xml" ContentType="` + content + `spreadsheetml.styles+xml"/>`)
	types.WriteString(`<Override PartName="/xl/sharedStrings.xml" ContentType="` + content + `spreadsheetml.sharedStrings+xml"/></Types>`)

	parts := []xlsxPart{
		{name: "[Content_Types].xml", data: []byte(types.String())},
		{name: "_rels/.rels", data: []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)},
		{name: "xl/workbook.xml", data: []byte(workbook.String())},
		{name: "xl/_rels/workbook.xml.rels", data: []byte(rels.String())},
		{name: "xl/styles.xml", data: []byte(fixtureStylesXML)},
		{name: "xl/sharedStrings.xml", data: []byte(fixtureSharedStringsXML(shared))},
	}
	for index, xml := range sheetXML {
		parts = append(parts, xlsxPart{name: fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1), data: []byte(xml)})
	}
	for name, data := range extras {
		parts = append(parts, xlsxPart{name: name, data: data})
	}
	if err := writeXLSXPackage(target, parts); err != nil {
		t.Fatalf("build fixture: %v", err)
	}
}

// excelBasicFixture mirrors the upstream basic_xlsx fixture.
func excelBasicFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	registry, dir := newExcelTestRegistry(t)
	buildFixtureWorkbook(t, filepath.Join(dir, "basic.xlsx"), []fixtureSheet{
		{name: "Sheet1", rows: [][]fixtureCell{
			{fxText("name"), fxText("age"), fxText("city")},
			{fxRich("Alice"), fxNumber(30), fxText("NYC")},
			{fxText("Bob"), fxNumber(25), fxText("LA")},
			{fxText("Charlie"), fxNumber(35), fxText("Chicago")},
		}},
	}, nil)
	return registry, dir
}

// excelMultiSheetFixture mirrors the upstream multi_sheet_xlsx fixture.
func excelMultiSheetFixture(t *testing.T) (*Registry, string) {
	t.Helper()
	registry, dir := newExcelTestRegistry(t)
	buildFixtureWorkbook(t, filepath.Join(dir, "multi_sheet.xlsx"), []fixtureSheet{
		{name: "Employees", rows: [][]fixtureCell{
			{fxText("id"), fxText("name"), fxText("department")},
			{fxNumber(1), fxRich("Alice"), fxText("Engineering")},
			{fxNumber(2), fxText("Bob"), fxText("Marketing")},
		}},
		{name: "Products", rows: [][]fixtureCell{
			{fxText("id"), fxText("name"), fxText("price")},
			{fxNumber(1), fxText("Widget"), fxNumber(99.99)},
			{fxNumber(2), fxText("Gadget"), fxNumber(149.99)},
		}},
		{name: "Summary", rows: [][]fixtureCell{
			{fxText("metric"), fxText("value")},
			{fxText("total_employees"), fxNumber(2)},
			{fxText("total_products"), fxNumber(2)},
		}},
	}, map[string][]byte{"xl/custom/marker.txt": []byte("hive-batch3")})
	return registry, dir
}

func readZipPart(t *testing.T, target, name string) []byte {
	t.Helper()
	reader, err := zip.OpenReader(target)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()
	for _, file := range reader.File {
		if file.Name != name {
			continue
		}
		handle, err := file.Open()
		if err != nil {
			t.Fatalf("open part: %v", err)
		}
		defer handle.Close()
		data, err := io.ReadAll(handle)
		if err != nil {
			t.Fatalf("read part: %v", err)
		}
		return data
	}
	t.Fatalf("part %q missing", name)
	return nil
}

// ---------------------------------------------------------------------------
// Catalog and installation
// ---------------------------------------------------------------------------

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests call the MCP tool functions, not a registry surface.
func TestExcelCatalogIntegrity(t *testing.T) {
	registry := NewRegistry()
	if names := registry.Names(); len(names) != 1 || names[0] != "excel" {
		t.Fatalf("names = %v", names)
	}
	if got := registry.ActionCount(); got != 6 {
		t.Fatalf("actions = %d, want 6", got)
	}
	if got := registry.ImplementedActions(); got != 6 {
		t.Fatalf("implemented = %d, want 6", got)
	}
	tool, ok := registry.Lookup("excel")
	if !ok {
		t.Fatal("excel tool missing")
	}
	if tool.ID != "excel_tool" || tool.DisplayName != "Excel" || tool.Summary == "" {
		t.Fatalf("tool identity = %+v", tool)
	}
	for _, action := range tool.Actions {
		if registry.Impls(tool.Name)[action.Name] == nil {
			t.Fatalf("%s has no implementation", action.Name)
		}
		if action.Source != "excel_tool/excel_tool.py" {
			t.Fatalf("%s source = %q", action.Name, action.Source)
		}
		if action.Summary == "" || len(action.Params) == 0 {
			t.Fatalf("%s missing schema: %+v", action.Name, action)
		}
	}
	// excel_sql needs DuckDB upstream; the port excludes it instead of
	// shipping a stub, mirroring gen_catalog.py's csv_sql exclusion.
	if _, err := registry.Execute(context.Background(), "excel", map[string]any{"action": "excel_sql"}); !errors.Is(err, integrationkit.ErrUnknownAction) {
		t.Fatalf("excel_sql err = %v, want ErrUnknownAction", err)
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestInstall(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := NewRegistry().Install(shared); installed != 1 {
		t.Fatalf("installed = %d, want 1", installed)
	}
	if !shared.HasTool("excel") {
		t.Fatal("shared registry missing excel")
	}
	tool, ok := shared.GetTool("excel")
	if !ok {
		t.Fatal("excel tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %#v", tool.Parameters["properties"])
	}
	selector, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("missing action selector: %#v", properties)
	}
	enum, _ := selector["enum"].([]string)
	if len(enum) != 6 {
		t.Fatalf("action enum = %v", selector["enum"])
	}
	for _, name := range []string{"path", "columns", "rows", "sheet", "limit", "offset", "search_term", "match_type", "case_sensitive"} {
		if _, present := properties[name]; !present {
			t.Fatalf("schema missing %q: %#v", name, properties)
		}
	}
	if _, present := properties["agent_id"]; present {
		t.Fatalf("agent_id is runtime identity, not a model parameter: %#v", properties)
	}
	if !strings.Contains(tool.Description, "excel_read") || !strings.Contains(tool.Description, "required:") {
		t.Fatalf("description = %q", tool.Description)
	}
}

// ---------------------------------------------------------------------------
// excel_read
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_read_basic_xlsx
func TestExcelReadBasic(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "basic.xlsx"})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	if columns := excelStrings(t, result, "columns"); strings.Join(columns, ",") != "name,age,city" {
		t.Fatalf("columns = %v", columns)
	}
	excelWantInt(t, result, "column_count", 3)
	excelWantInt(t, result, "row_count", 3)
	excelWantInt(t, result, "total_rows", 3)
	excelWantString(t, result, "sheet_name", "Sheet1")
	rows := excelRows(t, result)
	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[0]["name"] != "Alice" || rows[0]["age"] != int64(30) || rows[0]["city"] != "NYC" {
		t.Fatalf("first row = %#v", rows[0])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_read_specific_sheet
func TestExcelReadSpecificSheet(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "multi_sheet.xlsx", "sheet": "Products"})
	excelWantString(t, result, "sheet_name", "Products")
	if columns := excelStrings(t, result, "columns"); strings.Join(columns, ",") != "id,name,price" {
		t.Fatalf("columns = %v", columns)
	}
	excelWantInt(t, result, "row_count", 2)
	rows := excelRows(t, result)
	if rows[0]["name"] != "Widget" || rows[0]["price"] != 99.99 {
		t.Fatalf("first row = %#v", rows[0])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_read_nonexistent_sheet_error
func TestExcelReadMissingSheet(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "multi_sheet.xlsx", "sheet": "NonExistent"})
	excelWantError(t, result, "not found", "Available sheets", "Employees")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_read_with_limit, tools/tests/tools/test_excel_tool.py::test_read_with_offset
func TestExcelReadLimitAndOffset(t *testing.T) {
	registry, dir := excelBasicFixture(t)

	limited := excelAction(t, registry, "excel_read", map[string]any{"path": "basic.xlsx", "limit": 2})
	excelWantInt(t, limited, "row_count", 2)
	excelWantInt(t, limited, "total_rows", 3)
	if rows := excelRows(t, limited); rows[0]["name"] != "Alice" || rows[1]["name"] != "Bob" {
		t.Fatalf("limited rows = %#v", rows)
	}

	offset := excelAction(t, registry, "excel_read", map[string]any{"path": "basic.xlsx", "offset": 1})
	excelWantInt(t, offset, "row_count", 2)
	excelWantInt(t, offset, "offset", 1)
	if rows := excelRows(t, offset); rows[0]["name"] != "Bob" || rows[1]["name"] != "Charlie" {
		t.Fatalf("offset rows = %#v", rows)
	}

	// Pagination across 100 data rows.
	largeRows := [][]fixtureCell{{fxText("id"), fxText("value")}}
	for index := 0; index < 100; index++ {
		largeRows = append(largeRows, []fixtureCell{fxNumber(float64(index)), fxNumber(float64(index * 10))})
	}
	buildFixtureWorkbook(t, filepath.Join(dir, "large.xlsx"), []fixtureSheet{{name: "Data", rows: largeRows}}, nil)
	paged := excelAction(t, registry, "excel_read", map[string]any{"path": "large.xlsx", "limit": 10, "offset": 50})
	excelWantInt(t, paged, "row_count", 10)
	excelWantInt(t, paged, "total_rows", 100)
	rows := excelRows(t, paged)
	if rows[0]["id"] != int64(50) || rows[0]["value"] != int64(500) {
		t.Fatalf("paged first row = %#v", rows[0])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_offset_beyond_rows
func TestExcelReadOffsetBeyondRows(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "basic.xlsx", "offset": 100})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	excelWantInt(t, result, "row_count", 0)
	excelWantInt(t, result, "total_rows", 3)
	if rows := excelRows(t, result); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_file_not_found
func TestExcelReadMissingFile(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "nonexistent.xlsx"})
	excelWantError(t, result, "not found")
}

// Parity-exempt: openpyxl raises BadZipFile for a non-workbook; this pins the
// equivalent Go error being reported instead of panicking.
func TestExcelReadCorruptWorkbook(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "broken.xlsx"), []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "broken.xlsx"})
	excelWantError(t, result, "Failed to read Excel file")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_non_xlsx_extension
func TestExcelReadRejectsExtension(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("name,age\nAlice,30\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "data.txt"})
	excelWantError(t, result, ".xlsx")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_empty_xlsx_file
func TestExcelReadEmptyWorkbook(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	buildFixtureWorkbook(t, filepath.Join(dir, "empty.xlsx"), []fixtureSheet{{name: "Sheet1"}}, nil)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "empty.xlsx"})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	excelWantInt(t, result, "row_count", 0)
	if rows := excelRows(t, result); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_headers_only_xlsx
func TestExcelReadHeadersOnly(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	buildFixtureWorkbook(t, filepath.Join(dir, "headers_only.xlsx"), []fixtureSheet{
		{name: "Sheet1", rows: [][]fixtureCell{{fxText("name"), fxText("age"), fxText("city")}}},
	}, nil)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "headers_only.xlsx"})
	if columns := excelStrings(t, result, "columns"); strings.Join(columns, ",") != "name,age,city" {
		t.Fatalf("columns = %v", columns)
	}
	excelWantInt(t, result, "row_count", 0)
	excelWantInt(t, result, "total_rows", 0)
	if rows := excelRows(t, result); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_negative_limit, tools/tests/tools/test_excel_tool.py::test_negative_offset
func TestExcelReadNegativePaging(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	for _, input := range []map[string]any{
		{"path": "basic.xlsx", "limit": -1},
		{"path": "basic.xlsx", "offset": -1},
	} {
		result := excelAction(t, registry, "excel_read", input)
		excelWantError(t, result, "non-negative")
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_read_with_dates
func TestExcelReadDates(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	stamp := time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)
	buildFixtureWorkbook(t, filepath.Join(dir, "dates.xlsx"), []fixtureSheet{
		{name: "Sheet1", rows: [][]fixtureCell{
			{fxText("name"), fxText("created_at")},
			{fxText("Alice"), fxDate(fixtureSerial(stamp))},
			{fxText("Bob"), fxDate(fixtureSerial(time.Date(2024, 6, 20, 14, 45, 0, 0, time.UTC)))},
		}},
	}, nil)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "dates.xlsx"})
	rows := excelRows(t, result)
	created, _ := rows[0]["created_at"].(string)
	if !strings.HasPrefix(created, "2024-01-15") {
		t.Fatalf("created_at = %q", created)
	}
	if created != "2024-01-15T10:30:00" {
		t.Fatalf("created_at = %q, want exact serial conversion", created)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_path_traversal_blocked
func TestExcelReadPathTraversalBlocked(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "../../../etc/passwd.xlsx"})
	excelWantError(t, result, "denied")
}

// Parity-exempt: openpyxl returns cached formula results under data_only=True;
// this asserts the same contract in the Go reader, which never evaluates
// formulas itself.
func TestExcelReadCachedFormulaAndBlankCells(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	buildFixtureWorkbook(t, filepath.Join(dir, "formulas.xlsx"), []fixtureSheet{
		{name: "Sheet1", rows: [][]fixtureCell{
			{fxText("amount")},
			{fxNumber(10)},
			{fxNumber(20)},
			{fxFormula("30")},
			{fxFormulaNoCache()},
			{fxEmpty()},
			{fxBool(true)},
			{fxInline("inline text")},
		}},
	}, nil)
	result := excelAction(t, registry, "excel_read", map[string]any{"path": "formulas.xlsx"})
	rows := excelRows(t, result)
	if len(rows) != 7 {
		t.Fatalf("rows = %d", len(rows))
	}
	if rows[2]["amount"] != int64(30) {
		t.Fatalf("cached formula = %#v", rows[2]["amount"])
	}
	if rows[3]["amount"] != nil {
		t.Fatalf("uncached formula = %#v, want nil", rows[3]["amount"])
	}
	if rows[4]["amount"] != nil {
		t.Fatalf("blank cell = %#v, want nil", rows[4]["amount"])
	}
	if rows[5]["amount"] != true {
		t.Fatalf("boolean cell = %#v", rows[5]["amount"])
	}
	if rows[6]["amount"] != "inline text" {
		t.Fatalf("inline cell = %#v", rows[6]["amount"])
	}
}

// ---------------------------------------------------------------------------
// excel_write
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_write_new_xlsx
func TestExcelWriteNewFile(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "output.xlsx",
		"columns": []string{"name", "age", "city"},
		"rows": []map[string]any{
			{"name": "Alice", "age": 30, "city": "NYC"},
			{"name": "Bob", "age": 25, "city": "LA"},
		},
	})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	excelWantInt(t, result, "rows_written", 2)
	excelWantInt(t, result, "column_count", 3)
	excelWantString(t, result, "sheet_name", "Sheet1")
	if !integrationkit.FileExists(filepath.Join(dir, "output.xlsx")) {
		t.Fatal("output.xlsx was not written")
	}
	read := excelAction(t, registry, "excel_read", map[string]any{"path": "output.xlsx"})
	rows := excelRows(t, read)
	if len(rows) != 2 || rows[1]["name"] != "Bob" || rows[1]["age"] != int64(25) {
		t.Fatalf("round-trip rows = %#v", rows)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_with_custom_sheet_name
func TestExcelWriteCustomSheet(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "output.xlsx",
		"columns": []string{"id", "value"},
		"rows":    []map[string]any{{"id": 1, "value": 100}},
		"sheet":   "MyData",
	})
	excelWantString(t, result, "sheet_name", "MyData")
	list := excelAction(t, registry, "excel_sheet_list", map[string]any{"path": "output.xlsx"})
	if names := excelStrings(t, list, "sheet_names"); len(names) != 1 || names[0] != "MyData" {
		t.Fatalf("sheet_names = %v", names)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_creates_parent_directories
func TestExcelWriteCreatesParentDirs(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "subdir/nested/output.xlsx",
		"columns": []string{"id"},
		"rows":    []map[string]any{{"id": 1}},
	})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	if !integrationkit.FileExists(filepath.Join(dir, "subdir", "nested", "output.xlsx")) {
		t.Fatal("nested workbook missing")
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_empty_columns_error
func TestExcelWriteEmptyColumns(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "output.xlsx",
		"columns": []string{},
		"rows":    []map[string]any{},
	})
	excelWantError(t, result, "empty")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_non_xlsx_extension_error
func TestExcelWriteRejectsExtension(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "output.txt",
		"columns": []string{"id"},
		"rows":    []map[string]any{},
	})
	excelWantError(t, result, ".xlsx")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_empty_rows
func TestExcelWriteEmptyRows(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "output.xlsx",
		"columns": []string{"name", "age"},
		"rows":    []map[string]any{},
	})
	excelWantInt(t, result, "rows_written", 0)
	read := excelAction(t, registry, "excel_read", map[string]any{"path": "output.xlsx"})
	if columns := excelStrings(t, read, "columns"); strings.Join(columns, ",") != "name,age" {
		t.Fatalf("columns = %v", columns)
	}
	excelWantInt(t, read, "row_count", 0)
}

// ---------------------------------------------------------------------------
// excel_append
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_append_to_existing_xlsx
func TestExcelAppendRows(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_append", map[string]any{
		"path": "basic.xlsx",
		"rows": []map[string]any{
			{"name": "David", "age": 28, "city": "Seattle"},
			{"name": "Eve", "age": 32, "city": "Boston"},
		},
	})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	excelWantInt(t, result, "rows_appended", 2)
	excelWantInt(t, result, "total_rows", 5)
	read := excelAction(t, registry, "excel_read", map[string]any{"path": "basic.xlsx"})
	excelWantInt(t, read, "row_count", 5)
	rows := excelRows(t, read)
	if rows[3]["name"] != "David" || rows[3]["age"] != int64(28) || rows[4]["city"] != "Boston" {
		t.Fatalf("appended rows = %#v", rows[3:])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_append_to_specific_sheet
func TestExcelAppendSpecificSheet(t *testing.T) {
	registry, dir := excelMultiSheetFixture(t)
	target := filepath.Join(dir, "multi_sheet.xlsx")
	beforeEmployees := readZipPart(t, target, "xl/worksheets/sheet1.xml")

	result := excelAction(t, registry, "excel_append", map[string]any{
		"path":  "multi_sheet.xlsx",
		"sheet": "Products",
		"rows":  []map[string]any{{"id": 3, "name": "Doohickey", "price": 49.99}},
	})
	excelWantString(t, result, "sheet_name", "Products")
	excelWantInt(t, result, "rows_appended", 1)
	excelWantInt(t, result, "total_rows", 3)

	products := excelAction(t, registry, "excel_read", map[string]any{"path": "multi_sheet.xlsx", "sheet": "Products"})
	rows := excelRows(t, products)
	if rows[2]["name"] != "Doohickey" || rows[2]["price"] != 49.99 {
		t.Fatalf("appended row = %#v", rows[2])
	}
	// Untouched parts of the package survive byte for byte.
	if afterEmployees := readZipPart(t, target, "xl/worksheets/sheet1.xml"); !bytes.Equal(beforeEmployees, afterEmployees) {
		t.Fatal("appending to Products rewrote the Employees sheet")
	}
	if marker := readZipPart(t, target, "xl/custom/marker.txt"); string(marker) != "hive-batch3" {
		t.Fatalf("marker part = %q", marker)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_append_file_not_found
func TestExcelAppendMissingFile(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_append", map[string]any{
		"path": "nonexistent.xlsx",
		"rows": []map[string]any{{"name": "Alice"}},
	})
	excelWantError(t, result, "not found", "excel_write")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_append_empty_rows_error
func TestExcelAppendEmptyRows(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_append", map[string]any{"path": "basic.xlsx", "rows": []map[string]any{}})
	excelWantError(t, result, "empty")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_append_non_xlsx_extension_error
func TestExcelAppendRejectsExtension(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("name\nAlice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := excelAction(t, registry, "excel_append", map[string]any{
		"path": "data.txt",
		"rows": []map[string]any{{"name": "Bob"}},
	})
	excelWantError(t, result, ".xlsx")
}

// ---------------------------------------------------------------------------
// excel_info and excel_sheet_list
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_get_info_basic_xlsx
func TestExcelInfoBasic(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_info", map[string]any{"path": "basic.xlsx"})
	if result["success"] != true {
		t.Fatalf("result = %#v", result)
	}
	excelWantInt(t, result, "sheet_count", 1)
	if names := excelStrings(t, result, "sheet_names"); len(names) != 1 || names[0] != "Sheet1" {
		t.Fatalf("sheet_names = %v", names)
	}
	size, ok := result["file_size_bytes"].(int64)
	if !ok || size <= 0 {
		t.Fatalf("file_size_bytes = %#v", result["file_size_bytes"])
	}
	sheets, ok := result["sheets"].([]map[string]any)
	if !ok || len(sheets) != 1 {
		t.Fatalf("sheets = %#v", result["sheets"])
	}
	excelWantString(t, sheets[0], "name", "Sheet1")
	excelWantInt(t, sheets[0], "row_count", 3)
	columns, _ := sheets[0]["columns"].([]string)
	if strings.Join(columns, ",") != "name,age,city" {
		t.Fatalf("columns = %v", columns)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_get_info_multi_sheet_xlsx
func TestExcelInfoMultiSheet(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_info", map[string]any{"path": "multi_sheet.xlsx"})
	excelWantInt(t, result, "sheet_count", 3)
	if names := excelStrings(t, result, "sheet_names"); strings.Join(names, ",") != "Employees,Products,Summary" {
		t.Fatalf("sheet_names = %v", names)
	}
	sheets, _ := result["sheets"].([]map[string]any)
	if len(sheets) != 3 {
		t.Fatalf("sheets = %d", len(sheets))
	}
	for _, sheet := range sheets {
		if sheet["name"] == "Products" {
			excelWantInt(t, sheet, "row_count", 2)
			columns, _ := sheet["columns"].([]string)
			if strings.Join(columns, ",") != "id,name,price" {
				t.Fatalf("Products columns = %v", columns)
			}
		}
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_get_info_file_not_found
func TestExcelInfoMissingFile(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_info", map[string]any{"path": "nonexistent.xlsx"})
	excelWantError(t, result, "not found")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_get_info_non_xlsx_extension_error
func TestExcelInfoRejectsExtension(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("name\nAlice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := excelAction(t, registry, "excel_info", map[string]any{"path": "data.txt"})
	excelWantError(t, result, ".xlsx")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_list_sheets_basic
func TestExcelSheetListBasic(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_sheet_list", map[string]any{"path": "basic.xlsx"})
	if names := excelStrings(t, result, "sheet_names"); len(names) != 1 || names[0] != "Sheet1" {
		t.Fatalf("sheet_names = %v", names)
	}
	excelWantInt(t, result, "sheet_count", 1)
}

// Parity: tools/tests/tools/test_excel_tool.py::test_list_sheets_multi_sheet
func TestExcelSheetListMultiSheet(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_sheet_list", map[string]any{"path": "multi_sheet.xlsx"})
	excelWantInt(t, result, "sheet_count", 3)
	if names := excelStrings(t, result, "sheet_names"); strings.Join(names, ",") != "Employees,Products,Summary" {
		t.Fatalf("sheet_names = %v", names)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_list_sheets_file_not_found
func TestExcelSheetListMissingFile(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_sheet_list", map[string]any{"path": "nonexistent.xlsx"})
	excelWantError(t, result, "not found")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_list_sheets_non_xlsx_extension_error
func TestExcelSheetListRejectsExtension(t *testing.T) {
	registry, dir := newExcelTestRegistry(t)
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("name\nAlice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result := excelAction(t, registry, "excel_sheet_list", map[string]any{"path": "data.txt"})
	excelWantError(t, result, ".xlsx")
}

// ---------------------------------------------------------------------------
// excel_search
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_search_basic_contains
func TestExcelSearchContains(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{"path": "basic.xlsx", "search_term": "Alice"})
	matches, _ := result["matches"].([]map[string]any)
	if len(matches) < 1 {
		t.Fatalf("matches = %#v", result["matches"])
	}
	if matches[0]["value"] != "Alice" || matches[0]["sheet"] != "Sheet1" || matches[0]["column"] != "name" {
		t.Fatalf("first match = %#v", matches[0])
	}
	if matches[0]["row"] != 2 || matches[0]["column_index"] != 1 {
		t.Fatalf("match position = %#v", matches[0])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_case_insensitive
func TestExcelSearchCaseInsensitive(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "alice", "case_sensitive": false,
	})
	excelWantInt(t, result, "match_count", 1)
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_case_sensitive
func TestExcelSearchCaseSensitive(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "alice", "case_sensitive": true,
	})
	excelWantInt(t, result, "match_count", 0)
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_exact_match
func TestExcelSearchExact(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "NYC", "match_type": "exact",
	})
	excelWantInt(t, result, "match_count", 1)
	matches, _ := result["matches"].([]map[string]any)
	if matches[0]["value"] != "NYC" {
		t.Fatalf("match = %#v", matches[0])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_starts_with
func TestExcelSearchStartsWith(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "Ch", "match_type": "starts_with",
	})
	excelWantInt(t, result, "match_count", 2)
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_across_sheets
func TestExcelSearchAcrossSheets(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{"path": "multi_sheet.xlsx", "search_term": "Alice"})
	if searched := excelStrings(t, result, "sheets_searched"); len(searched) != 3 {
		t.Fatalf("sheets_searched = %v", searched)
	}
	if count, _ := result["match_count"].(int); count < 1 {
		t.Fatalf("match_count = %#v", result["match_count"])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_specific_sheet
func TestExcelSearchSpecificSheet(t *testing.T) {
	registry, _ := excelMultiSheetFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "multi_sheet.xlsx", "search_term": "Widget", "sheet": "Products",
	})
	if searched := excelStrings(t, result, "sheets_searched"); len(searched) != 1 || searched[0] != "Products" {
		t.Fatalf("sheets_searched = %v", searched)
	}
	if count, _ := result["match_count"].(int); count < 1 {
		t.Fatalf("match_count = %#v", result["match_count"])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_skips_header_row
func TestExcelSearchSkipsHeader(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "name", "match_type": "exact",
	})
	excelWantInt(t, result, "match_count", 0)
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_no_matches
func TestExcelSearchNoMatches(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{"path": "basic.xlsx", "search_term": "ZZZZNOTFOUND"})
	excelWantInt(t, result, "match_count", 0)
	if matches, _ := result["matches"].([]map[string]any); len(matches) != 0 {
		t.Fatalf("matches = %#v", matches)
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_empty_term_error
func TestExcelSearchEmptyTerm(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{"path": "basic.xlsx", "search_term": ""})
	excelWantError(t, result, "empty")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_invalid_match_type
func TestExcelSearchInvalidMatchType(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	result := excelAction(t, registry, "excel_search", map[string]any{
		"path": "basic.xlsx", "search_term": "test", "match_type": "invalid",
	})
	excelWantError(t, result, "match_type")
}

// Parity: tools/tests/tools/test_excel_tool.py::test_search_file_not_found
func TestExcelSearchMissingFile(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	result := excelAction(t, registry, "excel_search", map[string]any{"path": "nonexistent.xlsx", "search_term": "test"})
	excelWantError(t, result, "not found")
}

// ---------------------------------------------------------------------------
// Round-trip integration
// ---------------------------------------------------------------------------

// Parity: tools/tests/tools/test_excel_tool.py::test_write_then_read
func TestExcelWriteThenRead(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	write := excelAction(t, registry, "excel_write", map[string]any{
		"path":    "test.xlsx",
		"columns": []string{"name", "score"},
		"rows": []map[string]any{
			{"name": "Alice", "score": 95},
			{"name": "Bob", "score": 87},
			{"name": "Charlie", "score": 92},
		},
	})
	if write["success"] != true {
		t.Fatalf("write = %#v", write)
	}
	read := excelAction(t, registry, "excel_read", map[string]any{"path": "test.xlsx"})
	excelWantInt(t, read, "row_count", 3)
	rows := excelRows(t, read)
	if rows[0]["name"] != "Alice" || rows[0]["score"] != int64(95) {
		t.Fatalf("row = %#v", rows[0])
	}
	// A column omitted from a row is written as an empty value, like upstream's
	// row_data.get(column, "").
	excelAction(t, registry, "excel_append", map[string]any{
		"path": "test.xlsx",
		"rows": []map[string]any{{"name": "Dana"}},
	})
	after := excelAction(t, registry, "excel_read", map[string]any{"path": "test.xlsx"})
	appended := excelRows(t, after)
	if appended[3]["name"] != "Dana" || appended[3]["score"] != "" {
		t.Fatalf("row with missing column = %#v", appended[3])
	}
}

// Parity: tools/tests/tools/test_excel_tool.py::test_write_append_read
func TestExcelWriteAppendRead(t *testing.T) {
	registry, _ := newExcelTestRegistry(t)
	excelAction(t, registry, "excel_write", map[string]any{
		"path":    "test.xlsx",
		"columns": []string{"id", "value"},
		"rows": []map[string]any{
			{"id": 1, "value": "A"},
			{"id": 2, "value": "B"},
		},
	})
	appended := excelAction(t, registry, "excel_append", map[string]any{
		"path": "test.xlsx",
		"rows": []map[string]any{
			{"id": 3, "value": "C"},
			{"id": 4, "value": "D"},
		},
	})
	excelWantInt(t, appended, "total_rows", 4)
	read := excelAction(t, registry, "excel_read", map[string]any{"path": "test.xlsx"})
	excelWantInt(t, read, "row_count", 4)
	rows := excelRows(t, read)
	if rows[2]["id"] != int64(3) || rows[3]["value"] != "D" {
		t.Fatalf("rows = %#v", rows)
	}
}

// Parity-exempt: excel_sql is intentionally absent (DuckDB dependency); this
// pins the documented gap so a later lane cannot claim it silently.
func TestExcelSqlNotPorted(t *testing.T) {
	registry, _ := excelBasicFixture(t)
	_, err := registry.Execute(context.Background(), "excel", map[string]any{
		"action": "excel_sql",
		"path":   "basic.xlsx",
		"query":  "SELECT * FROM data",
	})
	if !errors.Is(err, integrationkit.ErrUnknownAction) {
		t.Fatalf("err = %v, want ErrUnknownAction", err)
	}
	tool, _ := registry.Lookup("excel")
	for _, action := range tool.ActionNames() {
		if action == "excel_sql" {
			t.Fatal("excel_sql must not be advertised without an implementation")
		}
	}
}
