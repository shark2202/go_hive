package tasks

// Parity: framework/tasks/reminders.py

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

const StateFilename = "reminder_state.json"

const ReminderStopPoint agentloop.ReminderPoint = "stop"

// envInt mirrors upstream `int(os.environ.get(name, str(default)))`: surrounding
// whitespace and digit-group underscores are accepted, invalid values fall back.
func envInt(name string, def int) int {
	return pyenv.Int(name, def)
}

var (
	ReminderThresholdTurns    = envInt("HIVE_TASK_REMINDER_TURNS", 10)
	UntrackedTurns            = envInt("HIVE_TASK_UNTRACKED_TURNS", 5)
	ReminderCooldownTurns     = envInt("HIVE_TASK_REMINDER_COOLDOWN", 10)
	DriftSoftCap              = envInt("HIVE_TASK_REMINDER_SOFT_CAP", 2)
	DriftBackoffFactor        = envInt("HIVE_TASK_REMINDER_BACKOFF", 2)
	ReminderWarmupTurns       = envInt("HIVE_TASK_REMINDER_WARMUP", 4)
	SnapshotHeartbeatTurns    = envInt("HIVE_TASK_SNAPSHOT_HEARTBEAT", 12)
	MaxSnapshots              = envInt("HIVE_TASK_SNAPSHOT_MAX", 40)
	SnapshotMaxItems          = envInt("HIVE_TASK_SNAPSHOT_ITEMS", 12)
	CompletedCleanupThreshold = envInt("HIVE_TASK_COMPLETED_CLEANUP", 20)
)

func IsTaskTurn(toolNames []string) bool {
	for _, name := range toolNames {
		if IsTaskWriteTool(name) {
			return true
		}
	}
	return false
}

