// Batch 3 of the integration port: the Excel module (upstream excel_tool).
//
// The port reads and writes the OOXML spreadsheet package directly through
// archive/zip and encoding/xml, so excel works with CGO_ENABLED=0 and adds no
// dependency. Upstream used openpyxl; the observable dict shapes and the
// header-row conventions are preserved.
//
// Deliberate limits, all user-visible rather than silent:
//
//   - Formulas are never evaluated. Reading returns the value Excel cached in
//     the file (openpyxl with data_only=True behaves the same); a formula cell
//     without a cached value reads as null. Writing stores values only.
//   - excel_write produces a single-sheet workbook whose only styling is a
//     date number format. excel_append edits the existing worksheet XML in
//     place, so other sheets, styles, merged cells and formulas survive.
//   - excel_sql is not ported: upstream backs it with DuckDB, a native
//     dependency this build cannot take. It is excluded from the catalog the
//     same way gen_catalog.py already excludes csv_sql, and callers filter
//     rows after excel_read instead. Nothing is shipped as a stub.
//   - The workbook package is held in memory (XML parts only), so a very large
//     workbook costs memory proportional to its uncompressed size.
package integrations

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools"
)

// InstallBatch3Excel publishes the Excel module into a shared registry and
// returns the number of tools registered (one tool, six actions).
func InstallBatch3Excel(target *tools.ToolRegistry) int {
	return newBatch3ExcelRegistry().Install(target)
}

// batch3ExcelOptions carries the sandbox roots used by file-backed actions.
type batch3ExcelOptions struct {
	roots []string
}

type batch3ExcelOption func(*batch3ExcelOptions)

// withBatch3ExcelRoots points the sandbox at a directory (tests).
func withBatch3ExcelRoots(roots ...string) batch3ExcelOption {
	return func(options *batch3ExcelOptions) { options.roots = roots }
}

// newBatch3ExcelRegistry builds a registry containing only the Excel tool,
// sharing the Runtime surface used by the rest of the port.
func newBatch3ExcelRegistry(opts ...batch3ExcelOption) *Registry {
	options := batch3ExcelOptions{}
	for _, opt := range opts {
		opt(&options)
	}
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Roots:   options.roots,
		Modules: map[string]*ModuleConfig{},
		Getenv:  os.Getenv,
	}
	registry := &Registry{
		runtime: runtime,
		tools:   map[string]*Tool{},
		specs:   map[string]map[string]RestSpec{},
		impls:   map[string]map[string]ActionFunc{},
	}
	for _, tool := range batch3ExcelCatalog {
		registry.tools[tool.Name] = tool
		registry.order = append(registry.order, tool.Name)
	}
	for action, fn := range map[string]ActionFunc{
		"excel_append":     excelAppend,
		"excel_info":       excelInfo,
		"excel_read":       excelRead,
		"excel_search":     excelSearch,
		"excel_sheet_list": excelSheetList,
		"excel_write":      excelWrite,
	} {
		registry.addImpl("excel", action, fn)
	}
	return registry
}

// batch3ExcelCatalog mirrors gen_catalog.py output for excel_tool. Actions are
// in the generator's sorted order; agent_id/workspace_id/session_id are
// runtime identity in the Go port and therefore not model parameters.
var batch3ExcelCatalog = []*Tool{
	{
		ID:          "excel_tool",
		Name:        "excel",
		DisplayName: "Excel",
		Summary:     "Excel integration (6 actions).",
		Actions: []Action{
			{
				Name:     "excel_append",
				Summary:  "Append rows to an existing Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "rows"},
				Params: map[string]any{
					"path":  param("string"),
					"rows":  paramArrayOfObjects(),
					"sheet": param("string"),
				},
			},
			{
				Name:     "excel_info",
				Summary:  "Get metadata about an Excel file without reading all data.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path": param("string"),
				},
			},
			{
				Name:     "excel_read",
				Summary:  "Read an Excel file and return its contents.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path":   param("string"),
					"sheet":  param("string"),
					"limit":  param("integer"),
					"offset": paramDefault("integer", 0),
				},
			},
			{
				Name:     "excel_search",
				Summary:  "Search for values across Excel sheets.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "search_term"},
				Params: map[string]any{
					"path":           param("string"),
					"search_term":    param("string"),
					"sheet":          param("string"),
					"case_sensitive": paramDefault("boolean", false),
					"match_type":     paramDefault("string", "contains"),
				},
			},
			{
				Name:     "excel_sheet_list",
				Summary:  "List all sheet names in an Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path"},
				Params: map[string]any{
					"path": param("string"),
				},
			},
			{
				Name:     "excel_write",
				Summary:  "Write data to a new Excel file.",
				Source:   "excel_tool/excel_tool.py",
				Required: []string{"path", "columns", "rows"},
				Params: map[string]any{
					"path":    param("string"),
					"columns": paramItems("array", "string"),
					"rows":    paramArrayOfObjects(),
					"sheet":   paramDefault("string", "Sheet1"),
				},
			},
		},
	},
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

