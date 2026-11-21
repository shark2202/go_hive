package builtin

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// The terminal_exec envelope contract. tools/src/terminal_tools/truncation.py
// builds a 16-key dict (build_exec_envelope) and a 17-key variant with `error`
// (_err_envelope); the model sees that dict serialized. These tests pin the
// key set, the null-vs-value semantics and the exit-code classification so the
// Go port cannot drift from the Python reference.

// terminalExecEnvelopeKeys is the Python key set, in build order.
var terminalExecEnvelopeKeys = []string{
	"exit_code",
	"stdout",
	"stderr",
	"stdout_truncated_bytes",
	"stderr_truncated_bytes",
	"runtime_ms",
	"pid",
	"output_handle",
	"timed_out",
	"semantic_status",
	"semantic_message",
	"warning",
	"auto_backgrounded",
	"job_id",
	"auto_shell",
	"shell_kind",
}

func decodeExecPayload(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("terminal_exec result is not a JSON object: %v\n%s", err, content)
	}
	if decoder.More() {
		t.Fatalf("terminal_exec result has trailing content after the JSON document: %q", content)
	}
	return payload
}

// assertExecKeys checks the exact key set: the 16 envelope keys, plus `error`
// and `blocked` only when the caller asked for them.
func assertExecKeys(t *testing.T, payload map[string]any, extra ...string) {
	t.Helper()
	want := append([]string{}, terminalExecEnvelopeKeys...)
	want = append(want, extra...)
	sort.Strings(want)
	got := make([]string, 0, len(payload))
	for key := range payload {
		got = append(got, key)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("envelope keys = %v, want %v", got, want)
	}
}

func execPayload(t *testing.T, tb *Toolbox, input map[string]any) (map[string]any, Result) {
	t.Helper()
	res, err := tb.Execute(context.Background(), ToolTerminalExec, input)
	if err != nil {
		t.Fatalf("terminal_exec: %v", err)
	}
	return decodeExecPayload(t, res.Content), res
}

func payloadString(t *testing.T, payload map[string]any, key string) string {
	t.Helper()
	value, ok := payload[key].(string)
	if !ok {
		t.Fatalf("envelope[%q] = %#v, want a string", key, payload[key])
	}
	return value
}

// TestTerminalExecEnvelopeSuccess pins the ordinary success envelope against
// the captured Python payload (docs/e2e-assets/tool-output/results.json,
// scenario terminal_exec_ok).
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeSuccess(t *testing.T) {
	if isWindows() {
		t.Skip("printf semantics differ on Windows")
	}
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{"command": "printf 'hi\\n'"})
	assertExecKeys(t, payload)
	if res.IsError {
		t.Fatal("terminal_exec marked a successful command as an error")
	}

	checks := map[string]any{
		"exit_code":              json.Number("0"),
		"stdout":                 "hi\n",
		"stderr":                 "",
		"stdout_truncated_bytes": json.Number("0"),
		"stderr_truncated_bytes": json.Number("0"),
		"timed_out":              false,
		"semantic_status":        "ok",
		"auto_backgrounded":      false,
		"auto_shell":             false,
		"shell_kind":             "direct",
	}
	for key, want := range checks {
		if payload[key] != want {
			t.Fatalf("envelope[%q] = %#v, want %#v", key, payload[key], want)
		}
	}
	for _, nullable := range []string{"semantic_message", "warning", "output_handle", "job_id"} {
		if payload[nullable] != nil {
			t.Fatalf("envelope[%q] = %#v, want null", nullable, payload[nullable])
		}
	}
	if pid, ok := payload["pid"].(json.Number); !ok || pid == "0" {
		t.Fatalf("envelope[pid] = %#v, want the child pid", payload["pid"])
	}
	if _, ok := payload["runtime_ms"].(json.Number); !ok {
		t.Fatalf("envelope[runtime_ms] = %#v, want an integer", payload["runtime_ms"])
	}
}

// TestTerminalExecEnvelopeNonZeroExit pins the failure semantics: the result is
// NOT error-marked (the Python tool returns a dict), the exit code is carried
// and semantic_status explains it.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeNonZeroExit(t *testing.T) {
	if isWindows() {
		t.Skip("POSIX exit semantics")
	}
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{"command": "echo broken >&2; exit 3"})
	assertExecKeys(t, payload)
	if res.IsError {
		t.Fatal("terminal_exec error-marked a nonzero exit; the Python envelope is a normal result")
	}
	if payload["exit_code"] != json.Number("3") {
		t.Fatalf("exit_code = %#v, want 3", payload["exit_code"])
	}
	if payload["semantic_status"] != "error" || payloadString(t, payload, "semantic_message") != "Command failed with exit code 3" {
		t.Fatalf("semantic = %#v / %#v", payload["semantic_status"], payload["semantic_message"])
	}
	if !strings.Contains(payloadString(t, payload, "stderr"), "broken") {
		t.Fatalf("stderr = %q, want the command's stderr", payload["stderr"])
	}
}

