package server

// Overseer -> chat transcript.
//
// The persistent overseer executes the session's turns on the colony bus.
// Its client_output_delta frames are mirrored onto the session's queen stream
// for the live pane, but a mirror is not a transcript: the chat conversation
// is what a restarted session rebuilds from. This file folds one overseer
// turn's frames into a single assistant message and enqueues it as an
// external record-only turn, so the reply survives a restart without running
// the chat queen a second time.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report
// (a worker/overseer reply is recorded in the session conversation)
// Parity-exempt: Go-side front-end recorder. Upstream keeps the queen's own
// conversation as the session transcript; this port front-ends a separate
// overseer process, so its final text is appended to that transcript here.

import (
	"log"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// overseerTranscriptPrefix marks an assistant message the session did not
// generate itself. The marker is stable so a replayed transcript can tell
// the overseer's reply apart from the chat queen's own turns.
const overseerTranscriptPrefix = "[overseer] "

// maxRecordedOverseerTurns bounds the per-execution dedupe set. A session
// outlives thousands of overseer turns; the map only needs to remember
// recent turns to reject a repeated execution_completed event.
const maxRecordedOverseerTurns = 256

// overseerTranscriptRecorder accumulates the final assistant text of each
// overseer turn. The bridge observes the colony bus, so a turn arrives as
// several client_output_delta frames (one per inner LLM call) followed by
// execution_completed. Only the last non-empty snapshot is kept: earlier
// frames are pre-tool-call text, not the turn's final answer.
type overseerTranscriptRecorder struct {
	mu       sync.Mutex
	pending  map[string]string
	recorded map[string]struct{}
}

// observe folds one colony event into the recorder. It returns the turn's
// final text exactly once, when the turn completes, and nothing for token
// increments or for a repeated completion event.
func (rec *overseerTranscriptRecorder) observe(event *eventbus.AgentEvent) (string, bool) {
	if rec == nil || event == nil || event.StreamID != overseerStreamID {
		return "", false
	}
	executionID := ""
	if event.ExecutionID != nil {
		executionID = strings.TrimSpace(*event.ExecutionID)
	}
	// Without a turn id frames cannot be grouped safely, and recording the
	// same text once per frame would be worse than a missing history row.
	if executionID == "" {
		return "", false
	}
	switch event.Type {
	case eventbus.EventExecutionStarted:
		// Execution ids are only unique within a colony run: a reloaded
		// colony restarts its turn counter, so a new turn can reuse an id
		// from before the reload. A fresh start re-arms the id; a repeated
		// execution_completed (without a new start) stays deduped.
		rec.mu.Lock()
		delete(rec.recorded, executionID)
		delete(rec.pending, executionID)
		rec.mu.Unlock()
	case eventbus.EventClientOutputDelta:
		if event.Data == nil {
			return "", false
		}
		snapshot, _ := event.Data["snapshot"].(string)
		if strings.TrimSpace(snapshot) == "" {
			snapshot, _ = event.Data["content"].(string)
		}
		if strings.TrimSpace(snapshot) == "" {
			return "", false
		}
		rec.mu.Lock()
		if rec.pending == nil {
			rec.pending = map[string]string{}
		}
		rec.pending[executionID] = snapshot
		rec.mu.Unlock()
		return "", false
	case eventbus.EventExecutionCompleted:
		rec.mu.Lock()
		text := rec.pending[executionID]
		delete(rec.pending, executionID)
		if _, done := rec.recorded[executionID]; done || strings.TrimSpace(text) == "" {
			rec.mu.Unlock()
			return "", false
		}
		rec.rememberLocked(executionID)
		rec.mu.Unlock()
		return text, true
	case eventbus.EventToolCallStarted:
		// Text published before a tool call is the iteration's preamble, not
		// the turn's final answer. Drop it so a turn that ends on a tool
		// boundary records nothing instead of a stale preamble.
		rec.mu.Lock()
		delete(rec.pending, executionID)
		rec.mu.Unlock()
	case eventbus.EventExecutionFailed:
		// A failed turn has no final reply; drop the partial frames rather
		// than leaving them to be attributed to the next execution id.
		rec.mu.Lock()
		delete(rec.pending, executionID)
		rec.mu.Unlock()
	}
	return "", false
}

// rememberLocked records the execution as written. Caller holds rec.mu.
func (rec *overseerTranscriptRecorder) rememberLocked(executionID string) {
	if rec.recorded == nil {
		rec.recorded = map[string]struct{}{}
	}
	if len(rec.recorded) >= maxRecordedOverseerTurns {
		// Drop the whole window rather than evicting one id: an id can be
		// re-armed by a new execution_started, and evicting its older twin
		// must not un-record the current turn.
		rec.recorded = map[string]struct{}{}
	}
	rec.recorded[executionID] = struct{}{}
}

// observeOverseerTranscriptEvent runs on the colony bridge subscription. It
// enqueues a record-only assistant turn on the runtime's queue: the
// conversation keeps its single writer and no model turn is run.
func (r *ChatRuntime) observeOverseerTranscriptEvent(event *eventbus.AgentEvent) {
	if r == nil {
		return
	}
	text, ok := r.overseerTranscript.observe(event)
	if !ok {
		return
	}
	_ = r.enqueueTurn(chatTurnRequest{
		message:           text,
		recordOnly:        true,
		external:          true,
		externalAssistant: true,
	})
}

// recordExternalAssistant appends the overseer's finished reply as an
// assistant message. It runs on the runtime's queue goroutine (the
// conversation's only writer), so it never races a running turn.
func (r *ChatRuntime) recordExternalAssistant(message string) {
	if r == nil || r.conv == nil || strings.TrimSpace(message) == "" {
		return
	}
	if _, err := r.conv.AddAssistantMessage(overseerTranscriptPrefix+message, nil, agentloop.AssistantMessageOptions{}); err != nil {
		log.Printf("chat runtime %s: overseer reply not recorded: %v", r.sessionID, err)
	}
}
