package claudecode

// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens
//
// A queen run by Claude Code takes part in queen mail: a letter to her runs a
// turn of `claude -p` in a conversation of hers kept for its writer, started
// with --session-id and resumed with --resume from then on. Input that arrives
// while a turn runs waits and goes in together as the next turn; what the turn
// ends with goes back the way a Hive queen's words do.
//
// The conversations themselves (HIVE_HOME/claude_code/<id>/conversations.json)
// live in Store; this file turns the letters waiting in her inbox into
// turns, at most queen_mail.max_parallel_turns at a time, each in its own
// process tree.

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/queenmail"
)

// claudeMCPTools are the tools of the `hive` MCP server, as Claude Code names
// them.
var claudeMCPTools = []string{"mcp__hive__list_queens", "mcp__hive__message_queen"}

// Runner runs the turns of the queens configured under
// queen_mail.claude_code. One per server; attached to the Mail service with
// Mail.SetClaudeRunner and paired with the Store its inboxes live in.
type Runner struct {
	mail *queenmail.Mail
	// store is where her conversations and inboxes live: the same Store Mail
	// delivers letters through.
	store *Store

	mu      sync.Mutex
	running map[string]bool
	closed  bool
	warned  map[string]bool
	procs   map[*exec.Cmd]bool
	procsMu sync.Mutex
	// limit holds one slot per turn that may run at once
	// (queen_mail.max_parallel_turns).
	limit chan struct{}

	// turnTimeout is one turn's clock; a field so tests can shorten it.
	turnTimeout func(queenmail.ClaudeQueen) time.Duration
	// observe sees every run's raw outcome (tests record stderr and cost).
	observe func(claudeTurnRun)
	logf    func(format string, args ...any)
}

// Runner implements queenmail.ClaudeRunner: Mail wakes it when a letter lands.
var _ queenmail.ClaudeRunner = (*Runner)(nil)

// NewRunner builds the runner for a queen-mail service and the store holding
// the configured queens' conversations and inboxes.
func NewRunner(mail *queenmail.Mail, store *Store) *Runner {
	workers := queenmail.DefaultMaxParallelTurns
	if mail != nil && mail.Config().MaxParallelTurns > 0 {
		workers = mail.Config().MaxParallelTurns
	}
	return &Runner{
		mail:    mail,
		store:   store,
		running: map[string]bool{},
		warned:  map[string]bool{},
		procs:   map[*exec.Cmd]bool{},
		limit:   make(chan struct{}, workers),
		turnTimeout: func(queen queenmail.ClaudeQueen) time.Duration {
			return time.Duration(queen.TimeoutMinutes) * time.Minute
		},
		logf: log.Printf,
	}
}

// Notify checks whether queenID has letters waiting and starts a turn for each
// conversation that has none running. It returns at once.
func (r *Runner) Notify(queenID string) {
	if r == nil || r.mail == nil || queenID == "" {
		return
	}
	if _, configured := r.mail.Config().Claude(queenID); !configured {
		return
	}
	for _, ref := range r.store.PendingRefs(queenID) {
		r.kick(ref)
	}
}

// NotifyAll starts turns for every waiting letter of every configured Claude
// Code queen: what SetBaseURL and a fresh runner use to catch up.
func (r *Runner) NotifyAll() {
	if r == nil || r.mail == nil {
		return
	}
	for queenID := range r.mail.Config().ClaudeCode {
		r.Notify(queenID)
	}
}

// kick starts one conversation's worker unless it is already running.
func (r *Runner) kick(ref string) {
	r.mu.Lock()
	if r.closed || r.running[ref] {
		r.mu.Unlock()
		return
	}
	r.running[ref] = true
	r.mu.Unlock()
	go r.work(ref)
}

