package agentloop

// Continue-nudge recovery for a stalled LLM stream.
//
// Upstream's _run_turn_loop runs a stream watchdog; when it trips (TTFT or
// inter-event silence) the loop cancels the stream, consults the
// STREAM_STALLED reminder sources synchronously, injects their nudge into the
// conversation and re-streams the same inner turn. The Go transport differs —
// the watchdog lives in the streaming completer (internal/server/chat_stream.go)
// and reports the trip as a typed *llm.StreamStalledError — but the loop-side
// recovery is the same: collect, inject, retry; the source owns the body and
// the per-turn cap.
//
// Parity: framework/agent_loop/agent_loop.py::_run_turn_loop
// (the `_watchdog_verdict != "ok"` recovery block)
// Parity: framework/agent_loop/stream_stall.py::StreamStallSource.

import (
	"errors"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

// StreamStallNudge reports one continue-nudge the loop injected after a
// watchdog trip: the verdict, the budgets it was judged against, and which
// nudge of the turn it is. Hosts use it to publish the stream-level diagnostic
// events (upstream: stream_ttft_exceeded / stream_inactive / stream_nudge_sent,
// agent_loop.py::_run_turn_loop).
type StreamStallNudge struct {
	Reason         string
	ElapsedSeconds float64
	LimitSeconds   float64
	NudgeCount     int
}

// injectStreamStallNudge handles a failed completion call that carries a
// stream-stall verdict. It returns true when a reminder was collected and
// appended to the conversation (the caller should retry the same inner turn);
// false leaves the error to the caller's normal failure path.
func (l *AgentLoop) injectStreamStallNudge(err error, reminderCtx *ReminderAgentContext, conv *NodeConversation) bool {
	var stalled *llm.StreamStalledError
	if !errors.As(err, &stalled) || l.Hub == nil || conv == nil {
		return false
	}
	items := l.Hub.CollectWithSignals(
		STREAM_STALLED,
		reminderCtx,
		&LoopSignals{StallReason: stalled.Reason, StallElapsed: stalled.ElapsedSeconds},
		nil,
		"",
	)
	if len(items) == 0 {
		return false
	}
	body := strings.TrimSpace(items[0].Body)
	if body == "" {
		return false
	}
	if _, addErr := conv.AddUserMessage(WrapReminder([]string{body}), UserMessageOptions{IsSystemReminder: true}); addErr != nil {
		return false
	}
	l.bumpBy("stream_stall_nudged", 1)
	if l.OnStreamStallNudge != nil {
		count, _ := items[0].Meta["nudge_count"].(int)
		l.OnStreamStallNudge(StreamStallNudge{
			Reason:         stalled.Reason,
			ElapsedSeconds: stalled.ElapsedSeconds,
			LimitSeconds:   stalled.LimitSeconds,
			NudgeCount:     count,
		})
	}
	return true
}
