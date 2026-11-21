package cli

// Tests for the queen-lifecycle wiring into the session tool catalogue: the
// ten tools are registered for queen chat sessions only, execute through the
// catalogue, and never reach a colony worker's tool surface.
//
// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review

import (
	"context"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

func lifecycleCatalogueNames(t *testing.T, catalogue *sessionToolCatalogue) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, def := range catalogue.Defs() {
		names[def.Name] = true
	}
	return names
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestSessionCatalogueRegistersQueenLifecycleTools(t *testing.T) {
	root := testTempDir(t)
	home := hivehome.Home{Root: t.TempDir()}
	catalogue := newSessionToolCatalogue(root, builtin.New(""), home)
	names := lifecycleCatalogueNames(t, catalogue)
	for _, name := range hiveserver.QueenLifecycleToolNames() {
		if names[name] {
			t.Fatalf("%s advertised before the queen lifecycle table was enabled", name)
		}
	}

	session := hiveserver.NewSession("session_cli", eventbus.NewEventBus())
	enableQueenLifecycleTools(catalogue, session, home)
	names = lifecycleCatalogueNames(t, catalogue)
	for _, name := range hiveserver.QueenLifecycleToolNames() {
		if !names[name] {
			t.Fatalf("%s missing from a queen session's catalogue", name)
		}
	}

	// Execution reaches the binding: list_worker_questions on an empty inbox.
	content, isError, err := catalogue.Execute(context.Background(), "list_worker_questions", nil)
	if err != nil || isError {
		t.Fatalf("list_worker_questions: err=%v isError=%v content=%s", err, isError, content)
	}
	if !strings.Contains(content, `"count":0`) {
		t.Fatalf("empty inbox payload = %s", content)
	}

	// An invalid trigger id answers with the tool's own error, not an
	// unknown-tool fallthrough.
	content, isError, err = catalogue.Execute(context.Background(), "set_trigger", map[string]any{"trigger_id": "missing"})
	if err != nil || !isError {
		t.Fatalf("set_trigger: err=%v isError=%v content=%s", err, isError, content)
	}
	if !strings.Contains(content, "not found. Provide trigger_type and trigger_config") {
		t.Fatalf("set_trigger payload = %s", content)
	}
}

// Parity: tests/test_trigger_fires_into_queen.py::test_colony_tools_include_monitoring_and_review
func TestWorkerCatalogueExcludesQueenLifecycleTools(t *testing.T) {
	root := testTempDir(t)
	home := hivehome.Home{Root: t.TempDir()}
	_, _, _ = colonyWorkerToolSurface(root, builtin.New(root), home)
	workerCatalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	names := lifecycleCatalogueNames(t, workerCatalogue)
	for _, name := range hiveserver.QueenLifecycleToolNames() {
		if names[name] {
			t.Fatalf("worker catalogue must not advertise %s", name)
		}
	}
}

// TestQueenSessionCatalogueAdvertisesWorkerHealthSummary covers the
// worker-monitoring registration surface: a queen catalogue carries the
// get_worker_health_summary definition (from worker_monitoring_tools.py
// upstream), it executes through the lifecycle binding, and a worker
// catalogue never sees it.
//
// Parity: core/framework/tools/worker_monitoring_tools.py::register_worker_monitoring_tools
func TestQueenSessionCatalogueAdvertisesWorkerHealthSummary(t *testing.T) {
	root := testTempDir(t)
	home := hivehome.Home{Root: t.TempDir()}
	catalogue := newSessionToolCatalogue(root, builtin.New(root), home)
	if lifecycleCatalogueNames(t, catalogue)[hiveserver.QueenWorkerHealthSummaryToolName] {
		t.Fatal("get_worker_health_summary advertised before the queen lifecycle table was enabled")
	}
	session := hiveserver.NewSession("session_cli", eventbus.NewEventBus())
	session.WorkerPath = root
	enableQueenLifecycleTools(catalogue, session, home)
	if !lifecycleCatalogueNames(t, catalogue)[hiveserver.QueenWorkerHealthSummaryToolName] {
		t.Fatal("get_worker_health_summary missing from a queen session's catalogue")
	}

	// Execution reaches the binding: an empty storage root answers with the
	// upstream error envelope, not an unknown-tool fallthrough.
	content, isError, err := catalogue.Execute(context.Background(), hiveserver.QueenWorkerHealthSummaryToolName, nil)
	if err != nil || isError {
		t.Fatalf("get_worker_health_summary: err=%v isError=%v content=%s", err, isError, content)
	}
	if !strings.Contains(content, "No sessions found — worker has not started yet") {
		t.Fatalf("empty-storage payload = %s", content)
	}

	// The worker catalogue never advertises it (queen-only lifecycle surface).
	_, _, _ = colonyWorkerToolSurface(t.TempDir(), builtin.New(""), home)
	workerCatalogue := newSessionToolCatalogue(t.TempDir(), builtin.New(""), home)
	if lifecycleCatalogueNames(t, workerCatalogue)[hiveserver.QueenWorkerHealthSummaryToolName] {
		t.Fatal("worker catalogue must not advertise get_worker_health_summary")
	}
}
