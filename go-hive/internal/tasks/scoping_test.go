package tasks

// Parity: framework/tasks/tests/test_scoping.py

import "testing"

// Parity: core/framework/tasks/tests/test_scoping.py::test_session_helper
func TestSessionHelper(t *testing.T) {
	if got := SessionTaskListID("a", "b"); got != "session:a:b" {
		t.Fatalf("SessionTaskListID(a, b) = %q, want %q", got, "session:a:b")
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_colony_helper
func TestColonyHelper(t *testing.T) {
	if got := ColonyTaskListID("c"); got != "colony:c" {
		t.Fatalf("ColonyTaskListID(c) = %q, want %q", got, "colony:c")
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_parse_session
func TestParseSession(t *testing.T) {
	parts := ParseTaskListID("session:agent:sess")
	if parts.Kind != "session" || parts.AgentID != "agent" || parts.SessionID != "sess" {
		t.Fatalf("ParseTaskListID(session:agent:sess) = %+v", parts)
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_parse_colony
func TestParseColony(t *testing.T) {
	parts := ParseTaskListID("colony:abc")
	if parts.Kind != "colony" || parts.ColonyID != "abc" {
		t.Fatalf("ParseTaskListID(colony:abc) = %+v", parts)
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_resolve_uses_existing
func TestResolveUsesExisting(t *testing.T) {
	t.Setenv("HIVE_TASK_LIST_ID", "")
	ctx := TaskContext{AgentID: "x", RunID: "r1", TaskListID: "session:x:r1"}
	if got := ResolveTaskListID(ctx); got != "session:x:r1" {
		t.Fatalf("ResolveTaskListID = %q, want %q", got, "session:x:r1")
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_resolve_env_override
func TestResolveEnvOverride(t *testing.T) {
	t.Setenv("HIVE_TASK_LIST_ID", "forced")
	ctx := TaskContext{AgentID: "x", RunID: "r1"}
	if got := ResolveTaskListID(ctx); got != "forced" {
		t.Fatalf("ResolveTaskListID = %q, want %q", got, "forced")
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_resolve_synthesizes_session
func TestResolveSynthesizesSession(t *testing.T) {
	t.Setenv("HIVE_TASK_LIST_ID", "")
	ctx := TaskContext{AgentID: "alice", RunID: "r123"}
	if got := ResolveTaskListID(ctx); got != "session:alice:r123" {
		t.Fatalf("ResolveTaskListID = %q, want %q", got, "session:alice:r123")
	}
}

// Parity: core/framework/tasks/tests/test_scoping.py::test_resolve_falls_back_to_unscoped
func TestResolveFallsBackToUnscoped(t *testing.T) {
	t.Setenv("HIVE_TASK_LIST_ID", "")
	ctx := TaskContext{AgentID: "alice"}
	got := ResolveTaskListID(ctx)
	if len(got) < len("unscoped:") || got[:len("unscoped:")] != "unscoped:" {
		t.Fatalf("ResolveTaskListID = %q, want prefix %q", got, "unscoped:")
	}
}
