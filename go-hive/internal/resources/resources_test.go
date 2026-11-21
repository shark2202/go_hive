package resources

import (
	"os"
	"testing"
	"time"
)

// Tests for the ported tunables and the sampler's pure helpers: the env table,
// the threshold maths and the /ps-derived classification.

// Parity: core/tests/test_runtime_resources.py::test_classify_thresholds
func TestResourceTunableDefaults(t *testing.T) {
	for _, name := range []string{
		"HIVE_RESOURCE_INTERVAL_S", "HIVE_RESOURCE_HISTORY",
		"HIVE_RESOURCE_WARN_AVAIL_FRAC", "HIVE_RESOURCE_CRIT_AVAIL_FRAC",
		"HIVE_RESOURCE_WARN_RENDERERS", "HIVE_RESOURCE_CRIT_RENDERERS",
	} {
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetenv %s: %v", name, err)
		}
	}
	if got := IntervalSeconds(); got != 15.0 {
		t.Fatalf("IntervalSeconds default = %v, want 15.0", got)
	}
	if got := HistoryMax(); got != 480 {
		t.Fatalf("HistoryMax default = %d, want 480", got)
	}
	thresholds := ThresholdsFromEnv()
	want := Thresholds{WarnAvailFrac: 0.25, CritAvailFrac: 0.15, WarnRenderers: 45, CritRenderers: 60}
	if thresholds != want {
		t.Fatalf("ThresholdsFromEnv default = %#v, want %#v", thresholds, want)
	}
}

// Parity-exempt: upstream resolves the six knobs as import-time module
// constants, so no Python test can override them; the Go readers are per-call.
// The fallback rule those reads share is pinned by
// core/tests/test_runtime_resources.py::test_env_num_falls_back_on_garbage.
func TestResourceTunableOverrides(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_INTERVAL_S", "2.5")
	t.Setenv("HIVE_RESOURCE_HISTORY", "12")
	t.Setenv("HIVE_RESOURCE_WARN_AVAIL_FRAC", "0.4")
	t.Setenv("HIVE_RESOURCE_CRIT_AVAIL_FRAC", "0.2")
	t.Setenv("HIVE_RESOURCE_WARN_RENDERERS", "7")
	t.Setenv("HIVE_RESOURCE_CRIT_RENDERERS", "9")
	if got := IntervalSeconds(); got != 2.5 {
		t.Fatalf("IntervalSeconds = %v, want 2.5", got)
	}
	if got := HistoryMax(); got != 12 {
		t.Fatalf("HistoryMax = %d, want 12", got)
	}
	want := Thresholds{WarnAvailFrac: 0.4, CritAvailFrac: 0.2, WarnRenderers: 7, CritRenderers: 9}
	if got := ThresholdsFromEnv(); got != want {
		t.Fatalf("ThresholdsFromEnv = %#v, want %#v", got, want)
	}
}

// Parity: core/tests/test_runtime_resources.py::test_env_num_falls_back_on_garbage
func TestResourceTunableGarbageFallsBackLikeEnvNum(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_INTERVAL_S", "soon")
	t.Setenv("HIVE_RESOURCE_HISTORY", "many")
	t.Setenv("HIVE_RESOURCE_WARN_AVAIL_FRAC", "low")
	if got := IntervalSeconds(); got != 15.0 {
		t.Fatalf("garbage interval = %v, want the documented 15.0 fallback", got)
	}
	if got := HistoryMax(); got != 480 {
		t.Fatalf("garbage history = %d, want 480", got)
	}
	if got := ThresholdsFromEnv().WarnAvailFrac; got != 0.25 {
		t.Fatalf("garbage warn frac = %v, want 0.25", got)
	}
}

// Parity-exempt: Go-only guard — upstream feeds SAMPLE_INTERVAL_S straight to
// its loop and would spin on 0; the Go ticker falls back to the 15s default.
func TestResourceZeroIntervalKeepsTheDefaultCadence(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_INTERVAL_S", "0")
	if got := IntervalSeconds(); got != 15.0 {
		t.Fatalf("zero interval = %v, want the default (a 0s ticker would spin)", got)
	}
}

