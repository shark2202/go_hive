package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

const timelineSession = "session_20231015_100000_abcd1234"

type timelineFake struct {
	mu       sync.Mutex
	model    string
	itemsFor func(prompt string) []map[string]any
	prompts  []string
}

func (f *timelineFake) ModelName() string { return f.model }

func (f *timelineFake) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	prompt := ""
	if len(req.Messages) > 0 {
		prompt = req.Messages[0].Content
	}
	f.mu.Lock()
	f.prompts = append(f.prompts, prompt)
	f.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"items": f.itemsFor(prompt)})
	return llm.Completion{Content: string(payload), Model: f.model}, nil
}

func (f *timelineFake) promptList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.prompts...)
}

func mustSessionStart(t *testing.T, id string) time.Time {
	t.Helper()
	started, ok := SessionStartedAt(id)
	if !ok {
		t.Fatalf("cannot parse session start from %q", id)
	}
	return started
}

func repeatStrings(value string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func writeTimelineSession(t *testing.T, root, session string, userMessages []string) string {
	t.Helper()
	dir := filepath.Join(root, session)
	mkdirAll(t, dir)
	var lines []string
	for _, text := range userMessages {
		events := []map[string]any{
			{"type": "client_input_received", "data": map[string]any{"content": text}},
			{"type": "client_output_delta", "data": map[string]any{"snapshot": "ok", "iteration": 0, "inner_turn": 0}},
		}
		for _, event := range events {
			data, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(data))
		}
	}
	content := strings.Join(lines, "\n") + "\n"
	writeFile(t, filepath.Join(dir, "events.jsonl"), content)
	return dir
}

func readTimelineItems(t *testing.T, sessionDir string) []TimelineItem {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sessionDir, TimelineFile))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []TimelineItem{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var item TimelineItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("decode timeline line %q: %v", line, err)
		}
		out = append(out, item)
	}
	return out
}

// Parity: tests/test_memory_timeline_recall.py::test_extract_cleans_items_and_resolves_defaults
func TestExtractCleansItemsAndResolvesDefaults(t *testing.T) {
	itemsFor := func(_ string) []map[string]any {
		return []map[string]any{
			{"session": "S1", "turn": 2, "kind": "event", "text": "The user attended Jen's wedding.",
				"start": "2023-10-08", "end": "2023-10-07"},
			{"session": "S1", "turn": 1, "kind": "FACT", "text": "The user commutes 45 minutes each way.",
				"aliases": repeatStrings("drive to work", 6)},
			{"session": "S1", "turn": 99, "kind": "rumor", "text": "Out-of-range turn and unknown kind."},
			{"session": "S9", "text": "Unknown session label is dropped."},
			{"session": "S1", "text": "   "},
		}
	}

	session := SessionText{
		SessionID: timelineSession,
		StartedAt: mustSessionStart(t, timelineSession),
		Messages: []TimelineMessage{
			{Turn: 1, Text: "My commute is 45 minutes."},
			{Turn: 2, Text: "Back from Jen's wedding."},
		},
	}
	fake := &timelineFake{model: "fake-model", itemsFor: itemsFor}
	out, err := ExtractTimeline(context.Background(), fake, []SessionText{session}, 4)
	if err != nil {
		t.Fatal(err)
	}

	items := out[timelineSession]
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = item.Text
	}
	wantTexts := []string{
		"The user attended Jen's wedding.",
		"The user commutes 45 minutes each way.",
		"Out-of-range turn and unknown kind.",
	}
	if !reflect.DeepEqual(texts, wantTexts) {
		t.Fatalf("texts = %v", texts)
	}

	wedding, commute, odd := items[0], items[1], items[2]
	if wedding.Start != "2023-10-07" || wedding.End != "2023-10-08" {
		t.Fatalf("wedding dates not swapped into order: %s..%s", wedding.Start, wedding.End)
	}
	if commute.Kind != "fact" || commute.Start != "2023-10-15" || commute.End != "2023-10-15" {
		t.Fatalf("commute = %+v", commute)
	}
	if len(commute.Aliases) != 4 {
		t.Fatalf("aliases = %v", commute.Aliases)
	}
	if odd.Kind != "event" || odd.Turn != 1 {
		t.Fatalf("odd = %+v", odd)
	}
}

