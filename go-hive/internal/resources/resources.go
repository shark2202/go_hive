package resources

// Parity: framework/host/runtime_resources.py
//
// The host-resource monitor: a background loop samples the runtime's process
// tree plus system memory, keeps a bounded rolling history, classifies a
// health verdict and logs an early warning the moment the verdict degrades.
// Served at GET /api/health/resources (server/routesapp/routes_app_extra.go).
//
// Sampling is done from `ps` output instead of Python's psutil, so the port
// keeps the same observable numbers (RSS, renderer headcount, CPU%) with no
// cgo and no new dependency. CPU percentages are the delta of each process's
// cumulative CPU time (`ps` time=) over the wall time between two samples —
// the same non-blocking computation upstream performs with psutil.cpu_times:
// a process seen for the first time reads 0%, and dead PIDs are evicted.
//
// Env tunables (upstream names/defaults, parsed with the upstream helper's
// fall-back-on-garbage rule):
//
//	HIVE_RESOURCE_INTERVAL_S      float 15.0   sampler cadence
//	HIVE_RESOURCE_HISTORY         int   480    rolling history depth
//	HIVE_RESOURCE_WARN_AVAIL_FRAC float 0.25   avail/total warn threshold
//	HIVE_RESOURCE_CRIT_AVAIL_FRAC float 0.15   avail/total critical threshold
//	HIVE_RESOURCE_WARN_RENDERERS  int   45     chrome renderer warn count
//	HIVE_RESOURCE_CRIT_RENDERERS  int   60     chrome renderer critical count

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

// Defaults mirror runtime_resources.py exactly.
const (
	defaultIntervalS      = 15.0
	defaultHistoryMax     = 480
	defaultWarnAvailFrac  = 0.25
	defaultCritAvailFrac  = 0.15
	defaultWarnRenderers  = 45
	defaultCritRenderers  = 60
	defaultSnapshotLength = 120
)

// Bucket names mirror upstream's _BUCKETS, in the same order.
var buckets = []string{"hive_serve", "bridge_host", "gcu", "mcp_servers", "other"}

// envFloat/envInt are the package-local spellings of the Python helpers. They
// exist so the contract scanner sees the same `env<Kind>("HIVE_…", default)`
// shape every other Go reader uses.
func envFloat(name string, def float64) float64 { return pyenv.Float(name, def) }

func envInt(name string, def int) int { return pyenv.Int(name, def) }

// IntervalSeconds is the sampler cadence (HIVE_RESOURCE_INTERVAL_S, 15.0).
// A bad value falls back to the default, like upstream's _env_num.
func IntervalSeconds() float64 {
	value := envFloat("HIVE_RESOURCE_INTERVAL_S", 15.0)
	if value <= 0 {
		// A zero/negative cadence would spin; upstream's loop would also be
		// pathological there. Keep the documented default instead.
		return defaultIntervalS
	}
	return value
}

// HistoryMax is the rolling history depth (HIVE_RESOURCE_HISTORY, 480).
func HistoryMax() int {
	value := envInt("HIVE_RESOURCE_HISTORY", 480)
	if value < 1 {
		value = 1
	}
	return value
}

// Thresholds carries the four health thresholds as resolved from the env.
type Thresholds struct {
	WarnAvailFrac float64
	CritAvailFrac float64
	WarnRenderers int
	CritRenderers int
}

// ThresholdsFromEnv resolves the four tunables on every call.
func ThresholdsFromEnv() Thresholds {
	return Thresholds{
		WarnAvailFrac: envFloat("HIVE_RESOURCE_WARN_AVAIL_FRAC", 0.25),
		CritAvailFrac: envFloat("HIVE_RESOURCE_CRIT_AVAIL_FRAC", 0.15),
		WarnRenderers: envInt("HIVE_RESOURCE_WARN_RENDERERS", 45),
		CritRenderers: envInt("HIVE_RESOURCE_CRIT_RENDERERS", 60),
	}
}

// process is one row of the sampled process table.
type process struct {
	PID  int
	PPID int
	RSS  float64 // MiB
	// CPUSeconds is the process's cumulative user+system CPU time, the psutil
	// cpu_times().user + .system equivalent the CPU% delta is taken from.
	CPUSeconds float64
	Name       string
	Cmd        string // lower-cased joined command line
}

