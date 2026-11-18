//go:build !windows

package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	terminalPTYMaxDefault  = 8
	terminalPTYDrainBytes  = 64000
	terminalPTYCloseBytes  = 64 * 1024
	terminalPTYPromptGrace = 2 * time.Second
)

// terminalPTYBusyError reports a concurrent run on the same session.
type terminalPTYBusyError struct{ sessionID string }

func (e terminalPTYBusyError) Error() string {
	return fmt.Sprintf("session %s is busy", e.sessionID)
}

// terminalPTYRunResult is the run/expect reply; field order mirrors Python.
type terminalPTYRunResult struct {
	Output        string `json:"output"`
	PromptAfter   bool   `json:"prompt_after"`
	MatchedExpect bool   `json:"matched_expect"`
	NextOffset    int    `json:"next_offset"`
	TimedOut      bool   `json:"timed_out"`
}

type terminalPTYDrainResult struct {
	Output     string `json:"output"`
	More       bool   `json:"more"`
	Offset     int    `json:"offset"`
	NextOffset int    `json:"next_offset"`
	TimedOut   bool   `json:"timed_out"`
}

type terminalPTYCloseResult struct {
	ExitCode     *int   `json:"exit_code"`
	FinalOutput  string `json:"final_output"`
	AlreadyClose bool   `json:"already_closed"`
}

// terminalPTYSession is one persistent bash bound to a PTY. A unique PS1
// sentinel marks command completion so run() can tell output from prompt.
type terminalPTYSession struct {
	SessionID   string
	ShellPath   string
	PID         int
	idleTimeout time.Duration
	createdAt   time.Time

	sentinel   string
	sentinelRE *regexp.Regexp

	master *os.File
	cmd    *exec.Cmd
	ring   *terminalRing
	busy   chan struct{}

	mu           sync.Mutex
	lastActivity time.Time
	closed       bool
	exitCode     *int
}

// terminalPTYRegistry caps concurrent sessions and reaps idle ones.
type terminalPTYRegistry struct {
	mu       sync.Mutex
	max      int
	sessions map[string]*terminalPTYSession
}

func newTerminalPTYRegistry() *terminalPTYRegistry {
	maxSessions := terminalPTYMaxDefault
	if raw := strings.TrimSpace(os.Getenv("TERMINAL_TOOLS_MAX_PTY")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxSessions = parsed
		}
	}
	return &terminalPTYRegistry{max: maxSessions, sessions: make(map[string]*terminalPTYSession)}
}

var terminalPTYs = newTerminalPTYRegistry()

func terminalPTYID() string {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "pty_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "pty_" + hex.EncodeToString(buf)
}

func (r *terminalPTYRegistry) reapIdle() {
	now := time.Now()
	r.mu.Lock()
	stale := make([]*terminalPTYSession, 0)
	for id, session := range r.sessions {
		session.mu.Lock()
		idle := now.Sub(session.lastActivity)
		session.mu.Unlock()
		if !session.isAlive() || idle > session.idleTimeout {
			delete(r.sessions, id)
			stale = append(stale, session)
		}
	}
	r.mu.Unlock()
	for _, session := range stale {
		_ = session.close(true, 500*time.Millisecond)
	}
}

func (r *terminalPTYRegistry) add(session *terminalPTYSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.sessions) >= r.max {
		return fmt.Errorf("terminal-tools PTY cap reached (%d). Close idle sessions or raise TERMINAL_TOOLS_MAX_PTY.", r.max)
	}
	r.sessions[session.SessionID] = session
	return nil
}

func (r *terminalPTYRegistry) get(sessionID string) *terminalPTYSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID]
}

func (r *terminalPTYRegistry) remove(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, sessionID)
}

func (r *terminalPTYRegistry) shutdownAll() {
	r.mu.Lock()
	sessions := make([]*terminalPTYSession, 0, len(r.sessions))
	for _, session := range r.sessions {
		sessions = append(sessions, session)
	}
	r.sessions = make(map[string]*terminalPTYSession)
	r.mu.Unlock()
	for _, session := range sessions {
		_ = session.close(true, 500*time.Millisecond)
	}
}

