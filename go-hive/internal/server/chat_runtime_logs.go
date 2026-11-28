package server

// Three-level runtime-log wiring for the chat runtime (extracted from
// chat_runtime.go to keep that file within the repo's file-size budget).
//
// Parity: framework/loader/agent_loader.py::AgentLoader._setup
// (RuntimeLogStore(base_path=self._storage_path / "runtime_logs"))
// Parity: framework/orchestrator/orchestrator.py::execute (end_run)

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/runtimelogs"
)

// newChatRuntimeLogger wires the three-level runtime log store for a
// session-shaped root (<storage>/sessions/<session_id>). Upstream keeps the
// store at {storage}/runtime_logs and uses the session id as the run id, so
// every level lands under {storage}/sessions/{id}/logs/. Roots that are not
// shaped like a session directory (unit tests, ad-hoc dirs) stay unwired.
func newChatRuntimeLogger(root, sessionID string) *runtimelogs.RuntimeLogger {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	clean := filepath.Clean(root)
	if filepath.Base(filepath.Dir(clean)) != "sessions" {
		return nil
	}
	storageRoot := filepath.Dir(filepath.Dir(clean))
	logger := runtimelogs.NewRuntimeLogger(
		runtimelogs.NewRuntimeLogStore(filepath.Join(storageRoot, "runtime_logs")), "queen")
	logger.StartRun("", sessionID)
	// Upstream's execution manager establishes trace context at
	// Runtime.start_run (framework/host/execution_manager.py ->
	// stream_runtime.py::set_trace_context) before the runtime logger records
	// anything. Bind the equivalent pair to this session's logger: assigning
	// the package-level TraceContextProvider here would race between
	// concurrently running sessions and could stamp one session's trace on
	// another's records.
	logger.SetTraceContext(runtimelogs.NewTraceContext())
	return logger
}

// finishRuntimeLogs writes the L1 summary when the session's run is over.
// Status follows the most recent turn's outcome; a logging failure never
// fails a close (EndRun swallows it).
func (r *ChatRuntime) finishRuntimeLogs() {
	if r == nil || r.runtimeLogger == nil {
		return
	}
	status := "success"
	// A turn cancelled by teardown is not a failed run: the runtime is going
	// away, not the model failing. Only a real turn error claims failure.
	if err := r.LastError(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		status = "failure"
	}
	r.runtimeLogger.EndRun(status, int(time.Since(r.runtimeLogStartedAt).Milliseconds()), []string{"queen"}, "")
}
