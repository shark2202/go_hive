package playbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

func ExtractRowsResult(res map[string]any) map[string]any {
	if res == nil {
		return nil
	}
	switch kind, _ := res["kind"].(string); kind {
	case "rows":
		return res
	case "script":
		for _, sub := range reverseResults(res["results"]) {
			if sub["kind"] == "rows" {
				return sub
			}
		}
	}
	return nil
}

func reverseResults(raw any) []map[string]any {
	var results []map[string]any
	switch v := raw.(type) {
	case []any:
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				results = append(results, m)
			}
		}
	case []map[string]any:
		results = v
	}
	out := make([]map[string]any, 0, len(results))
	for i := len(results) - 1; i >= 0; i-- {
		out = append(out, results[i])
	}
	return out
}

func ErrorDetail(err error) (concise, full string) {
	if err == nil {
		return "", ""
	}
	full = err.Error()

	root := err
	var runtimeErr *ScriptRuntimeError
	for root != nil {
		var candidate *ScriptRuntimeError
		if errors.As(root, &candidate) {
			runtimeErr = candidate
		}
		root = errors.Unwrap(root)
	}
	if runtimeErr != nil {
		where := ""
		if runtimeErr.Line > 0 {
			where = fmt.Sprintf(" (playbook line %d)", runtimeErr.Line)
		}
		return fmt.Sprintf("%s: %s%s", runtimeErr.Kind, runtimeErr.Message, where), full
	}
	return fmt.Sprintf("%T: %v", err, err), full
}

func SanitizeName(name string) string {
	trimmed := strings.ToLower(strings.TrimSpace(fmt.Sprint(name)))
	var b strings.Builder
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	safe := strings.Trim(b.String(), "-")
	if safe == "" {
		safe = "playbook"
	}
	runes := []rune(safe)
	if len(runes) > 64 {
		safe = string(runes[:64])
	}
	return safe
}

func PersistScript(colonyDir, name, script string) (string, bool) {
	target := filepath.Join(colonyDir, "playbooks", SanitizeName(name)+".play.py")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", false
	}
	if err := os.WriteFile(target, []byte(script), 0o644); err != nil {
		return "", false
	}
	return target, true
}

func SavedPlaybookNames(playbooksDir string) []string {
	entries, err := os.ReadDir(playbooksDir)
	if err != nil {
		return []string{}
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type().IsRegular() && strings.HasSuffix(name, ".play.py") {
			out = append(out, strings.TrimSuffix(name, ".play.py"))
		}
	}
	sort.Strings(out)
	return out
}

func ResolveScript(playbook, playbookPath, playbookName *string, playbooksDir string) (script, sourcePath string, err error) {
	given := 0
	for _, p := range []*string{playbook, playbookPath, playbookName} {
		if truthyStringPtr(p) {
			given++
		}
	}
	if given > 1 {
		return "", "", errors.New("Pass exactly ONE of `playbook` (inline), `playbook_path`, or `playbook_name`.")
	}

	if truthyStringPtr(playbook) {
		return *playbook, "", nil
	}
	if truthyStringPtr(playbookPath) {
		p := expandUser(*playbookPath)
		info, statErr := os.Stat(p)
		if statErr != nil || info.IsDir() {
			return "", "", fmt.Errorf("playbook_path '%s' does not exist or is not a file.", *playbookPath)
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return "", "", fmt.Errorf("failed to read playbook_path: %v", readErr)
		}
		return string(data), p, nil
	}
	if truthyStringPtr(playbookName) {
		p := filepath.Join(playbooksDir, SanitizeName(*playbookName)+".play.py")
		info, statErr := os.Stat(p)
		if statErr != nil || info.IsDir() {
			saved := SavedPlaybookNames(playbooksDir)
			list := "(none yet)"
			if len(saved) > 0 {
				list = fmt.Sprint(saved)
			}
			return "", "", fmt.Errorf("no saved playbook named '%s'. Saved playbooks in this colony: %s.", *playbookName, list)
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return "", "", fmt.Errorf("failed to read saved playbook: %v", readErr)
		}
		return string(data), p, nil
	}
	return "", "", errors.New("Provide `playbook` (inline script), `playbook_path`, or `playbook_name`.")
}

