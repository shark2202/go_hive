package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

func readEventFrame(t *testing.T, reader *bufio.Reader) (string, string) {
	t.Helper()
	eventName := ""
	data := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE frame: %v", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if eventName != "" || data != "" {
				return eventName, data
			}
			continue
		}
		if strings.HasPrefix(trimmed, "event: ") {
			eventName = strings.TrimPrefix(trimmed, "event: ")
		}
		if strings.HasPrefix(trimmed, "data: ") {
			data = strings.TrimPrefix(trimmed, "data: ")
		}
	}
}

func waitForSubscriber(t *testing.T, bus *eventbus.EventBus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bus.SubscriptionCount() > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("global SSE handler never subscribed")
}

// Parity-exempt: routes_events.py has no Python test for the global SSE
// stream; this pins the subscribe + frame contract against the real bus.
func TestGlobalEventsStreamDeliversCRMChange(t *testing.T) {
	bus := eventbus.NewEventBus(eventbus.WithMaxHistory(200))
	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{
		GlobalBus: func() *eventbus.EventBus { return bus },
		Keepalive: time.Hour,
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events/global", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET global: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	waitForSubscriber(t, bus)

	event := eventbus.NewAgentEvent(eventbus.EventCRMChanged, "global")
	event.Data = map[string]any{"entities": []string{"people"}, "schema": false}
	bus.Publish(event)

	reader := bufio.NewReader(response.Body)
	name, data := readEventFrame(t, reader)
	if name != "" {
		t.Fatalf("event = %q, want a data-only frame on the global channel", name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["type"] != "crm_changed" || payload["stream_id"] != "global" {
		t.Fatalf("payload = %#v", payload)
	}
	cancel()
}

// Parity-exempt: no upstream test asserts that the global channel ignores
// event types outside _GLOBAL_EVENT_TYPES.
func TestGlobalEventsStreamFiltersUnrelatedTypes(t *testing.T) {
	bus := eventbus.NewEventBus(eventbus.WithMaxHistory(200))
	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{
		GlobalBus: func() *eventbus.EventBus { return bus },
		Keepalive: time.Hour,
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events/global", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET global: %v", err)
	}
	defer response.Body.Close()
	waitForSubscriber(t, bus)

	bus.Publish(eventbus.NewAgentEvent(eventbus.EventToolCallStarted, "queen"))
	time.Sleep(20 * time.Millisecond)
	bus.Publish(eventbus.NewAgentEvent(eventbus.EventCredentialProviderConnected, "global"))

	reader := bufio.NewReader(response.Body)
	name, data := readEventFrame(t, reader)
	if name != "" {
		t.Fatalf("event = %q, want a data-only frame", name)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["type"] != "credential_provider_connected" {
		t.Fatalf("first event = %#v, want credential_provider_connected (unrelated type leaked)", payload["type"])
	}
	cancel()
}

// Parity-exempt: routes_events.py sends the same 15s keepalive comment; this
// pins that an idle global stream emits it at the configured cadence.
func TestGlobalEventsStreamKeepalive(t *testing.T) {
	bus := eventbus.NewEventBus(eventbus.WithMaxHistory(200))
	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{
		GlobalBus: func() *eventbus.EventBus { return bus },
		Keepalive: 10 * time.Millisecond,
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/events/global", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET global: %v", err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read keepalive: %v", err)
	}
	if strings.TrimRight(line, "\r\n") != ": keepalive" {
		t.Fatalf("line = %q, want keepalive comment", line)
	}
	cancel()
}

// Parity-exempt: no upstream test covers /api/crm/changed; this pins the
// entity allowlist, de-duplication, sorting and schema flag.
func TestCRMChangedPublishesFilteredEntities(t *testing.T) {
	bus := eventbus.GetGlobalEventBus()
	queue := make(chan *eventbus.AgentEvent, 4)
	subID := bus.Subscribe([]eventbus.EventType{eventbus.EventCRMChanged}, func(event *eventbus.AgentEvent) {
		select {
		case queue <- event:
		default:
		}
	})
	defer bus.Unsubscribe(subID)

	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{})
	recorder := httptest.NewRecorder()
	body := `{"entities":["people","bogus","organizations","people",5],"schema":true}`
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/crm/changed", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response["ok"] != true {
		t.Fatalf("response = %#v", response)
	}

	select {
	case event := <-queue:
		entities, _ := event.Data["entities"].([]string)
		if len(entities) != 2 || entities[0] != "organizations" || entities[1] != "people" {
			t.Fatalf("entities = %#v", event.Data["entities"])
		}
		if event.Data["schema"] != true {
			t.Fatalf("schema = %#v", event.Data["schema"])
		}
		if event.StreamID != "global" {
			t.Fatalf("stream_id = %q", event.StreamID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("crm_changed event was never published")
	}
}

// Parity-exempt: a malformed ping is explicitly tolerated upstream; this
// pins the empty payload and schema=false rather than a 500.
func TestCRMChangedToleratesMalformedBody(t *testing.T) {
	bus := eventbus.GetGlobalEventBus()
	queue := make(chan *eventbus.AgentEvent, 4)
	subID := bus.Subscribe([]eventbus.EventType{eventbus.EventCRMChanged}, func(event *eventbus.AgentEvent) {
		select {
		case queue <- event:
		default:
		}
	})
	defer bus.Unsubscribe(subID)

	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/crm/changed", strings.NewReader("{not json")))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	select {
	case event := <-queue:
		entities, ok := event.Data["entities"].([]string)
		if !ok || len(entities) != 0 {
			t.Fatalf("entities = %#v, want empty list", event.Data["entities"])
		}
		if event.Data["schema"] != false {
			t.Fatalf("schema = %#v, want false", event.Data["schema"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("crm_changed event was never published")
	}
}

// Parity-exempt: no upstream test asserts route ownership; this fails loudly
// if events ever re-registers the session SSE route owned by chat_routes.go.
func TestEventRoutesDoNotDuplicateSessionEventsRoute(t *testing.T) {
	mux := http.NewServeMux()
	RegisterChatRoutes(mux, ChatRouteDeps{})
	RegisterEventRoutes(mux, EventRouteDeps{GlobalBus: func() *eventbus.EventBus {
		return eventbus.NewEventBus()
	}})

	_, globalPattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/api/events/global", nil))
	if globalPattern != "GET /api/events/global" {
		t.Fatalf("global pattern = %q", globalPattern)
	}
	_, sessionPattern := mux.Handler(httptest.NewRequest(http.MethodGet, "/api/sessions/session_a/events", nil))
	if sessionPattern != "GET /api/sessions/{session_id}/events" {
		t.Fatalf("session pattern = %q, want the chat_routes owner", sessionPattern)
	}

	// A POST to the session SSE pattern is 405 — proof the events module added
	// no competing pattern.
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/sessions/session_a/events", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST session events status = %d, want 405", post.Code)
	}
}