func memorySample(totalMB, availMB float64, renderers int) *sample {
	thresholds := ThresholdsFromEnv()
	raw := &sample{
		TS:         float64(time.Now().Unix()),
		System:     systemMemory{TotalMB: floatPtr(totalMB), AvailMB: floatPtr(availMB)},
		Components: map[string]*component{"hive_serve": {}},
		Chrome:     chromeSample{Renderers: renderers},
		Context:    map[string]any{},
	}
	raw.Verdict, raw.Reasons = classify(raw, thresholds)
	return raw
}

// Parity: core/tests/test_runtime_resources.py::test_classify_thresholds
func TestClassifyUsesEnvThresholds(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_WARN_AVAIL_FRAC", "0.3")
	t.Setenv("HIVE_RESOURCE_CRIT_AVAIL_FRAC", "0.1")
	t.Setenv("HIVE_RESOURCE_WARN_RENDERERS", "2")
	t.Setenv("HIVE_RESOURCE_CRIT_RENDERERS", "4")

	// 1000 MB total: warn below 300 MB, critical below 100 MB.
	cases := []struct {
		name      string
		availMB   float64
		renderers int
		want      string
	}{
		{"healthy", 800, 1, "ok"},
		{"memory warn", 250, 1, "warn"},
		{"memory critical", 50, 1, "critical"},
		{"renderer warn", 800, 3, "warn"},
		{"renderer critical", 800, 5, "critical"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			raw := memorySample(1000, testCase.availMB, testCase.renderers)
			if raw.Verdict != testCase.want {
				t.Fatalf("verdict = %q (%v), want %q", raw.Verdict, raw.Reasons, testCase.want)
			}
		})
	}
}

// Parity: core/tests/test_runtime_resources.py::test_snapshot_history_slicing, core/tests/test_runtime_resources.py::test_history_is_bounded
func TestMonitorSnapshotResolvesThresholdsAndCapsHistory(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_HISTORY", "3")
	monitor := NewMonitor()
	monitor.sampler = func() *sample { return memorySample(1000, 900, 1) }
	monitor.historyMax = HistoryMax()
	for i := 0; i < 5; i++ {
		monitor.Sample()
	}

	snapshot := monitor.Snapshot(nil)
	history, _ := snapshot["history"].([]any)
	if len(history) != 3 {
		t.Fatalf("history len = %d, want the HIVE_RESOURCE_HISTORY cap 3", len(history))
	}
	if snapshot["available"] != true {
		t.Fatalf("available = %v, want true after sampling", snapshot["available"])
	}
	thresholds, _ := snapshot["thresholds"].(map[string]any)
	if thresholds["warn_avail_frac"] != 0.25 || thresholds["crit_renderers"] != 60 {
		t.Fatalf("thresholds = %#v", thresholds)
	}
	// 900 MB of 1000 MB is healthy, and both dimensions passed.
	if snapshot["verdict"] != "ok" {
		t.Fatalf("verdict = %v, want ok", snapshot["verdict"])
	}
	if snapshot["interval_s"] != 15.0 {
		t.Fatalf("interval_s = %v, want 15.0", snapshot["interval_s"])
	}

	zero := 0
	empty := monitor.Snapshot(&zero)
	if history, _ := empty["history"].([]any); len(history) != 0 {
		t.Fatalf("history=0 should return no entries, got %d", len(history))
	}
	if empty["warn_avail_mb"] != nil {
		// The sample keys are merged at the top level; thresholds carry MB.
		if thresholds, _ := empty["thresholds"].(map[string]any); thresholds["warn_avail_mb"] != float64(250) {
			t.Fatalf("resolved warn_avail_mb = %v, want 250 (25%% of 1000MB)", thresholds["warn_avail_mb"])
		}
	}
}

