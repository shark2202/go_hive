package tasks

// Parity: framework/tasks/store.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const DocFilename = "tasks.json"

var errEmptySessionID = errors.New("session_id must be a non-empty string")

var (
	inprocLocksMu sync.Mutex
	inprocLocks   = map[string]*sync.Mutex{}
)

func getInprocLock(sessionID string) *sync.Mutex {
	inprocLocksMu.Lock()
	defer inprocLocksMu.Unlock()
	lock := inprocLocks[sessionID]
	if lock == nil {
		lock = &sync.Mutex{}
		inprocLocks[sessionID] = lock
	}
	return lock
}

var (
	sessionDirCacheMu sync.Mutex
	sessionDirCache   = map[string]string{}
)

var findSessionDirFn = findSessionDir

func HiveRoot() string {
	if v := os.Getenv("HIVE_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".hive"
	}
	return filepath.Join(home, ".hive")
}

func findSessionDir(sessionID, hiveRoot string) (string, bool) {
	queensDir := filepath.Join(hiveRoot, "queens")
	if entries, err := os.ReadDir(queensDir); err == nil {
		for _, queenRoot := range entries {
			if !queenRoot.IsDir() {
				continue
			}
			candidate := filepath.Join(queensDir, queenRoot.Name(), "sessions", sessionID)
			if isDir(candidate) {
				return candidate, true
			}
		}
	}
	coloniesDir := filepath.Join(hiveRoot, "colonies")
	if entries, err := os.ReadDir(coloniesDir); err == nil {
		for _, colonyRoot := range entries {
			if !colonyRoot.IsDir() {
				continue
			}
			candidate := filepath.Join(coloniesDir, colonyRoot.Name(), "workers", sessionID)
			if isDir(candidate) {
				return candidate, true
			}
			colonyQueens := filepath.Join(coloniesDir, colonyRoot.Name(), "queens")
			queenEntries, err := os.ReadDir(colonyQueens)
			if err != nil {
				continue
			}
			for _, queenRoot := range queenEntries {
				if !queenRoot.IsDir() {
					continue
				}
				candidate = filepath.Join(colonyQueens, queenRoot.Name(), "sessions", sessionID)
				if isDir(candidate) {
					return candidate, true
				}
			}
		}
	}
	return "", false
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func SessionStorageDir(sessionID string, hiveRoot ...string) (string, error) {
	if sessionID == "" {
		return "", errEmptySessionID
	}
	root := HiveRoot()
	if len(hiveRoot) > 0 && hiveRoot[0] != "" {
		root = hiveRoot[0]
	}
	cacheKey := sessionID + "\x00" + root
	sessionDirCacheMu.Lock()
	cached, ok := sessionDirCache[cacheKey]
	sessionDirCacheMu.Unlock()
	if ok {
		if isDir(cached) {
			return cached, nil
		}
		sessionDirCacheMu.Lock()
		if sessionDirCache[cacheKey] == cached {
			delete(sessionDirCache, cacheKey)
		}
		sessionDirCacheMu.Unlock()
	}
	if canonical, found := findSessionDirFn(sessionID, root); found {
		sessionDirCacheMu.Lock()
		sessionDirCache[cacheKey] = canonical
		sessionDirCacheMu.Unlock()
		return canonical, nil
	}
	var b strings.Builder
	for _, c := range sessionID {
		if isAlnum(c) || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteRune('_')
		}
	}
	return filepath.Join(root, "_misc", b.String()), nil
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r > 127
}

func ResetSessionDirCache() {
	sessionDirCacheMu.Lock()
	sessionDirCache = map[string]string{}
	sessionDirCacheMu.Unlock()
}

type TaskStore struct {
	hiveRoot string
}

func NewTaskStore(hiveRoot string) *TaskStore {
	return &TaskStore{hiveRoot: hiveRoot}
}

func (s *TaskStore) root() string {
	if s.hiveRoot != "" {
		return s.hiveRoot
	}
	return HiveRoot()
}

func (s *TaskStore) listDir(sessionID string) (string, error) {
	return SessionStorageDir(sessionID, s.root())
}

func (s *TaskStore) DocPath(sessionID string) (string, error) {
	dir, err := s.listDir(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, DocFilename), nil
}