// cpuTracker keeps the previous sample's per-PID CPU seconds and wall clock,
// mirroring ResourceMonitor._prev_cpu/_prev_wall. Only processes that matched
// a bucket are retained, exactly like upstream's cur_cpu map.
type cpuTracker struct {
	prevCPU  map[int]float64
	prevWall *time.Time
}

// component is one bucket of the sampled tree.
type component struct {
	RSSMB  float64 `json:"rss_mb"`
	CPUPct float64 `json:"cpu_pct"`
	Procs  int     `json:"procs"`
}

// sample is one full snapshot.
type sample struct {
	TS         float64               `json:"ts"`
	System     systemMemory          `json:"system"`
	Components map[string]*component `json:"components"`
	Chrome     chromeSample          `json:"chrome"`
	Context    map[string]any        `json:"context"`
	Verdict    string                `json:"verdict"`
	Reasons    []string              `json:"reasons"`
}

type chromeSample struct {
	Renderers int     `json:"renderers"`
	RSSMB     float64 `json:"rss_mb"`
}

// systemMemory mirrors upstream's system dict: the four values are null when
// the probe fails (Python None), so they are pointers.
type systemMemory struct {
	TotalMB *float64 `json:"total_mb"`
	UsedMB  *float64 `json:"used_mb"`
	AvailMB *float64 `json:"avail_mb"`
	Pct     *float64 `json:"pct"`
}

func floatPtr(value float64) *float64 { return &value }

func derefFloat(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

// Monitor keeps the current sample and its bounded history.
type Monitor struct {
	mu          sync.Mutex
	history     []map[string]any
	historyMax  int
	last        *sample
	lastVerdict *string

	// sampler is the seam tests replace; production uses sampleHost.
	sampler func(*cpuTracker) *sample
	// processRows lets tests inject a synthetic process table.
	processRows func() []process
	// cpu carries the previous sample's CPU-time deltas.
	cpu cpuTracker
}

// NewMonitor builds the monitor with the env-resolved history depth.
func NewMonitor() *Monitor {
	return &Monitor{
		historyMax:  HistoryMax(),
		sampler:     sampleHost,
		processRows: readProcessTable,
	}
}

// Sample takes one snapshot, appends its compact form to history, and logs a
// verdict transition. It never panics: a failed probe degrades that field.
func (m *Monitor) Sample() map[string]any {
	raw := m.sampler(&m.cpu)
	compact := compactSample(raw)
	m.mu.Lock()
	if m.historyMax <= 0 {
		m.historyMax = HistoryMax()
	}
	m.history = append(m.history, compact)
	if len(m.history) > m.historyMax {
		m.history = m.history[len(m.history)-m.historyMax:]
	}
	prev := ""
	if m.lastVerdict != nil {
		prev = *m.lastVerdict
	}
	verdict := raw.Verdict
	m.lastVerdict = &verdict
	m.last = raw
	m.mu.Unlock()
	m.logTransition(prev, raw)
	return compact
}

func (m *Monitor) logTransition(prev string, raw *sample) {
	if raw.Verdict == prev {
		return
	}
	if prev == "" {
		prev = "init"
	}
	line := fmt.Sprintf(
		"runtime-resources verdict %s->%s | avail=%sMB renderers=%d chrome_rss=%.0fMB hive_rss=%.0fMB workers=%v | %s",
		prev, raw.Verdict, formatMB(raw.System.AvailMB), raw.Chrome.Renderers, raw.Chrome.RSSMB,
		hiveRSS(raw), raw.Context["active_workers"], strings.Join(raw.Reasons, "; "),
	)
	if raw.Verdict == "critical" {
		log.Printf("ERROR: %s", line)
		return
	}
	if raw.Verdict == "warn" {
		log.Printf("WARNING: %s", line)
		return
	}
	log.Printf("%s", line)
}

// Start samples once immediately and then on the configured cadence until ctx
// ends. A non-positive interval means "use the env/default cadence".
func (m *Monitor) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Duration(IntervalSeconds() * float64(time.Second))
	}
	if interval <= 0 {
		interval = time.Duration(defaultIntervalS * float64(time.Second))
	}
	go func() {
		m.Sample()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.Sample()
			}
		}
	}()
}