// Parity: tests/test_memory_timeline_recall.py::test_big_sessions_split_across_calls_but_keep_their_identity
func TestBigSessionsSplitAcrossCallsButKeepIdentity(t *testing.T) {
	long := strings.Repeat("x", 5000)
	s1ID, s2ID := "session_20230101_000000_s1", "session_20230102_000000_s2"
	s1 := SessionText{SessionID: s1ID, StartedAt: mustSessionStart(t, s1ID)}
	for i := 1; i <= 8; i++ {
		s1.Messages = append(s1.Messages, TimelineMessage{Turn: i, Text: long})
	}
	s2 := SessionText{
		SessionID: s2ID,
		StartedAt: mustSessionStart(t, s2ID),
		Messages:  []TimelineMessage{{Turn: 1, Text: "short"}},
	}

	batches := TimelineBatches([]SessionText{s1, s2})

	if len(batches) <= 1 {
		t.Fatalf("batches = %d, want more than one", len(batches))
	}
	turns := []int{}
	s2Seen := false
	for _, batch := range batches {
		total := 0
		for _, session := range batch {
			if session.SessionID == s2ID {
				s2Seen = true
			}
			for _, msg := range session.Messages {
				total += utf8.RuneCountInString(msg.Text)
				if session.SessionID == s1ID {
					turns = append(turns, msg.Turn)
				}
			}
		}
		if total > TimelineBatchChars {
			t.Fatalf("batch carries %d chars", total)
		}
	}
	sort.Ints(turns)
	wantTurns := []int{1, 2, 3, 4, 5, 6, 7, 8}
	if !reflect.DeepEqual(turns, wantTurns) {
		t.Fatalf("turns = %v", turns)
	}
	if !s2Seen {
		t.Fatal("second session missing from every batch")
	}
}

// Parity: tests/test_memory_timeline_recall.py::test_update_extracts_only_new_messages
func TestUpdateExtractsOnlyNewMessages(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeTimelineSession(t, root, timelineSession,
		[]string{"I bought a road bike.", "I rode 40 km today."})

	turnLineRe := regexp.MustCompile(`(?m)^\[(\d+)\]`)
	fake := &timelineFake{model: "fake-model", itemsFor: func(prompt string) []map[string]any {
		var items []map[string]any
		for _, match := range turnLineRe.FindAllStringSubmatch(prompt, -1) {
			items = append(items, map[string]any{
				"session": "S1",
				"turn":    match[1],
				"text":    "item from turn " + match[1],
			})
		}
		return items
	}}
	ctx := context.Background()

	if n, err := UpdateSessionTimeline(ctx, fake, sessionDir); err != nil || n != 2 {
		t.Fatalf("first update = %d, %v", n, err)
	}
	if n, err := UpdateSessionTimeline(ctx, fake, sessionDir); err != nil || n != 0 {
		t.Fatalf("second update = %d, %v", n, err)
	}
	if got := len(fake.promptList()); got != 1 {
		t.Fatalf("prompts after unchanged run = %d", got)
	}

	writeTimelineSession(t, root, timelineSession,
		[]string{"I bought a road bike.", "I rode 40 km today.", "I sold my old bike."})
	if n, err := UpdateSessionTimeline(ctx, fake, sessionDir); err != nil || n != 1 {
		t.Fatalf("third update = %d, %v", n, err)
	}
	prompts := fake.promptList()
	last := prompts[len(prompts)-1]
	if !strings.Contains(last, "[3] I sold my old bike.") || strings.Contains(last, "[1]") {
		t.Fatalf("last prompt = %q", last)
	}
	items := readTimelineItems(t, sessionDir)
	turns := make([]int, len(items))
	for i, item := range items {
		turns[i] = item.Turn
	}
	if !reflect.DeepEqual(turns, []int{1, 2, 3}) {
		t.Fatalf("timeline turns = %v", turns)
	}
}

// Parity: tests/test_memory_timeline_recall.py::test_backfill_reuses_cached_extractions
func TestBackfillReusesCachedExtractions(t *testing.T) {
	fake := &timelineFake{model: "fake-model", itemsFor: func(_ string) []map[string]any {
		return []map[string]any{{"session": "S1", "turn": 1, "text": "The user adopted a cat."}}
	}}
	root := t.TempDir()
	first := filepath.Join(root, "home1", "sessions")
	second := filepath.Join(root, "home2", "sessions")
	messages := []string{"We adopted a cat named Miso."}
	writeTimelineSession(t, first, timelineSession, messages)
	writeTimelineSession(t, second, timelineSession, messages)
	cache := filepath.Join(root, "cache")
	ctx := context.Background()

	if n, err := BackfillTimelines(ctx, fake, first, BackfillOptions{CacheDir: cache}); err != nil || n != 1 {
		t.Fatalf("first backfill = %d, %v", n, err)
	}
	if n, err := BackfillTimelines(ctx, fake, second, BackfillOptions{CacheDir: cache}); err != nil || n != 1 {
		t.Fatalf("second backfill = %d, %v", n, err)
	}
	if got := len(fake.promptList()); got != 1 {
		t.Fatalf("second home was not served from cache: prompts = %d", got)
	}
	firstItems := readTimelineItems(t, filepath.Join(first, timelineSession))
	secondItems := readTimelineItems(t, filepath.Join(second, timelineSession))
	if !reflect.DeepEqual(secondItems, firstItems) {
		t.Fatalf("timelines differ: %v vs %v", secondItems, firstItems)
	}
	if n, err := BackfillTimelines(ctx, fake, first, BackfillOptions{CacheDir: cache}); err != nil || n != 0 {
		t.Fatalf("already-current backfill = %d, %v", n, err)
	}
}