// openTerminalPTY starts bash on a fresh PTY with a sentinel prompt.
func openTerminalPTY(cwd string, env map[string]string, cols, rows int, idleTimeout time.Duration) (*terminalPTYSession, error) {
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}
	shellPath := "/bin/bash"
	master, slave, err := terminalOpenPTY()
	if err != nil {
		return nil, err
	}
	terminalSetWinsize(master.Fd(), rows, cols)

	session := &terminalPTYSession{
		SessionID:    terminalPTYID(),
		ShellPath:    shellPath,
		idleTimeout:  idleTimeout,
		createdAt:    time.Now(),
		lastActivity: time.Now(),
		master:       master,
		ring:         newTerminalRing(terminalRingPTYBytes),
		busy:         make(chan struct{}, 1),
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	session.sentinel = "__TERMINALTOOLS_PROMPT_" + hex.EncodeToString(token) + "__"
	session.sentinelRE = regexp.MustCompile(regexp.QuoteMeta(session.sentinel))

	fullEnv := terminalEnvWith(env, map[string]string{
		"PS1":            session.sentinel + "\n$ ",
		"PS2":            "",
		"PROMPT_COMMAND": "",
	})
	cmd := exec.Command(shellPath, "--norc", "--noprofile", "-i")
	cmd.Dir = cwd
	cmd.Env = fullEnv
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		master.Close()
		slave.Close()
		return nil, err
	}
	slave.Close()
	session.cmd = cmd
	session.PID = cmd.Process.Pid

	go session.readLoop()
	go session.waitLoop()
	session.waitForSentinel(terminalPTYPromptGrace, 0, "")
	return session, nil
}

// terminalEnvWith merges overrides into the sanitized environment, setting
// TERM last so the PTY always advertises a usable terminal.
func terminalEnvWith(overrides map[string]string, extra map[string]string) []string {
	merged := make(map[string]string, len(overrides)+len(extra)+1)
	for key, value := range overrides {
		merged[key] = value
	}
	if _, ok := merged["TERM"]; !ok {
		merged["TERM"] = "xterm-256color"
	}
	for key, value := range extra {
		merged[key] = value
	}
	return terminalSanitizedEnv(merged)
}

func (s *terminalPTYSession) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.ring.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	s.ring.Close()
}

func (s *terminalPTYSession) waitLoop() {
	err := s.cmd.Wait()
	s.mu.Lock()
	if s.exitCode == nil {
		code := -1
		if s.cmd.ProcessState != nil {
			code = terminalExitCode(s.cmd.ProcessState)
		} else if err != nil {
			code = 1
		}
		s.exitCode = &code
	}
	s.mu.Unlock()
}

func (s *terminalPTYSession) isAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.exitCode != nil {
		return false
	}
	return true
}

func (s *terminalPTYSession) touch() {
	s.mu.Lock()
	s.lastActivity = time.Now()
	s.mu.Unlock()
}