// Parity: core/tests/test_runtime_resources.py::test_snapshot_when_never_sampled
func TestMonitorPreSampleSnapshotMirrorsUpstreamShape(t *testing.T) {
	monitor := NewMonitor()
	snapshot := monitor.Snapshot(nil)
	if snapshot["available"] != false || snapshot["verdict"] != "unknown" {
		t.Fatalf("pre-sample snapshot = %#v", snapshot)
	}
	thresholds, _ := snapshot["thresholds"].(map[string]any)
	if thresholds["warn_avail_mb"] != 0.25 {
		t.Fatalf("pre-sample warn_avail_mb = %v, want the raw fraction (no total to resolve against)", thresholds["warn_avail_mb"])
	}
}

// Parity: core/tests/test_runtime_resources.py::test_sample_attribution_and_chrome_excludes_electron, core/tests/test_runtime_resources.py::test_tools_servers_bucket_as_mcp_not_other, core/tests/test_runtime_resources.py::test_is_chrome_renderer_cross_platform
func TestProcessTableBucketsAndChromeRenderers(t *testing.T) {
	table := `    1     0   1024 /sbin/launchd /sbin/launchd
  100     1    512 /usr/bin/hive_serve /usr/bin/hive_serve serve
  200   100   2048 /usr/bin/python3 python3 mcp_server.py --stdio
  300   100   4096 /usr/bin/python3 python3 tools_server.py --stdio
  400     1   8192 /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --type=renderer --user-data-dir=/tmp/x
  500     1   2048 /Applications/Visual Studio Code.app/Contents/MacOS/Electron --type=renderer
  600     0   4096 /usr/bin/python3 python3 gcu.bridge_host --client
  700   100   1024 /usr/bin/python3 python3 gcu.server
  800   100   2048 /usr/bin/other /usr/bin/other --work`
	rows := parseProcessTable(table)
	if len(rows) != 9 {
		t.Fatalf("parsed %d rows, want 9", len(rows))
	}
	descendants := descendantPIDs(rows, 100)
	byPID := map[int]process{}
	for _, row := range rows {
		byPID[row.PID] = row
	}
	cases := []struct {
		pid  int
		want string
	}{
		{100, "hive_serve"},
		{200, "mcp_servers"},
		{300, "mcp_servers"},
		{400, ""},
		{500, ""},
		{600, "bridge_host"},
		{700, "gcu"},
		{800, "other"},
		{1, ""},
	}
	for _, testCase := range cases {
		if got := bucketFor(byPID[testCase.pid], 100, descendants); got != testCase.want {
			t.Fatalf("bucketFor(pid=%d) = %q, want %q", testCase.pid, got, testCase.want)
		}
	}
	if !isChromeRenderer(byPID[400]) {
		t.Fatal("chrome renderer not recognised")
	}
	if isChromeRenderer(byPID[500]) {
		t.Fatal("an Electron renderer must not count as a chrome renderer")
	}
}

// Parity: core/tests/test_runtime_resources.py::test_history_is_bounded, core/tests/test_runtime_resources.py::test_history_entry_has_per_component_rss
func TestMonitorHistoryIsBoundedByTheEnvAtFirstUse(t *testing.T) {
	t.Setenv("HIVE_RESOURCE_HISTORY", "2")
	monitor := NewMonitor()
	if monitor.historyMax != 2 {
		t.Fatalf("monitor historyMax = %d, want the env value 2", monitor.historyMax)
	}
	monitor.sampler = func() *sample { return memorySample(1000, 900, 1) }
	for i := 0; i < 4; i++ {
		monitor.Sample()
	}
	history, _ := monitor.Snapshot(nil)["history"].([]any)
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	entry, _ := history[0].(map[string]any)
	for _, key := range []string{"ts", "verdict", "avail_mb", "sys_pct", "renderers", "chrome_rss_mb", "hive_rss_mb", "comp_rss_mb"} {
		if _, ok := entry[key]; !ok {
			t.Fatalf("history entry missing %q: %#v", key, entry)
		}
	}
}