func FingerprintTasks(records []*TaskRecord) string {
	ordered := append([]*TaskRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	parts := make([]string, 0, len(ordered))
	for _, r := range ordered {
		parts = append(parts, strconv.Itoa(r.ID)+":"+string(r.Status)+":"+r.Subject)
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:12]
}

type ReminderState struct {
	TurnsTotal              int     `json:"turns_total"`
	TurnsSinceTaskOp        int     `json:"turns_since_task_op"`
	TaskToolEverUsed        bool    `json:"task_tool_ever_used"`
	TurnsSinceLastReminder  int     `json:"turns_since_last_reminder"`
	DriftRemindersSent      int     `json:"drift_reminders_sent"`
	TurnsSinceSnapshot      int     `json:"turns_since_snapshot"`
	SnapshotsSent           int     `json:"snapshots_sent"`
	LastSnapshotFingerprint *string `json:"last_snapshot_fingerprint"`
}

func NewReminderState() *ReminderState {
	return &ReminderState{
		TurnsSinceLastReminder: 1_000_000,
		TurnsSinceSnapshot:     1_000_000,
	}
}

func (s *ReminderState) OnTurn(isTaskTurn bool, touchedTasks ...bool) {
	touched := len(touchedTasks) > 0 && touchedTasks[0]
	s.TurnsTotal++
	s.TurnsSinceLastReminder++
	s.TurnsSinceSnapshot++
	if isTaskTurn {
		s.TaskToolEverUsed = true
		s.TurnsSinceTaskOp = 0
		s.DriftRemindersSent = 0
	} else if touched && s.TaskToolEverUsed {
		s.TurnsSinceTaskOp = 0
	} else {
		s.TurnsSinceTaskOp++
	}
}

func (s *ReminderState) DriftTrigger(openTaskCount int, completedTaskCount ...int) string {
	completed := 0
	if len(completedTaskCount) > 0 {
		completed = completedTaskCount[0]
	}
	if s.TurnsTotal < ReminderWarmupTurns {
		return ""
	}
	backoff := 1
	if s.DriftRemindersSent >= DriftSoftCap {
		backoff = DriftBackoffFactor
	}
	if s.TurnsSinceLastReminder < ReminderCooldownTurns*backoff {
		return ""
	}
	if completed > CompletedCleanupThreshold {
		return "too_many_completed"
	}
	if !s.TaskToolEverUsed && s.TurnsSinceTaskOp >= UntrackedTurns*backoff {
		return "untracked"
	}
	if s.TaskToolEverUsed && openTaskCount == 0 && s.TurnsSinceTaskOp >= 1 {
		return "all_done"
	}
	if openTaskCount > 0 && s.TurnsSinceTaskOp >= ReminderThresholdTurns*backoff {
		return "stale"
	}
	return ""
}

func (s *ReminderState) SnapshotDue(fingerprint string, hasTasks bool, ignoreFingerprint ...bool) bool {
	ignore := len(ignoreFingerprint) > 0 && ignoreFingerprint[0]
	if !hasTasks {
		return false
	}
	if s.TurnsTotal < ReminderWarmupTurns {
		return false
	}
	if s.SnapshotsSent >= MaxSnapshots {
		return false
	}
	if !ignore {
		if s.LastSnapshotFingerprint == nil || *s.LastSnapshotFingerprint != fingerprint {
			return true
		}
	}
	return s.TurnsSinceSnapshot >= SnapshotHeartbeatTurns
}

func (s *ReminderState) NoteDriftSent() {
	s.TurnsSinceLastReminder = 0
	s.DriftRemindersSent++
}

func (s *ReminderState) NoteSnapshotShown(fingerprint string) {
	s.TurnsSinceSnapshot = 0
	s.SnapshotsSent++
	fp := fingerprint
	s.LastSnapshotFingerprint = &fp
}

func (s *ReminderState) ToDict() map[string]any {
	out := map[string]any{
		"turns_total":               s.TurnsTotal,
		"turns_since_task_op":       s.TurnsSinceTaskOp,
		"task_tool_ever_used":       s.TaskToolEverUsed,
		"turns_since_last_reminder": s.TurnsSinceLastReminder,
		"drift_reminders_sent":      s.DriftRemindersSent,
		"turns_since_snapshot":      s.TurnsSinceSnapshot,
		"snapshots_sent":            s.SnapshotsSent,
	}
	if s.LastSnapshotFingerprint != nil {
		out["last_snapshot_fingerprint"] = *s.LastSnapshotFingerprint
	} else {
		out["last_snapshot_fingerprint"] = nil
	}
	return out
}

func ReminderStateFromDict(d map[string]any) *ReminderState {
	state := NewReminderState()
	if v, ok := d["turns_total"]; ok {
		state.TurnsTotal = jsonInt(v, state.TurnsTotal)
	}
	if v, ok := d["turns_since_task_op"]; ok {
		state.TurnsSinceTaskOp = jsonInt(v, state.TurnsSinceTaskOp)
	}
	if v, ok := d["task_tool_ever_used"]; ok {
		state.TaskToolEverUsed = jsonBool(v, state.TaskToolEverUsed)
	}
	if v, ok := d["turns_since_last_reminder"]; ok {
		state.TurnsSinceLastReminder = jsonInt(v, state.TurnsSinceLastReminder)
	}
	if v, ok := d["drift_reminders_sent"]; ok {
		state.DriftRemindersSent = jsonInt(v, state.DriftRemindersSent)
	}
	if v, ok := d["turns_since_snapshot"]; ok {
		state.TurnsSinceSnapshot = jsonInt(v, state.TurnsSinceSnapshot)
	}
	if v, ok := d["snapshots_sent"]; ok {
		state.SnapshotsSent = jsonInt(v, state.SnapshotsSent)
	}
	if v, ok := d["last_snapshot_fingerprint"]; ok {
		switch typed := v.(type) {
		case nil:
			state.LastSnapshotFingerprint = nil
		case string:
			fp := typed
			state.LastSnapshotFingerprint = &fp
		}
	}
	return state
}

func jsonInt(v any, def int) int {
	switch typed := v.(type) {
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

func jsonBool(v any, def bool) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}

func SnapshotLines(records []*TaskRecord, goal string) []string {
	if len(records) == 0 {
		return nil
	}
	ordered := append([]*TaskRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	lines := []string{}
	if goal != "" {
		lines = append(lines, "Goal: "+goal, "")
	}
	lines = append(lines, "Here are the existing tasks:", "")
	limit := SnapshotMaxItems
	if len(ordered) < limit {
		limit = len(ordered)
	}
	for _, r := range ordered[:limit] {
		lines = append(lines, "#"+strconv.Itoa(r.ID)+". ["+string(r.Status)+"] "+r.Subject)
	}
	if len(ordered) > SnapshotMaxItems {
		lines = append(lines, "... and "+strconv.Itoa(len(ordered)-SnapshotMaxItems)+" more")
	}
	return lines
}

func DriftLines(kind string, records []*TaskRecord) []string {
	switch kind {
	case "untracked":
		return []string{
			"[task_reminder] You've done several steps of real work but " +
				"haven't created any tasks. If this is multi-step work, track " +
				"it: create tasks with task_create and keep " +
				"their status current (in_progress when you start, completed " +
				"the moment a step is done). One task per atomic action/batch — don't " +
				"umbrella-track.",
		}
	case "all_done":
		return []string{
			"[task_reminder] No active task — every task on your list is " +
				"complete. If there's more to do, create the next task with " +
				"task_create. If the new work falls outside the goal above, " +
				"treat it as a pivot. If the goal is fully done, confirm with " +
				"the user before starting anything new.",
		}
	case "too_many_completed":
		completed := 0
		for _, r := range records {
			if r.Status == TaskStatusCompleted {
				completed++
			}
		}
		return []string{
			"[task_reminder] " + strconv.Itoa(completed) + " completed tasks are piling up on " +
				"your active plan. Clear the finished work out of it: archive the " +
				"completed tasks with task_update status='archived' so they move " +
				"into History and the plan shows only current and upcoming work. " +
				"Keep any completed task that an open task still lists as a " +
				"blocker.",
		}
	}
	inProgress := []*TaskRecord{}
	for _, r := range records {
		if r.Status == TaskStatusInProgress {
			inProgress = append(inProgress, r)
		}
	}
	lines := []string{
		"[task_reminder] The task tools haven't been used in several turns. If you're working on tasks that would benefit from tracked progress:",
		"  - Mark the in_progress task `completed` THE MOMENT it's done, in the same response as the next step's tool call. Don't batch completions.",
		"  - If you've finished work that wasn't on the list, add a task_create + task_update completed pair so the panel reflects it.",
		"  - If you're umbrella-tracking ('reply to all posts' as one task), " +
			"break it into one task per atomic action — one `task_create` call " +
			"with one entry/batch per action.",
		"  - If any open task no longer applies (user pivoted, scope " +
			"shifted, created in error), delete it via `task_update` with " +
			"status='deleted'. Don't leave stale items on the list.",
	}
	if len(inProgress) > 0 {
		limit := len(inProgress)
		if limit > 5 {
			limit = 5
		}
		labels := make([]string, 0, limit)
		for _, r := range inProgress[:limit] {
			labels = append(labels, "#"+strconv.Itoa(r.ID)+" \""+r.Subject+"\"")
		}
		lines = append(lines, "  - Currently in_progress (check they're really still active): "+strings.Join(labels, ", "))
	}
	return lines
}

func RenderBlock(records []*TaskRecord, driftKind string, includeSnapshot bool, goal string) string {
	body := []string{}
	if driftKind == "all_done" {
		if goal != "" {
			body = append(body, "Goal: "+goal, "")
		}
		body = append(body, DriftLines(driftKind, records)...)
		return strings.Join(body, "\n")
	}
	if driftKind != "" {
		body = append(body, DriftLines(driftKind, records)...)
	}
	if includeSnapshot {
		snap := SnapshotLines(records, goal)
		if len(snap) > 0 {
			if len(body) > 0 {
				body = append(body, "")
			}
			body = append(body, snap...)
		}
	}
	return strings.Join(body, "\n")
}

func ReadRecords(sessionID string) (records []*TaskRecord, ok bool) {
	return readRecordsFrom(GetTaskStore(), sessionID)
}

// readRecordsFrom is ReadRecords against an explicit store, so a caller with
// its own task scope (a colony worker's private list, the overseer's colony
// plan) reads exactly the list its task tools write.
func readRecordsFrom(store *TaskStore, sessionID string) (records []*TaskRecord, ok bool) {
	if store == nil {
		store = GetTaskStore()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("task reminder: list_tasks(%s) failed: %v", sessionID, recovered)
			records = nil
			ok = false
		}
	}()
	stored := store.ListTasks(sessionID)
	out := []*TaskRecord{}
	for _, r := range stored {
		if r.Status == TaskStatusArchived {
			continue
		}
		out = append(out, r)
	}
	return out, true
}

func ReadGoal(sessionID string) string {
	return readGoalFrom(GetTaskStore(), sessionID)
}

func readGoalFrom(store *TaskStore, sessionID string) string {
	if store == nil {
		store = GetTaskStore()
	}
	meta := store.GetMeta(sessionID)
	if meta == nil || meta.Goal == nil {
		return ""
	}
	return *meta.Goal
}

func reminderStatePathFor(store *TaskStore, sessionID string) (string, bool) {
	if store == nil {
		store = GetTaskStore()
	}
	dir, err := store.listDir(sessionID)
	if err != nil {
		return "", false
	}
	return filepath.Join(dir, StateFilename), true
}

func LoadReminderState(sessionID string) *ReminderState {
	return loadReminderStateFor(GetTaskStore(), sessionID)
}

func loadReminderStateFor(store *TaskStore, sessionID string) *ReminderState {
	path, ok := reminderStatePathFor(store, sessionID)
	if !ok {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		log.Printf("reminder state load failed for %s: %v", sessionID, err)
		return nil
	}
	return ReminderStateFromDict(raw)
}

func SaveReminderState(sessionID string, state *ReminderState) {
	saveReminderStateFor(GetTaskStore(), sessionID, state)
}

func saveReminderStateFor(store *TaskStore, sessionID string, state *ReminderState) {
	path, ok := reminderStatePathFor(store, sessionID)
	if !ok {
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("reminder state save failed for %s: %v", sessionID, err)
		return
	}
	if err := atomicfs.WriteFile(path, data, 0o600); err != nil {
		log.Printf("reminder state save failed for %s: %v", sessionID, err)
	}
}

type TaskReminderAgentContext struct {
	SessionID       string
	AvailableTools  []agentloop.Tool
	ChatAppProvider func() any
}

type TaskReminderContext struct {
	Point     agentloop.ReminderPoint
	AgentCtx  *TaskReminderAgentContext
	ToolNames []string
}

type TaskReminderSource struct {
	state     *ReminderState
	loaded    bool
	inChatApp bool
	// store/sessionID scope the source to a task list other than the
	// process-wide default. A colony worker writes its own store under
	// <workerDir> and the overseer keeps its plan in the colony directory;
	// both must read the list their own task tools wrote.
	store     *TaskStore
	sessionID string
}

func NewTaskReminderSource() *TaskReminderSource {
	return &TaskReminderSource{state: NewReminderState()}
}

// NewTaskReminderSourceFor binds the one reminder policy to the task store and
// session id the caller's task tools write. A nil store keeps the process-wide
// default; an empty sessionID keeps the session id carried by the render
// context (the chat runtime's shape).
//
// Parity: framework/tasks/reminders.py::TaskReminderSource (the Go port scopes
// it per agent because worker/overseer task lists are not process-wide).
func NewTaskReminderSourceFor(store *TaskStore, sessionID string) *TaskReminderSource {
	return &TaskReminderSource{state: NewReminderState(), store: store, sessionID: sessionID}
}

func (s *TaskReminderSource) Name() string { return "tasks" }

func (s *TaskReminderSource) Points() []agentloop.ReminderPoint {
	return []agentloop.ReminderPoint{
		agentloop.SESSION_START,
		agentloop.USER_PROMPT_SUBMIT,
		agentloop.POST_TOOL_USE,
		agentloop.POST_COMPACT,
		ReminderStopPoint,
	}
}

func (s *TaskReminderSource) AppliesTo(agentCtx *TaskReminderAgentContext) bool {
	if agentCtx == nil {
		return false
	}
	for _, tool := range agentCtx.AvailableTools {
		if IsTaskWriteTool(tool.Name) {
			return true
		}
	}
	return false
}

func (s *TaskReminderSource) ObserveTurn(toolNames []string) {
	s.state.OnTurn(IsTaskTurn(toolNames), anyTaskTool(toolNames))
}

func anyTaskTool(toolNames []string) bool {
	for _, name := range toolNames {
		if IsTaskTool(name) {
			return true
		}
	}
	return false
}

func (s *TaskReminderSource) ensureLoaded(store *TaskStore, sessionID string) {
	if s.loaded {
		return
	}
	s.loaded = true
	if loaded := loadReminderStateFor(store, sessionID); loaded != nil {
		s.state = loaded
	}
}

func (s *TaskReminderSource) Render(rctx TaskReminderContext) (string, bool) {
	if rctx.AgentCtx == nil {
		return "", false
	}
	store := s.store
	if store == nil {
		store = GetTaskStore()
	}
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = rctx.AgentCtx.SessionID
	}
	if sessionID == "" {
		return "", false
	}
	s.ensureLoaded(store, sessionID)
	records, ok := readRecordsFrom(store, sessionID)
	if !ok {
		return "", false
	}
	goal := readGoalFrom(store, sessionID)
	s.inChatApp = providerTruthy(rctx.AgentCtx.ChatAppProvider)
	if rctx.Point == agentloop.SESSION_START && len(records) > 0 {
		s.state.TaskToolEverUsed = true
	}
	var body string
	switch rctx.Point {
	case agentloop.POST_TOOL_USE:
		body = s.renderDriftOrSnapshot(records, true, goal, rctx.ToolNames)
	case ReminderStopPoint:
		body = s.renderStop(records, goal)
	default:
		body = s.renderSnapshot(records, rctx.Point, goal)
	}
	saveReminderStateFor(store, sessionID, s.state)
	if body == "" {
		return "", false
	}
	return body, true
}

func providerTruthy(provider func() any) bool {
	if provider == nil {
		return false
	}
	switch value := provider().(type) {
	case nil:
		return false
	case string:
		return value != ""
	case bool:
		return value
	default:
		return true
	}
}

func (s *TaskReminderSource) driftKind(openCount, completedCount int) string {
	if s.inChatApp {
		return ""
	}
	return s.state.DriftTrigger(openCount, completedCount)
}

func (s *TaskReminderSource) renderDriftOrSnapshot(records []*TaskRecord, snapshotGated bool, goal string, toolNames []string) string {
	snapDue := len(records) > 0
	fp := FingerprintTasks(records)
	if snapshotGated {
		names := map[string]bool{}
		for _, name := range toolNames {
			names[name] = true
		}
		updateOnly := names[TaskUpdate] && !names[TaskCreate]
		snapDue = s.state.SnapshotDue(fp, len(records) > 0, updateOnly)
	}
	openCount, completedCount := 0, 0
	for _, r := range records {
		if r.Status != TaskStatusCompleted {
			openCount++
		}
		if r.Status == TaskStatusCompleted {
			completedCount++
		}
	}
	drift := s.driftKind(openCount, completedCount)
	if !snapDue && drift == "" {
		return ""
	}
	includeSnapshot := drift != "all_done" && len(records) > 0 && (snapDue || drift != "")
	body := RenderBlock(records, drift, includeSnapshot, goal)
	if body == "" {
		return ""
	}
	if includeSnapshot {
		s.state.NoteSnapshotShown(fp)
	}
	if drift != "" {
		s.state.NoteDriftSent()
	}
	return body
}

func (s *TaskReminderSource) renderStop(records []*TaskRecord, goal string) string {
	openCount, completedCount := 0, 0
	for _, r := range records {
		if r.Status != TaskStatusCompleted {
			openCount++
		}
		if r.Status == TaskStatusCompleted {
			completedCount++
		}
	}
	drift := s.driftKind(openCount, completedCount)
	if drift == "" {
		return ""
	}
	includeSnapshot := drift != "all_done"
	body := RenderBlock(records, drift, includeSnapshot, goal)
	if body == "" {
		return ""
	}
	if includeSnapshot && len(records) > 0 {
		s.state.NoteSnapshotShown(FingerprintTasks(records))
	}
	s.state.NoteDriftSent()
	return body
}

func (s *TaskReminderSource) renderSnapshot(records []*TaskRecord, point agentloop.ReminderPoint, goal string) string {
	openCount := 0
	for _, r := range records {
		if r.Status != TaskStatusCompleted {
			openCount++
		}
	}
	if openCount == 0 {
		return ""
	}
	fp := FingerprintTasks(records)
	always := point == agentloop.SESSION_START || point == agentloop.POST_COMPACT
	if !always && !s.state.SnapshotDue(fp, true) {
		return ""
	}
	body := RenderBlock(records, "", true, goal)
	if body == "" {
		return ""
	}
	s.state.NoteSnapshotShown(fp)
	return body
}
