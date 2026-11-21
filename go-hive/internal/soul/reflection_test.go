package soul

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
	"gopkg.in/yaml.v3"
)

const reflectionTestQueen = "queen_x"

// reflectionTestNow is the moment the upstream suite reflects at.
func reflectionTestNow() time.Time {
	return time.Date(2026, 10, 21, 5, 0, 0, 0, time.FixedZone("CST", 8*3600))
}

func writeReflectionTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeReflectionTestText(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

type reflectionTestHome struct {
	root      string
	queensDir string
	entry     map[string]any
}

func reflectionTestEntry() map[string]any {
	return map[string]any{
		"enabled": true,
		"sections": []any{
			map[string]any{"key": "now", "title": "Now", "limit": 80, "max_age_days": 14},
			map[string]any{"key": "people", "title": "Me and them", "limit": 80, "evidence": 2},
		},
		"sources": map[string]any{"file": "channels/x/sessions.json", "keys": []any{"/group/"}},
	}
}

func newReflectionTestHome(t *testing.T) *reflectionTestHome {
	t.Helper()
	root := t.TempDir()
	home := &reflectionTestHome{root: root, queensDir: filepath.Join(root, "queens"), entry: reflectionTestEntry()}
	if err := os.MkdirAll(filepath.Join(home.queensDir, reflectionTestQueen), 0o755); err != nil {
		t.Fatal(err)
	}
	home.saveConfig(t)
	writeReflectionTestJSON(t, filepath.Join(root, "channels", "x", "sessions.json"), map[string]any{
		"1/group/100":   "s_group",
		"1/private/42":  "s_private",
		"1/private/900": "s_dashboard",
	})
	return home
}

func (h *reflectionTestHome) saveConfig(t *testing.T) {
	t.Helper()
	writeReflectionTestJSON(t, filepath.Join(h.root, "configuration.json"), map[string]any{
		"soul": map[string]any{"queens": map[string]any{reflectionTestQueen: h.entry}},
	})
}

// set updates the queen's soul entry the way the upstream suite does before
// building a fresh config.
func (h *reflectionTestHome) set(t *testing.T, changes map[string]any) {
	t.Helper()
	for key, value := range changes {
		h.entry[key] = value
	}
	h.saveConfig(t)
}

func (h *reflectionTestHome) config(t *testing.T) map[string]any {
	t.Helper()
	value, _ := readJSONFile(filepath.Join(h.root, "configuration.json")).(map[string]any)
	return value
}

func (h *reflectionTestHome) soulConfig(t *testing.T) *SoulConfig {
	t.Helper()
	cfg := GetSoulConfig(h.config(t), reflectionTestQueen)
	if cfg == nil {
		t.Fatal("the queen has no soul configuration")
	}
	return cfg
}

func (h *reflectionTestHome) profile(t *testing.T, name string) {
	t.Helper()
	writeReflectionTestText(t, filepath.Join(h.queensDir, reflectionTestQueen, "profile.yaml"),
		"name: "+name+"\ntitle: Sketch artist\ncore_traits: "+name+" draws.\n")
}

// reflectionTestProfileSource is the reflection's profile seam in tests: it
// reads the profile.yaml the tests write and renders its core traits as the
// identity block. The queen-backed source lives in internal/cli
// (soulProfileSource) so internal/soul never imports internal/queen; the fake
// still proves that the name and identity the profile carries reach the
// reflection prompt.
type reflectionTestProfileSource struct{}

func (reflectionTestProfileSource) LoadQueenProfile(queensDir, queenID string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(queensDir, queenID, "profile.yaml"))
	if err != nil {
		return nil, err
	}
	profile := map[string]any{}
	if err := yaml.Unmarshal(data, &profile); err != nil {
		return nil, err
	}
	return profile, nil
}

func (reflectionTestProfileSource) FormatQueenIdentityPrompt(profile map[string]any, maxExamples *int) string {
	core, _ := profile["core_traits"].(string)
	if core == "" {
		return ""
	}
	return "<core_identity>\n" + core + "\n</core_identity>"
}

func (h *reflectionTestHome) session(t *testing.T, sessionID string, events []map[string]any, title string, channel any) string {
	t.Helper()
	dir := filepath.Join(h.queensDir, reflectionTestQueen, "sessions", sessionID)
	writeReflectionTestJSON(t, filepath.Join(dir, "meta.json"), map[string]any{"title": title, "channel": channel})
	writeReflectionTestEvents(t, dir, events)
	return dir
}

