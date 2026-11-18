package colony

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const playbookRunsSchema = `
CREATE TABLE IF NOT EXISTS playbook_runs (
    run_id        TEXT PRIMARY KEY,
    name          TEXT,
    status        TEXT NOT NULL,           -- 'done' | 'error'
    dispatched    INTEGER NOT NULL DEFAULT 0,
    dead_lettered INTEGER NOT NULL DEFAULT 0,
    error         TEXT,
    result_json   TEXT,                    -- the playbook run() return value
    logs_json     TEXT,                    -- the log() narration lines
    created_at    TEXT NOT NULL
);`

func DBPath(home hivehome.Home, colonyID string) string {
	return filepath.Join(home.ColonyDir(colonyID), "colony.db")
}

type PlaybookRunInput struct {
	RunID        string
	Name         *string
	Status       string
	Dispatched   int
	DeadLettered int
	Error        *string
	Result       any
	Logs         []string
}

type PlaybookRun struct {
	RunID        string
	Name         *string
	Status       string
	Dispatched   int
	DeadLettered int
	Error        *string
	ResultJSON   *string
	LogsJSON     *string
	CreatedAt    string
}

func connect(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(playbookRunsSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func RecordPlaybookRun(dbPath string, in PlaybookRunInput) error {
	db, err := connect(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var resultJSON any
	if in.Result != nil {
		resultJSON = pyJSONDumps(in.Result)
	}
	logs := in.Logs
	if logs == nil {
		logs = []string{}
	}
	logsJSON := pyJSONDumps(logs)
	createdAt := time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")

	_, err = db.Exec(`
INSERT OR REPLACE INTO playbook_runs
    (run_id, name, status, dispatched, dead_lettered, error,
     result_json, logs_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.RunID, in.Name, in.Status, in.Dispatched, in.DeadLettered,
		in.Error, resultJSON, logsJSON, createdAt,
	)
	return err
}

func ListPlaybookRuns(dbPath string, limit int) ([]PlaybookRun, error) {
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	db, err := connect(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT run_id, name, status, dispatched, dead_lettered,
        error, result_json, logs_json, created_at FROM playbook_runs
        ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []PlaybookRun
	for rows.Next() {
		var r PlaybookRun
		if err := rows.Scan(&r.RunID, &r.Name, &r.Status, &r.Dispatched,
			&r.DeadLettered, &r.Error, &r.ResultJSON, &r.LogsJSON, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func pyJSONDumps(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return pyJSONString(fmt.Sprint(value))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return pyJSONString(fmt.Sprint(value))
	}
	var buf strings.Builder
	writePyJSON(&buf, normalized)
	return buf.String()
}

func writePyJSON(buf *strings.Builder, value any) {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		buf.WriteString(pyJSONString(v))
	case json.Number:
		buf.WriteString(v.String())
	case float64:
		encoded, err := json.Marshal(v)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(encoded)
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteString("{")
		for i, key := range keys {
			if i > 0 {
				buf.WriteString(", ")
			}
			buf.WriteString(pyJSONString(key))
			buf.WriteString(": ")
			writePyJSON(buf, v[key])
		}
		buf.WriteString("}")
	case []any:
		buf.WriteString("[")
		for i, item := range v {
			if i > 0 {
				buf.WriteString(", ")
			}
			writePyJSON(buf, item)
		}
		buf.WriteString("]")
	default:
		buf.WriteString("null")
	}
}

func pyJSONString(value string) string {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
