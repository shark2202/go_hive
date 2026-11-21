package server

// Parity: framework/server/queen_mail.py::QueenMail (server backend)

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/claudecode"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queenmail"
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
// call, backed by a queenmail.Mail and by Hive session injection.
type QueenMailAdapter struct {
	home          hivehome.Home
	manager       *SessionManager
	createSession func(CreateSessionParams) (*Session, error)
	sessionDir    func(*Session) string
	mail          *queenmail.Mail
	// claude is the conversations and inboxes of the queens run by Claude
	// Code, and claudeRunner the runner of their `claude -p` turns. The mail
	// service holds them as interfaces (queenmail must not import
	// claudecode); the adapter keeps the concrete values for the dashboard.
	claude       *claudecode.Store
	claudeRunner *claudecode.Runner

	// replySubs marks the thread sessions whose words are taken back to their
	// writers: one subscription per live conversation, however many letters
	// arrive. The bus is part of the key because a resumed session gets a new
	// one. replyMu guards it.
	replyMu   sync.Mutex
	replySubs map[string]*eventbus.EventBus
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
		replySubs:     map[string]*eventbus.EventBus{},
	}
	adapter.mail = queenmail.New(home, queenmail.ConfigFromHive(HiveConfig(home)), adapter)
	adapter.claude = claudecode.NewStore(home)
	adapter.mail.SetClaudeInboxes(adapter.claude)
	adapter.claudeRunner = claudecode.NewRunner(adapter.mail, adapter.claude)
	adapter.mail.SetClaudeRunner(adapter.claudeRunner)
	return adapter
}

// Close stops the process's Claude Code queen runner: no more turns start, and
// the process trees of the running ones are killed.
func (a *QueenMailAdapter) Close() {
	if a == nil || a.mail == nil {
		return
	}
	a.mail.Close()
}

// Mail is the service behind the adapter (tokens, inboxes, queue).
func (a *QueenMailAdapter) Mail() *queenmail.Mail {
	if a == nil {
		return nil
	}
	return a.mail
}

// ClaudeStore is the conversations and inboxes of the queens run by Claude
// Code: the dashboard reads them through the adapter.
func (a *QueenMailAdapter) ClaudeStore() *claudecode.Store {
	if a == nil {
		return nil
	}
	return a.claude
}