func writeReflectionTestEvents(t *testing.T, sessionDir string, events []map[string]any) {
	t.Helper()
	var builder strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(data)
		builder.WriteString("\n")
	}
	writeReflectionTestText(t, filepath.Join(sessionDir, "events.jsonl"), builder.String())
}

func appendReflectionTestEvents(t *testing.T, sessionDir string, events []map[string]any) {
	t.Helper()
	var builder strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		builder.Write(data)
		builder.WriteString("\n")
	}
	handle, err := os.OpenFile(filepath.Join(sessionDir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString(builder.String()); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func reflectionSaid(at, content, stream string) map[string]any {
	return map[string]any{"type": "client_input_received", "timestamp": at, "stream_id": stream, "data": map[string]any{"content": content}}
}

func reflectionReplied(at, snapshot string, turn int) map[string]any {
	return map[string]any{
		"type": "client_output_delta", "timestamp": at, "stream_id": "queen", "execution_id": "e",
		"data": map[string]any{"snapshot": snapshot, "iteration": turn, "inner_turn": 0},
	}
}

// reflectionGroupEvents is the upstream fixture conversation: a group message
// with context and a reminder, her two reply snapshots, and Bo's remark.
func reflectionGroupEvents() []map[string]any {
	return []map[string]any{
		reflectionSaid("2026-10-20T14:00:00", "<system-reminder>a brief</system-reminder>\n[recent lines]\nBo(7): lol\n\n[Ann(42)]: your lantern sketch is lovely", "queen"),
		reflectionReplied("2026-10-20T14:01:00", "Than", 1),
		reflectionReplied("2026-10-20T14:01:02", "Thank you, the next one will be a cat", 1),
		reflectionSaid("2026-10-20T15:00:00", "[Bo(7)]: Ann is always praising you", "queen"),
	}
}

// --- The fake model -------------------------------------------------------------------------

type reflectionTestLLM struct {
	mu       sync.Mutex
	turns    [][]llm.ToolCall
	calls    int
	requests []llm.CompletionRequest
	failWith error
	onCall   func(request llm.CompletionRequest)
}

func newReflectionTestLLM(turns ...[]llm.ToolCall) *reflectionTestLLM {
	return &reflectionTestLLM{turns: turns}
}

// reflectionAsk is one turn: a single tool call with these arguments.
func reflectionAsk(name string, args map[string]any) []llm.ToolCall {
	encoded, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return []llm.ToolCall{{ID: "c0", Name: name, Arguments: string(encoded)}}
}

func (l *reflectionTestLLM) AComplete(_ context.Context, request llm.CompletionRequest) (llm.Completion, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, request)
	if l.onCall != nil {
		l.onCall(request)
	}
	if l.failWith != nil {
		return llm.Completion{}, l.failWith
	}
	if l.calls >= len(l.turns) {
		return llm.Completion{Content: "done"}, nil
	}
	turn := l.turns[l.calls]
	l.calls++
	if len(turn) == 0 {
		return llm.Completion{Content: "done"}, nil
	}
	return llm.Completion{ToolCalls: turn}, nil
}

func (l *reflectionTestLLM) requestCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.requests)
}

func (l *reflectionTestLLM) firstRequest() llm.CompletionRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.requests) == 0 {
		return llm.CompletionRequest{}
	}
	return l.requests[0]
}

// reflectionTestResults lists every tool result the model was handed.
func reflectionTestResults(llm *reflectionTestLLM) []string {
	out := make([]string, 0)
	for _, request := range llm.requests {
		for _, message := range request.Messages {
			if message.Role == "tool" {
				out = append(out, message.Content)
			}
		}
	}
	return out
}

func reflectionTestContains(results []string, want string) bool {
	for _, result := range results {
		if strings.Contains(result, want) {
			return true
		}
	}
	return false
}

func reflectionTestReflect(t *testing.T, home *reflectionTestHome, llm *reflectionTestLLM, bootstrap bool) *Proposal {
	t.Helper()
	home.profile(t, "Fox")
	proposal, err := Reflect(ReflectionOptions{
		HomeRoot:      home.root,
		QueensDir:     home.queensDir,
		QueenID:       reflectionTestQueen,
		Config:        home.soulConfig(t),
		Completer:     llm,
		ProfileSource: reflectionTestProfileSource{},
		Bootstrap:     bootstrap,
		Now:           reflectionTestNow(),
	})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	return proposal
}

