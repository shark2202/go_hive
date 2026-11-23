package channels

// One subscription's ordered stream from the session backend to a plugin:
// bounded queue, drop-oldest policy, and a single delivery goroutine.
//
// Parity: framework/server/channels.py::_Subscription
// Parity-exempt: bounded queue with seq gaps is the stdio host's backpressure
// policy (upstream's asyncio queue is unbounded).

import (
	"fmt"
	"sync"
)

// subscription is one session's ordered output stream to one plugin.
type subscription struct {
	id        string
	owner     *Host
	runtime   *channelRuntime
	sessionID string
	handler   func(Output)
	backendUn func()
	process   *pluginProcess

	queue  chan Output
	stop   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	closed bool
	seq    int64
	drops  int64
}

func (h *Host) subscribe(runtime *channelRuntime, sessionID string) (any, error) {
	// Upstream's subscribe replaces the previous handler for the same
	// session; do the same across subscription ids.
	h.closeSubscriptions(func(sub *subscription) bool {
		return sub.runtime == runtime && sub.sessionID == sessionID
	})

	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return nil, Errorf(KindInternal, "channel host is stopping")
	}
	h.nextSub++
	sub := &subscription{
		id:        fmt.Sprintf("sub_%d", h.nextSub),
		owner:     h,
		runtime:   runtime,
		sessionID: sessionID,
		process:   runtime.process(),
		queue:     make(chan Output, h.options.QueueSize),
		stop:      make(chan struct{}),
	}
	sub.handler = sub.enqueue
	h.subs[sub.id] = sub
	// Count the worker while the host lock is held: Stop sets stopped and then
	// waits, and an Add that raced in afterwards would let the wait pass
	// before the goroutine starts.
	h.wg.Add(1)
	h.mu.Unlock()

	unsubscribe, err := runtime.backend.Subscribe(sessionID, sub.handler)
	if err != nil {
		h.removeSub(sub)
		h.wg.Done()
		return nil, err
	}
	sub.setBackendUnsubscribe(unsubscribe)
	go h.deliverOutputs(sub)
	return subscribeResult{SubscriptionID: sub.id}, nil
}

func (h *Host) unsubscribe(runtime *channelRuntime, subscriptionID string) {
	h.closeSubscriptions(func(sub *subscription) bool {
		return sub.id == subscriptionID && sub.runtime == runtime
	})
}

func (h *Host) removeSub(sub *subscription) {
	h.mu.Lock()
	if h.subs[sub.id] == sub {
		delete(h.subs, sub.id)
	}
	h.mu.Unlock()
}

// closeSubscriptions closes every matching subscription, one at a time, and
// never while holding the host lock (backend unsubscribe may run slowly).
func (h *Host) closeSubscriptions(match func(*subscription) bool) {
	h.mu.Lock()
	var toClose []*subscription
	for _, sub := range h.subs {
		if match(sub) {
			toClose = append(toClose, sub)
		}
	}
	h.mu.Unlock()
	for _, sub := range toClose {
		sub.close()
	}
}

func (s *subscription) enqueue(output Output) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case <-s.stop:
		return
	default:
	}
	// Bounded queue with an explicit drop-oldest policy: when the plugin
	// cannot consume QueenOutputs as fast as the queen produces them, the
	// newest output wins and the seq gap tells the plugin something was lost.
	if len(s.queue) == cap(s.queue) {
		select {
		case <-s.queue:
			s.drops++
			if s.owner != nil && (s.drops == 1 || s.drops%64 == 0) {
				s.owner.options.Logf("channel: output queue for subscription %s is full; dropped=%d", s.id, s.drops)
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

// setBackendUnsubscribe records the backend's unsubscribe hook. A close that
// raced ahead runs the hook immediately instead of leaking the subscription.
func (s *subscription) setBackendUnsubscribe(unsubscribe func()) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if unsubscribe != nil {
			unsubscribe()
		}
		return
	}
	s.backendUn = unsubscribe
	s.mu.Unlock()
}

func (s *subscription) close() {
	s.once.Do(func() {
		s.mu.Lock()
		s.closed = true
		unsubscribe := s.backendUn
		s.backendUn = nil
		s.mu.Unlock()
		close(s.stop)
		if unsubscribe != nil {
			unsubscribe()
		}
		if s.owner != nil {
			s.owner.removeSub(s)
		}
	})
}

func (h *Host) deliverOutputs(sub *subscription) {
	defer h.wg.Done()
	for {
		select {
		case <-sub.stop:
			return
		case output := <-sub.queue:
			if sub.process == nil {
				h.options.Logf("channel: subscription %s has no plugin process", sub.id)
				return
			}
			if err := sub.process.notify("output", sub.outputParams(output)); err != nil {
				return
			}
		}
	}
}

func (s *subscription) outputParams(output Output) outputParams {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	sessionID := output.SessionID
	if sessionID == "" {
		sessionID = s.sessionID
	}
	return outputParams{
		SubscriptionID: s.id,
		SessionID:      sessionID,
		Kind:           output.Kind,
		Text:           output.Text,
		Questions:      output.Questions,
		Files:          output.Files,
		Seq:            seq,
	}
}
