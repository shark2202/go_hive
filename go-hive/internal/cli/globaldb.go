package cli

// Parity: framework/global_db/cli.py

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

const (
	globalDBImportBatch = 1000
	globalDBExportPage  = 500
)

// globalDBCommands mounts the standalone hive-global-db CLI under the hive
// binary. Upstream ships it as its own argparse program whose prog is
// "hive-global-db"; InvokeAs makes `hive global-db ...` reach it while every
// rendered page keeps the upstream prog (usage, option labels, error prefix).
// Hidden keeps the frozen CLI-surface contract (C11) intact: the six verbs are
// a Go-side mount of a separate entrypoint, not a new visible `hive` verb that
// the Python reference tree does not have.
func (r *Runner) globalDBCommands() []*commandSpec {
	rowCap := argSpec{Dest: "row_cap", Long: "row-cap", Kind: kindInt}
	return []*commandSpec{{
		Prog:        "hive-global-db",
		Help:        "Shared cloud team global DB CLI",
		Description: "Shared cloud team global DB CLI.",
		Dest:        "cmd",
		InvokeAs:    "global-db",
		Hidden:      true,
		Subcommands: []*commandSpec{
			{
				Prog: "hive-global-db tables",
				Help: "list tables + row counts",
				Run:  r.cmdGlobalDBTables,
			},
			{
				Prog:        "hive-global-db query",
				Help:        "read-only SQL",
				Positionals: []positionalSpec{{Dest: "sql", Required: true}},
				Flags:       []argSpec{rowCap},
				Run:         r.cmdGlobalDBQuery,
			},
			{
				Prog:        "hive-global-db sql",
				Help:        "full SQL (DDL/DML/SELECT)",
				Positionals: []positionalSpec{{Dest: "sql", Required: true}},
				Flags:       []argSpec{rowCap},
				Run:         r.cmdGlobalDBSQL,
			},
			{
				Prog: "hive-global-db upsert",
				Help: "upsert one row",
				Flags: []argSpec{
					{Dest: "table", Long: "table", Kind: kindString, Required: true},
					{Dest: "row", Long: "row", Kind: kindString, Required: true,
						Help: "JSON object of column->value"},
					{Dest: "source_colony", Long: "source-colony", Kind: kindString},
				},
				Run: r.cmdGlobalDBUpsert,
			},
			{
				Prog: "hive-global-db import",
				Help: "bulk upsert from CSV/JSON",
				Flags: []argSpec{
					{Dest: "table", Long: "table", Kind: kindString, Required: true},
					{Dest: "file", Long: "file", Kind: kindString, Required: true,
						Help: "CSV or JSON (array) file"},
					{Dest: "source_colony", Long: "source-colony", Kind: kindString},
				},
				Run: r.cmdGlobalDBImport,
			},
			{
				Prog: "hive-global-db export",
				Help: "stream a whole table out",
				Flags: []argSpec{
					{Dest: "table", Long: "table", Kind: kindString, Required: true},
					{Dest: "format", Long: "format", Kind: kindString, Default: "csv",
						Choices: []string{"csv", "json"}},
				},
				Run: r.cmdGlobalDBExport,
			},
		},
	}}
}

// runGlobalDBValue builds the env client and runs one call. The returned exit
// code is non-zero when the call failed; the error has already been printed.
func (r *Runner) runGlobalDBValue(call func(context.Context, *globaldb.Client) (any, error)) (any, int) {
	client, err := globaldb.NewClientFromEnv()
	if err != nil {
		return nil, r.reportGlobalDBError(err)
	}
	value, err := call(context.Background(), client)
	if err != nil {
		return nil, r.reportGlobalDBError(err)
	}
	return value, 0
}

// runGlobalDB runs one call and dumps the response the way upstream's _run +
// _dump pair does: pretty JSON on stdout, errors on stderr.
func (r *Runner) runGlobalDB(call func(context.Context, *globaldb.Client) (any, error)) int {
	value, code := r.runGlobalDBValue(call)
	if code != 0 {
		return code
	}
	writeJSONOut(r.Stdout, value, true)
	return 0
}

