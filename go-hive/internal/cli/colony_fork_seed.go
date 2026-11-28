package cli

// Colony fork seed: the queen session an independent DM forks into its new
// colony. The source session tree is copied into the colony's canonical
// queen-session dir (colonies/<c>/queens/<q>/sessions/<id>), its meta.json is
// re-stamped as the colony's own session, and the inherited transcript is
// compacted in the background — wall-capped — while the fork blocks on the
// compaction marker (bounded and fail-soft) before resuming the colony. The
// compacted (or raw) conversations are snapshotted into the colony's
// seed_conversation once the compaction settles.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony
// Parity: framework/server/routes_execution.py::_compact_inherited_conversation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	hivesession "github.com/shark2202/go_hive/go-hive/internal/session"
)

const (
	// colonyForkCompactionTimeout is the wall-clock cap on the background
	// compaction's LLM call (_COMPACTION_TIMEOUT_SECONDS). Without it a hung
	// model would leave compaction_status="in_progress" forever.
	colonyForkCompactionTimeout = 180 * time.Second
	// colonyForkAwaitTimeout is the cap on the fork's wait for that background
	// compactor before the colony session opens — upstream
	// _create_session_for_existing_colony's
	// compaction_status.await_completion(..., timeout=180.0).
	colonyForkAwaitTimeout = 180 * time.Second
	// colonyForkAwaitPoll is await_completion's marker poll interval.
	colonyForkAwaitPoll = 500 * time.Millisecond
	// inheritedCompactionMinWindowShare carries a small inherited transcript
	// over verbatim: compacting below this share of the queen's window would
	// lose detail for nothing (_INHERITED_COMPACTION_MIN_WINDOW_SHARE).
	inheritedCompactionMinWindowShare = 0.25
	// colonyForkMarkerPreviewChars is the summary preview the fork marker
	// carries (summary_text[:240]).
	colonyForkMarkerPreviewChars = 240
	// colonyForkContextTokens is the queen window fallback _queen_window_tokens
	// uses when the loop config carries no max_context_tokens.
	colonyForkContextTokens = 180_000
)

// colonyForkSeed is the forked queen session the colony resumes: the generated
// session id, the canonical colony-tree dir it lives in, and whether a source
// transcript was actually copied. Copied=false means the source had no session
// dir on disk; the colony opens fresh, as upstream does.
type colonyForkSeed struct {
	SessionID string
	QueenDir  string
	Copied    bool
}

// prepareColonyForkSeed copies sourceQueenDir into the colony's canonical queen
// session dir for a freshly generated session id and re-stamps the copy's
// meta.json. The copy is a directory merge (shutil.copytree dirs_exist_ok=True):
// files present only in the destination stay, source files win.
//
// A failed copy is returned as an error — the caller tears the half-colony down
// and reports 500, as upstream lets the copytree exception fail the fork.
func prepareColonyForkSeed(home hivehome.Home, colonyID, queenScope, sourceQueenDir, sourceSessionID string) (colonyForkSeed, error) {
	sessionID, err := hivesession.NewStore("").GenerateID(time.Now())
	if err != nil {
		return colonyForkSeed{}, err
	}
	seed := colonyForkSeed{SessionID: sessionID}
	if strings.TrimSpace(sourceQueenDir) == "" || !isSessionDir(sourceQueenDir) {
		return seed, nil
	}
	destQueenDir := home.ColonyQueenSessionDir(colonyID, queenScope, sessionID)
	if err := copyTreeMerge(sourceQueenDir, destQueenDir); err != nil {
		return colonyForkSeed{}, err
	}
	if err := rewriteColonyForkMeta(destQueenDir, home.ColonyDir(colonyID), colonyID, queenScope, sourceSessionID); err != nil {
		return colonyForkSeed{}, err
	}
	seed.QueenDir = destQueenDir
	seed.Copied = true
	return seed, nil
}

