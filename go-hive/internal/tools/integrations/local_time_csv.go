package integrations

import (
	"context"
	"fmt"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hiveclock"
)

// getCurrentTime ports time_tool.get_current_time.
//
// `datetime` is Python's datetime.isoformat(): microsecond precision when the
// clock has sub-second component, and a numeric UTC offset ("+00:00"), never
// the "Z" that time.RFC3339 emits for UTC.
func getCurrentTime(_ context.Context, _ *Runtime, input map[string]any) (any, error) {
	name := stringArg(input, "timezone")
	if name == "" {
		name = "UTC"
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return map[string]any{"error": "Invalid timezone: " + name}, nil
	}
	now := hiveclock.Now().In(location)
	return map[string]any{
		"datetime":       pythonISODatetime(now),
		"date":           now.Format("2006-01-02"),
		"time":           now.Format("15:04:05"),
		"timezone":       name,
		"day_of_week":    now.Weekday().String(),
		"unix_timestamp": now.Unix(),
	}, nil
}

// pythonISODatetime renders t the way datetime.isoformat() does for an aware
// datetime: "2006-01-02T15:04:05.ffffff+07:00", with the fractional part
// omitted when the microsecond component is zero.
func pythonISODatetime(t time.Time) string {
	rendered := t.Format("2006-01-02T15:04:05")
	if micro := t.Nanosecond() / 1000; micro != 0 {
		rendered += fmt.Sprintf(".%06d", micro)
	}
	return rendered + t.Format("-07:00")
}

// csvPath validates the upstream two-part precondition (a .csv path inside an
// allowed root) and returns the resolved path or a model-facing error.
func csvPath(rt *Runtime, input map[string]any) (string, map[string]any) {
	raw := stringArg(input, "path")
	if raw == "" {
		return "", map[string]any{"error": "path is required"}
	}
	if len(raw) < 4 || !hasSuffixFold(raw, ".csv") {
		return "", map[string]any{"error": "File must have .csv extension"}
	}
	path, err := rt.ResolveSafePath(raw)
	if err != nil {
		return "", map[string]any{"error": err.Error()}
	}
	return path, nil
}

func csvRead(_ context.Context, rt *Runtime, input map[string]any) (any, error) {
	path, problem := csvPath(rt, input)
	if problem != nil {
		return problem, nil
	}
	offset := intArg(input, "offset", 0)
	limit := intArg(input, "limit", -1)
	if offset < 0 || limit < -1 {
		return map[string]any{"error": "offset and limit must be non-negative"}, nil
	}
	table, err := readCSV(path)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	rows := []map[string]any{}
	for index, record := range table.rows {
		if index < offset {
			continue
		}
		if limit >= 0 && len(rows) >= limit {
			break
		}
		rows = append(rows, record)
	}
	return map[string]any{
		"success":      true,
		"path":         stringArg(input, "path"),
		"columns":      table.columns,
		"column_count": len(table.columns),
		"rows":         rows,
		"row_count":    len(rows),
		"total_rows":   table.totalRows,
		"offset":       offset,
		"limit":        nullableInt(limit),
	}, nil
}

func csvWrite(_ context.Context, rt *Runtime, input map[string]any) (any, error) {
	path, problem := csvPath(rt, input)
	if problem != nil {
		return problem, nil
	}
	columns := stringSliceArg(input, "columns")
	if len(columns) == 0 {
		return map[string]any{"error": "columns cannot be empty"}, nil
	}
	rows := mapRows(input["rows"])
	if err := writeCSV(path, columns, rows, false); err != nil {
		return map[string]any{"error": "Failed to write CSV: " + err.Error()}, nil
	}
	return map[string]any{
		"success":      true,
		"path":         stringArg(input, "path"),
		"columns":      columns,
		"column_count": len(columns),
		"rows_written": len(rows),
	}, nil
}

func csvAppend(_ context.Context, rt *Runtime, input map[string]any) (any, error) {
	raw := stringArg(input, "path")
	if raw == "" {
		return map[string]any{"error": "path is required"}, nil
	}
	path, err := rt.ResolveSafePath(raw)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	// Upstream checks existence before the .csv suffix, so a missing non-.csv
	// path reports "File not found" instead of the extension error.
	if !fileExists(path) {
		return map[string]any{"error": fmt.Sprintf("File not found: %s. Use csv_write to create a new file.", raw)}, nil
	}
	if len(raw) < 4 || !hasSuffixFold(raw, ".csv") {
		return map[string]any{"error": "File must have .csv extension"}, nil
	}
	rows := mapRows(input["rows"])
	if len(rows) == 0 {
		return map[string]any{"error": "rows cannot be empty"}, nil
	}
	table, err := readCSV(path)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if err := writeCSV(path, table.columns, rows, true); err != nil {
		return map[string]any{"error": "Failed to append to CSV: " + err.Error()}, nil
	}
	// Upstream recounts the file after the write: only non-empty records count,
	// so an appended row whose keys all fall outside the columns does not raise
	// total_rows. Re-reading also keeps blank/partial rows consistent with
	// csv_read and csv_info.
	updated, err := readCSV(path)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{
		"success":       true,
		"path":          raw,
		"rows_appended": len(rows),
		"total_rows":    updated.totalRows,
	}, nil
}

func csvInfo(_ context.Context, rt *Runtime, input map[string]any) (any, error) {
	path, problem := csvPath(rt, input)
	if problem != nil {
		return problem, nil
	}
	if !fileExists(path) {
		return map[string]any{"error": "File not found: " + stringArg(input, "path")}, nil
	}
	table, err := readCSV(path)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	size, statErr := fileSize(path)
	if statErr != nil {
		return map[string]any{"error": "Failed to get CSV info: " + statErr.Error()}, nil
	}
	return map[string]any{
		"success":         true,
		"path":            stringArg(input, "path"),
		"columns":         table.columns,
		"column_count":    len(table.columns),
		"total_rows":      table.totalRows,
		"file_size_bytes": size,
	}, nil
}

func nullableInt(value int) any {
	if value < 0 {
		return nil
	}
	return value
}