// --- What she read --------------------------------------------------------------------------

// Parity: core/tests/test_soul_reflection.py::test_she_reads_what_was_said_to_her_and_her_replies
func TestSheReadsWhatWasSaidToHerAndHerReplies(t *testing.T) {
	home := newReflectionTestHome(t)
	events := append([]map[string]any{reflectionSaid("2026-10-19T09:00:00", "[Old(1)]: before", "queen")}, reflectionGroupEvents()...)
	events = append(events, reflectionSaid("2026-10-20T16:00:00", "[W(9)]: x", "worker"))
	dir := home.session(t, "s_group", events, "Night owls", "x")

	chat := ReadChat(dir, "2026-10-19T23:00:00", 400, 10000, "Fox")

	want := []ReflectionChatLine{
		{At: "10-20 14:00", Who: "Ann(42)", Text: "your lantern sketch is lovely"},
		{At: "10-20 14:01", Who: "Fox", Text: "Thank you, the next one will be a cat"},
		{At: "10-20 15:00", Who: "Bo(7)", Text: "Ann is always praising you"},
	}
	if len(chat.Lines) != len(want) {
		t.Fatalf("lines = %#v, want %#v", chat.Lines, want)
	}
	for i, line := range want {
		if chat.Lines[i] != line {
			t.Fatalf("line %d = %#v, want %#v", i, chat.Lines[i], line)
		}
	}
	if chat.LastAt != "2026-10-20T15:00:00" {
		t.Fatalf("last_at = %q, want 2026-10-20T15:00:00", chat.LastAt)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_a_long_chat_keeps_its_newest_lines
func TestALongChatKeepsItsNewestLines(t *testing.T) {
	home := newReflectionTestHome(t)
	events := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		events = append(events, reflectionSaid(fmt.Sprintf("2026-10-20T1%d:00:00", i), fmt.Sprintf("[Ann(42)]: message number %d", i), "queen"))
	}
	dir := home.session(t, "s_group", events, "Night owls", "x")

	chat := ReadChat(dir, "", 400, 90, "Fox")

	texts := make([]string, 0, len(chat.Lines))
	for _, line := range chat.Lines {
		texts = append(texts, line.Text)
	}
	if strings.Join(texts, "|") != "message number 4|message number 5" {
		t.Fatalf("texts = %#v, want the newest two", texts)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_only_the_chats_the_source_file_lets_in_are_read
func TestOnlyTheChatsTheSourceFileLetsInAreRead(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	home.session(t, "s_private", reflectionGroupEvents(), "Ann", "x")
	home.session(t, "s_dashboard", reflectionGroupEvents(), "Planning", nil)

	dirs := SessionsToRead(home.root, home.queensDir, reflectionTestQueen, home.soulConfig(t))

	names := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		names = append(names, filepath.Base(dir))
	}
	if strings.Join(names, ",") != "s_group" {
		t.Fatalf("sessions = %#v, want only s_group", names)
	}
}

// Parity-exempt: the sourced filter has an upstream test; the include/exclude
// title branches of the same function do not, and they decide what she reads.
func TestTitleFiltersDecideWhatSheReads(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	home.set(t, map[string]any{"sources": nil, "include_titles": []any{"Night"}, "exclude_titles": []any{"Planning"}})
	home.session(t, "s_later", reflectionGroupEvents(), "Planning", "x")
	home.session(t, "s_other", reflectionGroupEvents(), "Chores", "x")

	dirs := SessionsToRead(home.root, home.queensDir, reflectionTestQueen, home.soulConfig(t))

	if len(dirs) != 1 || filepath.Base(dirs[0]) != "s_group" {
		t.Fatalf("sessions = %#v, want only s_group", dirs)
	}
}

// --- What the code lets through ---------------------------------------------------------------

// Parity: core/tests/test_soul_reflection.py::test_a_change_resting_on_what_was_said_waits_for_approval
func TestAChangeRestingOnWhatWasSaidWaitsForApproval(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "next I draw a cat", "date": "2026-10-20", "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("finish", map[string]any{"summary": "promised a cat"}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	if proposal == nil {
		t.Fatal("no proposal")
	}
	if len(proposal.Edits) != 1 || proposal.Edits[0].Op != "add" || proposal.Edits[0].Section != "now" || proposal.Edits[0].Text != "next I draw a cat" {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
	if len(proposal.Edits[0].Evidence) != 1 ||
		proposal.Edits[0].Evidence[0]["quote"] != "the next one will be a cat" ||
		proposal.Edits[0].Evidence[0]["where"] != "Night owls · Fox" {
		t.Fatalf("evidence = %#v", proposal.Edits[0].Evidence)
	}
	if proposal.Summary != "promised a cat" || proposal.Sources[0] != "Night owls" {
		t.Fatalf("summary/sources = %q %#v", proposal.Summary, proposal.Sources)
	}
	if !Load(home.queensDir, reflectionTestQueen).IsEmpty() {
		t.Fatal("review mode wrote the soul")
	}
	pending := PendingProposal(home.queensDir, reflectionTestQueen)
	if pending == nil || pending.ID != proposal.ID {
		t.Fatalf("pending proposal = %#v, want %s", pending, proposal.ID)
	}
	cursors, _ := LoadReflectionState(home.queensDir, reflectionTestQueen)["cursors"].(map[string]any)
	if cursors["s_group"] != "2026-10-20T15:00:00" {
		t.Fatalf("cursor = %#v, want the newest event read", cursors)
	}
	system := llm.firstRequest().System
	if !strings.HasPrefix(system, "You are Fox.") || !strings.Contains(system, "Fox draws.") {
		t.Fatalf("system prompt = %q", system)
	}
}

// Parity-exempt: the profile seam is Go-specific (internal/soul must not
// import internal/queen); upstream reads the profile store in the same
// package. Without an injected source a queen reads like one without a
// profile: her id, and no identity block.
func TestReflectionWithoutAProfileSourceUsesTheQueenID(t *testing.T) {
	home := newReflectionTestHome(t)
	home.profile(t, "Fox")
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("finish", map[string]any{"summary": "nothing changed"}),
	)

	proposal, err := Reflect(ReflectionOptions{
		HomeRoot:  home.root,
		QueensDir: home.queensDir,
		QueenID:   reflectionTestQueen,
		Config:    home.soulConfig(t),
		Completer: llm,
		Now:       reflectionTestNow(),
	})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if proposal == nil || len(proposal.Edits) != 0 {
		t.Fatalf("proposal = %#v", proposal)
	}
	system := llm.firstRequest().System
	if !strings.HasPrefix(system, "You are "+reflectionTestQueen+".") {
		t.Fatalf("system prompt = %q", system)
	}
	if strings.Contains(system, "Fox") {
		t.Fatalf("a reflection without a profile source read the profile anyway: %q", system)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_a_quote_nobody_said_is_refused_and_she_can_correct_it
func TestAQuoteNobodySaidIsRefusedAndSheCanCorrectIt(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "Ann loves me", "evidence": []any{"Ann said she loves me"}}),
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "Ann likes my lantern", "evidence": []any{"your lantern sketch is lovely"}}),
		reflectionAsk("finish", map[string]any{"summary": "ok"}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	if len(proposal.Edits) != 1 || proposal.Edits[0].Text != "Ann likes my lantern" {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
	results := reflectionTestResults(llm)
	if !reflectionTestContains(results, "is not in what you read") {
		t.Fatalf("the invented quote was not refused: %#v", results)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_a_section_that_needs_two_sources_refuses_one
func TestASectionThatNeedsTwoSourcesRefusesOne(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "people", "text": "Ann is kind to me", "evidence": []any{"your lantern sketch is lovely"}}),
		reflectionAsk("add_item", map[string]any{"section": "people", "text": "Ann is kind to me", "evidence": []any{"your lantern sketch is lovely", "Ann is always praising you"}}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	results := reflectionTestResults(llm)
	if !reflectionTestContains(results, "needs quotes from 2 different people or chats; you gave 1") {
		t.Fatalf("one source was enough: %#v", results)
	}
	if len(proposal.Edits) != 1 || proposal.Edits[0].Section != "people" || len(proposal.Edits[0].Evidence) != 2 {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_changes_past_the_budget_and_over_the_limit_are_refused
func TestChangesPastTheBudgetAndOverTheLimitAreRefused(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"max_edits": 1})
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": strings.Repeat("x", 81), "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "a cat next", "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "and a fox", "evidence": []any{"the next one will be a cat"}}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	results := reflectionTestResults(llm)
	if !reflectionTestContains(results, "would hold 81/80 characters") {
		t.Fatalf("the overflowing item was accepted: %#v", results)
	}
	if !reflectionTestContains(results, "all 1 changes") {
		t.Fatalf("the budget was not enforced: %#v", results)
	}
	if len(proposal.Edits) != 1 || proposal.Edits[0].Text != "a cat next" {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_saying_the_same_thing_twice_is_refused
func TestSayingTheSameThingTwiceIsRefused(t *testing.T) {
	home := newReflectionTestHome(t)
	cfg := home.soulConfig(t)
	if _, err := Save(home.queensDir, cfg, reflectionTestQueen, map[string][]Item{"now": {{Text: "a cat next"}}}, "owner", "v1", nil); err != nil {
		t.Fatal(err)
	}
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "a  cat next", "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "and a fox", "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "and a fox", "evidence": []any{"the next one will be a cat"}}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	results := reflectionTestResults(llm)
	if !reflectionTestContains(results, "already says that") {
		t.Fatalf("the duplicate was accepted: %#v", results)
	}
	if len(proposal.Edits) != 1 || proposal.Edits[0].Text != "and a fox" {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_items_past_their_age_are_dropped_on_their_own
func TestItemsPastTheirAgeAreDroppedOnTheirOwn(t *testing.T) {
	home := newReflectionTestHome(t)
	cfg := home.soulConfig(t)
	items := []Item{{Text: "drawing a lantern", Date: "2026-09-01"}, {Text: "tired today", Date: "2026-10-20"}}
	if _, err := Save(home.queensDir, cfg, reflectionTestQueen, map[string][]Item{"now": items}, "owner", "v1", nil); err != nil {
		t.Fatal(err)
	}
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("update_item", map[string]any{"section": "now", "number": 1, "text": "x", "evidence": []any{"the next one will be a cat"}}),
		reflectionAsk("finish", map[string]any{"summary": "nothing new"}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	if len(proposal.Edits) != 1 || proposal.Edits[0].Op != "remove" || proposal.Edits[0].Old != "drawing a lantern" || !proposal.Edits[0].Expired {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
	if !reflectionTestContains(reflectionTestResults(llm), "already being changed") {
		t.Fatal("an expiring item was still editable")
	}
}

// Parity: core/tests/test_soul_reflection.py::test_auto_mode_applies_the_changes_at_once
func TestAutoModeAppliesTheChangesAtOnce(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"mode": "auto"})
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "a cat next", "date": "2026-10-20", "evidence": []any{"the next one will be a cat"}}),
	)

	proposal := reflectionTestReflect(t, home, llm, false)

	items := Load(home.queensDir, reflectionTestQueen).Items("now")
	if len(items) != 1 || items[0].Text != "a cat next" || items[0].Date != "2026-10-20" {
		t.Fatalf("soul = %#v", items)
	}
	applied := LoadProposal(home.queensDir, reflectionTestQueen, proposal.ID)
	if applied == nil || applied.Status != "applied" {
		t.Fatalf("proposal = %#v", applied)
	}
	history := History(home.queensDir, reflectionTestQueen)
	if len(history) == 0 || history[0]["author"] != "her" {
		t.Fatalf("history = %#v", history)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_nothing_new_records_the_run_without_asking_the_model
func TestNothingNewRecordsTheRunWithoutAskingTheModel(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	if err := SaveReflectionState(home.queensDir, reflectionTestQueen, map[string]any{"cursors": map[string]any{"s_group": "2026-10-20T15:00:00"}}); err != nil {
		t.Fatal(err)
	}
	llm := newReflectionTestLLM()

	proposal := reflectionTestReflect(t, home, llm, false)

	if proposal != nil {
		t.Fatalf("proposal = %#v, want nil", proposal)
	}
	if llm.requestCount() != 0 {
		t.Fatalf("the model was asked %d times", llm.requestCount())
	}
	if lastRun, _ := LoadReflectionState(home.queensDir, reflectionTestQueen)["last_run"].(string); lastRun != "2026-10-21" {
		t.Fatalf("last_run = %q", lastRun)
	}
}

// Parity: core/tests/test_soul_reflection.py::test_the_first_draft_reads_the_listed_memories
func TestTheFirstDraftReadsTheListedMemories(t *testing.T) {
	home := newReflectionTestHome(t)
	writeReflectionTestText(t, filepath.Join(home.root, "memories", "agents", "queens", reflectionTestQueen, "me.md"),
		"---\nname: me\n---\nI used to hiss at everyone, now I tease softly.\n")
	home.set(t, map[string]any{"bootstrap_memories": []any{"me.md"}})
	llm := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "I tease softly now", "evidence": []any{"now I tease softly"}}),
	)

	proposal := reflectionTestReflect(t, home, llm, true)

	if proposal == nil || !proposal.Bootstrap {
		t.Fatalf("proposal = %#v", proposal)
	}
	if len(proposal.Sources) != 1 || proposal.Sources[0] != "memory me.md" {
		t.Fatalf("sources = %#v", proposal.Sources)
	}
	if len(proposal.Edits) != 1 || proposal.Edits[0].Evidence[0]["where"] != "memory me.md · memory" {
		t.Fatalf("edits = %#v", proposal.Edits)
	}
	if !strings.Contains(llm.firstRequest().Messages[0].Content, "This is your first time") {
		t.Fatalf("first message = %q", llm.firstRequest().Messages[0].Content)
	}
}

// --- When it runs -----------------------------------------------------------------------------

// Parity: core/tests/test_soul_reflection.py::test_when_the_reflection_is_due
func TestWhenTheReflectionIsDue(t *testing.T) {
	home := newReflectionTestHome(t)
	cfg := home.soulConfig(t)
	now := reflectionTestNow()

	if ReflectionDue(home.queensDir, reflectionTestQueen, cfg, time.Date(2026, 10, 21, 3, 59, 0, 0, now.Location())) {
		t.Fatal("due before the configured hour")
	}
	if !ReflectionDue(home.queensDir, reflectionTestQueen, cfg, now) {
		t.Fatal("not due after the configured hour")
	}
	if err := SaveReflectionState(home.queensDir, reflectionTestQueen, map[string]any{"last_run": "2026-10-21"}); err != nil {
		t.Fatal(err)
	}
	if ReflectionDue(home.queensDir, reflectionTestQueen, cfg, now) {
		t.Fatal("due again on a day already reflected")
	}
	if err := SaveReflectionState(home.queensDir, reflectionTestQueen, map[string]any{
		"attempts": map[string]any{"day": "2026-10-21", "count": 3, "at": "2026-10-21T01:00:00+08:00"},
	}); err != nil {
		t.Fatal(err)
	}
	if ReflectionDue(home.queensDir, reflectionTestQueen, cfg, now) {
		t.Fatal("due after three failed attempts")
	}
	if err := SaveReflectionState(home.queensDir, reflectionTestQueen, map[string]any{
		"attempts": map[string]any{"day": "2026-10-21", "count": 1, "at": "2026-10-21T04:30:00+08:00"},
	}); err != nil {
		t.Fatal(err)
	}
	if ReflectionDue(home.queensDir, reflectionTestQueen, cfg, now) {
		t.Fatal("due less than an hour after a failed attempt")
	}
	if err := SaveReflectionState(home.queensDir, reflectionTestQueen, map[string]any{
		"attempts": map[string]any{"day": "2026-10-21", "count": 1, "at": "2026-10-21T03:00:00+08:00"},
	}); err != nil {
		t.Fatal(err)
	}
	if !ReflectionDue(home.queensDir, reflectionTestQueen, cfg, now) {
		t.Fatal("not due two hours after a failed attempt")
	}
}

// Parity-exempt: proposal files are compared between the Go and Python runs;
// Python's dataclass writes [] for these lists on a not-yet-decided proposal,
// so Go must persist the same shape instead of null.
func TestASavedProposalWritesEmptyListsNotNull(t *testing.T) {
	home := newReflectionTestHome(t)
	pending := &Proposal{
		ID:          "2026-10-20-040000",
		CreatedAt:   "2026-10-20T04:00:00+08:00",
		BaseVersion: 0,
		Edits: []Edit{{
			Op: "remove", Section: "now", Old: "an old line", Reason: "older than 14 days", Expired: true,
		}},
		Status: "pending",
	}
	if err := SaveProposal(home.queensDir, reflectionTestQueen, pending); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(SoulDir(home.queensDir, reflectionTestQueen), "proposals", pending.ID+".json"))
	if err != nil {
		t.Fatalf("read saved proposal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode saved proposal: %v", err)
	}
	for _, key := range []string{"applied", "probes", "sources"} {
		list, ok := decoded[key].([]any)
		if !ok || len(list) != 0 {
			t.Fatalf("%s = %#v, want an empty list (Python writes [] not null): %s", key, decoded[key], raw)
		}
	}
	edits, _ := decoded["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("edits = %#v", decoded["edits"])
	}
	edit, _ := edits[0].(map[string]any)
	if evidence, ok := edit["evidence"].([]any); !ok || len(evidence) != 0 {
		t.Fatalf("edit evidence = %#v, want an empty list", edit["evidence"])
	}
}