func (s *TaskStore) listLock(sessionID string) (*sync.Mutex, error) {
	dir, err := s.listDir(sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return getInprocLock(sessionID), nil
}

func (s *TaskStore) readDoc(sessionID string) *TaskListDocument {
	path, err := s.DocPath(sessionID)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc TaskListDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Printf("WARNING: Corrupt tasks.json at %s: %v", path, err)
		return nil
	}
	// Python validates the document through TaskListDocument.model_validate_json:
	// a status outside TaskStatus raises and _read_doc_sync logs the file as
	// corrupt and returns None, hiding the whole list. Match that read semantics
	// for legacy files written before the write-side guard existed (P0-10): the
	// guard prevents new invalid values, and this makes the on-disk outcome
	// identical on both runtimes instead of Go silently listing an unreadable doc.
	for _, record := range doc.Tasks {
		if record == nil || record.Status == "" {
			continue
		}
		if !ValidTaskStatus(string(record.Status)) {
			log.Printf("WARNING: Corrupt tasks.json at %s: task #%d has invalid status %q", path, record.ID, record.Status)
			return nil
		}
	}
	normalizeDoc(&doc)
	return &doc
}

func normalizeDoc(doc *TaskListDocument) {
	if doc.Meta == nil {
		doc.Meta = NewTaskListMeta()
	} else {
		if doc.Meta.Role == "" {
			doc.Meta.Role = "session"
		}
		if doc.Meta.SchemaVersion == 0 {
			doc.Meta.SchemaVersion = 1
		}
	}
	if doc.Tasks == nil {
		doc.Tasks = []*TaskRecord{}
	}
	for _, r := range doc.Tasks {
		if r == nil {
			continue
		}
		if r.Status == "" {
			r.Status = TaskStatusPending
		}
		if r.Blocks == nil {
			r.Blocks = []int{}
		}
		if r.BlockedBy == nil {
			r.BlockedBy = []int{}
		}
		if r.Metadata == nil {
			r.Metadata = map[string]any{}
		}
	}
}

func (s *TaskStore) writeDoc(sessionID string, doc *TaskListDocument) error {
	// Python validates the document with pydantic on read: one record with an
	// unknown status makes tasks.json look corrupt and the whole task list
	// disappears. Refuse that write at the boundary so no caller can produce a
	// file the Python implementation cannot read.
	for _, record := range doc.Tasks {
		if record != nil && !ValidTaskStatus(string(record.Status)) {
			return fmt.Errorf("refusing to write tasks.json: task #%d has invalid status %q", record.ID, record.Status)
		}
	}
	path, err := s.DocPath(sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	// Owner-only like the rest of the session tree (upstream atomic writers).
	return atomicfs.WriteFile(path, data, 0o600)
}

func (s *TaskStore) EnsureTaskList(sessionID string, creatorAgentID *string) (*TaskListMeta, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		meta := NewTaskListMeta()
		meta.CreatorAgentID = creatorAgentID
		doc = &TaskListDocument{Meta: meta, Tasks: []*TaskRecord{}}
		if err := s.writeDoc(sessionID, doc); err != nil {
			return nil, err
		}
		return meta, nil
	}
	return doc.Meta, nil
}

func (s *TaskStore) SetGoal(sessionID, goal string) error {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		doc = &TaskListDocument{Meta: NewTaskListMeta(), Tasks: []*TaskRecord{}}
	}
	g := goal
	doc.Meta.Goal = &g
	return s.writeDoc(sessionID, doc)
}

func (s *TaskStore) ListExists(sessionID string) bool {
	path, err := s.DocPath(sessionID)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(path)
	return statErr == nil
}

func (s *TaskStore) GetMeta(sessionID string) *TaskListMeta {
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil
	}
	return doc.Meta
}

func (s *TaskStore) ResetTaskList(sessionID string) error {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil
	}
	maxID := 0
	for _, r := range doc.Tasks {
		if r.ID > maxID {
			maxID = r.ID
		}
	}
	if maxID > doc.Highwatermark {
		doc.Highwatermark = maxID
	}
	doc.Tasks = []*TaskRecord{}
	return s.writeDoc(sessionID, doc)
}

func (s *TaskStore) ArchiveCompletedTasks(sessionID string) ([]*TaskRecord, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil, nil
	}
	now := NowSeconds()
	archived := []*TaskRecord{}
	for _, record := range doc.Tasks {
		if record.Status != TaskStatusCompleted {
			continue
		}
		record.Metadata["archived_from"] = string(record.Status)
		record.Metadata["archived_at"] = now
		record.Metadata["archived_goal"] = goalValue(doc.Meta)
		record.Status = TaskStatusArchived
		record.UpdatedAt = now
		archived = append(archived, record)
	}
	if len(archived) == 0 {
		return nil, nil
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return nil, err
	}
	return archived, nil
}

