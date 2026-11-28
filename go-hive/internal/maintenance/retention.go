package maintenance

// Parity: framework/maintenance/retention.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity: framework/maintenance/retention.py::KEEP_ALWAYS
var KeepAlways = map[string]bool{
	"memories":           true,
	"charts":             true,
	"credentials":        true,
	"secrets":            true,
	"mcp_registry":       true,
	"configuration.json": true,
	"runtime_id":         true,
	"sentinel_secret":    true,
	"desktop":            true,
	"skills":             true,
	"workspace":          true,
	"sessions":           true,
	"archive":            true,
	"maintenance":        true,
}

// Parity: framework/maintenance/retention.py::KNOWN_ENTRIES
var KnownEntries = map[string]bool{
	"queens":          true,
	"colonies":        true,
	"agents":          true,
	"event_logs":      true,
	"llm_logs":        true,
	"compaction_log":  true,
	"tool-artifacts":  true,
	"failed_requests": true,
	"logs":            true,
	".message_index":  true,
	".janitor":        true,
}

const JanitorLockName = ".janitor.lock"

const SessionLockTTLSeconds = 3600.0

const CompactionStaleSeconds = 1800.0

const EventsTmpSuffix = ".janitor-tmp"

var WorkerKeepFiles = map[string]bool{
	"meta.json":   true,
	"tasks.json":  true,
	"result.json": true,
}

const TombstoneSummaryCap = 1000

var (
	reSessionTS    = regexp.MustCompile(`^session_(\d{8})_(\d{6})_`)
	reLogTS        = regexp.MustCompile(`^(\d{8})_(\d{6})\.jsonl$`)
	reCompactionTS = regexp.MustCompile(`^(\d{8})T(\d{6})_\d+`)
	reEpochMS      = regexp.MustCompile(`_(\d{13})[_.]`)
)

// Parity: framework/maintenance/retention.py::parse_session_dir_ts
func ParseSessionDirTS(name string) (float64, bool) {
	m := reSessionTS.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	return parseLocalStamp(m[1] + m[2])
}

func parseLocalStamp(value string) (float64, bool) {
	t, err := time.ParseInLocation("20060102150405", value, time.Local)
	if err != nil {
		return 0, false
	}
	return float64(t.UnixNano()) / float64(time.Second), true
}

// Parity: framework/maintenance/retention.py::_read_json
func ReadJSONObject(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

// Parity: framework/maintenance/retention.py::_safe_stat_mtime
func SafeStatMtime(path string) float64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(info.ModTime().UnixNano()) / float64(time.Second)
}

// Parity: framework/maintenance/retention.py::_dir_size
func DirSize(path string) (int, int64) {
	files := 0
	var total int64
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			files++
			total += info.Size()
		}
		return nil
	})
	return files, total
}

func resolveLongestExisting(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	abs = filepath.Clean(abs)
	existing := abs
	var tail []string
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			break
		}
		tail = append([]string{filepath.Base(existing)}, tail...)
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		resolved = existing
	}
	return filepath.Join(append([]string{resolved}, tail...)...)
}

// Parity: framework/maintenance/retention.py::assert_safe_target
func AssertSafeTarget(home hivehome.Home, path string) error {
	homeResolved := resolveLongestExisting(home.Root)
	target := resolveLongestExisting(path)
	rel, err := filepath.Rel(homeResolved, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("janitor target escapes HIVE_HOME: %s", path)
	}
	if rel != "." {
		first := strings.Split(rel, string(filepath.Separator))[0]
		if KeepAlways[first] {
			return fmt.Errorf("janitor refusing protected target: %s", path)
		}
	}
	return nil
}