// Parity: core/tests/test_soul_reflection.py::test_a_pending_proposal_holds_the_next_reflection_in_review_mode
func TestAPendingProposalHoldsTheNextReflectionInReviewMode(t *testing.T) {
	home := newReflectionTestHome(t)
	pending := &Proposal{ID: "2026-10-20-040000", CreatedAt: "", BaseVersion: 0, Edits: []Edit{}, Status: "pending"}
	if err := SaveProposal(home.queensDir, reflectionTestQueen, pending); err != nil {
		t.Fatal(err)
	}

	if ReflectionDue(home.queensDir, reflectionTestQueen, home.soulConfig(t), reflectionTestNow()) {
		t.Fatal("due while a proposal still waits for its decision")
	}
}

// Parity-exempt: each chat is read once; no upstream test runs the same
// session twice and checks that only the newer events are read.
func TestTheCursorReadsOnlyNewMessages(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"mode": "auto"})
	dir := home.session(t, "s_group", []map[string]any{
		reflectionSaid("2026-10-20T14:00:00", "[Ann(42)]: your lantern sketch is lovely", "queen"),
	}, "Night owls", "x")
	first := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "Ann likes my lantern", "evidence": []any{"your lantern sketch is lovely"}}),
	)
	reflectionTestReflect(t, home, first, false)

	appendReflectionTestEvents(t, dir, []map[string]any{
		reflectionSaid("2026-10-21T09:00:00", "[Bo(7)]: the cat drawing is great", "queen"),
	})
	second := newReflectionTestLLM(
		reflectionAsk("add_item", map[string]any{"section": "now", "text": "Bo likes the cat", "evidence": []any{"the cat drawing is great"}}),
	)
	proposal := reflectionTestReflect(t, home, second, false)

	if proposal == nil || len(proposal.Edits) != 1 {
		t.Fatalf("proposal = %#v", proposal)
	}
	prompt := second.firstRequest().Messages[0].Content
	if !strings.Contains(prompt, "the cat drawing is great") || strings.Contains(prompt, "lantern sketch is lovely") {
		t.Fatalf("the second reflection read the wrong events: %q", prompt)
	}
}

