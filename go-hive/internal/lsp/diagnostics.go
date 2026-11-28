package lsp

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"
)

// diagnosticEntry is the last publish for one URI.
type diagnosticEntry struct {
	version    int // -1 when the server omitted version
	items      []Diagnostic
	received   time.Time
	generation int64
}

// diagnosticStore keeps push diagnostics per URI. Publishes for an older
// version are dropped so a late stale publish cannot overwrite current
// results.
type diagnosticStore struct {
	mu         sync.Mutex
	byURI      map[string]*diagnosticEntry
	generation int64
}

func newDiagnosticStore() *diagnosticStore {
	return &diagnosticStore{byURI: map[string]*diagnosticEntry{}}
}

// Publish records one textDocument/publishDiagnostics notification.
func (s *diagnosticStore) Publish(uri string, version *int, items []Diagnostic) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := -1
	if version != nil {
		v = *version
	}
	entry := s.byURI[uri]
	if entry != nil && entry.version >= 0 && v >= 0 && v < entry.version {
		return
	}
	s.generation++
	s.byURI[uri] = &diagnosticEntry{
		version:    v,
		items:      items,
		received:   time.Now(),
		generation: s.generation,
	}
}

// Latest returns the current entry for uri.
func (s *diagnosticStore) Latest(uri string) (DiagnosticSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byURI[uri]
	if !ok {
		return DiagnosticSnapshot{URI: uri, Version: -1}, false
	}
	return snapshotFrom(uri, entry), true
}

// DiagnosticSnapshot is one URI's diagnostics plus freshness evidence.
type DiagnosticSnapshot struct {
	URI      string
	Version  int
	Items    []Diagnostic
	Received time.Time
	Fresh    bool
	Note     string
}

func snapshotFrom(uri string, entry *diagnosticEntry) DiagnosticSnapshot {
	items := append([]Diagnostic(nil), entry.items...)
	return DiagnosticSnapshot{
		URI:      uri,
		Version:  entry.version,
		Items:    items,
		Received: entry.received,
	}
}

// Wait waits for a publish that is current for minVersion. A publish is
// current when its version is >= minVersion. A version-less publish counts
// only when it arrived after the wait started, so a publish from before the
// document's current version can never be mistaken for a clean result.
//
// After the first qualifying publish it waits settle so a burst of diagnostics
// is coalesced; if nothing new arrives the snapshot is fresh.
func (s *diagnosticStore) Wait(ctx context.Context, uri string, minVersion int, settle time.Duration) DiagnosticSnapshot {
	return s.WaitSince(ctx, uri, minVersion, settle, time.Time{})
}

// WaitSince is Wait with an extra lower bound for version-less publishes. A
// version-less publish that arrived at or after since (normally the document's
// open time) counts as current; older one does not, so a stale set can never be
// reported clean.
func (s *diagnosticStore) WaitSince(ctx context.Context, uri string, minVersion int, settle time.Duration, since time.Time) DiagnosticSnapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	if settle < 0 {
		settle = 0
	}
	start := time.Now()
	lastGen := int64(-1)
	var lastChange time.Time
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var latest DiagnosticSnapshot
	var have bool
	for {
		s.mu.Lock()
		entry := s.byURI[uri]
		s.mu.Unlock()
		if entry != nil {
			latest = snapshotFrom(uri, entry)
			have = true
			qualifies := entry.version >= 0 && entry.version >= minVersion
			if entry.version < 0 {
				qualifies = entry.received.After(start) || (!since.IsZero() && entry.received.After(since))
			}
			if !since.IsZero() && entry.received.Before(since) {
				// A publish from before the current document version was sent
				// cannot describe it, even when the server omitted a version.
				qualifies = false
			}
			if qualifies {
				if entry.generation != lastGen {
					lastGen = entry.generation
					lastChange = time.Now()
				}
				if time.Since(lastChange) >= settle {
					latest.Fresh = true
					return latest
				}
			}
		}
		select {
		case <-ctx.Done():
			if !have {
				latest = DiagnosticSnapshot{URI: uri, Version: -1}
				latest.Note = "no diagnostics were published before the wait expired"
				return latest
			}
			if latest.Version >= 0 && latest.Version < minVersion {
				latest.Note = "the last publish is older than the current document version"
			} else {
				latest.Note = "diagnostics were not refreshed before the wait expired"
			}
			return latest
		case <-ticker.C:
		}
	}
}

// dedupeDiagnostics is not used by the renderer (which keys on path+range+
// message); it is retained for unit-level ordering tests of one file.
func dedupeDiagnostics(items []Diagnostic) []Diagnostic {
	seen := map[string]struct{}{}
	out := make([]Diagnostic, 0, len(items))
	for _, item := range items {
		key := item.Message + "\x00" +
			strconv.Itoa(item.Range.Start.Line) + ":" + strconv.Itoa(item.Range.Start.Character) + "-" +
			strconv.Itoa(item.Range.End.Line) + ":" + strconv.Itoa(item.Range.End.Character)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if severityRank(a.Severity) != severityRank(b.Severity) {
			return severityRank(a.Severity) < severityRank(b.Severity)
		}
		if a.Range.Start.Line != b.Range.Start.Line {
			return a.Range.Start.Line < b.Range.Start.Line
		}
		if a.Range.Start.Character != b.Range.Start.Character {
			return a.Range.Start.Character < b.Range.Start.Character
		}
		return a.Message < b.Message
	})
	return out
}

func severityRank(severity int) int {
	if severity <= 0 {
		return 5
	}
	return severity
}
