package server

// Parity: framework/server/queen_mail.py::QueenMail (server backend)

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/qmail"
)

// QueenMailAdapterOptions wires the queen-mail service to the server. Home and
// Manager are required; CreateSession is the same hook the session routes use,
// and nil leaves letters to stopped Hive queens undelivered rather than
// starting a session the build can't run. SessionDir is where a live session's
// meta.json lives when Session.QueenDir does not say (tests and alternate
// loaders); nil falls back to the queens directory scan.
type QueenMailAdapterOptions struct {
	Home          hivehome.Home
	Manager       *SessionManager
	CreateSession func(CreateSessionParams) (*Session, error)
	SessionDir    func(*Session) string
}

// QueenMailAdapter is the server's queen mail: the QueenMailBackend the routes
// call, backed by a qmail.Mail and by Hive session injection.
type QueenMailAdapter struct {
	home          hivehome.Home
	manager       *SessionManager
	createSession func(CreateSessionParams) (*Session, error)
	sessionDir    func(*Session) string
	mail          *qmail.Mail
}

var _ QueenMailBackend = (*QueenMailAdapter)(nil)

// NewQueenMailAdapter builds the backend the queen-mail routes should be
// registered with. It never returns nil: with queen_mail off the same
// contract answers (authorized conversations get an empty list, letters are
// refused), which is what upstream does.
func NewQueenMailAdapter(options QueenMailAdapterOptions) *QueenMailAdapter {
	home := options.Home
	if home.Root == "" {
		if resolved, err := hivehome.Resolve(); err == nil {
			home = resolved
		}
	}
	adapter := &QueenMailAdapter{
		home:          home,
		manager:       options.Manager,
		createSession: options.CreateSession,
		sessionDir:    options.SessionDir,
	}
	adapter.mail = qmail.New(home, qmail.ConfigFromHive(HiveConfig(home)), adapter)
	return adapter
}

// Mail is the service behind the adapter (tokens, inboxes, queue).
func (a *QueenMailAdapter) Mail() *qmail.Mail {
	if a == nil {
		return nil
	}
	return a.mail
}

// TokenFor is the token a Claude Code run writes from conversation ref with.
func (a *QueenMailAdapter) TokenFor(ref string) string {
	if a == nil || a.mail == nil {
		return ""
	}
	return a.mail.TokenFor(ref)
}

// Authorizes reports whether token is good for conversation ref.
func (a *QueenMailAdapter) Authorizes(ref, token string) bool {
	if a == nil || a.mail == nil {
		return false
	}
	return a.mail.Authorizes(ref, token)
}

// QueenOf names the queen behind a conversation ref.
func (a *QueenMailAdapter) QueenOf(ref string) (string, bool) {
	if a == nil || a.mail == nil {
		return "", false
	}
	return a.mail.QueenOf(ref)
}

// Correspondents lists the queens queenID may write to.
func (a *QueenMailAdapter) Correspondents(queenID string) []map[string]any {
	if a == nil || a.mail == nil {
		return []map[string]any{}
	}
	return a.mail.Correspondents(queenID)
}

// SendFromConversation hands a letter from a Claude Code queen's conversation
// to another queen. Refusals come back as the routes' QueenMailError (400).
func (a *QueenMailAdapter) SendFromConversation(ref, toQueen, message string) (map[string]any, error) {
	if a == nil || a.mail == nil {
		return nil, &QueenMailError{Message: "This conversation isn't known to Hive"}
	}
	sent, err := a.mail.SendFromConversation(ref, toQueen, message)
	if err != nil {
		var refusal *qmail.Error
		if errors.As(err, &refusal) {
			return nil, &QueenMailError{Message: refusal.Message}
		}
		return nil, err
	}
	return sent, nil
}

// Deliver hands a letter to a Hive queen's session with writer, opening or
// resuming that thread when it isn't live.
//
// Parity: framework/server/queen_mail.py::QueenMail._send_to_hive_queen
func (a *QueenMailAdapter) Deliver(ctx context.Context, toQueen, writer, fromRef string, letter qmail.Letter) (string, error) {
	if a == nil || a.manager == nil {
		return "", qmailError(toQueen + " can't take letters now")
	}
	threadID, threadExists := a.threadOf(toQueen, writer)
	var session *Session
	if threadID != "" {
		session = a.manager.GetSession(threadID)
	}
	if session == nil {
		if a.createSession == nil {
			return "", qmailError(toQueen + " can't take letters now")
		}
		params := CreateSessionParams{QueenName: &toQueen}
		if threadExists {
			params.QueenResumeFrom = &threadID
		}
		created, err := a.createSession(params)
		if err != nil {
			return "", qmailError(toQueen + " can't take letters now: " + err.Error())
		}
		session = created
	}
	if session == nil {
		return "", qmailError(toQueen + " can't take letters now")
	}
	if session.QueenName != "" && session.QueenName != toQueen {
		return "", qmailError(toQueen + " can't take letters now")
	}
	if queenMailLocked(session) {
		return "", qmailError(toQueen + " can't take letters now: the conversation is in a colony")
	}
	a.markThread(session, toQueen, writer, fromRef, letter.FromName)
	if err := queenMailInject(session, qmail.LetterText(letter.FromName, letter.From, letter.Text)); err != nil {
		return "", qmailError(toQueen + " can't take letters now: " + err.Error())
	}
	return session.ID, nil
}

