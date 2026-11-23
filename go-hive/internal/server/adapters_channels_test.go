package server

// Channel host session backend: open/send/subscribe/set_title/judge over the
// real SessionManager and ChatRuntime.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/channels"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

type createdChannelSession struct {
	params  CreateSessionParams
	session *Session
}

func newChannelTestBackend(t *testing.T, manager *SessionManager) (*ChannelBackend, *[]createdChannelSession) {
	t.Helper()
	home := newFeatureHome(t)
	created := []createdChannelSession{}
	backend := NewChannelBackend(ChannelBackendOptions{
		Channel: "chat",
		Home:    home,
		Manager: manager,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			id := "session_created"
			if params.SessionID != nil && *params.SessionID != "" {
				id = *params.SessionID
			}
			if params.QueenResumeFrom != nil && *params.QueenResumeFrom != "" {
				id = *params.QueenResumeFrom
			}
			queenName := "default"
			if params.QueenName != nil && *params.QueenName != "" {
				queenName = *params.QueenName
			}
			dir := home.QueenSessionDir(queenName, id)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			session := NewSession(id, eventbus.NewEventBus())
			session.QueenName = queenName
			session.QueenDir = dir
			manager.AddSession(session)
			created = append(created, createdChannelSession{params: params, session: session})
			return session, nil
		},
	})
	return backend, &created
}

// TestChannelBackendOpenSessionCreatesClaimsAndTitles covers the new-session
// branch of ChannelHost.open_session.
//
// Parity: core/framework/server/channels.py::ChannelHost.open_session
func TestChannelBackendOpenSessionCreatesClaimsAndTitles(t *testing.T) {
	manager := NewSessionManager()
	backend, created := newChannelTestBackend(t, manager)

	id, err := backend.OpenSession(context.Background(), "queen_operations", "", "Design chat")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if id != "session_created" {
		t.Fatalf("session id = %q", id)
	}
	if len(*created) != 1 {
		t.Fatalf("create calls = %d", len(*created))
	}
	params := (*created)[0].params
	if params.QueenName == nil || *params.QueenName != "queen_operations" {
		t.Fatalf("QueenName = %#v", params.QueenName)
	}
	if params.InitialPhase == nil || *params.InitialPhase != "independent" {
		t.Fatalf("InitialPhase = %#v", params.InitialPhase)
	}
	if params.QueenResumeFrom != nil {
		t.Fatalf("QueenResumeFrom = %#v, want nil for a fresh session", params.QueenResumeFrom)
	}
	session := (*created)[0].session
	if session.Channel() != "chat" {
		t.Fatalf("session channel = %q", session.Channel())
	}
	meta, err := readJSONObject(filepath.Join(session.QueenDir, "meta.json"))
	if err != nil || meta["channel"] != "chat" {
		t.Fatalf("meta = %#v (%v)", meta, err)
	}
	if title, ok := ReadTitle(session.QueenDir); !ok || title != "Design chat" {
		t.Fatalf("title = %q (present %v)", title, ok)
	}
}

// TestChannelBackendOpenSessionRefusesUnknownQueen maps a missing queen onto
// ChannelError's upstream wording.
//
// Parity: core/framework/server/channels.py::ChannelHost.open_session
func TestChannelBackendOpenSessionRefusesUnknownQueen(t *testing.T) {
	manager := NewSessionManager()
	backend, created := newChannelTestBackend(t, manager)
	_, err := backend.OpenSession(context.Background(), "queen_nope", "", "")
	if err == nil {
		t.Fatal("OpenSession accepted an unknown queen")
	}
	var channelErr *channels.ChannelError
	if !errors.As(err, &channelErr) {
		t.Fatalf("error = %T %v, want ChannelError", err, err)
	}
	if channelErr.Kind != channels.KindNoQueen {
		t.Fatalf("kind = %q", channelErr.Kind)
	}
	if !strings.Contains(channelErr.Message, "No queen 'queen_nope'") ||
		!strings.Contains(channelErr.Message, "'chat' channel's settings") {
		t.Fatalf("message = %q", channelErr.Message)
	}
	if len(*created) != 0 {
		t.Fatalf("unknown queen started %d sessions", len(*created))
	}
}

