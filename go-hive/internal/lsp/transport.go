package lsp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxFrameBytes caps one inbound JSON-RPC body. A language server that emits
// a larger frame is treated as broken instead of growing the process without
// bound.
const MaxFrameBytes = 16 << 20

// maxHeaderBytes caps the header block of one frame.
const maxHeaderBytes = 8 << 10

// ErrFrameTooLarge is returned when Content-Length exceeds the configured
// frame limit.
var ErrFrameTooLarge = errors.New("lsp: frame exceeds limit")

// ErrMissingContentLength is returned for a frame without Content-Length.
var ErrMissingContentLength = errors.New("lsp: missing Content-Length header")

// readFrame reads one Content-Length framed JSON-RPC body. It tolerates extra
// headers and both CRLF and bare LF line endings; the body itself is returned
// verbatim.
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	if max <= 0 {
		max = MaxFrameBytes
	}
	length := -1
	headerBytes := 0
	for {
		line, err := readHeaderLine(r, &headerBytes)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("lsp: malformed frame header %q", trimmed)
		}
		if strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || parsed < 0 {
				return nil, fmt.Errorf("lsp: invalid Content-Length %q", strings.TrimSpace(value))
			}
			length = parsed
		}
	}
	if length < 0 {
		return nil, ErrMissingContentLength
	}
	if length > max {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, length, max)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("lsp: read frame body: %w", err)
	}
	return body, nil
}

// readHeaderLine reads one header line without letting an attacker grow the
// process memory with a newline-free stream. total accumulates across lines.
func readHeaderLine(r *bufio.Reader, total *int) (string, error) {
	var buf []byte
	for {
		fragment, err := r.ReadSlice('\n')
		*total += len(fragment)
		if *total > maxHeaderBytes {
			return "", fmt.Errorf("lsp: frame header exceeds %d bytes", maxHeaderBytes)
		}
		buf = append(buf, fragment...)
		if err == nil {
			return string(buf), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(buf) == 0 {
				return "", io.EOF
			}
			return "", fmt.Errorf("lsp: truncated frame header: %w", err)
		}
		return "", err
	}
}

// writeFrame writes one Content-Length framed body in a single Write call so
// concurrent writers cannot interleave headers and bodies.
func writeFrame(w io.Writer, body []byte) error {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "Content-Length: %d\r\n\r\n", len(body))
	buf.Write(body)
	_, err := w.Write(buf.Bytes())
	return err
}

// tailBuffer keeps the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int
}

func newTailBuffer(limit int) *tailBuffer {
	if limit <= 0 {
		limit = 8 << 10
	}
	return &tailBuffer{limit: limit}
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-t.limit:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// sensitiveStderrKeys are matched case-insensitively against each stderr line.
// A matching line is replaced whole instead of trying to preserve a prefix:
// language servers print tokens in many shapes, and dropping the line is the
// only bounded guarantee.
var sensitiveStderrKeys = []string{
	"token", "secret", "password", "passwd", "api_key", "apikey",
	"authorization", "credential", "bearer ",
}

// redactSecrets removes stderr lines that look like they carry credentials and
// caps the result to the last max bytes / lines.
func redactSecrets(raw string, max int, maxLines int) string {
	if max <= 0 {
		max = 2048
	}
	if maxLines <= 0 {
		maxLines = 40
	}
	lines := strings.Split(strings.TrimRight(raw, "\r\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return ""
	}
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		lower := strings.ToLower(line)
		redacted := false
		for _, key := range sensitiveStderrKeys {
			if strings.Contains(lower, key) {
				kept = append(kept, "[redacted stderr line]")
				redacted = true
				break
			}
		}
		if !redacted {
			kept = append(kept, line)
		}
	}
	if len(kept) > maxLines {
		kept = kept[len(kept)-maxLines:]
	}
	out := strings.Join(kept, "\n")
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return strings.TrimSpace(out)
}

// processSpec is the resolved command a client is allowed to execute.
type processSpec struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
}

