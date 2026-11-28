package maintenance

// Parity: framework/maintenance/janitor.py

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const GlobalLockTTLSeconds = 2 * 3600.0

const (
	ReportFilename       = "last_janitor_report.json"
	ServerMarkerFilename = "server.lock"
)

// Parity: framework/maintenance/janitor.py::JanitorReport
type JanitorReport struct {
	StartedAt    float64
	FinishedAt   float64
	DryRun       bool
	Mode         string
	Tiers        []int
	Targets      []TargetReport
	ManifestPath string
	ArchivePath  string
	Error        string
}

func (r *JanitorReport) TotalBytes() int64 {
	var total int64
	for _, t := range r.Targets {
		total += t.BytesFreed
	}
	return total
}

func (r *JanitorReport) TotalFiles() int {
	total := 0
	for _, t := range r.Targets {
		total += t.Files
	}
	return total
}

func (r *JanitorReport) ToMap() map[string]any {
	targets := make([]any, 0, len(r.Targets))
	for _, t := range r.Targets {
		targets = append(targets, t.ToMap())
	}
	tiers := r.Tiers
	if tiers == nil {
		tiers = []int{}
	}
	return map[string]any{
		"started_at":    r.StartedAt,
		"finished_at":   r.FinishedAt,
		"dry_run":       r.DryRun,
		"mode":          r.Mode,
		"tiers":         tiers,
		"total_bytes":   r.TotalBytes(),
		"total_files":   r.TotalFiles(),
		"targets":       targets,
		"manifest_path": r.ManifestPath,
		"archive_path":  r.ArchivePath,
		"error":         r.Error,
	}
}

func MaintenanceDir(home hivehome.Home) string {
	return filepath.Join(home.Root, "maintenance")
}

func ServerMarkerPath(home hivehome.Home) string {
	return filepath.Join(MaintenanceDir(home), ServerMarkerFilename)
}

// Parity: framework/maintenance/janitor.py::write_server_marker
func WriteServerMarker(home hivehome.Home, port *int) {
	path := ServerMarkerPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("janitor: failed to write server marker: %v", err)
		return
	}
	var portValue any
	if port != nil {
		portValue = *port
	}
	payload := map[string]any{
		"pid":        os.Getpid(),
		"port":       portValue,
		"started_at": float64(time.Now().UnixNano()) / float64(time.Second),
	}
	data, err := marshalNoEscape(payload)
	if err != nil {
		log.Printf("janitor: failed to write server marker: %v", err)
		return
	}
	if err := atomicfs.WriteFile(path, data, 0o600); err != nil {
		log.Printf("janitor: failed to write server marker: %v", err)
	}
}

// Parity: framework/maintenance/janitor.py::clear_server_marker
func ClearServerMarker(home hivehome.Home) {
	data, err := os.ReadFile(ServerMarkerPath(home))
	if err != nil {
		return
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		return
	}
	if pid, ok := intFromJSON(marker["pid"]); ok && pid == os.Getpid() {
		_ = os.Remove(ServerMarkerPath(home))
	}
}

// Parity: framework/maintenance/janitor.py::live_server_owns_hive_home
func LiveServerOwnsHiveHome(home hivehome.Home) bool {
	data, err := os.ReadFile(ServerMarkerPath(home))
	if err != nil {
		return false
	}
	var marker map[string]any
	if err := json.Unmarshal(data, &marker); err != nil {
		return false
	}
	pid, ok := intFromJSON(marker["pid"])
	if !ok {
		return false
	}
	return pid != os.Getpid() && PidAlive(pid)
}

