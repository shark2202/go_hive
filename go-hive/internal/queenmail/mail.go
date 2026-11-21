package queenmail

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queen"
)

// LettersKept is how many return addresses are remembered (a reply goes back
// where the letter it answers came from).
const LettersKept = 2000

// HiveDeliverer is the port the server adapter fills: it hands a letter to a
// queen run by Hive, in her session with the writer.
type HiveDeliverer interface {
	// Deliver returns the id of the session the letter landed in.
	Deliver(ctx context.Context, toQueen, writer, fromRef string, letter Letter) (string, error)
	// DeliverReply returns the writer's session the reply landed in, or
	// "replyTo" when no live session could be found.
	DeliverReply(ctx context.Context, replyTo, queenID, writer string, letter Letter) (string, error)
}

// Mail is the queen-mail service behind the routes and, later, the
// message_queen tools. One per server.
type Mail struct {
	home      hivehome.Home
	config    Config
	store     *Store
	queue     *Queue
	claude    ClaudeInboxes
	deliverer HiveDeliverer
	secret    []byte

	// baseURL is where a Claude Code queen's runs reach this server; the serve
	// entry point sets it once it listens (upstream QueenMail.base_url).
	baseURL string
	// claudeRunner turns the letters waiting for a Claude Code queen into
	// `claude -p` turns. Nil leaves those letters waiting.
	claudeRunner ClaudeRunner

	mu          sync.Mutex
	day         string
	sent        map[string]int
	letters     map[string]string
	letterOrder []string
	routes      map[string]string
	causes      map[string]*Cause

	now   func() time.Time
	newID func() string
}

// New builds the service. A nil deliverer leaves delivery to Hive queens
// unavailable ("X can't take letters now"), which is what a build without a
// queen runtime reports rather than pretending the letter landed.
func New(home hivehome.Home, config Config, deliverer HiveDeliverer) *Mail {
	if config.DailyLimit < 1 {
		config.DailyLimit = DefaultDailyLimit
	}
	if config.MaxDepth < 1 {
		config.MaxDepth = DefaultMaxDepth
	}
	if config.MaxParallelTurns < 1 {
		config.MaxParallelTurns = DefaultMaxParallelTurns
	}
	if config.ClaudeCode == nil {
		config.ClaudeCode = map[string]ClaudeQueen{}
	}
	for queenID, queen := range config.ClaudeCode {
		queen.ID = queenID
		config.ClaudeCode[queenID] = normalizeClaudeQueen(queen)
	}
	store := NewStore(home.QueensDir())
	return &Mail{
		home:      home,
		config:    config,
		store:     store,
		queue:     NewQueue(store),
		deliverer: deliverer,
		secret:    newSecret(),
		sent:      map[string]int{},
		letters:   map[string]string{},
		routes:    map[string]string{},
		causes:    map[string]*Cause{},
		now:       time.Now,
		newID:     NewHopID,
	}
}

// Config is the configuration the service runs with.
func (m *Mail) Config() Config { return m.config }

// Store is the letter store the service writes through.
func (m *Mail) Store() *Store { return m.store }

// Queue is the pending-delivery queue.
func (m *Mail) Queue() *Queue { return m.queue }

// Claude is the conversations and inboxes of the Claude Code queens.
func (m *Mail) Claude() ClaudeInboxes { return m.claude }

// SetClaudeInboxes attaches the conversations and inboxes of the queens run
// by Claude Code: a letter to one of them lands in her conversation with the
// writer. Without it such a letter waits, as with a missing deliverer.
func (m *Mail) SetClaudeInboxes(inboxes ClaudeInboxes) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claude = inboxes
}

// SetBaseURL records where a Claude Code queen's runs reach this server, and
// catches up on letters already waiting for her. The serve entry point calls
// it once it listens; before that, a turn runs without the hive MCP server.
func (m *Mail) SetBaseURL(url string) {
	m.mu.Lock()
	m.baseURL = url
	runner := m.claudeRunner
	m.mu.Unlock()
	if runner != nil {
		runner.NotifyAll()
	}
}

// BaseURL is where a Claude Code queen's runs reach this server, or "" while
// there is no address.
func (m *Mail) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// SetClaudeRunner attaches the runner that turns the letters waiting for a
// Claude Code queen into `claude -p` turns.
func (m *Mail) SetClaudeRunner(runner ClaudeRunner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.claudeRunner = runner
}

// ClaudeRunner is the runner of the Claude Code queens, or nil when none is
// attached (letters then simply wait in her inbox).
func (m *Mail) ClaudeRunner() ClaudeRunner {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.claudeRunner
}

// Close stops the Claude Code queen runner: no more turns start, and the
// process trees of the running ones are killed. Safe on a nil Mail.
func (m *Mail) Close() {
	if m == nil {
		return
	}
	if runner := m.ClaudeRunner(); runner != nil {
		runner.Close()
	}
}

