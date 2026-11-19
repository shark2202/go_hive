package soul

// Parity-exempt: no upstream counterpart. Upstream revises a queen's soul in a
// separate daily reflection that reads her chats
// (framework/agents/queen/soul_reflection.py); the Go port has no chat-reading
// soul reflection, so a finished memory reflection sediments its conclusions
// here instead. Storage stays upstream-shaped: soul.json, history/v<n>.json and
// log.jsonl are all written through Save, so the soul routes keep reading it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// SedimentFile is the bookkeeping file kept beside soul.json: the items the
	// sediment itself wrote (so it may drop them again when a section fills)
	// and when it last wrote. It never holds soul content.
	SedimentFile = "sediment.json"
	// SedimentSection is the section conclusions land in when the configuration
	// has it; otherwise the first configured section is used.
	SedimentSection = "now"
	// SedimentAuthor is the log.jsonl author of a sedimented version, so the
	// history tells an automatic write apart from an owner's edit.
	SedimentAuthor = "reflection"
	// MaxSedimentItemRunes caps one conclusion. The soul is small by design and
	// a memory description is a line, not an essay.
	MaxSedimentItemRunes = 240
	// MaxSedimentConclusions caps how many conclusions one reflection may add.
	MaxSedimentConclusions = 3
	// maxSedimentEntries caps the bookkeeping list; older entries are dropped
	// from tracking, never from the soul.
	maxSedimentEntries = 500
)

var (
	// ErrInvalidQueenID rejects a queen id that is not exactly one path element,
	// so a soul write can never escape queens/<id>/soul.
	ErrInvalidQueenID = errors.New("soul: invalid queen id")
	// ErrUnknownQueen rejects a sediment for a queen that has no directory yet:
	// creating one would invent a queen.
	ErrUnknownQueen = errors.New("soul: queen has no directory")
)

// SedimentConclusion is one durable learning a reflection produced.
type SedimentConclusion struct {
	Text string
	// Date is the day the conclusion is about (YYYY-MM-DD). Empty means the
	// day of the write.
	Date string
	// Source names where it came from (a memory file). It is bookkeeping only:
	// soul items carry text and date, exactly like an upstream add_item.
	Source string
}

// SedimentEntry is one item this queen's sediment wrote.
type SedimentEntry struct {
	Section string `json:"section"`
	Text    string `json:"text"`
	At      string `json:"at"`
}

// SedimentState is sediment.json: what the sediment wrote, and when.
type SedimentState struct {
	QueenID   string          `json:"queen_id"`
	LastRun   string          `json:"last_run"`
	UpdatedAt string          `json:"updated_at"`
	Version   int             `json:"version"`
	LastAdded int             `json:"last_added"`
	Written   []SedimentEntry `json:"written"`
}

type SedimentOptions struct {
	QueensDir   string
	QueenID     string
	Config      *SoulConfig
	Conclusions []SedimentConclusion
	// Author overrides SedimentAuthor (tests, or a different automatic writer).
	Author string
	// Now is the clock the write is stamped with; the zero value means time.Now.
	Now time.Time
}

type SedimentResult struct {
	// Written is false when nothing new was added: no file was touched.
	Written bool
	Version int
	Section string
	Added   []Item
	// Evicted holds earlier sedimented items dropped to make room. Human
	// items are never evicted.
	Evicted []Item
	// Duplicates counts conclusions the soul already carries.
	Duplicates int
	// Dropped counts conclusions that were empty or did not fit.
	Dropped int
}

// ValidQueenID reports whether id is exactly one ordinary path element.
func ValidQueenID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.HasPrefix(id, ".") {
		return false
	}
	if strings.ContainsAny(id, "/\\\x00") || filepath.IsAbs(id) || filepath.VolumeName(id) != "" {
		return false
	}
	return filepath.Base(id) == id
}