// colonyForkSourceQueenDir resolves the source session's on-disk tree. The live
// session record is authoritative; a record without one falls back to the
// session-id lookup the other fork paths use.
func colonyForkSourceQueenDir(home hivehome.Home, source *hiveserver.Session) string {
	if source == nil {
		return ""
	}
	if dir := strings.TrimSpace(source.QueenDir); dir != "" && isSessionDir(dir) {
		return dir
	}
	if home.Root == "" {
		return ""
	}
	if found, err := hiveserver.FindQueenSessionDir(home, source.ID); err == nil && isSessionDir(found) {
		return found
	}
	return ""
}

// colonyForkSourceQueenName reads the queen segment out of a queen session tree
// (.../queens/<name>/sessions/<id>), which is what upstream's fork treats as
// the colony's queen identity when the source dir exists. "" means the layout
// was not recognised.
func colonyForkSourceQueenName(queenDir string) string {
	queenDir = strings.TrimSpace(queenDir)
	if queenDir == "" {
		return ""
	}
	sessionsDir := filepath.Dir(filepath.Clean(queenDir))
	queenDirPath := filepath.Dir(sessionsDir)
	if filepath.Base(sessionsDir) != "sessions" || filepath.Base(filepath.Dir(queenDirPath)) != "queens" {
		return ""
	}
	name := filepath.Base(queenDirPath)
	if !hivesession.ValidSegment(name) {
		return ""
	}
	return name
}

// copyTreeMerge mirrors shutil.copytree(src, dst, dirs_exist_ok=True): every
// source directory merges into dst and every source file is written over the
// destination, while destination-only files are left in place.
func copyTreeMerge(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		perm := info.Mode().Perm()
		if perm == 0 {
			perm = 0o644
		}
		return atomicfs.CopyFile(path, target, perm)
	})
}

// rewriteColonyForkMeta points the copied session's meta.json at the colony and
// stamps the fork provenance. The spawn-lock fields are stripped: they belonged
// to the PARENT session, never to this freshly-forked one.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony (dest_meta)
func rewriteColonyForkMeta(destQueenDir, colonyPath, colonyID, queenName, sourceSessionID string) error {
	metaPath := filepath.Join(destQueenDir, "meta.json")
	meta, _ := readJSONObject(metaPath)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["agent_path"] = colonyPath
	meta["agent_name"] = humanizeColonyName(colonyID)
	meta["queen_id"] = queenName
	meta["forked_from"] = sourceSessionID
	meta["colony_fork"] = true
	for _, key := range []string{"colony_spawned", "spawned_colony_id", "spawned_colony_at"} {
		delete(meta, key)
	}
	return atomicfs.WriteJSON(metaPath, meta, 0o644)
}

// colonyForkFinalize is the background half of the fork: compact the inherited
// transcript (bounded by timeout), snapshot the conversations into the colony's
// seed_conversation, and record the outcome in compaction_status.json. Every
// stage is fail-soft — the colony keeps working on the raw transcript.
type colonyForkFinalize struct {
	queenDir         string
	seedConversation string
	sourceSessionID  string
	completer        llm.Completer
	maxContextTokens int
	timeout          time.Duration
}

// scheduleColonyForkFinalize starts the background finalizer. It runs on its
// own goroutine; the fork only ever waits on its compaction_status marker,
// through awaitColonyForkCompaction's bounded poll.
func (l *colonyLifecycle) scheduleColonyForkFinalize(seed colonyForkSeed, colonyID, sourceSessionID string, completer llm.Completer, maxContextTokens int) {
	if seed.QueenDir == "" {
		return
	}
	job := colonyForkFinalize{
		queenDir:         seed.QueenDir,
		seedConversation: l.home.ColonySeedConversationDir(colonyID),
		sourceSessionID:  sourceSessionID,
		completer:        completer,
		maxContextTokens: maxContextTokens,
		timeout:          l.forkCompactionTimeout,
	}
	l.forkBackground.Add(1)
	go func() {
		defer l.forkBackground.Done()
		job.run(l.out)
	}()
}

