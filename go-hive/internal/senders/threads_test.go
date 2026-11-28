package senders

// threads.py has no pytest coverage in this repo, so these tests are
// parity-exempt but pin the upstream contract: the bare token-keyed JSON
// object, the exact field names, reply accounting, and the token embedded in
// a minted Message-ID. They also pin the two local-only guarantees the port
// adds: concurrent appends never lose a conversation, and a corrupt store
// fails closed instead of being silently replaced.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func openTestThreads(t *testing.T) (*ThreadStore, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "senders")
	return OpenThreadStore(dir, testClock()), dir
}

func sampleThreadSend(token string) ThreadSend {
	return ThreadSend{
		Token:     token,
		MessageID: "<hive." + token + "@example.com>",
		ColonyID:  "colony-1",
		SenderID:  "snd_1",
		FromEmail: "sender@example.com",
		ToEmail:   "sarah@example.com",
		Subject:   "Worth a chat?",
	}
}

// Parity-exempt: threads.py has no pytest coverage; this pins the on-disk
// shape field-for-field against ThreadRecord/record_send.
func TestThreadStoreWritesUpstreamShape(t *testing.T) {
	store, dir := openTestThreads(t)
	token := "0123456789abcdef"
	record, err := store.RecordSend(sampleThreadSend(token))
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if record.SentAt != "2026-11-19T09:00:00Z" {
		t.Fatalf("sent_at = %q, want the injected clock", record.SentAt)
	}
	if record.Replies != 0 || record.LastReplyAt != nil {
		t.Fatalf("new record = %+v, want replies=0 and last_reply_at=null", record)
	}
	if len(record.References) != 1 || record.References[0] != record.MessageID {
		t.Fatalf("references = %#v, want [message_id]", record.References)
	}

	data, err := os.ReadFile(filepath.Join(dir, threadsFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("threads.json is not the upstream token-keyed object: %v\n%s", err, data)
	}
	row, ok := raw[token]
	if !ok {
		t.Fatalf("token %q missing from %s", token, data)
	}
	wantKeys := []string{
		"token", "message_id", "colony_id", "sender_id", "from_email",
		"to_email", "subject", "sent_at", "replies", "last_reply_at", "references",
	}
	if len(row) != len(wantKeys) {
		t.Fatalf("row has %d fields, want exactly %d: %#v", len(row), len(wantKeys), row)
	}
	for _, key := range wantKeys {
		if _, ok := row[key]; !ok {
			t.Fatalf("upstream field %q missing: %#v", key, row)
		}
	}
	if row["token"] != token || row["message_id"] != record.MessageID {
		t.Fatalf("token/message_id = %#v", row)
	}
	if row["replies"] != float64(0) || row["last_reply_at"] != nil {
		t.Fatalf("replies/last_reply_at = %#v", row)
	}
	refs, ok := row["references"].([]any)
	if !ok || len(refs) != 1 || refs[0] != record.MessageID {
		t.Fatalf("references = %#v, want [%q]", row["references"], record.MessageID)
	}

	info, err := os.Stat(filepath.Join(dir, threadsFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("threads.json perm = %o, want 600", info.Mode().Perm())
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("senders dir perm = %o, want 700", info.Mode().Perm())
	}
}

// Parity-exempt: threads.py has no pytest coverage; this pins get/record_reply
// semantics across a reopened store.
func TestThreadStoreReopenAndReplyAccounting(t *testing.T) {
	store, dir := openTestThreads(t)
	token := "fedcba9876543210"
	if _, err := store.RecordSend(sampleThreadSend(token)); err != nil {
		t.Fatal(err)
	}

	reopened := OpenThreadStore(dir, testClock())
	got, err := reopened.Get(token)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Token != token || got.ColonyID != "colony-1" || got.Subject != "Worth a chat?" {
		t.Fatalf("reopened record = %+v", got)
	}

	updated, err := reopened.RecordReply(token)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Replies != 1 || updated.LastReplyAt == nil {
		t.Fatalf("first reply = %+v", updated)
	}
	updated, err = reopened.RecordReply(token)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Replies != 2 {
		t.Fatalf("second reply = %+v", updated)
	}

	unknown, err := reopened.RecordReply("unknown-token")
	if err != nil || unknown != nil {
		t.Fatalf("unknown token = %+v (err %v), want nil", unknown, err)
	}
	missing, err := reopened.Get("unknown-token")
	if err != nil || missing != nil {
		t.Fatalf("unknown get = %+v (err %v), want nil", missing, err)
	}

	conversations, err := reopened.Conversations()
	if err != nil || len(conversations) != 1 || conversations[0].Replies != 2 {
		t.Fatalf("conversations = %+v (err %v)", conversations, err)
	}
}

// Parity-exempt: threads.py has no pytest coverage; two store instances model
// two processes appending at once (the mutex alone cannot cover that).
func TestThreadStoreConcurrentAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "senders")
	stores := []*ThreadStore{
		OpenThreadStore(dir, testClock()),
		OpenThreadStore(dir, testClock()),
	}
	const writers = 8
	const perWriter = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			store := stores[n%len(stores)]
			for i := 0; i < perWriter; i++ {
				token := fmt.Sprintf("%016x", n*perWriter+i)
				if _, err := store.RecordSend(sampleThreadSend(token)); err != nil {
					errs <- err
				}
			}
		}(writer)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}

	state, err := os.ReadFile(filepath.Join(dir, threadsFile))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]ThreadRecord
	if err := json.Unmarshal(state, &raw); err != nil {
		t.Fatalf("threads.json corrupt after concurrent appends: %v\n%s", err, state)
	}
	want := writers * perWriter
	if len(raw) != want {
		t.Fatalf("persisted %d conversations, want %d (an append was lost)", len(raw), want)
	}
	for writer := 0; writer < writers; writer++ {
		for i := 0; i < perWriter; i++ {
			token := fmt.Sprintf("%016x", writer*perWriter+i)
			if _, ok := raw[token]; !ok {
				t.Fatalf("token %q lost", token)
			}
		}
	}
}

