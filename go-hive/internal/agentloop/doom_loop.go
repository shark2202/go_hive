package agentloop

// Doom-loop / stall detection inside the turn loop.
//
// The audit's gap was "repeated tool calls are never noticed": a model that
// re-issues the same call with the same arguments gets the same result forever,
// and nothing in the loop says so. Upstream has two deterministic guards:
// exact tool-call fingerprints and response-text n-gram similarity
// (framework/agent_loop/internals/stall_detector.py). This file ports the
// tool-fingerprint half; the response-text half lives in stall_detector.go.
// The judge-based `stall_check` (framework/server/stall_check.py) is a third,
// turn-end guard, wired separately in internal/server/chat_loop_guards.go.
//
// This file ports the per-call fingerprint half (the P0-14 guard: warn at the
// threshold, park past the park threshold). The turn-level duplicate-batch
// half of upstream's 6f' guard lives in doom_loop_window.go; both halves hand
// their first detection of a turn-loop to the host's DoomLoopSink, which
// publishes node_tool_doom_loop. The per-call warn/park ladder is the port's
// own deterministic guard (upstream has no per-call park), while the window is
// upstream's exact-fingerprint branch: escalate for a colony worker, park a
// queen on the user, warn otherwise.

import (
	"encoding/json"
	"fmt"
	"sync"
)

// DefaultDoomLoopThreshold is how many identical consecutive calls trip the
// warning. Three is the first count at which "same input, same output" is
// certain for a deterministic tool, and leaves room for one retry.
const DefaultDoomLoopThreshold = 3

// DefaultDoomLoopParkThreshold is the streak at which the loop stops warning
// and parks the turn with ParkReasonDoomLoop. Five identical consecutive calls
// with identical arguments is past any retry/polling story: the model has
// stopped reacting to the result it already has.
const DefaultDoomLoopParkThreshold = 5

// DoomLoopReminder is the advisory appended to the tool result. It names the
// call and the streak so the model can see the repetition, and says what to do
// instead of repeating it.
const DoomLoopReminder = "<system-reminder>\n" +
	"You have now called `%s` %d times in a row with identical arguments, and each call returned the same " +
	"thing. Repeating it will not change the result. Change approach: re-read the last result, try different " +
	"arguments, use another tool, or tell the user what is blocking you. Don't mention this reminder.\n" +
	"</system-reminder>"

// ToolCallFingerprint is the identity of one tool call: the tool name plus its
// arguments encoded canonically (encoding/json sorts map keys, so two calls
// that differ only in argument order fingerprint the same).
func ToolCallFingerprint(name string, input map[string]any) string {
	fingerprint, _ := toolCallFingerprintParts(name, input)
	return fingerprint
}

// toolCallFingerprintParts returns the identity and the canonical argument
// JSON separately. The cursor persists upstream's (tool name, canonical args)
// pair, so the argument half must stay available after the identity is built.
//
// Parity: framework/agent_loop/internals/stall_detector.py::fingerprint_tool_calls
func toolCallFingerprintParts(name string, input map[string]any) (string, string) {
	encoded, err := json.Marshal(input)
	if err != nil {
		// An argument map that cannot be encoded cannot be compared; fall back
		// to the name alone so a repeated call still counts as repeated.
		return name + "\x00?", "?"
	}
	args := string(encoded)
	return name + "\x00" + args, args
}

// DoomLoopDetector tracks consecutive identical tool calls. The zero value is
// ready: Threshold 0 means DefaultDoomLoopThreshold, ParkThreshold 0 means
// DefaultDoomLoopParkThreshold.
type DoomLoopDetector struct {
	Threshold int
	// ParkThreshold is the streak at which the loop parks the turn instead of
	// only warning. It must be above Threshold (a check cannot park before it
	// warns); 0 selects DefaultDoomLoopParkThreshold.
	ParkThreshold int

	mu          sync.Mutex
	fingerprint string
	streak      int
	warned      bool
	toolName    string
	warnings    int
}

// NewDoomLoopDetector returns a detector with the default threshold.
func NewDoomLoopDetector() *DoomLoopDetector { return &DoomLoopDetector{} }

func (d *DoomLoopDetector) threshold() int {
	if d == nil {
		return DefaultDoomLoopThreshold
	}
	if d.Threshold > 0 {
		return d.Threshold
	}
	return DefaultDoomLoopThreshold
}

