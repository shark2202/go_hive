package colony

// Worker-side reminder wiring.
//
// Workers run the same AgentLoop as the queen, but until now they were built
// as a bare &AgentLoop{} with a nil Hub: no task reminder, no idle nudge, no
// ticker. A worker is short-lived, yet its single ExecuteTurn can run for
// minutes (long tool calls, slow model responses), which is exactly the span
// the idle source is meant to cover. This file gives every worker loop the
// same hub shape the chat session uses and stops the ticker with the run.
//
// Parity: framework/agent_loop/agent_loop.py::AgentLoop.__init__ (the loop owns
// a ReminderHub; the session runtime registers sources on it)

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/tasks"
)

// workerReminderClock is the worker's activity clock: the idle source reads it
// to decide whether the worker has gone quiet. Every tool execution marks
// progress, so a worker actively using tools is never nudged.
type workerReminderClock struct {
	lastProgress atomic.Int64
}

func newWorkerReminderClock() *workerReminderClock {
	clock := &workerReminderClock{}
	clock.mark()
	return clock
}

func (c *workerReminderClock) mark() {
	c.lastProgress.Store(time.Now().UnixNano())
}

func (c *workerReminderClock) idleSeconds() float64 {
	last := c.lastProgress.Load()
	if last == 0 {
		return 0
	}
	return time.Since(time.Unix(0, last)).Seconds()
}

// workerTaskReminderSource bridges tasks.TaskReminderSource (whose Render takes
// tasks.TaskReminderContext) onto agentloop.ReminderSource. It is the worker
// twin of the chat runtime's adapter; both wrap the one ported policy.
//
// Parity: framework/tasks/reminders.py::TaskReminderSource
type workerTaskReminderSource struct {
	inner *tasks.TaskReminderSource
}

func (s *workerTaskReminderSource) Name() string { return s.inner.Name() }

func (s *workerTaskReminderSource) Points() []agentloop.ReminderPoint {
	return s.inner.Points()
}

func (s *workerTaskReminderSource) AppliesTo(agentCtx *agentloop.ReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	return s.inner.AppliesTo(&tasks.TaskReminderAgentContext{
		SessionID:      agentCtx.SessionID,
		AvailableTools: agentCtx.AvailableTools,
	})
}

func (s *workerTaskReminderSource) ObserveTurn(toolNames []string) {
	s.inner.ObserveTurn(toolNames)
}

func (s *workerTaskReminderSource) Render(rctx agentloop.ReminderContext) (string, bool) {
	if rctx.AgentCtx == nil {
		return "", false
	}
	return s.inner.Render(tasks.TaskReminderContext{
		Point: rctx.Point,
		AgentCtx: &tasks.TaskReminderAgentContext{
			SessionID:      rctx.AgentCtx.SessionID,
			AvailableTools: rctx.AgentCtx.AvailableTools,
		},
		ToolNames: rctx.ToolNames,
	})
}

func reminderToolsFromDefs(defs []llm.ToolDef) []agentloop.Tool {
	out := make([]agentloop.Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, agentloop.Tool{Name: def.Name, Description: def.Description})
	}
	return out
}

// newWorkerAgentLoop builds a worker loop whose Hub carries the task reminder
// and the idle nudge. The idle ticker is started here and stopped by the
// returned cleanup; a parked reminder is drained back into the loop's input
// queue as a <system-reminder> block so it is not lost when the worker's
// ExecuteTurn is still in flight.
func (r *ColonyRuntime) newWorkerAgentLoop(worker *Worker, cfg agentloop.LoopConfig, tools []llm.ToolDef) (*agentloop.AgentLoop, func()) {
	clock := newWorkerReminderClock()
	baseExec := r.executorFor(worker)
	executor := func(req agentloop.ToolCallRequest) (agentloop.ToolResult, error) {
		clock.mark()
		return baseExec(req)
	}
	loop := agentloop.NewAgentLoop(cfg, executor)
	if loop.Hub == nil {
		return loop, func() {}
	}

	hub := loop.Hub
	hub.Register(&workerTaskReminderSource{inner: tasks.NewTaskReminderSource()})
	idle := agentloop.NewIdleNudgeSource(agentloop.IdleNudgeConfig{
		BudgetSeconds:         cfg.SessionIdleNudgeSeconds,
		MaxNudges:             cfg.SessionIdleNudgeMaxPerSession,
		AwaitingBudgetSeconds: cfg.SessionIdleNudgeAwaitingSeconds,
		BrokenBudgetSeconds:   cfg.SessionIdleNudgeBrokenSeconds,
	})
	hub.Register(idle)

	reminderCtx := &agentloop.ReminderAgentContext{
		SessionID:      fmt.Sprintf("%s/%s", r.streamID, worker.ID),
		ColonyID:       r.streamID,
		AvailableTools: reminderToolsFromDefs(tools),
	}
	hub.Bind(reminderCtx)
	hub.Start(reminderCtx, func() *agentloop.LoopSignals {
		return &agentloop.LoopSignals{
			IdleSeconds:    clock.idleSeconds(),
			StreamActive:   false,
			FirstEventSeen: true,
		}
	}, func() {
		items := hub.TakePending()
		if len(items) == 0 {
			return
		}
		bodies := make([]string, 0, len(items))
		for _, item := range items {
			if body := strings.TrimSpace(item.Body); body != "" {
				bodies = append(bodies, body)
			}
		}
		block := agentloop.WrapReminder(bodies)
		if block != "" {
			loop.InjectEvent(block, false, nil)
		}
	})
	return loop, func() { hub.Stop() }
}