// Snapshot returns the current sample plus thresholds and a history slice.
// historyN nil means the whole buffer (?full=1); <=0 means snapshot only.
func (m *Monitor) Snapshot(historyN *int) map[string]any {
	m.mu.Lock()
	last := m.last
	var history []any
	switch {
	case historyN == nil:
		history = make([]any, 0, len(m.history))
		for _, entry := range m.history {
			history = append(history, entry)
		}
	case *historyN <= 0:
		history = []any{}
	default:
		start := len(m.history) - *historyN
		if start < 0 {
			start = 0
		}
		history = make([]any, 0, len(m.history)-start)
		for _, entry := range m.history[start:] {
			history = append(history, entry)
		}
	}
	m.mu.Unlock()

	thresholds := ThresholdsFromEnv()
	out := map[string]any{
		"available":  last != nil,
		"interval_s": IntervalSeconds(),
		"thresholds": thresholdsPayload(thresholds, last),
		"history":    history,
	}
	if last == nil {
		out["verdict"] = "unknown"
		return out
	}
	out["ts"] = last.TS
	out["system"] = last.System
	out["components"] = last.Components
	out["chrome"] = last.Chrome
	out["context"] = last.Context
	out["verdict"] = last.Verdict
	out["reasons"] = last.Reasons
	return out
}

// Rollup is the compact summary folded into /api/health.
func (m *Monitor) Rollup() map[string]any {
	m.mu.Lock()
	last := m.last
	m.mu.Unlock()
	if last == nil {
		return map[string]any{"verdict": "unknown"}
	}
	return map[string]any{
		"verdict":   last.Verdict,
		"avail_mb":  last.System.AvailMB,
		"renderers": last.Chrome.Renderers,
	}
}

// thresholdsPayload mirrors ResourceMonitor.snapshot: fraction thresholds are
// resolved to absolute MB against the last sample's total when one exists.
func thresholdsPayload(t Thresholds, last *sample) map[string]any {
	warnMB := any(t.WarnAvailFrac)
	critMB := any(t.CritAvailFrac)
	if last != nil {
		if total := derefFloat(last.System.TotalMB); total > 0 {
			warnMB = roundTo(total*t.WarnAvailFrac, 0)
			critMB = roundTo(total*t.CritAvailFrac, 0)
		}
	}
	return map[string]any{
		"warn_avail_mb":   warnMB,
		"crit_avail_mb":   critMB,
		"warn_avail_frac": t.WarnAvailFrac,
		"crit_avail_frac": t.CritAvailFrac,
		"warn_renderers":  t.WarnRenderers,
		"crit_renderers":  t.CritRenderers,
	}
}

// sampleHost takes one production sample.
func sampleHost(tracker *cpuTracker) *sample {
	now := time.Now()
	return buildSample(readProcessTable(), os.Getpid(), now, systemMemorySample(), tracker, ThresholdsFromEnv())
}

// buildSample folds one process table into the component buckets, computes the
// per-bucket CPU% from the previous sample, and classifies the result.
func buildSample(rows []process, self int, now time.Time, system systemMemory, tracker *cpuTracker, thresholds Thresholds) *sample {
	components := map[string]*component{}
	for _, name := range buckets {
		components[name] = &component{}
	}
	descendants := descendantPIDs(rows, self)
	chrome := chromeSample{}
	// curCPU holds only the PIDs that matched a bucket, as upstream's cur_cpu
	// does; a PID missing from it reads 0% on the next sample.
	curCPU := map[int]float64{}
	wallDelta := 0.0
	if tracker != nil && tracker.prevWall != nil {
		wallDelta = now.Sub(*tracker.prevWall).Seconds()
	}

	for _, proc := range rows {
		bucket := bucketFor(proc, self, descendants)
		isChrome := bucket == "" && isChromeRenderer(proc)
		if bucket == "" && !isChrome {
			continue
		}
		curCPU[proc.PID] = proc.CPUSeconds
		cpuPct := 0.0
		if wallDelta > 0 && tracker != nil {
			if previous, ok := tracker.prevCPU[proc.PID]; ok {
				cpuPct = math.Max(0.0, (proc.CPUSeconds-previous)/wallDelta*100.0)
			}
		}
		if isChrome {
			chrome.Renderers++
			chrome.RSSMB += proc.RSS
			continue
		}
		entry := components[bucket]
		if entry == nil {
			continue
		}
		entry.RSSMB += proc.RSS
		entry.CPUPct += cpuPct
		entry.Procs++
	}
	for _, entry := range components {
		entry.RSSMB = roundTo(entry.RSSMB, 1)
		entry.CPUPct = roundTo(entry.CPUPct, 1)
	}
	chrome.RSSMB = roundTo(chrome.RSSMB, 1)

	if tracker != nil {
		tracker.prevCPU = curCPU
		tracker.prevWall = &now
	}

	sample := &sample{
		TS:         float64(now.UnixNano()) / 1e9,
		System:     system,
		Components: components,
		Chrome:     chrome,
		Context:    map[string]any{},
	}
	sample.Verdict, sample.Reasons = classify(sample, thresholds)
	return sample
}