// excelRead returns the sheet as a header row plus row objects, with the same
// offset/limit maths as upstream (pagination applies to data rows only).
func excelRead(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	offset, present := excelOptionalInt(input, "offset")
	if !present {
		offset = 0
	}
	limit, hasLimit := excelOptionalInt(input, "limit")
	if offset < 0 || (hasLimit && limit < 0) {
		return map[string]any{"error": "offset and limit must be non-negative"}, nil
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !fileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	book, err := openXLSX(resolved)
	if err != nil {
		return map[string]any{"error": "Failed to read Excel file: " + err.Error()}, nil
	}
	defer book.Close()

	sheet, err := book.sheet(strings.TrimSpace(stringArg(input, "sheet")))
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if len(sheet.rows) == 0 {
		return map[string]any{
			"success":      true,
			"path":         raw,
			"sheet_name":   sheet.name,
			"columns":      []string{},
			"column_count": 0,
			"rows":         []map[string]any{},
			"row_count":    0,
			"total_rows":   0,
			"offset":       offset,
			"limit":        excelEchoLimit(limit, hasLimit),
		}, nil
	}

	columns := make([]string, sheet.width)
	for index := 0; index < sheet.width; index++ {
		columns[index] = excelColumnLabel(sheet.value(0, index), index)
	}
	dataRows := sheet.rows[1:]
	totalRows := len(dataRows)
	if offset > 0 {
		if offset >= len(dataRows) {
			dataRows = nil
		} else {
			dataRows = dataRows[offset:]
		}
	}
	if hasLimit && limit < len(dataRows) {
		dataRows = dataRows[:limit]
	}
	rows := make([]map[string]any, 0, len(dataRows))
	for _, row := range dataRows {
		record := make(map[string]any, len(columns))
		for index := 0; index < sheet.width; index++ {
			name := ""
			if index < len(columns) {
				name = columns[index]
			}
			if name == "" {
				name = fmt.Sprintf("Column_%d", index+1)
			}
			record[name] = excelRowValue(row, index)
		}
		rows = append(rows, record)
	}
	return map[string]any{
		"success":      true,
		"path":         raw,
		"sheet_name":   sheet.name,
		"columns":      columns,
		"column_count": len(columns),
		"rows":         rows,
		"row_count":    len(rows),
		"total_rows":   totalRows,
		"offset":       offset,
		"limit":        excelEchoLimit(limit, hasLimit),
	}, nil
}

// excelWrite creates a new single-sheet workbook from a header list and row
// objects. Values missing from a row become the empty string, like upstream.
func excelWrite(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	columns := stringSliceArg(input, "columns")
	if len(columns) == 0 {
		return map[string]any{"error": "columns cannot be empty"}, nil
	}
	sheetName := strings.TrimSpace(stringArg(input, "sheet"))
	if sheetName == "" {
		sheetName = "Sheet1"
	}
	if err := excelValidateSheetName(sheetName); err != nil {
		return map[string]any{"error": "Failed to write Excel file: " + err.Error()}, nil
	}
	rows := mapRows(input["rows"])
	table := make([][]any, 0, len(rows)+1)
	header := make([]any, len(columns))
	for index, column := range columns {
		header[index] = column
	}
	table = append(table, header)
	for _, row := range rows {
		record := make([]any, len(columns))
		for index, column := range columns {
			value, present := row[column]
			if !present {
				value = ""
			}
			record[index] = value
		}
		table = append(table, record)
	}
	if dir := filepath.Dir(resolved); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return map[string]any{"error": "Failed to write Excel file: " + err.Error()}, nil
		}
	}
	if err := writeXLSXFile(resolved, sheetName, table); err != nil {
		return map[string]any{"error": "Failed to write Excel file: " + err.Error()}, nil
	}
	return map[string]any{
		"success":      true,
		"path":         raw,
		"sheet_name":   sheetName,
		"columns":      columns,
		"column_count": len(columns),
		"rows_written": len(rows),
	}, nil
}