func (s *TaskStore) UnarchiveTasks(sessionID string, taskIDs []int) ([]int, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil, nil
	}
	wanted := map[int]struct{}{}
	for _, id := range taskIDs {
		wanted[id] = struct{}{}
	}
	now := NowSeconds()
	restored := []int{}
	for _, record := range doc.Tasks {
		if _, ok := wanted[record.ID]; !ok || record.Status != TaskStatusArchived {
			continue
		}
		origin, _ := record.Metadata["archived_from"].(string)
		delete(record.Metadata, "archived_from")
		delete(record.Metadata, "archived_at")
		delete(record.Metadata, "archived_goal")
		record.Status = statusFromOrigin(origin)
		record.UpdatedAt = now
		restored = append(restored, record.ID)
	}
	if len(restored) == 0 {
		return nil, nil
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return nil, err
	}
	return restored, nil
}

func statusFromOrigin(origin string) TaskStatus {
	switch TaskStatus(origin) {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted, TaskStatusAbandoned, TaskStatusArchived:
		return TaskStatus(origin)
	}
	return TaskStatusPending
}

func goalValue(meta *TaskListMeta) any {
	if meta == nil || meta.Goal == nil {
		return nil
	}
	return *meta.Goal
}

type TaskSpec struct {
	Subject     any
	Description any
	ActiveForm  any
	Owner       any
	Metadata    any
}

func SpecFromMap(m map[string]any) TaskSpec {
	return TaskSpec{
		Subject:     m["subject"],
		Description: m["description"],
		ActiveForm:  m["active_form"],
		Owner:       m["owner"],
		Metadata:    m["metadata"],
	}
}

func asOptionalString(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asIntSlice(v any) []int {
	switch values := v.(type) {
	case []int:
		return append([]int(nil), values...)
	case []any:
		out := make([]int, 0, len(values))
		for _, item := range values {
			switch n := item.(type) {
			case int:
				out = append(out, n)
			case float64:
				out = append(out, int(n))
			}
		}
		return out
	}
	return nil
}

func (s *TaskStore) CreateTasksBatch(sessionID string, specs []TaskSpec, goal *string) ([]*TaskRecord, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	for i, spec := range specs {
		subj, ok := spec.Subject.(string)
		if !ok || strings.TrimSpace(subj) == "" {
			return nil, fmt.Errorf("specs[%d].subject must be a non-empty string", i)
		}
	}
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		doc = &TaskListDocument{Meta: NewTaskListMeta(), Tasks: []*TaskRecord{}}
	}
	if goal != nil {
		doc.Meta.Goal = goal
	}
	baseID := nextIDForDoc(doc)
	now := NowSeconds()
	records := make([]*TaskRecord, 0, len(specs))
	for offset, spec := range specs {
		var metadata map[string]any
		if m, ok := spec.Metadata.(map[string]any); ok && m != nil {
			metadata = CloneMap(m)
		} else {
			metadata = map[string]any{}
		}
		records = append(records, &TaskRecord{
			ID:          baseID + offset,
			Subject:     spec.Subject.(string),
			Description: asString(spec.Description),
			ActiveForm:  asOptionalString(spec.ActiveForm),
			Owner:       asOptionalString(spec.Owner),
			Status:      TaskStatusPending,
			Blocks:      []int{},
			BlockedBy:   []int{},
			Metadata:    metadata,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	doc.Tasks = append(doc.Tasks, records...)
	if highest := records[len(records)-1].ID; highest > doc.Highwatermark {
		doc.Highwatermark = highest
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return nil, err
	}
	return records, nil
}

type CreateTaskSpec struct {
	Subject     string
	Description string
	ActiveForm  *string
	Owner       *string
	Metadata    map[string]any
}

func (s *TaskStore) CreateTask(sessionID string, spec CreateTaskSpec) (*TaskRecord, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		doc = &TaskListDocument{Meta: NewTaskListMeta(), Tasks: []*TaskRecord{}}
	}
	newID := nextIDForDoc(doc)
	now := NowSeconds()
	metadata := spec.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	} else {
		metadata = CloneMap(metadata)
	}
	record := &TaskRecord{
		ID:          newID,
		Subject:     spec.Subject,
		Description: spec.Description,
		ActiveForm:  spec.ActiveForm,
		Owner:       spec.Owner,
		Status:      TaskStatusPending,
		Blocks:      []int{},
		BlockedBy:   []int{},
		Metadata:    metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	doc.Tasks = append(doc.Tasks, record)
	if newID > doc.Highwatermark {
		doc.Highwatermark = newID
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return nil, err
	}
	return record, nil
}

func nextIDForDoc(doc *TaskListDocument) int {
	maxExisting := 0
	for _, r := range doc.Tasks {
		if r.ID > maxExisting {
			maxExisting = r.ID
		}
	}
	if doc.Highwatermark > maxExisting {
		return doc.Highwatermark + 1
	}
	return maxExisting + 1
}

func (s *TaskStore) GetTask(sessionID string, taskID int) *TaskRecord {
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil
	}
	for _, r := range doc.Tasks {
		if r.ID == taskID {
			return r
		}
	}
	return nil
}

func (s *TaskStore) ListTasks(sessionID string, includeInternal ...bool) []*TaskRecord {
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil
	}
	records := append([]*TaskRecord(nil), doc.Tasks...)
	sort.SliceStable(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	include := len(includeInternal) > 0 && includeInternal[0]
	if include {
		return records
	}
	out := make([]*TaskRecord, 0, len(records))
	for _, r := range records {
		if internal, _ := r.Metadata["_internal"].(bool); internal {
			continue
		}
		out = append(out, r)
	}
	return out
}

type UpdateTaskOptions struct {
	Subject       *string
	Description   *string
	ActiveForm    *string
	Owner         *string
	OwnerSet      bool
	Status        *TaskStatus
	AddBlocks     []int
	AddBlockedBy  []int
	MetadataPatch map[string]any
}

func (s *TaskStore) UpdateTask(sessionID string, taskID int, opts UpdateTaskOptions) (*TaskRecord, []string) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, nil
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil, nil
	}
	var target *TaskRecord
	for _, r := range doc.Tasks {
		if r.ID == taskID {
			target = r
			break
		}
	}
	if target == nil {
		return nil, nil
	}
	newRecord, changed := updateTaskInDoc(doc, target, opts)
	if len(changed) > 0 {
		if err := s.writeDoc(sessionID, doc); err != nil {
			return nil, nil
		}
	}
	return newRecord, changed
}

