package sentinel

// On-disk config and records for Sentinel.
//
// Two concerns:
//
//  1. Config — a global ``sentinel`` block in ``<hive home>/configuration.json``
//     (tuning only) merged with a per-colony
//     ``<hive home>/colonies/<id>/notifications.json`` (per-colony opt-in and
//     channel routing). A colony with no config resolves to channel="hive" and
//     sentinel_enabled=true.
//
//  2. Escalation records — one JSON file per open escalation under
//     ``<hive home>/colonies/<id>/escalations/<escalation_id>.json``. Disk is
//     the source of truth so a reply that arrives after a restart still
//     resolves.
//
// Parity: core/framework/sentinel/store.py

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

// Defaults for the global sentinel block (all tunable via configuration.json).
const (
	DefaultClassifyAfterSeconds    = 300.0
	DefaultMaxNudgesBeforeEscalate = 5
	DefaultEscalateWhenUIAttached  = false
	DefaultChannel                 = "hive"
	StatusOpen                     = "open"
	StatusResolved                 = "resolved"
	notificationsFilename          = "notifications.json"
	escalationsDirName             = "escalations"
)

// ErrColonyNotFound mirrors Python's FileNotFoundError when a notifications
// update targets a colony directory that does not exist.
var ErrColonyNotFound = errors.New("colony directory not found")

// Store owns all on-disk paths. Construct with NewStore; the zero value is not
// usable.
type Store struct {
	// ColoniesDir is <hive home>/colonies.
	ColoniesDir string
	// ConfigFile is <hive home>/configuration.json.
	ConfigFile string
	// GlobalConfig overrides the configuration-file read (tests / hosts that
	// already hold the config in memory). Nil reads ConfigFile.
	GlobalConfig func() map[string]any
	// Now supplies record timestamps; nil means time.Now.
	Now func() time.Time
}

// NewStore returns a store rooted at hiveRoot ("" → HIVE_HOME / ~/.hive).
func NewStore(hiveRoot string) *Store {
	if hiveRoot == "" {
		hiveRoot = DefaultHome()
	}
	return &Store{
		ColoniesDir: filepath.Join(hiveRoot, "colonies"),
		ConfigFile:  filepath.Join(hiveRoot, "configuration.json"),
	}
}

var (
	defaultStoreOnce sync.Once
	defaultStore     *Store
)

// DefaultStore returns the process-wide store.
func DefaultStore() *Store {
	defaultStoreOnce.Do(func() { defaultStore = NewStore("") })
	return defaultStore
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) nowISO() string {
	return s.now().UTC().Format(time.RFC3339Nano)
}

// ----- global config ------------------------------------------------------

// GlobalSentinelConfig returns the “sentinel“ block from the global hive
// config, or an empty map.
func (s *Store) GlobalSentinelConfig() map[string]any {
	var raw map[string]any
	if s.GlobalConfig != nil {
		raw = s.GlobalConfig()
	} else if s.ConfigFile != "" {
		data, err := os.ReadFile(s.ConfigFile)
		if err == nil {
			_ = json.Unmarshal(data, &raw)
		}
	}
	if raw == nil {
		return map[string]any{}
	}
	if block, ok := raw["sentinel"].(map[string]any); ok {
		return block
	}
	return map[string]any{}
}

// ClassifyAfterSeconds is the global idle budget before Sentinel evaluates.
func (s *Store) ClassifyAfterSeconds() float64 {
	block := s.GlobalSentinelConfig()
	raw, ok := block["classify_after_seconds"]
	if !ok {
		return DefaultClassifyAfterSeconds
	}
	value, ok := ToFloat(raw)
	if !ok {
		return DefaultClassifyAfterSeconds
	}
	if value < 1.0 {
		return 1.0
	}
	return value
}

// MaxNudgesBeforeEscalate is the auto-continue budget before a heartbeat.
func (s *Store) MaxNudgesBeforeEscalate() int {
	block := s.GlobalSentinelConfig()
	raw, ok := block["max_nudges_before_escalate"]
	if !ok {
		return DefaultMaxNudgesBeforeEscalate
	}
	value, ok := ToInt(raw)
	if !ok {
		return DefaultMaxNudgesBeforeEscalate
	}
	if value < 1 {
		return 1
	}
	return value
}

