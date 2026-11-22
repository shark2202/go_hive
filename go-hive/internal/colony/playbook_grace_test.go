package colony

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// Evidence for HIVE_PLAYBOOK_FAST_GRACE and the [PLAYBOOK_COMPLETE] notice.
// Upstream has no test for the grace window; these drive the production
// run_playbook path (real Python bridge) and the bus notification.

func sleepingPythonPlaybook(name string) string {
	return strings.Join([]string{
		"import asyncio",
		`meta = {"name": "` + name + `", "description": "slow converge", "concurrency": 2}`,
		"",
		"async def run(args):",
		"    await asyncio.sleep(1.0)",
		"    await converge(",
		`        pending=lambda: tracker_query("SELECT id, name FROM jobs WHERE done=0"),`,
		"        dispatch=lambda row, i: worker(",
		`            task=f"Process job {row['name']} (id {row['id']})",`,
		`            goal=f"Processing {row['name']}"),`,
		"        max_rounds=1)",
		`    return {"remaining": tracker_count("SELECT id FROM jobs WHERE done=0")}`,
	}, "\n") + "\n"
}

func waitForPlaybookRun(t *testing.T, dbPath string, timeout time.Duration) []PlaybookRun {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		runs, err := ListPlaybookRuns(dbPath, 10)
		// The background finisher writes the same colony.db; a busy database is
		// a retry, not a failure.
		lastErr = err
		if err == nil && len(runs) > 0 {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("background playbook run was never recorded (last error: %v)", lastErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Parity: framework/tools/playbook_tools.py::_fast_result_grace
func TestPlaybookFastGraceZeroReturnsStartedAndRecordsInBackground(t *testing.T) {
	requirePlaybookPython(t)
	t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", "0")
	runtime := playbookTestRuntime(t, 2)

	start := time.Now()
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": sleepingPythonPlaybook("slow-jobs"),
	})
	elapsed := time.Since(start)
	if isError {
		t.Fatalf("started payload reported an error: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "started" {
		t.Fatalf("status = %v, want started (grace 0 is pure async)", payload["status"])
	}
	if elapsed > 700*time.Millisecond {
		t.Fatalf("grace=0 returned after %s; the run must not be awaited", elapsed)
	}
	if payload["run_id"] == "" || payload["playbook_name"] != "slow-jobs" {
		t.Fatalf("payload = %#v", payload)
	}
	if payload["log_db"] != filepath.Join(runtime.binding.Dir, "colony.db") {
		t.Fatalf("log_db = %v, want the colony.db path", payload["log_db"])
	}

	runs := waitForPlaybookRun(t, filepath.Join(runtime.binding.Dir, "colony.db"), 15*time.Second)
	if runs[0].Status != "done" || runs[0].Dispatched != 2 {
		t.Fatalf("background run = %#v, want done with 2 dispatches", runs[0])
	}
	runtime.mu.Lock()
	spawned := len(runtime.spawned)
	runtime.mu.Unlock()
	if spawned != 2 {
		t.Fatalf("runtime spawned %d workers, want 2", spawned)
	}
}

// Parity: framework/tools/playbook_tools.py::_fast_result_grace
func TestPlaybookDefaultGraceWaitsOutShortRuns(t *testing.T) {
	requirePlaybookPython(t)
	// Default grace (1.5s) with a run that finishes well inside it: the queen
	// gets the real outcome, not "started".
	t.Setenv("HIVE_PLAYBOOK_FAST_GRACE", "1.5")
	runtime := playbookTestRuntime(t, 2)
	content, isError := RunPlaybook(context.Background(), runtime, map[string]any{
		"playbook": pythonJobsScript("fast-jobs"),
	})
	if isError {
		t.Fatalf("fast playbook failed: %s", content)
	}
	payload := playbookPayload(t, content)
	if payload["status"] != "completed" {
		t.Fatalf("status = %v, want completed inside the grace window", payload["status"])
	}
}

// Parity: framework/tools/playbook_tools.py::_notify_complete (the notice data
// and its rendered [PLAYBOOK_COMPLETE] text).
func TestNotifyPlaybookCompletePublishesOnTheColonyBus(t *testing.T) {
	bus := eventbus.NewEventBus()
	runtime := NewColonyRuntime(RuntimeOptions{
		AgentSpec:   testAgentSpec(),
		StoragePath: t.TempDir(),
		StreamID:    "colony_notify",
		EventBus:    bus,
	})
	var received *eventbus.AgentEvent
	subscriptionID, err := runtime.SubscribeToEvents([]eventbus.EventType{eventbus.EventSubagentReport}, func(event *eventbus.AgentEvent) {
		received = event
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer runtime.Unsubscribe(subscriptionID)

	notice := playbookCompleteNoticeFields("pb_123", "slow-jobs", "done", "/tmp/colony.db", "", "hint text", `{"remaining": 0}`, 3, 1, []string{"phase: work", "round 1: 3 dispatched, 3 ok, 0 failed"})
	runtime.NotifyPlaybookComplete(notice)
	if received == nil {
		t.Fatal("no completion event published")
	}
	if received.Data["worker_id"] != "playbook:pb_123" {
		t.Fatalf("worker_id = %v", received.Data["worker_id"])
	}
	if received.Data["playbook_run_id"] != "pb_123" || received.Data["playbook_name"] != "slow-jobs" {
		t.Fatalf("event data = %#v", received.Data)
	}
	want := "[PLAYBOOK_COMPLETE]\n" +
		"<run_id>pb_123</run_id>\n" +
		"<status>done</status>\n" +
		"<log_db>/tmp/colony.db</log_db>\n" +
		"<dispatched>3</dispatched>\n" +
		"<dead_lettered>1</dead_lettered>\n" +
		"<result>{\"remaining\": 0}</result>\n" +
		"<hint>hint text</hint>\n" +
		"<log_tail>\nphase: work\nround 1: 3 dispatched, 3 ok, 0 failed\n</log_tail>"
	if got := received.Data["summary"]; got != want {
		t.Fatalf("summary = %q\nwant    = %q", got, want)
	}
	if remaining := runtime.BatchRemaining("", "playbook:pb_123"); remaining != 0 {
		t.Fatalf("batch remaining = %d", remaining)
	}
}

// Parity: framework/tools/playbook_tools.py::_notify_complete (error branch)
func TestPlaybookCompleteNoticeErrorBranchIsUpstreamShaped(t *testing.T) {
	notice := playbookCompleteNoticeForError("pb_err", "jobs", "/tmp/colony.db", "KeyError: 0 (playbook line 16)")
	want := "[PLAYBOOK_COMPLETE]\n" +
		"<run_id>pb_err</run_id>\n" +
		"<status>error</status>\n" +
		"<log_db>/tmp/colony.db</log_db>\n" +
		"<error>KeyError: 0 (playbook line 16)</error>"
	if got := notice["summary"]; got != want {
		t.Fatalf("summary = %q\nwant    = %q", got, want)
	}
	if _, present := notice["dispatched"]; present {
		t.Fatalf("error notice must not carry dispatched: %#v", notice)
	}
}