// parkThreshold is the streak at which ShouldPark flips. It never sits below
// the warning threshold: parking without the advisory first would remove the
// model's one chance to change approach.
func (d *DoomLoopDetector) parkThreshold() int {
	if d == nil {
		return DefaultDoomLoopParkThreshold
	}
	if d.ParkThreshold > 0 && d.ParkThreshold > d.threshold() {
		return d.ParkThreshold
	}
	if DefaultDoomLoopParkThreshold > d.threshold() {
		return DefaultDoomLoopParkThreshold
	}
	return d.threshold() + 1
}

// Observe records one tool call. It returns the reminder body the first time a
// streak reaches the threshold, and "" otherwise: a streak warns once, so a
// mistaken judgment cannot turn into a reminder on every iteration.
func (d *DoomLoopDetector) Observe(name string, input map[string]any) string {
	if d == nil {
		return ""
	}
	fingerprint := ToolCallFingerprint(name, input)
	d.mu.Lock()
	defer d.mu.Unlock()
	if fingerprint == d.fingerprint {
		d.streak++
	} else {
		d.fingerprint = fingerprint
		d.streak = 1
		d.warned = false
	}
	d.toolName = name
	if d.warned || d.streak < d.threshold() {
		return ""
	}
	d.warned = true
	d.warnings++
	return formatDoomLoopReminder(name, d.streak)
}

// Reset clears the streak. The loop calls it at the start of every turn: a new
// user message is a new attempt, not a continuation of the last repetition.
func (d *DoomLoopDetector) Reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fingerprint = ""
	d.streak = 0
	d.warned = false
	d.toolName = ""
}

// Streak reports the current run of identical calls.
func (d *DoomLoopDetector) Streak() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streak
}

// Warnings reports how many reminders this detector has emitted.
func (d *DoomLoopDetector) Warnings() int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.warnings
}

// Active reports whether the current streak is at or past the threshold.
func (d *DoomLoopDetector) Active() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streak >= d.threshold()
}

// ShouldPark reports whether the current streak has reached the park
// threshold. The turn loop checks it after firing the advisory: past this
// point the repetition is no longer a warning, it is a stuck turn.
func (d *DoomLoopDetector) ShouldPark() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streak >= d.parkThreshold()
}

// doomLoopDescription is the one-line identity of a repeated-call streak,
// shared by the escalation context and the park detail.
func doomLoopDescription(tool string, streak int) string {
	return fmt.Sprintf("`%s` called %d times in a row with identical arguments and the same result", tool, streak)
}

// DoomLoopDescription is doomLoopDescription for hosts that publish the
// detection as an event (the colony runtime's node_tool_doom_loop payload), so
// the event, the escalation context and the park detail name the repetition
// with one wording.
func DoomLoopDescription(tool string, streak int) string {
	return doomLoopDescription(tool, streak)
}

// DoomLoopSink publishes the loop's first tool doom-loop detection of a
// turn-loop. The repeating tool, the streak that crossed the warning threshold
// and the upstream-worded description ride the call; the host (the colony
// runtime) stamps them onto the node_tool_doom_loop event. Description is the
// detection's own wording: the per-call streak's one-line identity, the
// turn-level window's batch description, or the replay breaker's detail, so the
// event never re-derives a description that disagrees with the park/escalation
// text for the same detection. A nil sink is a no-op.
type DoomLoopSink func(tool string, streak int, description string)

// DoomLoopParkedError is the turn-loop error that ends a turn parked on a
// repeated identical tool call. It carries the repeating call and the streak
// so the publisher (execution_failed) and the worker failure report name the
// loop instead of a generic "did not complete".
type DoomLoopParkedError struct {
	Tool   string
	Streak int
	// Breaker is true when the replay breaker refused the call before it ran,
	// rather than the post-execution detector observing the streak. The two
	// share ParkReasonDoomLoop (upstream reuses the reason) but must not
	// describe the same execution history.
	Breaker bool
}

func (e *DoomLoopParkedError) Error() string {
	if e == nil {
		return "agent loop: parked on a repeated tool call"
	}
	if e.Breaker {
		return fmt.Sprintf(
			"agent loop: replay breaker tripped — `%s` reached %d consecutive identical call(s) and was NOT executed",
			e.Tool, e.Streak)
	}
	return fmt.Sprintf(
		"agent loop: parked on a doom loop — `%s` ran %d times in a row with identical arguments and the same result",
		e.Tool, e.Streak)
}

// ParkReason names the park this error produced.
func (e *DoomLoopParkedError) ParkReason() ParkReason { return ParkReasonDoomLoop }

func formatDoomLoopReminder(name string, streak int) string {
	return fmt.Sprintf(DoomLoopReminder, name, streak)
}
