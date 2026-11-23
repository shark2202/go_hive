package server

// Turning one session's queen event stream into channels.Output calls, in
// order, and moving that stream when a fork moves the conversation. The
// event-bus handler only enqueues; a dedicated goroutine calls the host so a
// slow plugin can never stall the queen.
//
// Parity: framework/server/channels.py::_Subscription

import (
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/channels"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// channelSubscriptionTypes are the events upstream's _Subscription listens to.
var channelSubscriptionTypes = []eventbus.EventType{
	eventbus.EventClientOutputDelta,
	eventbus.EventToolCallStarted,
	eventbus.EventLLMTurnComplete,
	eventbus.EventClientInputRequested,
	eventbus.EventSessionForked,
	eventbus.EventToolCallCompleted,
}

type channelSubscription struct {
	backend *ChannelBackend
	handler func(channels.Output)

	queue chan channels.Output
	stop  chan struct{}
	once  sync.Once

	mu             sync.Mutex
	sessionID      string
	bus            *eventbus.EventBus
	busSubs        []string
	pending        string
	listenerRemove func()
	closed         bool
	drops          int64
}

func newChannelSubscription(backend *ChannelBackend, sessionID string, handler func(channels.Output)) *channelSubscription {
	sub := &channelSubscription{
		backend:   backend,
		handler:   handler,
		sessionID: sessionID,
		queue:     make(chan channels.Output, channels.DefaultQueueSize),
		stop:      make(chan struct{}),
	}
	sub.listenerRemove = backend.manager.AddSessionListener(sub.sessionLive)
	go sub.deliver()
	return sub
}

func (s *channelSubscription) sessionLive(session *Session) {
	if session == nil {
		return
	}
	s.mu.Lock()
	matches := !s.closed && session.ID == s.sessionID
	s.mu.Unlock()
	if !matches {
		return
	}
	s.backend.claim(session)
	s.attach(session)
}

// attach moves the subscription onto session's bus (a resumed session has a
// new one). Re-attaching to the same bus is a no-op.
//
// Parity: framework/server/channels.py::_Subscription.attach
func (s *channelSubscription) attach(session *Session) {
	if session == nil || session.EventBus == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || session.EventBus == s.bus {
		return
	}
	s.detachLocked()
	s.bus = session.EventBus
	s.pending = ""
	s.busSubs = []string{s.bus.Subscribe(
		channelSubscriptionTypes,
		s.handleEvent,
		eventbus.WithFilterStream(chatStreamID),
	)}
}

func (s *channelSubscription) detachLocked() {
	for _, id := range s.busSubs {
		if s.bus != nil {
			s.bus.Unsubscribe(id)
		}
	}
	s.busSubs = nil
	s.bus = nil
}

func (s *channelSubscription) move(sessionID string) {
	s.mu.Lock()
	s.sessionID = sessionID
	s.pending = ""
	s.mu.Unlock()
}

func (s *channelSubscription) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.detachLocked()
		remove := s.listenerRemove
		s.listenerRemove = nil
		s.mu.Unlock()
		close(s.stop)
		if remove != nil {
			remove()
		}
	})
}

// deliver calls the host handler one output at a time. A handler panic is
// logged; later outputs still arrive.
func (s *channelSubscription) deliver() {
	for {
		select {
		case <-s.stop:
			return
		case output := <-s.queue:
			s.deliverOne(output)
		}
	}
}

func (s *channelSubscription) deliverOne(output channels.Output) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("channel: handler failed for a %s in session %s: %v", output.Kind, output.SessionID, recovered)
		}
	}()
	s.handler(output)
}

