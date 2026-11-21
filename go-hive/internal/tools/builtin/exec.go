package builtin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	// killGrace bounds how long a killed process may keep us waiting.
	killGrace = 5 * time.Second
	// terminalExecInlineMaxSeconds mirrors upstream MAX_INLINE_SECONDS: the
	// foreground wait must stay inside the caller's tool-call budget.
	terminalExecInlineMaxSeconds = 220.0
	// terminalExecAutoBackgroundDefault mirrors the
	// auto_background_after_sec default.
	terminalExecAutoBackgroundDefault = 30.0
	// terminalExecPromotedTailBytes is how much already-produced output a
	// promoted envelope carries (upstream uses 64 KiB).
	terminalExecPromotedTailBytes = 64 * 1024
	// terminalExecDrainGrace bounds the post-exit wait for the pipe pumps.
	terminalExecDrainGrace = 2 * time.Second
	// terminalExecTimeoutMessage is upstream's validation error verbatim.
	terminalExecTimeoutMessage = "timeout_sec and auto_background_after_sec must be finite and non-negative"
	// terminalExecForegroundMessage is upstream's inline-budget error verbatim.
	terminalExecForegroundMessage = "Foreground wait must be at most 220 seconds; use auto_background_after_sec=30 or terminal_job_start for longer commands"
)

// terminalExecArgs is one validated terminal_exec call.
type terminalExecArgs struct {
	command           string
	timeoutSec        float64
	autoBackgroundSec float64
	wakeSeconds       float64
	shell             bool
	env               map[string]string
	stdinPayload      string
	hasStdin          bool
	maxOutputBytes    int
	limits            map[string]int
	cwd               string
}

// terminalExecProcess is a spawned terminal_exec command plus the pieces
// needed to wait on it, promote it or reap it.
type terminalExecProcess struct {
	cmd            *exec.Cmd
	spec           terminalShellSpec
	autoShell      bool
	displayCommand any
	stdout         *terminalRing
	stderr         *terminalRing
	pumps          *sync.WaitGroup
	waited         chan error
	started        time.Time
	wakeAt         time.Time
	deadline       time.Time
}

// terminalExec implements terminal_exec: direct-exec with automatic shell
// fallback, session-workdir cwd, env/stdin/limits overrides, bounded inline
// output with an output_handle, and auto-promotion into the background job
// manager past auto_background_after_sec.
//
// Parity: tools/src/terminal_tools/exec.py::register_exec_tools
func (t *Toolbox) terminalExec(ctx context.Context, input map[string]any) (Result, error) {
	args, failed, err := t.terminalExecArgs(input)
	if err != nil {
		return Result{}, err
	}
	if failed != nil {
		return *failed, nil
	}
	if blocked := terminalCheckCommand(args.command); blocked != "" {
		return terminalExecBlockedEnvelope(blocked), nil
	}
	proc, failed, err := t.terminalExecStart(args)
	if err != nil {
		return Result{}, err
	}
	if failed != nil {
		return *failed, nil
	}

	timer := time.NewTimer(time.Until(proc.wakeAt))
	defer timer.Stop()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-proc.waited:
	case <-timer.C:
		if record, promoted := t.terminalExecPromote(proc, args); promoted {
			runtimeMS := int(time.Since(proc.started).Milliseconds())
			jobID := record.JobID
			pid := proc.cmd.Process.Pid
			shellKind := proc.spec.kind
			return terminalEncode(terminalExecEnvelopeResult(
				args.command,
				nil,
				proc.stdout.Tail(terminalExecPromotedTailBytes).Data,
				proc.stderr.Tail(terminalExecPromotedTailBytes).Data,
				runtimeMS,
				&pid,
				false,
				false,
				args.maxOutputBytes,
				true,
				&jobID,
				proc.autoShell,
				&shellKind,
			)), nil
		}
		terminalTerminateTree(proc.cmd, 2*time.Second)
		timedOut = true
		select {
		case waitErr = <-proc.waited:
		case <-time.After(killGrace):
			return Result{}, fmt.Errorf("terminal_exec: command timed out after %s and could not be killed", time.Duration(args.timeoutSec*float64(time.Second)))
		}
	case <-ctx.Done():
		terminalTerminateTree(proc.cmd, 2*time.Second)
		select {
		case <-proc.waited:
		case <-time.After(killGrace):
			return Result{}, fmt.Errorf("terminal_exec: cancelled and could not kill command: %w", ctx.Err())
		}
		return Result{}, ctx.Err()
	}

	return terminalExecInlineResult(proc, args, timedOut, waitErr), nil
}