func intFromJSON(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		if n != float64(int64(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

func GlobalLockPath(home hivehome.Home) string {
	return filepath.Join(MaintenanceDir(home), "janitor.lock")
}

// Parity: framework/maintenance/janitor.py::_acquire_global_lock
func AcquireGlobalLock(home hivehome.Home, now float64) bool {
	lock := GlobalLockPath(home)
	_ = os.MkdirAll(filepath.Dir(lock), 0o755)
	if info, err := os.Stat(lock); err == nil && now-float64(info.ModTime().UnixNano())/float64(time.Second) < GlobalLockTTLSeconds {
		return false
	}
	_ = os.Remove(lock)
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	data, _ := marshalNoEscape(map[string]any{"pid": os.Getpid(), "started_at": now})
	_, _ = f.Write(data)
	_ = f.Close()
	return true
}

func ReleaseGlobalLock(home hivehome.Home) {
	_ = os.Remove(GlobalLockPath(home))
}

// Parity: framework/maintenance/janitor.py::load_last_report
func LoadLastReport(home hivehome.Home) (any, bool) {
	data, err := os.ReadFile(filepath.Join(MaintenanceDir(home), ReportFilename))
	if err != nil {
		return nil, false
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, false
	}
	return out, true
}

// Parity: framework/maintenance/janitor.py::_save_report
func SaveReport(home hivehome.Home, report *JanitorReport) error {
	path := filepath.Join(MaintenanceDir(home), ReportFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report.ToMap(), "", "  ")
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(path, data, 0o600)
}

// Parity: framework/maintenance/janitor.py::_write_manifest
func WriteManifest(home hivehome.Home, manifest *Manifest, startedAt float64) string {
	stamp := time.Unix(int64(startedAt), int64((startedAt-float64(int64(startedAt)))*1e9)).Format("20060102_150405")
	path := filepath.Join(MaintenanceDir(home), "prune_manifest-"+stamp+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("janitor: failed to write manifest %s: %v", path, err)
		return ""
	}
	var buf bytes.Buffer
	for _, item := range manifest.Items {
		data, err := marshalNoEscape(item)
		if err != nil {
			continue
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	if err := atomicfs.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		log.Printf("janitor: failed to write manifest %s: %v", path, err)
		return ""
	}
	return path
}

var BuildDisposer = func(home hivehome.Home, cfg RetentionConfig, execute bool) (Disposer, error) {
	if !execute {
		return DryRunDisposer{}, nil
	}
	if cfg.Mode == "archive" {
		return NewArchiveDisposer(home, cfg.ArchiveDir)
	}
	return DeleteDisposer{Home: home}, nil
}

type RunOptions struct {
	Tiers          map[int]bool
	Execute        bool
	IncludeLegacy  bool
	IncludeJunk    bool
	ProcessStartTS *float64
}

func sortedTiers(tiers map[int]bool) []int {
	out := make([]int, 0, len(tiers))
	for tier := range tiers {
		out = append(out, tier)
	}
	sort.Ints(out)
	return out
}

// Parity: framework/maintenance/janitor.py::run_once
func RunOnce(home hivehome.Home, safety *SafetyContext, cfg *RetentionConfig, opts RunOptions) *JanitorReport {
	effective := DefaultRetentionConfig()
	if cfg != nil {
		effective = *cfg
	}
	if safety == nil {
		safety = ForOffline(effective, float64(time.Now().UnixNano())/float64(time.Second))
	}
	tiers := opts.Tiers
	if tiers == nil {
		tiers = map[int]bool{1: true, 2: true, 3: true}
	}
	wallNow := func() float64 { return float64(time.Now().UnixNano()) / float64(time.Second) }

	report := &JanitorReport{
		StartedAt: safety.NowSeconds(),
		DryRun:    !opts.Execute,
		Mode:      effective.Mode,
		Tiers:     sortedTiers(tiers),
		Targets:   []TargetReport{},
	}

	if !effective.Enabled {
		report.Error = "retention disabled (kill switch)"
		report.FinishedAt = wallNow()
		return report
	}

	destructive := tiers[2] || tiers[3] || opts.IncludeLegacy || opts.IncludeJunk
	if opts.Execute && destructive && safety.Offline && LiveServerOwnsHiveHome(home) {
		report.Error = "a live runtime owns this HIVE_HOME (maintenance/server.lock); run tiers 2/3 through POST /api/maintenance/janitor/run or stop the server"
		report.FinishedAt = wallNow()
		return report
	}

	manifest := &Manifest{}
	var tier1Disposer Disposer = DryRunDisposer{}
	if opts.Execute {
		tier1Disposer = DeleteDisposer{Home: home}
	}

	disposer, err := BuildDisposer(home, effective, opts.Execute)
	if err != nil {
		report.Error = "disposer setup failed: " + err.Error()
		report.FinishedAt = wallNow()
		report.ManifestPath = WriteManifest(home, manifest, report.StartedAt)
		_ = SaveReport(home, report)
		return report
	}

	if opts.Execute && !AcquireGlobalLock(home, report.StartedAt) {
		closeDisposer(disposer)
		report.Error = "another janitor run is in progress"
		report.FinishedAt = wallNow()
		return report
	}

	pace := func() {
		if effective.IOSleepMS > 0 {
			time.Sleep(time.Duration(effective.IOSleepMS) * time.Millisecond)
		}
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("janitor: run failed: %v", r)
				report.Error = fmt.Sprint(r)
			}
			if archivePath := closeDisposer(disposer); archivePath != "" {
				report.ArchivePath = archivePath
			}
			if opts.Execute {
				ReleaseGlobalLock(home)
			}
		}()

		if opts.Execute {
			report.Targets = append(report.Targets, SweepJanitorLeftovers(home, manifest))
		}

		if tiers[1] {
			specs := []struct {
				name string
				days int
				keep int
			}{
				{"event_logs", effective.EventLogsDays, 1},
				{"llm_logs", effective.LLMLogsDays, 1},
				{"compaction_log", effective.CompactionLogDays, 0},
				{"tool-artifacts", effective.ToolArtifactsDays, 0},
				{"logs", effective.RotatedLogsDays, 2},
			}
			for _, spec := range specs {
				report.Targets = append(report.Targets, PruneAgedDir(PruneAgedDirOptions{
					Dir:            filepath.Join(home.Root, spec.name),
					Target:         spec.name,
					MaxAgeDays:     float64(spec.days),
					Disposer:       tier1Disposer,
					Manifest:       manifest,
					KeepNewest:     spec.keep,
					ProcessStartTS: opts.ProcessStartTS,
					Now:            report.StartedAt,
					Pace:           pace,
				}))
			}
		}

		if tiers[2] {
			tier2 := TargetReport{Name: "worker_deep_clean", Tier: 2}
			for _, worker := range IterWorkerSessions(home) {
				colonyID, workerID, wdir := worker[0], worker[1], worker[2]
				if ok, _ := safety.IsSafeToPrune(wdir, float64(effective.WorkerDeepCleanDays), false); !ok {
					tier2.Skipped++
					continue
				}
				meta := ReadJSONObject(filepath.Join(wdir, "meta.json"))
				if queenSessionID, ok := meta["queen_session_id"].(string); ok && safety.ProtectedSessionIDs[queenSessionID] {
					tier2.Skipped++
					continue
				}
				sub := DeepCleanWorker(home, colonyID, workerID, wdir, disposer, manifest)
				tier2.Files += sub.Files
				tier2.BytesFreed += sub.BytesFreed
				tier2.Errors = append(tier2.Errors, sub.Errors...)
				pace()
			}
			report.Targets = append(report.Targets, tier2)
		}

		if tiers[3] || opts.IncludeLegacy {
			tier3 := TargetReport{Name: "queen_hygiene", Tier: 3}
			var dirs []string
			index := map[string]int{}
			set := func(dir string) {
				key := resolveLongestExisting(dir)
				if i, ok := index[key]; ok {
					dirs[i] = dir
					return
				}
				index[key] = len(dirs)
				dirs = append(dirs, dir)
			}
			setDefault := func(dir string) {
				key := resolveLongestExisting(dir)
				if _, ok := index[key]; ok {
					return
				}
				index[key] = len(dirs)
				dirs = append(dirs, dir)
			}
			if tiers[3] {
				for _, dir := range IterQueenSessions(home) {
					set(dir)
				}
			}
			if opts.IncludeLegacy {
				for _, dir := range IterLegacyQueenSessions(home) {
					setDefault(dir)
				}
			}
			for _, dir := range dirs {
				if ok, _ := safety.IsSafeToPrune(dir, float64(effective.QueenHygieneDays), false); !ok {
					tier3.Skipped++
					continue
				}
				sub := PruneQueenSession(home, dir, effective, safety, disposer, manifest)
				tier3.Files += sub.Files
				tier3.BytesFreed += sub.BytesFreed
				tier3.Skipped += sub.Skipped
				tier3.Errors = append(tier3.Errors, sub.Errors...)
				pace()
			}
			report.Targets = append(report.Targets, tier3)
		}

		if opts.IncludeLegacy {
			var legacyDisposer Disposer = DryRunDisposer{}
			if opts.Execute {
				legacyDisposer = DeleteDisposer{Home: home}
			}
			report.Targets = append(report.Targets, SweepOrphanMessageIndex(home, legacyDisposer, manifest))
		}

		junkReport := TargetReport{Name: "junk", Tier: 0}
		junkAction := "delete"
		if Archives(disposer) {
			junkAction = "archive"
		}
		for _, entry := range FindJunkEntries(home, effective.JunkMinBytes) {
			item := NewPruneItem(entry.Path, entry.Bytes, 0, "junk", junkAction, "top-level entry outside known layout")
			if opts.IncludeJunk && opts.Execute {
				info, statErr := os.Stat(entry.Path)
				switch {
				case statErr == nil && info.IsDir():
					files, freed, disposeErr := disposer.DisposeDir(entry.Path)
					if disposeErr != nil {
						item.Outcome = "error"
						item.Error = disposeErr.Error()
						junkReport.Errors = append(junkReport.Errors, entry.Path+": "+disposeErr.Error())
					} else {
						junkReport.Files += files
						junkReport.BytesFreed += freed
						item.Outcome = "done"
					}
				default:
					freed, disposeErr := disposer.DisposeFile(entry.Path)
					if disposeErr != nil {
						item.Outcome = "error"
						item.Error = disposeErr.Error()
						junkReport.Errors = append(junkReport.Errors, entry.Path+": "+disposeErr.Error())
					} else {
						junkReport.Files++
						junkReport.BytesFreed += freed
						item.Outcome = "done"
					}
				}
			} else {
				item.Outcome = "candidate"
				junkReport.Skipped++
			}
			manifest.Add(item)
		}
		report.Targets = append(report.Targets, junkReport)
	}()

	report.FinishedAt = wallNow()
	report.ManifestPath = WriteManifest(home, manifest, report.StartedAt)
	_ = SaveReport(home, report)
	if log.Writer() != nil {
		verb := "run"
		if report.DryRun {
			verb = "would have"
		}
		log.Printf("janitor: %s freed %.1f MB across %d files (tiers=%v, mode=%s)",
			verb, float64(report.TotalBytes())/1e6, report.TotalFiles(), report.Tiers, report.Mode)
	}
	return report
}

func closeDisposer(d Disposer) string {
	type closable interface{ Close() string }
	if c, ok := d.(closable); ok {
		return c.Close()
	}
	return ""
}

func expandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), string(filepath.Separator)))
		}
	}
	return path
}

func parsePort(value string) *int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return nil
	}
	return &n
}
