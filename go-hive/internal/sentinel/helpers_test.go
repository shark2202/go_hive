package sentinel

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeClock is a manually-advanced Clock.
type fakeClock struct {
	mu      sync.Mutex
	current time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{current: time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(d)
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.Advance(d)
	return ctx.Err()
}

// newTestStore returns a Store rooted in a temp dir with the given sentinel
// global block (nil → defaults).
func newTestStore(t *testing.T, global map[string]any) *Store {
	t.Helper()
	root := t.TempDir()
	store := NewStore(root)
	if global == nil {
		global = map[string]any{}
	}
	store.GlobalConfig = func() map[string]any {
		return map[string]any{"sentinel": global}
	}
	if err := os.MkdirAll(store.ColoniesDir, 0o755); err != nil {
		t.Fatalf("mkdir colonies: %v", err)
	}
	return store
}

// writeColonyConfig writes a notifications.json for colony under the store.
func writeColonyConfig(t *testing.T, store *Store, colony string, config map[string]any) {
	t.Helper()
	dir := filepath.Join(store.ColoniesDir, colony)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, notificationsFilename), data, 0o600); err != nil {
		t.Fatalf("write notifications: %v", err)
	}
}

// fixedSecretTokens returns a token manager pinned to a deterministic secret.
func fixedSecretTokens(t *testing.T, store *Store) *TokenManager {
	t.Helper()
	path := filepath.Join(filepath.Dir(store.ColoniesDir), secretFilename)
	tokens := &TokenManager{SecretPath: path}
	tokens.SetSecret(bytes.Repeat([]byte{0}, 32))
	return tokens
}

// noopSink records inbound calls for listener tests.
type noopSink struct {
	mu      sync.Mutex
	chats   map[string]map[string]any
	inbound []inboundCall
}

type inboundCall struct {
	channel   string
	senderID  string
	text      string
	threadRef map[string]any
	source    string
}

func (s *noopSink) RecordRecentChat(channel string, info map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.chats == nil {
		s.chats = map[string]map[string]any{}
	}
	s.chats[channel] = info
}

func (s *noopSink) OnInbound(_ context.Context, channel, senderID, text string, threadRef map[string]any, source string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inbound = append(s.inbound, inboundCall{channel: channel, senderID: senderID, text: text, threadRef: threadRef, source: source})
}