// compactSample is the bounded history entry (upstream's record()).
func compactSample(raw *sample) map[string]any {
	componentRSS := map[string]float64{}
	hiveRSS := 0.0
	for name, entry := range raw.Components {
		componentRSS[name] = roundTo(entry.RSSMB, 1)
		hiveRSS += entry.RSSMB
	}
	return map[string]any{
		"ts":             raw.TS,
		"verdict":        raw.Verdict,
		"avail_mb":       raw.System.AvailMB,
		"sys_pct":        raw.System.Pct,
		"renderers":      raw.Chrome.Renderers,
		"chrome_rss_mb":  raw.Chrome.RSSMB,
		"hive_rss_mb":    roundTo(hiveRSS, 1),
		"comp_rss_mb":    componentRSS,
		"active_workers": raw.Context["active_workers"],
	}
}

// classify is upstream's _classify: worst-of-dimensions verdict with reasons.
func classify(raw *sample, thresholds Thresholds) (string, []string) {
	reasons := []string{}
	rank := map[string]int{"ok": 0, "warn": 1, "critical": 2}
	worst := "ok"
	bump := func(level, reason string) {
		reasons = append(reasons, reason)
		if rank[level] > rank[worst] {
			worst = level
		}
	}
	avail := derefFloat(raw.System.AvailMB)
	total := derefFloat(raw.System.TotalMB)
	if total > 0 && raw.System.AvailMB != nil {
		warnThreshold := total * thresholds.WarnAvailFrac
		critThreshold := total * thresholds.CritAvailFrac
		switch {
		case avail < critThreshold:
			bump("critical", fmt.Sprintf(
				"system available %.0fMB < %.0f (%.0f%% of %.0fMB total, critical)",
				avail, critThreshold, thresholds.CritAvailFrac*100, total))
		case avail < warnThreshold:
			bump("warn", fmt.Sprintf(
				"system available %.0fMB < %.0f (%.0f%% of %.0fMB total, warn)",
				avail, warnThreshold, thresholds.WarnAvailFrac*100, total))
		}
	}
	if raw.Chrome.Renderers > thresholds.CritRenderers {
		bump("critical", fmt.Sprintf("%d chrome renderers > %d (critical)", raw.Chrome.Renderers, thresholds.CritRenderers))
	} else if raw.Chrome.Renderers > thresholds.WarnRenderers {
		bump("warn", fmt.Sprintf("%d chrome renderers > %d (warn)", raw.Chrome.Renderers, thresholds.WarnRenderers))
	}
	return worst, reasons
}

// bucketFor is upstream's _bucket_for.
func bucketFor(proc process, self int, descendants map[int]struct{}) string {
	if proc.PID == self {
		return "hive_serve"
	}
	if strings.Contains(proc.Cmd, "gcu.bridge_host") {
		return "bridge_host"
	}
	if _, ok := descendants[proc.PID]; ok {
		if strings.Contains(proc.Cmd, "gcu.server") || strings.Contains(proc.Cmd, "gcu.") {
			return "gcu"
		}
		if strings.Contains(proc.Cmd, "mcp_server") || strings.Contains(proc.Cmd, "tools_server") ||
			strings.Contains(proc.Cmd, "fastmcp") || strings.Contains(proc.Cmd, "-m mcp") {
			return "mcp_servers"
		}
		return "other"
	}
	return ""
}

// isChromeRenderer is upstream's _is_chrome_renderer.
func isChromeRenderer(proc process) bool {
	if !strings.Contains(proc.Cmd, "--type=renderer") {
		return false
	}
	hay := strings.ToLower(proc.Name + " " + proc.Cmd)
	if strings.Contains(hay, "electron") {
		return false
	}
	return strings.Contains(hay, "chrome") || strings.Contains(hay, "chromium")
}

