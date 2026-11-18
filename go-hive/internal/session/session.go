package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Timestamps struct {
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type Progress struct {
	CurrentStep int     `json:"current_step"`
	TotalSteps  int     `json:"total_steps"`
	Percent     float64 `json:"percent"`
	Message     string  `json:"message,omitempty"`
}

type Result struct {
	Success bool           `json:"success"`
	Output  string         `json:"output,omitempty"`
	Error   string         `json:"error,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

type Metrics struct {
	Iterations   int `json:"iterations"`
	ToolCalls    int `json:"tool_calls"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type State struct {
	SchemaVersion  string           `json:"schema_version"`
	SessionID      string           `json:"session_id"`
	StreamID       string           `json:"stream_id,omitempty"`
	CorrelationID  string           `json:"correlation_id,omitempty"`
	Status         Status           `json:"status"`
	GoalID         string           `json:"goal_id"`
	AgentID        string           `json:"agent_id,omitempty"`
	EntryPoint     string           `json:"entry_point,omitempty"`
	Timestamps     Timestamps       `json:"timestamps"`
	Progress       Progress         `json:"progress"`
	Result         Result           `json:"result"`
	DataBuffer     map[string]any   `json:"data_buffer,omitempty"`
	Metrics        Metrics          `json:"metrics"`
	Problems       []map[string]any `json:"problems,omitempty"`
	Decisions      []map[string]any `json:"decisions,omitempty"`
	CurrentRunID   string           `json:"current_run_id,omitempty"`
	PID            *int             `json:"pid,omitempty"`
	IsolationLevel string           `json:"isolation_level,omitempty"`
}

func (s *State) Validate() error {
	if s.SessionID == "" {
		return errors.New("session_id is required")
	}
	if s.GoalID == "" {
		return errors.New("goal_id is required")
	}
	switch s.Status {
	case StatusActive, StatusPaused, StatusCompleted, StatusFailed, StatusCancelled:
	default:
		return fmt.Errorf("invalid status %q", s.Status)
	}
	return nil
}

func (s *State) Touch(now time.Time) {
	s.Timestamps.UpdatedAt = now
	if s.Timestamps.CreatedAt.IsZero() {
		s.Timestamps.CreatedAt = now
	}
}

type Store struct {
	base string
}

var sessionIDPattern = regexp.MustCompile(`^session_[0-9]{8}_[0-9]{6}_[a-z0-9]{8}$`)

func NewStore(base string) *Store { return &Store{base: base} }

func (s *Store) Base() string { return s.base }

func (s *Store) GenerateID(now time.Time) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return "session_" + now.Format("20060102_150405") + "_" + hex.EncodeToString(b[:]), nil
}

func ValidID(id string) bool { return sessionIDPattern.MatchString(id) }

func (s *Store) Dir(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	dir := filepath.Join(s.base, id)
	resolved, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	baseAbs, err := filepath.Abs(s.base)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(baseAbs, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("session id escapes store: %q", id)
	}
	return dir, nil
}

func (s *Store) Create(goalID string, now time.Time) (*State, string, error) {
	id, err := s.GenerateID(now)
	if err != nil {
		return nil, "", err
	}
	dir, err := s.Dir(id)
	if err != nil {
		return nil, "", err
	}
	for _, sub := range []string{"conversations/parts", "conversations/partials", "artifacts", "checkpoints", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			return nil, "", fmt.Errorf("create session dir: %w", err)
		}
	}
	state := &State{
		SchemaVersion:  "1.1",
		SessionID:      id,
		Status:         StatusActive,
		GoalID:         goalID,
		EntryPoint:     "start",
		DataBuffer:     map[string]any{},
		IsolationLevel: "shared",
	}
	state.Touch(now)
	if err := s.WriteState(state); err != nil {
		return nil, "", err
	}
	conv := s.Conversations(id)
	if err := conv.WriteMeta(map[string]any{"session_id": id, "phase_id": "", "created_at": now.UTC().Format(time.RFC3339Nano)}); err != nil {
		return nil, "", err
	}
	return state, dir, nil
}

func (s *Store) ReadState(id string) (*State, error) {
	dir, err := s.Dir(id)
	if err != nil {
		return nil, err
	}
	var state State
	if err := atomicfs.ReadJSON(filepath.Join(dir, "state.json"), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) WriteState(state *State) error {
	if err := state.Validate(); err != nil {
		return err
	}
	dir, err := s.Dir(state.SessionID)
	if err != nil {
		return err
	}
	return atomicfs.WriteJSON(filepath.Join(dir, "state.json"), state, 0o644)
}

func (s *Store) UpdateState(id string, now time.Time, fn func(*State) error) (*State, error) {
	state, err := s.ReadState(id)
	if err != nil {
		return nil, err
	}
	if err := fn(state); err != nil {
		return nil, err
	}
	state.Touch(now)
	if err := s.WriteState(state); err != nil {
		return nil, err
	}
	return state, nil
}

func (s *Store) List(status Status, limit int) ([]*State, error) {
	entries, err := os.ReadDir(s.base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*State
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		st, err := s.ReadState(e.Name())
		if err != nil {
			continue
		}
		if status != "" && st.Status != status {
			continue
		}
		out = append(out, st)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamps.CreatedAt.After(out[j].Timestamps.CreatedAt)
	})
	return out, nil
}

