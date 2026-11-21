// Package claudecode runs Claude Code queens: the `claude -p` turn runner,
// transcript capture, conversation store and process-tree helpers.
//
// It consumes the mailbox (internal/queenmail) through the ClaudeInboxes /
// ClaudeRunner interfaces; the server adapter is the single wiring point, so
// this package never imports the transport layer.
package claudecode