func (s *terminalPTYSession) tryAcquire() bool {
	select {
	case s.busy <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *terminalPTYSession) release() { <-s.busy }

func (s *terminalPTYSession) write(data []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, errors.New("session is closed")
	}
	deadline := time.Now().Add(time.Second)
	for {
		n, err := s.master.Write(data)
		if err == nil {
			return n, nil
		}
		if time.Now().After(deadline) {
			return n, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (s *terminalPTYSession) run(command, expect string, timeout time.Duration) (terminalPTYRunResult, error) {
	if !s.tryAcquire() {
		return terminalPTYRunResult{}, terminalPTYBusyError{sessionID: s.SessionID}
	}
	defer s.release()
	startOffset := s.ring.TotalWritten()
	if _, err := s.write([]byte(command + "\n")); err != nil {
		return terminalPTYRunResult{}, err
	}
	s.touch()
	return s.waitForSentinel(timeout, startOffset, expect), nil
}

func (s *terminalPTYSession) sendRaw(data string, addNewline bool) (int, error) {
	payload := []byte(data)
	if addNewline {
		payload = append(payload, '\n')
	}
	n, err := s.write(payload)
	s.touch()
	return n, err
}

func (s *terminalPTYSession) drain(timeout time.Duration) terminalPTYDrainResult {
	deadline := time.Now().Add(timeout)
	last := s.ring.TotalWritten()
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		current := s.ring.TotalWritten()
		if current == last {
			break
		}
		last = current
	}
	read := s.ring.Tail(terminalPTYDrainBytes)
	return terminalPTYDrainResult{
		Output:     string(read.Data),
		More:       read.NextOffset < s.ring.TotalWritten(),
		Offset:     read.Offset,
		NextOffset: read.NextOffset,
		TimedOut:   false,
	}
}

func (s *terminalPTYSession) close(force bool, grace time.Duration) terminalPTYCloseResult {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return terminalPTYCloseResult{ExitCode: nil, FinalOutput: "", AlreadyClose: true}
	}
	s.mu.Unlock()

	if !force {
		_, _ = s.write([]byte("exit\n"))
	}

	deadline := time.Now().Add(grace)
	var code *int
	for time.Now().Before(deadline) {
		s.mu.Lock()
		current := s.exitCode
		s.mu.Unlock()
		if current != nil {
			code = current
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if code == nil {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		killDeadline := time.Now().Add(grace)
		for time.Now().Before(killDeadline) {
			s.mu.Lock()
			current := s.exitCode
			s.mu.Unlock()
			if current != nil {
				code = current
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if code == nil {
		_ = s.cmd.Process.Kill()
	}

	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	_ = s.master.Close()

	read := s.ring.Tail(terminalPTYCloseBytes)
	if code == nil {
		s.mu.Lock()
		code = s.exitCode
		s.mu.Unlock()
	}
	return terminalPTYCloseResult{
		ExitCode:     code,
		FinalOutput:  string(read.Data),
		AlreadyClose: false,
	}
}

func (s *terminalPTYSession) waitForSentinel(timeout time.Duration, sinceOffset int, expect string) terminalPTYRunResult {
	var expectRE *regexp.Regexp
	if expect != "" {
		if compiled, err := regexp.Compile(expect); err == nil {
			expectRE = compiled
		}
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		total := s.ring.TotalWritten()
		read := s.ring.Read(sinceOffset, total-sinceOffset)
		text := string(read.Data)
		if expectRE != nil {
			if loc := expectRE.FindStringIndex(text); loc != nil {
				return terminalPTYRunResult{
					Output:        text[:loc[0]],
					PromptAfter:   true,
					MatchedExpect: true,
					NextOffset:    sinceOffset + len([]byte(text[:loc[1]])),
					TimedOut:      false,
				}
			}
		} else if loc := s.sentinelRE.FindStringIndex(text); loc != nil {
			return terminalPTYRunResult{
				Output:        terminalStripCommandEcho(text[:loc[0]]),
				PromptAfter:   true,
				MatchedExpect: false,
				NextOffset:    sinceOffset + len([]byte(text[:loc[1]])),
				TimedOut:      false,
			}
		}
		time.Sleep(50 * time.Millisecond)
		if !s.isAlive() {
			break
		}
	}

	total := s.ring.TotalWritten()
	read := s.ring.Read(sinceOffset, total-sinceOffset)
	return terminalPTYRunResult{
		Output:        string(read.Data),
		PromptAfter:   false,
		MatchedExpect: false,
		NextOffset:    read.NextOffset,
		TimedOut:      true,
	}
}

// terminalStripCommandEcho drops the first line when it looks like the echoed
// command: a PTY in canonical mode echoes input back.
func terminalStripCommandEcho(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 && idx < 4096 {
		return text[idx+1:]
	}
	return text
}

// terminalPTYOpen implements terminal_pty_open.
func (t *Toolbox) terminalPTYOpen(ctx context.Context, input map[string]any) (Result, error) {
	terminalPTYs.reapIdle()
	cwd, err := t.terminalWorkdir(input)
	if err != nil {
		return Result{}, err
	}
	env, ok := terminalEnvMap(input, "env")
	if !ok {
		return terminalErr("terminal_pty_open: env must map names to strings"), nil
	}
	cols := 120
	if v, present := input["cols"]; present && v != nil {
		parsed, ok := intArg(input, "cols")
		if !ok {
			return terminalErr("terminal_pty_open: cols must be an integer"), nil
		}
		cols = parsed
	}
	rows := 40
	if v, present := input["rows"]; present && v != nil {
		parsed, ok := intArg(input, "rows")
		if !ok {
			return terminalErr("terminal_pty_open: rows must be an integer"), nil
		}
		rows = parsed
	}
	idleSeconds := 1800
	if v, present := input["idle_timeout_sec"]; present && v != nil {
		parsed, ok := intArg(input, "idle_timeout_sec")
		if !ok {
			return terminalErr("terminal_pty_open: idle_timeout_sec must be an integer"), nil
		}
		idleSeconds = parsed
	}
	if idleSeconds <= 0 {
		idleSeconds = 1800
	}

	session, err := openTerminalPTY(cwd, env, cols, rows, time.Duration(idleSeconds)*time.Second)
	if err != nil {
		return terminalErr("failed to open session: %v", err), nil
	}
	if err := terminalPTYs.add(session); err != nil {
		_ = session.close(true, 200*time.Millisecond)
		return terminalErr("%v", err), nil
	}
	return terminalEncode(struct {
		SessionID string `json:"session_id"`
		PID       int    `json:"pid"`
		Shell     string `json:"shell"`
	}{SessionID: session.SessionID, PID: session.PID, Shell: session.ShellPath}), nil
}

// terminalPTYRun implements terminal_pty_run: command, raw send or drain.
func (t *Toolbox) terminalPTYRun(ctx context.Context, input map[string]any) (Result, error) {
	sessionID, ok := stringArg(input, "session_id")
	if !ok || sessionID == "" {
		return terminalErr("terminal_pty_run: session_id is required"), nil
	}
	session := terminalPTYs.get(sessionID)
	if session == nil {
		return terminalErr("unknown session_id: %s", sessionID), nil
	}
	if !session.isAlive() {
		terminalPTYs.remove(sessionID)
		return terminalErr("session %s has exited", sessionID), nil
	}
	timeout := 60 * time.Second
	if v, present := input["timeout_sec"]; present && v != nil {
		seconds, ok := finiteNumberArg(input, "timeout_sec")
		if !ok {
			return terminalErr("terminal_pty_run: timeout_sec must be a finite number"), nil
		}
		timeout = time.Duration(seconds * float64(time.Second))
	}

	if terminalBool(input, "read_only") {
		return terminalEncode(session.drain(timeout)), nil
	}

	command, hasCommand := stringArg(input, "command")
	if !hasCommand {
		return terminalErr("command is required unless read_only=True"), nil
	}
	if blocked := terminalCheckCommand(command); blocked != "" {
		return terminalEncode(struct {
			Error   string `json:"error"`
			Blocked bool   `json:"blocked"`
		}{Error: blocked, Blocked: true}), nil
	}
	if terminalBool(input, "raw_send") {
		sent, err := session.sendRaw(command, false)
		if err != nil {
			return terminalErr("%v", err), nil
		}
		return terminalEncode(struct {
			BytesSent int `json:"bytes_sent"`
		}{BytesSent: sent}), nil
	}
	expect, _ := stringArg(input, "expect")
	result, err := session.run(command, expect, timeout)
	if err != nil {
		var busy terminalPTYBusyError
		if errors.As(err, &busy) {
			return terminalErr("%v", err), nil
		}
		return terminalErr("%v", err), nil
	}
	return terminalEncode(result), nil
}

// terminalPTYClose implements terminal_pty_close.
func (t *Toolbox) terminalPTYClose(ctx context.Context, input map[string]any) (Result, error) {
	sessionID, ok := stringArg(input, "session_id")
	if !ok || sessionID == "" {
		return terminalErr("terminal_pty_close: session_id is required"), nil
	}
	session := terminalPTYs.get(sessionID)
	if session == nil {
		return terminalErr("unknown session_id: %s", sessionID), nil
	}
	force := terminalBool(input, "force")
	grace := time.Second
	if force {
		grace = 100 * time.Millisecond
	}
	result := session.close(force, grace)
	terminalPTYs.remove(sessionID)
	return terminalEncode(result), nil
}
