package channels

// SessionBackend is what the host needs from the server's queen-session
// runtime. The server adapter implements it over the live SessionManager;
// tests use an in-memory fake. Methods mirror upstream ChannelHost's public
// surface, with the channel name supplied by the host factory rather than
// stored here.
//
// Parity: framework/server/channels.py::ChannelHost (open_session, send,
// subscribe, unsubscribe, set_title, judge)
// Parity-exempt: the backend is an interface so the host package does not
// import the server runtime (no import cycle and a fake for protocol tests).

import (
	"context"
	"time"
)

type SessionBackend interface {
	// OpenSession returns the session for one conversation with queenID,
	// resuming sessionID when given (or its newest successor) and starting a
	// fresh one otherwise. title names a new session in the dashboard.
	OpenSession(ctx context.Context, queenID, sessionID, title string) (string, error)

	// Send injects a user message into sessionID, resuming it if it stopped.
	// images are image bytes; displayText replaces text in the dashboard echo
	// when non-empty; correlationID marks the client_input_received event.
	Send(ctx context.Context, sessionID, text string, images [][]byte, displayText, correlationID string) (string, error)

	// Subscribe calls fn with what the queen says in sessionID from now on.
	// Calls arrive one at a time, in order, from one goroutine; fn must not
	// block for long. Subscribing again for the same session replaces the
	// previous subscription. The returned function stops the subscription.
	Subscribe(sessionID string, fn func(Output)) (func(), error)

	// SetTitle names a session in the dashboard; an empty title clears it.
	SetTitle(ctx context.Context, sessionID, title string) error

	// Judge returns the judge slot's probability that the answer to question
	// is yes, or nil when no judge is configured or it cannot answer.
	Judge(ctx context.Context, question string, state map[string]any, criteria map[string]string) (*float64, error)
}

// Default timeouts mirror upstream's 30-second start and stop budget. The
// hello handshake and a plugin request get their own budgets so one slow
// plugin call cannot hold a session's event bus.
const (
	DefaultStartTimeout     = 30 * time.Second
	DefaultStopTimeout      = 30 * time.Second
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultCallTimeout      = 60 * time.Second
)
