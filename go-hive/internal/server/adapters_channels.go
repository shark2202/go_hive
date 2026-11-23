package server

// The channel host's session backend: upstream's ChannelHost methods over this
// server's SessionManager, ChatRuntime and session trees. The CLI builds one
// per configured channel, passing the same CreateSession factory the HTTP
// routes use.
//
// Parity: framework/server/channels.py::ChannelHost

import (
	"context"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/channels"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
)

// ChannelBackendOptions wires one channel to the session runtime. Channel is
// the name written to the claimed sessions' meta.json; Home and Manager are
// required; CreateSession is the same hook the session routes use (nil leaves
// open/send unable to start a session rather than faking one).
type ChannelBackendOptions struct {
	Channel       string
	Home          hivehome.Home
	Manager       *SessionManager
	CreateSession func(CreateSessionParams) (*Session, error)
}

// ChannelBackend is the server implementation of channels.SessionBackend.
type ChannelBackend struct {
	channel       string
	home          hivehome.Home
	manager       *SessionManager
	createSession func(CreateSessionParams) (*Session, error)

	mu   sync.Mutex
	subs map[string]*channelSubscription
}

var _ channels.SessionBackend = (*ChannelBackend)(nil)

// NewChannelBackend builds the backend for one channel. It resolves HIVE_HOME
// like the queen-mail adapter does so callers that could not resolve it still
// get a usable home.
func NewChannelBackend(options ChannelBackendOptions) *ChannelBackend {
	home := options.Home
	if home.Root == "" {
		if resolved, err := hivehome.Resolve(); err == nil {
			home = resolved
		}
	}
	return &ChannelBackend{
		channel:       strings.TrimSpace(options.Channel),
		home:          home,
		manager:       options.Manager,
		createSession: options.CreateSession,
		subs:          map[string]*channelSubscription{},
	}
}

// OpenSession returns the session for one conversation with queenID, resuming
// sessionID when it is a resumable DM of that queen and starting a fresh
// independent session otherwise. A missing queen is a ChannelError with
// upstream's wording.
//
// Parity: framework/server/channels.py::ChannelHost.open_session
func (b *ChannelBackend) OpenSession(ctx context.Context, queenID, sessionID, title string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	queen.EnsureDefaultQueens()
	if _, err := queen.LoadQueenProfile(b.home.QueensDir(), queenID); err != nil {
		return "", channels.Errorf(channels.KindNoQueen,
			"No queen '%s': check the queen id in the '%s' channel's settings", queenID, b.channel)
	}

	if strings.TrimSpace(sessionID) != "" {
		resumed, resumeErr := b.resumeSession(queenID, sessionID)
		if resumeErr != nil {
			return "", resumeErr
		}
		if resumed != nil {
			b.followSessionID(sessionID, resumed.ID)
			b.claim(resumed)
			return resumed.ID, nil
		}
	}

	created, err := b.create(queenID, "", "independent")
	if err != nil {
		return "", err
	}
	b.claim(created)
	if strings.TrimSpace(title) != "" && created.QueenDir != "" {
		if _, err := WriteTitle(created.QueenDir, title, "user"); err != nil {
			return "", channels.Errorf(channels.KindInternal, "title write failed: %v", err)
		}
	}
	return created.ID, nil
}

