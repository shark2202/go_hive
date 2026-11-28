package agentloop

// Turn-level duplicate-fingerprint window (A5).
//
// Upstream's 6f' guard fingerprints the *whole batch* of real tool calls in a
// turn — order-sensitive, duplicates preserved — keeps the last
// tool_doom_loop_threshold batches, and trips when every batch in the window is
// identical. The per-call DoomLoopDetector cannot see that shape: a turn that
// alternates [A, B] never builds a consecutive single-call streak, so a model
// can repeat the same two-call plan forever without the port noticing.
//
// This window is additive, not a replacement: the per-call detector keeps the
// P0-14 colony contract (warn at 3, auto-escalate on the first detection, park
// at 5). When the per-call guard warns, the window is cleared for that turn so
// the two guards never double-report one repetition.
//
// Parity: framework/agent_loop/internals/stall_detector.py::fingerprint_tool_calls
// Parity: framework/agent_loop/internals/stall_detector.py::is_tool_doom_loop
// Parity: framework/agent_loop/agent_loop.py::_execute_impl (6f' tool doom loop)

import (
	"fmt"
	"strings"
	"sync"
)

// batchFingerprint is one tool call's identity inside a turn's fingerprint
// list. The name is kept separately so the doom-loop description can print the
// same tool names upstream joins, and args is the canonical argument JSON the
// cursor persists (upstream's (name, canonical_args) pair).
type batchFingerprint struct {
	name string
	fp   string
	args string
}

// ToolFingerprint is one tool call in upstream's cursor-persisted form:
// fingerprint_tool_calls returns (tool_name, canonical_args_json) pairs, and
// recent_tool_fingerprints is the list of per-turn pair lists.
//
// Parity: framework/agent_loop/internals/stall_detector.py::fingerprint_tool_calls
type ToolFingerprint struct {
	Name      string
	Arguments string
}

// DoomLoopWindow tracks recent per-turn tool-call fingerprint lists. The zero
// value is ready: Threshold 0 means DefaultDoomLoopThreshold.
type DoomLoopWindow struct {
	// Threshold is the number of consecutive turns that must carry the same
	// ordered fingerprint list before the window trips. It mirrors upstream's
	// tool_doom_loop_threshold, which doubles as the window size.
	Threshold int

	mu       sync.Mutex
	window   [][]batchFingerprint
	lastDesc string
}

// NewDoomLoopWindow returns a window with the default threshold.
func NewDoomLoopWindow() *DoomLoopWindow { return &DoomLoopWindow{} }

func (w *DoomLoopWindow) threshold() int {
	if w == nil {
		return DefaultDoomLoopThreshold
	}
	if w.Threshold > 0 {
		return w.Threshold
	}
	return DefaultDoomLoopThreshold
}

// ObserveTurn records one turn's guarded calls and reports the upstream doom
// loop description when the last `threshold` turns are identical. An empty
// call list is a text-only turn: it breaks the chain, exactly like upstream's
// recent_tool_fingerprints.clear() in the else branch.
//
// The caller owns the routing decision (worker escalation, queen park, or a
// plain warning) and must Clear the window once it has acted, matching
// upstream's per-branch clear.
func (w *DoomLoopWindow) ObserveTurn(calls []ToolCallRequest) string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(calls) == 0 {
		w.window = nil
		w.lastDesc = ""
		return ""
	}
	batch := make([]batchFingerprint, 0, len(calls))
	for _, call := range calls {
		fp, args := toolCallFingerprintParts(call.Name, call.Input)
		batch = append(batch, batchFingerprint{
			name: call.Name,
			fp:   fp,
			args: args,
		})
	}
	w.window = append(w.window, batch)
	limit := w.threshold()
	for len(w.window) > limit {
		w.window = w.window[1:]
	}
	if len(w.window) < limit {
		return ""
	}
	first := w.window[0]
	for _, candidate := range w.window[1:] {
		if len(candidate) != len(first) {
			return ""
		}
		for i := range first {
			if candidate[i].fp != first[i].fp {
				return ""
			}
		}
	}
	names := make([]string, 0, len(first))
	for _, item := range first {
		names = append(names, item.name)
	}
	w.lastDesc = fmt.Sprintf("Doom loop detected: %d identical consecutive tool calls (%s)",
		len(w.window), strings.Join(names, ", "))
	return w.lastDesc
}

// Snapshot returns a deep copy of the window in upstream's cursor shape: one
// entry per recorded turn, each an ordered list of (name, canonical args)
// pairs. The caller can persist it directly as recent_tool_fingerprints.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::write_cursor
func (w *DoomLoopWindow) Snapshot() [][]ToolFingerprint {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.window) == 0 {
		return nil
	}
	out := make([][]ToolFingerprint, 0, len(w.window))
	for _, batch := range w.window {
		turn := make([]ToolFingerprint, 0, len(batch))
		for _, item := range batch {
			turn = append(turn, ToolFingerprint{Name: item.name, Arguments: item.args})
		}
		out = append(out, turn)
	}
	return out
}

// Restore replaces the window with fingerprints read from the cursor, so a
// cold-started loop resumes the interrupted turn's repetition history. Empty
// input clears the window, which is how a reset state clears the persisted
// key on the next write.
//
// Parity: framework/agent_loop/internals/cursor_persistence.py::restore
func (w *DoomLoopWindow) Restore(turns [][]ToolFingerprint) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.window = nil
	w.lastDesc = ""
	for _, turn := range turns {
		if len(turn) == 0 {
			// Upstream never records an empty turn (a text-only turn clears
			// the list), so an empty entry carries no repetition signal.
			continue
		}
		batch := make([]batchFingerprint, 0, len(turn))
		for _, item := range turn {
			batch = append(batch, batchFingerprint{
				name: item.Name,
				fp:   item.Name + "\x00" + item.Arguments,
				args: item.Arguments,
			})
		}
		w.window = append(w.window, batch)
	}
	limit := w.threshold()
	for len(w.window) > limit {
		w.window = w.window[1:]
	}
}

// Clear drops the window. Upstream clears it after escalating, after parking
// the queen, and on a text-only turn.
func (w *DoomLoopWindow) Clear() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.window = nil
	w.lastDesc = ""
}

// Depth is the number of turns currently held by the window.
func (w *DoomLoopWindow) Depth() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.window)
}

// LastDescription is the most recent upstream-style detection description.
func (w *DoomLoopWindow) LastDescription() string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastDesc
}
