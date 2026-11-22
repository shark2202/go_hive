package memory

// Every change to a memory file goes through this file, and is kept.
//
// A memory file used to be overwritten or deleted in place: a rewrite lost
// whatever it didn't restate, a deletion was final, and nothing said who had
// written a file or when it was last true. So:
//
//   - each scope folder keeps ``log.jsonl``, one line per change: when, who,
//     why, on what evidence, and the file's text before and after. A file that
//     is retired leaves the folder and stays in the log, from where it can be
//     restored (Restore).
//   - each file's frontmatter says who wrote it and how fresh it is:
//     author: owner | queen | reflection, learned: YYYY-MM-DD,
//     confirmed: YYYY-MM-DD. A file the owner wrote is theirs: she can't
//     overwrite or retire it (OwnerWritten). A file with no author predates
//     this and is nobody's yet; the owner's first edit claims it. confirmed is
//     what a recalled memory's age counts from, so tidying a file up doesn't
//     make an old fact look new.
//
// Parity: core/framework/agents/queen/memory_log.py
// Parity: core/tests/test_memory_log.py

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const (
	// LogFile is the per-scope change log beside the memory files.
	LogFile = "log.jsonl"
	// AuthorOwner is the user: only they may change or retire their own file.
	AuthorOwner = "owner"
)

// Authors are the writers a memory file's frontmatter may name.
var Authors = []string{AuthorOwner, "queen", "reflection"}

// OwnerWritten reports a file the owner wrote; only they change or retire it.
type OwnerWritten struct {
	Message string
}

func (e *OwnerWritten) Error() string { return e.Message }

// TooLarge reports content that would exceed MaxFileSizeBytes once stamped.
type TooLarge struct {
	Message string
}

func (e *TooLarge) Error() string { return e.Message }

// NoChange reports a restore target that names no change in the file's log.
type NoChange struct {
	Message string
}

func (e *NoChange) Error() string { return e.Message }

// WriteOptions carries the context of one memory write. The zero value keeps
// the upstream defaults: the change claims the file and confirms it as of now.
type WriteOptions struct {
	// Author is "owner", "queen" or "reflection".
	Author string
	// Reason is the human-readable why, stored in the log entry.
	Reason string
	// Sources is the evidence the change rests on, stored in the log entry.
	Sources []map[string]string
	// Reorganised marks a change that only tidies the file up: it keeps the
	// day the file was last confirmed (upstream ``confirms=False``).
	Reorganised bool
	// KeepAuthor marks a change that leaves the file whoever's it was, like
	// the settings page's identity lines (upstream ``claim=False``).
	KeepAuthor bool
	// Now is the clock for tests; the zero value means time.Now().
	Now time.Time
}

// HistoryEntry is one line of log.jsonl plus its 0-based line number.
type HistoryEntry struct {
	N       int     `json:"n"`
	At      string  `json:"at"`
	Op      string  `json:"op"`
	File    string  `json:"file"`
	Author  string  `json:"author"`
	Reason  string  `json:"reason"`
	Sources any     `json:"sources"`
	Before  *string `json:"before"`
	After   *string `json:"after"`
}

// memoryLogMu serialises read-modify-write sequences against one process's
// memory folders, so an API edit and a reflection write never interleave into
// a half-applied log entry.
var memoryLogMu sync.Mutex

// AuthorOf reports who last wrote a memory file, or "" for one written before
// authors were recorded.
//
// Parity: core/framework/agents/queen/memory_log.py::author_of
func AuthorOf(text string) string {
	author := strings.ToLower(ParseFrontmatter(text)["author"])
	for _, candidate := range Authors {
		if author == candidate {
			return author
		}
	}
	return ""
}

// ConfirmedOn reports the last day a conversation supported the file, when it
// says.
//
// Parity: core/framework/agents/queen/memory_log.py::confirmed_on
func ConfirmedOn(text string) (string, bool) {
	day := Day(ParseFrontmatter(text)["confirmed"])
	return day, day != ""
}

