package qmail

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// Letter kinds and delivery states.
const (
	KindMessage = "message"
	KindReply   = "reply"

	StatePending   = "pending"
	StateDelivered = "delivered"
	StateStored    = "stored"
)

// Letter is one hop between queens. The audit log keeps only its metadata; the
// store keeps the text so a queued letter survives a restart.
type Letter struct {
	ID          string  `json:"id"`
	At          string  `json:"at"`
	Kind        string  `json:"kind"`
	From        string  `json:"from"`
	To          string  `json:"to"`
	FromSession string  `json:"from_session"`
	ToSession   string  `json:"to_session"`
	CausedBy    *string `json:"caused_by"`
	Depth       int     `json:"depth"`
	Chars       int     `json:"chars"`
	Dropped     string  `json:"dropped,omitempty"`
	Text        string  `json:"text,omitempty"`
	FromName    string  `json:"from_name,omitempty"`
	State       string  `json:"state,omitempty"`
}

// Cause is the hop that set off something a queen writes next: its id and how
// deep the chain had come.
type Cause struct {
	Hop   string
	Depth int
}

// auditRecord is the mail.jsonl line, in the field order upstream writes.
type auditRecord struct {
	ID          string  `json:"id"`
	At          string  `json:"at"`
	Kind        string  `json:"kind"`
	From        string  `json:"from"`
	To          string  `json:"to"`
	FromSession string  `json:"from_session"`
	ToSession   string  `json:"to_session"`
	CausedBy    *string `json:"caused_by"`
	Depth       int     `json:"depth"`
	Chars       int     `json:"chars"`
	Dropped     string  `json:"dropped,omitempty"`
}

func (l Letter) auditRecord() auditRecord {
	return auditRecord{
		ID:          l.ID,
		At:          l.At,
		Kind:        l.Kind,
		From:        l.From,
		To:          l.To,
		FromSession: l.FromSession,
		ToSession:   l.ToSession,
		CausedBy:    l.CausedBy,
		Depth:       l.Depth,
		Chars:       l.Chars,
		Dropped:     l.Dropped,
	}
}

func letterFromAudit(record auditRecord) Letter {
	return Letter{
		ID:          record.ID,
		At:          record.At,
		Kind:        record.Kind,
		From:        record.From,
		To:          record.To,
		FromSession: record.FromSession,
		ToSession:   record.ToSession,
		CausedBy:    record.CausedBy,
		Depth:       record.Depth,
		Chars:       record.Chars,
		Dropped:     record.Dropped,
	}
}

// NewHopID mints the id of one hop: "hop_" and twelve hex characters, as
// upstream's _hop_id does.
func NewHopID() string {
	buffer := make([]byte, 6)
	if _, err := rand.Read(buffer); err != nil {
		return "hop_" + time.Now().Format("150405.000000")
	}
	return "hop_" + hex.EncodeToString(buffer)
}

// NewSessionID mints a UUIDv4 string, the conversation id a Claude Code queen's
// conversation is named by.
func NewSessionID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(time.Now().Format("20060102-150405.000000000"), ".", "-")
	}
	buffer[6] = (buffer[6] & 0x0f) | 0x40
	buffer[8] = (buffer[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buffer)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// Stamp renders the audit log's `at`: local time, to the second, with the
// numeric offset isoformat(timespec="seconds") produces.
func Stamp(now time.Time) string {
	return now.Format("2006-01-02T15:04:05-07:00")
}

// Chars counts text the way Python's len(str) does: code points, not bytes.
func Chars(text string) int {
	return utf8.RuneCountInString(text)
}

// Said is how a queen's words appear to another: the "[name(id)]: text" a chat
// app's message has. A "[...]:" inside the text is broken up so the words can't
// be credited to someone else.
func Said(name, queenID, text string) string {
	return "[" + name + "(" + queenID + ")]: " + strings.ReplaceAll(text, "]:", "] :")
}

// LetterText is what a queen reads when a letter is handed to her.
func LetterText(name, queenID, text string) string {
	return "(A letter from " + name + ", another queen, sent with message_queen. What you say in this " +
		"conversation goes back to her, so just answer here; you don't need message_queen to reply.)\n" +
		Said(name, queenID, text)
}

// ReplyText is what a writer reads when the queen she wrote to answers.
func ReplyText(name, queenID, text string) string {
	return "(A reply from " + name + " to your message_queen letter. To go on, write to her again.)\n" +
		Said(name, queenID, text)
}

// pyReprString is Python's repr() for a string, as refusal messages spell ids.
func pyReprString(value string) string {
	quote := '\''
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteRune(quote)
	for _, char := range value {
		switch char {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if char == quote {
				builder.WriteByte('\\')
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteRune(quote)
	return builder.String()
}
