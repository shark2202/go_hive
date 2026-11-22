package agentloop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Parity: core/framework/agent_loop/memory_reminders.py::INDEX_HEADER
// Parity: core/framework/agent_loop/memory_reminders.py::INDEX_CHANGED_HEADER
// Parity: core/framework/agent_loop/memory_reminders.py::RECALL_HEADER
// (byte-frozen in testdata/memory_note_headers.json from the Python constants;
// the probe docs/e2e-assets/memory-notes-1015/py-probe.json carries the same
// strings in its observed renderings).
func TestMemoryNoteHeadersMatchTheReference(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "memory_note_headers.json"))
	if err != nil {
		t.Fatalf("ReadFile fixture: %v", err)
	}
	var fixture struct {
		IndexHeader        string `json:"index_header"`
		IndexChangedHeader string `json:"index_changed_header"`
		RecallHeader       string `json:"recall_header"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("Unmarshal fixture: %v", err)
	}
	if MemoryIndexHeader != fixture.IndexHeader {
		t.Fatalf("INDEX_HEADER drifted:\n go: %q\n py: %q", MemoryIndexHeader, fixture.IndexHeader)
	}
	if MemoryIndexChangedHeader != fixture.IndexChangedHeader {
		t.Fatalf("INDEX_CHANGED_HEADER drifted:\n go: %q\n py: %q", MemoryIndexChangedHeader, fixture.IndexChangedHeader)
	}
	if MemoryRecallHeader != fixture.RecallHeader {
		t.Fatalf("RECALL_HEADER drifted:\n go: %q\n py: %q", MemoryRecallHeader, fixture.RecallHeader)
	}
}
