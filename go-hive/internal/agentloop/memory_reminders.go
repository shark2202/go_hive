package agentloop

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// How the queen's memory notes reach her before she answers: the two
// ReminderSources behind memory_recall.mode "index" and "wait".
//
// Each source shows notes only while its mode is set, read at each message
// through MemoryRecallSettingsProvider, so a change in Settings reaches
// running sessions. "background" is neither: the orchestrator picks after the
// turn has started and injects the result whenever it lands (the chat
// runtime's injectRecall).
//
// Parity: framework/agent_loop/memory_reminders.py

// Parity: framework/agent_loop/memory_reminders.py::RECALL_HEADER
const MemoryRecallHeader = "Recalled memories relevant to the latest user message (supersedes earlier recall reminders):"

// Parity: framework/agent_loop/memory_reminders.py::INDEX_HEADER
const MemoryIndexHeader = "Your memory notes: what earlier conversations taught you, one line per note " +
	"(type, file name, what it holds). When one may bear on the conversation, read it with " +
	"terminal_exec (cat \"<folder>/<file name>\"): the line is only its description."

// Parity: framework/agent_loop/memory_reminders.py::INDEX_CHANGED_HEADER
const MemoryIndexChangedHeader = "Your memory notes changed since they were last listed (same folders):"

// DefaultMemoryRecallWaitSeconds mirrors MemoryRecallConfig.wait_seconds'
// default for a runtime that wired no settings provider.
const DefaultMemoryRecallWaitSeconds = 10.0

// MemoryRecallSettings is the (mode, wait_seconds) pair read at each message.
//
// Parity: framework/config.py::MemoryRecallConfig
type MemoryRecallSettings struct {
	Mode        string
	WaitSeconds float64
}

// MemoryNoteEntry is one note's manifest line as the index source renders it.
type MemoryNoteEntry struct {
	Name string
	Line string
}

// MemoryNoteScope is one (label, folder) listing the index source shows.
type MemoryNoteScope struct {
	Label   string
	Folder  string
	Entries []MemoryNoteEntry
}

// MemoryRecallReminderSource injects the notes a model picked for the user's
// message, before she answers it ("wait" mode).
//
// Parity: framework/agent_loop/memory_reminders.py::MemoryRecallReminderSource
type MemoryRecallReminderSource struct {
	// last is the last block shown; an unchanged pick isn't shown again.
	last string
	mu   sync.Mutex
}

func NewMemoryRecallReminderSource() *MemoryRecallReminderSource {
	return &MemoryRecallReminderSource{}
}

func (s *MemoryRecallReminderSource) Name() string { return "memory_recall" }

func (s *MemoryRecallReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT}
}

func (s *MemoryRecallReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	return agentCtx != nil && agentCtx.IsQueenStream && agentCtx.MemoryRecallProvider != nil
}

func (s *MemoryRecallReminderSource) Render(rctx ReminderContext) (string, bool) {
	settings := memoryRecallSettings(rctx.AgentCtx)
	if rctx.Point == POST_COMPACT || settings.Mode != "wait" {
		// The summary doesn't carry the last pick, and one shown before
		// another mode ran may be long out of view: the next pick is shown
		// even if the same.
		s.mu.Lock()
		s.last = ""
		s.mu.Unlock()
		return "", false
	}
	if rctx.AgentCtx == nil || rctx.AgentCtx.MemoryRecallProvider == nil {
		return "", false
	}
	text := strings.TrimSpace(rctx.UserText)
	if text == "" {
		return "", false
	}
	block, ok := callMemoryRecallProvider(rctx.AgentCtx.MemoryRecallProvider, text, settings.WaitSeconds)
	if !ok || strings.TrimSpace(block) == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if block == s.last {
		return "", false
	}
	s.last = block
	return MemoryRecallHeader + "\n\n" + block, true
}

// callMemoryRecallProvider waits up to waitSeconds for the pick. Past the cap
// the turn proceeds without it (the pick itself keeps running); a panicking
// provider is a failed pick.
func callMemoryRecallProvider(provider func(string) string, text string, waitSeconds float64) (string, bool) {
	if waitSeconds <= 0 {
		waitSeconds = DefaultMemoryRecallWaitSeconds
	}
	done := make(chan string, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- ""
			}
		}()
		done <- provider(text)
	}()
	select {
	case block := <-done:
		return block, true
	case <-time.After(time.Duration(waitSeconds * float64(time.Second))):
		return "", false
	}
}

// MemoryIndexReminderSource lists every memory note, so she can read the ones
// that bear on the conversation ("index" mode). Shown in full at the session's
// start and after compaction; after that only what changed.
//
// Parity: framework/agent_loop/memory_reminders.py::MemoryIndexReminderSource
type MemoryIndexReminderSource struct {
	mu sync.Mutex
	// shown: (folder, file name) -> the line she was last shown for it.
	shown map[[2]string]string
	// labels: folder -> the scope's label, for a scope that has since lost
	// its last note.
	labels map[string]string
}

func NewMemoryIndexReminderSource() *MemoryIndexReminderSource {
	return &MemoryIndexReminderSource{shown: map[[2]string]string{}, labels: map[string]string{}}
}

func (s *MemoryIndexReminderSource) Name() string { return "memory_index" }

