package qmail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

// Conversation is one Claude Code queen's conversation with a writer: her
// Claude Code session id, who she is writing with, and where what she says
// goes (the writer's latest letter or session).
type Conversation struct {
	Ref       string
	QueenID   string
	SessionID string
	Peer      string
	ReplyTo   string
	Started   bool
	// Cause is what set off the turn now queued in this conversation; only in
	// memory, as upstream keeps it.
	Cause *Cause
}

// storedConversation keeps pointers for peer/reply_to so a missing field is
// told apart from an empty one, as upstream's isinstance checks do.
type storedConversation struct {
	Peer    *string `json:"peer"`
	ReplyTo *string `json:"reply_to"`
	Started bool    `json:"started"`
}

type storedConversations struct {
	Conversations map[string]storedConversation `json:"conversations"`
}

// ClaudeStore keeps the conversations of the queens run by Claude Code, and the
// letters each has waiting for her next turn. It mirrors
// framework/server/claude_code_queens.py's on-disk record
// (HIVE_HOME/claude_code/<queen>/conversations.json), so conversations written
// by the Python server stay readable.
type ClaudeStore struct {
	home          hivehome.Home
	mu            sync.Mutex
	loaded        map[string]bool
	conversations map[string]*Conversation
}

// NewClaudeStore roots the conversation records at a Hive home.
func NewClaudeStore(home hivehome.Home) *ClaudeStore {
	return &ClaudeStore{
		home:          home,
		loaded:        map[string]bool{},
		conversations: map[string]*Conversation{},
	}
}

// validClaudePathSegment keeps an id that becomes one directory or file name
// (a queen id or a Claude Code session id) to a single path segment.
func validClaudePathSegment(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\\x00")
}

// Home is one queen's Claude Code folder: HIVE_HOME/claude_code/<queen id>.
func (c *ClaudeStore) Home(queenID string) string {
	if !validClaudePathSegment(queenID) {
		return filepath.Join(c.home.Root, "claude_code", "__invalid__")
	}
	return filepath.Join(c.home.Root, "claude_code", queenID)
}

// Inbox is where letters to a queen wait for a run of hers.
func (c *ClaudeStore) Inbox(queenID string) string {
	return filepath.Join(c.Home(queenID), "inbox")
}

// InboxFile lists the hops waiting for a queen's next turn.
func (c *ClaudeStore) InboxFile(queenID string) string {
	return filepath.Join(c.Home(queenID), "inbox.jsonl")
}

// Load reads one queen's conversations.json once. A malformed or absent file
// leaves her with no conversations, which the routes answer 404 for.
func (c *ClaudeStore) Load(queenID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked(queenID)
}

func (c *ClaudeStore) loadLocked(queenID string) {
	if c.loaded[queenID] {
		return
	}
	c.loaded[queenID] = true
	data, err := os.ReadFile(filepath.Join(c.Home(queenID), "conversations.json"))
	if err != nil {
		return
	}
	var stored storedConversations
	if err := json.Unmarshal(data, &stored); err != nil {
		return
	}
	for sessionID, entry := range stored.Conversations {
		if sessionID == "" || entry.Peer == nil || entry.ReplyTo == nil {
			continue
		}
		conversation := &Conversation{
			Ref:       ConversationRef(queenID, sessionID),
			QueenID:   queenID,
			SessionID: sessionID,
			Peer:      *entry.Peer,
			ReplyTo:   *entry.ReplyTo,
			Started:   entry.Started,
		}
		if _, exists := c.conversations[conversation.Ref]; !exists {
			c.conversations[conversation.Ref] = conversation
		}
	}
}

// Save writes one queen's conversations.json back, atomically.
func (c *ClaudeStore) Save(queenID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(queenID)
}