type fakeTimelineBus struct {
	id         string
	handler    func(streamID, stopReason string)
	subscribed bool
}

func (b *fakeTimelineBus) SubscribeLLMTurnComplete(handler func(streamID, stopReason string)) string {
	b.handler = handler
	b.subscribed = true
	return b.id
}

// Parity: tests/test_memory_timeline_recall.py::test_timeline_switch_controls_extraction_and_the_tool
func TestTimelineSwitchControlsExtraction(t *testing.T) {
	bus := &fakeTimelineBus{id: "sub_1"}
	fake := &timelineFake{model: "fake-model", itemsFor: func(string) []map[string]any { return nil }}
	sessionDir := filepath.Join(t.TempDir(), timelineSession)
	ctx := context.Background()

	t.Setenv("HIVE_MEMORY_TIMELINE", "0")
	if subs := SubscribeTimelineTriggers(ctx, bus, sessionDir, fake, 0); len(subs) != 0 {
		t.Fatalf("disabled switch returned %v", subs)
	}
	if bus.subscribed {
		t.Fatal("disabled switch still subscribed")
	}

	t.Setenv("HIVE_MEMORY_TIMELINE", "1")
	subs := SubscribeTimelineTriggers(ctx, bus, sessionDir, fake, 0)
	if !reflect.DeepEqual(subs, []string{"sub_1"}) {
		t.Fatalf("enabled switch returned %v", subs)
	}
	if !bus.subscribed || bus.handler == nil {
		t.Fatal("enabled switch did not subscribe")
	}
}

// Parity-exempt: Python's session_started_at is exercised only indirectly by
func TestSessionStartedAtParsesAndRejects(t *testing.T) {
	started, ok := SessionStartedAt("session_20231015_100000_abcd1234")
	if !ok {
		t.Fatal("canonical session id rejected")
	}
	want := time.Date(2023, 10, 15, 10, 0, 0, 0, time.Local)
	if !started.Equal(want) {
		t.Fatalf("started = %v, want %v", started, want)
	}
	for _, bad := range []string{"", "session_bad", "session_20231315_100000_abcd1234"} {
		if _, ok := SessionStartedAt(bad); ok {
			t.Fatalf("%q parsed", bad)
		}
	}
}

// Parity-exempt: Python's timeline user-message reader has no direct pytest;
func TestUserMessagesReadsOnlyClientInput(t *testing.T) {
	root := t.TempDir()
	sessionDir := writeTimelineSession(t, root, timelineSession, []string{"first", "  ", "third"})
	got := UserMessages(sessionDir)
	want := []string{"first", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user messages = %v", got)
	}
}

// Parity-exempt: content-addressed timeline cache keys are a Go-only helper; the
func TestTimelineCacheKeyIsContentAddressed(t *testing.T) {
	session := SessionText{
		SessionID: "session_20231015_100000_abcd1234",
		StartedAt: mustSessionStart(t, "session_20231015_100000_abcd1234"),
		Messages:  []TimelineMessage{{Turn: 1, Text: "hello"}},
	}
	key := TimelineCacheKey(session, "model-a")
	if len(key) != 32 {
		t.Fatalf("cache key length = %d", len(key))
	}
	if other := TimelineCacheKey(session, "model-a"); other != key {
		t.Fatal("cache key is not stable")
	}
	if other := TimelineCacheKey(session, "model-b"); other == key {
		t.Fatal("model not part of cache key")
	}
	session.Messages[0].Text = "changed"
	if other := TimelineCacheKey(session, "model-a"); other == key {
		t.Fatal("message text not part of cache key")
	}
}

// Parity-exempt: fenced-JSON tolerance for timeline replies has no Python pytest
func TestParseTimelineJSONToleratesFences(t *testing.T) {
	fenced := "Here you go:\n```json\n{\"items\": [{\"text\": \"x\"}]}\n```\n"
	data := ParseTimelineJSON(fenced)
	items, _ := data["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("fenced reply parsed as %v", data)
	}
	if got := ParseTimelineJSON("no json here"); len(got) != 0 {
		t.Fatalf("prose parsed as %v", got)
	}
	if got := ParseTimelineJSON("[]"); len(got) != 0 {
		t.Fatalf("list parsed as %v", got)
	}
}