// notifyClaude tells the runner a letter for queenID has landed.
func (m *Mail) notifyClaude(queenID string) {
	if runner := m.ClaudeRunner(); runner != nil {
		runner.Notify(queenID)
	}
}

// TokenFor is the token that lets a Claude Code run write from conversation
// ref, and from it alone.
func (m *Mail) TokenFor(ref string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(ref))
	return hex.EncodeToString(mac.Sum(nil))
}

// Authorizes reports whether token is good for conversation ref.
func (m *Mail) Authorizes(ref, token string) bool {
	if ref == "" || token == "" {
		return false
	}
	return hmac.Equal([]byte(token), []byte(m.TokenFor(ref)))
}

// Correspondents lists the queens queenID may write to: every queen taking
// part but her.
func (m *Mail) Correspondents(queenID string) []map[string]any {
	out := []map[string]any{}
	if !m.config.Enabled {
		return out
	}
	for _, entry := range queen.ListQueens(m.home.QueensDir()) {
		id, _ := entry["id"].(string)
		if id == "" || id == queenID {
			continue
		}
		if _, claude := m.config.Claude(id); claude {
			continue
		}
		if !m.config.Includes(id) {
			continue
		}
		name, _ := entry["name"].(string)
		if name == "" {
			name = id
		}
		title, _ := entry["title"].(string)
		out = append(out, map[string]any{"id": id, "name": name, "title": title})
	}
	// Claude Code queens take part whatever `queens` says; keep their order
	// deterministic for the route's JSON.
	claudeIDs := make([]string, 0, len(m.config.ClaudeCode))
	for id := range m.config.ClaudeCode {
		if id != queenID {
			claudeIDs = append(claudeIDs, id)
		}
	}
	sort.Strings(claudeIDs)
	for _, id := range claudeIDs {
		entry := m.config.ClaudeCode[id]
		out = append(out, map[string]any{"id": entry.ID, "name": entry.Name, "title": entry.Title})
	}
	return out
}

// NameOf is queenID's name, as letters and replies show it.
func (m *Mail) NameOf(queenID string) string {
	if entry, ok := m.config.Claude(queenID); ok {
		return entry.Name
	}
	profile, err := queen.LoadQueenProfile(m.home.QueensDir(), queenID)
	if err != nil {
		return queenID
	}
	if name, ok := profile["name"].(string); ok && name != "" {
		return name
	}
	return queenID
}

// QueenOf names the queen behind a conversation ref, while she is configured.
func (m *Mail) QueenOf(ref string) (string, bool) {
	if _, configured := m.config.Claude(QueenIDOfRef(ref)); !configured {
		return "", false
	}
	if m.claude == nil {
		return "", false
	}
	return m.claude.QueenOf(ref)
}

// CauseOf is what set off the turn now queued in a conversation ref.
func (m *Mail) CauseOf(ref string) *Cause {
	if _, configured := m.config.Claude(QueenIDOfRef(ref)); !configured {
		return nil
	}
	if m.claude == nil {
		return nil
	}
	return m.claude.CauseOf(ref)
}

// SendFromConversation hands a letter from a Claude Code queen's conversation
// ref to toQueen.
func (m *Mail) SendFromConversation(ref, toQueen, message string) (map[string]any, error) {
	writer, ok := m.QueenOf(ref)
	if !ok {
		return nil, &Error{Message: "This conversation isn't known to Hive"}
	}
	cause := m.CauseOf(ref)
	return m.Send(writer, ref, toQueen, message, cause)
}

// Send hands text from the queen of writer (whose latest input came from
// fromRef) to toQueen.
func (m *Mail) Send(writer, fromRef, toQueen, text string, cause *Cause) (map[string]any, error) {
	text = strings.TrimSpace(text)
	takingPart := m.takingPart()
	if writer == "" || !takingPart[writer] {
		return nil, &Error{Message: "You don't take part in queen mail (queen_mail in configuration.json)"}
	}
	if text == "" {
		return nil, &Error{Message: "The message is empty"}
	}
	if toQueen == writer {
		return nil, &Error{Message: "That's your own id"}
	}
	if !takingPart[toQueen] {
		return nil, &Error{Message: "There's no queen " + pyReprString(toQueen) + " to write to; list_queens names them"}
	}
	causedBy, depth := causeHop(cause)
	if depth > m.config.MaxDepth {
		return nil, &Error{Message: "This chain of letters already has " + strconv.Itoa(m.config.MaxDepth) +
			" hops, the most queen_mail.max_depth allows. It starts again when someone else writes to you."}
	}
	if !m.reserve(writer) {
		return nil, &Error{Message: "You've sent " + strconv.Itoa(m.config.DailyLimit) +
			" letters today, the most queen_mail.daily_limit allows"}
	}

	name := m.NameOf(writer)
	hop := m.newID()
	m.rememberLetter(hop, fromRef)
	m.rememberRoute(toQueen, writer, fromRef)
	letter := Letter{
		ID:          hop,
		At:          Stamp(m.now()),
		Kind:        KindMessage,
		From:        writer,
		To:          toQueen,
		FromSession: fromRef,
		CausedBy:    causedBy,
		Depth:       depth,
		Chars:       Chars(text),
		Text:        text,
		FromName:    name,
		State:       StatePending,
	}
	if err := m.queue.Enqueue(letter); err != nil {
		m.release(writer)
		return nil, err
	}

	toRef, err := m.deliver(letter)
	if err != nil {
		m.release(writer)
		return nil, err
	}
	letter.ToSession = toRef
	letter.State = StateDelivered
	_ = m.store.Save(letter)
	_ = m.queue.Ack(hop)
	if err := m.store.AppendAudit(letter); err != nil {
		return nil, err
	}
	m.rememberCause(toRef, hop, depth)
	return map[string]any{"sent": hop, "to": toQueen, "depth": depth}, nil
}

