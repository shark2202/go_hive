package server

// Sentinel wiring for the chat runtime.
//
// The engine itself (`internal/sentinel`) is a faithful port; this file is the
// production assembly the audit found missing:
//
//   - `NewSentinelManager` builds the process-global manager over the app's
//     Hive home and the live session table, so an escalation record and its
//     inbound reply both resolve against real sessions;
//   - every colony-queen chat runtime registers an `EscalationSource` on its
//     reminder hub, so a parked queen is classified (continue → nudge,
//     needs_human → escalate, done → completion report) instead of sitting
//     silent;
//   - a real user message closes any open escalation for the session.
//
// Parity: framework/sentinel/manager.py::get_sentinel_manager
// Parity: framework/agent_loop/agent_loop.py::_build_sentinel_park_context
// Parity: framework/agent_loop/agent_loop.py::_notify_sentinel_local_resume

import (
	"context"
	"errors"
	"strings"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/sentinel"
)

// ErrSentinelColdRestoreUnsupported is returned when a reply targets a session
// that is no longer live. Rebuilding a chat session needs the loader (provider,
// tool catalogue, colony host), which the server package cannot construct, so
// the manager logs the failed resume instead of pretending it delivered.
var ErrSentinelColdRestoreUnsupported = errors.New("server: sentinel cannot cold-restore a session")

// NewSentinelManager builds and installs the process-global Sentinel manager.
// Callers own its lifecycle (Start/Stop); installing it is what makes the
// chat runtimes register the escalation source and what the inbound listeners
// deliver replies through.
func NewSentinelManager(homeRoot string, sessions *SessionManager) *sentinel.Manager {
	manager := sentinel.NewManager(sentinel.ManagerOptions{
		Store:    sentinel.NewStore(homeRoot),
		Sessions: sentinelSessionManager{manager: sessions},
	})
	sentinel.SetSentinelManager(manager)
	return manager
}

// sentinelSessionManager projects the live session table onto Sentinel's
// session seam.
type sentinelSessionManager struct {
	manager *SessionManager
}

func (m sentinelSessionManager) GetLiveSession(sessionID string) sentinel.LiveSession {
	if m.manager == nil {
		return nil
	}
	session := m.manager.GetSession(sessionID)
	if session == nil {
		return nil
	}
	return sentinelLiveSession{session: session}
}

func (m sentinelSessionManager) CreateSession(_ context.Context, _, _ string) (sentinel.LiveSession, error) {
	return nil, ErrSentinelColdRestoreUnsupported
}

// sentinelLiveSession is the read surface Sentinel uses to decide whether a
// desktop client is attached and how to resume a parked queen.
type sentinelLiveSession struct {
	session *Session
}

func (s sentinelLiveSession) SSEClientCount() int {
	if s.session == nil {
		return 0
	}
	return s.session.SSEClientCount
}

func (s sentinelLiveSession) QueenNode() sentinel.QueenNode {
	if s.session == nil || s.session.Chat == nil {
		return nil
	}
	return sentinelQueenNode{chat: s.session.Chat}
}

// sentinelQueenNode adapts the chat runtime's InjectEvent (the server's
// QueenNode shape) to Sentinel's context-carrying seam.
type sentinelQueenNode struct {
	chat *ChatRuntime
}

func (n sentinelQueenNode) InjectEvent(_ context.Context, content string, isClientInput bool) error {
	return n.chat.InjectEvent(content, isClientInput)
}

// configureSentinel arms Sentinel for this session: the autopilot gate the
// idle-nudge source reads, and the escalation source itself. It must run before
// the hub binds its agent context, because binding freezes the consulted
// source list; a source registered later would never be asked.
//
// Parity: framework/agent_loop/agent_loop.py (EscalationSource registration)
func (r *ChatRuntime) configureSentinel() {
	if r == nil {
		return
	}
	manager := sentinel.GetSentinelManager()
	if manager == nil {
		return
	}
	if r.agentCtx != nil {
		r.agentCtx.SentinelAutopilotProvider = r.sentinelAutopilotEnabled
	}
	hub := r.loopHub()
	if hub == nil {
		return
	}
	r.sentinelSource = newChatSentinelSource(r, manager)
	if r.sentinelSource == nil {
		return
	}
	hub.Register(r.sentinelSource)
}

// loopHub returns the hub to register on: the live runtime's once configured,
// otherwise the loop's hub (during construction).
func (r *ChatRuntime) loopHub() *agentloop.ReminderHub {
	if r == nil {
		return nil
	}
	if r.hub != nil {
		return r.hub
	}
	if r.loop != nil {
		return r.loop.Hub
	}
	return nil
}

// sentinelAutopilotEnabled reports whether the bound colony opted into
// Sentinel. A plain queen (no colony) never opts in.
func (r *ChatRuntime) sentinelAutopilotEnabled() (enabled bool) {
	manager := sentinel.GetSentinelManager()
	if manager == nil {
		return false
	}
	runtime := r.boundColonyRuntime()
	if runtime == nil || runtime.StreamID() == "" {
		return false
	}
	defer func() {
		if recover() != nil {
			enabled = false
		}
	}()
	return manager.Store.LoadNotificationsConfig(runtime.StreamID()).SentinelEnabled
}