// Parity: framework/maintenance/retention.py::PruneItem
type PruneItem struct {
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Tier    int    `json:"tier"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

func NewPruneItem(path string, bytes int64, tier int, target, action, reason string) PruneItem {
	return PruneItem{
		Path:    path,
		Bytes:   bytes,
		Tier:    tier,
		Target:  target,
		Action:  action,
		Reason:  reason,
		Outcome: "candidate",
	}
}

// Parity: framework/maintenance/retention.py::TargetReport
type TargetReport struct {
	Name          string   `json:"name"`
	Tier          int      `json:"tier"`
	Files         int      `json:"files"`
	BytesFreed    int64    `json:"bytes_freed"`
	ArchivedFiles int      `json:"archived_files"`
	Skipped       int      `json:"skipped"`
	Errors        []string `json:"errors"`
}

// Parity: framework/maintenance/retention.py::TargetReport.to_dict
func (r TargetReport) ToMap() map[string]any {
	errs := r.Errors
	if len(errs) > 20 {
		errs = errs[:20]
	}
	list := make([]any, 0, len(errs))
	for _, e := range errs {
		list = append(list, e)
	}
	return map[string]any{
		"name":           r.Name,
		"tier":           r.Tier,
		"files":          r.Files,
		"bytes_freed":    r.BytesFreed,
		"archived_files": r.ArchivedFiles,
		"skipped":        r.Skipped,
		"errors":         list,
	}
}

// Parity: framework/maintenance/retention.py::Manifest
type Manifest struct {
	Items []PruneItem
}

func (m *Manifest) Add(item PruneItem) {
	if m == nil {
		return
	}
	m.Items = append(m.Items, item)
}

// Parity: framework/maintenance/retention.py::Disposer
type Disposer interface {
	DryRun() bool
	DisposeFile(path string) (int64, error)
	DisposeDir(path string) (int, int64, error)
}

func Archives(d Disposer) bool {
	type archiver interface{ Archives() bool }
	if a, ok := d.(archiver); ok {
		return a.Archives()
	}
	return false
}

// Parity: framework/maintenance/retention.py::DryRunDisposer
type DryRunDisposer struct{}

func (DryRunDisposer) DryRun() bool { return true }

func (DryRunDisposer) DisposeFile(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, nil
	}
	return info.Size(), nil
}

func (DryRunDisposer) DisposeDir(path string) (int, int64, error) {
	files, total := DirSize(path)
	return files, total, nil
}

// Parity: framework/maintenance/retention.py::DeleteDisposer
type DeleteDisposer struct {
	Home hivehome.Home
}

func (DeleteDisposer) DryRun() bool { return false }

func (d DeleteDisposer) DisposeFile(path string) (int64, error) {
	if err := AssertSafeTarget(d.Home, path); err != nil {
		return 0, err
	}
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	return size, nil
}

func (d DeleteDisposer) DisposeDir(path string) (int, int64, error) {
	if err := AssertSafeTarget(d.Home, path); err != nil {
		return 0, 0, err
	}
	files, total := DirSize(path)
	trash := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.jtrash-%d", filepath.Base(path), time.Now().Unix()))
	if err := os.Rename(path, trash); err != nil {
		trash = path
	}
	_ = os.RemoveAll(trash)
	return files, total, nil
}

// Parity: framework/maintenance/retention.py::SafetyContext
type SafetyContext struct {
	ProtectedSessionIDs map[string]bool
	LiveSessionDirs     map[string]bool
	GraceSeconds        float64
	Now                 float64
	Offline             bool
	Refresher           func() (map[string]bool, map[string]bool)
}

func (s *SafetyContext) NowSeconds() float64 {
	if s.Now > 0 {
		return s.Now
	}
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

// Parity: framework/maintenance/retention.py::SafetyContext.refresh
func (s *SafetyContext) Refresh() {
	if s.Refresher == nil {
		return
	}
	protected, live := func() (p, l map[string]bool) {
		defer func() { _ = recover() }()
		return s.Refresher()
	}()
	if protected == nil {
		protected = map[string]bool{}
	}
	if live == nil {
		live = map[string]bool{}
	}
	s.ProtectedSessionIDs = protected
	s.LiveSessionDirs = live
}

// Parity: framework/maintenance/retention.py::SafetyContext.for_offline
func ForOffline(cfg RetentionConfig, now float64) *SafetyContext {
	return &SafetyContext{
		ProtectedSessionIDs: map[string]bool{},
		LiveSessionDirs:     map[string]bool{},
		GraceSeconds:        float64(cfg.ActiveGraceHours) * 3600.0,
		Now:                 now,
		Offline:             true,
	}
}

// Parity: framework/maintenance/retention.py::SafetyContext.last_activity
func (s *SafetyContext) LastActivity(sessionDir string) float64 {
	candidates := []float64{0}
	if ts, ok := ParseSessionDirTS(filepath.Base(sessionDir)); ok {
		candidates = append(candidates, ts)
	}
	summary := ReadJSONObject(filepath.Join(sessionDir, "summary.json"))
	if v := summary["last_active_at"]; v != nil {
		if f, ok := toFloat(v); ok {
			candidates = append(candidates, f)
		}
	}
	candidates = append(candidates,
		SafeStatMtime(filepath.Join(sessionDir, "events.jsonl")),
		SafeStatMtime(filepath.Join(sessionDir, "conversations", "cursor.json")),
		SafeStatMtime(filepath.Join(sessionDir, "conversations", "parts")),
	)
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c > best {
			best = c
		}
	}
	return best
}

// Parity: framework/maintenance/retention.py::SafetyContext.is_safe_to_prune
func (s *SafetyContext) IsSafeToPrune(sessionDir string, minAgeDays float64, ignoreJanitorLock bool) (bool, string) {
	now := s.NowSeconds()
	sid := filepath.Base(sessionDir)
	if s.ProtectedSessionIDs[sid] {
		return false, "live in SessionManager"
	}
	if s.LiveSessionDirs[sessionDir] {
		return false, "dir belongs to a live session"
	}
	status := ReadJSONObject(filepath.Join(sessionDir, "compaction_status.json"))
	if st, _ := status["status"].(string); st == "in_progress" {
		started, ok := parseISOTs(status["started_at"])
		if !ok || now-started < CompactionStaleSeconds {
			return false, "fork-compaction in progress"
		}
	}
	if !ignoreJanitorLock && FreshLockExists(filepath.Join(sessionDir, JanitorLockName), now, SessionLockTTLSeconds) {
		return false, "another janitor holds the session lock"
	}
	ageGate := minAgeDays * 86400.0
	idle := now - s.LastActivity(sessionDir)
	gate := gateMax(ageGate, s.GraceSeconds)
	if idle < gate {
		return false, fmt.Sprintf("active %.1fd ago (< gate)", idle/86400.0)
	}
	partials := filepath.Join(sessionDir, "conversations", "partials")
	if hasEntries(partials) && idle < 2*ageGate {
		return false, "in-flight partials present (< 2x gate)"
	}
	return true, ""
}

func gateMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func hasEntries(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Readdirnames(1); err != nil {
		return false
	}
	return true
}

func parseISOTs(v any) (float64, bool) {
	text, ok := v.(string)
	if !ok {
		return 0, false
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	} {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return float64(t.UnixNano()) / float64(time.Second), true
		}
	}
	if t, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return float64(t.UnixNano()) / float64(time.Second), true
	}
	return 0, false
}

func ParseISOTs(v any) (float64, bool) { return parseISOTs(v) }

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// Parity: framework/maintenance/retention.py::_fresh_lock_exists
func FreshLockExists(lockPath string, now, ttl float64) bool {
	return now-SafeStatMtime(lockPath) < ttl
}

// Parity: framework/maintenance/retention.py::acquire_session_lock
func AcquireSessionLock(sessionDir string, now float64) bool {
	lockPath := filepath.Join(sessionDir, JanitorLockName)
	if FreshLockExists(lockPath, now, SessionLockTTLSeconds) {
		return false
	}
	_ = os.Remove(lockPath)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	data, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "ts": now})
	_, _ = f.Write(data)
	_ = f.Close()
	return true
}

// Parity: framework/maintenance/retention.py::release_session_lock
func ReleaseSessionLock(sessionDir string) {
	_ = os.Remove(filepath.Join(sessionDir, JanitorLockName))
}

// Parity: framework/maintenance/retention.py::_tier1_file_ts
func tier1FileTS(name string) (float64, bool) {
	if m := reLogTS.FindStringSubmatch(name); m != nil {
		return parseLocalStamp(m[1] + m[2])
	}
	if m := reCompactionTS.FindStringSubmatch(name); m != nil {
		return parseLocalStamp(m[1] + m[2])
	}
	if m := reEpochMS.FindStringSubmatch(name); m != nil {
		n, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return float64(n) / 1000.0, true
	}
	return 0, false
}

type PruneAgedDirOptions struct {
	Dir            string
	Target         string
	MaxAgeDays     float64
	Disposer       Disposer
	Manifest       *Manifest
	KeepNewest     int
	ProcessStartTS *float64
	Now            float64
	Pace           func()
}

// Parity: framework/maintenance/retention.py::prune_aged_dir
func PruneAgedDir(opts PruneAgedDirOptions) TargetReport {
	report := TargetReport{Name: opts.Target, Tier: 1}
	now := opts.Now
	if now == 0 {
		now = float64(time.Now().UnixNano()) / float64(time.Second)
	}
	info, err := os.Stat(opts.Dir)
	if err != nil || !info.IsDir() {
		return report
	}
	entries, err := os.ReadDir(opts.Dir)
	if err != nil {
		return report
	}
	type entry struct {
		ts   float64
		path string
	}
	var list []entry
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		p := filepath.Join(opts.Dir, de.Name())
		ts, ok := tier1FileTS(de.Name())
		if !ok {
			ts = SafeStatMtime(p)
		}
		list = append(list, entry{ts: ts, path: p})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ts != list[j].ts {
			return list[i].ts > list[j].ts
		}
		return list[i].path > list[j].path
	})
	cutoff := now - opts.MaxAgeDays*86400.0
	for i, e := range list {
		if i < opts.KeepNewest {
			continue
		}
		mtime := SafeStatMtime(e.path)
		if opts.ProcessStartTS != nil && mtime >= *opts.ProcessStartTS {
			continue
		}
		maxts := e.ts
		if mtime > maxts {
			maxts = mtime
		}
		if maxts >= cutoff {
			continue
		}
		item := NewPruneItem(e.path, 0, 1, opts.Target, "delete", "older than "+formatG(opts.MaxAgeDays)+"d")
		freed, derr := opts.Disposer.DisposeFile(e.path)
		switch {
		case derr != nil && errors.Is(derr, fs.ErrPermission):
			item.Outcome = "skipped"
			item.Error = "permission denied (open handle?)"
			report.Skipped++
		case derr != nil:
			item.Outcome = "error"
			item.Error = derr.Error()
			report.Errors = append(report.Errors, filepath.Base(e.path)+": "+derr.Error())
		default:
			item.Bytes = freed
			if opts.Disposer.DryRun() {
				item.Outcome = "candidate"
			} else {
				item.Outcome = "done"
			}
			report.Files++
			report.BytesFreed += freed
		}
		opts.Manifest.Add(item)
		if opts.Pace != nil {
			opts.Pace()
		}
	}
	return report
}

func formatG(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Parity: framework/maintenance/retention.py::iter_worker_sessions
func IterWorkerSessions(home hivehome.Home) [][3]string {
	var out [][3]string
	coloniesDir := home.ColoniesDir()
	if !isDir(coloniesDir) {
		return out
	}
	colonies, err := readDirNames(coloniesDir, true)
	if err != nil {
		return out
	}
	for _, colony := range colonies {
		workersDir := filepath.Join(coloniesDir, colony, "workers")
		if !isDir(workersDir) {
			continue
		}
		workers, err := readDirNames(workersDir, true)
		if err != nil {
			continue
		}
		for _, w := range workers {
			if strings.HasSuffix(w, "~") || strings.Contains(w, ".jtrash-") {
				continue
			}
			out = append(out, [3]string{colony, w, filepath.Join(workersDir, w)})
		}
	}
	return out
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func readDirNames(dir string, dirsOnly bool) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, de := range entries {
		if dirsOnly && !de.IsDir() {
			continue
		}
		names = append(names, de.Name())
	}
	sort.Strings(names)
	return names, nil
}

// Parity: framework/maintenance/retention.py::_extract_worker_summary
func ExtractWorkerSummary(wdir string) (string, string) {
	partsDir := filepath.Join(wdir, "conversations", "parts")
	entries, err := os.ReadDir(partsDir)
	if err != nil {
		return "unknown", ""
	}
	var files []string
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			continue
		}
		files = append(files, de.Name())
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))
	lastRole := ""
	for _, name := range files {
		data := ReadJSONObject(filepath.Join(partsDir, name))
		if lastRole == "" {
			if role, ok := data["role"].(string); ok {
				lastRole = role
			}
		}
		role, _ := data["role"].(string)
		if role == "assistant" {
			content, ok := data["content"].(string)
			if ok && strings.TrimSpace(content) != "" {
				summary := truncateRunes(strings.TrimSpace(content), TombstoneSummaryCap)
				status := "unknown"
				if lastRole == "assistant" {
					status = "completed"
				}
				return status, summary
			}
		}
	}
	return "unknown", ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Parity: framework/maintenance/retention.py::_delete_message_index_subtrees
func DeleteMessageIndexSubtrees(home hivehome.Home, sessionID string, disposer Disposer, manifest *Manifest, tier int, target string) int64 {
	indexRoot := filepath.Join(home.Root, ".message_index")
	var freed int64
	for _, tree := range []string{"events", "data", "meta"} {
		base := filepath.Join(indexRoot, tree)
		if !isDir(base) {
			continue
		}
		for _, sub := range globTwoThen(indexRoot, tree, sessionID) {
			item := NewPruneItem(sub, 0, tier, target, "delete", "message index copy of pruned session")
			_, bytes, err := disposer.DisposeDir(sub)
			if err != nil {
				item.Outcome = "error"
				item.Error = err.Error()
			} else {
				item.Bytes = bytes
				if disposer.DryRun() {
					item.Outcome = "candidate"
				} else {
					item.Outcome = "done"
				}
				freed += bytes
			}
			manifest.Add(item)
		}
	}
	return freed
}

func globTwoThen(root, tree, name string) []string {
	base := filepath.Join(root, tree)
	var out []string
	first, err := readDirNames(base, true)
	if err != nil {
		return out
	}
	for _, a := range first {
		second, err := readDirNames(filepath.Join(base, a), true)
		if err != nil {
			continue
		}
		for _, b := range second {
			candidate := filepath.Join(base, a, b, name)
			if _, err := os.Lstat(candidate); err == nil {
				out = append(out, candidate)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Parity: framework/maintenance/retention.py::deep_clean_worker
func DeepCleanWorker(home hivehome.Home, colonyID, workerID, wdir string, disposer Disposer, manifest *Manifest) TargetReport {
	report := TargetReport{Name: "worker_deep_clean", Tier: 2}
	resultPath := filepath.Join(wdir, "result.json")

	tombstoneExists := pathExists(resultPath)
	alreadyCleaned := tombstoneExists && !pathExists(filepath.Join(wdir, "conversations"))
	if alreadyCleaned {
		return report
	}

	if !tombstoneExists {
		status, summary := ExtractWorkerSummary(wdir)
		source := "synthesized"
		if summary != "" {
			source = "worker_parts"
		}
		tombstone := map[string]any{
			"status":           status,
			"summary":          summary,
			"error":            nil,
			"tokens_used":      0,
			"duration_seconds": 0.0,
			"_janitor": map[string]any{
				"pruned_at": time.Now().Format("2006-01-02T15:04:05-07:00"),
				"source":    source,
			},
		}
		if !disposer.DryRun() {
			data, err := marshalNoEscape(tombstone)
			// retention.py writes the tombstone through atomic_write (mkstemp
			// 0600); keep the preserved worker summary owner-only.
			if err != nil {
				report.Errors = append(report.Errors, workerID+"/result.json: "+err.Error())
			} else if err := atomicfs.WriteFile(resultPath, data, 0o600); err != nil {
				report.Errors = append(report.Errors, workerID+"/result.json: "+err.Error())
			}
		}
		outcome := "candidate"
		if !disposer.DryRun() {
			outcome = "done"
		}
		item := NewPruneItem(resultPath, 0, 2, "worker_deep_clean", "tombstone", "preserve status/summary for workers UI")
		item.Outcome = outcome
		manifest.Add(item)
	}

	report.BytesFreed += DeleteMessageIndexSubtrees(home, workerID, disposer, manifest, 2, "worker_deep_clean")

	var targets []string
	for _, sub := range []string{"conversations", "data"} {
		p := filepath.Join(wdir, sub)
		if pathExists(p) {
			targets = append(targets, p)
		}
	}
	if entries, err := os.ReadDir(wdir); err == nil {
		for _, de := range entries {
			if !de.IsDir() && !WorkerKeepFiles[de.Name()] {
				targets = append(targets, filepath.Join(wdir, de.Name()))
			}
		}
	}

	action := "delete"
	if Archives(disposer) {
		action = "archive"
	}
	for _, p := range targets {
		item := NewPruneItem(p, 0, 2, "worker_deep_clean", action, "finished worker in colony "+colonyID)
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			files, bytes, derr := disposer.DisposeDir(p)
			if derr != nil {
				item.Outcome = "error"
				item.Error = derr.Error()
				report.Errors = append(report.Errors, workerID+"/"+filepath.Base(p)+": "+derr.Error())
			} else {
				item.Bytes = bytes
				report.Files += files
				report.BytesFreed += bytes
				if disposer.DryRun() {
					item.Outcome = "candidate"
				} else {
					item.Outcome = "done"
				}
			}
		} else {
			bytes, derr := disposer.DisposeFile(p)
			if derr != nil {
				item.Outcome = "error"
				item.Error = derr.Error()
				report.Errors = append(report.Errors, workerID+"/"+filepath.Base(p)+": "+derr.Error())
			} else {
				item.Bytes = bytes
				report.Files++
				report.BytesFreed += bytes
				if disposer.DryRun() {
					item.Outcome = "candidate"
				} else {
					item.Outcome = "done"
				}
			}
		}
		manifest.Add(item)
	}
	return report
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Parity: framework/maintenance/retention.py::iter_queen_sessions
func IterQueenSessions(home hivehome.Home) []string {
	var out []string
	queensDir := home.QueensDir()
	if isDir(queensDir) {
		out = append(out, globDeepSessions(queensDir)...)
	}
	coloniesDir := home.ColoniesDir()
	if isDir(coloniesDir) {
		out = append(out, globColonyQueenSessions(coloniesDir)...)
	}
	return out
}

func globDeepSessions(dir string) []string {
	var out []string
	owners, err := readDirNames(dir, true)
	if err != nil {
		return out
	}
	for _, owner := range owners {
		sessionsDir := filepath.Join(dir, owner, "sessions")
		names, err := readDirNames(sessionsDir, true)
		if err != nil {
			continue
		}
		for _, name := range names {
			if strings.HasPrefix(name, "session_") {
				out = append(out, filepath.Join(sessionsDir, name))
			}
		}
	}
	sort.Strings(out)
	return out
}

func globColonyQueenSessions(coloniesDir string) []string {
	var out []string
	colonies, err := readDirNames(coloniesDir, true)
	if err != nil {
		return out
	}
	for _, colony := range colonies {
		queensDir := filepath.Join(coloniesDir, colony, "queens")
		owners, err := readDirNames(queensDir, true)
		if err != nil {
			continue
		}
		for _, owner := range owners {
			sessionsDir := filepath.Join(queensDir, owner, "sessions")
			names, err := readDirNames(sessionsDir, true)
			if err != nil {
				continue
			}
			for _, name := range names {
				if strings.HasPrefix(name, "session_") {
					out = append(out, filepath.Join(sessionsDir, name))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

var readTextFile = func(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Parity: framework/maintenance/retention.py::_spillover_reference_corpus
func SpilloverReferenceCorpus(sessionDir string) (string, bool) {
	var chunks []string
	conv := filepath.Join(sessionDir, "conversations")
	for _, sub := range []string{"parts", "partials"} {
		d := filepath.Join(conv, sub)
		if !isDir(d) {
			continue
		}
		names, err := readDirNames(d, false)
		if err != nil {
			return "", false
		}
		for _, name := range names {
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			text, err := readTextFile(filepath.Join(d, name))
			if err != nil {
				return "", false
			}
			chunks = append(chunks, text)
		}
	}
	cursor := filepath.Join(conv, "cursor.json")
	if pathExists(cursor) {
		text, err := readTextFile(cursor)
		if err != nil {
			return "", false
		}
		chunks = append(chunks, text)
	}
	return strings.Join(chunks, "\n"), true
}

// Parity: framework/maintenance/retention.py::find_orphan_spillovers
func FindOrphanSpillovers(sessionDir string, minAgeDays, now float64) []string {
	dataDir := filepath.Join(sessionDir, "data")
	if !isDir(dataDir) {
		return nil
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}
	var candidates []string
	for _, de := range entries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		if !(strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".json")) {
			continue
		}
		if strings.HasPrefix(name, "conversation_") {
			continue
		}
		candidates = append(candidates, filepath.Join(dataDir, name))
	}
	if len(candidates) == 0 {
		return nil
	}
	corpus, ok := SpilloverReferenceCorpus(sessionDir)
	if !ok {
		return nil
	}
	cutoff := now - minAgeDays*86400.0
	var orphans []string
	for _, p := range candidates {
		if strings.Contains(corpus, filepath.Base(p)) {
			continue
		}
		if SafeStatMtime(p) >= cutoff {
			continue
		}
		orphans = append(orphans, p)
	}
	return orphans
}

// Parity: framework/maintenance/retention.py::rewrite_events_jsonl
func RewriteEventsJSONL(eventsPath string, minBytes int64, disposer Disposer, manifest *Manifest) int64 {
	return RewriteEventsJSONLWithTypes(eventsPath, minBytes, disposer, manifest, map[string]bool{"context_usage_updated": true})
}

func RewriteEventsJSONLWithTypes(eventsPath string, minBytes int64, disposer Disposer, manifest *Manifest, dropTypes map[string]bool) int64 {
	pre, err := os.Stat(eventsPath)
	if err != nil {
		return 0
	}
	if pre.Size() < minBytes {
		return 0
	}

	data, err := os.ReadFile(eventsPath)
	if err != nil {
		return 0
	}
	var kept []string
	droppedLines := 0
	strippedFields := 0
	for _, line := range strings.Split(string(data), "\n") {
		stripped := strings.TrimSuffix(line, "\n")
		if stripped == "" {
			continue
		}
		var evt any
		if err := json.Unmarshal([]byte(stripped), &evt); err != nil {
			kept = append(kept, stripped)
			continue
		}
		obj, isObj := evt.(map[string]any)
		if isObj {
			if t, _ := obj["type"].(string); dropTypes[t] {
				droppedLines++
				continue
			}
		}
		var payload map[string]any
		if isObj {
			payload, _ = obj["data"].(map[string]any)
		}
		if payload != nil {
			if _, has := payload["full_request"]; has {
				delete(payload, "full_request")
				strippedFields++
				encoded, err := marshalNoEscape(obj)
				if err == nil {
					kept = append(kept, string(encoded))
					continue
				}
			}
		}
		kept = append(kept, stripped)
	}

	if droppedLines == 0 && strippedFields == 0 {
		return 0
	}

	body := strings.Join(kept, "\n")
	if len(kept) > 0 {
		body += "\n"
	}
	saved := pre.Size() - int64(len([]byte(body)))
	if float64(saved) < float64(pre.Size())*0.10 {
		return 0
	}

	reason := fmt.Sprintf("drop %d telemetry events, strip %d full_request", droppedLines, strippedFields)
	item := NewPruneItem(eventsPath, saved, 3, "events_rewrite", "rewrite", reason)
	if disposer.DryRun() {
		item.Outcome = "candidate"
		manifest.Add(item)
		return saved
	}

	tmp := eventsPath + EventsTmpSuffix
	if err := writeAndSync(tmp, body); err != nil {
		_ = os.Remove(tmp)
		item.Outcome = "error"
		item.Error = err.Error()
		manifest.Add(item)
		return 0
	}
	post, err := os.Stat(eventsPath)
	if err != nil || post.Size() != pre.Size() || post.ModTime().UnixNano() != pre.ModTime().UnixNano() {
		_ = os.Remove(tmp)
		item.Outcome = "skipped"
		item.Error = "source changed during rewrite"
		manifest.Add(item)
		return 0
	}
	if err := os.Rename(tmp, eventsPath); err != nil {
		_ = os.Remove(tmp)
		item.Outcome = "error"
		item.Error = err.Error()
		manifest.Add(item)
		return 0
	}
	item.Outcome = "done"
	manifest.Add(item)
	return saved
}

func writeAndSync(path, body string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Parity: framework/maintenance/retention.py::_reset_message_index_for_session
func ResetMessageIndexForSession(home hivehome.Home, sessionID string) {
	indexRoot := filepath.Join(home.Root, ".message_index")
	metaBase := filepath.Join(indexRoot, "meta")
	if !isDir(metaBase) {
		return
	}
	for _, metaDir := range globTwoThen(indexRoot, "meta", sessionID) {
		rel, err := filepath.Rel(metaBase, metaDir)
		if err != nil {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) < 2 {
			continue
		}
		scopeOwner := parts[:2]
		lockPath := filepath.Join(metaDir, ".lock")
		f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			continue
		}
		unlock := lockFile(f)
		for _, tree := range []string{"events", "data"} {
			sub := filepath.Join(indexRoot, tree, scopeOwner[0], scopeOwner[1], sessionID)
			_ = os.RemoveAll(sub)
		}
		_ = os.Remove(filepath.Join(metaDir, "cursor.json"))
		_ = os.Remove(filepath.Join(metaDir, "data_map.json"))
		unlock()
		_ = f.Close()
	}
}

// Parity: framework/maintenance/retention.py::prune_queen_session
func PruneQueenSession(home hivehome.Home, sessionDir string, cfg RetentionConfig, safety *SafetyContext, disposer Disposer, manifest *Manifest) TargetReport {
	report := TargetReport{Name: "queen_hygiene", Tier: 3}
	now := safety.NowSeconds()

	action := "delete"
	if Archives(disposer) {
		action = "archive"
	}
	for _, orphan := range FindOrphanSpillovers(sessionDir, float64(cfg.QueenHygieneDays), now) {
		item := NewPruneItem(orphan, 0, 3, "orphan_spillover", action, "spillover unreferenced by any live message")
		bytes, err := disposer.DisposeFile(orphan)
		if err != nil {
			item.Outcome = "error"
			item.Error = err.Error()
			report.Errors = append(report.Errors, filepath.Base(orphan)+": "+err.Error())
		} else {
			item.Bytes = bytes
			if disposer.DryRun() {
				item.Outcome = "candidate"
			} else {
				item.Outcome = "done"
			}
			report.Files++
			report.BytesFreed += bytes
		}
		manifest.Add(item)
	}

	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	if disposer.DryRun() {
		saved := RewriteEventsJSONL(eventsPath, cfg.EventsRewriteMin, disposer, manifest)
		if saved != 0 {
			report.BytesFreed += saved
			report.Files++
		}
		return report
	}

	if !AcquireSessionLock(sessionDir, now) {
		report.Skipped++
		return report
	}
	defer ReleaseSessionLock(sessionDir)

	safety.Refresh()
	if ok, _ := safety.IsSafeToPrune(sessionDir, float64(cfg.QueenHygieneDays), true); !ok {
		report.Skipped++
		return report
	}

	saved := RewriteEventsJSONL(eventsPath, cfg.EventsRewriteMin, disposer, manifest)
	if saved != 0 {
		report.BytesFreed += saved
		report.Files++
		ResetMessageIndexForSession(home, filepath.Base(sessionDir))
	}

	if report.Files != 0 || report.BytesFreed != 0 {
		pruned := map[string]any{
			"schema":      1,
			"pruned_at":   now,
			"bytes_freed": report.BytesFreed,
		}
		if data, err := json.Marshal(pruned); err == nil {
			_ = atomicfs.WriteFile(filepath.Join(sessionDir, ".pruned.json"), data, 0o600)
		}
	}
	return report
}

// Parity: framework/maintenance/retention.py::sweep_janitor_leftovers
func SweepJanitorLeftovers(home hivehome.Home, manifest *Manifest) TargetReport {
	report := TargetReport{Name: "leftover_sweep", Tier: 0}
	if !isDir(home.Root) {
		return report
	}
	now := float64(time.Now().UnixNano()) / float64(time.Second)
	var matches []string
	_ = filepath.WalkDir(home.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == home.Root {
			return nil
		}
		name := d.Name()
		if strings.HasSuffix(name, EventsTmpSuffix) || strings.Contains(name, ".jtrash-") {
			matches = append(matches, p)
		}
		return nil
	})
	for _, p := range matches {
		if now-SafeStatMtime(p) < SessionLockTTLSeconds {
			continue
		}
		var size int64
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		if info.IsDir() {
			_, size = DirSize(p)
			_ = os.RemoveAll(p)
		} else {
			size = info.Size()
			_ = os.Remove(p)
		}
		report.Files++
		report.BytesFreed += size
		manifest.Add(PruneItem{
			Path:    p,
			Bytes:   size,
			Tier:    0,
			Target:  "leftover_sweep",
			Action:  "delete",
			Reason:  "crash residue from prior janitor run",
			Outcome: "done",
		})
	}
	return report
}

// Parity: framework/maintenance/retention.py::sweep_orphan_message_index
func SweepOrphanMessageIndex(home hivehome.Home, disposer Disposer, manifest *Manifest) TargetReport {
	report := TargetReport{Name: "orphan_index_sweep", Tier: 0}
	indexRoot := filepath.Join(home.Root, ".message_index")
	metaBase := filepath.Join(indexRoot, "meta")
	if !isDir(metaBase) {
		return report
	}
	var metas []string
	first, _ := readDirNames(metaBase, true)
	for _, scope := range first {
		owners, _ := readDirNames(filepath.Join(metaBase, scope), true)
		for _, owner := range owners {
			sessions, _ := readDirNames(filepath.Join(metaBase, scope, owner), true)
			for _, session := range sessions {
				metas = append(metas, filepath.Join(metaBase, scope, owner, session))
			}
		}
	}
	sort.Strings(metas)
	for _, metaDir := range metas {
		rel, err := filepath.Rel(metaBase, metaDir)
		if err != nil {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 3 {
			continue
		}
		scope, owner, session := parts[0], parts[1], parts[2]
		var source string
		switch scope {
		case "queens":
			source = filepath.Join(home.Root, "agents", "queens", owner, "sessions", session, "events.jsonl")
		case "colonies":
			source = filepath.Join(home.Root, "colonies", owner, "sessions", session, "events.jsonl")
		default:
			continue
		}
		if pathExists(source) {
			continue
		}
		for _, tree := range []string{"events", "data", "meta"} {
			sub := filepath.Join(indexRoot, tree, scope, owner, session)
			if !pathExists(sub) {
				continue
			}
			item := NewPruneItem(sub, 0, 0, "orphan_index_sweep", "delete", "source session gone ("+source+")")
			_, bytes, derr := disposer.DisposeDir(sub)
			if derr != nil {
				item.Outcome = "error"
				item.Error = derr.Error()
				report.Errors = append(report.Errors, sub+": "+derr.Error())
			} else {
				item.Bytes = bytes
				if disposer.DryRun() {
					item.Outcome = "candidate"
				} else {
					item.Outcome = "done"
				}
				report.Files++
				report.BytesFreed += bytes
			}
			manifest.Add(item)
		}
	}
	return report
}

// Parity: framework/maintenance/retention.py::iter_legacy_queen_sessions
func IterLegacyQueenSessions(home hivehome.Home) []string {
	legacy := filepath.Join(home.Root, "agents", "queens")
	if !isDir(legacy) {
		return nil
	}
	return globDeepSessions(legacy)
}

type JunkEntry struct {
	Path  string
	Bytes int64
}

// Parity: framework/maintenance/retention.py::find_junk_entries
func FindJunkEntries(home hivehome.Home, minBytes int64) []JunkEntry {
	var out []JunkEntry
	if !isDir(home.Root) {
		return out
	}
	entries, err := os.ReadDir(home.Root)
	if err != nil {
		return out
	}
	var names []string
	for _, de := range entries {
		names = append(names, de.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		if KeepAlways[name] || KnownEntries[name] || strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(home.Root, name)
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		var size int64
		if info.IsDir() {
			_, size = DirSize(p)
		} else {
			size = info.Size()
		}
		if size >= minBytes {
			out = append(out, JunkEntry{Path: p, Bytes: size})
		}
	}
	return out
}