// DeliverReply hands a reply into the writer's session (resuming it when it
// stopped), so the queen whose letter is answered wakes with it.
//
// Parity: framework/server/queen_mail.py::QueenMail._reachable
func (a *QueenMailAdapter) DeliverReply(ctx context.Context, replyTo, queenID, writer string, letter qmail.Letter) (string, error) {
	if a == nil || a.manager == nil || replyTo == "" {
		return "", qmailError("the conversation is unreachable")
	}
	session := a.manager.GetSession(replyTo)
	if session == nil {
		if a.createSession == nil {
			return "", qmailError("the conversation is unreachable")
		}
		params := CreateSessionParams{QueenName: &writer, QueenResumeFrom: &replyTo}
		created, err := a.createSession(params)
		if err != nil {
			return "", qmailError("the conversation is unreachable: " + err.Error())
		}
		session = created
	}
	if session == nil {
		return "", qmailError("the conversation is unreachable")
	}
	if session.QueenName != "" && session.QueenName != writer {
		return "", qmailError("the conversation is unreachable")
	}
	if queenMailLocked(session) {
		return "", qmailError("the conversation is unreachable")
	}
	if err := queenMailInject(session, qmail.ReplyText(letter.FromName, letter.From, letter.Text)); err != nil {
		return "", qmailError("the conversation is unreachable: " + err.Error())
	}
	return session.ID, nil
}

// threadOf finds the session writer already has with toQueen: channel
// "queens", peer writer, the newest id first.
//
// Parity: framework/server/queen_mail.py::QueenMail._thread_of
func (a *QueenMailAdapter) threadOf(toQueen, writer string) (string, bool) {
	for _, sessionsDir := range a.threadDirs(toQueen) {
		entries, err := os.ReadDir(sessionsDir)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names {
			meta := readJSONObjectOr(filepath.Join(sessionsDir, name, "meta.json"), map[string]any{})
			if queenStr(meta["channel"]) != "queens" || queenStr(meta["peer"]) != writer {
				continue
			}
			if queenID := queenStr(meta["queen_id"]); queenID != "" && queenID != toQueen {
				continue
			}
			return name, true
		}
	}
	return "", false
}

func (a *QueenMailAdapter) threadDirs(toQueen string) []string {
	dirs := []string{a.home.QueenSessionsDir(toQueen)}
	// The Go session factory keeps queen sessions under queens/default today;
	// thread meta records queen_id so those can still be recognised.
	if toQueen != "default" {
		dirs = append(dirs, a.home.QueenSessionsDir("default"))
	}
	return dirs
}

// markThread records on disk what the thread is, so a restart finds it again.
//
// Parity: framework/server/channels.py::_merge_meta
func (a *QueenMailAdapter) markThread(session *Session, queenID, writer, fromRef, title string) {
	dir := a.sessionDirOf(session)
	if dir == "" {
		return
	}
	meta := readJSONObjectOr(filepath.Join(dir, "meta.json"), map[string]any{})
	meta["channel"] = "queens"
	meta["peer"] = writer
	meta["reply_to"] = fromRef
	meta["queen_id"] = queenID
	if strings.TrimSpace(title) != "" {
		meta["title"] = title
		meta["title_source"] = "user"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = atomicfs.WriteJSON(filepath.Join(dir, "meta.json"), meta, 0o644)
}

func (a *QueenMailAdapter) sessionDirOf(session *Session) string {
	if session == nil {
		return ""
	}
	if session.QueenDir != "" {
		return session.QueenDir
	}
	if a.sessionDir != nil {
		if dir := a.sessionDir(session); dir != "" {
			return dir
		}
	}
	dir, err := FindQueenSessionDir(a.home, session.ID)
	if err != nil || dir == "" {
		return ""
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	// The session's transcript store is created lazily; the thread record must
	// exist from the first letter or the next one opens a second session.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// queenMailLocked mirrors the colony-spawned lock: the conversation went on in
// the colony, so letters can't be put back into it.
func queenMailLocked(session *Session) bool {
	if session == nil {
		return true
	}
	return session.ColonySpawned || session.SpawnedColonyID != "" || session.ColonyID != ""
}

// queenMailInject puts one message into a live session: through the chat
// runtime when that is the queen, otherwise through her queen node.
func queenMailInject(session *Session, text string) error {
	if session == nil {
		return errors.New("no session")
	}
	if session.Chat != nil {
		return session.Chat.Deliver(text)
	}
	if found, err := injectQueenEvent(session, text, true); found {
		return err
	}
	return errors.New("the queen isn't running")
}

func qmailError(message string) *qmail.Error {
	return &qmail.Error{Message: message}
}