// Send injects a user message into sessionID (or its newest successor),
// resuming the session if it stopped. The returned id is the session that got
// the message when the queen moved the conversation on.
//
// Parity: framework/server/channels.py::ChannelHost.send
func (b *ChannelBackend) Send(ctx context.Context, sessionID, text string, images [][]byte, displayText, correlationID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	queenID := b.queenOf(sessionID)
	if queenID == "" {
		return "", channels.Errorf(channels.KindNoSession, "No session '%s'", sessionID)
	}
	resolvedID := b.newestSuccessor(queenID, sessionID)
	session := b.liveSession(resolvedID)
	if session == nil {
		var resumeErr error
		session, resumeErr = b.resumeSession(queenID, resolvedID)
		if resumeErr != nil {
			return "", channels.Errorf(channels.KindQueenUnavailable,
				"Session '%s' can't be resumed: %v", sessionID, resumeErr)
		}
	}
	if session == nil {
		return "", channels.Errorf(channels.KindNoSession, "Session '%s' can't be resumed", sessionID)
	}
	if session.ColonySpawned || session.ColonyID != "" {
		return "", channels.Errorf(channels.KindSessionLocked,
			"Session '%s' is locked because it spawned a colony; open a new one", session.ID)
	}
	b.followSessionID(sessionID, session.ID)
	// A real message ends a fork's kickoff turn, as it does in /chat.
	session.ForkKickoffPending = false

	blocks := channelImageBlocks(images)
	message, blocks, saved, err := prepareChatInput(session, text, blocks)
	if err != nil {
		return "", channels.Errorf(channels.KindInternal, "prepare message: %v", err)
	}
	if strings.TrimSpace(message) == "" && len(blocks) == 0 {
		return "", channels.Errorf(channels.KindInvalidParams,
			"Nothing to send: the message has no text and no readable image")
	}
	if session.Chat == nil {
		return "", channels.Errorf(channels.KindQueenUnavailable,
			"The queen of session '%s' can't be started", session.ID)
	}

	echo := message
	if displayText != "" {
		echo = displayText
	}
	if correlationID == "" {
		correlationID = newChatCorrelationID()
	}
	publishChannelClientInputReceived(session, echo, len(blocks), saved, correlationID)
	if err := session.Chat.DeliverClientInputWithImages(message, blocks, correlationID); err != nil {
		return "", channels.Errorf(channels.KindQueenUnavailable,
			"The queen of session '%s' can't be started", session.ID)
	}
	return session.ID, nil
}

// Subscribe follows a session's queen output from now on, including after a
// fork moved the conversation to a new session id.
//
// Parity: framework/server/channels.py::ChannelHost.subscribe
func (b *ChannelBackend) Subscribe(sessionID string, fn func(channels.Output)) (func(), error) {
	if b.manager == nil {
		return nil, errors.New("channel backend has no session manager")
	}
	sub := newChannelSubscription(b, sessionID, fn)
	b.mu.Lock()
	previous := b.subs[sessionID]
	if previous != nil {
		delete(b.subs, sessionID)
	}
	b.subs[sessionID] = sub
	b.mu.Unlock()
	if previous != nil {
		previous.close()
	}

	if live := b.liveSession(sessionID); live != nil {
		sub.attach(live)
	}
	unsubscribe := func() {
		// The map key moves when a fork follows the conversation, so remove
		// by identity instead of the session id the caller subscribed with.
		b.mu.Lock()
		for id, candidate := range b.subs {
			if candidate == sub {
				delete(b.subs, id)
			}
		}
		b.mu.Unlock()
		sub.close()
	}
	return unsubscribe, nil
}

// SetTitle names a session in the dashboard; an empty title clears it.
//
// Parity: framework/server/channels.py::ChannelHost.set_title
func (b *ChannelBackend) SetTitle(ctx context.Context, sessionID, title string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := FindQueenSessionDir(b.home, sessionID)
	if err != nil || !entryIsDir(dir) {
		return channels.Errorf(channels.KindNoSession, "No session '%s'", sessionID)
	}
	if _, err := WriteTitle(dir, title, "user"); err != nil {
		return channels.Errorf(channels.KindInternal, "title write failed: %v", err)
	}
	return nil
}

// Judge returns the judge slot's probability for question, or nil when no
// judge is configured or it cannot answer now.
//
// Parity: framework/server/channels.py::ChannelHost.judge
func (b *ChannelBackend) Judge(ctx context.Context, question string, state map[string]any, criteria map[string]string) (*float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	probability, ok := AskJudge(b.home, question, state, criteria, nil)
	if !ok {
		return nil, nil
	}
	return &probability, nil
}