// excelAppend adds rows to an existing sheet. The workbook package is rewritten
// surgically: only the target worksheet part changes, so every other part
// (styles, shared strings, other sheets, media) is copied byte for byte.
func excelAppend(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !fileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw + ". Use excel_write to create a new file."}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	rows := mapRows(input["rows"])
	if len(rows) == 0 {
		return map[string]any{"error": "rows cannot be empty"}, nil
	}
	title, appended, total, err := appendXLSXRows(resolved, strings.TrimSpace(stringArg(input, "sheet")), rows)
	if err != nil {
		var message *xlsxMessageError
		if errors.As(err, &message) {
			return map[string]any{"error": message.Error()}, nil
		}
		return map[string]any{"error": "Failed to append to Excel file: " + err.Error()}, nil
	}
	return map[string]any{
		"success":       true,
		"path":          raw,
		"sheet_name":    title,
		"rows_appended": appended,
		"total_rows":    total,
	}, nil
}

// excelInfo reports file size, sheet names and per-sheet header/row counts.
func excelInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !fileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	size, err := fileSize(resolved)
	if err != nil {
		return map[string]any{"error": "Failed to get Excel info: " + err.Error()}, nil
	}
	book, err := openXLSX(resolved)
	if err != nil {
		return map[string]any{"error": "Failed to get Excel info: " + err.Error()}, nil
	}
	defer book.Close()

	names := book.sheetNames()
	sheets := make([]map[string]any, 0, len(names))
	for _, name := range names {
		sheet, err := book.sheet(name)
		if err != nil {
			return map[string]any{"error": "Failed to get Excel info: " + err.Error()}, nil
		}
		columns := make([]string, 0, sheet.width)
		for index := 0; index < sheet.width; index++ {
			columns = append(columns, excelColumnLabel(sheet.value(0, index), index))
		}
		rowCount := 0
		if len(sheet.rows) > 1 {
			rowCount = len(sheet.rows) - 1
		}
		sheets = append(sheets, map[string]any{
			"name":         name,
			"columns":      columns,
			"column_count": len(columns),
			"row_count":    rowCount,
		})
	}
	return map[string]any{
		"success":         true,
		"path":            raw,
		"file_size_bytes": size,
		"sheet_count":     len(names),
		"sheet_names":     names,
		"sheets":          sheets,
	}, nil
}

// excelSheetList lists sheet names without reading cell data.
func excelSheetList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !fileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	book, err := openXLSX(resolved)
	if err != nil {
		return map[string]any{"error": "Failed to list sheets: " + err.Error()}, nil
	}
	defer book.Close()
	names := book.sheetNames()
	return map[string]any{
		"success":     true,
		"path":        raw,
		"sheet_names": names,
		"sheet_count": len(names),
	}, nil
}

// excelSearch scans data rows (never the header) for a term, reporting the
// sheet, 1-based row, header name, 1-based column index and converted value.
func excelSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, resolved, err := excelResolvePath(rt, input)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if !fileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	term := stringArg(input, "search_term")
	if term == "" {
		return map[string]any{"error": "search_term cannot be empty"}, nil
	}
	matchType := strings.TrimSpace(stringArg(input, "match_type"))
	if matchType == "" {
		matchType = "contains"
	}
	switch matchType {
	case "contains", "exact", "starts_with", "ends_with":
	default:
		return map[string]any{"error": "match_type must be 'contains', 'exact', 'starts_with', or 'ends_with'"}, nil
	}
	caseSensitive := boolArg(input, "case_sensitive", false)
	needle := term
	if !caseSensitive {
		needle = strings.ToLower(needle)
	}

	book, err := openXLSX(resolved)
	if err != nil {
		return map[string]any{"error": "Search failed: " + err.Error()}, nil
	}
	defer book.Close()

	requested := strings.TrimSpace(stringArg(input, "sheet"))
	searched := book.sheetNames()
	if requested != "" {
		if _, ok := book.sheetRef(requested); !ok {
			return map[string]any{"error": "Sheet '" + requested + "' not found. Available: " + strings.Join(book.sheetNames(), ", ")}, nil
		}
		searched = []string{requested}
	}
	matches := make([]map[string]any, 0)
	for _, name := range searched {
		sheet, err := book.sheet(name)
		if err != nil {
			return map[string]any{"error": "Search failed: " + err.Error()}, nil
		}
		headers := make([]string, sheet.width)
		for index := 0; index < sheet.width; index++ {
			headers[index] = excelColumnLabel(sheet.value(0, index), index)
		}
		for rowIndex := 1; rowIndex < len(sheet.rows); rowIndex++ {
			row := sheet.rows[rowIndex]
			for column := 0; column < sheet.width; column++ {
				value := excelRowValue(row, column)
				if value == nil {
					continue
				}
				candidate := excelCellText(value)
				compare := candidate
				if !caseSensitive {
					compare = strings.ToLower(compare)
				}
				matched := false
				switch matchType {
				case "contains":
					matched = strings.Contains(compare, needle)
				case "exact":
					matched = compare == needle
				case "starts_with":
					matched = strings.HasPrefix(compare, needle)
				case "ends_with":
					matched = strings.HasSuffix(compare, needle)
				}
				if !matched {
					continue
				}
				columnName := fmt.Sprintf("Column_%d", column+1)
				if column < len(headers) {
					columnName = headers[column]
				}
				matches = append(matches, map[string]any{
					"sheet":        name,
					"row":          rowIndex + 1,
					"column":       columnName,
					"column_index": column + 1,
					"value":        value,
				})
			}
		}
	}
	return map[string]any{
		"success":         true,
		"path":            raw,
		"search_term":     term,
		"match_type":      matchType,
		"case_sensitive":  caseSensitive,
		"sheets_searched": searched,
		"matches":         matches,
		"match_count":     len(matches),
	}, nil
}