// work runs her turns until nothing waits. What came in during a turn waits
// for the next one and goes in together.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._work
func (r *Runner) work(ref string) {
	for {
		batch := r.store.TakePending(ref)
		if len(batch) == 0 {
			r.mu.Lock()
			// Re-check under the lock Notify's kick takes: a letter that
			// arrived between the take and here must not be left behind.
			if !r.closed && r.store.HasPending(ref) {
				r.mu.Unlock()
				continue
			}
			delete(r.running, ref)
			r.mu.Unlock()
			return
		}
		if r.stopped() {
			return
		}
		conversation, ok := r.store.Find(ref)
		if !ok {
			r.logf("claude code queen: no conversation record for %s; %d letter(s) dropped", ref, len(batch))
			continue
		}
		r.runBatch(conversation, batch)
		if r.stopped() {
			return
		}
	}
}

// runBatch runs one turn on the letters taken together, then delivers what she
// ended it with to the writer the last letter came from.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens._work (the
// batch's cause, route and reply hand-off)
func (r *Runner) runBatch(conversation *Conversation, batch []queenmail.Letter) {
	cause := batchCause(batch)
	r.store.SetCause(conversation.Ref, cause)
	prompt := batchPrompt(batch)
	r.acquire()
	if r.stopped() {
		r.release()
		return
	}
	text := r.turn(conversation, prompt)
	r.release()
	if r.stopped() || text == "" {
		return
	}
	route := batchRoute(batch, conversation)
	if err := r.mail.DeliverReply(route, conversation.QueenID, conversation.Peer, conversation.Ref, text, cause); err != nil {
		r.logf("claude code queen: couldn't deliver what %s said: %v", conversation.QueenID, err)
	}
}

func (r *Runner) acquire() {
	if r.limit != nil {
		r.limit <- struct{}{}
	}
}

func (r *Runner) release() {
	if r.limit != nil {
		<-r.limit
	}
}

// Close stops the runner: no more turns start, and the process tree of every
// running turn is killed.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens.close
func (r *Runner) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	already := r.closed
	r.closed = true
	r.mu.Unlock()
	if already {
		return
	}
	r.procsMu.Lock()
	running := make([]*exec.Cmd, 0, len(r.procs))
	for command := range r.procs {
		running = append(running, command)
	}
	r.procsMu.Unlock()
	for _, command := range running {
		killProcessTree(command)
	}
}

func (r *Runner) stopped() bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// Running reports whether a turn of hers is running in conversation ref now —
// the dashboard's "running" dot for that conversation.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens.conversations
func (r *Runner) Running(ref string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[ref]
}

// warnOnce reports whether this is the first warning for queenID.
func (r *Runner) warnOnce(queenID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warned[queenID] {
		return false
	}
	r.warned[queenID] = true
	return true
}

func (r *Runner) track(command *exec.Cmd) {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	if r.procs == nil {
		r.procs = map[*exec.Cmd]bool{}
	}
	r.procs[command] = true
}

func (r *Runner) untrack(command *exec.Cmd) {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	delete(r.procs, command)
}

// trackedCount is how many turns' process trees are running (tests and a
// graceful stop watch it).
func (r *Runner) trackedCount() int {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	return len(r.procs)
}

// batchPrompt is what one turn is handed: the letters' texts, in order, with
// the way a Hive queen reads them.
func batchPrompt(batch []queenmail.Letter) string {
	parts := make([]string, 0, len(batch))
	for _, letter := range batch {
		if letter.Kind == queenmail.KindReply {
			parts = append(parts, queenmail.ReplyText(letter.FromName, letter.From, letter.Text))
			continue
		}
		parts = append(parts, queenmail.LetterText(letter.FromName, letter.From, letter.Text))
	}
	return strings.Join(parts, "\n\n")
}

// batchCause is what set off the turn: the newest letter's hop, and how deep
// the chain had come.
func batchCause(batch []queenmail.Letter) *queenmail.Cause {
	deepest := 0
	for _, letter := range batch {
		if letter.Depth > deepest {
			deepest = letter.Depth
		}
	}
	return &queenmail.Cause{Hop: batch[len(batch)-1].ID, Depth: deepest}
}

// batchRoute is where her words go: the writer's session the newest letter in
// the batch came from, else the conversation's latest return address.
func batchRoute(batch []queenmail.Letter, conversation *Conversation) string {
	for index := len(batch) - 1; index >= 0; index-- {
		if batch[index].FromSession != "" {
			return batch[index].FromSession
		}
	}
	return conversation.ReplyTo
}