// reportGlobalDBError mirrors _run's two exception arms: an unsigned session
// exits 2, a backend error exits 1, and both print "error: <message>".
func (r *Runner) reportGlobalDBError(err error) int {
	fmt.Fprintf(r.Stderr, "error: %s\n", err.Error())
	var signedOut *globaldb.NotSignedInError
	if errors.As(err, &signedOut) {
		return 2
	}
	return 1
}

// Parity: framework/global_db/cli.py::cmd_tables
func (r *Runner) cmdGlobalDBTables(*parsedArgs) int {
	return r.runGlobalDB(func(ctx context.Context, client *globaldb.Client) (any, error) {
		return client.ListTables(ctx)
	})
}

// Parity: framework/global_db/cli.py::cmd_query
func (r *Runner) cmdGlobalDBQuery(args *parsedArgs) int {
	return r.runGlobalDB(func(ctx context.Context, client *globaldb.Client) (any, error) {
		return client.Query(ctx, args.str("sql"), globalDBRowCap(args))
	})
}

// Parity: framework/global_db/cli.py::cmd_sql
func (r *Runner) cmdGlobalDBSQL(args *parsedArgs) int {
	return r.runGlobalDB(func(ctx context.Context, client *globaldb.Client) (any, error) {
		return client.SQL(ctx, args.str("sql"), globalDBRowCap(args))
	})
}

// globalDBRowCap keeps argparse's None default: an absent flag stays nil (the
// client omits row_cap), while an explicit 0 is passed through and the client
// drops it just like upstream's truthiness check.
func globalDBRowCap(args *parsedArgs) *int {
	if !args.wasProvided("row_cap") {
		return nil
	}
	value := args.intValue("row_cap")
	return &value
}

// Parity: framework/global_db/cli.py::cmd_upsert
func (r *Runner) cmdGlobalDBUpsert(args *parsedArgs) int {
	var decoded any
	if err := json.Unmarshal([]byte(args.str("row")), &decoded); err != nil {
		fmt.Fprintf(r.Stderr, "error: --row is not valid JSON: %s\n", err.Error())
		return 1
	}
	row, ok := decoded.(map[string]any)
	if !ok {
		fmt.Fprint(r.Stderr, "error: --row must be a JSON object of column->value\n")
		return 1
	}
	return r.runGlobalDB(func(ctx context.Context, client *globaldb.Client) (any, error) {
		return client.Upsert(ctx, args.str("table"), row, args.str("source_colony"), "")
	})
}

// Parity: framework/global_db/cli.py::cmd_import
func (r *Runner) cmdGlobalDBImport(args *parsedArgs) int {
	rows, err := globalDBReadRows(args.str("file"))
	if err != nil {
		fmt.Fprintf(r.Stderr, "error: %s\n", err.Error())
		return 1
	}
	total := 0
	for start := 0; start < len(rows); start += globalDBImportBatch {
		end := start + globalDBImportBatch
		if end > len(rows) {
			end = len(rows)
		}
		chunk := rows[start:end]
		value, code := r.runGlobalDBValue(func(ctx context.Context, client *globaldb.Client) (any, error) {
			return client.ImportRows(ctx, args.str("table"), chunk, args.str("source_colony"))
		})
		if code != 0 {
			return code
		}
		total += globalDBResultCount(value, len(chunk))
		fmt.Fprintf(r.Stderr, "imported %d/%d\n", total, len(rows))
	}
	writeJSONOut(r.Stdout, globalDBImportSummary{Success: true, Count: total}, true)
	return 0
}

type globalDBImportSummary struct {
	Success bool `json:"success"`
	Count   int  `json:"count"`
}