func (s *Store) Conversations(id string) *ConversationStore {
	dir, err := s.Dir(id)
	if err != nil {
		return &ConversationStore{base: filepath.Join(s.base, "__invalid__")}
	}
	return &ConversationStore{base: filepath.Join(dir, "conversations")}
}

func (s *Store) ArtifactsDir(id string) (string, error) {
	dir, err := s.Dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "artifacts"), nil
}

func (s *Store) LogsDir(id string) (string, error) {
	dir, err := s.Dir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

type ConversationStore struct {
	base string
}

type Cursor struct {
	Iteration         int              `json:"iteration"`
	Turn              int              `json:"turn"`
	AccumulatedOutput string           `json:"accumulated_output"`
	ToolCalls         int              `json:"tool_calls"`
	StallRepeat       int              `json:"stall_repeat"`
	LastFingerprint   string           `json:"last_fingerprint,omitempty"`
	PendingInputs     []map[string]any `json:"pending_inputs,omitempty"`
	ParkReason        string           `json:"park_reason,omitempty"`
	UpdatedAt         time.Time        `json:"updated_at"`
}

func (c *ConversationStore) AddPart(seq int, data map[string]any) error {
	if seq < 0 {
		return errors.New("negative part sequence")
	}
	name := fmt.Sprintf("%010d.json", seq)
	return atomicfs.WriteJSON(filepath.Join(c.base, "parts", name), data, 0o644)
}

func (c *ConversationStore) NextSeq() (int, error) {
	dir := filepath.Join(c.base, "parts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	// The next seq follows the highest part on disk, not the number of parts:
	// compaction deletes parts and a corrupt part is skipped on read, so both
	// would otherwise hand out a seq that already exists.
	maxSeq := -1
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if seq, ok := ParsePartSeq(e.Name()); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq + 1, nil
}

func (c *ConversationStore) ReadParts() ([]map[string]any, error) {
	dir := filepath.Join(c.base, "parts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var out []map[string]any
	for _, name := range names {
		var part map[string]any
		if err := atomicfs.ReadJSON(filepath.Join(dir, name), &part); err != nil {
			continue
		}
		out = append(out, part)
	}
	return out, nil
}

func (c *ConversationStore) PartCount() int {
	parts, _ := c.ReadParts()
	return len(parts)
}

func (c *ConversationStore) WriteMeta(data map[string]any) error {
	return atomicfs.WriteJSON(filepath.Join(c.base, "meta.json"), data, 0o644)
}

// readDerivedJSON reads a derived (rebuildable) JSON file. A file that exists
// but does not decode is treated as absent: callers fall back to defaults
// instead of failing the whole session on a truncated cursor/meta write.
func readDerivedJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		// Corrupt derived state (truncated write, bad type) reads as absent.
		return nil
	}
	return nil
}

func (c *ConversationStore) ReadMeta() (map[string]any, error) {
	var m map[string]any
	if err := readDerivedJSON(filepath.Join(c.base, "meta.json"), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *ConversationStore) WriteCursor(cur Cursor) error {
	return atomicfs.WriteJSON(filepath.Join(c.base, "cursor.json"), cur, 0o644)
}

func (c *ConversationStore) ReadCursor() (Cursor, error) {
	var cur Cursor
	if err := readDerivedJSON(filepath.Join(c.base, "cursor.json"), &cur); err != nil {
		return Cursor{}, err
	}
	return cur, nil
}

func (c *ConversationStore) WritePartial(callID string, data map[string]any) error {
	if callID == "" {
		return errors.New("empty partial id")
	}
	return atomicfs.WriteJSON(filepath.Join(c.base, "partials", sanitize(callID)+".json"), data, 0o644)
}

func (c *ConversationStore) ReadPartial(callID string) (map[string]any, error) {
	var m map[string]any
	if err := atomicfs.ReadJSON(filepath.Join(c.base, "partials", sanitize(callID)+".json"), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *ConversationStore) DeletePartial(callID string) error {
	err := os.Remove(filepath.Join(c.base, "partials", sanitize(callID)+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

type Checkpoint struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Node      string         `json:"node"`
	CreatedAt time.Time      `json:"created_at"`
	Data      map[string]any `json:"data"`
}

type CheckpointStore struct {
	base string
}

func NewCheckpointStore(base string) *CheckpointStore { return &CheckpointStore{base: base} }

func (s *CheckpointStore) Save(cp Checkpoint) error {
	if cp.ID == "" {
		return errors.New("checkpoint id is required")
	}
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	name := fmt.Sprintf("cp_%s_%s_%d.json", sanitize(cp.Type), sanitize(cp.Node), cp.CreatedAt.UnixNano())
	if err := atomicfs.WriteJSON(filepath.Join(s.base, name), cp, 0o644); err != nil {
		return err
	}
	return s.reindex()
}

func (s *CheckpointStore) Index() ([]string, error) {
	var idx []string
	err := atomicfs.ReadJSON(filepath.Join(s.base, "index.json"), &idx)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return idx, nil
}

func (s *CheckpointStore) reindex() error {
	entries, err := os.ReadDir(s.base)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "cp_") && strings.HasSuffix(n, ".json") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return atomicfs.WriteJSON(filepath.Join(s.base, "index.json"), names, 0o644)
}

func ParsePartSeq(name string) (int, bool) {
	if !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
	if err != nil {
		return 0, false
	}
	return n, true
}
