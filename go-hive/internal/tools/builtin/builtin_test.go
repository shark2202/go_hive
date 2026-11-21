package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func toolDef(t *testing.T, name string) bool {
	t.Helper()
	for _, def := range Defs() {
		if def.Name == name {
			if def.Description == "" {
				t.Fatalf("tool %s has empty description", name)
			}
			if def.Parameters["type"] != "object" {
				t.Fatalf("tool %s parameters type = %v, want object", name, def.Parameters["type"])
			}
			return true
		}
	}
	return false
}

// Parity-exempt: Go-only P0 builtin; Python registers read_file/write_file/
// edit_file/search_files through files_server.py and terminal_exec through
// terminal_tools/exec.py.
func TestDefsExposeP0Tools(t *testing.T) {
	defs := Defs()
	if len(defs) != 14 {
		t.Fatalf("Defs() = %d tools, want 14 (5 file tools + 9 terminal)", len(defs))
	}
	for _, name := range []string{ToolReadFile, ToolWriteFile, ToolEditFile, ToolSearchFiles, ToolTerminalExec} {
		if !toolDef(t, name) {
			t.Fatalf("missing tool definition %q", name)
		}
	}
	read := defs[0].Parameters["required"].([]string)
	if len(read) != 1 || read[0] != "path" {
		t.Fatalf("read_file required = %v, want [path]", read)
	}
	write := defs[1].Parameters["required"].([]string)
	if len(write) != 2 || write[0] != "path" || write[1] != "content" {
		t.Fatalf("write_file required = %v, want [path content]", write)
	}
	if _, required := defs[2].Parameters["required"]; required {
		t.Fatalf("edit_file should default every argument, got required = %v", defs[2].Parameters["required"])
	}
	search := defs[3].Parameters["required"].([]string)
	if len(search) != 1 || search[0] != "pattern" {
		t.Fatalf("search_files required = %v, want [pattern]", search)
	}
	exec := defs[4].Parameters["required"].([]string)
	if len(exec) != 1 || exec[0] != "command" {
		t.Fatalf("terminal_exec required = %v, want [command]", exec)
	}
}

// Parity-exempt: Go-only P0 builtin; mirrors read_file in
// tools/src/aden_tools/file_ops.py.
func TestReadFileHappyPathAndPaging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tb := New(dir)

	res, err := tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "notes.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	want := "     1\talpha\n     2\tbeta\n     3\tgamma"
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}

	res, err = tb.Execute(context.Background(), ToolReadFile, map[string]any{
		"path":   path,
		"offset": 2,
		"limit":  1,
	})
	if err != nil {
		t.Fatalf("paged Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	if !strings.HasPrefix(res.Content, "     2\tbeta") {
		t.Fatalf("paged content = %q, want line 2 first", res.Content)
	}
	if !strings.Contains(res.Content, "Showing lines 2-2 of 3") ||
		!strings.Contains(res.Content, "Use offset=3 to continue reading.") {
		t.Fatalf("paged content missing continuation hint: %q", res.Content)
	}
}

// Parity-exempt: Go-only P0 builtin; mirrors read_file error text in
// tools/src/aden_tools/file_ops.py.
func TestReadFileRejectsMissingBinaryAndOversized(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)

	res, err := tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "absent.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// file_ops.py answers a missing file with plain text, so read_file does
	// too (no is_error marker the model has to decode).
	if res.IsError || !strings.Contains(res.Content, "File not found") {
		t.Fatalf("missing file result = %+v, want the upstream not-found text", res)
	}

	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{'a', 0, 'b'}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "blob.bin"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "binary") {
		t.Fatalf("binary file result = %+v, want binary error", res)
	}
	if err := os.WriteFile(filepath.Join(dir, "utf16.txt"), []byte{0xff, 0xfe, 'h', 0, 'i', 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "utf16.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "binary") {
		t.Fatalf("utf-16 file result = %+v, want binary error", res)
	}

	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("x", 128)), 0o644); err != nil {
		t.Fatal(err)
	}
	small := New(dir)
	small.maxReadSize = 32
	res, err = small.Execute(context.Background(), ToolReadFile, map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content, "too large") {
		t.Fatalf("oversized file result = %+v, want too-large error", res)
	}

	res, err = tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// file_ops.py::read_file answers a directory with its entries, not an error.
	if res.IsError || !strings.HasPrefix(res.Content, "Directory: . (") {
		t.Fatalf("directory result = %+v, want the upstream listing", res)
	}
}