// ClaudeRunner runs the `claude -p` turns of the configured Claude Code
// queens; the dashboard reads its "running" marks.
func (a *QueenMailAdapter) ClaudeRunner() *claudecode.Runner {
	if a == nil {
		return nil
	}
	return a.claudeRunner
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
		var refusal *queenmail.Error
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
func (a *QueenMailAdapter) Deliver(ctx context.Context, toQueen, writer, fromRef string, letter queenmail.Letter) (string, error) {
	if a == nil || a.manager == nil {
		return "", queenMailError(toQueen + " can't take letters now")
	}
	threadID, threadExists := a.threadOf(toQueen, writer)
	var session *Session
	if threadID != "" {
		session = a.manager.GetSession(threadID)
	}
	if session == nil {
		if a.createSession == nil {
			return "", queenMailError(toQueen + " can't take letters now")
		}
		params := CreateSessionParams{QueenName: &toQueen}
		if threadExists {
			params.QueenResumeFrom = &threadID
		}
		created, err := a.createSession(params)
		if err != nil {
			return "", queenMailError(toQueen + " can't take letters now: " + err.Error())
		}
		session = created
	}
	if session == nil {
		return "", queenMailError(toQueen + " can't take letters now")
	}
	if session.QueenName != "" && session.QueenName != toQueen {
		return "", queenMailError(toQueen + " can't take letters now")
	}
	if queenMailLocked(session) {
		return "", queenMailError(toQueen + " can't take letters now: the conversation is in a colony")
	}
	a.markThread(session, toQueen, writer, fromRef, letter.FromName)
	// Subscribe before the letter enters the turn: a fast answer must not miss
	// the events that carry it back.
	a.subscribeReplies(session, toQueen, writer)
	if err := queenMailInject(session, queenmail.LetterText(letter.FromName, letter.From, letter.Text)); err != nil {
		return "", queenMailError(toQueen + " can't take letters now: " + err.Error())
	}
	return session.ID, nil
}

// DeliverReply hands a reply into the writer's session (resuming it when it
// stopped), so the queen whose letter is answered wakes with it.
//
// Parity: framework/server/queen_mail.py::QueenMail._reachable
func (a *QueenMailAdapter) DeliverReply(ctx context.Context, replyTo, queenID, writer string, letter queenmail.Letter) (string, error) {
	if a == nil || a.manager == nil || replyTo == "" {
		return "", queenMailError("the conversation is unreachable")
	}
	session := a.manager.GetSession(replyTo)
	if session == nil {
		if a.createSession == nil {
			return "", queenMailError("the conversation is unreachable")
		}
		params := CreateSessionParams{QueenName: &writer, QueenResumeFrom: &replyTo}
		created, err := a.createSession(params)
		if err != nil {
			return "", queenMailError("the conversation is unreachable: " + err.Error())
		}
		session = created
	}
	if session == nil {
		return "", queenMailError("the conversation is unreachable")
	}
	if session.QueenName != "" && session.QueenName != writer {
		return "", queenMailError("the conversation is unreachable")
	}
	if queenMailLocked(session) {
		return "", queenMailError("the conversation is unreachable")
	}
	if err := queenMailInject(session, queenmail.ReplyText(letter.FromName, letter.From, letter.Text)); err != nil {
		return "", queenMailError("the conversation is unreachable: " + err.Error())
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
		return session.Chat.InjectQueenMail(text)
	}
	if found, err := injectQueenEvent(session, text, true); found {
		return err
	}
	return errors.New("the queen isn't running")
}

func queenMailError(message string) *queenmail.Error {
	return &queenmail.Error{Message: message}
}

// subscribeReplies takes what the queen says in her thread session with writer
// back to the writer, however many letters arrive in that conversation. One
// watcher and one delivery worker per live session keeps her words in order;
// delivering inside the event handler would hold the session bus while a
// stopped writer's session starts.
//
// Parity: framework/server/queen_mail.py::QueenMail._subscribe
// Parity: framework/server/queen_mail.py::QueenMail._reply
func (a *QueenMailAdapter) subscribeReplies(session *Session, queenID, writer string) {
	if a == nil || a.mail == nil || session == nil || session.EventBus == nil {
		return
	}
	a.replyMu.Lock()
	if a.replySubs == nil {
		a.replySubs = map[string]*eventbus.EventBus{}
	}
	if a.replySubs[session.ID] == session.EventBus {
		a.replyMu.Unlock()
		return
	}
	a.replySubs[session.ID] = session.EventBus
	a.replyMu.Unlock()

	replies := make(chan string, 16)
	go func() {
		for text := range replies {
			a.deliverQueenReply(session, queenID, writer, text)
		}
	}()
	var pendingMu sync.Mutex
	pending := ""

	put := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		replies <- text
	}
	flush := func() {
		pendingMu.Lock()
		text := pending
		pending = ""
		pendingMu.Unlock()
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if isQueenMailVisibleError(text) {
			put("(Her reply failed: " + text + ")")
			return
		}
		put(text)
	}

	session.EventBus.Subscribe(
		[]eventbus.EventType{
			eventbus.EventClientOutputDelta,
			eventbus.EventToolCallStarted,
			eventbus.EventToolCallCompleted,
			eventbus.EventLLMTurnComplete,
			eventbus.EventClientInputRequested,
			eventbus.EventExecutionFailed,
		},
		func(event *eventbus.AgentEvent) {
			data := event.Data
			switch event.Type {
			case eventbus.EventClientOutputDelta:
				if snapshot := queenStr(data["snapshot"]); snapshot != "" {
					pendingMu.Lock()
					pending = snapshot
					pendingMu.Unlock()
				}
			case eventbus.EventToolCallStarted:
				// What she wrote before a tool call is finished; an ask_user
				// question carries its own text and is not said twice.
				if queenStr(data["tool_name"]) != "ask_user" {
					flush()
				}
			case eventbus.EventToolCallCompleted:
				if queenStr(data["tool_name"]) == "attach_file" && !queenMailBool(data["is_error"]) {
					if files := queenMailAttachedFiles(queenStr(data["result"])); len(files) > 0 {
						put("(She attached: " + strings.Join(files, ", ") + ")")
					}
				}
			case eventbus.EventLLMTurnComplete:
				flush()
			case eventbus.EventClientInputRequested:
				questions, _ := data["questions"].([]map[string]any)
				if queenStr(data["park_reason"]) == "ask_user" && len(questions) > 0 {
					put(queenMailQuestionsText(questions))
				} else {
					flush()
				}
			case eventbus.EventExecutionFailed:
				flush()
				if message := queenStr(data["error"]); message != "" {
					put("(Her reply failed: " + message + ")")
				}
			}
		},
		eventbus.WithFilterStream(chatStreamID),
	)
}