// TestChannelBackendOpenSessionResumesTheNewestSuccessor follows
// meta.json[superseded_by] before resuming.
//
// Parity: core/framework/server/channels.py::ChannelHost.open_session
func TestChannelBackendOpenSessionResumesTheNewestSuccessor(t *testing.T) {
	manager := NewSessionManager()
	backend, created := newChannelTestBackend(t, manager)
	home := backend.home
	oldDir := home.QueenSessionDir("queen_operations", "session_old")
	newDir := home.QueenSessionDir("queen_operations", "session_new")
	for dir, meta := range map[string]map[string]any{
		oldDir: {"queen_id": "queen_operations", "superseded_by": "session_new"},
		newDir: {"queen_id": "queen_operations"},
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(meta)
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	id, err := backend.OpenSession(context.Background(), "queen_operations", "session_old", "")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if id != "session_new" {
		t.Fatalf("resumed id = %q, want the successor session_new", id)
	}
	if len(*created) != 1 {
		t.Fatalf("create calls = %d", len(*created))
	}
	params := (*created)[0].params
	if params.QueenResumeFrom == nil || *params.QueenResumeFrom != "session_new" {
		t.Fatalf("QueenResumeFrom = %#v", params.QueenResumeFrom)
	}
}

// TestChannelBackendQueenOfFallsBackToTheSessionTree resolves the owning queen
// when meta.json has no queen_id, as upstream _resume_queen_name does.
//
// Parity: core/framework/server/channels.py::ChannelHost._queen_of
func TestChannelBackendQueenOfFallsBackToTheSessionTree(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	dir := filepath.Join(backend.home.QueenSessionsDir("queen_operations"), "session_legacy")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if queenID := backend.queenOf("session_legacy"); queenID != "queen_operations" {
		t.Fatalf("queenOf = %q, want the directory's queen", queenID)
	}
}

// TestChannelBackendOpenSessionStartsFreshWhenTheSessionIsLocked refuses to
// resume a colony-spawned session, as upstream's _resume returns None.
//
// Parity: core/framework/server/channels.py::ChannelHost._resume
func TestChannelBackendOpenSessionStartsFreshWhenTheSessionIsLocked(t *testing.T) {
	manager := NewSessionManager()
	backend, created := newChannelTestBackend(t, manager)
	dir := backend.home.QueenSessionDir("queen_operations", "session_colony")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"queen_id": "queen_operations", "colony_spawned": true})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	id, err := backend.OpenSession(context.Background(), "queen_operations", "session_colony", "")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if id != "session_created" {
		t.Fatalf("id = %q, want a fresh session", id)
	}
	if params := (*created)[0].params; params.QueenResumeFrom != nil {
		t.Fatalf("QueenResumeFrom = %#v, want nil for a locked session", params.QueenResumeFrom)
	}
}

// TestChannelBackendSendDeliversEchoAndImages injects a real client turn and
// publishes the dashboard echo with the plugin's correlation id.
//
// Parity: core/framework/server/channels.py::ChannelHost.send
func TestChannelBackendSendDeliversEchoAndImages(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	session, _ := newChannelChatSession(t, manager, "session_send", "queen_operations")

	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	// The second attachment is not an image: upstream _image_block skips it
	// with a warning, and only the PNG is saved and sent.
	id, err := backend.Send(context.Background(), session.ID, "look at this", [][]byte{png, []byte("not an image")}, "shown echo", "corr-chat-1")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != session.ID {
		t.Fatalf("Send returned %q", id)
	}
	waitForAssistantParts(t, session.Chat, 1)

	events := session.EventBus.GetHistory(
		eventbus.WithHistoryType(eventbus.EventClientInputReceived),
		eventbus.WithHistoryLimit(5),
	)
	if len(events) == 0 {
		t.Fatal("no CLIENT_INPUT_RECEIVED event")
	}
	event := events[0]
	if event.CorrelationID == nil || *event.CorrelationID != "corr-chat-1" {
		t.Fatalf("correlation id = %#v", event.CorrelationID)
	}
	if event.Data["content"] != "shown echo" {
		t.Fatalf("echo content = %#v", event.Data["content"])
	}
	if event.Data["image_count"] != 1 {
		t.Fatalf("image_count = %#v", event.Data["image_count"])
	}
	if paths, _ := event.Data["image_paths"].([]string); len(paths) != 1 {
		t.Fatalf("image_paths = %#v", event.Data["image_paths"])
	}
}