// Parity-exempt: Go-only P0 builtin; mirrors write_file in
// tools/src/aden_tools/file_ops.py with atomic replacement.
func TestWriteFileCreatesParentsAtomically(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)

	res, err := tb.Execute(context.Background(), ToolWriteFile, map[string]any{
		"path":    "nested/deep/file.txt",
		"content": "hello world\n",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	data, err := os.ReadFile(filepath.Join(dir, "nested", "deep", "file.txt"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello world\n" {
		t.Fatalf("content = %q", data)
	}

	// Second write replaces the file and leaves no temp artifacts.
	res, err = tb.Execute(context.Background(), ToolWriteFile, map[string]any{
		"path":    "nested/deep/file.txt",
		"content": "replaced",
	})
	if err != nil || res.IsError {
		t.Fatalf("replace: err=%v result=%+v", err, res)
	}
	data, err = os.ReadFile(filepath.Join(dir, "nested", "deep", "file.txt"))
	if err != nil || string(data) != "replaced" {
		t.Fatalf("content after replace = %q err=%v", data, err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "nested", "deep"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("atomic write left temp file %q", entry.Name())
		}
	}

	res, err = tb.Execute(context.Background(), ToolWriteFile, map[string]any{
		"path":    "empty.txt",
		"content": "",
	})
	if err != nil || res.IsError {
		t.Fatalf("empty write: err=%v result=%+v", err, res)
	}
	info, err := os.Stat(filepath.Join(dir, "empty.txt"))
	if err != nil || info.Size() != 0 {
		t.Fatalf("empty file size = %v err=%v", info, err)
	}
}

// Parity-exempt: Go-only P0 builtin sandbox enforcement.
func TestPathTraversalAndAbsoluteEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	tb := New(dir)

	for _, raw := range []string{"../escape.txt", "../../escape.txt", filepath.Join(outside, "escape.txt")} {
		res, err := tb.Execute(context.Background(), ToolWriteFile, map[string]any{
			"path":    raw,
			"content": "nope",
		})
		if err != nil {
			t.Fatalf("write %s: %v", raw, err)
		}
		if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
			t.Fatalf("write %s result = %+v, want sandbox escape error", raw, res)
		}
		res, err = tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": raw})
		if err != nil {
			t.Fatalf("read %s: %v", raw, err)
		}
		// Read refusals are plain text (file_ops.py policy branch); the
		// security property is that no content comes back.
		if !strings.Contains(res.Content, "escapes the sandbox root") || strings.Contains(res.Content, "nope") {
			t.Fatalf("read %s result = %+v, want the sandbox escape refusal", raw, res)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape file was written outside the sandbox: %v", err)
	}
}

// Parity-exempt: Go-only P0 builtin sandbox enforcement.
func TestSymlinkEscapeRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "outdir")); err != nil {
		t.Fatal(err)
	}
	tb := New(dir)

	res, err := tb.Execute(context.Background(), ToolReadFile, map[string]any{"path": "link.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(res.Content, "escapes the sandbox root") || strings.Contains(res.Content, "secret") {
		t.Fatalf("symlink read result = %+v, want the escape refusal without content", res)
	}

	res, err = tb.Execute(context.Background(), ToolWriteFile, map[string]any{
		"path":    "outdir/planted.txt",
		"content": "escaped",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "escapes the sandbox root") {
		t.Fatalf("symlinked dir write result = %+v, want escape error", res)
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); !os.IsNotExist(err) {
		t.Fatalf("write through symlink created %s: %v", filepath.Join(outside, "planted.txt"), err)
	}
}

// Parity-exempt: Go-only P0 builtin; terminal_exec mirrors
// tools/src/terminal_tools/exec.py in foreground mode.
func TestTerminalExecCapturesStreamsAndCwd(t *testing.T) {
	dir := t.TempDir()
	tb := New(dir)
	root, err := tb.rootPath()
	if err != nil {
		t.Fatalf("rootPath: %v", err)
	}

	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "pwd; echo hello; echo oops >&2",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	payload := decodeExecPayload(t, res.Content)
	assertExecKeys(t, payload)
	if payload["exit_code"] != json.Number("0") {
		t.Fatalf("exit_code = %#v, want 0 in %s", payload["exit_code"], res.Content)
	}
	stdout := payloadString(t, payload, "stdout")
	if !strings.Contains(stdout, "hello") || !strings.Contains(stdout, root) {
		t.Fatalf("stdout = %q, want hello and root %q", stdout, root)
	}
	stderr := payloadString(t, payload, "stderr")
	if !strings.Contains(stderr, "oops") {
		t.Fatalf("stderr = %q, want oops", stderr)
	}
}

// Parity-exempt: Go-only P0 builtin; non-zero exit envelope.
func TestTerminalExecNonZeroExit(t *testing.T) {
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "echo broken >&2; exit 3",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	payload := decodeExecPayload(t, res.Content)
	if res.IsError {
		t.Fatalf("nonzero exit must stay a normal result (Python returns a dict): %+v", res)
	}
	if payload["exit_code"] != json.Number("3") {
		t.Fatalf("exit_code = %#v, want 3", payload["exit_code"])
	}
	if !strings.Contains(payloadString(t, payload, "stderr"), "broken") {
		t.Fatalf("stderr = %q, want broken", payload["stderr"])
	}
}

// Parity-exempt: Go-only P0 builtin; timeout kill mirrors the deadline
// handling in tools/src/terminal_tools/exec.py.
func TestTerminalExecTimeoutKillsProcessGroup(t *testing.T) {
	tb := New(t.TempDir())
	start := time.Now()
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":         "sleep 5; echo never",
		"timeout_seconds": 0.3,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 4*time.Second {
		t.Fatalf("timeout took %s, want process killed near the 300ms deadline", elapsed)
	}
	payload := decodeExecPayload(t, res.Content)
	if payload["timed_out"] != true {
		t.Fatalf("timed_out = %#v, want true", payload["timed_out"])
	}
	if strings.Contains(payloadString(t, payload, "stdout"), "never") {
		t.Fatalf("command produced output after the deadline: %q", payload["stdout"])
	}
}

// Parity-exempt: Go-only P0 builtin; output cap mirrors max_output_kb in
// tools/src/terminal_tools/exec.py.
func TestTerminalExecTruncatesOutput(t *testing.T) {
	tb := New(t.TempDir())
	tb.outputLimit = 128
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command": "i=0; while [ $i -lt 200 ]; do printf '0123456789'; i=$((i+1)); done",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %q", res.Content)
	}
	payload := decodeExecPayload(t, res.Content)
	if payload["stdout_truncated_bytes"] != json.Number("1872") {
		t.Fatalf("stdout_truncated_bytes = %#v, want 1872 (2000 produced - 128 inline)", payload["stdout_truncated_bytes"])
	}
	stdout := payloadString(t, payload, "stdout")
	if len(stdout) != 128 {
		t.Fatalf("stdout captured %d bytes, want exactly the 128 byte cap", len(stdout))
	}
	if strings.Contains(stdout, strings.Repeat("0123456789", 13)) {
		t.Fatalf("stdout was not capped: %q", stdout)
	}
	if !strings.HasPrefix(payloadString(t, payload, "output_handle"), "out_") {
		t.Fatalf("output_handle = %#v, want a handle for the dropped bytes", payload["output_handle"])
	}
}

