package claudecode

// Fixtures shared by the runner and conversation tests. The Hive-home and
// deliverer helpers mirror the queenmail package's own test fixtures; test
// helpers do not cross package boundaries, and these tests drive the real
// mail service (queenmail.Mail) from the Claude Code side.

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queenmail"
)

const (
	ava = "queen_ava"
	ivy = "queen_ivy"
	// claude is the configured Claude Code queen's id.
	claude = "queen_claude"
)

// fakeDeliverer stands in for Hive queens' sessions: Deliver records the
// letters that landed and answers with a session id, DeliverReply records the
// replies. fail makes both report the error instead.
type fakeDeliverer struct {
	mu        sync.Mutex
	fail      error
	sessionID string
	replies   []string
	delivered []queenmail.Letter
}

func (f *fakeDeliverer) Deliver(_ context.Context, toQueen, writer, fromRef string, letter queenmail.Letter) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.delivered = append(f.delivered, letter)
	if f.sessionID == "" {
		return "session_" + toQueen, nil
	}
	return f.sessionID, nil
}

func (f *fakeDeliverer) DeliverReply(_ context.Context, replyTo, queenID, writer string, letter queenmail.Letter) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.replies = append(f.replies, letter.Text)
	return replyTo, nil
}

// newMailHome is a fresh Hive home with the Hive queens' profiles written, as
// the queenmail tests build it.
func newMailHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id, name := range map[string]string{ava: "Ava", ivy: "Ivy"} {
		writeQueenProfile(t, home.Root, id, name, "Cartographer")
	}
	return home
}

func writeQueenProfile(t *testing.T, homeRoot, queenID, name, title string) {
	t.Helper()
	dir := filepath.Join(homeRoot, "queens", queenID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "name: " + name + "\ntitle: " + title + "\n"
	if err := os.WriteFile(filepath.Join(dir, "profile.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testConfig is queen_mail on with two Hive queens and one Claude Code queen.
func testConfig() queenmail.Config {
	return queenmail.Config{
		Enabled:    true,
		Queens:     []string{ava, ivy},
		DailyLimit: queenmail.DefaultDailyLimit,
		MaxDepth:   queenmail.DefaultMaxDepth,
		ClaudeCode: map[string]queenmail.ClaudeQueen{
			claude: {ID: claude, Name: "Claude", Title: "Cartographer"},
		},
	}
}

// newQueenMailService builds the mail service and the store its Claude Code
// queens' inboxes live in, wired the way the server adapter wires them.
func newQueenMailService(t *testing.T, home hivehome.Home, config queenmail.Config, deliverer queenmail.HiveDeliverer) (*queenmail.Mail, *Store) {
	t.Helper()
	mail := queenmail.New(home, config, deliverer)
	store := NewStore(home)
	mail.SetClaudeInboxes(store)
	return mail, store
}