// TestChannelBackendSendRefusesLockedSessionsAndMissingOnes maps the two
// ChannelError branches upstream send raises.
//
// Parity: core/framework/server/channels.py::ChannelHost.send
func TestChannelBackendSendRefusesLockedSessionsAndMissingOnes(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)

	locked := NewSession("session_locked", eventbus.NewEventBus())
	locked.QueenName = "queen_operations"
	locked.ColonySpawned = true
	manager.AddSession(locked)
	_, err := backend.Send(context.Background(), locked.ID, "hello", nil, "", "")
	var channelErr *channels.ChannelError
	if !errors.As(err, &channelErr) || channelErr.Kind != channels.KindSessionLocked {
		t.Fatalf("locked send error = %#v", err)
	}
	if !strings.Contains(channelErr.Message, "locked because it spawned a colony") {
		t.Fatalf("locked message = %q", channelErr.Message)
	}

	_, err = backend.Send(context.Background(), "session_missing", "hello", nil, "", "")
	if !errors.As(err, &channelErr) || channelErr.Kind != channels.KindNoSession {
		t.Fatalf("missing send error = %#v", err)
	}
	if channelErr.Message != "No session 'session_missing'" {
		t.Fatalf("missing message = %q", channelErr.Message)
	}

	unavailable := NewSession("session_no_chat", eventbus.NewEventBus())
	unavailable.QueenName = "queen_operations"
	manager.AddSession(unavailable)
	_, err = backend.Send(context.Background(), unavailable.ID, "hello", nil, "", "")
	if !errors.As(err, &channelErr) || channelErr.Kind != channels.KindQueenUnavailable {
		t.Fatalf("unavailable send error = %#v", err)
	}
}

// TestChannelBackendSetTitleWritesAndClears covers the dashboard title write.
//
// Parity: core/framework/server/channels.py::ChannelHost.set_title
func TestChannelBackendSetTitleWritesAndClears(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	dir := backend.home.QueenSessionDir("queen_operations", "session_title")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{"queen_id": "queen_operations"})
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := backend.SetTitle(context.Background(), "session_title", "A name"); err != nil {
		t.Fatalf("SetTitle: %v", err)
	}
	if title, ok := ReadTitle(dir); !ok || title != "A name" {
		t.Fatalf("title = %q (present %v)", title, ok)
	}
	if err := backend.SetTitle(context.Background(), "session_title", ""); err != nil {
		t.Fatalf("SetTitle clear: %v", err)
	}
	if _, ok := ReadTitle(dir); ok {
		t.Fatal("clearing the title left one behind")
	}

	err := backend.SetTitle(context.Background(), "session_missing", "x")
	var channelErr *channels.ChannelError
	if !errors.As(err, &channelErr) || channelErr.Kind != channels.KindNoSession {
		t.Fatalf("missing SetTitle error = %#v", err)
	}
}