// Parity: tools/tests/test_terminal_tools_exec.py::test_timed_out_marker
func TestTerminalExecRejectsInvalidArguments(t *testing.T) {
	tb := New(t.TempDir())
	res, err := tb.Execute(context.Background(), ToolTerminalExec, map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	payload := decodeExecPayload(t, res.Content)
	if payloadString(t, payload, "error") != "terminal_exec: command is required" {
		t.Fatalf("missing command result = %+v", res)
	}
	if payload["semantic_status"] != "error" {
		t.Fatalf("missing command semantic_status = %#v", payload["semantic_status"])
	}

	for _, timeout := range []any{-1, "soon"} {
		res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
			"command":     "true",
			"timeout_sec": timeout,
		})
		if err != nil {
			t.Fatalf("timeout %v: %v", timeout, err)
		}
		payload = decodeExecPayload(t, res.Content)
		if payload["error"] != terminalExecTimeoutMessage {
			t.Fatalf("timeout %v accepted: %+v", timeout, res)
		}
		// A pre-spawn failure is a normal result upstream (_err_envelope).
		if res.IsError {
			t.Fatalf("timeout %v error-marked: %+v", timeout, res)
		}
	}

	// Both waits zero means no finite foreground budget: upstream rejects it
	// and points at terminal_job_start (exec.py).
	res, err = tb.Execute(context.Background(), ToolTerminalExec, map[string]any{
		"command":                   "true",
		"timeout_sec":               0,
		"auto_background_after_sec": 0,
	})
	if err != nil {
		t.Fatalf("zero waits: %v", err)
	}
	payload = decodeExecPayload(t, res.Content)
	if payloadString(t, payload, "error") != terminalExecForegroundMessage {
		t.Fatalf("zero waits result = %+v, want the foreground-budget error", res)
	}
}

// Parity-exempt: Go-only P0 builtin dispatch contract.
func TestExecuteUnknownToolReturnsError(t *testing.T) {
	if _, err := Execute(context.Background(), "nope", nil); err == nil {
		t.Fatal("Execute with unknown tool returned nil error")
	}
}

// section returns the text after label up to the next known section header.
func section(content, label string) string {
	idx := strings.Index(content, label)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(label):]
	for _, next := range []string{"stdout:", "stderr:"} {
		if next == label {
			continue
		}
		if n := strings.Index(rest, next); n >= 0 {
			rest = rest[:n]
		}
	}
	return rest
}
