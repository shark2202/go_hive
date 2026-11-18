package integrations

import (
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// csvTable is the parsed shape shared by read/append/info.
type csvTable struct {
	columns   []string
	rows      []map[string]any
	totalRows int
}

// readCSV mirrors Python's csv.DictReader: the first record is the header,
// missing trailing fields become empty strings, and total_rows counts
// non-empty records minus the header.
func readCSV(path string) (*csvTable, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("File not found: %s", filepath.Base(path))
		}
		return nil, fmt.Errorf("Failed to read CSV: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV parsing error: %w", err)
	}
	if len(records) == 0 || len(records[0]) == 0 {
		return nil, errors.New("CSV file is empty or has no headers")
	}
	columns := append([]string(nil), records[0]...)
	table := &csvTable{columns: columns}
	for _, record := range records[1:] {
		if !anyField(record) {
			continue
		}
		table.totalRows++
		row := make(map[string]any, len(columns))
		for index, column := range columns {
			if index < len(record) {
				row[column] = record[index]
			} else {
				row[column] = ""
			}
		}
		table.rows = append(table.rows, row)
	}
	return table, nil
}

// writeCSV writes only the declared columns, in order, filling missing values
// with empty strings (DictWriter's default restval).
func writeCSV(path string, columns []string, rows []map[string]any, appendMode bool) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	flags := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(path, flags, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	if !appendMode {
		if err := writer.Write(columns); err != nil {
			return err
		}
	}
	record := make([]string, len(columns))
	for _, row := range rows {
		for index, column := range columns {
			record[index] = scalarString(row[column])
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func anyField(record []string) bool {
	for _, field := range record {
		if field != "" {
			return true
		}
	}
	return false
}

// mapRows accepts the shapes JSON and Go callers produce for a list of objects.
func mapRows(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if row, ok := item.(map[string]any); ok {
				out = append(out, row)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{typed}
	default:
		return nil
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func hasSuffixFold(value, suffix string) bool {
	return len(value) >= len(suffix) && strings.EqualFold(value[len(value)-len(suffix):], suffix)
}

// resolveSafePath expands ~ and enforces the allowed roots. An empty root list
// falls back to the upstream defaults (~/.hive and ~/aden/hive/exports).
func (rt *Runtime) resolveSafePath(raw string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", errors.New("Path cannot be empty.")
	}
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(os.PathSeparator)))
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	roots := rt.Roots
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		resolvedRoot := filepath.Clean(root)
		if absolute == resolvedRoot || strings.HasPrefix(absolute, resolvedRoot+string(os.PathSeparator)) {
			return absolute, nil
		}
	}
	return "", fmt.Errorf("Access denied: '%s' is outside allowed directories. Use absolute paths under ~/.hive/ or exports/.", raw)
}

func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".hive"),
		filepath.Join(home, "aden", "hive", "exports"),
	}
}
