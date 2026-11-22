package memory

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

// Past turns as a per-owner index, so recall can answer without rescanning
// every session's events.jsonl on each message.
//
// Recall runs before the model is called, on every user message. Asking each
// session's events.jsonl for the terms of a query costs seconds on a store of
// a few dozen sessions. So each scope/owner keeps one derived file beside the
// session mirror: a row per turn (its user text and reply). The file follows
// each session's events.jsonl size, so a session that hasn't changed costs one
// stat per query.
//
// The file is derived from the sessions: deleting it costs a rebuild, nothing
// more. The first query after it disappears starts that rebuild in the
// background; a turn that cannot wait for it (past the recall cap) proceeds
// without excerpts while the rebuild keeps going.
//
// Parity: tools/src/memory_tools/turn_index.py
// (the SQLite engine is Python-only; this port keeps the same layout under
// HIVE_HOME/.message_index/recall/<scope>/ with a JSON body, the same
// per-owner file, the same "derived, deletable, rebuilds on first query"
// contract, and the same results — proven by recall_index_test.go).

// RecallIndexVersion bumps when the file format changes; an older file is
// rebuilt.
const RecallIndexVersion = 1

type recallIndexSession struct {
	// EventsSize is the session's events.jsonl size when it was indexed;
	// an unchanged size skips the session.
	EventsSize int64 `json:"events_size"`
}

type recallIndexTurn struct {
	Session string `json:"session"`
	Ordinal int    `json:"ordinal"`
	User    string `json:"user"`
	Reply   string `json:"reply"`
}

// recallIndex is the on-disk derived index.
type recallIndex struct {
	Version  int                           `json:"version"`
	Sessions map[string]recallIndexSession `json:"sessions"`
	Turns    []recallIndexTurn             `json:"turns"`
}

// RecallIndexPath is the derived index for one (scope, owner) pair. The path
// mirrors the Python reference's .message_index/recall/<scope>/<owner>
// location.
func RecallIndexPath(homeRoot string, ref ScopeRef) string {
	return filepath.Join(homeRoot, ".message_index", "recall", string(ref.Scope), ref.Owner+".json")
}

// recallIndexLocks serialises builds/refreshes per index path in this process.
var recallIndexLocks sync.Map // string -> *sync.Mutex

