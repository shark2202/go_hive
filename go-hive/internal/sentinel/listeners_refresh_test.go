package sentinel

// A16: the notification/config routes call Manager.RefreshListeners after a
// save so a channel connected while the server is running starts listening
// without a restart. Python's SentinelManager.refresh_listeners is idempotent —
// a listener that is already running is left alone — and gates each channel on
// its token (telegram, slack app) or sign-in (hive inbox).

import (
	"context"
	"testing"
	"time"
)

type refreshTokenNotifier struct {
	telegram string
	slackApp string
	hive     bool
}

func (n *refreshTokenNotifier) Send(context.Context, string, map[string]any, string, map[string]any, map[string]any) NotifierResult {
	return NotifierResult{OK: true}
}

func (n *refreshTokenNotifier) TelegramToken() string { return n.telegram }
func (n *refreshTokenNotifier) SlackAppToken() string { return n.slackApp }
func (n *refreshTokenNotifier) HiveConnected() bool   { return n.hive }

type refreshListener struct {
	name    string
	started chan<- string
}

func (l refreshListener) Run(ctx context.Context) error {
	l.started <- l.name
	<-ctx.Done()
	return nil
}

// Parity: core/framework/sentinel/manager.py::SentinelManager.refresh_listeners
func TestManagerRefreshListenersStartsNewChannelsOnce(t *testing.T) {
	store := NewStore(t.TempDir())
	notifier := &refreshTokenNotifier{}
	started := make(chan string, 8)
	manager := NewManager(ManagerOptions{
		Store:    store,
		Tokens:   fixedSecretTokens(t, store),
		Notifier: notifier,
		Clock:    newFakeClock(),
		ListenerFactory: func(name string, _ *Manager) Listener {
			return refreshListener{name: name, started: started}
		},
	})
	if err := manager.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(manager.Stop)

	// No tokens at start: the manager must not spin up channel listeners.
	select {
	case name := <-started:
		t.Fatalf("listener %q started without a token", name)
	case <-time.After(50 * time.Millisecond):
	}

	notifier.telegram = "bot-token"
	manager.RefreshListeners()
	select {
	case name := <-started:
		if name != "telegram" {
			t.Fatalf("started listener = %q, want telegram", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("telegram listener did not start after a token appeared")
	}
	if !manager.IsListening(ChannelTelegram) {
		t.Fatal("telegram listener is not reported as running")
	}

	// A second save must not start a duplicate listener for the same channel.
	manager.RefreshListeners()
	select {
	case name := <-started:
		t.Fatalf("duplicate %q listener started", name)
	case <-time.After(100 * time.Millisecond):
	}

	// Each channel is independently gated: the hive inbox needs a signed-in
	// account, not a channel token.
	notifier.hive = true
	manager.RefreshListeners()
	select {
	case name := <-started:
		if name != "hive" {
			t.Fatalf("started listener = %q, want hive", name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hive listener did not start after sign-in")
	}
	if manager.IsListening(ChannelSlack) {
		t.Fatal("slack listener started without an app token")
	}
}
