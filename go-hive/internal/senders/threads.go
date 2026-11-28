package senders

// Outbound conversation store — the half that lets a reply find its way home.
//
// Every trackable send is stamped with an RFC 5322 Message-ID we mint
// ourselves, <hive.TOKEN@from-domain>. A well-behaved mail client echoes that
// value in the reply's In-Reply-To/References headers, so the reply carries
// our opaque token back without plus-addressing or a visible footer. This
// store maps that token to the colony and sender that produced the message.
//
// The file is $HIVE_HOME/senders/threads.json and its shape is upstream
// tools/src/aden_tools/senders/threads.py, byte-for-byte in field names: a
// bare JSON object keyed by token, with no version wrapper. Keeping the
// Python shape means a Go-written store is readable by the upstream runtime
// (and vice versa).
//
// Writes are read-modify-write under both an in-process mutex and an OS file
// lock, because two engines in one process (or two processes) may append at
// once; losing an append would orphan a live conversation. Reads rely on
// atomic replace, so they never observe a half-written file.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const threadsFile = "threads.json"

// threadTokenPattern is the token embedded in a Message-ID we minted: the
// same base32-ish lowercase alphabet and length bounds as threads.py.
var threadTokenPattern = regexp.MustCompile(`(?i)hive\.([a-z0-9]{8,32})@`)

// ThreadRecord is one conversation, exactly as threads.py serializes it.
// last_reply_at and references are always present in the file: upstream
// writes null and an array, never omitted keys.
type ThreadRecord struct {
	Token       string   `json:"token"`
	MessageID   string   `json:"message_id"`
	ColonyID    string   `json:"colony_id"`
	SenderID    string   `json:"sender_id"`
	FromEmail   string   `json:"from_email"`
	ToEmail     string   `json:"to_email"`
	Subject     string   `json:"subject"`
	SentAt      string   `json:"sent_at"`
	Replies     int      `json:"replies"`
	LastReplyAt *string  `json:"last_reply_at"`
	References  []string `json:"references"`
}

// ThreadSend is the outbound identity of a message, recorded after delivery.
type ThreadSend struct {
	Token     string
	MessageID string
	ColonyID  string
	SenderID  string
	FromEmail string
	ToEmail   string
	Subject   string
}

// ThreadStore is a JSON-backed conversation store. Its zero value is unusable;
// use OpenThreadStore.
type ThreadStore struct {
	dir string
	now func() time.Time
	mu  sync.Mutex
}

// OpenThreadStore binds a thread store to the senders directory.
func OpenThreadStore(dir string, now func() time.Time) *ThreadStore {
	if now == nil {
		now = time.Now
	}
	return &ThreadStore{dir: dir, now: now}
}

// Path is the threads.json location (diagnostics and tests).
func (t *ThreadStore) Path() string { return filepath.Join(t.dir, threadsFile) }

func (t *ThreadStore) lockPath() string { return t.Path() + ".lock" }

// load reads the store. A missing file is an empty store; an unreadable one is
// an error. Upstream logs and starts empty on a decode error, which would let
// the next append silently overwrite every live conversation — durability
// wins over that behaviour here.
func (t *ThreadStore) load() (map[string]ThreadRecord, error) {
	var state map[string]ThreadRecord
	if err := atomicfs.ReadJSON(t.Path(), &state); err != nil {
		if os.IsNotExist(err) {
			return map[string]ThreadRecord{}, nil
		}
		return nil, fmt.Errorf("read sender threads: %w", err)
	}
	if state == nil {
		state = map[string]ThreadRecord{}
	}
	return state, nil
}

func (t *ThreadStore) persist(state map[string]ThreadRecord) error {
	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	// The store carries recipient addresses and subjects; keep the directory
	// private even if it predates this store.
	if err := os.Chmod(t.dir, 0o700); err != nil {
		return fmt.Errorf("chmod senders dir: %w", err)
	}
	// No trailing newline: threads.py writes json.dump(state, f, indent=2).
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode sender threads: %w", err)
	}
	return atomicfs.WriteFile(t.Path(), data, 0o600)
}