// awaitColonyForkCompaction blocks until the scheduled finalizer leaves the
// copied session's marker at done/failed (or the wait cap fires). The colony
// session must not open before this: a create with an initial_prompt would
// write the new user message into the transcript the compactor is about to
// rewrite, and the compactor's stale-part sweep would drop it. The wait is
// bounded and fail-soft — on timeout or a missing marker the caller proceeds
// with the raw copied transcript, exactly like upstream.
//
// Parity: framework/server/session_manager.py::_create_session_for_existing_colony (await_completion)
func (l *colonyLifecycle) awaitColonyForkCompaction(queenDir string) {
	if strings.TrimSpace(queenDir) == "" {
		return
	}
	await := l.awaitForkCompaction
	if await == nil {
		await = hiveserver.AwaitCompletion
	}
	timeout := l.forkAwaitTimeout
	if timeout <= 0 {
		timeout = colonyForkAwaitTimeout
	}
	poll := l.forkAwaitPoll
	if poll <= 0 {
		poll = colonyForkAwaitPoll
	}
	// AwaitCompletion returns nil (no marker) or the last observed status
	// (still in_progress at the cap); neither is an error. Proceeding on both
	// is the fail-soft contract.
	await(context.Background(), queenDir, timeout, poll)
}

func (j colonyForkFinalize) run(out io.Writer) {
	defer func() {
		if recovered := recover(); recovered != nil {
			hiveserver.MarkFailed(j.queenDir, fmt.Sprintf("compaction failed: %v", recovered))
			fmt.Fprintf(out, "Warning: colony fork finalizer panicked for %s: %v\n", j.queenDir, recovered)
		}
	}()
	compactionErr := j.compactInherited(out)
	// The seed snapshot runs regardless of the compaction outcome: on success
	// the workers get the summary, on failure they get the raw transcript the
	// copytree left behind. Skipping it would open them to empty state on every
	// compaction hiccup.
	if err := j.copySeedConversation(); err != nil {
		fmt.Fprintf(out, "Warning: colony seed_conversation copy failed for %s: %v\n", j.seedConversation, err)
	}
	if compactionErr != nil {
		hiveserver.MarkFailed(j.queenDir, compactionErr.Error())
		return
	}
	// Upstream's finalizer calls mark_done() without counters; the counts live
	// in the colony_fork_marker event instead.
	hiveserver.MarkDone(j.queenDir, 0, 0)
}

// compactInherited runs the compaction under a wall-clock cap and records the
// outcome. A timeout is normalised to upstream's message whichever side of the
// select reports it first.
func (j colonyForkFinalize) compactInherited(out io.Writer) error {
	timeout := j.timeout
	if timeout <= 0 {
		timeout = colonyForkCompactionTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		// A panic on the compaction goroutine would take the whole server
		// down; upstream's fail-soft contract is that the colony keeps the
		// raw transcript instead.
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()
		done <- compactColonyInheritedConversation(ctx, j.queenDir, j.completer, j.maxContextTokens, j.sourceSessionID)
	}()

	var err error
	timedOut := false
	select {
	case err = <-done:
	case <-ctx.Done():
		timedOut = true
	}
	if timedOut || (err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		err = fmt.Errorf("compaction timed out after %.0fs (falling back to raw transcript)", timeout.Seconds())
		fmt.Fprintf(out, "Warning: fork compaction %v for %s\n", err, j.queenDir)
		return err
	}
	if err != nil {
		// Upstream records "compaction failed: <exc>"; the raw transcript is
		// what the colony keeps.
		return fmt.Errorf("compaction failed: %w", err)
	}
	return nil
}

// copySeedConversation snapshots the final conversation state into the colony's
// seed_conversation (the worker's starting context). The directory is created
// even when there is nothing to copy, as upstream's mkdir(parents=True) does.
//
// Parity: framework/server/routes_execution.py::fork_session_into_colony (v3 seed copy)
func (j colonyForkFinalize) copySeedConversation() error {
	if err := os.MkdirAll(j.seedConversation, 0o755); err != nil {
		return err
	}
	sourceConversations := filepath.Join(j.queenDir, "conversations")
	if !isSessionDir(sourceConversations) {
		return nil
	}
	return copyTreeMerge(sourceConversations, j.seedConversation)
}