// TestTerminalExecEnvelopeSemanticCatalog pins the command-specific exit-code
// catalog (semantic_exit.py). grep exiting 1 means "no matches", not an error.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeSemanticCatalog(t *testing.T) {
	if isWindows() {
		t.Skip("grep availability differs on Windows")
	}
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{
		"command": "grep zzz-not-present /dev/null",
		"shell":   true,
	})
	if res.IsError {
		t.Fatal("grep exit 1 must not be an error")
	}
	if payload["exit_code"] != json.Number("1") {
		t.Fatalf("exit_code = %#v, want 1", payload["exit_code"])
	}
	if payload["semantic_status"] != "ok" || payloadString(t, payload, "semantic_message") != "No matches found" {
		t.Fatalf("semantic = %#v / %#v, want ok / No matches found", payload["semantic_status"], payload["semantic_message"])
	}

	payload, _ = execPayload(t, tb, map[string]any{"command": "echo a | tr a b"})
	if payload["auto_shell"] != true || payload["shell_kind"] != "bash" {
		t.Fatalf("auto_shell/shell_kind = %#v / %#v", payload["auto_shell"], payload["shell_kind"])
	}
}

// TestTerminalExecEnvelopeTimeout pins the timeout branch: timed_out=true, the
// SIGTERM exit code, and semantic_status=error with the upstream message.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeTimeout(t *testing.T) {
	if isWindows() {
		t.Skip("sleep-based timing test")
	}
	tb := New(t.TempDir())
	start := time.Now()
	payload, res := execPayload(t, tb, map[string]any{
		"command":     "sleep 5; echo never",
		"timeout_sec": 0.3,
	})
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("timeout took %s, want the process killed near the deadline", elapsed)
	}
	if res.IsError {
		t.Fatal("timeout is carried by timed_out/semantic_status, not by an error marker")
	}
	if payload["timed_out"] != true {
		t.Fatalf("timed_out = %#v, want true", payload["timed_out"])
	}
	if payload["exit_code"] != json.Number("-15") {
		t.Fatalf("exit_code = %#v, want -15 (SIGTERM)", payload["exit_code"])
	}
	if payload["semantic_status"] != "error" || payloadString(t, payload, "semantic_message") != "Command timed out" {
		t.Fatalf("semantic = %#v / %#v", payload["semantic_status"], payload["semantic_message"])
	}
	if strings.Contains(payloadString(t, payload, "stdout"), "never") {
		t.Fatal("command produced output after the deadline")
	}
}

// TestTerminalExecEnvelopeTruncation pins the overflow branch: the inline
// streams are capped, the dropped byte counts are reported, and the full output
// is retrievable under output_handle.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeTruncation(t *testing.T) {
	if isWindows() {
		t.Skip("uses POSIX yes/head")
	}
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{
		"command":       "yes x | head -c 300000",
		"shell":         true,
		"max_output_kb": 64,
	})
	if res.IsError {
		t.Fatal("unexpected error result")
	}
	if len(payloadString(t, payload, "stdout")) != 64*1024 {
		t.Fatalf("stdout length = %d, want the 64 KiB cap", len(payloadString(t, payload, "stdout")))
	}
	if payload["stdout_truncated_bytes"] != json.Number("234464") {
		t.Fatalf("stdout_truncated_bytes = %#v, want 234464 (300000 produced - 64 KiB inline)", payload["stdout_truncated_bytes"])
	}
	handle := payloadString(t, payload, "output_handle")
	if !strings.HasPrefix(handle, "out_") {
		t.Fatalf("output_handle = %q, want out_<hex>", handle)
	}

	page, err := tb.Execute(context.Background(), ToolTerminalOutputGet, map[string]any{
		"output_handle": handle,
		"max_kb":        64,
	})
	if err != nil {
		t.Fatalf("terminal_output_get: %v", err)
	}
	pagePayload := decodeExecPayload(t, page.Content)
	if pagePayload["expired"] != false || pagePayload["next_offset"] != json.Number("65536") {
		t.Fatalf("output_get = expired:%#v next_offset:%#v, want a live first page", pagePayload["expired"], pagePayload["next_offset"])
	}
	if !strings.HasPrefix(payloadString(t, pagePayload, "data"), "--- stdout ---\n") {
		t.Fatalf("output_get data does not start with the combined-stream header")
	}
}

