package playbook

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func nativeIntPtr(v int) *int           { return &v }
func nativeFloatPtr(v float64) *float64 { return &v }

func sampleNativeOptions() NativeExportOptions {
	return NativeExportOptions{
		Name:           "Enrich Leads",
		Description:    "One worker per undone row",
		Concurrency:    nativeIntPtr(4),
		PendingSQL:     "SELECT id, url FROM leads WHERE done_at IS NULL",
		Task:           "Enrich {url} (row {id})",
		Goal:           "Enriching {id}",
		Profile:        "researcher",
		Skill:          "web-research",
		DataColumns:    []string{"id", "url"},
		Retries:        1,
		Backoff:        "exp",
		TimeoutSeconds: nativeFloatPtr(90),
		MaxRounds:      nativeIntPtr(3),
		Chunk:          nativeIntPtr(2),
		CircuitBreaker: nativeFloatPtr(0.5),
	}
}

// Parity-exempt: `.play.json` is a Go-only native library format; upstream has
// no converter because it never writes that file. This pins the one-way export
// that makes the library runnable again after a switch to Python.
func TestExportNativePythonMirrorShape(t *testing.T) {
	script, err := ExportNativePython(sampleNativeOptions())
	if err != nil {
		t.Fatalf("ExportNativePython: %v", err)
	}
	for _, want := range []string{
		PythonMirrorMarker,
		`"name": "Enrich Leads"`,
		`"concurrency": 4`,
		`_PENDING_SQL = "SELECT id, url FROM leads WHERE done_at IS NULL"`,
		`_TASK = "Enrich {url} (row {id})"`,
		`_GOAL = "Enriching {id}"`,
		`_DATA_COLUMNS = ("id", "url")`,
		"async def run(args):",
		"await converge(",
		"pending=lambda: tracker_query(_PENDING_SQL)",
		"task=_render(_TASK, row)",
		"goal=_render(_GOAL, row)",
		`profile="researcher"`,
		`skill="web-research"`,
		"retries=1",
		`backoff="exp"`,
		"timeout=90",
		"max_rounds=3",
		"chunk=2",
		"circuit_breaker=0.5",
		`result["args"] = args`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("generated mirror is missing %q:\n%s", want, script)
		}
	}
}

// Parity-exempt: see TestExportNativePythonMirrorShape. This pins that the
// template/SQL literals survive quotes, backslashes and newlines, and that the
// generated text is valid Python when an interpreter is available.
func TestExportNativePythonMirrorEscapesLiterals(t *testing.T) {
	opts := sampleNativeOptions()
	opts.Task = "line \"one\"\nline\\two {url}"
	opts.PendingSQL = `SELECT 'quoted', "double" FROM t WHERE note = 'a\b'`
	script, err := ExportNativePython(opts)
	if err != nil {
		t.Fatalf("ExportNativePython: %v", err)
	}

	taskLine := ""
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(line, "_TASK = ") {
			taskLine = strings.TrimPrefix(line, "_TASK = ")
			break
		}
	}
	if taskLine == "" {
		t.Fatalf("generated mirror has no _TASK literal:\n%s", script)
	}
	var decoded string
	if err := json.Unmarshal([]byte(taskLine), &decoded); err != nil {
		t.Fatalf("_TASK is not a JSON/Python-compatible string literal: %v (%s)", err, taskLine)
	}
	if decoded != opts.Task {
		t.Fatalf("_TASK = %q, want %q", decoded, opts.Task)
	}

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; literal round-trip checked above")
	}
	cmd := exec.Command(python, "-c", "import sys; compile(sys.stdin.read(), '<mirror>', 'exec')")
	cmd.Stdin = strings.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated mirror does not compile: %v\n%s\n%s", err, out, script)
	}
}

// Parity-exempt: see TestExportNativePythonMirrorShape. Negative regression:
// a hand-written `.play.py` must never be replaced by the generated mirror, or
// a Go native run would silently clobber the Python library.
func TestEnsureNativePythonMirrorDoesNotClobberUserScript(t *testing.T) {
	dir := t.TempDir()
	userScript := "meta = {\"name\": \"Enrich Leads\"}\n\nasync def run(args):\n    return \"mine\"\n"
	target := filepath.Join(dir, "enrich-leads.play.py")
	if err := os.WriteFile(target, []byte(userScript), 0o644); err != nil {
		t.Fatal(err)
	}

	path, wrote, err := EnsureNativePythonMirror(dir, "Enrich Leads", sampleNativeOptions())
	if err != nil {
		t.Fatalf("EnsureNativePythonMirror: %v", err)
	}
	if wrote || path != target {
		t.Fatalf("EnsureNativePythonMirror = (%q, %v), want (%q, false)", path, wrote, target)
	}
	data, _ := os.ReadFile(target)
	if string(data) != userScript {
		t.Fatalf("user script was clobbered:\n%s", data)
	}

	// A generated mirror is ours: changing the JSON refreshes it.
	generatedDir := t.TempDir()
	first, wrote, err := EnsureNativePythonMirror(generatedDir, "Enrich Leads", sampleNativeOptions())
	if err != nil || !wrote {
		t.Fatalf("first EnsureNativePythonMirror = (%q, %v, %v)", first, wrote, err)
	}
	changed := sampleNativeOptions()
	changed.Description = "changed"
	second, wrote, err := EnsureNativePythonMirror(generatedDir, "Enrich Leads", changed)
	if err != nil || !wrote || second != first {
		t.Fatalf("second EnsureNativePythonMirror = (%q, %v, %v)", second, wrote, err)
	}
	data, _ = os.ReadFile(second)
	if !strings.Contains(string(data), `"description": "changed"`) {
		t.Fatalf("generated mirror was not refreshed:\n%s", data)
	}
}

// Parity-exempt: see TestExportNativePythonMirrorShape. Pins the documented
// precedence: Go keeps `.play.json` authoritative, Python lists the generated
// `.play.py`, and both names collapse to one library entry.
func TestNativeMirrorCoexistsWithJSON(t *testing.T) {
	dir := t.TempDir()
	jsonScript := `{"name":"Enrich Leads","pending_sql":"SELECT 1","task":"x"}`
	if err := os.WriteFile(filepath.Join(dir, "enrich-leads.play.json"), []byte(jsonScript), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := EnsureNativePythonMirror(dir, "Enrich Leads", sampleNativeOptions()); err != nil || !wrote {
		t.Fatalf("EnsureNativePythonMirror = (%v, %v)", wrote, err)
	}

	names := SavedPlaybookNames(dir)
	if len(names) != 1 || names[0] != "enrich-leads" {
		t.Fatalf("SavedPlaybookNames = %v, want [enrich-leads]", names)
	}
	script, source, err := ResolveScript(nil, nil, strPtr("Enrich Leads"), dir)
	if err != nil || script != jsonScript || !strings.HasSuffix(source, ".play.json") {
		t.Fatalf("ResolveScript = (%q, %q, %v), want the .play.json source", script, source, err)
	}
}
