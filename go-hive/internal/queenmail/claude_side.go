package queenmail

// The queens run by Claude Code live in internal/claudecode: that package
// owns their conversations, their inboxes and the `claude -p` turns. The mail
// engine still delivers to them and wakes their runner, but it must not import
// that package (which imports this one for Letter, Cause and the ref helpers).
// These two interfaces are the boundary; the server adapter wires a
// claudecode.Store and claudecode.Runner in with SetClaudeInboxes and
// SetClaudeRunner.

// ClaudeInboxes is the conversation-and-inbox side of the queens run by Claude
// Code, as the mail engine uses it: a letter to one is queued in her
// conversation with the writer, and a reply to her goes back into the same
// conversation.
type ClaudeInboxes interface {
	// DeliverLetter queues letter in queenID's conversation with writer
	// (opening one if this is their first letter) and returns the ref of that
	// conversation.
	DeliverLetter(queenID, writer, replyTo string, letter Letter) (string, error)
	// DeliverReply queues a reply in conversation ref; false when ref names
	// no open conversation.
	DeliverReply(ref string, letter Letter) bool
	// QueenOf names the queen behind a conversation ref.
	QueenOf(ref string) (string, bool)
	// CauseOf is what set off the turn now queued in ref.
	CauseOf(ref string) *Cause
	// SetCause records what set off the turn now running in ref.
	SetCause(ref string, cause *Cause)
}

// ClaudeRunner is the runner of the queens run by Claude Code, as the mail
// engine uses it: waking it when a letter lands, catching up once the server
// has an address, and stopping it when the process ends.
type ClaudeRunner interface {
	// Notify starts turns for the letters waiting for queenID.
	Notify(queenID string)
	// NotifyAll catches up on the waiting letters of every configured queen.
	NotifyAll()
	// Close stops the runner and kills the running turns' process trees.
	Close()
}