func (b *ChannelBackend) create(queenID, resumeFrom, phase string) (*Session, error) {
	if b.createSession == nil {
		return nil, channels.Errorf(channels.KindQueenUnavailable, "session creation is not wired on this server")
	}
	params := CreateSessionParams{QueenName: &queenID}
	if strings.TrimSpace(phase) != "" {
		params.InitialPhase = &phase
	}
	if strings.TrimSpace(resumeFrom) != "" {
		params.QueenResumeFrom = &resumeFrom
	}
	session, err := b.createSession(params)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, channels.Errorf(channels.KindQueenUnavailable,
			"The queen of session '%s' can't be started", resumeFrom)
	}
	return session, nil
}

// resumeSession returns the live session (or resumes it from disk) when it is
// a DM of queenID that has not spawned a colony; nil means "start a new one".
//
// Parity: framework/server/channels.py::ChannelHost._resume
func (b *ChannelBackend) resumeSession(queenID, sessionID string) (*Session, error) {
	resolvedID := b.newestSuccessor(queenID, sessionID)
	if live := b.liveSession(resolvedID); live != nil {
		if live.QueenName != queenID || live.ColonyID != "" || live.ColonySpawned {
			return nil, nil
		}
		b.claim(live)
		return live, nil
	}
	dir := b.persistedSessionDir(queenID, resolvedID)
	if dir == "" {
		return nil, nil
	}
	meta := queenReadMeta(dir)
	if queenStr(meta["colony_id"]) != "" || pyTruthy(meta["colony_spawned"]) {
		return nil, nil
	}
	resumed, err := b.create(queenID, resolvedID, "")
	if err != nil || resumed == nil {
		log.Printf("channel %q: resuming session %s failed: %v", b.channel, resolvedID, err)
		if err == nil {
			err = errors.New("session factory returned no session")
		}
		return nil, err
	}
	b.claim(resumed)
	return resumed, nil
}

// persistedSessionDir returns the on-disk directory of a queen-scoped session,
// including the legacy shared default tree when its meta names the queen.
//
// Parity: framework/server/session_manager.py::_find_queen_session_dir
func (b *ChannelBackend) persistedSessionDir(queenID, sessionID string) string {
	target := b.home.QueenSessionDir(queenID, sessionID)
	if entryIsDir(target) {
		return target
	}
	if queenID == "default" {
		return ""
	}
	legacy := b.home.QueenSessionDir("default", sessionID)
	if !entryIsDir(legacy) {
		return ""
	}
	meta := queenReadMeta(legacy)
	recorded := queenStr(meta["queen_id"])
	if recorded == "" || recorded == queenID {
		return legacy
	}
	return ""
}

// newestSuccessor follows meta.json superseded_by within the queen's session
// tree, stopping at a cycle or the first session with no successor.
//
// Parity: framework/server/channels.py::_newest_successor
func (b *ChannelBackend) newestSuccessor(queenID, sessionID string) string {
	seen := map[string]bool{sessionID: true}
	dir := b.home.QueenSessionsDir(queenID)
	current := sessionID
	for {
		meta := queenReadMeta(filepath.Join(dir, current))
		successor := queenStr(meta["superseded_by"])
		if successor == "" || seen[successor] {
			return current
		}
		seen[successor] = true
		current = successor
	}
}

// queenOf resolves the queen that owns sessionID: the live session first, then
// meta.json queen_id, then the queen directory holding it.
//
// Parity: framework/server/channels.py::ChannelHost._queen_of
func (b *ChannelBackend) queenOf(sessionID string) string {
	if live := b.liveSession(sessionID); live != nil && strings.TrimSpace(live.QueenName) != "" {
		return live.QueenName
	}
	dir, err := FindQueenSessionDir(b.home, sessionID)
	if err != nil || !entryIsDir(dir) {
		return ""
	}
	if recorded := queenStr(queenReadMeta(dir)["queen_id"]); recorded != "" {
		return recorded
	}
	cleaned := filepath.Clean(dir)
	// queens/<queen>/sessions/<id> (or colonies/<c>/queens/<q>/sessions/<id>):
	// two levels up from the session dir is the queen directory, one more is
	// the queens root.
	queenDir := filepath.Dir(filepath.Dir(cleaned))
	queensRoot := filepath.Dir(queenDir)
	if filepath.Base(queensRoot) == "queens" {
		return filepath.Base(queenDir)
	}
	return ""
}