// Day normalises value to YYYY-MM-DD, or "" when it isn't a date. The first
// ten characters are read, so an ISO timestamp counts as its day.
//
// Parity: core/framework/agents/queen/memory_log.py::_day
func Day(value string) string {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) > 10 {
		trimmed = trimmed[:10]
	}
	for _, layout := range []string{"2006-01-02", "20060102"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return ""
}

type frontmatterField struct {
	key   string
	value string
}

// withFrontmatter returns text with fields set in its frontmatter, which is
// added when it has none.
//
// Parity: core/framework/agents/queen/memory_log.py::_with_frontmatter
func withFrontmatter(text string, fields []frontmatterField) string {
	loc := frontmatterRe.FindStringSubmatchIndex(text)
	if loc == nil {
		head := make([]string, 0, len(fields))
		for _, field := range fields {
			head = append(head, field.key+": "+field.value)
		}
		return "---\n" + strings.Join(head, "\n") + "\n---\n\n" + strings.TrimLeftFunc(text, unicode.IsSpace)
	}
	lines := splitLines(text[loc[2]:loc[3]])
	for _, field := range fields {
		at := -1
		for index, line := range lines {
			key, _, _ := strings.Cut(line, ":")
			if strings.ToLower(strings.TrimSpace(key)) == field.key {
				at = index
				break
			}
		}
		if at < 0 {
			lines = append(lines, field.key+": "+field.value)
		} else {
			lines[at] = field.key + ": " + field.value
		}
	}
	return "---\n" + strings.Join(lines, "\n") + text[loc[3]:]
}

// readMaybe reads path, reporting whether it exists: an empty file exists, a
// missing one does not.
func readMaybe(path string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// guard refuses a non-owner write over a file the owner wrote.
//
// Parity: core/framework/agents/queen/memory_log.py::_guard
func guard(before string, exists bool, filename, author string) error {
	if author != AuthorOwner && exists && AuthorOf(before) == AuthorOwner {
		return &OwnerWritten{Message: filename + " was written by the owner; write what you learned to another file and tell them where the two differ"}
	}
	return nil
}

// appendLog appends one change entry to memory_dir/log.jsonl.
func appendLog(memoryDir string, entry HistoryEntry) error {
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return err
	}
	line := marshalLogEntry(entry)
	file, err := os.OpenFile(filepath.Join(memoryDir, LogFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(append(line, '\n'))
	return err
}

// tarjanLogEntry renders one entry the way Python's json.dumps does: fixed key
// order, ", " between members and ": " after each key.
func marshalLogEntry(entry HistoryEntry) []byte {
	parts := []string{
		`"at": ` + pyJSONString(entry.At),
		`"op": ` + pyJSONString(entry.Op),
		`"file": ` + pyJSONString(entry.File),
		`"author": ` + pyJSONString(entry.Author),
		`"reason": ` + pyJSONString(entry.Reason),
		`"sources": ` + pyJSON(entry.Sources),
		`"before": ` + pyJSON(entry.Before),
		`"after": ` + pyJSON(entry.After),
	}
	return []byte("{" + strings.Join(parts, ", ") + "}")
}

// pyJSON renders the small JSON value space the log uses with Python's
// separators (", ", ": "), so a line written here reads exactly like the
// reference's.
func pyJSON(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return pyJSONString(typed)
	case *string:
		if typed == nil {
			return "null"
		}
		return pyJSONString(*typed)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case []map[string]string:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, pyJSON(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string]string:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, pyJSONString(key)+": "+pyJSONString(typed[key]))
		}
		return "{" + strings.Join(items, ", ") + "}"
	}
	return "null"
}

// pyJSONString encodes a string as JSON without HTML escaping, matching
// Python's json.dumps(ensure_ascii=False).
func pyJSONString(value string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(buf.String(), "\n")
}

