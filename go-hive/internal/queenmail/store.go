package queenmail

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store is the on-disk side of queen mail, rooted at HIVE_HOME/queens:
// mail.jsonl (the audit log), mail/letters (queued and delivered letters) and
// mail/queue.jsonl (which letters await delivery).
type Store struct {
	queensDir string
}

// NewStore roots a store at a Hive home's queens directory.
func NewStore(queensDir string) *Store {
	return &Store{queensDir: queensDir}
}

// QueensDir is where the store finds mail.jsonl.
func (s *Store) QueensDir() string { return s.queensDir }

// MailDir is the letter store's own directory.
func (s *Store) MailDir() string { return filepath.Join(s.queensDir, "mail") }

// LettersDir is where one file per letter lives.
func (s *Store) LettersDir() string { return filepath.Join(s.MailDir(), "letters") }

// QueueFile is the file listing the letters still waiting for delivery.
func (s *Store) QueueFile() string { return filepath.Join(s.MailDir(), "queue.jsonl") }

// AuditFile is queens/mail.jsonl, the log every hop is a line of.
func (s *Store) AuditFile() string { return filepath.Join(s.queensDir, "mail.jsonl") }

// validLetterID keeps a letter id (which becomes a filename) to one segment.
// The queue file is local state, but a tampered id must not reach outside the
// letters directory.
func validLetterID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, "/\\\x00")
}

// Save writes one letter, replacing any earlier copy (delivery fills in the
// session it landed in after the first save).
func (s *Store) Save(letter Letter) error {
	if !validLetterID(letter.ID) {
		return fmt.Errorf("invalid letter id %q", letter.ID)
	}
	if err := os.MkdirAll(s.LettersDir(), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(letter)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(s.LettersDir(), letter.ID+".json"), append(data, '\n'))
}

// Load reads one letter back.
func (s *Store) Load(id string) (Letter, bool) {
	if !validLetterID(id) {
		return Letter{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.LettersDir(), id+".json"))
	if err != nil {
		return Letter{}, false
	}
	var letter Letter
	if err := json.Unmarshal(data, &letter); err != nil || letter.ID == "" {
		return Letter{}, false
	}
	return letter, true
}

// AppendAudit adds one hop to mail.jsonl. Only metadata is written: the letter
// text stays in the sessions (and in the store while delivery is pending).
func (s *Store) AppendAudit(letter Letter) error {
	if err := os.MkdirAll(s.queensDir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(letter.auditRecord())
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.AuditFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

// ReadAudit reads every readable line of mail.jsonl, skipping lines that are
// not JSON objects the way upstream's _read_log does.
func (s *Store) ReadAudit() []Letter {
	file, err := os.Open(s.AuditFile())
	if err != nil {
		return nil
	}
	defer file.Close()
	var records []Letter
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record auditRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		records = append(records, letterFromAudit(record))
	}
	return records
}

// DailyCounts counts the letters each queen sent on day (a local date,
// "2006-01-02"), leaving out dropped lines and replies, as the daily limit does.
func (s *Store) DailyCounts(day string) map[string]int {
	counts := map[string]int{}
	for _, record := range s.ReadAudit() {
		if record.Kind != KindMessage || record.Dropped != "" || !strings.HasPrefix(record.At, day) {
			continue
		}
		counts[record.From]++
	}
	return counts
}

// writeQueue replaces the queue file with ids in order.
func (s *Store) writeQueue(ids []string) error {
	if err := os.MkdirAll(s.MailDir(), 0o755); err != nil {
		return err
	}
	if len(ids) == 0 {
		if err := os.Remove(s.QueueFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	var builder strings.Builder
	for _, id := range ids {
		builder.WriteString(id)
		builder.WriteByte('\n')
	}
	return writeFileAtomic(s.QueueFile(), []byte(builder.String()))
}

// readQueue reads the queued letter ids, dropping blanks and duplicates while
// keeping the order letters were sent in.
func (s *Store) readQueue() []string {
	data, err := os.ReadFile(s.QueueFile())
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ids []string
	for _, line := range strings.Split(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		os.Remove(tempPath)
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}
