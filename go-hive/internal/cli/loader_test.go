package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeFrontendTree(t *testing.T, withDist bool) string {
	t.Helper()
	root := t.TempDir()
	frontendDir := filepath.Join(root, "core", "frontend")
	if err := os.MkdirAll(filepath.Join(frontendDir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, "src", "main.tsx"), []byte("console.log('hi')"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withDist {
		if err := os.MkdirAll(filepath.Join(frontendDir, "dist"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(frontendDir, "dist", "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return frontendDir
}

// Parity: tests/test_runner_cli_frontend.py::test_build_frontend_handles_text_calledprocesserror
func TestBuildFrontendHandlesTextCalledProcessError(t *testing.T) {
	frontendDir := writeFrontendTree(t, false)

	var stdout bytes.Buffer
	runner := NewRunner()
	runner.WorkDir = filepath.Dir(filepath.Dir(frontendDir))
	runner.Stdout = &stdout
	runner.RunCommand = func(dir string, command []string) (commandOutput, error) {
		return commandOutput{Stdout: "npm output", Stderr: "vite config failed"},
			&commandExitError{Command: command, ReturnCode: 1, Stdout: "npm output", Stderr: "vite config failed"}
	}

	if runner.buildFrontend() {
		t.Fatal("buildFrontend() = true, want false")
	}

	output := stdout.String()
	if !strings.Contains(output, "Frontend build failed while running") {
		t.Fatalf("stdout = %q", output)
	}
	if !strings.Contains(output, "vite config failed") {
		t.Fatalf("stdout = %q", output)
	}
}

// Parity: tests/test_runner_cli_frontend.py::test_build_frontend_cleans_cache_and_uses_windows_npm_cmd
func TestBuildFrontendCleansCacheAndUsesWindowsNpmCmd(t *testing.T) {
	frontendDir := writeFrontendTree(t, false)
	cacheFile := filepath.Join(frontendDir, "tsconfig.app.tsbuildinfo")
	if err := os.WriteFile(cacheFile, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	commands := make([][]string, 0)
	runner := NewRunner()
	runner.WorkDir = filepath.Dir(filepath.Dir(frontendDir))
	runner.GOOS = "windows"
	runner.Stdout = &bytes.Buffer{}
	runner.RunCommand = func(dir string, command []string) (commandOutput, error) {
		commands = append(commands, append([]string{}, command...))
		return commandOutput{}, nil
	}

	if !runner.buildFrontend() {
		t.Fatal("buildFrontend() = false, want true")
	}
	if _, err := os.Stat(cacheFile); !os.IsNotExist(err) {
		t.Fatalf("cache file still exists: %v", err)
	}
	want := [][]string{
		{"npm.cmd", "install", "--no-fund", "--no-audit"},
		{"npm.cmd", "run", "build"},
	}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

// Parity: tests/test_runner_cli_frontend.py::test_cmd_open_starts_gateway_ping_in_background
func TestCmdOpenStartsGatewayPingInBackground(t *testing.T) {
	args := newArgs(map[string]any{"open": false})
	events := make([]string, 0)
	pingSource := ""

	runner := NewRunner()
	runner.StartPing = func(source string) {
		pingSource = source
		events = append(events, "ping")
	}
	served := 0
	runner.Serve = func(incoming *parsedArgs) int {
		if incoming != args {
			t.Fatalf("Serve received a different Namespace")
		}
		events = append(events, "serve")
		served++
		return 123
	}

	if result := runner.cmdOpen(args); result != 123 {
		t.Fatalf("cmdOpen = %d, want 123", result)
	}
	if !args.boolean("open") {
		t.Fatal("args.open = false, want true")
	}
	if pingSource != "hive-open" {
		t.Fatalf("ping source = %q, want hive-open", pingSource)
	}
	if served != 1 {
		t.Fatalf("Serve calls = %d, want 1", served)
	}
	if !reflect.DeepEqual(events, []string{"ping", "serve"}) {
		t.Fatalf("events = %v, want ping before serve", events)
	}
}

func writeColonyTree(t *testing.T, root, name string, metadata map[string]any, workers map[string]string) string {
	t.Helper()
	colonyPath := filepath.Join(root, "colonies", name)
	if err := os.MkdirAll(colonyPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if metadata != nil {
		body, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(colonyPath, "metadata.json"), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for filename, body := range workers {
		if err := os.WriteFile(filepath.Join(colonyPath, filename), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return colonyPath
}

// Parity-exempt: no pytest drives cmd_colony_list; this pins the Go handler to
func TestColonyListReadsMetadataJSON(t *testing.T) {
	root := t.TempDir()
	writeColonyTree(t, root,
		"alpha",
		map[string]any{
			"queen_name":       "queen_test",
			"queen_session_id": "session_20260514_120000_aaaaaaaa",
			"created_at":       "2026-05-14T12:00:00.123456",
		},
		map[string]string{
			"worker_a.json": `{"name": "Worker A"}`,
			"agent.json":    `{"name": "reserved"}`,
		},
	)
	writeColonyTree(t, root, "beta", map[string]any{"queen_name": "beta_queen"}, map[string]string{
		"meta.json": `{"queen_name": "wrong"}`,
	})

	runner := NewRunner()
	runner.HomeRoot = func() (string, error) { return root, nil }
	var stdout bytes.Buffer
	runner.Stdout = &stdout

	if code := runner.cmdColonyList(newArgs(map[string]any{"json": false})); code != 0 {
		t.Fatalf("cmdColonyList = %d, want 0", code)
	}
	output := stdout.String()
	if strings.Contains(output, "wrong") {
		t.Fatalf("output read the decoy meta.json:\n%s", output)
	}
	if !strings.Contains(output, "queen_test") || !strings.Contains(output, "beta_queen") {
		t.Fatalf("output missing queen_name from metadata.json:\n%s", output)
	}
	if !strings.Contains(output, "2026-05-14T12:00:00") {
		t.Fatalf("output missing created_at from metadata.json:\n%s", output)
	}
	if !strings.Contains(output, "  1 ") {
		t.Fatalf("output missing worker count 1:\n%s", output)
	}
}

// Parity-exempt: no pytest drives cmd_colony_info; this pins the JSON shape to
func TestColonyInfoKeysWorkersByStem(t *testing.T) {
	root := t.TempDir()
	colonyPath := writeColonyTree(t, root,
		"alpha",
		map[string]any{"queen_name": "queen_test", "queen_session_id": "s1"},
		map[string]string{
			"worker_a.json": `{"name": "Worker A", "description": "does work",
				"tools": ["read", "write"], "goal": {"description": "ship it"},
				"spawned_from": "queen"}`,
			"configuration.json": `{"reserved": true}`,
			"broken.json":        `[1, 2, 3]`,
			"odd.json":           `{"tools": 5}`,
		},
	)

	runner := NewRunner()
	runner.HomeRoot = func() (string, error) { return root, nil }
	var stdout bytes.Buffer
	runner.Stdout = &stdout

	if code := runner.cmdColonyInfo(newArgs(map[string]any{"name": colonyPath, "json": true})); code != 0 {
		t.Fatalf("cmdColonyInfo = %d, want 0", code)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, stdout.String())
	}
	workers, ok := decoded["workers"].(map[string]any)
	if !ok {
		t.Fatalf("workers is %T, want a dict keyed by stem", decoded["workers"])
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %v, want only worker_a", workers)
	}
	worker, ok := workers["worker_a"].(map[string]any)
	if !ok {
		t.Fatalf("workers[worker_a] = %T", workers["worker_a"])
	}
	if worker["name"] != "Worker A" || worker["description"] != "does work" ||
		worker["goal"] != "ship it" || worker["spawned_from"] != "queen" {
		t.Fatalf("worker = %v", worker)
	}
	if tools, ok := worker["tools"].(float64); !ok || tools != 2 {
		t.Fatalf("tools = %v, want 2", worker["tools"])
	}

	stdout.Reset()
	if code := runner.cmdColonyInfo(newArgs(map[string]any{"name": colonyPath, "json": false})); code != 0 {
		t.Fatalf("cmdColonyInfo text = %d, want 0", code)
	}
	output := stdout.String()
	if !strings.Contains(output, "  • worker_a\n") {
		t.Fatalf("text output missing stem bullet:\n%s", output)
	}
	if !strings.Contains(output, "      tools: 2\n") {
		t.Fatalf("text output missing tools count:\n%s", output)
	}
}

// Parity-exempt: no pytest drives `hive session list --cold`; this pins the
func TestSessionListColdUsesSeam(t *testing.T) {
	calls := 0
	runner := NewRunner()
	runner.HTTPGet = func(url string, timeout time.Duration) (map[string]any, error) {
		t.Fatalf("HTTPGet called for --cold listing: %s", url)
		return nil, nil
	}
	runner.ColdSessions = func() []map[string]any {
		calls++
		return []map[string]any{
			{"session_id": "session_20260514_120000_aaaaaaaa", "has_worker": false},
		}
	}
	var stdout bytes.Buffer
	runner.Stdout = &stdout

	args := newArgs(map[string]any{
		"cold":   true,
		"json":   false,
		"server": "http://127.0.0.1:1",
	})
	if code := runner.cmdSessionList(args); code != 0 {
		t.Fatalf("cmdSessionList = %d, want 0", code)
	}
	if calls != 1 {
		t.Fatalf("ColdSessions calls = %d, want 1", calls)
	}
	output := stdout.String()
	if !strings.Contains(output, "session_20260514_120000_aaaaaaaa") {
		t.Fatalf("output missing session id:\n%s", output)
	}
	if !strings.Contains(output, "  ? ") {
		t.Fatalf("output missing \"?\" phase for cold row:\n%s", output)
	}
	if !strings.HasSuffix(strings.TrimSpace(output), "no") {
		t.Fatalf("output missing \"no\" worker flag:\n%s", output)
	}
}

// Parity-exempt: no pytest drives `hive session list`; this pins the non-cold
func TestSessionListLiveUsesHTTPGet(t *testing.T) {
	var gotURL string
	runner := NewRunner()
	runner.HTTPGet = func(url string, timeout time.Duration) (map[string]any, error) {
		gotURL = url
		return map[string]any{"sessions": []any{
			map[string]any{
				"session_id":  "session_20260518_120000_bbbbbbbb",
				"colony_id":   "colony_x",
				"queen_phase": "staging",
				"has_worker":  true,
			},
		}}, nil
	}
	runner.ColdSessions = func() []map[string]any {
		t.Fatal("ColdSessions called without --cold")
		return nil
	}
	var stdout bytes.Buffer
	runner.Stdout = &stdout

	args := newArgs(map[string]any{
		"cold":   false,
		"json":   false,
		"server": "http://127.0.0.1:9",
	})
	if code := runner.cmdSessionList(args); code != 0 {
		t.Fatalf("cmdSessionList = %d, want 0", code)
	}
	if gotURL != "http://127.0.0.1:9/api/sessions" {
		t.Fatalf("HTTPGet url = %q, want .../api/sessions", gotURL)
	}
	output := stdout.String()
	if !strings.Contains(output, "session_20260518_120000_bbbbbbbb") ||
		!strings.Contains(output, "colony_x") || !strings.HasSuffix(strings.TrimSpace(output), "yes") {
		t.Fatalf("unexpected live session table:\n%s", output)
	}
}