// Write makes content the memory file filename, and logs the change. It
// returns the text written.
//
// Parity: core/framework/agents/queen/memory_log.py::write
func Write(memoryDir, filename, content string, opts WriteOptions) (string, error) {
	memoryLogMu.Lock()
	defer memoryLogMu.Unlock()

	path := filepath.Join(memoryDir, filename)
	before, exists := readMaybe(path)
	if err := guard(before, exists, filename, opts.Author); err != nil {
		return "", err
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	today := now.Format("2006-01-02")
	old := ParseFrontmatter(before)
	upcoming := ParseFrontmatter(content)

	author := opts.Author
	if opts.KeepAuthor {
		// A change that doesn't claim the file leaves it whoever's it was:
		// nobody's, when it is new.
		author = AuthorOf(before)
	}
	fields := make([]frontmatterField, 0, 3)
	if author != "" {
		fields = append(fields, frontmatterField{key: "author", value: author})
	}
	learned := Day(old["learned"])
	if learned == "" {
		candidate := Day(upcoming["learned"])
		if candidate == "" {
			candidate = today
		}
		learned = minDay(candidate, today)
	}
	fields = append(fields, frontmatterField{key: "learned", value: learned})
	confirmed := today
	if opts.Reorganised {
		confirmed = Day(old["confirmed"])
		if confirmed == "" {
			candidate := Day(upcoming["confirmed"])
			if candidate == "" {
				candidate = today
			}
			confirmed = minDay(candidate, today)
		}
	}
	fields = append(fields, frontmatterField{key: "confirmed", value: confirmed})

	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	text := withFrontmatter(content, fields)
	if len(text) > MaxFileSizeBytes {
		return "", &TooLarge{Message: fmt.Sprintf("Content exceeds %d byte limit.", MaxFileSizeBytes)}
	}
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return "", err
	}
	if err := atomicfs.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	beforePointer := (*string)(nil)
	if exists {
		beforePointer = &before
	}
	entry := HistoryEntry{
		At:      logTimestamp(now),
		Op:      "write",
		File:    filename,
		Author:  opts.Author,
		Reason:  opts.Reason,
		Sources: opts.Sources,
		Before:  beforePointer,
		After:   &text,
	}
	if entry.Sources == nil {
		entry.Sources = []map[string]string{}
	}
	if err := appendLog(memoryDir, entry); err != nil {
		return "", err
	}
	return text, nil
}

// Retire takes a memory file out of use. Its text stays in the log.
//
// Parity: core/framework/agents/queen/memory_log.py::retire
func Retire(memoryDir, filename, author, reason string, now time.Time) error {
	memoryLogMu.Lock()
	defer memoryLogMu.Unlock()

	path := filepath.Join(memoryDir, filename)
	before, exists := readMaybe(path)
	if !exists {
		return fmt.Errorf("%s: %w", filename, os.ErrNotExist)
	}
	if err := guard(before, exists, filename, author); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	entry := HistoryEntry{
		At:      logTimestamp(now),
		Op:      "retire",
		File:    filename,
		Author:  author,
		Reason:  reason,
		Sources: []map[string]string{},
		Before:  &before,
	}
	return appendLog(memoryDir, entry)
}

// History reports the changes made in memoryDir (to filename when given),
// oldest first, each with its line number in the log.
//
// Parity: core/framework/agents/queen/memory_log.py::history
func History(memoryDir string, filename *string) []HistoryEntry {
	memoryLogMu.Lock()
	defer memoryLogMu.Unlock()
	return historyLocked(memoryDir, filename)
}

// historyLocked is History without the lock, for callers that already hold it.
func historyLocked(memoryDir string, filename *string) []HistoryEntry {
	entries := []HistoryEntry{}
	data, err := os.ReadFile(filepath.Join(memoryDir, LogFile))
	if err != nil {
		return entries
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		var decoded map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err == nil && decoded != nil {
			file, isString := decoded["file"].(string)
			if filename == nil || (isString && file == *filename) {
				entries = append(entries, historyEntryFromMap(line, decoded))
			}
		}
		line++
	}
	return entries
}

