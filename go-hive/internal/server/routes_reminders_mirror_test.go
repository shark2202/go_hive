package server

import (
	"net/http"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

// Parity-exempt: Go-only REST mirror. Upstream exposes no reminder REST
// surface; this pins GET to the live session loop hub instead of the retired
// standalone ReminderRuntime queue.
func TestReminderRoutesMirrorSessionHubReadOnly(t *testing.T) {
	runtime := NewReminderRuntime()
	mux := http.NewServeMux()
	RegisterReminderRoutes(mux, ReminderRouteDeps{Runtime: runtime})

	hub := agentloop.NewReminderHub()
	hub.Register(agentloop.NewIdleNudgeSource(agentloop.IdleNudgeConfig{BudgetSeconds: 60, MaxNudges: 1}))
	hub.Post(agentloop.Reminder{
		Body:   "You have been idle for 61s. Check whether the current task is stuck.",
		Source: "idle_nudge",
		Meta:   map[string]any{"substate": "between_turns"},
	})
	unregister := registerSessionReminderHub("s-mirror", hub)
	t.Cleanup(unregister)

	status, first, _ := reminderGET(t, mux, "/api/sessions/s-mirror/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	items := reminderItems(t, first)
	if len(items) != 1 {
		t.Fatalf("reminders = %#v, want one hub reminder", items)
	}
	if items[0]["source"] != "idle_nudge" {
		t.Fatalf("source = %v, want idle_nudge", items[0]["source"])
	}
	if first["pending"] != float64(1) || first["active"] != true || first["mirror"] != true {
		t.Fatalf("mirror metadata = %#v", first)
	}
	sources, _ := first["sources"].([]any)
	found := false
	for _, name := range sources {
		if name == "idle_nudge" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sources = %v, want idle_nudge", first["sources"])
	}

	// A GET is a read-only mirror: the loop must still receive the reminder.
	if got := hub.PendingLen(); got != 1 {
		t.Fatalf("hub pending after GET = %d, want 1 (GET must not drain)", got)
	}
	_, second, _ := reminderGET(t, mux, "/api/sessions/s-mirror/reminders?drain=1")
	if got := reminderItems(t, second); len(got) != 1 {
		t.Fatalf("second GET = %#v, want the same reminder", got)
	}

	status, dismissed := reminderDELETE(t, mux, "/api/sessions/s-mirror/reminders")
	if status != http.StatusOK || dismissed["cleared"] != float64(1) {
		t.Fatalf("dismiss = %d %#v, want 200 cleared=1", status, dismissed)
	}
	if got := hub.PendingLen(); got != 0 {
		t.Fatalf("hub pending after DELETE = %d, want 0", got)
	}
}

// Parity-exempt: Go-only REST mirror. Pins the production source of truth: a
// live ChatRuntime's own hub wins over the legacy ReminderRuntime fallback,
// even when that fallback still holds reminders.
func TestReminderRoutesPreferLiveChatHubOverFallbackRuntime(t *testing.T) {
	chat, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		SessionID: "s-live-source",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = chat.Close() })

	chat.hub.Post(agentloop.Reminder{
		Body:   "LIVE-HUB-MARKER",
		Source: "skills_catalog",
	})

	fallback := NewReminderRuntime()
	state := fallback.session("s-live-source")
	state.mu.Lock()
	state.pending = []ReminderView{{ID: 99, Source: "fallback", Body: "FALLBACK-DECOY"}}
	state.mu.Unlock()

	mux := http.NewServeMux()
	RegisterReminderRoutes(mux, ReminderRouteDeps{Runtime: fallback})

	status, body, _ := reminderGET(t, mux, "/api/sessions/s-live-source/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	items := reminderItems(t, body)
	if len(items) != 1 || items[0]["body"] != "LIVE-HUB-MARKER" {
		t.Fatalf("reminders = %#v, want the live chat hub item only", items)
	}
	if body["mirror"] != true {
		t.Fatalf("mirror = %#v, want true for a live chat session", body["mirror"])
	}
	if body["pending"] != float64(1) {
		t.Fatalf("pending = %#v, want 1", body["pending"])
	}
	if got := chat.hub.PendingLen(); got != 1 {
		t.Fatalf("chat hub pending after GET = %d, want 1 (REST must not drain)", got)
	}
}

// Parity-exempt: Go-only REST mirror behaviour for a session that has no live
// chat runtime yet (cold session / no provider).
func TestReminderRoutesNoHubReturnsEmptyWithoutError(t *testing.T) {
	runtime := NewReminderRuntime()
	mux := http.NewServeMux()
	RegisterReminderRoutes(mux, ReminderRouteDeps{Runtime: runtime})

	status, body, _ := reminderGET(t, mux, "/api/sessions/s-cold/reminders")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if items := reminderItems(t, body); len(items) != 0 {
		t.Fatalf("reminders = %#v, want []", items)
	}
	if body["pending"] != float64(0) || body["active"] != false {
		t.Fatalf("mirror metadata = %#v", body)
	}
}

// Parity-exempt: proves the production registration path, not just the route
// helper: NewChatRuntime publishes its loop hub and Close removes it.
func TestChatRuntimePublishesReminderHubToMirror(t *testing.T) {
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       &fakeChatLLM{},
		Root:      t.TempDir(),
		SessionID: "s-live-hub",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	if hub := liveSessionReminderHub("s-live-hub"); hub == nil {
		t.Fatal("chat runtime did not publish its reminder hub")
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if hub := liveSessionReminderHub("s-live-hub"); hub != nil {
		t.Fatal("closed chat runtime left its reminder hub registered")
	}
}