// compactColonyInheritedConversation is _compact_inherited_conversation: keep a
// small transcript verbatim, otherwise compact it in place and tag the summary
// with inherited_from. Either way a colony_fork_marker is appended to
// events.jsonl so the frontend can group everything that preceded the fork.
func compactColonyInheritedConversation(ctx context.Context, queenDir string, completer llm.Completer, maxContextTokens int, sourceSessionID string) error {
	if maxContextTokens <= 0 {
		maxContextTokens = colonyForkContextTokens
	}
	inheritedChars, inheritedCount := colonyInheritedTranscriptSize(queenDir)
	inheritedTokens := inheritedChars / 4
	if inheritedCount > 0 && float64(inheritedTokens) < float64(maxContextTokens)*inheritedCompactionMinWindowShare {
		// Compacting would only lose detail; the colony opens on the raw copy.
		log.Printf("compact_inherited: keeping %d message(s) (~%d tokens) verbatim for colony forked from %s",
			inheritedCount, inheritedTokens, sourceSessionID)
		appendColonyForkMarker(queenDir, sourceSessionID, "", inheritedCount)
		return nil
	}
	result, err := hiveserver.CompactQueenConversationInPlace(ctx, queenDir, completer, maxContextTokens, sourceSessionID)
	if err != nil {
		return err
	}
	if result == nil {
		// No completer, no conversations dir, or no messages: nothing to
		// compact and nothing to mark.
		log.Printf("compact_inherited: nothing to compact for colony forked from %s", sourceSessionID)
		return nil
	}
	log.Printf("compact_inherited: compacted %d parent message(s) -> 1 summary (%d chars) for colony forked from %s",
		result.Messages, result.Chars, sourceSessionID)
	appendColonyForkMarker(queenDir, sourceSessionID, result.Text, result.Messages)
	return nil
}

// colonyInheritedTranscriptSize is _inherited_transcript_size: (content chars,
// message count) of the conversation in queenDir. Characters are counted as
// runes to match Python's len() on a str.
func colonyInheritedTranscriptSize(queenDir string) (int, int) {
	conversations := filepath.Join(queenDir, "conversations")
	if !isSessionDir(conversations) {
		return 0, 0
	}
	parts, err := agentloop.NewFileConversationStore(conversations).ReadParts()
	if err != nil {
		return 0, 0
	}
	chars := 0
	for _, part := range parts {
		if content, ok := part["content"].(string); ok {
			chars += utf8.RuneCountInString(content)
		}
	}
	return chars, len(parts)
}

// appendColonyForkMarker appends the boundary event the frontend folds the
// inherited DM history behind. Best effort: a failed append only costs the
// collapsed widget.
//
// Parity: framework/server/routes_execution.py::_compact_inherited_conversation (marker)
func appendColonyForkMarker(queenDir, sourceSessionID, summary string, inheritedMessages int) {
	stamp := pythonISOFormat(time.Now().UTC())
	marker := map[string]any{
		"type":      "colony_fork_marker",
		"stream_id": "queen",
		"data": map[string]any{
			"parent_session_id":       sourceSessionID,
			"fork_time":               stamp,
			"summary_preview":         truncateRunes(summary, colonyForkMarkerPreviewChars),
			"inherited_message_count": inheritedMessages,
		},
		"timestamp": stamp,
	}
	if err := atomicfs.AppendJSONL(filepath.Join(queenDir, "events.jsonl"), marker); err != nil {
		log.Printf("compact_inherited: failed to append fork marker: %v", err)
	}
}

// colonyForkCompleter resolves the source session's LLM for compaction. Nil
// means the session has no stamped provider; compaction is then unavailable and
// the raw transcript stays authoritative.
func colonyForkCompleter(session *hiveserver.Session) llm.Completer {
	if session == nil {
		return nil
	}
	completer, ok := session.LLM.(llm.Completer)
	if !ok || completer == nil {
		return nil
	}
	return llm.ResolveCompleter(completer)
}