func historyEntryFromMap(n int, decoded map[string]any) HistoryEntry {
	entry := HistoryEntry{N: n}
	entry.At, _ = decoded["at"].(string)
	entry.Op, _ = decoded["op"].(string)
	entry.File, _ = decoded["file"].(string)
	entry.Author, _ = decoded["author"].(string)
	entry.Reason, _ = decoded["reason"].(string)
	if sources, ok := decoded["sources"]; ok {
		entry.Sources = sources
	} else {
		entry.Sources = []any{}
	}
	entry.Before = stringPointer(decoded["before"])
	entry.After = stringPointer(decoded["after"])
	return entry
}

func stringPointer(value any) *string {
	text, ok := value.(string)
	if !ok {
		return nil
	}
	return &text
}

// Retired reports the files whose last change retired them, newest first:
// their retirement's log entry.
//
// Parity: core/framework/agents/queen/memory_log.py::retired
func Retired(memoryDir string) []HistoryEntry {
	memoryLogMu.Lock()
	defer memoryLogMu.Unlock()

	last := map[string]HistoryEntry{}
	for _, entry := range historyLocked(memoryDir, nil) {
		last[entry.File] = entry
	}
	gone := []HistoryEntry{}
	for name, entry := range last {
		if entry.Op != "retire" {
			continue
		}
		if _, err := os.Stat(filepath.Join(memoryDir, name)); err != nil {
			gone = append(gone, entry)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].N > gone[j].N })
	return gone
}

// Restore brings filename back to what it said after change n (before it, for
// a retirement). The owner's doing.
//
// Parity: core/framework/agents/queen/memory_log.py::restore
func Restore(memoryDir, filename string, n int, now time.Time) (string, error) {
	memoryLogMu.Lock()
	defer memoryLogMu.Unlock()

	var found *HistoryEntry
	for _, entry := range historyLocked(memoryDir, &filename) {
		if entry.N == n {
			candidate := entry
			found = &candidate
			break
		}
	}
	text, ok := "", false
	if found != nil {
		// Python's ``entry.get("after") or entry.get("before")``: an empty
		// string falls through to the other side.
		if found.After != nil && *found.After != "" {
			text, ok = *found.After, true
		} else if found.Before != nil && *found.Before != "" {
			text, ok = *found.Before, true
		}
	}
	if !ok {
		return "", &NoChange{Message: fmt.Sprintf("%s has no change %d to go back to", filename, n)}
	}
	if now.IsZero() {
		now = time.Now()
	}
	path := filepath.Join(memoryDir, filename)
	before, exists := readMaybe(path)
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		return "", err
	}
	// As it was, author and dates included: going back doesn't make it the
	// owner's or today's.
	if err := atomicfs.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", err
	}
	beforePointer := (*string)(nil)
	if exists {
		beforePointer = &before
	}
	entry := HistoryEntry{
		At:      logTimestamp(now),
		Op:      "restore",
		File:    filename,
		Author:  AuthorOwner,
		Reason:  fmt.Sprintf("back to change %d", n),
		Sources: []map[string]string{},
		Before:  beforePointer,
		After:   &text,
	}
	if err := appendLog(memoryDir, entry); err != nil {
		return "", err
	}
	return text, nil
}

// logTimestamp renders now the way upstream's
// “datetime.astimezone().isoformat(timespec="seconds")“ does.
func logTimestamp(now time.Time) string {
	return now.In(time.Local).Format("2006-01-02T15:04:05-07:00")
}

func minDay(left, right string) string {
	if left < right {
		return left
	}
	return right
}

// IsOwnerWritten reports whether err is the owner-file guard.
func IsOwnerWritten(err error) bool {
	var target *OwnerWritten
	return errors.As(err, &target)
}

// IsTooLarge reports whether err is the size guard.
func IsTooLarge(err error) bool {
	var target *TooLarge
	return errors.As(err, &target)
}

// IsNoChange reports whether err is a restore miss.
func IsNoChange(err error) bool {
	var target *NoChange
	return errors.As(err, &target)
}
