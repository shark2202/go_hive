package server

// Reminder-hub wiring for the session runtime.
//
// Upstream the agent loop owns a framework ReminderHub, registers a
// TaskReminderSource with it, and fires that source at five lifecycle points
// (session_start / user_prompt_submit / post_tool_use / post_compact / stop).
// The source decides whether a snapshot of the task list or a drift nudge is
// due; the hub wraps the bodies into one <system-reminder> block and the loop
// places that block in the conversation.
//
// This file gives the Go session runtime the same per-session hub + task
// source, plus a small REST read/dismiss surface so a session that is parked
// (turn complete == idle) can surface the reminders it produced. All policy
// (thresholds, cooldown, message text, persisted counters) stays in
// internal/tasks/reminders.go — the port of framework/tasks/reminders.py.
//
// Parity: framework/agent_loop/reminders.py::ReminderHub
// Parity: framework/tasks/reminders.py::TaskReminderSource

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// liveReminderHubs is the read-only mirror of the per-session loop hubs.
// ChatRuntime registers its hub here at construction and removes it on Close,
// so the REST surface reports the same queue the agent loop drains instead of
// the retired standalone ReminderRuntime state.
var liveReminderHubs sync.Map // sessionID -> *agentloop.ReminderHub

// registerSessionReminderHub publishes a session's live loop hub to the REST
// mirror. The returned function removes exactly this hub (a newer runtime for
// the same session id wins).
func registerSessionReminderHub(sessionID string, hub *agentloop.ReminderHub) func() {
	if sessionID == "" || hub == nil {
		return func() {}
	}
	liveReminderHubs.Store(sessionID, hub)
	return func() {
		liveReminderHubs.CompareAndDelete(sessionID, hub)
	}
}

func liveSessionReminderHub(sessionID string) *agentloop.ReminderHub {
	if sessionID == "" {
		return nil
	}
	value, ok := liveReminderHubs.Load(sessionID)
	if !ok {
		return nil
	}
	hub, _ := value.(*agentloop.ReminderHub)
	return hub
}

// reminderViewsFromHub converts the loop hub's pending queue into API records.
// IDs are positional: the mirror is read-only, so a stable id only needs to
// distinguish entries within one response.
func reminderViewsFromHub(items []agentloop.Reminder) []ReminderView {
	out := make([]ReminderView, 0, len(items))
	now := time.Now().Unix()
	for index, item := range items {
		out = append(out, ReminderView{
			ID:        int64(index + 1),
			Source:    item.Source,
			Body:      item.Body,
			CreatedAt: now,
			Meta:      item.Meta,
		})
	}
	return out
}

// ReminderView is one queued reminder as the HTTP surface exposes it. Body is
// the bare source body (parity with Reminder.body); the hub wrapper, when one
// was requested, rides the response's "block" field instead.
type ReminderView struct {
	ID        int64          `json:"id"`
	Source    string         `json:"source"`
	Point     string         `json:"point"`
	Body      string         `json:"body"`
	CreatedAt int64          `json:"created_at"`
	Meta      map[string]any `json:"meta,omitempty"`
}

// ReminderRouteDeps scopes the reminder route table to one runtime. A nil
// Runtime builds a private one; integrators that also drive TurnComplete
// should construct the runtime themselves and pass it here.
type ReminderRouteDeps struct {
	Runtime *ReminderRuntime
}

// ReminderRuntime keeps one ReminderHub + TaskReminderSource per session.
// Sources are stateful (turn counters, snapshot fingerprints) and persist
// their counter to reminder_state.json beside the session's task list; the
// pending queue is in memory only, exactly like ReminderHub._pending.
type ReminderRuntime struct {
	mu       sync.Mutex
	sessions map[string]*sessionReminders
}

type sessionReminders struct {
	mu      sync.Mutex
	hub     *agentloop.ReminderHub
	pending []ReminderView
	nextID  int64
}

// NewReminderRuntime returns a runtime with no bound sessions.
func NewReminderRuntime() *ReminderRuntime {
	return &ReminderRuntime{sessions: map[string]*sessionReminders{}}
}

// session returns (creating on first use) the hub/source pair for sessionID.
func (r *ReminderRuntime) session(sessionID string) *sessionReminders {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions == nil {
		r.sessions = map[string]*sessionReminders{}
	}
	state := r.sessions[sessionID]
	if state != nil {
		return state
	}
	adapter := &taskReminderSourceAdapter{inner: tasks.NewTaskReminderSource()}
	hub := agentloop.NewReminderHub()
	hub.Register(adapter)
	// Bind once, like AgentLoop.bind: the task source applies to any session
	// that has task write tools available.
	hub.Bind(reminderAgentContext(sessionID))
	state = &sessionReminders{hub: hub}
	r.sessions[sessionID] = state
	return state
}