func globalDBResultCount(value any, fallback int) int {
	body, ok := value.(map[string]any)
	if !ok {
		return fallback
	}
	switch typed := body["count"].(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(typed); err == nil {
			return parsed
		}
	}
	return fallback
}

// Parity: framework/global_db/cli.py::_read_rows
func globalDBReadRows(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".json") {
		return globalDBDecodeJSONRows(data)
	}
	return globalDBDecodeCSVRows(data)
}

var errGlobalDBImportShape = errors.New("JSON import file must be an array of row objects")

func globalDBDecodeJSONRows(data []byte) ([]map[string]any, error) {
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	list, ok := decoded.([]any)
	if !ok {
		return nil, errGlobalDBImportShape
	}
	rows := make([]map[string]any, 0, len(list))
	for _, item := range list {
		row, ok := item.(map[string]any)
		if !ok {
			return nil, errGlobalDBImportShape
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// globalDBDecodeCSVRows matches csv.DictReader: the first record is the
// header, short records get nil for the missing columns, and blank lines are
// skipped (FieldsPerRecord = -1 disables Go's stricter arity check).
func globalDBDecodeCSVRows(data []byte) ([]map[string]any, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []map[string]any
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(header))
		for index, name := range header {
			if index < len(record) {
				row[name] = record[index]
				continue
			}
			row[name] = nil
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// Parity: framework/global_db/cli.py::cmd_export
func (r *Runner) cmdGlobalDBExport(args *parsedArgs) int {
	client, err := globaldb.NewClientFromEnv()
	if err != nil {
		return r.reportGlobalDBError(err)
	}
	table := args.str("table")
	offset := 0
	columnsSeen := false
	var columns []string
	allRows := []map[string]any{}
	for {
		params := url.Values{
			"limit":  {strconv.Itoa(globalDBExportPage)},
			"offset": {strconv.Itoa(offset)},
		}
		result, err := client.ListRows(context.Background(), table, params)
		if err != nil {
			return r.reportGlobalDBError(err)
		}
		body, _ := result.(map[string]any)
		rows := globalDBRowMaps(body["rows"])
		if !columnsSeen {
			columns = globalDBColumnNames(body["columns"])
			columnsSeen = true
		}
		allRows = append(allRows, rows...)
		if len(rows) < globalDBExportPage {
			break
		}
		offset += globalDBExportPage
	}
	if columns == nil {
		columns = []string{}
	}
	if args.str("format") == "csv" {
		return r.writeGlobalDBCSV(columns, allRows)
	}
	writeJSONOut(r.Stdout, allRows, true)
	return 0
}

func globalDBRowMaps(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	rows := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if row, ok := item.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func globalDBColumnNames(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return []string{}
	}
	names := make([]string, 0, len(list))
	for _, item := range list {
		column, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, ok := column["name"].(string)
		if !ok {
			continue
		}
		names = append(names, name)
	}
	return names
}

// writeGlobalDBCSV mirrors csv.DictWriter: the column set is the first page's
// header, missing values render empty, and the excel dialect's CRLF line
// terminator is preserved.
func (r *Runner) writeGlobalDBCSV(columns []string, rows []map[string]any) int {
	writer := csv.NewWriter(r.Stdout)
	writer.UseCRLF = true
	if err := writer.Write(columns); err != nil {
		return r.reportGlobalDBCSVError(err)
	}
	for _, row := range rows {
		record := make([]string, len(columns))
		for index, column := range columns {
			record[index] = globalDBCSVValue(row[column])
		}
		if err := writer.Write(record); err != nil {
			return r.reportGlobalDBCSVError(err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return r.reportGlobalDBCSVError(err)
	}
	return 0
}

func (r *Runner) reportGlobalDBCSVError(err error) int {
	fmt.Fprintf(r.Stderr, "error: %s\n", err.Error())
	return 1
}

func globalDBCSVValue(value any) string {
	if value == nil {
		return ""
	}
	switch value.(type) {
	case map[string]any, []any:
		return pyRepr(value)
	}
	return pyStr(value)
}