// SedimentStateFromJSON reads a stored sediment.json value.
func SedimentStateFromJSON(value any) *SedimentState {
	mapping, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	state := &SedimentState{
		QueenID:   pyStringOrEmpty(mapping["queen_id"]),
		LastRun:   pyStringOrEmpty(mapping["last_run"]),
		UpdatedAt: pyStringOrEmpty(mapping["updated_at"]),
	}
	if version, ok := AsInt(mapping["version"]); ok {
		state.Version = version
	}
	if added, ok := AsInt(mapping["last_added"]); ok {
		state.LastAdded = added
	}
	entries, _ := mapping["written"].([]any)
	for _, raw := range entries {
		entry, isMap := raw.(map[string]any)
		if !isMap {
			continue
		}
		text := pyStringOrEmpty(entry["text"])
		if text == "" {
			continue
		}
		state.Written = append(state.Written, SedimentEntry{
			Section: pyStringOrEmpty(entry["section"]),
			Text:    text,
			At:      pyStringOrEmpty(entry["at"]),
		})
	}
	return state
}

// LoadSedimentState reads queens/<id>/soul/sediment.json; nil when absent.
func LoadSedimentState(queensDir, queenID string) *SedimentState {
	if !ValidQueenID(queenID) {
		return nil
	}
	return SedimentStateFromJSON(readJSONFile(filepath.Join(SoulDir(queensDir, queenID), SedimentFile)))
}

func (s *SedimentState) forget(section, text string) {
	if s == nil {
		return
	}
	key := normalizeSoulText(text)
	kept := s.Written[:0]
	for _, entry := range s.Written {
		if entry.Section == section && normalizeSoulText(entry.Text) == key {
			continue
		}
		kept = append(kept, entry)
	}
	s.Written = kept
}

// oldestTrackedIndex finds the first item of section that this sediment wrote,
// which is the oldest one it may drop: state.Written is in write order.
func (s *SedimentState) oldestTrackedIndex(section string, items []Item) int {
	if s == nil {
		return -1
	}
	at := map[string]int{}
	for index, item := range items {
		at[normalizeSoulText(item.Text)] = index
	}
	for _, entry := range s.Written {
		if entry.Section != section {
			continue
		}
		if index, ok := at[normalizeSoulText(entry.Text)]; ok {
			return index
		}
	}
	return -1
}

func normalizeSoulText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func clipRunes(text string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit-1]) + "…"
}

func sedimentSectionKey(cfg *SoulConfig) string {
	if cfg == nil || len(cfg.Sections) == 0 {
		return ""
	}
	for _, section := range cfg.Sections {
		if section.Key == SedimentSection {
			return section.Key
		}
	}
	return cfg.Sections[0].Key
}

func sedimentSectionLimit(cfg *SoulConfig, key string) int {
	for _, section := range cfg.Sections {
		if section.Key == key {
			return section.Limit
		}
	}
	return 0
}

func copySections(sections map[string][]Item) map[string][]Item {
	copied := make(map[string][]Item, len(sections))
	for key, items := range sections {
		copied[key] = append([]Item(nil), items...)
	}
	return copied
}

var sedimentLocks sync.Map