// --- Failures and retries -----------------------------------------------------------------------

// Parity-exempt: the retry ladder is upstream's run_due except-path; no
// upstream test drives it, and a failed day must never read as a run.
func TestAFailedReflectionIsRetriedAndNeverRecordedAsARun(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	now := reflectionTestNow()
	runner := NewDailyRunner(DailyRunnerOptions{
		HomeRoot:  home.root,
		QueensDir: home.queensDir,
		Config:    func() map[string]any { return home.config(t) },
		BuildCompleter: func(string, *SoulConfig) (llm.Completer, error) {
			return newReflectionTestLLM(), errors.New("no key")
		},
		Log: func(string, ...any) {},
	})

	if done := runner.RunDue(now); len(done) != 0 {
		t.Fatalf("done = %#v, want nothing reported as reflected", done)
	}
	state := LoadReflectionState(home.queensDir, reflectionTestQueen)
	if lastRun, _ := state["last_run"].(string); lastRun != "" {
		t.Fatalf("last_run = %q, want a failure not to count as a run", lastRun)
	}
	if message, _ := state["last_error"].(string); !strings.Contains(message, "no key") {
		t.Fatalf("last_error = %#v, want the failure recorded", state["last_error"])
	}
	attempts, _ := state["attempts"].(map[string]any)
	if count, _ := AsInt(attempts["count"]); count != 1 {
		t.Fatalf("attempts = %#v, want one attempt", attempts)
	}
	if ReflectionDue(home.queensDir, reflectionTestQueen, home.soulConfig(t), now.Add(30*time.Minute)) {
		t.Fatal("due again less than an hour after the failure")
	}
	if !ReflectionDue(home.queensDir, reflectionTestQueen, home.soulConfig(t), now.Add(61*time.Minute)) {
		t.Fatal("not due more than an hour after the failure")
	}
	if done := runner.RunDue(now.Add(61 * time.Minute)); len(done) != 0 {
		t.Fatalf("done = %#v, want nothing reported as reflected", done)
	}
	attempts, _ = LoadReflectionState(home.queensDir, reflectionTestQueen)["attempts"].(map[string]any)
	if count, _ := AsInt(attempts["count"]); count != 2 {
		t.Fatalf("attempts = %#v, want two attempts", attempts)
	}
	if done := runner.RunDue(now.Add(122 * time.Minute)); len(done) != 0 {
		t.Fatalf("done = %#v", done)
	}
	attempts, _ = LoadReflectionState(home.queensDir, reflectionTestQueen)["attempts"].(map[string]any)
	if count, _ := AsInt(attempts["count"]); count != 3 {
		t.Fatalf("attempts = %#v, want the third attempt", attempts)
	}
	if ReflectionDue(home.queensDir, reflectionTestQueen, home.soulConfig(t), now.Add(4*time.Hour)) {
		t.Fatal("a fourth attempt ran on the same day")
	}
}