// followSessionID moves a subscription when send/resume learns the
// conversation's live id differs from the one the plugin addressed.
//
// Parity: framework/server/channels.py::ChannelHost._follow
func (b *ChannelBackend) followSessionID(oldID, newID string) {
	if oldID == "" || oldID == newID {
		return
	}
	b.mu.Lock()
	sub := b.subs[oldID]
	previous := b.subs[newID]
	if sub != nil {
		delete(b.subs, oldID)
		b.subs[newID] = sub
	}
	b.mu.Unlock()
	if previous != nil && previous != sub {
		previous.close()
	}
	if sub == nil {
		return
	}
	sub.move(newID)
	if live := b.liveSession(newID); live != nil {
		sub.attach(live)
	}
}

// claim marks sessionID as this channel's, now and after a restart.
//
// Parity: framework/server/channels.py::ChannelHost._claim
func (b *ChannelBackend) claim(session *Session) {
	if session == nil || b.channel == "" {
		return
	}
	session.SetChannel(b.channel)
	if session.QueenDir == "" {
		return
	}
	metaPath := filepath.Join(session.QueenDir, "meta.json")
	if err := MergeSessionMeta(metaPath, map[string]any{"channel": b.channel}); err != nil {
		// Upstream lets _merge_meta raise; a read-only session tree must not
		// make a live claim unreachable, so this is logged and the in-memory
		// claim stands.
		log.Printf("channel %q: merge meta for session %s: %v", b.channel, session.ID, err)
	}
}

func (b *ChannelBackend) liveSession(sessionID string) *Session {
	if b.manager == nil {
		return nil
	}
	return b.manager.GetSession(sessionID)
}

// channelImageBlocks turns image bytes into the data-URI image blocks
// /chat takes. Non-image bytes are skipped, as upstream's _image_block does.
//
// Parity: framework/server/channels.py::_image_block
// Parity-exempt: Go sniffs the MIME instead of running PIL; the 2 MiB /
// 2048px downscale path is not ported (the host bounds frames by size).
func channelImageBlocks(images [][]byte) []map[string]any {
	if len(images) == 0 {
		return nil
	}
	blocks := make([]map[string]any, 0, len(images))
	for _, data := range images {
		if len(data) == 0 {
			continue
		}
		mime := http.DetectContentType(data)
		switch mime {
		case "image/jpeg", "image/png", "image/gif", "image/webp":
		default:
			// Upstream's _image_block accepts PIL's four formats; anything
			// else is skipped with a warning rather than sent as a block the
			// model cannot read.
			log.Printf("channel: skipped an attachment that is not a supported image (%s, %d bytes)", mime, len(data))
			continue
		}
		blocks = append(blocks, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data),
			},
		})
	}
	return blocks
}

// publishChannelClientInputReceived is upstream's CLIENT_INPUT_RECEIVED emit
// for a channel send, carrying the plugin's correlation id (or a fresh one)
// and the dashboard echo.
//
// Parity: framework/server/routes_execution.py::deliver_to_queen
// (CLIENT_INPUT_RECEIVED)
func publishChannelClientInputReceived(session *Session, message string, imageCount int, imagePaths []string, correlationID string) {
	if session == nil || session.EventBus == nil {
		return
	}
	executionID := session.ID
	nodeID := "queen"
	event := eventbus.NewAgentEvent(eventbus.EventClientInputReceived, "queen")
	event.NodeID = &nodeID
	event.ExecutionID = &executionID
	event.CorrelationID = &correlationID
	data := map[string]any{"content": message, "image_count": imageCount}
	if len(imagePaths) > 0 {
		data["image_paths"] = imagePaths
	}
	event.Data = data
	session.EventBus.Publish(event)
}

// channelAttachedFiles is upstream's _attached_files for a channel output.
//
// Parity: framework/server/channels.py::_attached_files
func channelAttachedFiles(result string) []string {
	return queenMailAttachedFiles(result)
}

// visibleErrorText reports a failed model call returned as an ordinary reply.
//
// Parity: framework/agent_loop/visible_error.py::is_visible_error
func visibleErrorText(text string) bool {
	return agentloop.IsVisibleError(text)
}