// lockSediment serializes writes per queen directory: two sessions reflecting
// for the same queen must not lose one version to the other.
func lockSediment(queensDir, queenID string) func() {
	value, _ := sedimentLocks.LoadOrStore(filepath.Join(queensDir, queenID), &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// withinRoot reports whether candidate is root itself or below it.
func withinRoot(root, candidate string) bool {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absoluteCandidate, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteCandidate)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Sediment appends a finished reflection's conclusions to the queen's soul as a
// new version. Items are only appended; existing items — in particular a
// human's edits — are never rewritten. A conclusion the soul already carries is
// skipped, and when every conclusion is skipped nothing is written at all.
//
// When the target section is full, the oldest items this sediment wrote earlier
// are dropped first; if the section is still full the conclusion is skipped
// rather than touching an item the sediment did not write.
func Sediment(opts SedimentOptions) (SedimentResult, error) {
	result := SedimentResult{}
	if opts.Config == nil || len(opts.Conclusions) == 0 {
		return result, nil
	}
	if !ValidQueenID(opts.QueenID) {
		return result, ErrInvalidQueenID
	}
	queenRoot := filepath.Join(opts.QueensDir, opts.QueenID)
	if !isDirectory(queenRoot) {
		return result, ErrUnknownQueen
	}
	resolvedQueens := resolvePath(opts.QueensDir)
	if !withinRoot(resolvedQueens, resolvePath(queenRoot)) {
		return result, ErrInvalidQueenID
	}
	section := sedimentSectionKey(opts.Config)
	limit := sedimentSectionLimit(opts.Config, section)
	if section == "" || limit <= 0 {
		return result, nil
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	author := strings.TrimSpace(opts.Author)
	if author == "" {
		author = SedimentAuthor
	}
	today := now.Format("2006-01-02")

	unlock := lockSediment(resolvedQueens, opts.QueenID)
	defer unlock()

	current := Load(opts.QueensDir, opts.QueenID)
	result.Version = current.Version
	seen := map[string]struct{}{}
	for _, items := range current.Sections {
		for _, item := range items {
			seen[normalizeSoulText(item.Text)] = struct{}{}
		}
	}

	additions := make([]Item, 0, MaxSedimentConclusions)
	for _, conclusion := range opts.Conclusions {
		text := clipRunes(normalizeSoulText(conclusion.Text), MaxSedimentItemRunes)
		if text == "" {
			result.Dropped++
			continue
		}
		if len(additions) >= MaxSedimentConclusions {
			result.Dropped++
			continue
		}
		key := normalizeSoulText(text)
		if _, exists := seen[key]; exists {
			result.Duplicates++
			continue
		}
		seen[key] = struct{}{}
		day := strings.TrimSpace(conclusion.Date)
		if day == "" {
			day = today
		} else if parsed, err := time.Parse("2006-01-02", day); err == nil {
			day = parsed.Format("2006-01-02")
		} else {
			day = today
		}
		additions = append(additions, Item{Text: text, Date: day})
	}
	if len(additions) == 0 {
		return result, nil
	}

	state := LoadSedimentState(opts.QueensDir, opts.QueenID)
	if state == nil {
		state = &SedimentState{QueenID: opts.QueenID}
	}
	sections := copySections(current.Sections)
	items := sections[section]
	used := SectionChars(items)
	for _, addition := range additions {
		needed := len([]rune(addition.Text))
		for used+needed > limit {
			evicted := state.oldestTrackedIndex(section, items)
			if evicted < 0 {
				break
			}
			guest := items[evicted]
			result.Evicted = append(result.Evicted, guest)
			state.forget(section, guest.Text)
			items = append(items[:evicted], items[evicted+1:]...)
			used -= len([]rune(guest.Text))
		}
		if used+needed > limit {
			result.Dropped++
			continue
		}
		items = append(items, addition)
		used += needed
		result.Added = append(result.Added, addition)
	}
	if len(result.Added) == 0 {
		return result, nil
	}
	sections[section] = items

	edits := make([]Edit, 0, len(result.Evicted)+len(result.Added))
	for _, item := range result.Evicted {
		edits = append(edits, Edit{
			Op:       "remove",
			Section:  section,
			Old:      item.Text,
			Evidence: []map[string]any{},
			Reason:   "older than the section keeps; it was sedimented earlier",
		})
	}
	for _, item := range result.Added {
		edits = append(edits, Edit{
			Op:       "add",
			Section:  section,
			Text:     item.Text,
			Date:     item.Date,
			Evidence: []map[string]any{},
			Reason:   "memory reflection conclusion",
		})
	}
	note := fmt.Sprintf("%d from memory reflection", len(result.Added))
	if len(result.Evicted) > 0 {
		note += fmt.Sprintf(", %d older sedimented items dropped", len(result.Evicted))
	}
	updated, err := Save(opts.QueensDir, opts.Config, opts.QueenID, sections, author, note, edits)
	if err != nil {
		return result, err
	}
	result.Written = true
	result.Version = updated.Version
	result.Section = section

	state.QueenID = opts.QueenID
	state.LastRun = today
	state.UpdatedAt = updated.UpdatedAt
	state.Version = updated.Version
	state.LastAdded = len(result.Added)
	for _, item := range result.Added {
		state.Written = append(state.Written, SedimentEntry{Section: section, Text: item.Text, At: updated.UpdatedAt})
	}
	if len(state.Written) > maxSedimentEntries {
		state.Written = state.Written[len(state.Written)-maxSedimentEntries:]
	}
	// The soul is already saved; a bookkeeping failure only costs future
	// eviction of these items (dedupe reads the soul itself).
	if err := writeJSONFile(filepath.Join(SoulDir(opts.QueensDir, opts.QueenID), SedimentFile), state); err != nil {
		return result, err
	}
	return result, nil
}

// resolvePath follows symlinks when the path exists; otherwise it is made
// absolute. Used to keep a soul write inside queensDir even when a queen
// directory is a symlink.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}
