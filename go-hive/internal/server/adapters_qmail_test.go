package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queenmail"
)

type queenMailRouteNode struct {
	mu    sync.Mutex
	texts []string
}

func (n *queenMailRouteNode) InjectEvent(text string, isClientInput bool) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, text)
	return nil
}

func (n *queenMailRouteNode) injected() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...)
}

func writeQueenMailRouteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func queenMailRouteHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{"queen_ava": "Ava", "queen_ivy": "Ivy"} {
		writeQueenMailRouteFile(t, filepath.Join(home.QueensDir(), id, "profile.yaml"), "name: "+name+"\ntitle: Cartographer\n")
	}
	writeQueenMailRouteFile(t, home.ConfigurationFile(), `{"queen_mail": {
		"enabled": true,
		"queens": ["queen_ava", "queen_ivy"],
		"claude_code": {"queen_claude": {"name": "Claude", "title": "Engineer"}}
	}}`)
	writeQueenMailRouteFile(t, filepath.Join(home.Root, "claude_code", "queen_claude", "conversations.json"),
		`{"conversations": {"abc": {"peer": "queen_ava", "reply_to": "session_ava", "started": true}}}`)
	return home
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_routes_want_the_token_and_one_of_her_conversations
func TestQueenMailRoutesUseTheAdapterAndDeliverALetter(t *testing.T) {
	home := queenMailRouteHome(t)
	manager := NewSessionManager()
	node := &queenMailRouteNode{}
	factoryCalls := 0
	adapter := NewQueenMailAdapter(QueenMailAdapterOptions{
		Home:    home,
		Manager: manager,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			factoryCalls++
			if params.QueenName == nil || *params.QueenName != "queen_ivy" {
				t.Fatalf("queen name = %#v", params.QueenName)
			}
			sessionID := "session_thread_" + strconv.Itoa(factoryCalls)
			sessionDir := filepath.Join(home.QueenSessionsDir("queen_ivy"), sessionID)
			if err := os.MkdirAll(sessionDir, 0o755); err != nil {
				t.Fatal(err)
			}
			session := NewSession(sessionID, eventbus.NewEventBus())
			session.QueenName = "queen_ivy"
			session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}
			manager.AddSession(session)
			return session, nil
		},
	})
	mux := http.NewServeMux()
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{QueenMail: adapter})
	ref := "claude_code:queen_claude:abc"
	token := adapter.TokenFor(ref)

	if recorder := doQueenMailRequest(t, mux, http.MethodGet, "/api/queen-mail/queens?from="+ref, "", ref, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d", recorder.Code)
	}
	recorder := doQueenMailRequest(t, mux, http.MethodGet, "/api/queen-mail/queens?from="+ref, "", ref, token)
	if recorder.Code != http.StatusOK {
		t.Fatalf("queens status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	body := queenDecodeResponse(t, recorder)
	queens, _ := body["queens"].([]any)
	if len(queens) != 2 {
		t.Fatalf("queens = %#v", body)
	}
	first, _ := queens[0].(map[string]any)
	secondQueen, _ := queens[1].(map[string]any)
	if first["id"] != "queen_ava" || first["name"] != "Ava" || secondQueen["id"] != "queen_ivy" {
		t.Fatalf("correspondents = %#v", queens)
	}

	letterBody := `{"from":"` + ref + `","to":"queen_ivy","message":"Hi"}`
	letter := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters", letterBody, ref, token)
	if letter.Code != http.StatusOK {
		t.Fatalf("letter status = %d, body = %s", letter.Code, letter.Body.String())
	}
	if sent := queenDecodeResponse(t, letter); sent["to"] != "queen_ivy" {
		t.Fatalf("sent = %#v", sent)
	}
	injected := node.injected()
	if len(injected) != 1 {
		t.Fatalf("injected = %#v", injected)
	}
	wantLetter := "(A letter from Claude, another queen, sent with message_queen. What you say in this conversation goes back to her, so just answer here; you don't need message_queen to reply.)\n[Claude(queen_claude)]: Hi"
	if injected[0] != wantLetter {
		t.Fatalf("letter = %q", injected[0])
	}

	// A second letter reuses the thread session and records the thread meta.
	second := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters", letterBody, ref, token)
	if second.Code != http.StatusOK || factoryCalls != 1 {
		t.Fatalf("second status = %d, factory calls = %d", second.Code, factoryCalls)
	}
	if len(node.injected()) != 2 {
		t.Fatalf("injected = %#v", node.injected())
	}
	metaRaw, err := os.ReadFile(filepath.Join(home.QueenSessionsDir("queen_ivy"), "session_thread_1", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]any
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["channel"] != "queens" || meta["peer"] != "queen_claude" || meta["reply_to"] != ref || meta["title"] != "Claude" {
		t.Fatalf("meta = %#v", meta)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_routes_want_the_token_and_one_of_her_conversations
func TestQueenMailRoutesRefusePosingAndUnknownConversations(t *testing.T) {
	home := queenMailRouteHome(t)
	adapter := NewQueenMailAdapter(QueenMailAdapterOptions{Home: home, Manager: NewSessionManager()})
	mux := http.NewServeMux()
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{QueenMail: adapter})
	ref := "claude_code:queen_claude:abc"
	token := adapter.TokenFor(ref)

	posing := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"queen_ava","to":"queen_ivy","message":"Hi"}`, ref, token)
	if posing.Code != http.StatusUnauthorized {
		t.Fatalf("posing status = %d", posing.Code)
	}
	unknownToken := adapter.TokenFor("claude_code:queen_claude:another")
	unknown := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"claude_code:queen_claude:another","to":"queen_ivy","message":"Hi"}`, "claude_code:queen_claude:another", unknownToken)
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d, body = %s", unknown.Code, unknown.Body.String())
	}
	if body := queenDecodeResponse(t, unknown); !containsSubstring(body["error"].(string), "isn't known to Hive") {
		t.Fatalf("error = %#v", body["error"])
	}
	unknownConversation := doQueenMailRequest(t, mux, http.MethodGet,
		"/api/queen-mail/queens?from=claude_code:queen_claude:another", "", "claude_code:queen_claude:another", unknownToken)
	if unknownConversation.Code != http.StatusNotFound {
		t.Fatalf("unknown conversation status = %d", unknownConversation.Code)
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_queen_outside_queen_mail_cant_write
func TestQueenMailRoutesPassRefusalsThrough(t *testing.T) {
	home := queenMailRouteHome(t)
	adapter := NewQueenMailAdapter(QueenMailAdapterOptions{Home: home, Manager: NewSessionManager()})
	mux := http.NewServeMux()
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{QueenMail: adapter})
	ref := "claude_code:queen_claude:abc"
	token := adapter.TokenFor(ref)

	ownID := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"`+ref+`","to":"queen_claude","message":"Hi"}`, ref, token)
	if ownID.Code != http.StatusBadRequest {
		t.Fatalf("own id status = %d, body = %s", ownID.Code, ownID.Body.String())
	}
	if body := queenDecodeResponse(t, ownID); body["error"] != "That's your own id" {
		t.Fatalf("error = %#v", body["error"])
	}
}

// Parity: framework/server/tests/test_queen_mail.py::test_a_writer_whose_session_stopped_is_woken_in_it
func TestQueenMailAdapterWakesAStoppedWritersSession(t *testing.T) {
	home := queenMailRouteHome(t)
	manager := NewSessionManager()
	node := &queenMailRouteNode{}
	adapter := NewQueenMailAdapter(QueenMailAdapterOptions{
		Home:    home,
		Manager: manager,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			if params.QueenName == nil || *params.QueenName != "queen_ava" {
				t.Fatalf("queen name = %#v", params.QueenName)
			}
			if params.QueenResumeFrom == nil || *params.QueenResumeFrom != "session_ava" {
				t.Fatalf("resume from = %#v", params.QueenResumeFrom)
			}
			session := NewSession("session_ava", eventbus.NewEventBus())
			session.QueenName = "queen_ava"
			session.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": node}}
			manager.AddSession(session)
			return session, nil
		},
	})
	letter := queenmail.Letter{ID: "hop_1", Kind: queenmail.KindReply, From: "queen_ivy", To: "queen_ava", FromName: "Ivy", Text: "North shore done.", Depth: 2}
	sessionID, err := adapter.DeliverReply(context.Background(), "session_ava", "queen_ivy", "queen_ava", letter)
	if err != nil || sessionID != "session_ava" {
		t.Fatalf("session = %q, err = %v", sessionID, err)
	}
	injected := node.injected()
	want := "(A reply from Ivy to your message_queen letter. To go on, write to her again.)\n[Ivy(queen_ivy)]: North shore done."
	if len(injected) != 1 || injected[0] != want {
		t.Fatalf("injected = %#v", injected)
	}
}

// waitForQueenMailInjections polls a fake queen node until it has at least
// count messages; the reply path runs on its own worker so the test must not
// assume the event publish alone completed it.
func waitForQueenMailInjections(t *testing.T, node *queenMailRouteNode, count int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if injected := node.injected(); len(injected) >= count {
			return injected
		}
		time.Sleep(5 * time.Millisecond)
	}
	return node.injected()
}

// Parity: framework/server/tests/test_queen_mail.py::test_what_she_says_goes_back_to_the_writer_in_her_name
func TestQueenMailRepliesGoBackToTheWriterAndContinueTheChain(t *testing.T) {
	home := queenMailRouteHome(t)
	manager := NewSessionManager()

	// The writer's own chat session is live and addressable by its id.
	avaNode := &queenMailRouteNode{}
	ava := NewSession("session_ava", eventbus.NewEventBus())
	ava.QueenName = "queen_ava"
	ava.QueenDir = filepath.Join(home.QueenSessionsDir("queen_ava"), "session_ava")
	ava.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": avaNode}}
	manager.AddSession(ava)

	// The recipient's thread session is opened by the adapter's deliverer, the
	// same way a real server starts (or resumes) her conversation.
	var ivy *Session
	ivyNode := &queenMailRouteNode{}
	adapter := NewQueenMailAdapter(QueenMailAdapterOptions{
		Home:    home,
		Manager: manager,
		CreateSession: func(params CreateSessionParams) (*Session, error) {
			if params.QueenName == nil || *params.QueenName != "queen_ivy" {
				t.Fatalf("queen name = %#v", params.QueenName)
			}
			ivy = NewSession("session_thread_1", eventbus.NewEventBus())
			ivy.QueenName = "queen_ivy"
			ivy.QueenDir = filepath.Join(home.QueenSessionsDir("queen_ivy"), "session_thread_1")
			ivy.QueenExecutor = &QueenExecutor{NodeRegistry: map[string]QueenNode{"queen": ivyNode}}
			manager.AddSession(ivy)
			return ivy, nil
		},
	})
	manager.SetQueenMail(adapter)

	sent, err := adapter.Mail().Send("queen_ava", "session_ava", "queen_ivy", "Can you map the north shore?", nil)
	if err != nil || sent["to"] != "queen_ivy" {
		t.Fatalf("send = %#v, err = %v", sent, err)
	}
	if ivy == nil {
		t.Fatal("the deliverer did not open the recipient's thread session")
	}
	if injected := ivyNode.injected(); len(injected) != 1 {
		t.Fatalf("letter injection = %#v", injected)
	}

	// What she says in that conversation is a reply, not a new letter she has
	// to send with message_queen.
	ivy.EventBus.EmitClientOutputDelta("queen", "queen", "The map is done.", "The map is done.", nil, nil, 0)
	ivy.EventBus.Publish(eventbus.NewAgentEvent(eventbus.EventLLMTurnComplete, "queen"))

	injected := waitForQueenMailInjections(t, avaNode, 1)
	want := "(A reply from Ivy to your message_queen letter. To go on, write to her again.)\n[Ivy(queen_ivy)]: The map is done."
	if len(injected) != 1 || injected[0] != want {
		t.Fatalf("reply injection = %#v", injected)
	}

	// The reply continued the chain: her next letter is hop 3. The delivery
	// worker records the cause right after the reply lands, so poll for it.
	var cause *queenmail.Cause
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if cause = adapter.Mail().CauseFor("session_ava"); cause != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cause == nil {
		t.Fatal("the delivered reply did not set the writer's cause")
	}
	next, err := adapter.Mail().Send("queen_ava", "session_ava", "queen_ivy", "And the east?", cause)
	if err != nil {
		t.Fatal(err)
	}
	if next["depth"] != 3 {
		t.Fatalf("next depth = %#v", next["depth"])
	}
	if !strings.Contains(adapter.Mail().ReplyTarget("queen_ivy", "queen_ava", "session_thread_1"), "session_ava") {
		t.Fatalf("reply target = %q", adapter.Mail().ReplyTarget("queen_ivy", "queen_ava", "session_thread_1"))
	}
}