// lookup returns the session's state without creating one, so read-only
// routes cannot grow the map for ids the runtime never observed.
func (r *ReminderRuntime) lookup(sessionID string) *sessionReminders {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID]
}

// reminderAgentContext is the per-session view the hub's sources inspect.
// The session runtime always exposes the task tools, which is what makes
// TaskReminderSource.AppliesTo true.
func reminderAgentContext(sessionID string) *agentloop.ReminderAgentContext {
	tools := make([]agentloop.Tool, 0, len(tasks.AllTaskTools))
	for _, name := range tasks.AllTaskTools {
		tools = append(tools, agentloop.Tool{Name: name})
	}
	return &agentloop.ReminderAgentContext{
		SessionID:      sessionID,
		AvailableTools: tools,
	}
}

// TurnComplete is the session-runtime hook: call it once when an outer turn
// finishes and the loop parks (the session's idle boundary), passing the tool
// names the turn executed (empty for a text-only turn).
//
// It mirrors what the upstream loop does across that turn: observe the turn
// (advances the drift/snapshot counters), then fire the lifecycle points that
// can produce a task reminder — post_tool_use when the turn ran tools (rides
// the tool-result tail) and stop when the agent yields. Reminders the source
// decides are due are queued for the REST surface and returned.
func (r *ReminderRuntime) TurnComplete(sessionID string, toolNames []string) []ReminderView {
	if sessionID == "" {
		return nil
	}
	state := r.session(sessionID)
	state.mu.Lock()
	defer state.mu.Unlock()

	state.hub.ObserveTurn(toolNames)
	agentCtx := reminderAgentContext(sessionID)

	produced := make([]ReminderView, 0, 2)
	if len(toolNames) > 0 {
		produced = append(produced, state.views(agentloop.POST_TOOL_USE, state.hub.Collect(agentloop.POST_TOOL_USE, agentCtx, toolNames, ""))...)
	}
	produced = append(produced, state.views(tasks.ReminderStopPoint, state.hub.Collect(tasks.ReminderStopPoint, agentCtx, nil, ""))...)
	state.pending = append(state.pending, produced...)
	return produced
}

// views converts hub output into API records, assigning stable ids.
func (s *sessionReminders) views(point agentloop.ReminderPoint, items []agentloop.Reminder) []ReminderView {
	out := make([]ReminderView, 0, len(items))
	now := time.Now().Unix()
	for _, item := range items {
		s.nextID++
		out = append(out, ReminderView{
			ID:        s.nextID,
			Source:    item.Source,
			Point:     string(point),
			Body:      item.Body,
			CreatedAt: now,
			Meta:      item.Meta,
		})
	}
	return out
}

// Take drains the pending queue — parity with ReminderHub.take_pending: the
// caller owns the reminders after reading them, so a second read is empty.
func (r *ReminderRuntime) Take(sessionID string) []ReminderView {
	state := r.lookup(sessionID)
	if state == nil {
		return []ReminderView{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	out := state.pending
	state.pending = nil
	if out == nil {
		return []ReminderView{}
	}
	return out
}

// TakeBlock drains the pending queue and returns the bodies wrapped as one
// <system-reminder> block, ready to inject into a conversation. Empty when
// nothing is due. This is the in-process equivalent of reading the REST
// surface (either consumer drains the same queue).
func (r *ReminderRuntime) TakeBlock(sessionID string) string {
	return remindersBlock(r.Take(sessionID))
}

// Peek returns the pending queue without consuming it (ReminderHub.pending).
func (r *ReminderRuntime) Peek(sessionID string) []ReminderView {
	state := r.lookup(sessionID)
	if state == nil {
		return []ReminderView{}
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.pending) == 0 {
		return []ReminderView{}
	}
	return append([]ReminderView(nil), state.pending...)
}

// Dismiss clears the pending queue without returning bodies — parity with
// ReminderHub.stop dropping undrained reminders.
func (r *ReminderRuntime) Dismiss(sessionID string) int {
	state := r.lookup(sessionID)
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	cleared := len(state.pending)
	state.pending = nil
	return cleared
}

// PendingLen reports the queue depth without consuming it.
func (r *ReminderRuntime) PendingLen(sessionID string) int {
	state := r.lookup(sessionID)
	if state == nil {
		return 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.pending)
}

// DropSession forgets a session's in-memory hub and queue. The persisted
// reminder_state.json is untouched (StopSession/restart keeps the counters,
// same as upstream).
func (r *ReminderRuntime) DropSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, sessionID)
}