// terminalExecArgs validates the call's arguments the way upstream does: a
// missing command, a non-finite wait, no finite foreground budget or a bad
// env/stdin/max_output_kb payload is an expected tool failure. A failed cwd
// resolution is an infrastructure error.
func (t *Toolbox) terminalExecArgs(input map[string]any) (terminalExecArgs, *Result, error) {
	command, ok := stringArg(input, "command")
	if !ok || strings.TrimSpace(command) == "" {
		failed := terminalExecErrorEnvelope("terminal_exec: command is required")
		return terminalExecArgs{}, &failed, nil
	}
	args := terminalExecArgs{command: command}

	// timeout_sec is the upstream name; timeout_seconds is the legacy Go alias
	// the first port advertised. Both accept fractional seconds.
	timeoutSec, present, valid := terminalExecNumber(input, "timeout_sec", "timeout_seconds")
	if present && !valid {
		failed := terminalExecErrorEnvelope(terminalExecTimeoutMessage)
		return terminalExecArgs{}, &failed, nil
	}
	if !present {
		timeoutSec = DefaultCommandTimeout.Seconds()
	}
	autoBackgroundSec, present, valid := terminalExecNumber(input, "auto_background_after_sec")
	if present && !valid {
		failed := terminalExecErrorEnvelope(terminalExecTimeoutMessage)
		return terminalExecArgs{}, &failed, nil
	}
	if !present {
		autoBackgroundSec = terminalExecAutoBackgroundDefault
	}
	if timeoutSec < 0 || autoBackgroundSec < 0 {
		failed := terminalExecErrorEnvelope(terminalExecTimeoutMessage)
		return terminalExecArgs{}, &failed, nil
	}
	waits := make([]float64, 0, 2)
	if timeoutSec > 0 {
		waits = append(waits, timeoutSec)
	}
	if autoBackgroundSec > 0 {
		waits = append(waits, autoBackgroundSec)
	}
	if len(waits) == 0 || terminalMinWait(waits) > terminalExecInlineMaxSeconds {
		failed := terminalExecErrorEnvelope(terminalExecForegroundMessage)
		return terminalExecArgs{}, &failed, nil
	}
	args.timeoutSec = timeoutSec
	args.autoBackgroundSec = autoBackgroundSec
	args.wakeSeconds = terminalMinWait(waits)

	args.shell = boolArgOr(input, "shell", false)
	env, envOK := terminalEnvMap(input, "env")
	if !envOK {
		failed := terminalExecErrorEnvelope("terminal_exec: env must map names to strings")
		return terminalExecArgs{}, &failed, nil
	}
	if principal, ok := stringArg(input, "crm_principal"); ok && strings.TrimSpace(principal) != "" {
		merged := make(map[string]string, len(env)+1)
		for name, value := range env {
			merged[name] = value
		}
		merged["HIVE_CRM_PRINCIPAL"] = principal
		env = merged
	}
	args.env = env

	if raw, present := input["stdin"]; present && raw != nil {
		payload, ok := raw.(string)
		if !ok {
			failed := terminalExecErrorEnvelope("terminal_exec: stdin must be a string")
			return terminalExecArgs{}, &failed, nil
		}
		args.stdinPayload, args.hasStdin = payload, true
	}

	args.maxOutputBytes = t.outputLimit
	if args.maxOutputBytes <= 0 {
		args.maxOutputBytes = MaxCommandOutputBytes
	}
	if raw, present := input["max_output_kb"]; present && raw != nil {
		kb, ok := intArg(input, "max_output_kb")
		if !ok {
			failed := terminalExecErrorEnvelope("terminal_exec: max_output_kb must be an integer")
			return terminalExecArgs{}, &failed, nil
		}
		args.maxOutputBytes = max(1024, kb*1024)
	}
	args.limits = terminalExecLimits(input)

	cwd, err := t.terminalWorkdir(input)
	if err != nil {
		return terminalExecArgs{}, nil, err
	}
	args.cwd = cwd
	return args, nil, nil
}

