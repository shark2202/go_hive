// File-based storage for the three runtime log levels.
//
// Parity: framework/tracker/runtime_log_store.py (module)
//
// Storage layout (identical to upstream):
//
//	{base_path}/
//	  sessions/
//	    {session_id}/
//	      logs/
//	        summary.json     # Level 1 — written once at end
//	        details.jsonl    # Level 2 — appended per node completion
//	        tool_logs.jsonl  # Level 3 — appended per step
//
// A base_path whose own name is "runtime_logs" is treated as the legacy
// caller-supplied subdirectory: its parent becomes the root, so the files
// still land under sessions/{run_id}/logs/.
package runtimelogs

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// RuntimeLogStore persists runtime logs at three levels.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore
type RuntimeLogStore struct {
	BasePath string
}

// NewRuntimeLogStore mirrors RuntimeLogStore(base_path).
func NewRuntimeLogStore(basePath string) *RuntimeLogStore {
	return &RuntimeLogStore{BasePath: filepath.Clean(basePath)}
}

// sessionLogsDir returns the unified session-backed logs directory for a run.
func (s *RuntimeLogStore) sessionLogsDir(runID string) string {
	root := s.BasePath
	if filepath.Base(s.BasePath) == "runtime_logs" {
		root = filepath.Dir(s.BasePath)
	}
	return filepath.Join(root, "sessions", runID, "logs")
}

// legacyRunDir returns the deprecated standalone runs directory for a run.
func (s *RuntimeLogStore) legacyRunDir(runID string) string {
	return filepath.Join(s.BasePath, "runs", runID)
}

// RunDir determines the run directory path based on the run_id format:
// session-backed runs use {storage_root}/sessions/{run_id}/logs/, anything
// else falls back to the deprecated {base_path}/runs/{run_id}/.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore._get_run_dir
func (s *RuntimeLogStore) RunDir(runID string) string {
	sessionRunDir := s.sessionLogsDir(runID)
	if _, err := os.Stat(sessionRunDir); err == nil {
		return sessionRunDir
	}
	if strings.HasPrefix(runID, "session_") {
		return sessionRunDir
	}
	return s.legacyRunDir(runID)
}

// EnsureRunDir creates the run directory immediately (start_run calls it).
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.ensure_run_dir
func (s *RuntimeLogStore) EnsureRunDir(runID string) error {
	return os.MkdirAll(s.RunDir(runID), 0o755)
}

// EnsureSessionRunDir creates the unified session-backed log directory.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.ensure_session_run_dir
func (s *RuntimeLogStore) EnsureSessionRunDir(runID string) error {
	return os.MkdirAll(s.sessionLogsDir(runID), 0o755)
}

// AppendStep appends one JSONL line to tool_logs.jsonl.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.append_step
func (s *RuntimeLogStore) AppendStep(runID string, step *NodeStepLog) error {
	path := filepath.Join(s.RunDir(runID), "tool_logs.jsonl")
	normalizeStep(step)
	line, err := marshalLine(step)
	if err != nil {
		return err
	}
	return appendFile(path, line)
}

// AppendNodeDetail appends one JSONL line to details.jsonl.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.append_node_detail
func (s *RuntimeLogStore) AppendNodeDetail(runID string, detail *NodeDetail) error {
	path := filepath.Join(s.RunDir(runID), "details.jsonl")
	normalizeDetail(detail)
	line, err := marshalLine(detail)
	if err != nil {
		return err
	}
	return appendFile(path, line)
}

// ReadNodeDetailsSync reads details.jsonl back into a list of NodeDetail.
// Corrupt lines are skipped (partial writes from crashes).
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.read_node_details_sync
func (s *RuntimeLogStore) ReadNodeDetailsSync(runID string) ([]NodeDetail, error) {
	path := filepath.Join(s.RunDir(runID), "details.jsonl")
	return readJSONL[NodeDetail](path)
}