// EscalateWhenUIAtached reports whether remote channels stay enabled while a
// desktop UI is attached.
func (s *Store) EscalateWhenUIAttached() bool {
	block := s.GlobalSentinelConfig()
	raw, ok := block["escalate_when_ui_attached"]
	if !ok {
		return DefaultEscalateWhenUIAttached
	}
	return PyTruthy(raw)
}

// ----- per-colony notifications config ------------------------------------

// NotificationsConfig is the resolved per-colony notification settings.
// ClassifyAfterSeconds is an optional per-colony idle budget; nil inherits the
// global default.
type NotificationsConfig struct {
	SentinelEnabled      bool           `json:"sentinel_enabled"`
	Channel              string         `json:"channel"`
	Target               map[string]any `json:"target"`
	Allowlist            []string       `json:"allowlist"`
	Thread               map[string]any `json:"thread"`
	ClassifyAfterSeconds *float64       `json:"classify_after_seconds,omitempty"`
}

// validColonyStoreID keeps a colony id, which becomes a directory name, to one
// segment. Store callers include configs and escalation records, so the guard
// lives at the path builder rather than only at the HTTP route.
func validColonyStoreID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if id == "." || id == ".." {
		return false
	}
	for _, ch := range id {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' {
			continue
		}
		return false
	}
	return true
}

// NotificationsConfigPath returns the colony's notifications.json path.
func (s *Store) NotificationsConfigPath(colonyID string) string {
	if !validColonyStoreID(colonyID) {
		return filepath.Join(s.ColoniesDir, "__invalid__", notificationsFilename)
	}
	return filepath.Join(s.ColoniesDir, colonyID, notificationsFilename)
}

func readJSONMap(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// LoadNotificationsConfig loads and resolves a colony's notification config
// best-effort; an unreadable or invalid file resolves to defaults.
func (s *Store) LoadNotificationsConfig(colonyID string) NotificationsConfig {
	raw := map[string]any{}
	if data, err := readJSONMap(s.NotificationsConfigPath(colonyID)); err == nil && data != nil {
		raw = data
	} else if err != nil && !os.IsNotExist(err) {
		sentinelLogf("sentinel: invalid notifications.json for %s; treating as unconfigured", colonyID)
	}

	allowlist := []string{}
	if values, ok := raw["allowlist"].([]any); ok {
		allowlist = make([]string, 0, len(values))
		for _, value := range values {
			allowlist = append(allowlist, PyStr(value))
		}
	}

	channel := DefaultChannel
	if value, ok := raw["channel"].(string); ok && value != "" {
		channel = value
	}

	var perColonyBudget *float64
	if value, ok := raw["classify_after_seconds"]; ok && value != nil {
		if budget, ok := ToFloat(value); ok {
			if budget < 1.0 {
				budget = 1.0
			}
			perColonyBudget = &budget
		}
	}

	target := map[string]any{}
	if value, ok := raw["target"].(map[string]any); ok {
		target = value
	}
	thread := map[string]any{}
	if value, ok := raw["thread"].(map[string]any); ok {
		thread = value
	}

	return NotificationsConfig{
		SentinelEnabled:      notificationsEnabled(raw),
		Channel:              channel,
		Target:               target,
		Allowlist:            allowlist,
		Thread:               thread,
		ClassifyAfterSeconds: perColonyBudget,
	}
}

// SentinelEnabledDefaulted reports the resolved enabled flag; a missing key is
// enabled by default (Python raw.get("sentinel_enabled", True)).
func notificationsEnabled(raw map[string]any) bool {
	value, ok := raw["sentinel_enabled"]
	if !ok {
		return true
	}
	return PyTruthy(value)
}

// NotificationsUpdate carries the fields update_notifications_config writes.
// A nil Channel persists JSON null; a nil ClassifyAfterSeconds clears the
// per-colony override.
type NotificationsUpdate struct {
	SentinelEnabled      bool
	Channel              *string
	Target               map[string]any
	Allowlist            []string
	ClassifyAfterSeconds *float64
}

// UpdateNotificationsConfig persists a colony's notification settings,
// merging with an existing file and preserving “thread“.
func (s *Store) UpdateNotificationsConfig(colonyID string, update NotificationsUpdate) error {
	colonyPath := filepath.Join(s.ColoniesDir, colonyID)
	if info, err := os.Stat(colonyPath); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrColonyNotFound, colonyID)
	}
	path := s.NotificationsConfigPath(colonyID)
	data := map[string]any{}
	if existing, err := readJSONMap(path); err == nil && existing != nil {
		data = existing
	}

	data["sentinel_enabled"] = update.SentinelEnabled
	if update.Channel == nil {
		data["channel"] = nil
	} else {
		data["channel"] = *update.Channel
	}
	if update.Target == nil {
		data["target"] = map[string]any{}
	} else {
		data["target"] = update.Target
	}
	allowlist := make([]string, 0, len(update.Allowlist))
	allowlist = append(allowlist, update.Allowlist...)
	data["allowlist"] = allowlist
	if update.ClassifyAfterSeconds == nil {
		delete(data, "classify_after_seconds")
	} else {
		data["classify_after_seconds"] = *update.ClassifyAfterSeconds
	}
	data["updated_at"] = s.nowISO()
	return atomicfs.WriteJSON(path, data, 0o600)
}

