package server

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
)

// assertWebhookPayloadValue compares an any-typed webhook payload against the
// expected value; nil stays distinguishable from an empty map.
func assertWebhookPayloadValue(t *testing.T, got, want any, context string) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Fatalf("%s = %#v (%T), want nil", context, got, got)
		}
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v (%T), want %#v (%T)", context, got, got, want, want)
	}
}

// Parity: framework/host/webhook_server.py::WebhookServer._handle_request
// Parity: framework/host/event_bus.py::EventBus.emit_webhook_received
func TestWebhookServerKeepsAnyJSONPayloadParity(t *testing.T) {
	// Upstream parses `payload = json.loads(body) if body else {}`: any JSON
	// type is kept verbatim (array/number/string/bool/null/object), and only a
	// decode failure falls back to {"raw_body": ...}.
	cases := []struct {
		name string
		body string
		want any
	}{
		{"object", `{"k":"v","n":2}`, map[string]any{"k": "v", "n": float64(2)}},
		{"array", `[1,"two",true,null]`, []any{float64(1), "two", true, nil}},
		{"number", `42`, float64(42)},
		{"string", `"hello"`, "hello"},
		{"bool", `false`, false},
		{"null", `null`, nil},
		{"non json raw_body", `not json`, map[string]any{"raw_body": "not json"}},
		{"empty body", ``, map[string]any{}},
	}

	bus := eventbus.NewEventBus()
	capture := newEventCapture(bus, eventbus.EventWebhookReceived)
	server := newWebhookServer(bus, "127.0.0.1", 0).(*webhookServer)
	server.AddRoute("src", "/hook", []string{"POST"})
	if err := server.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	if server.Port() == 0 {
		t.Fatalf("bound port is 0")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/hook", server.Port())

	emitted := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := client.Post(url, "application/json", strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", resp.StatusCode)
			}
			emitted++
			events := capture.eventsOf(eventbus.EventWebhookReceived)
			if len(events) != emitted {
				t.Fatalf("captured %d events, want %d", len(events), emitted)
			}
			event := events[len(events)-1]
			if event.StreamID != "src" {
				t.Fatalf("stream = %q, want %q", event.StreamID, "src")
			}
			got, present := event.Data["payload"]
			if !present {
				t.Fatalf("event data has no payload key: %#v", event.Data)
			}
			assertWebhookPayloadValue(t, got, tc.want, "event data payload")
		})
	}
}

// Parity: framework/tools/queen_lifecycle_tools.py::_start_trigger_webhook
func TestWebhookTriggerInjectsPayloadVerbatimParity(t *testing.T) {
	// data.get("payload", {}) keeps the event payload as-is, including None
	// (JSON null) and non-object JSON types.
	payloads := []struct {
		name  string
		value any
	}{
		{"object", map[string]any{"k": "v"}},
		{"array", []any{"a", float64(1)}},
		{"number", float64(7)},
		{"string", "text"},
		{"bool", true},
		{"null", nil},
	}
	for _, tc := range payloads {
		t.Run(tc.name, func(t *testing.T) {
			bus := eventbus.NewEventBus()
			_, node := startWebhookTriggerForTest(t, bus)

			bus.EmitWebhookReceived("hook", "/hook", "POST", map[string]string{}, tc.value, map[string]string{})

			events := node.triggerEvents()
			if len(events) != 1 {
				t.Fatalf("injected %d events, want 1", len(events))
			}
			got, present := events[0].Payload["payload"]
			if !present {
				t.Fatalf("injected payload has no payload key: %#v", events[0].Payload)
			}
			assertWebhookPayloadValue(t, got, tc.value, "trigger payload[payload]")
		})
	}

	t.Run("missing payload key", func(t *testing.T) {
		// data.get("payload", {}) defaults to {} only when the key is absent.
		bus := eventbus.NewEventBus()
		_, node := startWebhookTriggerForTest(t, bus)

		event := eventbus.NewAgentEvent(eventbus.EventWebhookReceived, "hook")
		event.Data = map[string]any{"path": "/hook", "method": "POST"}
		bus.Publish(event)

		events := node.triggerEvents()
		if len(events) != 1 {
			t.Fatalf("injected %d events, want 1", len(events))
		}
		got, present := events[0].Payload["payload"]
		if !present {
			t.Fatalf("injected payload has no payload key: %#v", events[0].Payload)
		}
		if !reflect.DeepEqual(got, map[string]any{}) {
			t.Fatalf("payload = %#v (%T), want empty map", got, got)
		}
	})
}

func startWebhookTriggerForTest(t *testing.T, bus *eventbus.EventBus) (*Session, *fakeQueenNode) {
	t.Helper()
	session := newTriggerTestSession(t, "test_agent", bus)
	node := &fakeQueenNode{}
	installFakeQueen(session, node)
	session.WebhookServer = &fakeWebhookServer{}
	def := triggers.TriggerDefinition{
		ID:            "hook",
		TriggerType:   "webhook",
		TriggerConfig: map[string]any{"path": "/hook", "methods": []any{"POST"}},
		Task:          "handle",
	}
	session.PutTrigger(def)
	if err := StartTriggerWebhook(session, "hook", &def); err != nil {
		t.Fatalf("start webhook: %v", err)
	}
	t.Cleanup(func() {
		if subID := session.PopActiveWebhookSub("hook"); subID != "" {
			bus.Unsubscribe(subID)
		}
	})
	return session, node
}