// deliver routes one letter: a configured Claude Code queen gets it in her
// conversation (and inbox), a Hive queen through the deliverer.
func (m *Mail) deliver(letter Letter) (string, error) {
	if _, ok := m.config.Claude(letter.To); ok {
		if m.claude == nil {
			return "", &Error{Message: letter.To + " can't take letters now"}
		}
		ref, err := m.claude.DeliverLetter(letter.To, letter.From, letter.FromSession, letter)
		if err != nil {
			return "", err
		}
		m.claude.SetCause(ref, &Cause{Hop: letter.ID, Depth: letter.Depth})
		m.notifyClaude(letter.To)
		return ref, nil
	}
	if m.deliverer == nil {
		return "", &Error{Message: letter.To + " can't take letters now"}
	}
	toRef, err := m.deliverer.Deliver(context.Background(), letter.To, letter.From, letter.FromSession, letter)
	if err != nil {
		return "", err
	}
	return toRef, nil
}

// DeliverReply takes text, what queenID said in her conversation/session
// fromRef with writer, to replyTo. cause is the hop that set off what she said.
func (m *Mail) DeliverReply(replyTo, queenID, writer, fromRef, text string, cause *Cause) error {
	causedBy, depth := causeHop(cause)
	if depth > m.config.MaxDepth {
		m.logDropped(KindReply, queenID, writer, fromRef, replyTo, causedBy, depth, Chars(text), "max_depth")
		return nil
	}
	if !(m.config.Includes(queenID) && m.config.Includes(writer)) {
		m.logDropped(KindReply, queenID, writer, fromRef, replyTo, causedBy, depth, Chars(text), "off")
		return nil
	}
	if replyTo == "" {
		m.logDropped(KindReply, queenID, writer, fromRef, "", causedBy, depth, Chars(text), "unreachable")
		return nil
	}
	name := m.NameOf(queenID)
	hop := m.newID()
	letter := Letter{
		ID:          hop,
		At:          Stamp(m.now()),
		Kind:        KindReply,
		From:        queenID,
		To:          writer,
		FromSession: fromRef,
		ToSession:   replyTo,
		CausedBy:    causedBy,
		Depth:       depth,
		Chars:       Chars(text),
		Text:        text,
		FromName:    name,
	}
	if IsConversationRef(replyTo) {
		if m.claude == nil || !m.claude.DeliverReply(replyTo, letter) {
			m.logDropped(KindReply, queenID, writer, fromRef, replyTo, causedBy, depth, Chars(text), "unreachable")
			return nil
		}
		_ = m.store.AppendAudit(letter)
		m.notifyClaude(QueenIDOfRef(replyTo))
		return nil
	}
	if m.deliverer == nil {
		m.logDropped(KindReply, queenID, writer, fromRef, replyTo, causedBy, depth, Chars(text), "unavailable")
		return nil
	}
	toRef, err := m.deliverer.DeliverReply(context.Background(), replyTo, queenID, writer, letter)
	if err != nil {
		m.logDropped(KindReply, queenID, writer, fromRef, replyTo, causedBy, depth, Chars(text), "unavailable")
		return nil
	}
	letter.ToSession = toRef
	_ = m.store.AppendAudit(letter)
	m.rememberCause(toRef, hop, depth)
	return nil
}

