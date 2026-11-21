package builtin

// The terminal_exec result envelope. The Python reference builds it in
// tools/src/terminal_tools/common/truncation.py::build_exec_envelope (and
// _err_envelope for pre-spawn failures); the model-visible text is that dict,
// serialized. Field order, null-vs-value semantics and the truncation handle
// rules are part of the contract — keep them in step with the Python module
// and docs/tool-output-contract.md.

// terminalExecResult mirrors build_exec_envelope()'s keys, in order.
type terminalExecResult struct {
	ExitCode             *int    `json:"exit_code"`
	Stdout               string  `json:"stdout"`
	Stderr               string  `json:"stderr"`
	StdoutTruncatedBytes int     `json:"stdout_truncated_bytes"`
	StderrTruncatedBytes int     `json:"stderr_truncated_bytes"`
	RuntimeMS            int     `json:"runtime_ms"`
	PID                  *int    `json:"pid"`
	OutputHandle         *string `json:"output_handle"`
	TimedOut             bool    `json:"timed_out"`
	SemanticStatus       string  `json:"semantic_status"`
	SemanticMessage      *string `json:"semantic_message"`
	Warning              *string `json:"warning"`
	AutoBackgrounded     bool    `json:"auto_backgrounded"`
	JobID                *string `json:"job_id"`
	AutoShell            bool    `json:"auto_shell"`
	ShellKind            *string `json:"shell_kind"`
}

// terminalExecErrorResult mirrors _err_envelope: the same keys plus a trailing
// `error`. semantic_status is "error", semantic_message carries the message.
type terminalExecErrorResult struct {
	ExitCode             *int    `json:"exit_code"`
	Stdout               string  `json:"stdout"`
	Stderr               string  `json:"stderr"`
	StdoutTruncatedBytes int     `json:"stdout_truncated_bytes"`
	StderrTruncatedBytes int     `json:"stderr_truncated_bytes"`
	RuntimeMS            int     `json:"runtime_ms"`
	PID                  *int    `json:"pid"`
	OutputHandle         *string `json:"output_handle"`
	TimedOut             bool    `json:"timed_out"`
	SemanticStatus       string  `json:"semantic_status"`
	SemanticMessage      *string `json:"semantic_message"`
	Warning              *string `json:"warning"`
	AutoBackgrounded     bool    `json:"auto_backgrounded"`
	JobID                *string `json:"job_id"`
	AutoShell            bool    `json:"auto_shell"`
	ShellKind            *string `json:"shell_kind"`
	Error                string  `json:"error"`
}

// terminalExecErrorEnvelope renders a pre-spawn failure exactly like
// exec.py::_err_envelope: a normal tool result (not an error-marked one — the
// Python tool returns the dict, so the loop does not prefix "ERROR: ").
func terminalExecErrorEnvelope(message string) Result {
	return terminalEncode(terminalExecErrorResult{
		Stdout:          "",
		Stderr:          message,
		SemanticStatus:  "error",
		SemanticMessage: &message,
		Error:           message,
	})
}

// terminalExecBlockedEnvelope is _err_envelope plus the `blocked: true` marker
// the guard adds when a command is refused before spawn.
func terminalExecBlockedEnvelope(message string) Result {
	return terminalEncode(struct {
		ExitCode             *int    `json:"exit_code"`
		Stdout               string  `json:"stdout"`
		Stderr               string  `json:"stderr"`
		StdoutTruncatedBytes int     `json:"stdout_truncated_bytes"`
		StderrTruncatedBytes int     `json:"stderr_truncated_bytes"`
		RuntimeMS            int     `json:"runtime_ms"`
		PID                  *int    `json:"pid"`
		OutputHandle         *string `json:"output_handle"`
		TimedOut             bool    `json:"timed_out"`
		SemanticStatus       string  `json:"semantic_status"`
		SemanticMessage      *string `json:"semantic_message"`
		Warning              *string `json:"warning"`
		AutoBackgrounded     bool    `json:"auto_backgrounded"`
		JobID                *string `json:"job_id"`
		AutoShell            bool    `json:"auto_shell"`
		ShellKind            *string `json:"shell_kind"`
		Error                string  `json:"error"`
		Blocked              bool    `json:"blocked"`
	}{
		Stdout:          "",
		Stderr:          message,
		SemanticStatus:  "error",
		SemanticMessage: &message,
		Error:           message,
		Blocked:         true,
	})
}

// terminalExecEnvelopeResult builds the success envelope: cmd output is capped
// per stream, the overflow is stashed under an output_handle, and the exit
// code is classified with the command-specific semantic catalog.
func terminalExecEnvelopeResult(command string, exitCode *int, stdoutBytes, stderrBytes []byte, runtimeMS int, pid *int, timedOut, signaled bool, maxBytes int, autoBackgrounded bool, jobID *string, autoShell bool, shellKind *string) terminalExecResult {
	stdout, stdoutDropped := terminalTruncateStream(stdoutBytes, maxBytes)
	stderr, stderrDropped := terminalTruncateStream(stderrBytes, maxBytes)

	var outputHandle *string
	if stdoutDropped > 0 || stderrDropped > 0 {
		// The *full* bytes are stored, matching _truncate_bytes' third return:
		// the model pages through exactly what the process emitted.
		if handle := terminalOutputs.Put(terminalCombinedOutput(stdoutBytes, stderrBytes)); handle != "" {
			outputHandle = &handle
		}
	}

	status, message := terminalClassifyExit(command, exitCode, timedOut, signaled)
	return terminalExecResult{
		ExitCode:             exitCode,
		Stdout:               string(stdout),
		Stderr:               string(stderr),
		StdoutTruncatedBytes: stdoutDropped,
		StderrTruncatedBytes: stderrDropped,
		RuntimeMS:            runtimeMS,
		PID:                  pid,
		OutputHandle:         outputHandle,
		TimedOut:             timedOut,
		SemanticStatus:       status,
		SemanticMessage:      message,
		Warning:              terminalDestructiveWarning(command),
		AutoBackgrounded:     autoBackgrounded,
		JobID:                jobID,
		AutoShell:            autoShell,
		ShellKind:            shellKind,
	}
}
