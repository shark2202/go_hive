package tasks

// Parity: framework/tasks/tests/test_reminders.py

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

func reminderRec(id int, subject string, status TaskStatus) *TaskRecord {
	return &TaskRecord{ID: id, Subject: subject, Status: status}
}

func reminderTaskStore(t *testing.T) *TaskStore {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HIVE_HOME", root)
	ResetSessionDirCache()
	previous := GetTaskStore()
	store := NewTaskStore(root)
	SetDefaultTaskStore(store)
	t.Cleanup(func() {
		SetDefaultTaskStore(previous)
		ResetSessionDirCache()
	})
	return store
}

// Parity: framework/tasks/tests/test_reminders.py::test_is_task_turn
func TestIsTaskTurn(t *testing.T) {
	if !IsTaskTurn([]string{"task_create"}) {
		t.Fatal("task_create should be a task turn")
	}
	if !IsTaskTurn([]string{"read_file", "task_update"}) {
		t.Fatal("task_update should be a task turn")
	}
	if IsTaskTurn([]string{"browser_click"}) {
		t.Fatal("non-task work should not be a task turn")
	}
	if IsTaskTurn([]string{"task_list", "task_get"}) {
		t.Fatal("read-only task tools should not be a task turn")
	}
	if IsTaskTurn(nil) {
		t.Fatal("pure conversation should not be a task turn")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_counter_advances_until_a_task_write
func TestCounterAdvancesUntilATaskWrite(t *testing.T) {
	s := NewReminderState()
	s.OnTurn(false)
	s.OnTurn(false)
	s.OnTurn(false)
	if s.TurnsSinceTaskOp != 3 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 3", s.TurnsSinceTaskOp)
	}
	if s.TurnsTotal != 3 {
		t.Fatalf("TurnsTotal = %d, want 3", s.TurnsTotal)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_write_resets_counter_and_sets_flag
func TestTaskWriteResetsCounterAndSetsFlag(t *testing.T) {
	s := NewReminderState()
	for i := 0; i < 5; i++ {
		s.OnTurn(false)
	}
	if s.TaskToolEverUsed {
		t.Fatal("TaskToolEverUsed should be false before any write")
	}
	s.OnTurn(true)
	if s.TurnsSinceTaskOp != 0 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 0", s.TurnsSinceTaskOp)
	}
	if !s.TaskToolEverUsed {
		t.Fatal("TaskToolEverUsed should be true after a write")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_read_resets_counter_only_after_tracking_began
func TestReadResetsCounterOnlyAfterTrackingBegan(t *testing.T) {
	s := NewReminderState()
	for i := 0; i < 3; i++ {
		s.OnTurn(false, true)
	}
	if s.TurnsSinceTaskOp != 3 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 3", s.TurnsSinceTaskOp)
	}
	if s.TaskToolEverUsed {
		t.Fatal("reads must not set TaskToolEverUsed")
	}

	s.OnTurn(true)
	s.OnTurn(false)
	if s.TurnsSinceTaskOp != 1 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 1", s.TurnsSinceTaskOp)
	}
	s.OnTurn(false, true)
	if s.TurnsSinceTaskOp != 0 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 0 after read", s.TurnsSinceTaskOp)
	}
	if !s.TaskToolEverUsed {
		t.Fatal("TaskToolEverUsed should remain true")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_reading_open_tasks_suppresses_stale_nudge
func TestReadingOpenTasksSuppressesStaleNudge(t *testing.T) {
	s := NewReminderState()
	s.OnTurn(true)
	for i := 0; i < ReminderThresholdTurns-1; i++ {
		s.OnTurn(false)
	}
	s.OnTurn(false, true)
	if got := s.DriftTrigger(2); got != "" {
		t.Fatalf("DriftTrigger = %q, want empty", got)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_untracked_trigger_fires_without_any_task
func TestUntrackedTriggerFiresWithoutAnyTask(t *testing.T) {
	s := NewReminderState()
	for i := 0; i < UntrackedTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(0); got != "untracked" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "untracked")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_stale_trigger_needs_open_tasks_and_prior_tracking
func TestStaleTriggerNeedsOpenTasksAndPriorTracking(t *testing.T) {
	s := NewReminderState()
	s.OnTurn(true)
	for i := 0; i < ReminderThresholdTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(2); got != "stale" {
		t.Fatalf("DriftTrigger(2) = %q, want %q", got, "stale")
	}
	if got := s.DriftTrigger(0); got != "all_done" {
		t.Fatalf("DriftTrigger(0) = %q, want %q", got, "all_done")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_all_done_trigger_fires_when_list_fully_complete
func TestAllDoneTriggerFiresWhenListFullyComplete(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = 1
	if got := s.DriftTrigger(0); got != "all_done" {
		t.Fatalf("DriftTrigger(0) = %q, want %q", got, "all_done")
	}
	if got := s.DriftTrigger(1); got != "" {
		t.Fatalf("DriftTrigger(1) = %q, want empty", got)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_all_done_not_on_the_completing_write_turn
func TestAllDoneNotOnTheCompletingWriteTurn(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.OnTurn(true)
	if got := s.DriftTrigger(0); got != "" {
		t.Fatalf("DriftTrigger on write turn = %q, want empty", got)
	}
	s.OnTurn(false)
	if got := s.DriftTrigger(0); got != "all_done" {
		t.Fatalf("DriftTrigger next turn = %q, want %q", got, "all_done")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_all_done_requires_prior_tracking
func TestAllDoneRequiresPriorTracking(t *testing.T) {
	s := NewReminderState()
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = UntrackedTurns
	if got := s.DriftTrigger(0); got != "untracked" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "untracked")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_too_many_completed_fires_over_threshold
func TestTooManyCompletedFiresOverThreshold(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = 1
	if got := s.DriftTrigger(1, CompletedCleanupThreshold); got != "" {
		t.Fatalf("DriftTrigger(at threshold) = %q, want empty", got)
	}
	if got := s.DriftTrigger(1, CompletedCleanupThreshold+1); got != "too_many_completed" {
		t.Fatalf("DriftTrigger(over threshold) = %q, want %q", got, "too_many_completed")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_too_many_completed_takes_priority_over_all_done
func TestTooManyCompletedTakesPriorityOverAllDone(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = 1
	if got := s.DriftTrigger(0, CompletedCleanupThreshold+1); got != "too_many_completed" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "too_many_completed")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_too_many_completed_respects_cooldown
func TestTooManyCompletedRespectsCooldown(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = 1
	over := CompletedCleanupThreshold + 1
	if got := s.DriftTrigger(1, over); got != "too_many_completed" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "too_many_completed")
	}
	s.NoteDriftSent()
	if got := s.DriftTrigger(1, over); got != "" {
		t.Fatalf("DriftTrigger after cooldown start = %q, want empty", got)
	}
	for i := 0; i < ReminderCooldownTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(1, over); got != "too_many_completed" {
		t.Fatalf("DriftTrigger after cooldown = %q, want %q", got, "too_many_completed")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_too_many_completed_text
func TestRenderBlockTooManyCompletedText(t *testing.T) {
	records := make([]*TaskRecord, 0, 21)
	for i := 1; i <= 21; i++ {
		records = append(records, reminderRec(i, fmt.Sprintf("step %d", i), TaskStatusCompleted))
	}
	body := RenderBlock(records, "too_many_completed", false, "")
	if !strings.Contains(body, "21 completed tasks") {
		t.Fatalf("body missing completed count: %q", body)
	}
	if !strings.Contains(body, "status='archived'") {
		t.Fatalf("body missing archive advice: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_too_many_completed_takes_priority_over_untracked
func TestTooManyCompletedTakesPriorityOverUntracked(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = false
	s.TurnsTotal = ReminderWarmupTurns
	s.TurnsSinceTaskOp = UntrackedTurns + 5
	if got := s.DriftTrigger(1, CompletedCleanupThreshold+1); got != "too_many_completed" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "too_many_completed")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_drift_respects_warmup
func TestDriftRespectsWarmup(t *testing.T) {
	s := NewReminderState()
	s.TurnsSinceTaskOp = UntrackedTurns + 10
	s.TurnsTotal = ReminderWarmupTurns - 1
	if got := s.DriftTrigger(3); got != "" {
		t.Fatalf("DriftTrigger = %q, want empty", got)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_drift_respects_cooldown
func TestDriftRespectsCooldown(t *testing.T) {
	s := NewReminderState()
	for i := 0; i < UntrackedTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(0); got != "untracked" {
		t.Fatalf("DriftTrigger = %q, want %q", got, "untracked")
	}
	s.NoteDriftSent()
	if got := s.DriftTrigger(0); got != "" {
		t.Fatalf("DriftTrigger after cooldown start = %q, want empty", got)
	}
	for i := 0; i < ReminderCooldownTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(0); got != "untracked" {
		t.Fatalf("DriftTrigger after cooldown = %q, want %q", got, "untracked")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_soft_cap_backs_off_instead_of_silencing
func TestSoftCapBacksOffInsteadOfSilencing(t *testing.T) {
	s := NewReminderState()
	s.TaskToolEverUsed = true
	s.TurnsTotal = ReminderWarmupTurns
	s.DriftRemindersSent = DriftSoftCap

	for i := 0; i < ReminderCooldownTurns; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(1); got != "" {
		t.Fatalf("DriftTrigger at cap = %q, want empty (not silent — just waiting)", got)
	}

	for i := 0; i < ReminderCooldownTurns*DriftBackoffFactor; i++ {
		s.OnTurn(false)
	}
	if got := s.DriftTrigger(1); got != "stale" {
		t.Fatalf("DriftTrigger after stretched cooldown = %q, want %q", got, "stale")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_write_resets_soft_cap_budget
func TestTaskWriteResetsSoftCapBudget(t *testing.T) {
	s := NewReminderState()
	s.DriftRemindersSent = DriftSoftCap + 1
	s.OnTurn(true)
	if s.DriftRemindersSent != 0 {
		t.Fatalf("DriftRemindersSent = %d, want 0 after write", s.DriftRemindersSent)
	}
	s.DriftRemindersSent = DriftSoftCap + 1
	s.OnTurn(false, true)
	if s.DriftRemindersSent != DriftSoftCap+1 {
		t.Fatalf("DriftRemindersSent = %d, want %d after read", s.DriftRemindersSent, DriftSoftCap+1)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_snapshot_due_on_change_then_quiet
func TestSnapshotDueOnChangeThenQuiet(t *testing.T) {
	s := NewReminderState()
	s.TurnsTotal = ReminderWarmupTurns
	if !s.SnapshotDue("fp1", true) {
		t.Fatal("SnapshotDue(fp1) should be true on first check")
	}
	s.NoteSnapshotShown("fp1")
	if s.SnapshotDue("fp1", true) {
		t.Fatal("SnapshotDue(fp1) should be false after showing it")
	}
	if !s.SnapshotDue("fp2", true) {
		t.Fatal("SnapshotDue(fp2) should be true after a change")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_snapshot_heartbeat_reshows_unchanged_list
func TestSnapshotHeartbeatReshowsUnchangedList(t *testing.T) {
	s := NewReminderState()
	s.TurnsTotal = ReminderWarmupTurns
	s.NoteSnapshotShown("fp1")
	s.TurnsSinceSnapshot = SnapshotHeartbeatTurns
	if !s.SnapshotDue("fp1", true) {
		t.Fatal("SnapshotDue should be true at the heartbeat")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_snapshot_skipped_when_no_tasks
func TestSnapshotSkippedWhenNoTasks(t *testing.T) {
	s := NewReminderState()
	s.TurnsTotal = ReminderWarmupTurns
	if s.SnapshotDue("fp1", false) {
		t.Fatal("SnapshotDue should be false without tasks")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_fingerprint_changes_on_status
func TestFingerprintChangesOnStatus(t *testing.T) {
	before := []*TaskRecord{reminderRec(1, "a", TaskStatusPending), reminderRec(2, "b", TaskStatusPending)}
	after := []*TaskRecord{reminderRec(1, "a", TaskStatusPending), reminderRec(2, "b", TaskStatusCompleted)}
	if FingerprintTasks(before) == FingerprintTasks(after) {
		t.Fatal("fingerprints should differ after a status change")
	}
	reversed := []*TaskRecord{before[1], before[0]}
	if FingerprintTasks(before) != FingerprintTasks(reversed) {
		t.Fatal("fingerprints should be order-independent")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_drift_plus_snapshot
func TestRenderBlockDriftPlusSnapshot(t *testing.T) {
	records := []*TaskRecord{
		reminderRec(1, "step 1", TaskStatusPending),
		reminderRec(2, "step 2", TaskStatusInProgress),
	}
	body := RenderBlock(records, "stale", true, "")
	if strings.HasPrefix(body, "<system-reminder>") {
		t.Fatalf("render_block returns the bare body: %q", body)
	}
	if !strings.Contains(body, "task_reminder") {
		t.Fatalf("body missing task_reminder: %q", body)
	}
	if !strings.Contains(body, "Here are the existing tasks:") {
		t.Fatalf("body missing snapshot header: %q", body)
	}
	if !strings.Contains(body, "step 1") || !strings.Contains(body, "step 2") {
		t.Fatalf("body missing task subjects: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_snapshot_only_has_no_nudge
func TestRenderBlockSnapshotOnlyHasNoNudge(t *testing.T) {
	body := RenderBlock([]*TaskRecord{reminderRec(1, "step 1", TaskStatusPending)}, "", true, "")
	if !strings.Contains(body, "Here are the existing tasks:") {
		t.Fatalf("body missing snapshot header: %q", body)
	}
	if strings.Contains(body, "task_reminder") {
		t.Fatalf("snapshot-only body should carry no nudge: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_untracked_nudge_text
func TestRenderBlockUntrackedNudgeText(t *testing.T) {
	body := RenderBlock(nil, "untracked", false, "")
	if !strings.Contains(body, "haven't created any tasks") {
		t.Fatalf("body missing untracked text: %q", body)
	}
	if strings.Contains(body, "Here are the existing tasks:") {
		t.Fatalf("untracked body should omit the snapshot: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_empty_when_nothing_to_say
func TestRenderBlockEmptyWhenNothingToSay(t *testing.T) {
	if body := RenderBlock(nil, "", true, ""); body != "" {
		t.Fatalf("RenderBlock = %q, want empty", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_render_block_all_done_shows_goal_and_message_not_task_list
func TestRenderBlockAllDoneShowsGoalAndMessageNotTaskList(t *testing.T) {
	records := []*TaskRecord{
		reminderRec(1, "step 1", TaskStatusCompleted),
		reminderRec(2, "step 2", TaskStatusCompleted),
	}
	body := RenderBlock(records, "all_done", true, "Ship the feature")
	if !strings.Contains(body, "No active task") {
		t.Fatalf("body missing all_done text: %q", body)
	}
	if !strings.Contains(body, "Goal: Ship the feature") {
		t.Fatalf("body missing goal: %q", body)
	}
	if strings.Contains(body, "Here are the existing tasks:") {
		t.Fatalf("all_done body should omit the snapshot: %q", body)
	}
	if strings.Contains(body, "step 1") || strings.Contains(body, "step 2") {
		t.Fatalf("all_done body should omit completed tasks: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_applies_to_requires_task_write_tools
func TestTaskSourceAppliesToRequiresTaskWriteTools(t *testing.T) {
	src := NewTaskReminderSource()
	if !src.AppliesTo(&TaskReminderAgentContext{AvailableTools: []agentloop.Tool{{Name: "task_update"}}}) {
		t.Fatal("task_update should make the source applicable")
	}
	if src.AppliesTo(&TaskReminderAgentContext{AvailableTools: []agentloop.Tool{{Name: "browser_click"}}}) {
		t.Fatal("a non-task tool should not make the source applicable")
	}
	if src.AppliesTo(&TaskReminderAgentContext{AvailableTools: nil}) {
		t.Fatal("no tools should mean not applicable")
	}
	if src.AppliesTo(nil) {
		t.Fatal("no available_tools surface should mean not applicable")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_declares_all_five_points
func TestTaskSourceDeclaresAllFivePoints(t *testing.T) {
	got := NewTaskReminderSource().Points()
	want := []agentloop.ReminderPoint{
		agentloop.SESSION_START,
		agentloop.USER_PROMPT_SUBMIT,
		agentloop.POST_TOOL_USE,
		agentloop.POST_COMPACT,
		ReminderStopPoint,
	}
	if len(got) != len(want) {
		t.Fatalf("Points() = %v, want %v", got, want)
	}
	for _, point := range want {
		found := false
		for _, candidate := range got {
			if candidate == point {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Points() = %v, missing %v", got, point)
		}
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_none_without_session_id
func TestTaskSourceNoneWithoutSessionID(t *testing.T) {
	src := NewTaskReminderSource()
	body, ok := src.Render(TaskReminderContext{
		Point:    agentloop.POST_TOOL_USE,
		AgentCtx: &TaskReminderAgentContext{},
	})
	if ok || body != "" {
		t.Fatalf("Render = %q, %v; want empty", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_post_tool_use_shows_snapshot
func TestTaskSourcePostToolUseShowsSnapshot(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "verify the sheet"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	src := NewTaskReminderSource()
	src.state.TurnsTotal = ReminderWarmupTurns
	ctx := &TaskReminderAgentContext{SessionID: sessionID}

	body, ok := src.Render(TaskReminderContext{Point: agentloop.POST_TOOL_USE, AgentCtx: ctx})
	if !ok || !strings.Contains(body, "verify the sheet") {
		t.Fatalf("Render = %q, %v; want snapshot with subject", body, ok)
	}
	if body, ok := src.Render(TaskReminderContext{Point: agentloop.POST_TOOL_USE, AgentCtx: ctx}); ok || body != "" {
		t.Fatalf("second Render = %q, %v; want empty", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_session_start_reasserts_resumed_list
func TestTaskSourceSessionStartReassertsResumedList(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "resumed work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	src := NewTaskReminderSource()
	ctx := &TaskReminderAgentContext{SessionID: sessionID}
	body, ok := src.Render(TaskReminderContext{Point: agentloop.SESSION_START, AgentCtx: ctx})
	if !ok || !strings.Contains(body, "resumed work") {
		t.Fatalf("Render = %q, %v; want resumed list", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_stop_emits_untracked_drift
func TestTaskSourceStopEmitsUntrackedDrift(t *testing.T) {
	reminderTaskStore(t)
	src := NewTaskReminderSource()
	for i := 0; i < UntrackedTurns; i++ {
		src.ObserveTurn([]string{"browser_click"})
	}
	body, ok := src.Render(TaskReminderContext{
		Point:    ReminderStopPoint,
		AgentCtx: &TaskReminderAgentContext{SessionID: "s1"},
	})
	if !ok || !strings.Contains(body, "haven't created any tasks") {
		t.Fatalf("Render = %q, %v; want untracked drift", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_does_not_push_the_panel_on_a_chat_app
func TestTaskSourceDoesNotPushThePanelOnAChatApp(t *testing.T) {
	reminderTaskStore(t)
	src := NewTaskReminderSource()
	for i := 0; i < UntrackedTurns; i++ {
		src.ObserveTurn([]string{"browser_click"})
	}

	onChatApp := &TaskReminderAgentContext{
		SessionID:       "s1",
		ChatAppProvider: func() any { return "onebot" },
	}
	if body, ok := src.Render(TaskReminderContext{Point: ReminderStopPoint, AgentCtx: onChatApp}); ok || body != "" {
		t.Fatalf("STOP on chat app = %q, %v; want empty", body, ok)
	}
	if body, ok := src.Render(TaskReminderContext{Point: agentloop.POST_TOOL_USE, AgentCtx: onChatApp}); ok || body != "" {
		t.Fatalf("POST_TOOL_USE on chat app = %q, %v; want empty", body, ok)
	}

	onDashboard := &TaskReminderAgentContext{
		SessionID:       "s1",
		ChatAppProvider: func() any { return nil },
	}
	body, ok := src.Render(TaskReminderContext{Point: ReminderStopPoint, AgentCtx: onDashboard})
	if !ok || !strings.Contains(body, "haven't created any tasks") {
		t.Fatalf("STOP on dashboard = %q, %v; want untracked drift", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_still_shows_the_list_she_keeps_on_a_chat_app
func TestTaskSourceStillShowsTheListSheKeepsOnAChatApp(t *testing.T) {
	store := reminderTaskStore(t)
	if _, err := store.CreateTask("s1", CreateTaskSpec{Subject: "long job"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	src := NewTaskReminderSource()
	ctx := &TaskReminderAgentContext{
		SessionID:       "s1",
		ChatAppProvider: func() any { return "onebot" },
	}
	body, ok := src.Render(TaskReminderContext{Point: agentloop.SESSION_START, AgentCtx: ctx})
	if !ok || !strings.Contains(body, "long job") {
		t.Fatalf("Render = %q, %v; want snapshot with subject", body, ok)
	}
}

func seedCompletedPile(t *testing.T, store *TaskStore, sessionID string, completed int, open ...int) {
	t.Helper()
	openCount := 1
	if len(open) > 0 {
		openCount = open[0]
	}
	specs := make([]TaskSpec, 0, completed+openCount)
	for i := 1; i <= completed; i++ {
		specs = append(specs, TaskSpec{Subject: fmt.Sprintf("done %d", i)})
	}
	for i := 1; i <= openCount; i++ {
		specs = append(specs, TaskSpec{Subject: fmt.Sprintf("open %d", i)})
	}
	goal := "Long-running goal"
	if _, err := store.CreateTasksBatch(sessionID, specs, &goal); err != nil {
		t.Fatalf("CreateTasksBatch: %v", err)
	}
	for taskID := 1; taskID <= completed; taskID++ {
		status := TaskStatusCompleted
		store.UpdateTask(sessionID, taskID, UpdateTaskOptions{Status: &status})
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_nudges_cleanup_on_completing_write_turn
func TestTaskSourceNudgesCleanupOnCompletingWriteTurn(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	seedCompletedPile(t, store, sessionID, CompletedCleanupThreshold+1)
	src := NewTaskReminderSource()
	src.state.TurnsTotal = ReminderWarmupTurns
	ctx := &TaskReminderAgentContext{SessionID: sessionID}

	src.ObserveTurn([]string{"task_update"})
	body, ok := src.Render(TaskReminderContext{
		Point:     agentloop.POST_TOOL_USE,
		AgentCtx:  ctx,
		ToolNames: []string{"task_update"},
	})
	if !ok {
		t.Fatal("Render should produce a cleanup nudge")
	}
	if !strings.Contains(body, fmt.Sprintf("%d completed tasks", CompletedCleanupThreshold+1)) {
		t.Fatalf("body missing completed count: %q", body)
	}
	if !strings.Contains(body, "status='archived'") {
		t.Fatalf("body missing archive advice: %q", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_stop_nudges_cleanup
func TestTaskSourceStopNudgesCleanup(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	seedCompletedPile(t, store, sessionID, CompletedCleanupThreshold+1)
	src := NewTaskReminderSource()
	src.state.TurnsTotal = ReminderWarmupTurns
	body, ok := src.Render(TaskReminderContext{
		Point:    ReminderStopPoint,
		AgentCtx: &TaskReminderAgentContext{SessionID: sessionID},
	})
	if !ok || !strings.Contains(body, "completed tasks are piling up") {
		t.Fatalf("Render = %q, %v; want cleanup nudge", body, ok)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_task_source_cleanup_nudge_stops_after_archiving
func TestTaskSourceCleanupNudgeStopsAfterArchiving(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	seedCompletedPile(t, store, sessionID, CompletedCleanupThreshold+1)
	src := NewTaskReminderSource()
	src.state.TurnsTotal = ReminderWarmupTurns
	ctx := &TaskReminderAgentContext{SessionID: sessionID}

	body, ok := src.Render(TaskReminderContext{Point: ReminderStopPoint, AgentCtx: ctx})
	if !ok || !strings.Contains(body, "piling up") {
		t.Fatalf("Render = %q, %v; want cleanup nudge", body, ok)
	}

	archived, err := store.ArchiveCompletedTasks(sessionID)
	if err != nil {
		t.Fatalf("ArchiveCompletedTasks: %v", err)
	}
	if len(archived) != CompletedCleanupThreshold+1 {
		t.Fatalf("archived = %d, want %d", len(archived), CompletedCleanupThreshold+1)
	}
	for i := 0; i < ReminderCooldownTurns; i++ {
		src.ObserveTurn([]string{"browser_click"})
	}
	body, ok = src.Render(TaskReminderContext{Point: ReminderStopPoint, AgentCtx: ctx})
	if ok && strings.Contains(body, "piling up") {
		t.Fatalf("Render after archiving = %q; cleanup nudge should stop", body)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_session_start_seeds_ever_tracked_flag
func TestSessionStartSeedsEverTrackedFlag(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "pre-existing work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	src := NewTaskReminderSource()
	ctx := &TaskReminderAgentContext{SessionID: sessionID}

	if _, ok := src.Render(TaskReminderContext{Point: agentloop.SESSION_START, AgentCtx: ctx}); !ok {
		t.Fatal("SESSION_START should render the resumed list")
	}
	if !src.state.TaskToolEverUsed {
		t.Fatal("SESSION_START should seed TaskToolEverUsed from persisted state")
	}

	for i := 0; i < UntrackedTurns+ReminderThresholdTurns; i++ {
		src.ObserveTurn([]string{"browser_click"})
	}
	if got := src.state.DriftTrigger(1); got != "stale" {
		t.Fatalf("DriftTrigger(1) = %q, want %q", got, "stale")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_reminder_state_survives_resume
func TestReminderStateSurvivesResume(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s1"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "work"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	ctx := &TaskReminderAgentContext{SessionID: sessionID}

	src1 := NewTaskReminderSource()
	src1.ObserveTurn([]string{"task_create"})
	for i := 0; i < 4; i++ {
		src1.ObserveTurn([]string{"browser_click"})
	}
	src1.state.DriftRemindersSent = 2
	if _, ok := src1.Render(TaskReminderContext{Point: agentloop.POST_TOOL_USE, AgentCtx: ctx}); !ok {
		t.Fatal("first render should produce a snapshot")
	}

	src2 := NewTaskReminderSource()
	if _, ok := src2.Render(TaskReminderContext{Point: agentloop.SESSION_START, AgentCtx: ctx}); !ok {
		t.Fatal("SESSION_START should render the resumed list")
	}
	if src2.state.TurnsSinceTaskOp != 4 {
		t.Fatalf("TurnsSinceTaskOp = %d, want 4 (counter survived)", src2.state.TurnsSinceTaskOp)
	}
	if src2.state.DriftRemindersSent != 2 {
		t.Fatalf("DriftRemindersSent = %d, want 2 (nudge cap survived)", src2.state.DriftRemindersSent)
	}
	if !src2.state.TaskToolEverUsed {
		t.Fatal("TaskToolEverUsed should survive the restart")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_reminder_state_dict_roundtrip_tolerates_unknown_keys
func TestReminderStateDictRoundtripToleratesUnknownKeys(t *testing.T) {
	s := NewReminderState()
	s.TurnsTotal = 7
	s.DriftRemindersSent = 3
	raw := s.ToDict()
	raw["removed_field"] = 99
	restored := ReminderStateFromDict(raw)
	if restored.TurnsTotal != 7 {
		t.Fatalf("TurnsTotal = %d, want 7", restored.TurnsTotal)
	}
	if restored.DriftRemindersSent != 3 {
		t.Fatalf("DriftRemindersSent = %d, want 3", restored.DriftRemindersSent)
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_read_records_excludes_archived
func TestReadRecordsExcludesArchived(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s_arch"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "active"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "parked"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	archived := TaskStatusArchived
	store.UpdateTask(sessionID, 2, UpdateTaskOptions{Status: &archived})

	records, ok := ReadRecords(sessionID)
	if !ok {
		t.Fatal("ReadRecords reported failure")
	}
	if len(records) != 1 || records[0].Subject != "active" {
		t.Fatalf("ReadRecords = %+v, want only %q", records, "active")
	}
}

// Parity: framework/tasks/tests/test_reminders.py::test_open_count_ignores_archived
func TestOpenCountIgnoresArchived(t *testing.T) {
	store := reminderTaskStore(t)
	sessionID := "s_done"
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "finished"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := store.CreateTask(sessionID, CreateTaskSpec{Subject: "parked"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	completed := TaskStatusCompleted
	archived := TaskStatusArchived
	store.UpdateTask(sessionID, 1, UpdateTaskOptions{Status: &completed})
	store.UpdateTask(sessionID, 2, UpdateTaskOptions{Status: &archived})

	records, ok := ReadRecords(sessionID)
	if !ok {
		t.Fatal("ReadRecords reported failure")
	}
	openCount := 0
	for _, r := range records {
		if r.Status != TaskStatusCompleted {
			openCount++
		}
	}
	if openCount != 0 {
		t.Fatalf("openCount = %d, want 0 (archived task must not count as open)", openCount)
	}
}