// transport owns one language-server process and its stdio framing.
type transport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *tailBuffer

	stderrDone chan struct{}

	writeMu sync.Mutex

	doneOnce sync.Once
	done     chan struct{}
	waitErr  error
	waitMu   sync.Mutex

	closeOnce sync.Once
	closeErr  error

	maxFrame int
}

// startProcess spawns the server in its own process group when the platform
// supports it, so shutdown can reap child processes too.
func startProcess(spec processSpec, maxFrame int) (*transport, error) {
	if strings.TrimSpace(spec.Command) == "" {
		return nil, errors.New("lsp: server command is empty")
	}
	if maxFrame <= 0 {
		maxFrame = MaxFrameBytes
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = mergeEnv(os.Environ(), spec.Env)
	configureProcessGroup(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: open server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: open server stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: open server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start server %q: %w", spec.Command, err)
	}
	t := &transport{
		cmd:        cmd,
		stdin:      stdin,
		stdout:     bufio.NewReaderSize(stdout, 64<<10),
		stderr:     newTailBuffer(8 << 10),
		stderrDone: make(chan struct{}),
		done:       make(chan struct{}),
		maxFrame:   maxFrame,
	}
	go func() {
		_, _ = io.Copy(t.stderr, stderrPipe)
		close(t.stderrDone)
	}()
	go func() {
		err := cmd.Wait()
		t.waitMu.Lock()
		t.waitErr = err
		t.waitMu.Unlock()
		t.doneOnce.Do(func() { close(t.done) })
	}()
	return t, nil
}

// mergeEnv appends extra variables, replacing any existing value for the same
// key so an explicitly passed PATH/GO_HIVE_FAKE_LSP wins deterministically.
func mergeEnv(base, extra []string) []string {
	if len(extra) == 0 {
		return base
	}
	index := make(map[string]int, len(base))
	out := append([]string(nil), base...)
	for i, entry := range out {
		if key, _, ok := strings.Cut(entry, "="); ok {
			index[key] = i
		}
	}
	for _, entry := range extra {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if i, exists := index[key]; exists {
			out[i] = entry
			continue
		}
		index[key] = len(out)
		out = append(out, entry)
	}
	return out
}

func (t *transport) Write(body []byte) error {
	if len(body) > t.maxFrame {
		return fmt.Errorf("%w: outgoing %d > %d", ErrFrameTooLarge, len(body), t.maxFrame)
	}
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return writeFrame(t.stdin, body)
}

func (t *transport) Read() ([]byte, error) { return readFrame(t.stdout, t.maxFrame) }

func (t *transport) Done() <-chan struct{} { return t.done }

// WaitErr returns the process wait error once the process has exited.
func (t *transport) WaitErr() error {
	t.waitMu.Lock()
	defer t.waitMu.Unlock()
	return t.waitErr
}

// ExitReason renders the process exit state for error messages.
func (t *transport) ExitReason() string {
	select {
	case <-t.done:
		if err := t.WaitErr(); err != nil {
			return err.Error()
		}
		return "exit status 0"
	default:
		return "connection closed"
	}
}

// StderrTail returns the bounded, redacted stderr tail. It briefly waits for
// the stderr copy goroutine so a crash report includes the lines the server
// wrote just before exiting.
func (t *transport) StderrTail() string {
	select {
	case <-t.stderrDone:
	default:
		select {
		case <-t.stderrDone:
		case <-time.After(150 * time.Millisecond):
		}
	}
	return redactSecrets(t.stderr.String(), 2048, 40)
}

// Close sends SIGTERM to the process group, waits briefly, then kills it.
func (t *transport) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		terminateProcess(t.cmd)
		select {
		case <-t.done:
		case <-time.After(2 * time.Second):
			killProcess(t.cmd)
			select {
			case <-t.done:
			case <-time.After(2 * time.Second):
				t.closeErr = errors.New("lsp: language server did not exit after kill")
			}
		}
	})
	return t.closeErr
}