// newChatSentinelSource binds the engine to this session: its park context,
// the manager's delivery queue, and the queen's own model for classification.
func newChatSentinelSource(r *ChatRuntime, manager *sentinel.Manager) *sentinel.EscalationSource {
	if r == nil || manager == nil {
		return nil
	}
	return sentinel.NewEscalationSource(sentinel.EscalationSourceOptions{
		Store:               manager.Store,
		Tokens:              manager.Tokens,
		ParkContextProvider: r.sentinelParkContext,
		OnEscalate:          manager.EnqueueEscalation,
		HasAttachedUI:       manager.HasAttachedUI,
		LLMProvider: func(agentCtx *agentloop.ReminderAgentContext) llm.Completer {
			if agentCtx == nil || agentCtx.SessionID != r.sessionID {
				return nil
			}
			return r.deps.LLM
		},
	})
}

// sentinelParkContext assembles what the classifier and the escalation message
// need: the goal, open tasks, the parked question, the queen's last message,
// the last real user steer, blocker-shaped tool errors, and any workers still
// in flight. Best-effort throughout — a missing store reads as empty.
//
// Parity: framework/agent_loop/agent_loop.py::_build_sentinel_park_context
func (r *ChatRuntime) sentinelParkContext() (sentinel.ParkContext, error) {
	park := sentinel.ParkContext{ParkReason: string(agentloop.ParkReasonUnknown)}
	if r == nil {
		return park, nil
	}
	r.parkMu.Lock()
	reason := r.parkReason
	r.parkMu.Unlock()
	if reason != "" {
		park.ParkReason = string(reason)
	}

	if goal := strings.TrimSpace(r.sessionGoal()); goal != "" {
		park.Goal = &goal
	}
	for _, task := range r.openIdleTasks() {
		status := strings.ToLower(task.Status)
		if status == "completed" || status == "archived" {
			continue
		}
		if subject := strings.TrimSpace(task.Subject); subject != "" {
			park.OpenTasks = append(park.OpenTasks, subject)
		}
	}

	if r.conv != nil {
		messages := r.conv.Messages()
		park.LastAssistantText, park.RecentUserText = pickParkTail(messages)
		park.RecentErrors = scanRecentToolErrors(messages)
	}

	r.mu.Lock()
	if len(r.pendingQuestions) > 0 {
		park.PendingQuestions = make([]map[string]any, 0, len(r.pendingQuestions))
		for _, question := range r.pendingQuestions {
			copied := make(map[string]any, len(question))
			for key, value := range question {
				copied[key] = value
			}
			park.PendingQuestions = append(park.PendingQuestions, copied)
		}
	}
	r.mu.Unlock()

	for _, worker := range r.activeWorkers() {
		park.RunningWorkers = append(park.RunningWorkers, map[string]any{
			"worker_id": worker.ID,
			"status":    worker.Status,
			"task":      worker.Task,
		})
	}
	return park, nil
}

// pickParkTail returns the most recent assistant message and the most recent
// real human message. Only is_client_input user messages count as the human's
// own words: reminders, trigger batches and worker reports are framework
// injections and must never masquerade as a steer.
//
// Parity: framework/agent_loop/agent_loop.py::_pick_park_tail
func pickParkTail(messages []agentloop.Message) (lastAssistant, lastUser string) {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if lastAssistant == "" && message.Role == agentloop.RoleAssistant {
			lastAssistant = message.Content
		} else if lastUser == "" && message.Role == agentloop.RoleUser && message.IsClientInput {
			lastUser = message.Content
		}
		if lastAssistant != "" && lastUser != "" {
			break
		}
	}
	return lastAssistant, lastUser
}

// errorMarkers are the blocker-shaped phrases the classifier treats as
// evidence (auth/credential/crash signals), not as a hard escalate trigger.
var errorMarkers = []string{
	"log in",
	"login",
	"sign in",
	"authenticat",
	"unauthor",
	"credential",
	"expired",
	"session expired",
	"not logged in",
	"captcha",
	"permission denied",
	"403",
	"401",
	"crashed",
	"disconnected",
	"timed out",
}

// scanRecentToolErrors returns short snippets of the last few tool results that
// look like blockers.
//
// Parity: framework/agent_loop/agent_loop.py::_scan_recent_tool_errors
func scanRecentToolErrors(messages []agentloop.Message) []string {
	toolMessages := make([]agentloop.Message, 0, 8)
	for index := len(messages) - 1; index >= 0 && len(toolMessages) < 8; index-- {
		if messages[index].Role == agentloop.RoleTool {
			toolMessages = append(toolMessages, messages[index])
		}
	}
	out := make([]string, 0, len(toolMessages))
	for _, message := range toolMessages {
		content := message.Content
		lowered := strings.ToLower(content)
		for _, marker := range errorMarkers {
			if strings.Contains(lowered, marker) {
				if len(content) > 200 {
					content = content[:200]
				}
				out = append(out, strings.TrimSpace(content))
				break
			}
		}
	}
	return out
}

// notifySentinelLocalResume closes any open escalation for this session: the
// user just answered in-app, so a pending message would be stale.
//
// Parity: framework/agent_loop/agent_loop.py::_notify_sentinel_local_resume
func (r *ChatRuntime) notifySentinelLocalResume() {
	if r == nil || r.sessionID == "" {
		return
	}
	manager := sentinel.GetSentinelManager()
	if manager == nil {
		return
	}
	manager.OnLocalResume(r.ctx, r.sessionID)
}
