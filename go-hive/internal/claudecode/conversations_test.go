package claudecode

// The conversations of the queens run by Claude Code, exercised from the
// Claude Code side: the mail service (queenmail.Mail) delivers into this
// package's Store, so these integration checks live here with both halves
// importable.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queenmail"
)

// Parity-exempt: queen ids become directory names under claude_code/; a
// tampered id must not escape it (the companion of the queenmail store's
// letter-id guard, which the same test used to cover from the other side).
func TestStoreHomeRejectsTraversalQueenID(t *testing.T) {
	root := t.TempDir()
	store := NewStore(hivehome.Home{Root: root})
	home := store.Home("../../escape")
	if !strings.HasPrefix(home, filepath.Join(root, "claude_code")+string(filepath.Separator)) {
		t.Fatalf("Store.Home escaped: %q", home)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_her_conversation_with_a_writer_is_resumed_next_time
func TestAClaudeCodeQueenGetsAConversationAndHerTokenAuthorizesIt(t *testing.T) {
	home := newMailHome(t)
	mail, store := newQueenMailService(t, home, testConfig(), &fakeDeliverer{})

	first, err := mail.Send(ava, "session_ava", claude, "Hello?", nil)
	if err != nil {
		t.Fatal(err)
	}
	records := mail.Store().ReadAudit()
	ref := records[0].ToSession
	if !queenmail.IsConversationRef(ref) || !strings.HasPrefix(ref, "claude_code:"+claude+":") {
		t.Fatalf("ref = %q", ref)
	}
	if queenID, ok := mail.QueenOf(ref); !ok || queenID != claude {
		t.Fatalf("queen = %q, ok = %v", queenID, ok)
	}
	if !mail.Authorizes(ref, mail.TokenFor(ref)) {
		t.Fatal("the token for the conversation must authorize it")
	}
	if mail.Authorizes(ref, "wrong") || mail.Authorizes("", mail.TokenFor("")) {
		t.Fatal("a wrong or empty token must not authorize")
	}
	pending := store.Pending(claude)
	if len(pending) != 1 || pending[0].Text != "Hello?" || pending[0].From != ava {
		t.Fatalf("pending = %#v", pending)
	}

	second, err := mail.Send(ava, "session_ava", claude, "And again?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if second["depth"] != 1 {
		t.Fatalf("second depth = %#v", second["depth"])
	}
	if len(store.Pending(claude)) != 2 {
		t.Fatalf("pending = %#v", store.Pending(claude))
	}
	if len(store.Conversations(claude)) != 1 {
		t.Fatalf("conversations = %#v", store.Conversations(claude))
	}
	if first["sent"] == second["sent"] {
		t.Fatal("each letter is its own hop")
	}

	// A conversation written by the Python server is picked up from disk.
	var saved storedConversations
	data, err := os.ReadFile(filepath.Join(home.Root, "claude_code", claude, "conversations.json"))
	if err != nil || !strings.Contains(string(data), "peer") {
		t.Fatalf("conversations.json = %q, err = %v", string(data), err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Conversations) != 1 {
		t.Fatalf("saved = %#v", saved)
	}
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_she_writes_to_a_hive_queen_and_her_answer_comes_back_to_the_conversation
func TestAReplyGoesToTheConversationAndContinuesTheChain(t *testing.T) {
	home := newMailHome(t)
	mail, store := newQueenMailService(t, home, testConfig(), &fakeDeliverer{})

	sent, err := mail.Send(ava, "session_ava", claude, "Question?", nil)
	if err != nil {
		t.Fatal(err)
	}
	record := mail.Store().ReadAudit()[0]
	ref := record.ToSession
	cause := &queenmail.Cause{Hop: sent["sent"].(string), Depth: 1}
	if err := mail.DeliverReply(ref, claude, ava, ref, "Answer.", cause); err != nil {
		t.Fatal(err)
	}
	records := mail.Store().ReadAudit()
	if len(records) != 2 {
		t.Fatalf("audit = %#v", records)
	}
	reply := records[1]
	if reply.Kind != queenmail.KindReply || reply.From != claude || reply.To != ava || reply.Depth != 2 {
		t.Fatalf("reply = %#v", reply)
	}
	if reply.CausedBy == nil || *reply.CausedBy != sent["sent"] {
		t.Fatalf("caused_by = %#v", reply.CausedBy)
	}
	pending := store.Pending(claude)
	if len(pending) != 2 || pending[1].Kind != queenmail.KindReply || pending[1].Text != "Answer." {
		t.Fatalf("pending = %#v", pending)
	}
}