// terminalExecStart resolves the shell mode, spawns the process and starts the
// output pumps plus the single Wait owner.
func (t *Toolbox) terminalExecStart(args terminalExecArgs) (*terminalExecProcess, *Result, error) {
	// Upstream auto-detects shell syntax when shell=False: tokens with pipes,
	// redirects or unexpanded globs are routed through the platform shell
	// instead of being mangled into junk argv.
	var tokens []string
	autoShell := false
	if !args.shell {
		split, splitErr := terminalSplitCommand(args.command)
		switch {
		case splitErr != nil:
			// Unbalanced quotes — almost certainly meant for the shell.
			autoShell = true
		case len(split) == 0:
			failed := terminalExecErrorEnvelope("command was empty")
			return nil, &failed, nil
		case terminalCommandNeedsShell(split):
			autoShell = true
		default:
			tokens = split
		}
	}
	spec := terminalShellDirect
	if args.shell || autoShell {
		resolved, err := terminalResolveShellSpec(true, "")
		if err != nil {
			failed := terminalExecErrorEnvelope(err.Error())
			return nil, &failed, nil
		}
		spec = resolved
	}
	if spec.executable == "" && runtime.GOOS == "windows" {
		// Windows has no useful direct-exec path for POSIX-shaped argv.
		spec = terminalWindowsShellSpec()
	}
	spawnArgv := tokens
	if spec.executable != "" {
		spawnArgv = spec.buildArgv(terminalExecLimitedCommand(args.command, spec, args.limits))
	} else {
		spawnArgv = terminalExecApplyLimits(spawnArgv, args.limits)
	}
	displayCommand := terminalJobCommand(args.command, tokens, spec)

	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		return nil, nil, err
	}
	cmd := exec.Command(spawnArgv[0], spawnArgv[1:]...)
	cmd.Dir = args.cwd
	cmd.Env = terminalSanitizedEnv(args.env)
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite
	var stdinRead, stdinWrite *os.File
	if args.hasStdin {
		stdinRead, stdinWrite, err = os.Pipe()
		if err != nil {
			stdoutRead.Close()
			stdoutWrite.Close()
			stderrRead.Close()
			stderrWrite.Close()
			return nil, nil, err
		}
		cmd.Stdin = stdinRead
	}
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		stdoutRead.Close()
		stdoutWrite.Close()
		stderrRead.Close()
		stderrWrite.Close()
		if stdinRead != nil {
			stdinRead.Close()
			stdinWrite.Close()
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			failed := terminalExecErrorEnvelope("command not found: " + spawnArgv[0])
			return nil, &failed, nil
		}
		failed := terminalExecErrorEnvelope("spawn failed: " + err.Error())
		return nil, &failed, nil
	}
	if stdinRead != nil {
		stdinRead.Close()
		go func(payload string, sink *os.File) {
			_, _ = sink.WriteString(payload)
			_ = sink.Close()
		}(args.stdinPayload, stdinWrite)
	}
	stdoutWrite.Close()
	stderrWrite.Close()

	proc := &terminalExecProcess{
		cmd:            cmd,
		spec:           spec,
		autoShell:      autoShell,
		displayCommand: displayCommand,
		stdout:         newTerminalRing(terminalRingJobBytes),
		stderr:         newTerminalRing(terminalRingJobBytes),
		pumps:          &sync.WaitGroup{},
		waited:         make(chan error, 1),
		started:        time.Now(),
	}
	proc.wakeAt = proc.started.Add(time.Duration(args.wakeSeconds * float64(time.Second)))
	if args.timeoutSec > 0 {
		proc.deadline = proc.started.Add(time.Duration(args.timeoutSec * float64(time.Second)))
	}
	proc.pumps.Add(2)
	go terminalPumpStream(stdoutRead, proc.stdout, proc.pumps)
	go terminalPumpStream(stderrRead, proc.stderr, proc.pumps)
	go func() { proc.waited <- proc.cmd.Wait() }()
	return proc, nil, nil
}