func updateTaskInDoc(doc *TaskListDocument, current *TaskRecord, opts UpdateTaskOptions) (*TaskRecord, []string) {
	changed := []string{}

	if opts.Subject != nil && *opts.Subject != current.Subject {
		current.Subject = *opts.Subject
		changed = append(changed, "subject")
	}
	if opts.Description != nil && *opts.Description != current.Description {
		current.Description = *opts.Description
		changed = append(changed, "description")
	}
	if opts.ActiveForm != nil && !ptrStringEqual(opts.ActiveForm, current.ActiveForm) {
		current.ActiveForm = opts.ActiveForm
		changed = append(changed, "active_form")
	}
	if opts.OwnerSet && !ptrStringEqual(opts.Owner, current.Owner) {
		current.Owner = opts.Owner
		changed = append(changed, "owner")
	}
	if opts.Status != nil && !ValidTaskStatus(string(*opts.Status)) {
		// Python's TaskStatus enum raises before the store is touched. Never
		// persist an unknown status: pydantic then rejects the whole
		// tasks.json on read, hiding every task in the list. The tool layer
		// rejects this first; this guard keeps internal callers honest.
		log.Printf("WARNING: refusing invalid task status %q for #%d", *opts.Status, current.ID)
	} else if opts.Status != nil && *opts.Status != current.Status {
		if *opts.Status == TaskStatusArchived {
			current.Metadata["archived_from"] = string(current.Status)
			current.Metadata["archived_at"] = NowSeconds()
			current.Metadata["archived_goal"] = goalValue(doc.Meta)
		} else if current.Status == TaskStatusArchived {
			delete(current.Metadata, "archived_from")
			delete(current.Metadata, "archived_at")
			delete(current.Metadata, "archived_goal")
		}
		current.Status = *opts.Status
		changed = append(changed, "status")
	}

	if len(opts.AddBlocks) > 0 {
		for _, b := range opts.AddBlocks {
			if b == current.ID || containsInt(current.Blocks, b) {
				continue
			}
			current.Blocks = append(current.Blocks, b)
			if !containsString(changed, "blocks") {
				changed = append(changed, "blocks")
			}
			if target := findTask(doc, b); target != nil && !containsInt(target.BlockedBy, current.ID) {
				target.BlockedBy = append(target.BlockedBy, current.ID)
				target.UpdatedAt = NowSeconds()
			}
		}
	}
	if len(opts.AddBlockedBy) > 0 {
		for _, b := range opts.AddBlockedBy {
			if b == current.ID || containsInt(current.BlockedBy, b) {
				continue
			}
			current.BlockedBy = append(current.BlockedBy, b)
			if !containsString(changed, "blocked_by") {
				changed = append(changed, "blocked_by")
			}
			if target := findTask(doc, b); target != nil && !containsInt(target.Blocks, current.ID) {
				target.Blocks = append(target.Blocks, current.ID)
				target.UpdatedAt = NowSeconds()
			}
		}
	}

	if opts.MetadataPatch != nil {
		md := CloneMap(current.Metadata)
		for k, v := range opts.MetadataPatch {
			if v == nil {
				delete(md, k)
			} else {
				md[k] = v
			}
		}
		if !reflect.DeepEqual(md, current.Metadata) {
			current.Metadata = md
			changed = append(changed, "metadata")
		}
	}

	if len(changed) == 0 {
		return current, nil
	}
	current.UpdatedAt = NowSeconds()
	return current, changed
}