// Flush retries the letters still waiting for delivery. A letter stays queued
// when its queen is still unavailable or today's limit has no room; the
// returned count is what landed now.
func (m *Mail) Flush(ctx context.Context) (int, error) {
	delivered := 0
	var firstErr error
	for _, letter := range m.queue.Letters() {
		if letter.Kind != KindMessage {
			continue
		}
		// The letter landed but the queue acknowledgement did not; repair the
		// bookkeeping instead of handing the target the same letter twice.
		if letter.State == StateDelivered {
			_ = m.queue.Ack(letter.ID)
			continue
		}
		if !m.reserve(letter.From) {
			if firstErr == nil {
				firstErr = &Error{Message: "You've sent " + strconv.Itoa(m.config.DailyLimit) +
					" letters today, the most queen_mail.daily_limit allows"}
			}
			continue
		}
		toRef, err := m.deliver(letter)
		if err != nil {
			m.release(letter.From)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		letter.ToSession = toRef
		letter.State = StateDelivered
		_ = m.store.Save(letter)
		_ = m.queue.Ack(letter.ID)
		if err := m.store.AppendAudit(letter); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		m.rememberCause(toRef, letter.ID, letter.Depth)
		delivered++
	}
	return delivered, firstErr
}

// takingPart is the set of queens who may write and be written to.
func (m *Mail) takingPart() map[string]bool {
	out := map[string]bool{}
	for _, entry := range m.Correspondents("") {
		if id, ok := entry["id"].(string); ok {
			out[id] = true
		}
	}
	return out
}

func (m *Mail) reserve(queenID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	today := m.now().Format("2006-01-02")
	if today != m.day {
		m.day = today
		m.sent = m.store.DailyCounts(today)
	}
	if m.sent[queenID] >= m.config.DailyLimit {
		return false
	}
	m.sent[queenID]++
	return true
}

func (m *Mail) release(queenID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sent[queenID] > 0 {
		m.sent[queenID]--
	}
}

func (m *Mail) rememberRoute(queenID, writer, fromRef string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[threadKey(queenID, writer)] = fromRef
}

func (m *Mail) rememberLetter(hop, fromRef string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.letters[hop] = fromRef
	m.letterOrder = append(m.letterOrder, hop)
	for len(m.letterOrder) > LettersKept {
		oldest := m.letterOrder[0]
		m.letterOrder = m.letterOrder[1:]
		delete(m.letters, oldest)
	}
}

func (m *Mail) rememberCause(ref, hop string, depth int) {
	if ref == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.causes[ref] = &Cause{Hop: hop, Depth: depth}
}

func (m *Mail) logDropped(kind, sender, recipient, fromRef, toRef string, causedBy *string, depth, chars int, reason string) {
	letter := Letter{
		ID:          m.newID(),
		At:          Stamp(m.now()),
		Kind:        kind,
		From:        sender,
		To:          recipient,
		FromSession: fromRef,
		ToSession:   toRef,
		CausedBy:    causedBy,
		Depth:       depth,
		Chars:       chars,
		Dropped:     reason,
	}
	_ = m.store.AppendAudit(letter)
}

// Error is a letter that can't be sent; the message says why, to the queen who
// tried. The server route maps it to a 400.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func newSecret() []byte {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		// A clock-derived secret still authenticates within one process; it is
		// weaker but never silently empty.
		sum := sha256.Sum256([]byte(time.Now().Format(time.RFC3339Nano)))
		return sum[:]
	}
	return secret
}

func causeHop(cause *Cause) (*string, int) {
	if cause == nil || cause.Hop == "" {
		return nil, 1
	}
	hop := cause.Hop
	return &hop, cause.Depth + 1
}

func threadKey(queenID, writer string) string { return queenID + "\x00" + writer }

// CauseFor is the hop that set off what was last delivered into ref, if any.
func (m *Mail) CauseFor(ref string) *Cause {
	m.mu.Lock()
	defer m.mu.Unlock()
	cause := m.causes[ref]
	if cause == nil {
		return nil
	}
	copied := *cause
	return &copied
}

// ForgetCause starts a new chain in ref: a person, a timer or a worker report
// spoke there, so the next letter sent from it is hop 1 again.
//
// Parity: framework/server/queen_mail.py::QueenMail._watch (the else branch)
func (m *Mail) ForgetCause(ref string) {
	if m == nil || ref == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.causes, ref)
}

// RouteOf is where what queenID says in her thread with writer goes when no
// newer letter says otherwise.
func (m *Mail) RouteOf(queenID, writer string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.routes[threadKey(queenID, writer)]
}

// ReplyTarget is where what queenID says in sessionID goes: the session the
// letter or reply her words answer came from, else the latest return address of
// her thread with writer. "" means there is no known way back.
//
// Parity: framework/server/queen_mail.py::QueenMail._reply (reply_to lookup)
func (m *Mail) ReplyTarget(queenID, writer, sessionID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cause := m.causes[sessionID]; cause != nil {
		if from := m.letters[cause.Hop]; from != "" {
			return from
		}
	}
	return m.routes[threadKey(queenID, writer)]
}
