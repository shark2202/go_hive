package memory

// memory_index: every note a queen can read, as one manifest line each.
//
// What framework.agent_loop.memory_reminders.MemoryIndexReminderSource shows
// her in "index" mode in place of a model's pick: notes she reads herself with
// terminal_exec. Scopes without a directory or without a note are left out.

// MemoryIndexScope names one (label, directory) pair to list notes from.
type MemoryIndexScope struct {
	// Label is the section heading, e.g. "Shared with every queen".
	Label string
	// Dir is the folder holding .md notes; empty means "no folder yet".
	Dir string
}

// MemoryIndexEntry is one note's manifest line, keyed by filename.
type MemoryIndexEntry struct {
	Name string
	Line string
}

// MemoryIndexScopeListing is one scope's notes, in scan order (newest first).
type MemoryIndexScopeListing struct {
	Label   string
	Folder  string
	Entries []MemoryIndexEntry
}

// MemoryIndex lists every note in each scope as one manifest line, keyed by
// filename. Scopes without a directory or a note are left out.
//
// Parity: framework/agents/queen/recall_selector.py::memory_index
func MemoryIndex(scopes []MemoryIndexScope) []MemoryIndexScopeListing {
	out := []MemoryIndexScopeListing{}
	for _, scope := range scopes {
		if scope.Dir == "" {
			continue
		}
		files := ScanMemoryFiles(scope.Dir)
		if len(files) == 0 {
			continue
		}
		entries := make([]MemoryIndexEntry, 0, len(files))
		for _, file := range files {
			entries = append(entries, MemoryIndexEntry{
				Name: file.Filename,
				Line: FormatMemoryManifest([]MemoryFile{file}),
			})
		}
		out = append(out, MemoryIndexScopeListing{Label: scope.Label, Folder: scope.Dir, Entries: entries})
	}
	return out
}