// ---------------------------------------------------------------------------
// Action helpers
// ---------------------------------------------------------------------------

// excelResolvePath expands ~ and enforces the sandbox roots. Unlike the CSV
// tool, a relative path is interpreted against the first sandbox root, which
// is what a model calling excel_read with "report.xlsx" expects.
func excelResolvePath(rt *Runtime, input map[string]any) (string, string, error) {
	raw := strings.TrimSpace(stringArg(input, "path"))
	if raw == "" {
		return "", "", errors.New("Path cannot be empty.")
	}
	candidate := raw
	if strings.HasPrefix(candidate, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return raw, "", err
		}
		candidate = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(candidate, "~"), string(os.PathSeparator)))
	}
	if !filepath.IsAbs(candidate) {
		roots := rt.Roots
		if len(roots) == 0 {
			roots = defaultRoots()
		}
		if len(roots) > 0 {
			candidate = filepath.Join(roots[0], candidate)
		}
	}
	resolved, err := rt.ResolveSafePath(candidate)
	if err != nil {
		return raw, "", err
	}
	return raw, resolved, nil
}

// excelExtensionOK reports whether the caller asked for an OOXML workbook.
func excelExtensionOK(raw string) bool {
	return hasSuffixFold(raw, ".xlsx") || hasSuffixFold(raw, ".xlsm")
}

// excelOptionalInt distinguishes "limit omitted" (all rows) from limit 0.
func excelOptionalInt(input map[string]any, name string) (int, bool) {
	value, ok := input[name]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// excelEchoLimit renders the pagination limit the way the caller supplied it.
func excelEchoLimit(limit int, present bool) any {
	if !present {
		return nil
	}
	return limit
}

// excelColumnLabel formats a header cell, falling back to Column_N.
func excelColumnLabel(value any, index int) string {
	if value == nil {
		return fmt.Sprintf("Column_%d", index+1)
	}
	return excelCellText(value)
}

// excelRowValue reads a padded row cell.
func excelRowValue(row []any, index int) any {
	if index < len(row) {
		return row[index]
	}
	return nil
}

// excelCellText renders a cell the way Python's str() would for the value
// types the reader produces (notably True/False for booleans).
func excelCellText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case time.Time:
		return typed.Format("2006-01-02T15:04:05")
	default:
		return fmt.Sprint(value)
	}
}

// excelValidateSheetName applies Excel's naming rules instead of writing a
// workbook that Excel would refuse to open.
func excelValidateSheetName(name string) error {
	if name == "" {
		return errors.New("sheet name cannot be empty")
	}
	if len([]rune(name)) > 31 {
		return fmt.Errorf("sheet name %q is longer than 31 characters", name)
	}
	if strings.ContainsAny(name, `[]:*?/\`) {
		return fmt.Errorf("sheet name %q cannot contain any of [ ] : * ? / \\", name)
	}
	return nil
}

// ---------------------------------------------------------------------------
// OOXML package reader
// ---------------------------------------------------------------------------

// xlsxPackage is an open workbook zip archive.
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