// update runs a read-modify-write under the process mutex and an OS file lock.
// The lock file is a sibling, not threads.json itself: the atomic replace
// swaps the inode, so locking the data file would stop excluding anyone the
// moment a write completes.
func (t *ThreadStore) update(mutate func(map[string]ThreadRecord) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := os.MkdirAll(t.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	lockFile, err := os.OpenFile(t.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("lock sender threads: %w", err)
	}
	defer func() { _ = lockFile.Close() }()
	unlock := lockThreadFile(lockFile)
	defer unlock()

	state, err := t.load()
	if err != nil {
		return err
	}
	if err := mutate(state); err != nil {
		return err
	}
	return t.persist(state)
}

// RecordSend remembers an outbound message so its reply can be routed back.
// Recording the same token again replaces the row, matching threads.py.
func (t *ThreadStore) RecordSend(send ThreadSend) (ThreadRecord, error) {
	token := strings.TrimSpace(send.Token)
	if token == "" {
		return ThreadRecord{}, errors.New("sender threads: conversation token is required")
	}
	record := ThreadRecord{
		Token:       token,
		MessageID:   send.MessageID,
		ColonyID:    send.ColonyID,
		SenderID:    send.SenderID,
		FromEmail:   send.FromEmail,
		ToEmail:     send.ToEmail,
		Subject:     send.Subject,
		SentAt:      t.now().UTC().Format(time.RFC3339Nano),
		Replies:     0,
		LastReplyAt: nil,
		References:  []string{send.MessageID},
	}
	err := t.update(func(state map[string]ThreadRecord) error {
		state[token] = record
		return nil
	})
	if err != nil {
		return ThreadRecord{}, err
	}
	return record, nil
}

// Get resolves a token to its conversation. A token we never minted returns
// nil, which is what makes a forged Message-ID inert.
func (t *ThreadStore) Get(token string) (*ThreadRecord, error) {
	state, err := t.load()
	if err != nil {
		return nil, err
	}
	record, ok := state[token]
	if !ok {
		return nil, nil
	}
	return &record, nil
}

// RecordReply marks that a reply arrived and returns the updated conversation.
// An unknown token returns nil, matching threads.record_reply.
func (t *ThreadStore) RecordReply(token string) (*ThreadRecord, error) {
	var updated *ThreadRecord
	err := t.update(func(state map[string]ThreadRecord) error {
		record, ok := state[token]
		if !ok {
			return nil
		}
		record.Replies++
		now := t.now().UTC().Format(time.RFC3339Nano)
		record.LastReplyAt = &now
		state[token] = record
		copy := record
		updated = &copy
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// Conversations returns every recorded conversation. Upstream returns them in
// file insertion order; a JSON object decoded into a Go map has no order, so
// this sorts by sent_at then token to stay deterministic across runs.
func (t *ThreadStore) Conversations() ([]ThreadRecord, error) {
	state, err := t.load()
	if err != nil {
		return nil, err
	}
	out := make([]ThreadRecord, 0, len(state))
	for _, record := range state {
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SentAt != out[j].SentAt {
			return out[i].SentAt < out[j].SentAt
		}
		return out[i].Token < out[j].Token
	})
	return out, nil
}

// ExtractThreadToken pulls our token out of an inbound reply's threading
// headers. Pass In-Reply-To and References: References accumulates the chain,
// so it still resolves on the second or third reply when a client omits
// In-Reply-To. Header order is the caller's preference order.
func ExtractThreadToken(headerValues ...string) string {
	for _, value := range headerValues {
		match := threadTokenPattern.FindStringSubmatch(value)
		if match != nil {
			return strings.ToLower(match[1])
		}
	}
	return ""
}