func truthyStringPtr(p *string) bool {
	return p != nil && *p != ""
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func intEnv(name string, def int) int {
	raw, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	if v < 1 {
		return 1
	}
	return v
}

var (
	MaxPlaybookConcurrency     = intEnv("HIVE_MAX_PLAYBOOK_CONCURRENCY", 32)
	DefaultPlaybookConcurrency = intEnv("HIVE_DEFAULT_PLAYBOOK_CONCURRENCY", 8)
)

func ResolveConcurrency(meta map[string]any) (int, error) {
	requested, ok := meta["concurrency"]
	if !ok || requested == nil {
		requested = DefaultPlaybookConcurrency
	}
	n, err := pyInt(requested)
	if err != nil {
		return 0, fmt.Errorf("meta['concurrency'] must be an integer, got %s.", pyRepr(requested))
	}
	if n < 1 {
		n = 1
	}
	if n > MaxPlaybookConcurrency {
		return 0, fmt.Errorf(
			"meta['concurrency']=%d exceeds the maximum %d. Lower it, split the batch, or raise HIVE_MAX_PLAYBOOK_CONCURRENCY.",
			n, MaxPlaybookConcurrency)
	}
	return n, nil
}

func pyInt(v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case float32:
		return int(math.Trunc(float64(t))), nil
	case float64:
		return int(math.Trunc(t)), nil
	case json.Number:
		i, err := t.Int64()
		if err != nil {
			f, ferr := t.Float64()
			if ferr != nil {
				return 0, ferr
			}
			return int(math.Trunc(f)), nil
		}
		return int(i), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(t))
	case bool:
		if t {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("not an integer")
}

func pyRepr(v any) string {
	if s, ok := v.(string); ok {
		return "'" + s + "'"
	}
	return fmt.Sprint(v)
}

var PlaybookToolNames = []string{
	"run_playbook",
	"get_playbook_status",
	"stop_playbook",
	"list_playbook_runs",
}

var ColonyQueenTools = map[string]struct{}{
	"run_playbook":        {},
	"get_playbook_status": {},
	"stop_playbook":       {},
	"list_playbook_runs":  {},
	"run_worker":          {},
}

var IndependentQueenTools = map[string]struct{}{}

func QueenOnlyTools() map[string]struct{} {
	out := map[string]struct{}{}
	for name := range ColonyQueenTools {
		if _, independent := IndependentQueenTools[name]; !independent {
			out[name] = struct{}{}
		}
	}
	return out
}

func IsQueenOnlyPlaybookTool(name string) bool {
	if _, ok := ColonyQueenTools[name]; !ok {
		return false
	}
	_, independent := IndependentQueenTools[name]
	return !independent
}

type WorkerRef struct {
	ID      string
	BatchID string
	Active  bool
}

type BatchWorkerColony interface {
	Workers() []WorkerRef
	StopWorker(ctx context.Context, id string) error
}

func StopBatchWorkers(ctx context.Context, colony BatchWorkerColony, batchID string) int {
	if colony == nil {
		return 0
	}
	stopped := 0
	for _, worker := range colony.Workers() {
		if worker.BatchID != batchID || !worker.Active {
			continue
		}
		_ = colony.StopWorker(ctx, worker.ID)
		stopped++
	}
	return stopped
}

type RunningPlaybook struct {
	Cancel context.CancelFunc
	Done   <-chan struct{}
	Run    *PlaybookRun
	Colony any
}

var (
	runningMu      sync.Mutex
	runningOrder   []string
	runningEntries = map[string]*RunningPlaybook{}
)

func RegisterRunningPlaybook(runID string, entry *RunningPlaybook) {
	runningMu.Lock()
	defer runningMu.Unlock()
	if _, exists := runningEntries[runID]; !exists {
		runningOrder = append(runningOrder, runID)
	}
	runningEntries[runID] = entry
}

func UnregisterRunningPlaybook(runID string) {
	runningMu.Lock()
	defer runningMu.Unlock()
	delete(runningEntries, runID)
	kept := runningOrder[:0]
	for _, id := range runningOrder {
		if id != runID {
			kept = append(kept, id)
		}
	}
	runningOrder = kept
}

func GetRunningPlaybook(runID string) (*RunningPlaybook, bool) {
	runningMu.Lock()
	defer runningMu.Unlock()
	entry, ok := runningEntries[runID]
	return entry, ok
}

func StopPlaybooksForColony(colony any) []string {
	runningMu.Lock()
	targets := make([]string, 0)
	entries := make([]*RunningPlaybook, 0)
	for _, runID := range runningOrder {
		entry := runningEntries[runID]
		if entry == nil || !sameColony(entry.Colony, colony) {
			continue
		}
		targets = append(targets, runID)
		entries = append(entries, entry)
	}
	runningMu.Unlock()

	for _, entry := range entries {
		if entry.Cancel != nil {
			entry.Cancel()
		}
		if entry.Done != nil {
			select {
			case <-entry.Done:
			case <-time.After(2 * time.Second):
			}
		}
	}
	return targets
}

func sameColony(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Type() != vb.Type() {
		return false
	}
	switch va.Kind() {
	case reflect.Ptr, reflect.Chan, reflect.Func, reflect.Map, reflect.Slice,
		reflect.UnsafePointer, reflect.Interface:
		if va.IsNil() || vb.IsNil() {
			return va.IsNil() && vb.IsNil()
		}
		return va.Pointer() == vb.Pointer()
	}
	if va.Type().Comparable() {
		return a == b
	}
	return false
}
