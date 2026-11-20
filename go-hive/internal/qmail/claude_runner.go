package qmail

// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens
//
// A queen run by Claude Code takes part in queen mail: a letter to her runs a
// turn of `claude -p` in a conversation of hers kept for its writer, started
// with --session-id and resumed with --resume from then on. Input that arrives
// while a turn runs waits and goes in together as the next turn; what the turn
// ends with goes back the way a Hive queen's words do.
//
// The conversations themselves (HIVE_HOME/claude_code/<id>/conversations.json)
// live in ClaudeStore; this file turns the letters waiting in her inbox into
// turns, at most queen_mail.max_parallel_turns at a time, each in its own
// process tree.

import (
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// claudeMCPTools are the tools of the `hive` MCP server, as Claude Code names
// them.
var claudeMCPTools = []string{"mcp__hive__list_queens", "mcp__hive__message_queen"}

// ClaudeRunner runs the turns of the queens configured under
// queen_mail.claude_code. One per server; attached to the Mail service with
// Mail.SetClaudeRunner.
type ClaudeRunner struct {
	mail *Mail

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
	turnTimeout func(ClaudeQueen) time.Duration
	// observe sees every run's raw outcome (tests record stderr and cost).
	observe func(claudeTurnRun)
	logf    func(format string, args ...any)
}

// NewClaudeRunner builds the runner for a queen-mail service.
func NewClaudeRunner(mail *Mail) *ClaudeRunner {
	workers := DefaultMaxParallelTurns
	if mail != nil && mail.Config().MaxParallelTurns > 0 {
		workers = mail.Config().MaxParallelTurns
	}
	return &ClaudeRunner{
		mail:        mail,
		running:     map[string]bool{},
		warned:      map[string]bool{},
		procs:       map[*exec.Cmd]bool{},
		limit:       make(chan struct{}, workers),
		turnTimeout: func(queen ClaudeQueen) time.Duration { return time.Duration(queen.TimeoutMinutes) * time.Minute },
		logf:        log.Printf,
	}
}

// Notify checks whether queenID has letters waiting and starts a turn for each
// conversation that has none running. It returns at once.
func (r *ClaudeRunner) Notify(queenID string) {
	if r == nil || r.mail == nil || queenID == "" {
		return
	}
	if _, configured := r.mail.Config().Claude(queenID); !configured {
		return
	}
	for _, ref := range r.mail.Claude().PendingRefs(queenID) {
		r.kick(ref)
	}
}

// NotifyAll starts turns for every waiting letter of every configured Claude
// Code queen: what SetBaseURL and a fresh runner use to catch up.
func (r *ClaudeRunner) NotifyAll() {
	if r == nil || r.mail == nil {
		return
	}
	for queenID := range r.mail.Config().ClaudeCode {
		r.Notify(queenID)
	}
}

// kick starts one conversation's worker unless it is already running.
func (r *ClaudeRunner) kick(ref string) {
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
func (r *ClaudeRunner) work(ref string) {
	for {
		batch := r.mail.Claude().TakePending(ref)
		if len(batch) == 0 {
			r.mu.Lock()
			// Re-check under the lock Notify's kick takes: a letter that
			// arrived between the take and here must not be left behind.
			if !r.closed && r.mail.Claude().HasPending(ref) {
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
		conversation, ok := r.mail.Claude().Find(ref)
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
func (r *ClaudeRunner) runBatch(conversation *Conversation, batch []Letter) {
	cause := batchCause(batch)
	r.mail.Claude().SetCause(conversation.Ref, cause)
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

func (r *ClaudeRunner) acquire() {
	if r.limit != nil {
		r.limit <- struct{}{}
	}
}

func (r *ClaudeRunner) release() {
	if r.limit != nil {
		<-r.limit
	}
}

// Close stops the runner: no more turns start, and the process tree of every
// running turn is killed.
//
// Parity: framework/server/claude_code_queens.py::ClaudeCodeQueens.close
func (r *ClaudeRunner) Close() {
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

func (r *ClaudeRunner) stopped() bool {
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
func (r *ClaudeRunner) Running(ref string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running[ref]
}

// warnOnce reports whether this is the first warning for queenID.
func (r *ClaudeRunner) warnOnce(queenID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warned[queenID] {
		return false
	}
	r.warned[queenID] = true
	return true
}

func (r *ClaudeRunner) track(command *exec.Cmd) {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	if r.procs == nil {
		r.procs = map[*exec.Cmd]bool{}
	}
	r.procs[command] = true
}

func (r *ClaudeRunner) untrack(command *exec.Cmd) {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	delete(r.procs, command)
}

// trackedCount is how many turns' process trees are running (tests and a
// graceful stop watch it).
func (r *ClaudeRunner) trackedCount() int {
	r.procsMu.Lock()
	defer r.procsMu.Unlock()
	return len(r.procs)
}

// batchPrompt is what one turn is handed: the letters' texts, in order, with
// the way a Hive queen reads them.
func batchPrompt(batch []Letter) string {
	parts := make([]string, 0, len(batch))
	for _, letter := range batch {
		if letter.Kind == KindReply {
			parts = append(parts, ReplyText(letter.FromName, letter.From, letter.Text))
			continue
		}
		parts = append(parts, LetterText(letter.FromName, letter.From, letter.Text))
	}
	return strings.Join(parts, "\n\n")
}

// batchCause is what set off the turn: the newest letter's hop, and how deep
// the chain had come.
func batchCause(batch []Letter) *Cause {
	deepest := 0
	for _, letter := range batch {
		if letter.Depth > deepest {
			deepest = letter.Depth
		}
	}
	return &Cause{Hop: batch[len(batch)-1].ID, Depth: deepest}
}

// batchRoute is where her words go: the writer's session the newest letter in
// the batch came from, else the conversation's latest return address.
func batchRoute(batch []Letter, conversation *Conversation) string {
	for index := len(batch) - 1; index >= 0; index-- {
		if batch[index].FromSession != "" {
			return batch[index].FromSession
		}
	}
	return conversation.ReplyTo
}