// terminalExecPromote hands a still-running process to the job manager when
// the inline budget elapses before the overall deadline.
func (t *Toolbox) terminalExecPromote(proc *terminalExecProcess, args terminalExecArgs) (*terminalJobRecord, bool) {
	if args.autoBackgroundSec <= 0 || (!proc.deadline.IsZero() && !time.Now().Before(proc.deadline)) {
		return nil, false
	}
	record, err := terminalJobs.adopt(terminalExecAdoption{
		cmd:       proc.cmd,
		stdout:    proc.stdout,
		stderr:    proc.stderr,
		pumps:     proc.pumps,
		waited:    proc.waited,
		startedAt: proc.started,
		deadline:  proc.deadline,
		command:   proc.displayCommand,
	})
	if err != nil {
		return nil, false
	}
	return record, true
}

// terminalExecInlineResult drains the pumps and renders the completed command.
func terminalExecInlineResult(proc *terminalExecProcess, args terminalExecArgs, timedOut bool, waitErr error) Result {
	drained := make(chan struct{})
	go func() { proc.pumps.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(terminalExecDrainGrace):
	}
	proc.stdout.Close()
	proc.stderr.Close()

	exitCode := terminalExitCode(proc.cmd.ProcessState)
	pid := proc.cmd.Process.Pid
	shellKind := proc.spec.kind
	runtimeMS := int(time.Since(proc.started).Milliseconds())
	return terminalEncode(terminalExecEnvelopeResult(
		args.command,
		&exitCode,
		proc.stdout.Read(0, proc.stdout.TotalWritten()).Data,
		proc.stderr.Read(0, proc.stderr.TotalWritten()).Data,
		runtimeMS,
		&pid,
		timedOut,
		exitCode < 0,
		args.maxOutputBytes,
		false,
		nil,
		proc.autoShell,
		&shellKind,
	))
}

// terminalExecNumber reads the first present numeric key. It reports
// (value, present, valid) so a present-but-invalid argument can be turned
// into the upstream validation error instead of silently defaulting.
func terminalExecNumber(input map[string]any, keys ...string) (float64, bool, bool) {
	for _, key := range keys {
		raw, present := input[key]
		if !present || raw == nil {
			continue
		}
		value, ok := finiteNumberArg(input, key)
		return value, true, ok
	}
	return 0, false, true
}

// terminalMinWait returns the smallest wait. Callers only pass a non-empty
// slice.
func terminalMinWait(waits []float64) float64 {
	smallest := waits[0]
	for _, wait := range waits[1:] {
		if wait < smallest {
			smallest = wait
		}
	}
	return smallest
}

// terminalTruncateStream caps one stream at maxBytes and reports how many
// bytes were dropped.
func terminalTruncateStream(data []byte, maxBytes int) ([]byte, int) {
	if maxBytes < 0 {
		maxBytes = 0
	}
	if len(data) <= maxBytes {
		return data, 0
	}
	return data[:maxBytes], len(data) - maxBytes
}

// terminalCombinedOutput is the payload an output_handle exposes: both
// streams with the separators the foundational skill documents.
func terminalCombinedOutput(stdout, stderr []byte) []byte {
	combined := make([]byte, 0, len(stdout)+len(stderr)+32)
	combined = append(combined, "--- stdout ---\n"...)
	combined = append(combined, stdout...)
	combined = append(combined, "\n--- stderr ---\n"...)
	combined = append(combined, stderr...)
	return combined
}