// deliverQueenReply takes one thing the queen said back to the writer's
// session. A refused or unreachable delivery is logged by the mail service as a
// dropped reply, so it never disappears silently.
func (a *QueenMailAdapter) deliverQueenReply(session *Session, queenID, writer, text string) {
	replyTo := a.mail.ReplyTarget(queenID, writer, session.ID)
	if replyTo == "" {
		replyTo = queenStr(a.threadMeta(session)["reply_to"])
	}
	if err := a.mail.DeliverReply(replyTo, queenID, writer, session.ID, text, a.mail.CauseFor(session.ID)); err != nil {
		log.Printf("queen mail: %s's reply to %s not delivered: %v", queenID, writer, err)
	}
}

// threadMeta is the thread's meta.json, or an empty map when the session has no
// directory yet.
func (a *QueenMailAdapter) threadMeta(session *Session) map[string]any {
	dir := a.sessionDirOf(session)
	if dir == "" {
		return map[string]any{}
	}
	return readJSONObjectOr(filepath.Join(dir, "meta.json"), map[string]any{})
}

// queenMailQuestionsText is what the writer is told of what her correspondent
// asked, with the options a structured question carries.
//
// Parity: framework/server/queen_mail.py::_reply_text (question)
func queenMailQuestionsText(questions []map[string]any) string {
	asked := make([]string, 0, len(questions))
	for _, question := range questions {
		prompt := queenStr(question["prompt"])
		choices := ""
		if options, ok := question["options"].([]any); ok && len(options) > 0 {
			texts := make([]string, 0, len(options))
			for _, option := range options {
				texts = append(texts, fmt.Sprint(option))
			}
			choices = " (" + strings.Join(texts, " / ") + ")"
		}
		asked = append(asked, prompt+choices)
	}
	return "(She asks:) " + strings.Join(asked, "\n")
}

// queenMailAttachedFiles is the files an attach_file result published into the
// session, per the JSON summary the tool returns.
//
// Parity: framework/server/channels.py::_attached_files
func queenMailAttachedFiles(result string) []string {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(result)))
	var summary map[string]any
	if err := decoder.Decode(&summary); err != nil {
		return nil
	}
	entries, _ := summary["attached"].([]any)
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		row, _ := entry.(map[string]any)
		resolved := queenStr(row["resolved"])
		if resolved == "" {
			continue
		}
		if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
			files = append(files, resolved)
		}
	}
	return files
}

func isQueenMailVisibleError(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " 	\r\n"), "[Error: LLM call failed: ")
}

func queenMailBool(value any) bool {
	flag, _ := value.(bool)
	return flag
}