// UpdateNotificationsThread persists the channel thread anchor (parent message
// id/ts) for a colony, merging rather than overwriting.
func (s *Store) UpdateNotificationsThread(colonyID string, thread map[string]any) error {
	path := s.NotificationsConfigPath(colonyID)
	data := map[string]any{}
	if existing, err := readJSONMap(path); err == nil && existing != nil {
		data = existing
	}
	if thread == nil {
		thread = map[string]any{}
	}
	data["thread"] = thread
	data["updated_at"] = s.nowISO()
	return atomicfs.WriteJSON(path, data, 0o600)
}

// ----- escalation records -------------------------------------------------

// EscalationRecord is one escalation. The JSON tags are the persisted wire
// contract (Python EscalationRecord.to_dict).
type EscalationRecord struct {
	EscalationID     string         `json:"escalation_id"`
	ColonyID         string         `json:"colony_id"`
	SessionID        string         `json:"session_id"`
	CorrelationToken string         `json:"correlation_token"`
	ParkReason       string         `json:"park_reason"`
	QuestionText     string         `json:"question_text"`
	Channel          string         `json:"channel"`
	ThreadRef        map[string]any `json:"thread_ref"`
	Status           string         `json:"status"`
	CreatedAt        string         `json:"created_at"`
	ResolvedAt       *string        `json:"resolved_at"`
	ResolvedBy       *string        `json:"resolved_by"`
}

// NewEscalationRecord builds a record with the default open status and a
// creation stamp, mirroring the dataclass defaults.
func NewEscalationRecord(escalationID, colonyID, sessionID, correlationToken, parkReason, questionText, channel string) *EscalationRecord {
	return &EscalationRecord{
		EscalationID:     escalationID,
		ColonyID:         colonyID,
		SessionID:        sessionID,
		CorrelationToken: correlationToken,
		ParkReason:       parkReason,
		QuestionText:     questionText,
		Channel:          channel,
		ThreadRef:        map[string]any{},
		Status:           StatusOpen,
		CreatedAt:        time.Now().UTC().Format(time.RFC3339Nano),
	}
}

// ToDict renders the record as the persisted map.
func (r *EscalationRecord) ToDict() map[string]any {
	threadRef := r.ThreadRef
	if threadRef == nil {
		threadRef = map[string]any{}
	}
	status := r.Status
	if status == "" {
		status = StatusOpen
	}
	var resolvedAt any
	if r.ResolvedAt != nil {
		resolvedAt = *r.ResolvedAt
	}
	var resolvedBy any
	if r.ResolvedBy != nil {
		resolvedBy = *r.ResolvedBy
	}
	return map[string]any{
		"escalation_id":     r.EscalationID,
		"colony_id":         r.ColonyID,
		"session_id":        r.SessionID,
		"correlation_token": r.CorrelationToken,
		"park_reason":       r.ParkReason,
		"question_text":     r.QuestionText,
		"channel":           r.Channel,
		"thread_ref":        threadRef,
		"status":            status,
		"created_at":        r.CreatedAt,
		"resolved_at":       resolvedAt,
		"resolved_by":       resolvedBy,
	}
}

