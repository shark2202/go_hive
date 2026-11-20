package builtin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	terminalJobMaxDefault = 32
	terminalJobRingBytes  = 4 * 1024 * 1024
	terminalJobRecentKeep = 50
	terminalJobLogBytes   = 64000
	terminalJobWaitCap    = 45 * time.Second
	terminalJobWaitMax    = 600.0
)

// terminalJobLimitError reports the concurrent-job cap, the one safety pin
// that cannot be bypassed by a caller.
type terminalJobLimitError struct{ max int }

func (e terminalJobLimitError) Error() string {
	return fmt.Sprintf("terminal-tools job cap reached (%d). Wait for a job to finish or raise TERMINAL_TOOLS_MAX_JOBS.", e.max)
}

// terminalJobRecord is one background process plus its output rings.
type terminalJobRecord struct {
	JobID     string
	PID       int
	Name      string
	Command   any
	Merged    bool
	Adopted   bool
	StartedAt time.Time
	deadline  time.Time

	stdin *os.File

	cmd    *exec.Cmd
	stdout *terminalRing
	stderr *terminalRing

	mu          sync.Mutex
	exitedAt    time.Time
	exitCode    *int
	timedOut    bool
	stdinClosed bool

	done chan struct{}
}

// terminalJobSummary is the manage(list) view of a job.
type terminalJobSummary struct {
	JobID       string  `json:"job_id"`
	PID         int     `json:"pid"`
	Name        string  `json:"name"`
	Command     any     `json:"command"`
	StartedAt   float64 `json:"started_at"`
	Status      string  `json:"status"`
	ExitCode    *int    `json:"exit_code"`
	RuntimeMS   int     `json:"runtime_ms"`
	Merged      bool    `json:"merged"`
	StdoutBytes int     `json:"stdout_bytes"`
	StderrBytes int     `json:"stderr_bytes"`
	Adopted     bool    `json:"adopted"`
	TimedOut    bool    `json:"timed_out"`
}

// terminalJobStartResult is the job_start reply.
type terminalJobStartResult struct {
	JobID     string  `json:"job_id"`
	PID       int     `json:"pid"`
	StartedAt float64 `json:"started_at"`
	Name      string  `json:"name"`
	Merged    bool    `json:"merged"`
}

// terminalJobLogsResult is the read + status + wait reply.
type terminalJobLogsResult struct {
	Data                  string `json:"data"`
	Offset                int    `json:"offset"`
	NextOffset            int    `json:"next_offset"`
	TruncatedBytesDropped int    `json:"truncated_bytes_dropped"`
	EOF                   bool   `json:"eof"`
	Status                string `json:"status"`
	ExitCode              *int   `json:"exit_code"`
	TimedOut              bool   `json:"timed_out"`
	RuntimeMS             int    `json:"runtime_ms"`
}

func (r *terminalJobRecord) isRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitedAt.IsZero()
}

func (r *terminalJobRecord) status() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exitedAt.IsZero() {
		return "running"
	}
	return "exited"
}

func (r *terminalJobRecord) exitState() (*int, bool, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.exitCode, r.timedOut, r.exitedAt
}

func (r *terminalJobRecord) runtimeMS() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.exitedAt
	if end.IsZero() {
		end = time.Now()
	}
	return int(end.Sub(r.StartedAt).Milliseconds())
}

func (r *terminalJobRecord) sortTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.exitedAt.IsZero() {
		return r.exitedAt
	}
	return r.StartedAt
}

func (r *terminalJobRecord) summary() terminalJobSummary {
	exitCode, timedOut, _ := r.exitState()
	return terminalJobSummary{
		JobID:       r.JobID,
		PID:         r.PID,
		Name:        r.Name,
		Command:     r.Command,
		StartedAt:   float64(r.StartedAt.UnixNano()) / 1e9,
		Status:      r.status(),
		ExitCode:    exitCode,
		RuntimeMS:   r.runtimeMS(),
		Merged:      r.Merged,
		StdoutBytes: r.stdout.TotalWritten(),
		StderrBytes: terminalRingWritten(r.stderr),
		Adopted:     r.Adopted,
		TimedOut:    timedOut,
	}
}

func terminalRingWritten(ring *terminalRing) int {
	if ring == nil {
		return 0
	}
	return ring.TotalWritten()
}

