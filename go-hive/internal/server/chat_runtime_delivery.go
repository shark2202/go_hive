package server

// Message intake: the queueing API a session's callers use to hand the queen
// a turn (Deliver / DeliverClientInput* / InjectQueenMail / InjectWorkerReport
// / InjectEvent / InjectTrigger), the queue itself, and the two read accessors
// LastError / Transcript. Split out of chat_runtime.go to stay under the
// file-size budget (internal/quality TestFileSizeBudget); behavior is
// unchanged.

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
)

// Deliver queues a user message and wakes the worker. It never blocks on a
// running turn: turns are serialized by the queue, not by the caller. On a
// colony-bound session with a live overseer (only when a test explicitly
// starts one) the message is relayed to it instead: the overseer is the
// session's executor and the chat queen its front end, so exactly one loop
// acts on the user's request. With production loading (overseer dormant) the
// chat queen executes the turn itself.
func (r *ChatRuntime) Deliver(message string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if r.relayClientInputToOverseer(message) {
		// The overseer owns the turn; the session transcript still records the
		// ask. The record rides the queue so the conversation keeps a single
		// writer, and a closed runtime only loses the record, not the relay.
		recorded := message
		if block := r.takeRelayCatalogBlock(); block != "" {
			recorded = block + "\n\n" + message
		}
		_ = r.enqueueTurn(chatTurnRequest{message: recorded, clientInput: true, recordOnly: true})
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: message, clientInput: true})
}

// DeliverClientInput is DeliverClientInputWithImages without attachments; it
// stays the seam every text-only caller (queen mail, sentinel resume, trigger
// runtimes) already uses.
func (r *ChatRuntime) DeliverClientInput(message, correlationID string) error {
	return r.DeliverClientInputWithImages(message, nil, correlationID)
}

// DeliverClientInputWithImages is Deliver for the HTTP chat route: the
// correlation id the handler published on CLIENT_INPUT_RECEIVED rides the
// turn so the committed boundary can carry the same id, as upstream's
// deliver_to_queen → _drain_injection_queue pair does. The images are the
// prepared user content blocks; they join the transcript turn so the model
// request carries them.
//
// Parity: framework/agent_loop/agent_loop.py::inject_event (image_content)
func (r *ChatRuntime) DeliverClientInputWithImages(message string, images []map[string]any, correlationID string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if r.relayClientInputToOverseer(message) {
		recorded := message
		if block := r.takeRelayCatalogBlock(); block != "" {
			recorded = block + "\n\n" + message
		}
		_ = r.enqueueTurn(chatTurnRequest{message: recorded, clientInput: true, recordOnly: true, correlationID: correlationID, images: images})
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: message, clientInput: true, correlationID: correlationID, images: images})
}

// InjectQueenMail queues a letter or reply from another queen: it enters as a
// client message (the way a chat app's message does, so she credits the
// writer) but is marked as queen mail, so the chain depth continues instead of
// starting over.
//
// Parity: framework/server/queen_mail.py::QueenMail._send_to_hive_queen
func (r *ChatRuntime) InjectQueenMail(content string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: content, clientInput: true, fromQueenMail: true})
}

// InjectWorkerReport queues a worker's [WORKER_REPORT] block as the queen's
// next turn. It is delivered as a non-client ("[External event]") user message,
// exactly like the upstream AgentLoop.inject_event(notification) the worker
// report handler calls, and never counted as a real user message.
//
// Parity: framework/server/queen_orchestrator.py::_on_worker_report (inject_event)
func (r *ChatRuntime) InjectWorkerReport(report string) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(report) == "" {
		return nil
	}
	return r.enqueueTurn(chatTurnRequest{message: report, clientInput: false})
}

// InjectEvent queues content as the queen's next turn. A client input is a
// real user message; anything else is delivered as an "[External event]" user
// turn and never counted as a real user message. This is the QueenNode seam
// the session's trigger timers and the Sentinel manager resume path call.
//
// Parity: framework/agent_loop/agent_loop.py::inject_event
func (r *ChatRuntime) InjectEvent(content string, isClientInput bool) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if isClientInput {
		return r.Deliver(content)
	}
	return r.enqueueTurn(chatTurnRequest{message: content, clientInput: false})
}

// InjectTrigger queues a framework trigger (timer tick or webhook hit) as the
// queen's next turn. Triggers are delivered as their own user message, tagged
// is_trigger so the dashboard renders a banner instead of the raw block; they
// are never counted as client input.
//
// Parity: framework/agent_loop/agent_loop.py::inject_trigger
// Parity: framework/agent_loop/internals/cursor_persistence.py::drain_trigger_queue
func (r *ChatRuntime) InjectTrigger(event agentloop.TriggerEvent) error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	pending := event
	// A live overseer owns the session's turns: the trigger goes to it alone,
	// so the chat queen does not answer the same tick a second time.
	if r.relayTriggerToOverseer(pending) {
		return nil
	}
	if err := r.enqueueTurn(chatTurnRequest{triggers: []agentloop.TriggerEvent{pending}}); err != nil {
		return err
	}
	// The colony's persistent overseer is the second lead for a bound colony;
	// give it the same trigger so it is not a Queen with no input source.
	r.forwardTriggerToOverseer(pending)
	return nil
}

func (r *ChatRuntime) enqueueTurn(request chatTurnRequest) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrChatRuntimeClosed
	}
	r.queue = append(r.queue, request)
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// LastError reports the outcome of the most recent turn (nil after a
// successful turn).
func (r *ChatRuntime) LastError() error {
	if r == nil {
		return ErrChatRuntimeNoLLM
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastErr
}

// Transcript reads the persisted conversation parts from the store.
func (r *ChatRuntime) Transcript() ([]map[string]any, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	return r.store.ReadParts()
}