// Parity-exempt: no upstream test for the loop; the server must catch up at
// startup, keep ticking, and stop cleanly.
func TestTheRunnerCatchesUpAtStartupAndStops(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"mode": "auto"})
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	built := make(chan string, 8)
	runner := NewDailyRunner(DailyRunnerOptions{
		HomeRoot:  home.root,
		QueensDir: home.queensDir,
		Config:    func() map[string]any { return home.config(t) },
		BuildCompleter: func(queenID string, _ *SoulConfig) (llm.Completer, error) {
			select {
			case built <- queenID:
			default:
			}
			return newReflectionTestLLM(
				reflectionAsk("add_item", map[string]any{"section": "now", "text": "a cat next", "evidence": []any{"the next one will be a cat"}}),
			), nil
		},
		Log: func(string, ...any) {},
		Now: reflectionTestNow,
	})
	stop := make(chan struct{})
	returned := make(chan struct{})
	go func() {
		runner.Run(stop, time.Hour, 0)
		close(returned)
	}()

	select {
	case <-built:
	case <-time.After(5 * time.Second):
		t.Fatal("the startup pass never ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(Load(home.queensDir, reflectionTestQueen).Items("now")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the startup pass did not reflect")
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("the runner did not stop")
	}
	if done := runner.RunDue(reflectionTestNow()); len(done) != 0 {
		t.Fatalf("done = %#v, want nothing due after the run", done)
	}
}