// TestChannelBackendJudgeWithoutASlotIsNil mirrors judge.ask returning None
// when no judge is configured.
//
// Parity: core/framework/server/channels.py::ChannelHost.judge
func TestChannelBackendJudgeWithoutASlotIsNil(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	probability, err := backend.Judge(context.Background(), "is it done?", map[string]any{"x": 1}, map[string]string{"true": "yes"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if probability != nil {
		t.Fatalf("probability = %v, want nil without a judge slot", *probability)
	}
}

// TestChannelBackendSubscriptionMapsQueenOutputs covers the event-to-output
// mapping, ordering, fork following and unsubscribe.
//
// Parity: core/framework/server/channels.py::_Subscription
func TestChannelBackendSubscriptionMapsQueenOutputs(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	outputs := make(chan channels.Output, 32)
	unsubscribe, err := backend.Subscribe("session_sub", func(output channels.Output) {
		outputs <- output
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()

	session := NewSession("session_sub", eventbus.NewEventBus())
	session.QueenName = "queen_operations"
	manager.AddSession(session) // the session-live listener attaches the subscription

	bus := session.EventBus
	bus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", "hello", nil, nil, 0)
	toolStarted := eventbus.NewAgentEvent(eventbus.EventToolCallStarted, chatStreamID)
	toolStarted.Data = map[string]any{"tool_name": "terminal", "tool_use_id": "t1"}
	bus.Publish(toolStarted)
	bus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", "world", nil, nil, 0)
	bus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, chatStreamID))
	askUser := "ask_user"
	bus.EmitClientInputRequested(chatStreamID, chatNodeID, nil, []map[string]any{
		{"id": "q1", "prompt": "Continue?"},
	}, &askUser)

	attachment := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(attachment, []byte("report"), 0o644); err != nil {
		t.Fatal(err)
	}
	bus.EmitToolCallCompleted(chatStreamID, chatNodeID, "t2", "attach_file",
		`{"attached":[{"resolved":`+strconvQuote(attachment)+`}]} trailing text`, false, nil)

	want := []channels.OutputKind{
		channels.OutputMessage,
		channels.OutputMessage,
		channels.OutputQuestion,
		channels.OutputIdle,
		channels.OutputFiles,
	}
	got := make([]channels.Output, 0, len(want))
	for len(got) < len(want) {
		select {
		case output := <-outputs:
			got = append(got, output)
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out after %d outputs: %#v", len(got), got)
		}
	}
	for index, kind := range want {
		if got[index].Kind != kind {
			t.Fatalf("output %d kind = %q, want %q (%#v)", index, got[index].Kind, kind, got)
		}
	}
	if got[0].Text != "hello" || got[1].Text != "world" {
		t.Fatalf("message texts = %q, %q", got[0].Text, got[1].Text)
	}
	if len(got[2].Questions) != 1 || got[2].Questions[0]["id"] != "q1" {
		t.Fatalf("question = %#v", got[2].Questions)
	}
	if len(got[4].Files) != 1 || got[4].Files[0] != attachment {
		t.Fatalf("files = %#v", got[4].Files)
	}

	// A fork moves the same subscription onto the new session's bus and the
	// outputs carry the new session id.
	next := NewSession("session_next", eventbus.NewEventBus())
	next.QueenName = "queen_operations"
	manager.AddSession(next)
	forked := eventbus.NewAgentEvent(eventbus.EventSessionForked, chatStreamID)
	forked.Data = map[string]any{"new_session_id": "session_next"}
	bus.Publish(forked)
	next.EventBus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", "after fork", nil, nil, 0)
	next.EventBus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, chatStreamID))

	select {
	case output := <-outputs:
		if output.SessionID != "session_next" || output.Text != "after fork" || output.Kind != channels.OutputMessage {
			t.Fatalf("fork output = %#v", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output after the fork")
	}

	unsubscribe()
	next.EventBus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", "after unsubscribe", nil, nil, 0)
	next.EventBus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, chatStreamID))
	select {
	case output := <-outputs:
		t.Fatalf("output after unsubscribe = %#v", output)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestChannelBackendSubscriptionSurvivesStopAndResume attaches the same
// subscription to the resumed session's new event bus.
//
// Parity: core/framework/server/channels.py::_Subscription
func TestChannelBackendSubscriptionSurvivesStopAndResume(t *testing.T) {
	home := newFeatureHome(t)
	manager := NewSessionManager()
	var resumes []string
	backend := NewChannelBackend(ChannelBackendOptions{
		Channel: "chat",
		Home:    home,
		Manager: manager,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			id := "session_resume"
			if params.QueenResumeFrom != nil && *params.QueenResumeFrom != "" {
				id = *params.QueenResumeFrom
				resumes = append(resumes, id)
			}
			dir := home.QueenSessionDir("queen_operations", id)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
			if _, err := os.Stat(filepath.Join(dir, "meta.json")); err != nil {
				meta, _ := json.Marshal(map[string]any{"queen_id": "queen_operations"})
				if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
					return nil, err
				}
			}
			session := NewSession(id, eventbus.NewEventBus())
			session.QueenName = "queen_operations"
			session.QueenDir = dir
			runtime, err := NewChatRuntime(ChatRuntimeDeps{
				LLM:       &fakeChatLLM{},
				Root:      filepath.Join(dir, "runtime-"+id),
				Bus:       session.EventBus,
				SessionID: id,
			})
			if err != nil {
				return nil, err
			}
			session.Chat = runtime
			manager.AddSession(session)
			return session, nil
		},
	})

	const sessionID = "session_resume"
	t.Cleanup(func() {
		if session := manager.GetSession(sessionID); session != nil {
			manager.StopSession(sessionID)
			if session.Chat != nil {
				_ = session.Chat.Close()
			}
		}
	})

	outputs := make(chan channels.Output, 8)
	unsubscribe, err := backend.Subscribe(sessionID, func(output channels.Output) {
		outputs <- output
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()

	id, err := backend.OpenSession(context.Background(), "queen_operations", "", "")
	if err != nil || id != sessionID {
		t.Fatalf("OpenSession = %q, %v", id, err)
	}
	if !manager.StopSession(id) {
		t.Fatal("StopSession refused the live session")
	}
	if _, err := backend.Send(context.Background(), id, "still there?", nil, "", ""); err != nil {
		t.Fatalf("Send after stop: %v", err)
	}
	if len(resumes) != 1 || resumes[0] != "session_resume" {
		t.Fatalf("resumes = %#v", resumes)
	}
	resumed := manager.GetSession(id)
	if resumed == nil {
		t.Fatalf("session %s was not resumed", id)
	}
	resumed.EventBus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", "Yes.", nil, nil, 0)
	resumed.EventBus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, chatStreamID))
	select {
	case output := <-outputs:
		if output.Text != "Yes." || output.SessionID != id {
			t.Fatalf("resumed output = %#v", output)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no output from the resumed session")
	}
}

// TestChannelBackendHandlerPanicKeepsLaterOutputs mirrors upstream's logged
// handler failure: one bad handler call does not silence the stream.
//
// Parity: core/framework/server/channels.py::_Subscription
func TestChannelBackendHandlerPanicKeepsLaterOutputs(t *testing.T) {
	manager := NewSessionManager()
	backend, _ := newChannelTestBackend(t, manager)
	received := make(chan string, 4)
	calls := 0
	unsubscribe, err := backend.Subscribe("session_flaky", func(output channels.Output) {
		calls++
		if calls == 1 {
			panic("chat app went away")
		}
		received <- output.Text
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer unsubscribe()
	session := NewSession("session_flaky", eventbus.NewEventBus())
	session.QueenName = "queen_operations"
	manager.AddSession(session)
	bus := session.EventBus
	for _, text := range []string{"one", "two"} {
		bus.EmitClientOutputDelta(chatStreamID, chatNodeID, "", text, nil, nil, 0)
		bus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, chatStreamID))
	}
	select {
	case text := <-received:
		if text != "two" {
			t.Fatalf("received %q, want the output after the panic", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the handler panic silenced later outputs")
	}
}

// newChannelChatSession builds a live manager-owned session with a real
// ChatRuntime over the fake test model.
func newChannelChatSession(t *testing.T, manager *SessionManager, id, queen string) (*Session, *fakeChatLLM) {
	t.Helper()
	dir := t.TempDir()
	session := NewSession(id, eventbus.NewEventBus())
	session.QueenName = queen
	session.QueenDir = dir
	model := &fakeChatLLM{}
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:       model,
		Root:      filepath.Join(dir, "runtime"),
		Bus:       session.EventBus,
		SessionID: id,
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	session.Chat = runtime
	manager.AddSession(session)
	t.Cleanup(func() {
		manager.StopSession(id)
		_ = runtime.Close()
	})
	return session, model
}

// strconvQuote quotes a path for a JSON string without importing strconv at
// the call site.
func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