// handleEvent maps one queen event to an Output. It runs on an event-bus
// goroutine, so it only appends to the bounded queue.
//
// Parity: framework/server/channels.py::_Subscription.attach (on_event)
func (s *channelSubscription) handleEvent(event *eventbus.AgentEvent) {
	if event == nil || event.StreamID != chatStreamID {
		return
	}
	data := event.Data
	if data == nil {
		data = map[string]any{}
	}
	forkTo := ""
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	switch event.Type {
	case eventbus.EventClientOutputDelta:
		// A model turn in progress; a later snapshot replaces this text.
		if snapshot := queenStr(data["snapshot"]); snapshot != "" {
			s.pending = snapshot
		}
	case eventbus.EventToolCallStarted:
		// What she wrote before a tool call is finished, and the tool may run
		// for minutes. An ask_user question carries its own text.
		if trimmedPending(s.pending) != "" && queenStr(data["tool_name"]) != "ask_user" {
			s.putLocked(s.replyKind(s.pending), s.pending, nil, nil)
			s.pending = ""
		}
	case eventbus.EventLLMTurnComplete:
		if trimmedPending(s.pending) != "" {
			s.putLocked(s.replyKind(s.pending), s.pending, nil, nil)
		}
		s.pending = ""
	case eventbus.EventClientInputRequested:
		reason := queenStr(data["park_reason"])
		questions := channelQuestions(data["questions"])
		switch {
		case reason == "ask_user" && len(questions) > 0:
			s.putLocked(channels.OutputQuestion, "", questions, nil)
		case reason == "llm_error" && trimmedPending(s.pending) != "":
			s.putLocked(channels.OutputError, s.pending, nil, nil)
		case trimmedPending(s.pending) != "":
			s.putLocked(s.replyKind(s.pending), s.pending, nil, nil)
		}
		s.pending = ""
		s.putLocked(channels.OutputIdle, "", nil, nil)
	case eventbus.EventSessionForked:
		forkTo = queenStr(data["new_session_id"])
	case eventbus.EventToolCallCompleted:
		if queenStr(data["tool_name"]) == "attach_file" && !channelBool(data["is_error"]) {
			if files := channelAttachedFiles(queenStr(data["result"])); len(files) > 0 {
				s.putLocked(channels.OutputFiles, "", nil, files)
			}
		}
	}
	s.mu.Unlock()
	if forkTo != "" {
		s.backend.followSubscription(s, forkTo)
	}
}

func (s *channelSubscription) replyKind(text string) channels.OutputKind {
	if visibleErrorText(text) {
		return channels.OutputError
	}
	return channels.OutputMessage
}

// putLocked enqueues one output; the caller holds s.mu. When the plugin falls
// behind, the oldest queued output is dropped (the host keeps the newest) so
// the queen's event bus is never blocked.
func (s *channelSubscription) putLocked(kind channels.OutputKind, text string, questions []map[string]any, files []string) {
	output := channels.Output{
		SessionID: s.sessionID,
		Kind:      kind,
		Text:      text,
		Questions: questions,
		Files:     files,
	}
	if len(s.queue) == cap(s.queue) {
		select {
		case <-s.queue:
			s.drops++
			if s.drops == 1 || s.drops%64 == 0 {
				log.Printf("channel: session %s output queue full; dropped=%d", s.sessionID, s.drops)
			}
		default:
		}
	}
	select {
	case s.queue <- output:
	default:
		s.drops++
	}
}

// channelQuestions reads the ask_user question list from an event, accepting
// both the in-process map shape and a decoded JSON array.
//
// Parity: framework/server/channels.py::QueenOutput.questions
func channelQuestions(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			switch question := entry.(type) {
			case map[string]any:
				out = append(out, question)
			default:
				if encoded, err := json.Marshal(entry); err == nil {
					decoded := map[string]any{}
					if json.Unmarshal(encoded, &decoded) == nil {
						out = append(out, decoded)
					}
				}
			}
		}
		return out
	default:
		return nil
	}
}

func trimmedPending(text string) string {
	return strings.TrimSpace(text)
}

func channelBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}

// followSubscription is the event's SESSION_FORKED callback: the conversation
// moved, so the same handler follows it to newID.
//
// Parity: framework/server/channels.py::ChannelHost._on_forked
func (b *ChannelBackend) followSubscription(sub *channelSubscription, newID string) {
	sub.mu.Lock()
	oldID := sub.sessionID
	sub.mu.Unlock()
	b.followSessionID(oldID, newID)
}
