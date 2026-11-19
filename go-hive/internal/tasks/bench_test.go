package tasks

// Performance baselines for the task-list snapshot path: the disk-backed list
// read used by GET /api/sessions/{id}/tasks and the reminder block render used
// by the task reminder source. Go-only measurements.

import (
	"fmt"
	"testing"
)

func benchmarkTaskStore(b *testing.B, count int) (*TaskStore, string) {
	b.Helper()
	root := b.TempDir()
	ResetSessionDirCache()
	b.Cleanup(ResetSessionDirCache)
	store := NewTaskStore(root)
	sessionID := "bench_session"
	specs := make([]TaskSpec, 0, count)
	for i := 0; i < count; i++ {
		specs = append(specs, TaskSpec{
			Subject:     fmt.Sprintf("task %d", i),
			Description: "benchmark task description",
			Owner:       "queen",
		})
	}
	goal := "benchmark goal"
	if _, err := store.CreateTasksBatch(sessionID, specs, &goal); err != nil {
		b.Fatalf("seed tasks: %v", err)
	}
	return store, sessionID
}

// BenchmarkTaskListSnapshot100 measures the route-shaped read: list tasks from
// disk and serialize every record.
func BenchmarkTaskListSnapshot100(b *testing.B) {
	store, sessionID := benchmarkTaskStore(b, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		records := store.ListTasks(sessionID)
		if len(records) != 100 {
			b.Fatalf("listed %d tasks, want 100", len(records))
		}
		out := make([]any, 0, len(records))
		for _, record := range records {
			out = append(out, SerializeRecord(record))
		}
		if len(out) != 100 {
			b.Fatalf("serialized %d tasks, want 100", len(out))
		}
	}
}

// BenchmarkTaskSnapshotRender100 measures the reminder snapshot render:
// fingerprint + snapshot lines + the rendered block for a full list.
func BenchmarkTaskSnapshotRender100(b *testing.B) {
	store, sessionID := benchmarkTaskStore(b, 100)
	records := store.ListTasks(sessionID)
	goal := "benchmark goal"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if fingerprint := FingerprintTasks(records); fingerprint == "" {
			b.Fatal("empty fingerprint")
		}
		lines := SnapshotLines(records, goal)
		block := RenderBlock(records, "", true, goal)
		if len(lines) == 0 || block == "" {
			b.Fatalf("empty snapshot (lines=%d block=%d)", len(lines), len(block))
		}
	}
}