// TestTerminalExecEnvelopeAutoBackground pins the promotion branch: exit_code
// null, auto_backgrounded true, a job_id, and semantic_status ok with the
// "Still running" message.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeAutoBackground(t *testing.T) {
	if isWindows() {
		t.Skip("sleep-based timing test")
	}
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{
		"command":                   "sleep 0.5",
		"timeout_sec":               2,
		"auto_background_after_sec": 0.1,
	})
	if res.IsError {
		t.Fatal("unexpected error result")
	}
	if payload["exit_code"] != nil {
		t.Fatalf("exit_code = %#v, want null while running", payload["exit_code"])
	}
	if payload["auto_backgrounded"] != true {
		t.Fatalf("auto_backgrounded = %#v, want true", payload["auto_backgrounded"])
	}
	if !strings.HasPrefix(payloadString(t, payload, "job_id"), "job_") {
		t.Fatalf("job_id = %#v, want job_<hex>", payload["job_id"])
	}
	if payload["semantic_status"] != "ok" || payloadString(t, payload, "semantic_message") != "Still running" {
		t.Fatalf("semantic = %#v / %#v", payload["semantic_status"], payload["semantic_message"])
	}
	if payload["output_handle"] != nil {
		t.Fatalf("output_handle = %#v, want null without overflow", payload["output_handle"])
	}
}

// TestTerminalExecEnvelopeWarning pins the advisory destructive-command
// catalog (destructive_warning.py), which rides on the envelope as `warning`.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeWarning(t *testing.T) {
	if isWindows() {
		t.Skip("POSIX command text")
	}
	tb := New(t.TempDir())
	payload, _ := execPayload(t, tb, map[string]any{
		"command": "rm -rf ./definitely-absent-path",
	})
	if payloadString(t, payload, "warning") != "may recursively force-remove files" {
		t.Fatalf("warning = %#v, want the recursive-force-remove advisory", payload["warning"])
	}

	payload, _ = execPayload(t, tb, map[string]any{"command": "git reset --hard"})
	if payloadString(t, payload, "warning") != "may discard uncommitted changes" {
		t.Fatalf("warning = %#v, want the git-reset advisory", payload["warning"])
	}

	payload, _ = execPayload(t, tb, map[string]any{"command": "echo safe"})
	if payload["warning"] != nil {
		t.Fatalf("warning = %#v, want null for a safe command", payload["warning"])
	}
}

// TestTerminalExecErrorEnvelope pins the pre-spawn failure shape
// (exec.py::_err_envelope): the 16 envelope keys plus `error`, semantic_status
// "error", and no error marker on the result.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecErrorEnvelope(t *testing.T) {
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{
		"command":     "true",
		"timeout_sec": -1,
	})
	assertExecKeys(t, payload, "error")
	if res.IsError {
		t.Fatal("the Python pre-spawn failure is a normal result; Go must not error-mark it")
	}
	if payload["semantic_status"] != "error" || payload["exit_code"] != nil || payload["pid"] != nil || payload["shell_kind"] != nil {
		t.Fatalf("error envelope = %#v", payload)
	}
	message := terminalExecTimeoutMessage
	if payloadString(t, payload, "stderr") != message ||
		payloadString(t, payload, "semantic_message") != message ||
		payloadString(t, payload, "error") != message {
		t.Fatalf("error envelope messages = %#v", payload)
	}
	if payload["timed_out"] != false || payload["auto_backgrounded"] != false || payload["auto_shell"] != false {
		t.Fatalf("error envelope flags = %#v", payload)
	}
}

// TestTerminalExecBlockedEnvelope pins the guard branch: the err envelope plus
// `blocked: true`, before any spawn.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecBlockedEnvelope(t *testing.T) {
	tb := New(t.TempDir())
	payload, res := execPayload(t, tb, map[string]any{"command": "pkill -9 -f chrome"})
	assertExecKeys(t, payload, "error", "blocked")
	if res.IsError {
		t.Fatal("blocked commands return a normal envelope upstream")
	}
	if payload["blocked"] != true {
		t.Fatalf("blocked = %#v, want true", payload["blocked"])
	}
	if payload["pid"] != nil || payload["exit_code"] != nil {
		t.Fatalf("blocked envelope ran something: %#v", payload)
	}
}

// TestTerminalExecEnvelopeJobLogsCarryNoErrorMarker pins the sibling change:
// a body-level terminal-job error (unknown job_id) is a normal JSON result,
// matching the Python tool's returned dict.
// Parity-exempt: terminal_exec 信封形状回放（由已提交 golden 与 Python build_exec_envelope 冻结，非单侧行为）
func TestTerminalExecEnvelopeJobLogsCarryNoErrorMarker(t *testing.T) {
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalJobLogs, map[string]any{"job_id": "job_nope"})
	if err != nil {
		t.Fatalf("job logs: %v", err)
	}
	if res.IsError {
		t.Fatalf("unknown job_id must be a body-level error, got %+v", res)
	}
	want := `{"error":"unknown job_id: job_nope"}`
	if res.Content != want {
		t.Fatalf("job logs content = %q, want %q", res.Content, want)
	}
}

func isWindows() bool {
	return runtime.GOOS == "windows"
}
