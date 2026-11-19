package agentloop

import (
	"strings"
	"sync"
	"time"
)

// Parity: framework/agent_loop/reminders.py _FOOTER.
const reminderFooter = "Only act on this if relevant to the current work. NEVER mention this reminder to the user."

// Parity: framework/agent_loop/reminders.py Reminder.
type Reminder struct {
	Body      string
	Source    string
	Energizes bool
	// Parity: Reminder.meta.
	Meta map[string]any
}

type TurnObserver interface {
	ObserveTurn(toolNames []string)
}

type EnergizingSource interface {
	Energizes() bool
}

type TemporalSource interface {
	TickInterval() (float64, bool)
}

type RichReminderSource interface {
	RenderReminder(rctx ReminderContext) (*Reminder, bool)
}

// Parity: framework/agent_loop/reminders.py ReminderHub. Fan-out is
type ReminderHub struct {
	mu      sync.Mutex
	sources []ReminderSource
	active  []ReminderSource

	pendingMu sync.Mutex
	pending   []Reminder

	tickerMu   sync.Mutex
	tickerStop chan struct{}
	tickerDone chan struct{}
}

func NewReminderHub() *ReminderHub { return &ReminderHub{} }

func (h *ReminderHub) Register(source ReminderSource) {
	if source == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sources = append(h.sources, source)
}

func (h *ReminderHub) Bind(agentCtx *ReminderAgentContext) {
	h.mu.Lock()
	defer h.mu.Unlock()
	active := make([]ReminderSource, 0, len(h.sources))
	for _, source := range h.sources {
		if appliesToSafely(source, agentCtx) {
			active = append(active, source)
		}
	}
	h.active = active
}

func appliesToSafely(source ReminderSource, agentCtx *ReminderAgentContext) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = true
		}
	}()
	return source.AppliesTo(agentCtx)
}

func (h *ReminderHub) consultedLocked() []ReminderSource {
	if h.active != nil {
		return h.active
	}
	return h.sources
}

func (h *ReminderHub) ObserveTurn(toolNames []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, source := range h.consultedLocked() {
		if observer, ok := source.(TurnObserver); ok {
			func() {
				defer func() { _ = recover() }()
				observer.ObserveTurn(toolNames)
			}()
		}
	}
}

// Parity: ReminderHub.collect. Sources whose Points does not include point
func (h *ReminderHub) Collect(point ReminderPoint, agentCtx *ReminderAgentContext, toolNames []string, userText string) []Reminder {
	return h.CollectWithSignals(point, agentCtx, nil, toolNames, userText)
}

// Parity: ReminderHub.collect(signals=...).
func (h *ReminderHub) CollectWithSignals(point ReminderPoint, agentCtx *ReminderAgentContext, signals *LoopSignals, toolNames []string, userText string) []Reminder {
	rctx := ReminderContext{
		Point:     point,
		AgentCtx:  agentCtx,
		Signals:   signals,
		ToolNames: toolNames,
		UserText:  userText,
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Reminder, 0)
	for _, source := range h.consultedLocked() {
		if !hasReminderPoint(source.Points(), point) {
			continue
		}
		reminder, ok := renderSourceSafely(source, rctx)
		if !ok || strings.TrimSpace(reminder.Body) == "" {
			continue
		}
		if reminder.Source == "" {
			reminder.Source = source.Name()
		}
		if reminder.Meta == nil {
			reminder.Meta = map[string]any{}
		}
		if _, exists := reminder.Meta["energizes"]; !exists {
			energizes := false
			if energizer, isEnergizer := source.(EnergizingSource); isEnergizer {
				energizes = energizer.Energizes()
			}
			reminder.Meta["energizes"] = energizes
			reminder.Energizes = energizes
		}
		out = append(out, reminder)
	}
	return out
}

func renderSourceSafely(source ReminderSource, rctx ReminderContext) (reminder Reminder, ok bool) {
	defer func() {
		if recover() != nil {
			reminder, ok = Reminder{}, false
		}
	}()
	if rich, isRich := source.(RichReminderSource); isRich {
		r, has := rich.RenderReminder(rctx)
		if !has || r == nil {
			return Reminder{}, false
		}
		return *r, true
	}
	body, has := source.Render(rctx)
	if !has {
		return Reminder{}, false
	}
	return Reminder{Body: body}, true
}

func reminderEnergizes(r Reminder) bool {
	if value, exists := r.Meta["energizes"]; exists {
		wants, _ := value.(bool)
		return wants
	}
	return r.Energizes
}

// Parity: ReminderHub.fire.
func (h *ReminderHub) Fire(point ReminderPoint, agentCtx *ReminderAgentContext, toolNames []string, userText string) string {
	block, _ := h.FireEnergized(point, agentCtx, toolNames, userText)
	return block
}