func (s *MemoryIndexReminderSource) Points() []ReminderPoint {
	return []ReminderPoint{SESSION_START, USER_PROMPT_SUBMIT, POST_COMPACT}
}

func (s *MemoryIndexReminderSource) AppliesTo(agentCtx *ReminderAgentContext) bool {
	return agentCtx != nil && agentCtx.IsQueenStream && agentCtx.MemoryIndexProvider != nil
}

func (s *MemoryIndexReminderSource) Render(rctx ReminderContext) (string, bool) {
	settings := memoryRecallSettings(rctx.AgentCtx)
	if settings.Mode != "index" {
		// Listed in full again once this mode is set again.
		s.mu.Lock()
		s.shown = map[[2]string]string{}
		s.mu.Unlock()
		return "", false
	}
	if rctx.AgentCtx == nil || rctx.AgentCtx.MemoryIndexProvider == nil {
		return "", false
	}
	scopes := rctx.AgentCtx.MemoryIndexProvider()

	current := map[[2]string]string{}
	for _, scope := range scopes {
		for _, entry := range scope.Entries {
			current[[2]string{scope.Folder, entry.Name}] = entry.Line
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	full := rctx.Point == SESSION_START || rctx.Point == POST_COMPACT || len(s.shown) == 0
	shown := s.shown
	s.shown = current
	labels := map[string]string{}
	for folder, label := range s.labels {
		labels[folder] = label
	}
	for _, scope := range scopes {
		s.labels[scope.Folder] = scope.Label
	}
	if full {
		if len(scopes) == 0 {
			return "", false
		}
		return renderMemoryIndex(scopes), true
	}

	sections := []string{}
	for _, scope := range scopes {
		key := scope.Folder
		changed := []string{}
		for _, entry := range scope.Entries {
			if shown[[2]string{key, entry.Name}] != entry.Line {
				changed = append(changed, entry.Line)
			}
		}
		removed := sortedRemovedNames(shown, key, current)
		if len(changed) > 0 || len(removed) > 0 {
			sections = append(sections, renderMemoryIndexScope(scope.Label, key, changed, removed))
		}
	}
	// A scope whose last note was removed is no longer among scopes.
	listed := map[string]struct{}{}
	for _, scope := range scopes {
		listed[scope.Folder] = struct{}{}
	}
	stale := map[string]struct{}{}
	for key := range shown {
		if _, ok := listed[key[0]]; !ok {
			stale[key[0]] = struct{}{}
		}
	}
	staleFolders := make([]string, 0, len(stale))
	for folder := range stale {
		staleFolders = append(staleFolders, folder)
	}
	sort.Strings(staleFolders)
	for _, folder := range staleFolders {
		names := []string{}
		for key := range shown {
			if key[0] == folder {
				names = append(names, key[1])
			}
		}
		sort.Strings(names)
		label := labels[folder]
		if label == "" {
			label = folder
		}
		sections = append(sections, renderMemoryIndexScope(label, folder, nil, names))
	}
	if len(sections) == 0 {
		return "", false
	}
	return MemoryIndexChangedHeader + "\n\n" + strings.Join(sections, "\n\n"), true
}

// sortedRemovedNames yields the names a folder's last listing carried that
// the current listing no longer has, sorted.
func sortedRemovedNames(shown map[[2]string]string, folder string, current map[[2]string]string) []string {
	out := []string{}
	for key := range shown {
		if key[0] != folder {
			continue
		}
		if _, ok := current[key]; !ok {
			out = append(out, key[1])
		}
	}
	sort.Strings(out)
	return out
}

// renderMemoryIndex renders the full listing.
//
// Parity: framework/agent_loop/memory_reminders.py::_render_index
func renderMemoryIndex(scopes []MemoryNoteScope) string {
	sections := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		lines := make([]string, 0, len(scope.Entries))
		for _, entry := range scope.Entries {
			lines = append(lines, "- "+entry.Line)
		}
		sections = append(sections, "## "+scope.Label+" ("+scope.Folder+")\n"+strings.Join(lines, "\n"))
	}
	return MemoryIndexHeader + "\n\n" + strings.Join(sections, "\n\n")
}

// renderMemoryIndexScope renders one scope's diff.
//
// Parity: framework/agent_loop/memory_reminders.py::_render_scope
func renderMemoryIndexScope(label, folder string, changed, removed []string) string {
	parts := []string{"## " + label + " (" + folder + ")"}
	if len(changed) > 0 {
		lines := make([]string, 0, len(changed))
		for _, line := range changed {
			lines = append(lines, "- "+line)
		}
		parts = append(parts, "New or updated:\n"+strings.Join(lines, "\n"))
	}
	if len(removed) > 0 {
		lines := make([]string, 0, len(removed))
		for _, name := range removed {
			lines = append(lines, "- "+name)
		}
		parts = append(parts, "Removed:\n"+strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n")
}

// memoryRecallSettings reads the provider or keeps the defaults.
func memoryRecallSettings(agentCtx *ReminderAgentContext) MemoryRecallSettings {
	if agentCtx != nil && agentCtx.MemoryRecallSettingsProvider != nil {
		return agentCtx.MemoryRecallSettingsProvider()
	}
	return MemoryRecallSettings{Mode: "index", WaitSeconds: DefaultMemoryRecallWaitSeconds}
}