// terminalJobManager owns every background job.
type terminalJobManager struct {
	mu          sync.Mutex
	maxJobs     int
	ringBytes   int
	jobs        map[string]*terminalJobRecord
	exitedOrder []string
}

func newTerminalJobManager() *terminalJobManager {
	maxJobs := terminalJobMaxDefault
	if raw := strings.TrimSpace(os.Getenv("TERMINAL_TOOLS_MAX_JOBS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			maxJobs = parsed
		}
	}
	return &terminalJobManager{
		maxJobs:   maxJobs,
		ringBytes: terminalJobRingBytes,
		jobs:      make(map[string]*terminalJobRecord),
	}
}

var terminalJobs = newTerminalJobManager()

func terminalJobID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "job_" + strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return "job_" + hex.EncodeToString(buf)
}

func (m *terminalJobManager) activeCount() int {
	m.mu.Lock()
	records := make([]*terminalJobRecord, 0, len(m.jobs))
	for _, record := range m.jobs {
		records = append(records, record)
	}
	m.mu.Unlock()
	active := 0
	for _, record := range records {
		if record.isRunning() {
			active++
		}
	}
	return active
}

func (m *terminalJobManager) get(jobID string) *terminalJobRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[jobID]
}

// start spawns command and begins pumping its output into ring buffers.
func (m *terminalJobManager) start(commandText string, argv []string, spec terminalShellSpec, cwd string, env []string, mergeStderr bool, name string) (*terminalJobRecord, error) {
	if m.activeCount() >= m.maxJobs {
		return nil, terminalJobLimitError{max: m.maxJobs}
	}

	spawnArgv := argv
	if spec.executable != "" {
		spawnArgv = spec.buildArgv(commandText)
	}
	if len(spawnArgv) == 0 || strings.TrimSpace(spawnArgv[0]) == "" {
		return nil, errTerminalEmptyCommand
	}

	cmd := exec.Command(spawnArgv[0], spawnArgv[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = env

	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var stderrRead, stderrWrite *os.File
	if mergeStderr {
		cmd.Stdout = stdoutWrite
		cmd.Stderr = stdoutWrite
	} else {
		stderrRead, stderrWrite, err = os.Pipe()
		if err != nil {
			stdoutRead.Close()
			stdoutWrite.Close()
			return nil, err
		}
		cmd.Stdout = stdoutWrite
		cmd.Stderr = stderrWrite
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		if stderrRead != nil {
			stderrRead.Close()
			stderrWrite.Close()
		}
		return nil, err
	}
	cmd.Stdin = stdinRead

	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		stdinRead.Close()
		stdinWrite.Close()
		stdoutRead.Close()
		stdoutWrite.Close()
		if stderrRead != nil {
			stderrRead.Close()
			stderrWrite.Close()
		}
		return nil, err
	}
	stdinRead.Close()
	stdoutWrite.Close()
	if stderrWrite != nil {
		stderrWrite.Close()
	}

	record := &terminalJobRecord{
		JobID:     terminalJobID(),
		PID:       cmd.Process.Pid,
		Name:      name,
		Command:   terminalJobCommand(commandText, argv, spec),
		Merged:    mergeStderr,
		StartedAt: time.Now(),
		stdin:     stdinWrite,
		cmd:       cmd,
		stdout:    newTerminalRing(m.ringBytes),
		done:      make(chan struct{}),
	}
	if !mergeStderr {
		record.stderr = newTerminalRing(m.ringBytes)
	}
	if record.Name == "" {
		record.Name = terminalJobDefaultName(record.Command)
	}

	var pumps sync.WaitGroup
	pumps.Add(1)
	go terminalPumpStream(stdoutRead, record.stdout, &pumps)
	if stderrRead != nil {
		pumps.Add(1)
		go terminalPumpStream(stderrRead, record.stderr, &pumps)
	}

	m.mu.Lock()
	m.jobs[record.JobID] = record
	m.mu.Unlock()

	go m.watch(record, &pumps, nil)
	return record, nil
}

// terminalExecAdoption carries the live pieces of a foreground exec that is
// being promoted into the job manager.
type terminalExecAdoption struct {
	cmd       *exec.Cmd
	stdout    *terminalRing
	stderr    *terminalRing
	pumps     *sync.WaitGroup
	waited    chan error
	startedAt time.Time
	deadline  time.Time
	command   any
}

// adopt registers an already-running process as a background job. The
// already-pumping rings, the pending Wait result and the original deadline
// transfer ownership; terminal_exec keeps none of them.
func (m *terminalJobManager) adopt(a terminalExecAdoption) (*terminalJobRecord, error) {
	if m.activeCount() >= m.maxJobs {
		return nil, terminalJobLimitError{max: m.maxJobs}
	}
	record := &terminalJobRecord{
		JobID:     terminalJobID(),
		PID:       a.cmd.Process.Pid,
		Command:   a.command,
		Adopted:   true,
		StartedAt: a.startedAt,
		deadline:  a.deadline,
		cmd:       a.cmd,
		stdout:    a.stdout,
		stderr:    a.stderr,
		done:      make(chan struct{}),
	}
	if record.Name == "" {
		record.Name = terminalJobDefaultName(record.Command)
	}

	m.mu.Lock()
	m.jobs[record.JobID] = record
	m.mu.Unlock()

	go m.watch(record, a.pumps, a.waited)
	return record, nil
}

// terminalJobCommand stores the command the way subprocess did: the argv list
// for direct exec, the shell string when a shell runs it.
func terminalJobCommand(commandText string, argv []string, spec terminalShellSpec) any {
	if spec.executable != "" {
		return commandText
	}
	clone := make([]string, len(argv))
	copy(clone, argv)
	return clone
}

func terminalJobDefaultName(command any) string {
	switch typed := command.(type) {
	case []string:
		if len(typed) > 0 {
			return typed[0]
		}
	case string:
		if fields := strings.Fields(typed); len(fields) > 0 {
			return fields[0]
		}
	}
	return "job"
}

func terminalPumpStream(file *os.File, ring *terminalRing, wg *sync.WaitGroup) {
	defer wg.Done()
	buf := make([]byte, 4096)
	for {
		n, err := file.Read(buf)
		if n > 0 {
			ring.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}
	file.Close()
	ring.Close()
}

// watch waits for the process, enforces its deadline, drains the pumps and
// stamps the final state. A promoted foreground exec passes its own Wait
// result channel so the process is never waited on twice.
func (m *terminalJobManager) watch(record *terminalJobRecord, pumps *sync.WaitGroup, awaited chan error) {
	if awaited == nil {
		awaited = make(chan error, 1)
		go func() { awaited <- record.cmd.Wait() }()
	}
	waited := awaited

	if record.deadline.IsZero() {
		<-waited
	} else {
		timer := time.NewTimer(time.Until(record.deadline))
		select {
		case <-waited:
			timer.Stop()
		case <-timer.C:
			record.mu.Lock()
			record.timedOut = true
			record.mu.Unlock()
			terminalTerminateTree(record.cmd, 2*time.Second)
			<-waited
		}
	}

	drained := make(chan struct{})
	go func() {
		pumps.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}

	record.stdout.Close()
	if record.stderr != nil {
		record.stderr.Close()
	}
	code := terminalExitCode(record.cmd.ProcessState)

	record.mu.Lock()
	if record.exitedAt.IsZero() {
		record.exitedAt = time.Now()
	}
	record.exitCode = &code
	stdin := record.stdin
	record.stdin = nil
	record.mu.Unlock()
	if stdin != nil {
		stdin.Close()
	}

	m.mu.Lock()
	m.exitedOrder = append(m.exitedOrder, record.JobID)
	for len(m.exitedOrder) > terminalJobRecentKeep {
		oldest := m.exitedOrder[0]
		m.exitedOrder = m.exitedOrder[1:]
		if oldest != record.JobID {
			delete(m.jobs, oldest)
		}
	}
	m.mu.Unlock()

	close(record.done)
}

// wait blocks until the job exits or timeout elapses.
func (m *terminalJobManager) wait(record *terminalJobRecord, timeout time.Duration) {
	if timeout < 0 {
		timeout = 0
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-record.done:
	case <-timer.C:
	}
}

// signal delivers sig to the job's process tree.
func (m *terminalJobManager) signal(record *terminalJobRecord, sig syscall.Signal) bool {
	if record == nil || !record.isRunning() {
		return false
	}
	if sig == syscall.SIGTERM {
		terminalTerminateTree(record.cmd, 2*time.Second)
		return true
	}
	return terminalSignalGroup(record.cmd, sig) == nil
}

func (m *terminalJobManager) writeStdin(record *terminalJobRecord, data []byte) int {
	record.mu.Lock()
	stdin := record.stdin
	if stdin == nil || record.stdinClosed || !record.exitedAt.IsZero() {
		record.mu.Unlock()
		return 0
	}
	record.mu.Unlock()
	n, err := stdin.Write(data)
	if err != nil {
		return 0
	}
	return n
}

func (m *terminalJobManager) closeStdin(record *terminalJobRecord) bool {
	record.mu.Lock()
	if record.stdin == nil || record.stdinClosed {
		record.mu.Unlock()
		return false
	}
	record.stdinClosed = true
	stdin := record.stdin
	record.mu.Unlock()
	return stdin.Close() == nil
}

// list returns running jobs first, then recent exits newest first.
func (m *terminalJobManager) list() []terminalJobSummary {
	m.mu.Lock()
	records := make([]*terminalJobRecord, 0, len(m.jobs))
	for _, record := range m.jobs {
		records = append(records, record)
	}
	m.mu.Unlock()

	sort.SliceStable(records, func(i, j int) bool {
		left, right := records[i], records[j]
		leftRunning, rightRunning := left.isRunning(), right.isRunning()
		if leftRunning != rightRunning {
			return leftRunning
		}
		return left.sortTime().After(right.sortTime())
	})

	out := make([]terminalJobSummary, 0, len(records))
	for _, record := range records {
		out = append(out, record.summary())
	}
	return out
}

// shutdownAll TERMs every running job, waits grace, then KILLs.
func (m *terminalJobManager) shutdownAll(grace time.Duration) {
	m.mu.Lock()
	running := make([]*terminalJobRecord, 0, len(m.jobs))
	for _, record := range m.jobs {
		if record.isRunning() {
			running = append(running, record)
		}
	}
	m.mu.Unlock()

	for _, record := range running {
		_ = terminalSignalGroup(record.cmd, syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive := false
		for _, record := range running {
			if record.isRunning() {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, record := range running {
		if record.isRunning() {
			terminalTerminateTree(record.cmd, 0)
		}
	}
}

// terminalJobStart implements terminal_job_start.
func (t *Toolbox) terminalJobStart(ctx context.Context, input map[string]any) (Result, error) {
	command, ok := stringArg(input, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return terminalErr("terminal_job_start: command is required"), nil
	}
	if blocked := terminalCheckCommand(command); blocked != "" {
		return terminalEncode(struct {
			Error   string `json:"error"`
			Blocked bool   `json:"blocked"`
		}{Error: blocked, Blocked: true}), nil
	}
	env, ok := terminalEnvMap(input, "env")
	if !ok {
		return terminalErr("terminal_job_start: env must map names to strings"), nil
	}
	if principal, ok := stringArg(input, "crm_principal"); ok && strings.TrimSpace(principal) != "" {
		merged := make(map[string]string, len(env)+1)
		for key, value := range env {
			merged[key] = value
		}
		merged["HIVE_CRM_PRINCIPAL"] = principal
		env = merged
	}
	limits := terminalExecLimits(input)
	name, _ := stringArg(input, "name")
	mergeStderr := terminalBool(input, "merge_stderr")
	useShell := terminalBool(input, "shell")

	cwd, err := t.terminalWorkdir(input)
	if err != nil {
		return Result{}, err
	}

	var argv []string
	spec := terminalShellDirect
	if useShell {
		resolved, err := terminalResolveShellSpec(true, "")
		if err != nil {
			return terminalErr("%v", err), nil
		}
		spec = resolved
	} else {
		argv = strings.Fields(command)
		if len(argv) == 0 {
			return terminalErr("command was empty"), nil
		}
	}
	if runtime.GOOS == "windows" && spec.executable == "" {
		spec = terminalWindowsShellSpec()
	}

	// limits are applied through the same ulimit preamble terminal_exec uses:
	// prefixed onto a bash command string, or wrapped around a direct argv.
	spawnCommand := command
	if spec.executable != "" {
		spawnCommand = terminalExecLimitedCommand(command, spec, limits)
	} else {
		argv = terminalExecApplyLimits(argv, limits)
	}

	record, err := terminalJobs.start(spawnCommand, argv, spec, cwd, terminalSanitizedEnv(env), mergeStderr, name)
	if err != nil {
		var limit terminalJobLimitError
		if errors.As(err, &limit) {
			return terminalErr("%s", limit.Error()), nil
		}
		if errors.Is(err, errTerminalEmptyCommand) {
			return terminalErr("command was empty"), nil
		}
		return terminalErr("%v", err), nil
	}
	return terminalEncode(terminalJobStartResult{
		JobID:     record.JobID,
		PID:       record.PID,
		StartedAt: float64(record.StartedAt.UnixNano()) / 1e9,
		Name:      record.Name,
		Merged:    record.Merged,
	}), nil
}

// terminalJobLogs implements terminal_job_logs.
func (t *Toolbox) terminalJobLogs(ctx context.Context, input map[string]any) (Result, error) {
	jobID, ok := stringArg(input, "job_id")
	if !ok || jobID == "" {
		return terminalErr("terminal_job_logs: job_id is required"), nil
	}
	record := terminalJobs.get(jobID)
	if record == nil {
		return terminalErr("unknown job_id: %s", jobID), nil
	}

	stream := "stdout"
	if raw, ok := stringArg(input, "stream"); ok && raw != "" {
		stream = raw
	}
	sinceOffset := 0
	if v, present := input["since_offset"]; present && v != nil {
		parsed, ok := intArg(input, "since_offset")
		if !ok {
			return terminalErr("terminal_job_logs: since_offset must be an integer"), nil
		}
		sinceOffset = parsed
	}
	if sinceOffset < 0 {
		sinceOffset = 0
	}
	maxBytes := terminalJobLogBytes
	if v, present := input["max_bytes"]; present && v != nil {
		parsed, ok := intArg(input, "max_bytes")
		if !ok {
			return terminalErr("terminal_job_logs: max_bytes must be an integer"), nil
		}
		maxBytes = parsed
	}
	if maxBytes < 0 {
		maxBytes = 0
	}

	if terminalBool(input, "wait_until_exit") {
		waitSec := terminalJobWaitMax
		if v, present := input["wait_timeout_sec"]; present && v != nil {
			parsed, ok := finiteNumberArg(input, "wait_timeout_sec")
			if !ok {
				return terminalErr("terminal_job_logs: wait_timeout_sec must be a finite number"), nil
			}
			waitSec = parsed
		}
		if waitSec > terminalJobWaitCap.Seconds() {
			waitSec = terminalJobWaitCap.Seconds()
		}
		if waitSec < 0 {
			waitSec = 0
		}
		terminalJobs.wait(record, time.Duration(waitSec*float64(time.Second)))
	}

	var ring *terminalRing
	switch stream {
	case "merged":
		ring = record.stdout
	case "stderr":
		ring = record.stderr
	default:
		ring = record.stdout
	}
	if ring == nil {
		return terminalErr("stream='%s' not available (merge_stderr=%t)", stream, record.Merged), nil
	}

	read := ring.Read(sinceOffset, maxBytes)
	if terminalBool(input, "tail") {
		read = ring.Tail(maxBytes)
	}
	exitCode, timedOut, _ := record.exitState()
	return terminalEncode(terminalJobLogsResult{
		Data:                  string(read.Data),
		Offset:                read.Offset,
		NextOffset:            read.NextOffset,
		TruncatedBytesDropped: read.TruncatedBytesDropped,
		EOF:                   ring.EOF() && read.NextOffset >= ring.TotalWritten(),
		Status:                record.status(),
		ExitCode:              exitCode,
		TimedOut:              timedOut,
		RuntimeMS:             record.runtimeMS(),
	}), nil
}

// terminalSignalSemantics maps a supported signal action to its behaviour.
type terminalSignalSemantics struct {
	SignalInt  string `json:"signal_int,omitempty"`
	SignalTerm string `json:"signal_term,omitempty"`
	SignalKill string `json:"signal_kill,omitempty"`
	SignalHup  string `json:"signal_hup,omitempty"`
	SignalUsr1 string `json:"signal_usr1,omitempty"`
	SignalUsr2 string `json:"signal_usr2,omitempty"`
}

// terminalJobCapabilities describes the implemented actions, not just the
// constants the platform happens to export.
type terminalJobCapabilities struct {
	Platform         string                  `json:"platform"`
	SupportedActions []string                `json:"supported_actions"`
	Signals          terminalSignalSemantics `json:"signals"`
	Note             string                  `json:"note"`
}

func terminalJobControlCapabilities() terminalJobCapabilities {
	if runtime.GOOS == "windows" {
		return terminalJobCapabilities{
			Platform:         "windows",
			SupportedActions: []string{"capabilities", "list", "stdin", "close_stdin", "signal_term", "signal_kill"},
			Signals: terminalSignalSemantics{
				SignalTerm: "Forcefully terminates the Windows job process tree; application cleanup handlers are not run.",
				SignalKill: "Forcefully terminates the Windows job process tree; application cleanup handlers are not run.",
			},
			Note: "Console interrupt (signal_int / Ctrl-C) is not supported for these background jobs.",
		}
	}
	return terminalJobCapabilities{
		Platform: "posix",
		SupportedActions: []string{
			"capabilities", "list", "stdin", "close_stdin",
			"signal_int", "signal_term", "signal_kill", "signal_hup", "signal_usr1", "signal_usr2",
		},
		Signals: terminalSignalSemantics{
			SignalInt:  "Send SIGINT to the process group; cleanup depends on the application.",
			SignalTerm: "Send SIGTERM to the process group, then force cleanup after up to 2 seconds.",
			SignalKill: "Send SIGKILL to the process group; application cleanup handlers are not run.",
			SignalHup:  "Send SIGHUP to the process group; application-defined behavior.",
			SignalUsr1: "Send SIGUSR1 to the process group; application-defined behavior.",
			SignalUsr2: "Send SIGUSR2 to the process group; application-defined behavior.",
		},
		Note: "Check exit status with terminal_job_logs after requesting a signal.",
	}
}

func (c terminalJobCapabilities) signalSemantics(action string) string {
	switch action {
	case "signal_int":
		return c.Signals.SignalInt
	case "signal_term":
		return c.Signals.SignalTerm
	case "signal_kill":
		return c.Signals.SignalKill
	case "signal_hup":
		return c.Signals.SignalHup
	case "signal_usr1":
		return c.Signals.SignalUsr1
	case "signal_usr2":
		return c.Signals.SignalUsr2
	}
	return ""
}

// terminalJobManage implements terminal_job_manage: capabilities, list,
// signals and stdin behind one action argument.
func (t *Toolbox) terminalJobManage(ctx context.Context, input map[string]any) (Result, error) {
	action, ok := stringArg(input, "action")
	if !ok || action == "" {
		return terminalErr("terminal_job_manage: action is required"), nil
	}
	capabilities := terminalJobControlCapabilities()
	if action == "capabilities" {
		return terminalEncode(capabilities), nil
	}
	if action == "list" {
		return terminalEncode(struct {
			Jobs         []terminalJobSummary    `json:"jobs"`
			Capabilities terminalJobCapabilities `json:"capabilities"`
		}{Jobs: terminalJobs.list(), Capabilities: capabilities}), nil
	}
	supported := false
	for _, candidate := range capabilities.SupportedActions {
		if candidate == action {
			supported = true
			break
		}
	}
	if !supported {
		return terminalEncode(struct {
			Error        string                  `json:"error"`
			Code         string                  `json:"code"`
			Capabilities terminalJobCapabilities `json:"capabilities"`
		}{
			Error:        fmt.Sprintf("action='%s' is not supported on %s", action, capabilities.Platform),
			Code:         "unsupported_action",
			Capabilities: capabilities,
		}), nil
	}

	jobID, _ := stringArg(input, "job_id")
	if jobID == "" {
		return terminalErr("action='%s' requires job_id", action), nil
	}
	record := terminalJobs.get(jobID)

	if sig, isSignal := terminalSignalForAction(action); isSignal {
		ok := record != nil && terminalJobs.signal(record, sig)
		verb := strings.TrimPrefix(action, "signal_")
		return terminalEncode(struct {
			OK        bool   `json:"ok"`
			Signal    string `json:"signal"`
			Semantics string `json:"semantics"`
		}{OK: ok, Signal: strings.ToUpper(verb), Semantics: capabilities.signalSemantics(action)}), nil
	}

	data, hasData := stringArg(input, "data")
	if action == "stdin" {
		if !hasData {
			return terminalErr("action=stdin requires data"), nil
		}
		written := 0
		if record != nil {
			written = terminalJobs.writeStdin(record, []byte(data))
		}
		return terminalEncode(struct {
			BytesWritten int `json:"bytes_written"`
		}{BytesWritten: written}), nil
	}
	if action == "close_stdin" {
		closed := record != nil && terminalJobs.closeStdin(record)
		return terminalEncode(struct {
			OK bool `json:"ok"`
		}{OK: closed}), nil
	}
	return terminalErr("unknown action: '%s'", action), nil
}
