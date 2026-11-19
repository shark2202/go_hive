package agentloop

// Doom-loop / stall detection inside the turn loop.
//
// The audit's gap was "repeated tool calls are never noticed": a model that
// re-issues the same call with the same arguments gets the same result forever,
// and nothing in the loop says so. Upstream has no numeric fingerprint rule —
// its stall guard is the judge-based `stall_check` (framework/server/
// stall_check.py::subscribe_stall_check, ported in internal/server/stall_check.go
// and still unsubscribed). This file adds the deterministic half that needs no
// model: N *consecutive* calls with the same (tool name, arguments) fingerprint
// produce one reminder appended to the tool result.
//
// It never interrupts: the loop keeps running, the model keeps its budget, and
// the reminder is advisory text in the tool result it already reads. That is
// deliberate — a repeated call can be legitimate (polling a job, re-reading a
// file that a concurrent writer updates), so the loop warns rather than parks.

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
	encoded, err := json.Marshal(input)
	if err != nil {
		// An argument map that cannot be encoded cannot be compared; fall back
		// to the name alone so a repeated call still counts as repeated.
		return name + "\x00?"
	}
	return name + "\x00" + string(encoded)
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

// DoomLoopParkedError is the turn-loop error that ends a turn parked on a
// repeated identical tool call. It carries the repeating call and the streak
// so the publisher (execution_failed) and the worker failure report name the
// loop instead of a generic "did not complete".
type DoomLoopParkedError struct {
	Tool   string
	Streak int
}

func (e *DoomLoopParkedError) Error() string {
	if e == nil {
		return "agent loop: parked on a repeated tool call"
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