func findTask(doc *TaskListDocument, id int) *TaskRecord {
	for _, r := range doc.Tasks {
		if r.ID == id {
			return r
		}
	}
	return nil
}

func ptrStringEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func containsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func (s *TaskStore) SweepIdleTasks(sessionID string) ([]*TaskRecord, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return nil, nil
	}
	now := NowSeconds()
	changed := []*TaskRecord{}
	for _, record := range doc.Tasks {
		if IsTaskIdle(record, now) {
			record.Status = TaskStatusAbandoned
			record.UpdatedAt = now
			changed = append(changed, record)
		}
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return nil, err
	}
	return changed, nil
}

func (s *TaskStore) DeleteTask(sessionID string, taskID int) (bool, []int, error) {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return false, nil, err
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return false, nil, nil
	}
	idx := -1
	for i, r := range doc.Tasks {
		if r.ID == taskID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, nil, nil
	}
	if taskID > doc.Highwatermark {
		doc.Highwatermark = taskID
	}
	doc.Tasks = append(doc.Tasks[:idx], doc.Tasks[idx+1:]...)
	cascaded := []int{}
	now := NowSeconds()
	for _, other := range doc.Tasks {
		touched := false
		if containsInt(other.Blocks, taskID) {
			other.Blocks = filterInt(other.Blocks, taskID)
			touched = true
		}
		if containsInt(other.BlockedBy, taskID) {
			other.BlockedBy = filterInt(other.BlockedBy, taskID)
			touched = true
		}
		if touched {
			other.UpdatedAt = now
			cascaded = append(cascaded, other.ID)
		}
	}
	if err := s.writeDoc(sessionID, doc); err != nil {
		return false, nil, err
	}
	return true, cascaded, nil
}

func filterInt(values []int, drop int) []int {
	out := make([]int, 0, len(values))
	for _, v := range values {
		if v != drop {
			out = append(out, v)
		}
	}
	return out
}

func (s *TaskStore) ClaimTaskWithBusyCheck(sessionID string, taskID int, claimant string) ClaimResult {
	lock, err := s.listLock(sessionID)
	if err != nil {
		return ClaimNotFound()
	}
	lock.Lock()
	defer lock.Unlock()
	doc := s.readDoc(sessionID)
	if doc == nil {
		return ClaimNotFound()
	}
	current := findTask(doc, taskID)
	if current == nil {
		return ClaimNotFound()
	}
	if current.Status == TaskStatusCompleted {
		return ClaimAlreadyCompleted()
	}
	if current.Owner != nil && *current.Owner != claimant {
		return ClaimAlreadyOwned(*current.Owner)
	}
	unresolved := []int{}
	for _, b := range current.BlockedBy {
		if blocker := findTask(doc, b); blocker != nil && blocker.Status != TaskStatusCompleted {
			unresolved = append(unresolved, b)
		}
	}
	if len(unresolved) > 0 {
		return ClaimBlocked(unresolved)
	}
	newRecord, _ := updateTaskInDoc(doc, current, UpdateTaskOptions{Owner: &claimant, OwnerSet: true})
	if err := s.writeDoc(sessionID, doc); err != nil {
		return ClaimNotFound()
	}
	return ClaimOk(newRecord)
}

var (
	defaultStoreMu sync.Mutex
	defaultStore   *TaskStore
)

func GetTaskStore() *TaskStore {
	defaultStoreMu.Lock()
	defer defaultStoreMu.Unlock()
	if defaultStore == nil {
		defaultStore = NewTaskStore("")
	}
	return defaultStore
}

func SetDefaultTaskStore(store *TaskStore) {
	defaultStoreMu.Lock()
	defaultStore = store
	defaultStoreMu.Unlock()
}
