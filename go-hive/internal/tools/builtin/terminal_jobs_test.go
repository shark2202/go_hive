package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

func terminalTestDecode(t *testing.T, res Result) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(res.Content), &decoded); err != nil {
		t.Fatalf("result %q is not JSON: %v", res.Content, err)
	}
	return decoded
}

func terminalTestJobTool(t *testing.T, tb *Toolbox, name string, input map[string]any) map[string]any {
	t.Helper()
	var (
		res Result
		err error
	)
	switch name {
	case ToolTerminalJobStart:
		res, err = tb.terminalJobStart(context.Background(), input)
	case ToolTerminalJobLogs:
		res, err = tb.terminalJobLogs(context.Background(), input)
	case ToolTerminalJobManage:
		res, err = tb.terminalJobManage(context.Background(), input)
	default:
		t.Fatalf("unknown job tool %q", name)
	}
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return terminalTestDecode(t, res)
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_start_logs_wait_basic
func TestTerminalJobStartLogsWaitBasic(t *testing.T) {
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{
		"command": "echo first; echo second; echo third",
		"shell":   true,
	})
	jobID, _ := started["job_id"].(string)
	if !strings.HasPrefix(jobID, "job_") || len(jobID) != len("job_")+12 {
		t.Fatalf("job_id = %v, want job_<12 hex>", started["job_id"])
	}

	result := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
		"job_id":           jobID,
		"wait_until_exit":  true,
		"wait_timeout_sec": 5,
	})
	if result["status"] != "exited" {
		t.Fatalf("status = %v, want exited", result["status"])
	}
	if code, _ := result["exit_code"].(float64); int(code) != 0 {
		t.Fatalf("exit_code = %v, want 0", result["exit_code"])
	}
	data, _ := result["data"].(string)
	for _, want := range []string{"first", "third"} {
		if !strings.Contains(data, want) {
			t.Fatalf("data %q missing %q", data, want)
		}
	}
	if result["eof"] != true {
		t.Fatalf("eof = %v, want true", result["eof"])
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_offset_bookkeeping
func TestTerminalJobOffsetBookkeeping(t *testing.T) {
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{
		"command": `for i in 1 2 3 4 5; do echo line$i; sleep 0.05; done`,
		"shell":   true,
	})
	jobID, _ := started["job_id"].(string)

	seen := ""
	offset := 0
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
			"job_id":       jobID,
			"since_offset": offset,
			"max_bytes":    4096,
		})
		data, _ := result["data"].(string)
		seen += data
		offset = int(result["next_offset"].(float64))
		if result["status"] == "exited" {
			tail := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
				"job_id":       jobID,
				"since_offset": offset,
				"max_bytes":    4096,
			})
			seen += tail["data"].(string)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for n := 1; n <= 5; n++ {
		if !strings.Contains(seen, "line"+string(rune('0'+n))) {
			t.Fatalf("line%d missing from %q", n, seen)
		}
	}
	if offset == 0 {
		t.Fatal("next_offset never advanced")
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_merge_stderr
func TestTerminalJobMergeStderr(t *testing.T) {
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{
		"command":      "echo stdout1; echo stderr1 1>&2; echo stdout2",
		"shell":        true,
		"merge_stderr": true,
	})
	jobID, _ := started["job_id"].(string)
	result := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
		"job_id":           jobID,
		"stream":           "merged",
		"wait_until_exit":  true,
		"wait_timeout_sec": 5,
	})
	data, _ := result["data"].(string)
	if !strings.Contains(data, "stdout1") || !strings.Contains(data, "stderr1") {
		t.Fatalf("merged data = %q", data)
	}

	missing := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
		"job_id": jobID,
		"stream": "stderr",
	})
	if _, ok := missing["error"]; !ok {
		t.Fatalf("stderr read on merged job = %v, want error", missing)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_signal_term
func TestTerminalJobSignalTerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM is POSIX-only")
	}
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{"command": "sleep 30"})
	jobID, _ := started["job_id"].(string)
	time.Sleep(100 * time.Millisecond)

	managed := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{
		"action": "signal_term",
		"job_id": jobID,
	})
	if managed["ok"] != true {
		t.Fatalf("signal_term = %v, want ok", managed)
	}
	if managed["signal"] != "TERM" {
		t.Fatalf("signal = %v, want TERM", managed["signal"])
	}

	final := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
		"job_id":           jobID,
		"wait_until_exit":  true,
		"wait_timeout_sec": 5,
	})
	if final["status"] != "exited" {
		t.Fatalf("status = %v, want exited", final["status"])
	}
	if code, ok := final["exit_code"].(float64); !ok || int(code) != -15 {
		t.Fatalf("exit_code = %v, want -15", final["exit_code"])
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_list_action
func TestTerminalJobListAction(t *testing.T) {
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{"command": "sleep 1", "name": "napper"})
	jobID, _ := started["job_id"].(string)

	listing := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{"action": "list"})
	jobs, ok := listing["jobs"].([]any)
	if !ok {
		t.Fatalf("jobs = %v", listing["jobs"])
	}
	found := false
	for _, entry := range jobs {
		job, _ := entry.(map[string]any)
		if job["job_id"] == jobID {
			found = true
			if job["name"] != "napper" {
				t.Fatalf("name = %v, want napper", job["name"])
			}
			if job["status"] != "running" {
				t.Fatalf("status = %v, want running", job["status"])
			}
			if _, has := job["stdout_bytes"]; !has {
				t.Fatalf("summary missing stdout_bytes: %v", job)
			}
		}
	}
	if !found {
		t.Fatalf("job %s missing from list %v", jobID, jobs)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_unknown_job_id
func TestTerminalJobUnknownID(t *testing.T) {
	tb := New(t.TempDir())
	result := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{"job_id": "job_doesnotexist"})
	if _, ok := result["error"]; !ok {
		t.Fatalf("unknown job = %v, want error", result)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_capabilities_report_platform_and_list_metadata
func TestTerminalJobCapabilities(t *testing.T) {
	tb := New(t.TempDir())
	capabilities := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{"action": "capabilities"})
	wantPlatform := "posix"
	if runtime.GOOS == "windows" {
		wantPlatform = "windows"
	}
	if capabilities["platform"] != wantPlatform {
		t.Fatalf("platform = %v, want %s", capabilities["platform"], wantPlatform)
	}
	actions, _ := capabilities["supported_actions"].([]any)
	joined := make([]string, 0, len(actions))
	for _, action := range actions {
		joined = append(joined, action.(string))
	}
	for _, want := range []string{"stdin", "close_stdin", "signal_term", "signal_kill"} {
		if !strings.Contains(strings.Join(joined, ","), want) {
			t.Fatalf("supported_actions %v missing %s", joined, want)
		}
	}
	listing := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{"action": "list"})
	if listing["capabilities"] == nil {
		t.Fatalf("list action missing capabilities: %v", listing)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_start_logs_wait_basic
func TestTerminalJobStdinAndClose(t *testing.T) {
	tb := New(t.TempDir())
	started := terminalTestJobTool(t, tb, ToolTerminalJobStart, map[string]any{
		"command": "cat",
		"shell":   true,
	})
	jobID, _ := started["job_id"].(string)

	written := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{
		"action": "stdin",
		"job_id": jobID,
		"data":   "hello-stdin\n",
	})
	if n, _ := written["bytes_written"].(float64); n <= 0 {
		t.Fatalf("bytes_written = %v", written["bytes_written"])
	}
	closed := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{
		"action": "close_stdin",
		"job_id": jobID,
	})
	if closed["ok"] != true {
		t.Fatalf("close_stdin = %v", closed)
	}

	result := terminalTestJobTool(t, tb, ToolTerminalJobLogs, map[string]any{
		"job_id":           jobID,
		"wait_until_exit":  true,
		"wait_timeout_sec": 5,
	})
	if data, _ := result["data"].(string); !strings.Contains(data, "hello-stdin") {
		t.Fatalf("data = %q, want stdin echo", data)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_windows_unsupported_signal_does_not_reach_process
func TestTerminalJobUnsupportedAction(t *testing.T) {
	tb := New(t.TempDir())
	result := terminalTestJobTool(t, tb, ToolTerminalJobManage, map[string]any{
		"action": "explode",
		"job_id": "job_placeholder",
	})
	if result["code"] != "unsupported_action" {
		t.Fatalf("unsupported action = %v", result)
	}
	if result["capabilities"] == nil {
		t.Fatalf("unsupported action missing capabilities: %v", result)
	}
}

// Parity: tools/tests/test_terminal_tools_jobs.py::test_unknown_job_id
func TestTerminalJobCap(t *testing.T) {
	manager := &terminalJobManager{maxJobs: 1, ringBytes: 64 * 1024, jobs: make(map[string]*terminalJobRecord)}
	first, err := manager.start("sleep 1", []string{"sleep", "1"}, terminalShellDirect, t.TempDir(), terminalSanitizedEnv(nil), false, "")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	defer manager.shutdownAll(200 * time.Millisecond)

	_, err = manager.start("sleep 1", []string{"sleep", "1"}, terminalShellDirect, t.TempDir(), terminalSanitizedEnv(nil), false, "")
	if err == nil {
		t.Fatal("second start succeeded past the cap")
	}
	var limit terminalJobLimitError
	if !errors.As(err, &limit) {
		t.Fatalf("error = %v, want terminalJobLimitError", err)
	}
	if !first.isRunning() {
		t.Fatalf("first job state = %v, want running", first.status())
	}
}
