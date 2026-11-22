package server

// Reliability probes for the HTTP surface: a stale cursor must not let the
// record-message route overwrite history, and SSE subscribers must be released
// when clients disconnect while events are being published.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// Parity-exempt: fault-injection probe for a stale cursor on the
// record-message route; the new turn must not overwrite an existing part.
func TestRecordUserMessageStaleCursorDoesNotOverwrite(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	sessionID := "session_20261119_120000_abcdef01"
	sessionDir := filepath.Join(home.QueensDir(), "default", "sessions", sessionID)
	convsDir := filepath.Join(sessionDir, "conversations")
	if err := os.MkdirAll(filepath.Join(convsDir, "parts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	store := agentloop.NewFileConversationStore(convsDir)
	for seq := 0; seq < 5; seq++ {
		message := agentloop.Message{Seq: seq, Role: agentloop.RoleUser, Content: fmt.Sprintf("m%d", seq)}
		if err := store.WritePart(seq, message.ToStorageDict()); err != nil {
			t.Fatalf("seed part: %v", err)
		}
	}
	if err := store.WriteCursor(map[string]any{"next_seq": 2}); err != nil {
		t.Fatalf("cursor: %v", err)
	}

	mux := http.NewServeMux()
	RegisterExecutionRoutes(mux, ExecutionRouteDeps{Home: home, Manager: NewSessionManager()})
	request := httptest.NewRequest(http.MethodPost, "/api/sessions/"+sessionID+"/record-message",
		strings.NewReader(`{"message":"new turn"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder, body := serveColonyRequest(mux, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", recorder.Code, body)
	}

	parts, err := store.ReadParts()
	if err != nil {
		t.Fatalf("ReadParts: %v", err)
	}
	if len(parts) != 6 {
		t.Fatalf("parts = %d, want 6 (a stale cursor overwrote history)", len(parts))
	}
	contents := map[int]string{}
	for _, part := range parts {
		seq, _ := part["seq"].(float64)
		content, _ := part["content"].(string)
		contents[int(seq)] = content
	}
	if contents[2] != "m2" {
		t.Fatalf("part 2 = %q, want original m2", contents[2])
	}
	if contents[5] != "new turn" {
		t.Fatalf("part 5 = %q, want the new turn", contents[5])
	}
	cursor, err := store.ReadCursor()
	if err != nil {
		t.Fatalf("ReadCursor: %v", err)
	}
	if next := agentloop.GetCursorNextSeq(cursor); next == nil || *next != 6 {
		t.Fatalf("cursor = %v, want next_seq 6", cursor)
	}
}

// Parity-exempt: SSE subscription churn under concurrent publishing must
// leave no subscriptions behind (and must be race-free).
func TestGlobalSSESubscriptionsReleasedUnderChurn(t *testing.T) {
	bus := eventbus.NewEventBus(eventbus.WithMaxConcurrentHandlers(16))
	mux := http.NewServeMux()
	RegisterEventRoutes(mux, EventRouteDeps{
		GlobalBus: func() *eventbus.EventBus { return bus },
		Keepalive: time.Hour,
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	const clients = 8
	var wg sync.WaitGroup
	connected := make(chan struct{}, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := http.Get(server.URL + "/api/events/global")
			if err != nil {
				return
			}
			connected <- struct{}{}
			// Simulate a slow-but-open client: let a few frames flush, then go.
			time.Sleep(20 * time.Millisecond)
			response.Body.Close()
		}()
	}

	deadline := time.Now().Add(3 * time.Second)
	for i := 0; i < clients; i++ {
		select {
		case <-connected:
		case <-time.After(3 * time.Second):
			t.Fatal("SSE clients did not connect")
		}
	}

	publishCtx, stopPublishing := context.WithCancel(context.Background())
	var publisher sync.WaitGroup
	publisher.Add(1)
	go func() {
		defer publisher.Done()
		for {
			select {
			case <-publishCtx.Done():
				return
			default:
			}
			event := eventbus.NewAgentEvent(eventbus.EventCredentialProviderConnected, "global")
			event.Data = map[string]any{"provider": "test"}
			bus.Publish(event)
		}
	}()

	wg.Wait()
	stopPublishing()
	publisher.Wait()

	for {
		if bus.SubscriptionCount() == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriptions = %d after all clients disconnected; handlers leaked", bus.SubscriptionCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