// Parity: ReminderHub.fire_energized.
func (h *ReminderHub) FireEnergized(point ReminderPoint, agentCtx *ReminderAgentContext, toolNames []string, userText string) (string, bool) {
	items := h.Collect(point, agentCtx, toolNames, userText)
	bodies := make([]string, 0, len(items))
	energized := false
	for _, item := range items {
		bodies = append(bodies, item.Body)
		if reminderEnergizes(item) {
			energized = true
		}
	}
	block := WrapReminder(bodies)
	return block, block != "" && energized
}

// Parity: framework/agent_loop/reminders.py::wrap_reminder.
func WrapReminder(bodies []string) string {
	trimmed := make([]string, 0, len(bodies))
	for _, body := range bodies {
		if stripped := strings.TrimSpace(body); stripped != "" {
			trimmed = append(trimmed, stripped)
		}
	}
	joined := strings.Join(trimmed, "\n\n")
	if joined == "" {
		return ""
	}
	return "<system-reminder>\n" + joined + "\n\n" + reminderFooter + "\n</system-reminder>"
}

// Parity: ReminderHub._min_tick_interval.
func (h *ReminderHub) minTickInterval() (float64, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	found := false
	best := 0.0
	for _, source := range h.consultedLocked() {
		poller, ok := source.(TemporalSource)
		if !ok {
			continue
		}
		interval, polls := poller.TickInterval()
		if !polls || interval <= 0 {
			continue
		}
		if !found || interval < best {
			best = interval
			found = true
		}
	}
	return best, found
}

// Parity: ReminderHub.start.
func (h *ReminderHub) Start(agentCtx *ReminderAgentContext, signalsProvider func() *LoopSignals, wake func()) {
	interval, ok := h.minTickInterval()
	if !ok {
		return
	}
	h.tickerMu.Lock()
	defer h.tickerMu.Unlock()
	if h.tickerStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	h.tickerStop = stop
	h.tickerDone = done
	go h.runTicker(interval, agentCtx, signalsProvider, wake, stop, done)
}

func (h *ReminderHub) stopTicker() {
	h.tickerMu.Lock()
	stop, done := h.tickerStop, h.tickerDone
	h.tickerStop, h.tickerDone = nil, nil
	h.tickerMu.Unlock()
	if stop == nil {
		return
	}
	close(stop)
	<-done
}

// Parity: ReminderHub.stop.
func (h *ReminderHub) Stop() {
	h.stopTicker()
	h.pendingMu.Lock()
	h.pending = nil
	h.pendingMu.Unlock()
}

func (h *ReminderHub) HasTicker() bool {
	h.tickerMu.Lock()
	defer h.tickerMu.Unlock()
	return h.tickerStop != nil
}

// Parity: ReminderHub._run_ticker.
func (h *ReminderHub) runTicker(interval float64, agentCtx *ReminderAgentContext, signalsProvider func() *LoopSignals, wake func(), stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Duration(interval * float64(time.Second)))
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			h.tickOnce(agentCtx, signalsProvider, wake)
		}
	}
}

func (h *ReminderHub) tickOnce(agentCtx *ReminderAgentContext, signalsProvider func() *LoopSignals, wake func()) {
	defer func() { _ = recover() }()
	var signals *LoopSignals
	if signalsProvider != nil {
		signals = signalsProvider()
	}
	items := h.CollectWithSignals(IDLE_TICK, agentCtx, signals, nil, "")
	if len(items) == 0 {
		return
	}
	h.pendingMu.Lock()
	h.pending = append(h.pending, items...)
	h.pendingMu.Unlock()
	if wake != nil {
		wake()
	}
}

// Parity: ReminderHub.post.
func (h *ReminderHub) Post(reminder Reminder) {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	h.pending = append(h.pending, reminder)
}

// Parity: ReminderHub.take_pending.
func (h *ReminderHub) TakePending() []Reminder {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	out := h.pending
	h.pending = nil
	return out
}

func (h *ReminderHub) PendingLen() int {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	return len(h.pending)
}

// PendingReminders returns a copy of the parked queue without draining it.
// Read-only consumers (the REST reminder mirror) use this instead of
// TakePending so the loop still receives the reminders it parked.
func (h *ReminderHub) PendingReminders() []Reminder {
	h.pendingMu.Lock()
	defer h.pendingMu.Unlock()
	if len(h.pending) == 0 {
		return nil
	}
	return append([]Reminder(nil), h.pending...)
}

// RegisteredSourceNames lists every source registered on the hub (bound or
// not), in registration order. Diagnostics and wiring tests use it to prove a
// hub actually carries the task/idle sources it claims to.
func (h *ReminderHub) RegisteredSourceNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.sources))
	for _, source := range h.sources {
		out = append(out, source.Name())
	}
	return out
}

// ActiveSourceNames lists the sources Bind selected for the current agent
// context, in registration order. Before a Bind the registered list is the
// active list (ReminderHub.consultedLocked semantics).
func (h *ReminderHub) ActiveSourceNames() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.consultedLocked()))
	for _, source := range h.consultedLocked() {
		out = append(out, source.Name())
	}
	return out
}