// Parity-exempt: threads.py has no pytest coverage; this pins extract_token's
// header order, case folding and the minimum/maximum token length.
func TestExtractThreadTokenFromReplyHeaders(t *testing.T) {
	messageID, token := MintMessageID("sender@example.com")
	if len(token) != 16 {
		t.Fatalf("minted token length = %d, want 16 hex chars", len(token))
	}
	if ExtractThreadToken(messageID) != token {
		t.Fatalf("extract(%q) = %q, want %q", messageID, ExtractThreadToken(messageID), token)
	}
	if ExtractThreadToken("<hive.ABCDEF12@example.com>") != "abcdef12" {
		t.Fatal("case-insensitive token match failed")
	}
	// In-Reply-To wins when present; References still resolves a later reply.
	references := "<hive.11111111@example.com> <hive.22222222@example.com>"
	if got := ExtractThreadToken("<hive.33333333@example.com>", references); got != "33333333" {
		t.Fatalf("first header = %q, want 33333333", got)
	}
	if got := ExtractThreadToken("", references); got != "11111111" {
		t.Fatalf("references fallback = %q, want 11111111", got)
	}
	if got := ExtractThreadToken("<20260101.abcdef@mail.example.com>", "not a token"); got != "" {
		t.Fatalf("foreign message ids resolved to %q, want empty", got)
	}
	if got := ExtractThreadToken("<hive.short@example.com>"); got != "" {
		t.Fatalf("short token resolved to %q, want empty", got)
	}
}

// Parity-exempt: upstream starts empty on an unreadable store; this is the
// local negative regression — a corrupt threads.json must fail closed, never
// be overwritten with a single new conversation.
func TestThreadStoreCorruptFileFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "senders")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, threadsFile)
	corrupt := []byte(`{"0123456789abcdef": {"token": `)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	store := OpenThreadStore(dir, testClock())
	if _, err := store.RecordSend(sampleThreadSend("aaaaaaaaaaaaaaaa")); err == nil {
		t.Fatal("RecordSend accepted a corrupt store")
	}
	if _, err := store.Conversations(); err == nil {
		t.Fatal("Conversations accepted a corrupt store")
	}
	if _, err := store.RecordReply("0123456789abcdef"); err == nil {
		t.Fatal("RecordReply accepted a corrupt store")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(corrupt) {
		t.Fatalf("corrupt store was rewritten:\n%s", data)
	}
}
