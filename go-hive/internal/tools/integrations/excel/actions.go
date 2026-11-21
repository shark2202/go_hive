package excel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

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
	if !integrationkit.FileExists(resolved) {
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

	sheet, err := book.sheet(strings.TrimSpace(integrationkit.StringArg(input, "sheet")))
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
	columns := integrationkit.StringSliceArg(input, "columns")
	if len(columns) == 0 {
		return map[string]any{"error": "columns cannot be empty"}, nil
	}
	sheetName := strings.TrimSpace(integrationkit.StringArg(input, "sheet"))
	if sheetName == "" {
		sheetName = "Sheet1"
	}
	if err := excelValidateSheetName(sheetName); err != nil {
		return map[string]any{"error": "Failed to write Excel file: " + err.Error()}, nil
	}
	rows := integrationkit.MapRows(input["rows"])
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
	if !integrationkit.FileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw + ". Use excel_write to create a new file."}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	rows := integrationkit.MapRows(input["rows"])
	if len(rows) == 0 {
		return map[string]any{"error": "rows cannot be empty"}, nil
	}
	title, appended, total, err := appendXLSXRows(resolved, strings.TrimSpace(integrationkit.StringArg(input, "sheet")), rows)
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
	if !integrationkit.FileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	size, err := integrationkit.FileSize(resolved)
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
	if !integrationkit.FileExists(resolved) {
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
	if !integrationkit.FileExists(resolved) {
		return map[string]any{"error": "File not found: " + raw}, nil
	}
	if !excelExtensionOK(raw) {
		return map[string]any{"error": "File must have .xlsx or .xlsm extension"}, nil
	}
	term := integrationkit.StringArg(input, "search_term")
	if term == "" {
		return map[string]any{"error": "search_term cannot be empty"}, nil
	}
	matchType := strings.TrimSpace(integrationkit.StringArg(input, "match_type"))
	if matchType == "" {
		matchType = "contains"
	}
	switch matchType {
	case "contains", "exact", "starts_with", "ends_with":
	default:
		return map[string]any{"error": "match_type must be 'contains', 'exact', 'starts_with', or 'ends_with'"}, nil
	}
	caseSensitive := integrationkit.BoolArg(input, "case_sensitive", false)
	needle := term
	if !caseSensitive {
		needle = strings.ToLower(needle)
	}

	book, err := openXLSX(resolved)
	if err != nil {
		return map[string]any{"error": "Search failed: " + err.Error()}, nil
	}
	defer book.Close()

	requested := strings.TrimSpace(integrationkit.StringArg(input, "sheet"))
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
	raw := strings.TrimSpace(integrationkit.StringArg(input, "path"))
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
			roots = integrationkit.DefaultRoots()
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
	return integrationkit.HasSuffixFold(raw, ".xlsx") || integrationkit.HasSuffixFold(raw, ".xlsm")
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