// descendantPIDs walks the ppid graph from self (psutil children(recursive)).
func descendantPIDs(rows []process, self int) map[int]struct{} {
	children := map[int][]int{}
	for _, proc := range rows {
		children[proc.PPID] = append(children[proc.PPID], proc.PID)
	}
	out := map[int]struct{}{}
	queue := append([]int(nil), children[self]...)
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if _, seen := out[pid]; seen {
			continue
		}
		out[pid] = struct{}{}
		queue = append(queue, children[pid]...)
	}
	return out
}

// readProcessTable runs `ps` once and parses pid/ppid/rss/cputime/comm/args.
// Any failure degrades to an empty table: the sample still carries system
// memory and the loop never dies.
func readProcessTable() []process {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,time=,comm=,args=").Output()
	if err != nil {
		return nil
	}
	return parseProcessTable(string(out))
}

// parseProcessTable splits `ps` output. comm can contain spaces on some
// systems, so the fixed numeric prefix is parsed positionally and the rest of
// the line is the command line (which is what the bucket matcher reads).
func parseProcessTable(text string) []process {
	rows := make([]process, 0, 256)
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		rssKB, errRSS := strconv.Atoi(fields[2])
		if errPID != nil || errPPID != nil || errRSS != nil {
			continue
		}
		name := ""
		cmd := ""
		cpuSeconds := 0.0
		if len(fields) > 3 {
			// A malformed time column degrades that row to 0% rather than
			// dropping the process (RSS/headcount still matter).
			if parsed, ok := parseCPUTime(fields[3]); ok {
				cpuSeconds = parsed
			}
			name = fields[4]
			cmd = strings.ToLower(strings.Join(fields[4:], " "))
		}
		rows = append(rows, process{
			PID:        pid,
			PPID:       ppid,
			RSS:        float64(rssKB) / 1024.0,
			CPUSeconds: cpuSeconds,
			Name:       filepath.Base(name),
			Cmd:        cmd,
		})
	}
	return rows
}

// parseCPUTime parses ps's TIME column: [[dd-]hh:]mm:ss[.cc]. Both the
// macOS (`35:13.92`) and Linux (`00:00:05`) spellings are accepted.
func parseCPUTime(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	days := 0
	if index := strings.Index(raw, "-"); index >= 0 {
		parsed, err := strconv.Atoi(raw[:index])
		if err != nil || parsed < 0 {
			return 0, false
		}
		days = parsed
		raw = raw[index+1:]
	}
	fraction := 0.0
	if index := strings.LastIndex(raw, "."); index >= 0 {
		digits := raw[index+1:]
		if digits == "" {
			return 0, false
		}
		parsed, err := strconv.ParseFloat("0."+digits, 64)
		if err != nil {
			return 0, false
		}
		fraction = parsed
		raw = raw[:index]
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 1 || len(parts) > 3 {
		return 0, false
	}
	values := make([]int, len(parts))
	for i, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return 0, false
		}
		values[i] = parsed
	}
	hours, minutes, seconds := 0, 0, 0
	switch len(values) {
	case 1:
		seconds = values[0]
	case 2:
		minutes, seconds = values[0], values[1]
	case 3:
		hours, minutes, seconds = values[0], values[1], values[2]
	}
	if minutes > 59 || seconds > 59 {
		return 0, false
	}
	return float64(days*24*3600+hours*3600+minutes*60+seconds) + fraction, true
}

func hiveRSS(raw *sample) float64 {
	total := 0.0
	for _, entry := range raw.Components {
		total += entry.RSSMB
	}
	return total
}

func formatMB(value *float64) string {
	if value == nil {
		return "None"
	}
	return strconv.FormatFloat(roundTo(*value, 0), 'f', -1, 64)
}

func roundTo(value float64, digits int) float64 {
	scale := 1.0
	for i := 0; i < digits; i++ {
		scale *= 10
	}
	// Python's round() is half-to-even; math.Round would round 2.25 to 2.3
	// where upstream's round(2.25, 1) is 2.2.
	return math.RoundToEven(value*scale) / scale
}

// DefaultSnapshotLength is the route's default history window (?history=).
const DefaultSnapshotLength = defaultSnapshotLength