// EscalationRecordFromDict parses a persisted record. escalation_id,
// colony_id and session_id are required.
func EscalationRecordFromDict(data map[string]any) (*EscalationRecord, error) {
	escalationID, ok := data["escalation_id"].(string)
	if !ok || escalationID == "" {
		return nil, errors.New("missing escalation_id")
	}
	colonyID, ok := data["colony_id"].(string)
	if !ok || colonyID == "" {
		return nil, errors.New("missing colony_id")
	}
	sessionID, ok := data["session_id"].(string)
	if !ok || sessionID == "" {
		return nil, errors.New("missing session_id")
	}
	record := &EscalationRecord{
		EscalationID: escalationID,
		ColonyID:     colonyID,
		SessionID:    sessionID,
		Status:       StatusOpen,
	}
	if value, ok := data["correlation_token"].(string); ok {
		record.CorrelationToken = value
	}
	if value, ok := data["park_reason"].(string); ok {
		record.ParkReason = value
	}
	if value, ok := data["question_text"].(string); ok {
		record.QuestionText = value
	}
	if value, ok := data["channel"].(string); ok {
		record.Channel = value
	}
	if value, ok := data["thread_ref"].(map[string]any); ok && value != nil {
		record.ThreadRef = value
	}
	if value, ok := data["status"].(string); ok && value != "" {
		record.Status = value
	}
	if value, ok := data["created_at"].(string); ok {
		record.CreatedAt = value
	}
	if value, ok := data["resolved_at"].(string); ok {
		record.ResolvedAt = &value
	}
	if value, ok := data["resolved_by"].(string); ok {
		record.ResolvedBy = &value
	}
	return record, nil
}

// EscalationsDir returns the colony's escalation-record directory.
func (s *Store) EscalationsDir(colonyID string) string {
	if !validColonyStoreID(colonyID) {
		return filepath.Join(s.ColoniesDir, "__invalid__", escalationsDirName)
	}
	return filepath.Join(s.ColoniesDir, colonyID, escalationsDirName)
}

func (s *Store) recordPath(colonyID, escalationID string) string {
	return filepath.Join(s.EscalationsDir(colonyID), escalationID+".json")
}

// WriteEscalation persists a record atomically, filling the dataclass
// defaults (open status, creation stamp, empty thread anchor) when unset.
func (s *Store) WriteEscalation(record *EscalationRecord) error {
	if record.Status == "" {
		record.Status = StatusOpen
	}
	if record.CreatedAt == "" {
		record.CreatedAt = s.nowISO()
	}
	if record.ThreadRef == nil {
		record.ThreadRef = map[string]any{}
	}
	return atomicfs.WriteJSON(s.recordPath(record.ColonyID, record.EscalationID), record.ToDict(), 0o600)
}

// LoadEscalation reads one record; missing or invalid records return ok=false.
func (s *Store) LoadEscalation(colonyID, escalationID string) (*EscalationRecord, bool) {
	path := s.recordPath(colonyID, escalationID)
	data, err := readJSONMap(path)
	if err != nil {
		if !os.IsNotExist(err) {
			sentinelLogf("sentinel: invalid escalation record %s: %v", path, err)
		}
		return nil, false
	}
	record, err := EscalationRecordFromDict(data)
	if err != nil {
		sentinelLogf("sentinel: invalid escalation record %s: %v", path, err)
		return nil, false
	}
	return record, true
}

// ListOpen returns the open escalations for one colony.
func (s *Store) ListOpen(colonyID string) []*EscalationRecord {
	dir := s.EscalationsDir(colonyID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	records := make([]*EscalationRecord, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := readJSONMap(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		record, err := EscalationRecordFromDict(data)
		if err != nil {
			continue
		}
		if record.Status == StatusOpen {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].EscalationID < records[j].EscalationID })
	return records
}

// ListAllOpen returns every open escalation across all colonies — used to
// rebuild the in-memory token index on manager startup.
func (s *Store) ListAllOpen() []*EscalationRecord {
	entries, err := os.ReadDir(s.ColoniesDir)
	if err != nil {
		return nil
	}
	records := make([]*EscalationRecord, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		records = append(records, s.ListOpen(entry.Name())...)
	}
	return records
}

// ResolveEscalation marks an escalation resolved. It returns false when the
// record is missing or already resolved.
func (s *Store) ResolveEscalation(colonyID, escalationID, resolvedBy string) bool {
	record, ok := s.LoadEscalation(colonyID, escalationID)
	if !ok || record.Status == StatusResolved {
		return false
	}
	record.Status = StatusResolved
	resolvedAt := s.nowISO()
	record.ResolvedAt = &resolvedAt
	if resolvedBy != "" {
		record.ResolvedBy = &resolvedBy
	} else {
		record.ResolvedBy = nil
	}
	if err := s.WriteEscalation(record); err != nil {
		sentinelLogf("sentinel: could not resolve escalation %s: %v", escalationID, err)
		return false
	}
	return true
}