func (c *ClaudeStore) saveLocked(queenID string) error {
	stored := storedConversations{Conversations: map[string]storedConversation{}}
	for _, conversation := range c.conversations {
		if conversation.QueenID != queenID {
			continue
		}
		peer, replyTo := conversation.Peer, conversation.ReplyTo
		stored.Conversations[conversation.SessionID] = storedConversation{
			Peer:    &peer,
			ReplyTo: &replyTo,
			Started: conversation.Started,
		}
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.Home(queenID), "conversations.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'))
}

// Find resolves a conversation ref, loading the queen's record on first use.
// Unknown refs and refs to queens no longer configured resolve false.
func (c *ClaudeStore) Find(ref string) (*Conversation, bool) {
	if !IsConversationRef(ref) {
		return nil, false
	}
	queenID := QueenIDOfRef(ref)
	if queenID == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded[queenID] {
		conversation, ok := c.conversations[ref]
		return conversation, ok
	}
	c.loadLocked(queenID)
	conversation, ok := c.conversations[ref]
	return conversation, ok
}

// CauseOf is what set off the turn now queued in ref, read under the store's
// lock because delivery updates it.
func (c *ClaudeStore) CauseOf(ref string) *Cause {
	c.mu.Lock()
	defer c.mu.Unlock()
	if queenID := QueenIDOfRef(ref); queenID != "" {
		c.loadLocked(queenID)
	}
	conversation, ok := c.conversations[ref]
	if !ok || conversation.Cause == nil {
		return nil
	}
	cause := *conversation.Cause
	return &cause
}

// QueenOf names the queen behind a conversation ref while she is configured.
func (c *ClaudeStore) QueenOf(ref string) (string, bool) {
	conversation, ok := c.Find(ref)
	if !ok {
		return "", false
	}
	return conversation.QueenID, true
}

// Letter queues a letter from writer in queenID's conversation with writer,
// opening one if this is their first letter, and returns that conversation.
// Her answer goes to replyTo until a newer letter moves it.
func (c *ClaudeStore) Letter(queenID, writer, replyTo string, letter Letter) (*Conversation, error) {
	c.mu.Lock()
	c.loadLocked(queenID)
	var conversation *Conversation
	for _, candidate := range c.conversations {
		if candidate.QueenID == queenID && candidate.Peer == writer {
			conversation = candidate
			break
		}
	}
	if conversation == nil {
		conversation = &Conversation{
			QueenID:   queenID,
			SessionID: NewSessionID(),
			Peer:      writer,
			ReplyTo:   replyTo,
		}
		conversation.Ref = ConversationRef(queenID, conversation.SessionID)
		c.conversations[conversation.Ref] = conversation
	}
	conversation.ReplyTo = replyTo
	if err := c.saveLocked(queenID); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	letter.ToSession = conversation.Ref
	letter.State = StateStored
	err := c.enqueueLocked(queenID, letter)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return conversation, nil
}

// Reply queues a reply from another queen in the conversation ref names.
func (c *ClaudeStore) Reply(ref string, letter Letter) bool {
	conversation, ok := c.Find(ref)
	if !ok {
		return false
	}
	c.mu.Lock()
	letter.ToSession = ref
	letter.State = StateStored
	err := c.enqueueLocked(conversation.QueenID, letter)
	c.mu.Unlock()
	return err == nil
}

func (c *ClaudeStore) enqueueLocked(queenID string, letter Letter) error {
	inbox := c.Inbox(queenID)
	if err := os.MkdirAll(inbox, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(letter)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(inbox, letter.ID+".json"), append(data, '\n')); err != nil {
		return err
	}
	file, err := os.OpenFile(c.InboxFile(queenID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write([]byte(letter.ID + "\n"))
	return err
}

// Pending lists the letters waiting for one queen's next turn, oldest first.
func (c *ClaudeStore) Pending(queenID string) []Letter {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pendingLocked(queenID)
}

// pendingLocked reads the letters one queen's inbox file names; the caller
// holds the store lock. Missing files and unreadable lines are skipped.
func (c *ClaudeStore) pendingLocked(queenID string) []Letter {
	data, err := os.ReadFile(c.InboxFile(queenID))
	if err != nil {
		return nil
	}
	var letters []Letter
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		if !validLetterID(line) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(c.Inbox(queenID), line+".json"))
		if err != nil {
			continue
		}
		var letter Letter
		if err := json.Unmarshal(raw, &letter); err != nil {
			continue
		}
		letters = append(letters, letter)
	}
	return letters
}

// TakePending removes and returns the letters waiting in conversation ref, in
// the order they arrived: one turn's batch. Letters for other conversations of
// the same queen stay in the inbox.
func (c *ClaudeStore) TakePending(ref string) []Letter {
	queenID := QueenIDOfRef(ref)
	if queenID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked(queenID)
	var taken, kept []Letter
	for _, letter := range c.pendingLocked(queenID) {
		if letter.ToSession == ref {
			taken = append(taken, letter)
			continue
		}
		kept = append(kept, letter)
	}
	if len(taken) == 0 {
		return nil
	}
	c.removePendingLocked(queenID, taken, kept)
	return taken
}

// removePendingLocked deletes the taken letters and rewrites the inbox file
// with the ones still waiting. The caller holds the store lock.
func (c *ClaudeStore) removePendingLocked(queenID string, taken, kept []Letter) {
	for _, letter := range taken {
		_ = os.Remove(filepath.Join(c.Inbox(queenID), letter.ID+".json"))
	}
	var body strings.Builder
	for _, letter := range kept {
		body.WriteString(letter.ID)
		body.WriteString("\n")
	}
	_ = writeFileAtomic(c.InboxFile(queenID), []byte(body.String()))
}

// HasPending reports whether conversation ref has letters waiting.
func (c *ClaudeStore) HasPending(ref string) bool {
	queenID := QueenIDOfRef(ref)
	if queenID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, letter := range c.pendingLocked(queenID) {
		if letter.ToSession == ref {
			return true
		}
	}
	return false
}

// PendingRefs lists the conversations of one queen with letters waiting, in
// the order the first of each arrived.
func (c *ClaudeStore) PendingRefs(queenID string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var refs []string
	seen := map[string]bool{}
	for _, letter := range c.pendingLocked(queenID) {
		if letter.ToSession == "" || seen[letter.ToSession] {
			continue
		}
		seen[letter.ToSession] = true
		refs = append(refs, letter.ToSession)
	}
	return refs
}

// SetCause records what set off the turn now running in ref (the hop a letter
// she writes in it follows). Only in memory, as upstream keeps it.
func (c *ClaudeStore) SetCause(ref string, cause *Cause) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if queenID := QueenIDOfRef(ref); queenID != "" {
		c.loadLocked(queenID)
	}
	if conversation, ok := c.conversations[ref]; ok {
		conversation.Cause = cause
	}
}

// SetStarted records whether her Claude Code session exists, and writes the
// conversations record so a restart resumes rather than starts again.
func (c *ClaudeStore) SetStarted(ref string, started bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if queenID := QueenIDOfRef(ref); queenID != "" {
		c.loadLocked(queenID)
	}
	conversation, ok := c.conversations[ref]
	if !ok {
		return
	}
	conversation.Started = started
	_ = c.saveLocked(conversation.QueenID)
}

// Conversations lists a queen's conversations, newest session id first.
func (c *ClaudeStore) Conversations(queenID string) []*Conversation {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadLocked(queenID)
	var out []*Conversation
	for _, conversation := range c.conversations {
		if conversation.QueenID == queenID {
			out = append(out, conversation)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID > out[j].SessionID })
	return out
}