// Parity-exempt: upstream's is_reflecting guards the route; the Go port's
// reflection must report itself running while the model is answering.
func TestIsReflectingDuringARun(t *testing.T) {
	home := newReflectionTestHome(t)
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	sawRunning := false
	model := newReflectionTestLLM()
	model.onCall = func(_ llm.CompletionRequest) {
		if IsReflecting(reflectionTestQueen) {
			sawRunning = true
		}
	}

	reflectionTestReflect(t, home, model, false)

	if !sawRunning {
		t.Fatal("the reflection did not report itself running")
	}
	if IsReflecting(reflectionTestQueen) {
		t.Fatal("the reflection is still marked running")
	}
}

// Parity-exempt: the runner reads the live configuration, so a queen without
// soul configuration is left alone.
func TestRunDueSkipsQueensWithoutSoulConfiguration(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"enabled": false})
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")
	runner := NewDailyRunner(DailyRunnerOptions{
		HomeRoot:  home.root,
		QueensDir: home.queensDir,
		Config:    func() map[string]any { return home.config(t) },
		BuildCompleter: func(string, *SoulConfig) (llm.Completer, error) {
			t.Fatal("a disabled queen's reflection was built a model")
			return nil, nil
		},
		Log: func(string, ...any) {},
	})

	if done := runner.RunDue(reflectionTestNow()); len(done) != 0 {
		t.Fatalf("done = %#v", done)
	}
}