// taskReminderSourceAdapter bridges tasks.TaskReminderSource — whose Render
// takes tasks.TaskReminderContext — onto agentloop.ReminderSource.
//
// Parity: framework/tasks/reminders.py::TaskReminderSource
type taskReminderSourceAdapter struct {
	inner *tasks.TaskReminderSource
}

func (a *taskReminderSourceAdapter) Name() string { return a.inner.Name() }

func (a *taskReminderSourceAdapter) Points() []agentloop.ReminderPoint {
	return a.inner.Points()
}

func (a *taskReminderSourceAdapter) AppliesTo(agentCtx *agentloop.ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	return a.inner.AppliesTo(&tasks.TaskReminderAgentContext{
		SessionID:      agentCtx.SessionID,
		AvailableTools: agentCtx.AvailableTools,
	})
}

func (a *taskReminderSourceAdapter) ObserveTurn(toolNames []string) {
	a.inner.ObserveTurn(toolNames)
}

func (a *taskReminderSourceAdapter) Render(rctx agentloop.ReminderContext) (string, bool) {
	if rctx.AgentCtx == nil {
		return "", false
	}
	return a.inner.Render(tasks.TaskReminderContext{
		Point: rctx.Point,
		AgentCtx: &tasks.TaskReminderAgentContext{
			SessionID:      rctx.AgentCtx.SessionID,
			AvailableTools: rctx.AgentCtx.AvailableTools,
		},
		ToolNames: rctx.ToolNames,
	})
}

// remindersBlock renders the hub's <system-reminder> wrapper for the given
// bodies, so a caller can inject the block into a conversation verbatim.
func remindersBlock(items []ReminderView) string {
	if len(items) == 0 {
		return ""
	}
	bodies := make([]string, 0, len(items))
	for _, item := range items {
		bodies = append(bodies, item.Body)
	}
	return agentloop.WrapReminder(bodies)
}

// RegisterReminderRoutes wires the per-session reminder surface:
//
//	GET    /api/sessions/{session_id}/reminders  -- read (and by default drain) due reminders
//	DELETE /api/sessions/{session_id}/reminders  -- dismiss every pending reminder
//
// GET drains by default, matching ReminderHub.take_pending; pass ?drain=0 to
// peek without consuming. The response carries both the structured list and
// the ready-to-inject <system-reminder> block.
//
// Parity-exempt: framework/server has no reminder REST route — upstream only
// injects reminders as conversation blocks. This is a Go-side read/dismiss
// surface over the same hub and source, not a new reminder policy.
func RegisterReminderRoutes(mux *http.ServeMux, deps ReminderRouteDeps) {
	runtime := deps.Runtime
	if runtime == nil {
		runtime = NewReminderRuntime()
	}
	mux.HandleFunc("GET /api/sessions/{session_id}/reminders", func(w http.ResponseWriter, r *http.Request) {
		handleGetSessionReminders(runtime, w, r)
	})
	mux.HandleFunc("DELETE /api/sessions/{session_id}/reminders", func(w http.ResponseWriter, r *http.Request) {
		handleDismissSessionReminders(runtime, w, r)
	})
}

func handleGetSessionReminders(runtime *ReminderRuntime, w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}
	// A live chat session owns the authoritative queue: mirror its loop hub
	// without draining, so the same reminders still reach the model on the
	// next turn. This path ignores ?drain because a read-only mirror must not
	// steal the loop's pending reminders.
	if hub := liveSessionReminderHub(sessionID); hub != nil {
		items := reminderViewsFromHub(hub.PendingReminders())
		sources := hub.ActiveSourceNames()
		body := map[string]any{
			"session_id": sessionID,
			"reminders":  items,
			"pending":    len(items),
			"active":     len(sources) > 0,
			"sources":    sources,
			"mirror":     true,
		}
		if block := remindersBlock(items); block != "" {
			body["block"] = block
		}
		writeJSON(w, http.StatusOK, body)
		return
	}
	drain := true
	switch strings.ToLower(r.URL.Query().Get("drain")) {
	case "0", "false", "no":
		drain = false
	}
	items := runtime.Peek(sessionID)
	if drain {
		items = runtime.Take(sessionID)
	}
	if items == nil {
		items = []ReminderView{}
	}
	body := map[string]any{
		"session_id": sessionID,
		"reminders":  items,
		"pending":    len(items),
		"active":     false,
	}
	if block := remindersBlock(items); block != "" {
		body["block"] = block
	}
	writeJSON(w, http.StatusOK, body)
}

func handleDismissSessionReminders(runtime *ReminderRuntime, w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id required"})
		return
	}
	cleared := 0
	if hub := liveSessionReminderHub(sessionID); hub != nil {
		cleared = len(hub.TakePending())
	} else {
		cleared = runtime.Dismiss(sessionID)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"cleared":    cleared,
	})
}
