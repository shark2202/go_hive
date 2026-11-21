package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func newJanitorRunner(t *testing.T) (*Runner, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	runner := NewRunner()
	var stdout, stderr bytes.Buffer
	runner.Stdout = &stdout
	runner.Stderr = &stderr
	runner.HomeRoot = func() (string, error) { return home, nil }
	return runner, home, &stdout, &stderr
}

// Parity-exempt: framework/maintenance/cli.py::register_janitor_commands has no
func TestJanitorHelpThroughRootListsRunAndStatus(t *testing.T) {
	runner, _, stdout, _ := newJanitorRunner(t)
	code := runner.Run([]string{"janitor", "--help"})
	if code != 0 {
		t.Fatalf("Run(janitor --help) = %d, want 0", code)
	}
	for _, want := range []string{
		"Prune aged debug logs",
		"{run,status}",
		"run",
		"status",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
}

// Parity-exempt: required=True on the janitor subparsers yields argparse's
func TestJanitorWithoutSubcommandExitsTwo(t *testing.T) {
	runner, _, _, stderr := newJanitorRunner(t)
	if code := runner.Run([]string{"janitor"}); code != 2 {
		t.Fatalf("Run(janitor) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "required") {
		t.Fatalf("stderr = %q, want a required-argument error", stderr.String())
	}
}

// Parity-exempt: framework/maintenance/cli.py::cmd_janitor_run dry-run path.
func TestJanitorRunDryRunReportsAndExitsZero(t *testing.T) {
	runner, home, stdout, stderr := newJanitorRunner(t)
	if err := os.MkdirAll(filepath.Join(home, "llm_logs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	code := runner.Run([]string{"janitor", "run"})
	if code != 0 {
		t.Fatalf("Run(janitor run) = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"janitor report — DRY-RUN (nothing deleted)",
		"  tiers: [1, 2, 3]   started: ",
		"  TOTAL: ",
		"\nelapsed: ",
		"dry-run only — re-run with --execute to apply\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

// Parity-exempt: cmd_janitor_run's two --tiers failure branches.
func TestJanitorRunRejectsInvalidTiers(t *testing.T) {
	cases := []struct {
		tiers string
		want  string
	}{
		{"1,x", "invalid --tiers value: '1,x'\n"},
		{"4", "invalid --tiers value: '4' (allowed: 1,2,3)\n"},
	}
	for _, testCase := range cases {
		runner, _, stdout, _ := newJanitorRunner(t)
		code := runner.Run([]string{"janitor", "run", "--tiers", testCase.tiers})
		if code != 2 {
			t.Fatalf("--tiers %q = %d, want 2", testCase.tiers, code)
		}
		if !strings.Contains(stdout.String(), testCase.want) {
			t.Fatalf("--tiers %q stdout missing %q:\n%s", testCase.tiers, testCase.want, stdout.String())
		}
	}
}

// Parity-exempt: a --tiers value with surrounding whitespace still parses,
func TestJanitorRunTiersToleratesWhitespace(t *testing.T) {
	runner, _, stdout, stderr := newJanitorRunner(t)
	code := runner.Run([]string{"janitor", "run", "--tiers", " 2 , 1 "})
	if code != 0 {
		t.Fatalf("Run = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "tiers: [1, 2]") {
		t.Fatalf("stdout missing normalised tiers:\n%s", stdout.String())
	}
}

// Parity-exempt: cmd_janitor_run's live-server refusal. Any HTTP listener on
func TestJanitorRunRefusesDestructiveExecuteWhenServerIsUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()
	port := serverPort(t, server.URL)

	runner, _, stdout, _ := newJanitorRunner(t)
	code := runner.Run([]string{"janitor", "run", "--execute", "--tiers", "2", "--port", port})
	if code != 1 {
		t.Fatalf("Run = %d, want 1", code)
	}
	out := stdout.String()
	for _, want := range []string{
		"A live Hive runtime owns this HIVE_HOME (server marker or port probe).\n",
		"are protected — POST /api/maintenance/janitor/run on the runtime's port, e.g.:\n",
		`  curl -X POST http://127.0.0.1:<port>/api/maintenance/janitor/run -H "Content-Type: application/json" -d '{"execute": true, "tiers": [2]}'` + "\n",
		"Or stop the desktop app / server and re-run this command.\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

// Parity-exempt: tier 1 is allowed offline even when a server is up — it only
func TestJanitorRunAllowsTierOneWithServerUp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer server.Close()
	port := serverPort(t, server.URL)

	runner, _, stdout, stderr := newJanitorRunner(t)
	code := runner.Run([]string{"janitor", "run", "--execute", "--tiers", "1", "--port", port})
	if code != 0 {
		t.Fatalf("Run = %d, want 0 (stderr: %s, stdout: %s)", code, stderr.String(), stdout.String())
	}
	if strings.Contains(stdout.String(), "A live Hive runtime owns this HIVE_HOME") {
		t.Fatalf("tier 1 should not be refused:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "EXECUTED (mode=") {
		t.Fatalf("stdout missing executed header:\n%s", stdout.String())
	}
}

// Parity-exempt: cmd_janitor_status with nothing recorded.
func TestJanitorStatusWithoutReport(t *testing.T) {
	runner, _, stdout, _ := newJanitorRunner(t)
	if code := runner.Run([]string{"janitor", "status"}); code != 1 {
		t.Fatalf("Run(janitor status) = %d, want 1", code)
	}
	if stdout.String() != "no janitor run recorded\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// Parity-exempt: cmd_janitor_status renders the saved report.
func TestJanitorStatusPrintsSavedReport(t *testing.T) {
	runner, home, stdout, _ := newJanitorRunner(t)
	maintenanceDir := filepath.Join(home, "maintenance")
	if err := os.MkdirAll(maintenanceDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := `{"started_at": 0, "finished_at": 1, "dry_run": true, "mode": "archive",` +
		` "tiers": [1], "total_bytes": 2048, "total_files": 2, "targets": [],` +
		` "manifest_path": "", "archive_path": "", "error": ""}`
	if err := os.WriteFile(filepath.Join(maintenanceDir, "last_janitor_report.json"), []byte(payload), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if code := runner.Run([]string{"janitor", "status"}); code != 0 {
		t.Fatalf("Run(janitor status) = %d, want 0", code)
	}
	out := stdout.String()
	for _, want := range []string{
		"janitor report — DRY-RUN (nothing deleted)\n",
		"  tiers: [1]   started: ",
		"  TOTAL: 2.0KB across 2 files\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q:\n%s", want, out)
		}
	}
}

// Parity-exempt: exact _print_report layout — column widths, empty-target
func TestPrintJanitorReportFormat(t *testing.T) {
	report := map[string]any{
		"dry_run":     false,
		"mode":        "delete",
		"tiers":       []any{float64(1), float64(2), float64(3)},
		"started_at":  float64(0),
		"total_bytes": int64(2048),
		"total_files": 3,
		"targets": []any{
			map[string]any{"name": "llm_logs", "files": 3, "bytes_freed": int64(2048), "skipped": 1, "errors": []any{}},
			map[string]any{"name": "idle", "files": 0, "bytes_freed": int64(0), "skipped": 0, "errors": []any{}},
			map[string]any{"name": "broken", "files": 0, "bytes_freed": int64(0), "skipped": 0, "errors": []any{"a", "b"}},
		},
		"archive_path":  "/tmp/archive.tar.gz",
		"manifest_path": "",
	}
	var out bytes.Buffer
	printJanitorReport(&out, report, false)
	want := strings.Join([]string{
		"janitor report — EXECUTED (mode=delete)",
		"  tiers: [1, 2, 3]   started: " + janitorCtime(0.0),
		"  llm_logs               files=3       freed=2.0KB      skipped=1",
		"  broken                 files=0       freed=0B         skipped=0 errors=2",
		"  TOTAL: 2.0KB across 3 files",
		"  archive: /tmp/archive.tar.gz",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("report =\n%q\nwant\n%q", out.String(), want)
	}
}

// Parity-exempt: verbose manifest lines mirror _print_report's tail.
func TestPrintJanitorManifestVerboseLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.jsonl")
	lines := strings.Join([]string{
		`{"path": "/home/llm_logs/a.jsonl", "bytes": 2048, "tier": 1, "target": "llm_logs", "action": "delete", "reason": "aged", "outcome": "candidate"}`,
		`{"path": "/home/llm_logs/b.jsonl", "bytes": 0, "tier": 1, "target": "llm_logs", "action": "delete", "reason": "aged", "outcome": "removed"}`,
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	var out bytes.Buffer
	printJanitorManifest(&out, path)
	want := strings.Join([]string{
		"    [candidate] delete    2.0KB      /home/llm_logs/a.jsonl  (aged)",
		"    [removed  ] delete    0B         /home/llm_logs/b.jsonl  (aged)",
		"",
	}, "\n")
	if out.String() != want {
		t.Fatalf("manifest =\n%q\nwant\n%q", out.String(), want)
	}

	broken := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(broken, []byte("{\"path\":\n"), 0o644); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}
	out.Reset()
	printJanitorManifest(&out, broken)
	if out.String() != "" {
		t.Fatalf("broken manifest printed %q, want nothing", out.String())
	}
}

// Parity-exempt: _human's unit ladder and rounding.
func TestJanitorHuman(t *testing.T) {
	cases := []struct {
		value int64
		want  string
	}{
		{0, "0B"},
		{1023, "1023B"},
		{1024, "1.0KB"},
		{1536, "1.5KB"},
		{1024 * 1024, "1.0MB"},
		{5 * 1024 * 1024 * 1024, "5.0GB"},
		{1024 * 1024 * 1024 * 1024, "1.0TB"},
	}
	for _, testCase := range cases {
		if got := janitorHuman(testCase.value); got != testCase.want {
			t.Fatalf("janitorHuman(%d) = %q, want %q", testCase.value, got, testCase.want)
		}
	}
}

func serverPort(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	if _, err := strconv.Atoi(parsed.Port()); err != nil {
		t.Fatalf("non-numeric port in %q", rawURL)
	}
	return parsed.Port()
}