// SaveSummary writes summary.json atomically (tmp + rename), once at end_run.
// The encoding is json.dumps(model_dump(), indent=2, ensure_ascii=False):
// two-space indent, no HTML escaping, no trailing newline.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.save_summary
func (s *RuntimeLogStore) SaveSummary(runID string, summary *RunSummaryLog) error {
	runDir := s.RunDir(runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	normalizeSummary(summary)
	content, err := marshalIndented(summary)
	if err != nil {
		return err
	}
	path := filepath.Join(runDir, "summary.json")
	tmp := strings.TrimSuffix(path, filepath.Ext(path)) + ".tmp"
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadSummary loads the L1 summary for a run. Missing or corrupt files return
// (nil, nil), like upstream's None.
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.load_summary
func (s *RuntimeLogStore) LoadSummary(runID string) (*RunSummaryLog, error) {
	data, err := s.readJSONFile(filepath.Join(s.RunDir(runID), "summary.json"))
	if err != nil || data == nil {
		return nil, err
	}
	var summary RunSummaryLog
	if err := json.Unmarshal(data, &summary); err != nil {
		log.Printf("runtimelogs: failed to read %s: %v", filepath.Join(s.RunDir(runID), "summary.json"), err)
		return nil, nil
	}
	return &summary, nil
}

// LoadDetails loads the L2 details from details.jsonl. A missing file returns
// (nil, nil).
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.load_details
func (s *RuntimeLogStore) LoadDetails(runID string) (*RunDetailsLog, error) {
	path := filepath.Join(s.RunDir(runID), "details.jsonl")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	nodes, err := readJSONL[NodeDetail](path)
	if err != nil {
		return nil, err
	}
	if nodes == nil {
		nodes = []NodeDetail{}
	}
	return &RunDetailsLog{RunID: runID, Nodes: nodes}, nil
}

// LoadToolLogs loads the L3 tool logs from tool_logs.jsonl. A missing file
// returns (nil, nil).
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.load_tool_logs
func (s *RuntimeLogStore) LoadToolLogs(runID string) (*RunToolLogs, error) {
	path := filepath.Join(s.RunDir(runID), "tool_logs.jsonl")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	steps, err := readJSONL[NodeStepLog](path)
	if err != nil {
		return nil, err
	}
	if steps == nil {
		steps = []NodeStepLog{}
	}
	return &RunToolLogs{RunID: runID, Steps: steps}, nil
}

// ListRuns scans both directory structures, loads summaries, filters and
// sorts by started_at descending. Directories without summary.json are
// treated as in-progress runs and get a synthetic summary with
// status="in_progress".
//
// Parity: framework/tracker/runtime_log_store.py::RuntimeLogStore.list_runs
func (s *RuntimeLogStore) ListRuns(status string, needsAttention *bool, limit int) ([]RunSummaryLog, error) {
	runIDs, err := s.scanRunDirs()
	if err != nil {
		return nil, err
	}
	summaries := []RunSummaryLog{}
	for _, runID := range runIDs {
		summary, err := s.LoadSummary(runID)
		if err != nil {
			return nil, err
		}
		if summary == nil {
			// In-progress run: no summary.json yet. Synthesize one.
			runDir := s.RunDir(runID)
			if info, statErr := os.Stat(runDir); statErr != nil || !info.IsDir() {
				continue
			}
			summary = &RunSummaryLog{
				RunID:     runID,
				Status:    "in_progress",
				StartedAt: inferStartedAt(runID),
			}
		}
		if status != "" && status != "needs_attention" && summary.Status != status {
			continue
		}
		if status == "needs_attention" && !summary.NeedsAttention {
			continue
		}
		if needsAttention != nil && summary.NeedsAttention != *needsAttention {
			continue
		}
		summaries = append(summaries, *summary)
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].StartedAt > summaries[j].StartedAt
	})
	if limit >= 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return summaries, nil
}

// scanRunDirs returns the run_id directory names from both the new sessions
// tree and the deprecated runs tree.
func (s *RuntimeLogStore) scanRunDirs() ([]string, error) {
	runIDs := []string{}
	root := s.BasePath
	if filepath.Base(s.BasePath) == "runtime_logs" {
		root = filepath.Dir(s.BasePath)
	}
	sessionsDir := filepath.Join(root, "sessions")
	if entries, err := os.ReadDir(sessionsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			logsDir := filepath.Join(sessionsDir, entry.Name(), "logs")
			if info, statErr := os.Stat(logsDir); statErr == nil && info.IsDir() {
				runIDs = append(runIDs, entry.Name())
			}
		}
	}
	oldRunsDir := filepath.Join(s.BasePath, "runs")
	if entries, err := os.ReadDir(oldRunsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				runIDs = append(runIDs, entry.Name())
			}
		}
	}
	return runIDs, nil
}

// readJSONFile reads and parses a JSON file. (nil, nil) when missing or
// corrupt, matching upstream's _read_json.
func (s *RuntimeLogStore) readJSONFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if !json.Valid(data) {
		log.Printf("runtimelogs: failed to read %s: invalid JSON", path)
		return nil, nil
	}
	return data, nil
}

// readJSONL parses a JSONL file into a slice, skipping blank and corrupt
// lines (the crash-resilience contract).
//
// Parity: framework/tracker/runtime_log_store.py::_read_jsonl_as_models
func readJSONL[T any](path string) ([]T, error) {
	results := []T{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return results, nil
		}
		log.Printf("runtimelogs: failed to read %s: %v", path, err)
		return results, nil
	}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		var item T
		if err := json.Unmarshal(line, &item); err != nil {
			log.Printf("runtimelogs: skipping corrupt JSONL line in %s: %v", path, err)
			continue
		}
		results = append(results, item)
	}
	return results, nil
}

// appendFile appends one pre-encoded line to path, creating nothing else:
// upstream's open(path, "a") fails when the run directory was never created.
func appendFile(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for len(line) > 0 {
		n, err := f.Write(line)
		if err != nil {
			return err
		}
		line = line[n:]
	}
	return nil
}

// marshalLine encodes one JSONL record like json.dumps(..., ensure_ascii=False)
// plus the trailing newline the writer appends. HTML escaping is disabled to
// match Python's output for text containing <, > or &.
func marshalLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// marshalIndented encodes a document like json.dumps(..., indent=2,
// ensure_ascii=False): two-space indent, no trailing newline.
func marshalIndented(v any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// inferStartedAt is the best-effort ISO timestamp from a run_id like
// '20250101T120000_abc12345'.
//
// Parity: framework/tracker/runtime_log_store.py::_infer_started_at
func inferStartedAt(runID string) string {
	tsPart := runID
	if idx := strings.Index(runID, "_"); idx >= 0 {
		tsPart = runID[:idx]
	}
	parsed, err := time.ParseInLocation("20060102T150405", tsPart, time.UTC)
	if err != nil {
		return ""
	}
	return parsed.UTC().Format("2006-01-02T15:04:05") + "+00:00"
}