func recallIndexLock(path string) *sync.Mutex {
	lock, _ := recallIndexLocks.LoadOrStore(path, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

func loadRecallIndex(path string) (*recallIndex, bool) {
	var index recallIndex
	if err := atomicfs.ReadJSON(path, &index); err != nil {
		return nil, false
	}
	if index.Version != RecallIndexVersion || index.Sessions == nil {
		return nil, false
	}
	return &index, true
}

func writeRecallIndex(path string, index *recallIndex) error {
	return atomicfs.WriteJSON(path, index, 0o644)
}

func recallSessionEventsSize(dir string) int64 {
	info, err := os.Stat(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		// A session with no events.jsonl of its own (a mirror whose source
		// is gone) sorts before every real size, so it is re-read when the
		// file appears.
		return -1
	}
	return info.Size()
}

func indexTurnsForSession(dir, session string) []recallIndexTurn {
	turns := pastRecallTurns(dir, session)
	out := make([]recallIndexTurn, 0, len(turns))
	for _, turn := range turns {
		out = append(out, recallIndexTurn{Session: turn.Session, Ordinal: turn.Ordinal, User: turn.User, Reply: turn.Reply})
	}
	return out
}

// BuildRecallIndex builds the index for one owner from scratch and writes it.
// A deleted index costs exactly this; the recall path calls it in the
// background on its first query.
func BuildRecallIndex(homeRoot string, ref ScopeRef) error {
	index := &recallIndex{Version: RecallIndexVersion, Sessions: map[string]recallIndexSession{}}
	sessions := SearchListSessions(homeRoot, ref.Scope, ref.Owner, "", nil, nil)
	for _, session := range sessions {
		dir := SearchSessionDir(homeRoot, ref.Scope, ref.Owner, session)
		index.Sessions[session] = recallIndexSession{EventsSize: recallSessionEventsSize(dir)}
		index.Turns = append(index.Turns, indexTurnsForSession(dir, session)...)
	}
	return writeRecallIndex(RecallIndexPath(homeRoot, ref), index)
}

// refreshRecallIndex brings the index up to date with every session but skip
// (the live session doesn't need to be current). Sessions whose size matches
// stay untouched; changed ones are re-read. The deadline bounds how long the
// refresh may run; whatever was refreshed is kept.
func refreshRecallIndex(index *recallIndex, homeRoot string, ref ScopeRef, skip string, deadline time.Time) bool {
	present := SearchListSessions(homeRoot, ref.Scope, ref.Owner, "", nil, nil)
	presentSet := make(map[string]struct{}, len(present))
	for _, session := range present {
		presentSet[session] = struct{}{}
	}

	turnsBySession := map[string][]recallIndexTurn{}
	for _, turn := range index.Turns {
		turnsBySession[turn.Session] = append(turnsBySession[turn.Session], turn)
	}

	changed := false
	for session := range index.Sessions {
		if _, ok := presentSet[session]; !ok {
			delete(index.Sessions, session)
			delete(turnsBySession, session)
			changed = true
		}
	}

	for _, session := range present {
		if session == skip {
			continue
		}
		dir := SearchSessionDir(homeRoot, ref.Scope, ref.Owner, session)
		size := recallSessionEventsSize(dir)
		if old, ok := index.Sessions[session]; ok && old.EventsSize == size {
			continue
		}
		if !time.Now().Before(deadline) {
			// Out of budget: the rest is picked up by the next query.
			break
		}
		turnsBySession[session] = indexTurnsForSession(dir, session)
		index.Sessions[session] = recallIndexSession{EventsSize: size}
		changed = true
	}
	if !changed {
		return false
	}
	rebuilt := make([]recallIndexTurn, 0, len(index.Turns))
	ordered := make([]string, 0, len(turnsBySession))
	for session := range turnsBySession {
		ordered = append(ordered, session)
	}
	sort.Strings(ordered)
	for _, session := range ordered {
		rebuilt = append(rebuilt, turnsBySession[session]...)
	}
	index.Turns = rebuilt
	return true
}

// beginRecallIndexBuild starts a background build for the owner unless one is
// already running. The recall path never waits on this lock: a first build of
// a large store can take a while, and later queries answer from what exists.
func beginRecallIndexBuild(homeRoot string, ref ScopeRef) {
	lock := recallIndexLock(RecallIndexPath(homeRoot, ref))
	if !lock.TryLock() {
		return
	}
	go func() {
		defer lock.Unlock()
		_ = BuildRecallIndex(homeRoot, ref)
	}()
}

// recallIndexForQuery returns the index to score from, or nil when the index
// is still being built and the deadline passed. The first query after an
// upgrade (or a deletion) starts the build and waits for it only up to the
// deadline; queries with a warm index read it and pick up changed sessions.
func recallIndexForQuery(homeRoot string, ref ScopeRef, skip string, deadline time.Time) (*recallIndex, bool) {
	path := RecallIndexPath(homeRoot, ref)
	if index, ok := loadRecallIndex(path); ok {
		if lock := recallIndexLock(path); lock.TryLock() {
			refreshed := false
			func() {
				defer lock.Unlock()
				refreshed = refreshRecallIndex(index, homeRoot, ref, skip, deadline)
			}()
			if refreshed {
				_ = writeRecallIndex(path, index)
			}
		}
		return index, true
	}
	beginRecallIndexBuild(homeRoot, ref)
	for {
		if index, ok := loadRecallIndex(path); ok {
			return index, true
		}
		if !time.Now().Before(deadline) {
			return nil, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
