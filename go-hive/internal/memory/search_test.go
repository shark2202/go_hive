package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func searchHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func searchUserEvent(content string) map[string]any {
	return map[string]any{"type": "client_input_received", "data": map[string]any{"content": content}}
}

func searchAssistantDeltas(snapshot string, iteration, innerTurn int) []map[string]any {
	half := snapshot[:maxInt(1, len(snapshot)/2)]
	return []map[string]any{
		{"type": "client_output_delta", "data": map[string]any{
			"content": half, "snapshot": half, "iteration": iteration, "inner_turn": innerTurn,
		}},
		{"type": "client_output_delta", "data": map[string]any{
			"content": snapshot[len(half):], "snapshot": snapshot, "iteration": iteration, "inner_turn": innerTurn,
		}},
	}
}

func searchToolEvent(toolName, result string) map[string]any {
	return map[string]any{"type": "tool_call_completed", "data": map[string]any{
		"tool_use_id": "call_x", "tool_name": toolName, "result": result, "is_error": false,
	}}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func writeSearchQueenSession(t *testing.T, home, queen, session string, events []map[string]any) string {
	t.Helper()
	dir := filepath.Join(home, "queens", queen, "sessions", session)
	mkdirAll(t, dir)
	var lines []string
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	writeFile(t, filepath.Join(dir, "events.jsonl"), strings.Join(lines, "\n")+"\n")
	return dir
}

func writeSearchColonySession(t *testing.T, home, colony, queen, session string, events []map[string]any) string {
	t.Helper()
	dir := filepath.Join(home, "colonies", colony, "queens", queen, "sessions", session)
	mkdirAll(t, dir)
	var lines []string
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	writeFile(t, filepath.Join(dir, "events.jsonl"), strings.Join(lines, "\n")+"\n")
	return dir
}

func buildSearchTestSession(t *testing.T, home, queen, session string) {
	t.Helper()
	events := []map[string]any{
		searchUserEvent("step by step help me find a needle"),
	}
	events = append(events, searchAssistantDeltas("Sure — let me search for a needle now.", 0, 0)...)
	events = append(events,
		searchToolEvent("bash", "found NEEDLE_HIT in haystack"),
	)
	events = append(events, searchAssistantDeltas("I found it.", 1, 0)...)
	events = append(events, searchUserEvent("great, do another one"))
	events = append(events, searchAssistantDeltas("ok", 2, 0)...)
	events = append(events, searchToolEvent("bash", "nothing relevant here"))
	writeSearchQueenSession(t, home, queen, session, events)
}

func searchEnvelope(t *testing.T, home string, env map[string]string, opts SearchMessagesOptions) map[string]any {
	t.Helper()
	return SearchMessagesEnvelope(env, home, opts)
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_returns_turn_window
func TestSearchMessagesReturnsTurnWindow(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_HIT"})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	if result["total_matches"] != 1 {
		t.Fatalf("total_matches = %v, want 1", result["total_matches"])
	}
	matches := result["matches"].([]SearchMessageMatch)
	if len(matches) != 1 {
		t.Fatalf("len(matches) = %d", len(matches))
	}
	match := matches[0]
	if !strings.HasPrefix(match.Session, "session_") {
		t.Fatalf("session = %q", match.Session)
	}
	if match.SessionStartedAt == nil {
		t.Fatal("session_started_at must be set")
	}
	if match.Turn == nil {
		t.Fatal("turn window missing")
	}
	turn := *match.Turn
	roles := map[string]bool{}
	hits := 0
	for _, message := range turn {
		roles[message.Role] = true
		if message.IsHit {
			hits++
			if message.Role != "tool" {
				t.Fatalf("hit role = %q, want tool", message.Role)
			}
		}
	}
	if !roles["user"] {
		t.Fatalf("turn lacks the preceding user message: %+v", turn)
	}
	if hits != 1 {
		t.Fatalf("is_hit entries = %d, want 1", hits)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_role_filter
func TestSearchMessagesRoleFilter(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	for _, role := range []string{"user", "assistant", "tool"} {
		result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "(?i)needle", Role: role})
		if result["error"] != nil {
			t.Fatalf("role %s: %v", role, result)
		}
		matches := result["matches"].([]SearchMessageMatch)
		if len(matches) == 0 {
			t.Fatalf("role %s returned no matches", role)
		}
		for _, match := range matches {
			if match.Turn == nil {
				continue
			}
			for _, message := range *match.Turn {
				if message.IsHit && message.Role != role {
					t.Fatalf("role %s hit role %s", role, message.Role)
				}
			}
		}
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_inline_case_flag_works
func TestSearchMessagesInlineCaseFlagWorks(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	caseSensitive := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_hit"})
	if caseSensitive["total_matches"] != 0 {
		t.Fatalf("case-sensitive total = %v", caseSensitive["total_matches"])
	}
	caseInsensitive := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "(?i)NEEDLE_hit"})
	if caseInsensitive["total_matches"] != 1 {
		t.Fatalf("case-insensitive total = %v", caseInsensitive["total_matches"])
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_rejects_lookaround
func TestSearchMessagesRejectsLookaround(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "needle(?=foo)"})
	if result["error"] != "regex_unsupported" {
		t.Fatalf("error = %v", result["error"])
	}
	if result["feature"] != "lookahead" {
		t.Fatalf("feature = %v", result["feature"])
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_rejects_backreference
func TestSearchMessagesRejectsBackreference(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: `(foo)\1`})
	if result["error"] != "regex_unsupported" {
		t.Fatalf("error = %v", result["error"])
	}
	if result["feature"] != "backreference" {
		t.Fatalf("feature = %v", result["feature"])
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_errors_when_scope_unbound
func TestSearchMessagesErrorsWhenScopeUnbound(t *testing.T) {
	home := searchHome(t)
	result := searchEnvelope(t, home, map[string]string{}, SearchMessagesOptions{Pattern: "x"})
	if result["error"] != "scope_unbound" {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_errors_when_both_scopes_bound
func TestSearchMessagesErrorsWhenBothScopesBound(t *testing.T) {
	home := searchHome(t)
	env := map[string]string{"HIVE_QUEEN_ID": "queen_a", "HIVE_COLONY_NAME": "colony_b"}
	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "x"})
	if result["error"] != "scope_unbound" {
		t.Fatalf("error = %v", result["error"])
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_unknown_queen
func TestSearchMessagesUnknownQueen(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_real", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_does_not_exist"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "x"})
	if result["error"] != "scope_not_found" {
		t.Fatalf("error = %v", result["error"])
	}
	available, _ := result["available"].([]string)
	found := false
	for _, name := range available {
		if name == "queen_real" {
			found = true
		}
	}
	if !found {
		t.Fatalf("available = %v", available)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_context_none_omits_turn
func TestSearchMessagesContextNoneOmitsTurn(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_HIT", Context: "none"})
	matches := result["matches"].([]SearchMessageMatch)
	if len(matches) != 1 {
		t.Fatalf("matches = %d", len(matches))
	}
	if matches[0].Turn != nil {
		t.Fatalf("turn must be omitted, got %+v", *matches[0].Turn)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_context_narrow_only_hit
func TestSearchMessagesContextNarrowOnlyHit(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_HIT", Context: "narrow"})
	matches := result["matches"].([]SearchMessageMatch)
	if len(matches) != 1 || matches[0].Turn == nil {
		t.Fatalf("matches = %+v", matches)
	}
	turn := *matches[0].Turn
	if len(turn) != 1 || !turn[0].IsHit {
		t.Fatalf("narrow turn = %+v", turn)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_colony_search_spans_every_overseer_queen
func TestColonySearchSpansEveryOverseerQueen(t *testing.T) {
	home := searchHome(t)
	writeSearchColonySession(t, home, "acme", "queen_ops", "session_20260601_090000_aaaa",
		append([]map[string]any{searchUserEvent("kick off the COLONY_NEEDLE migration")}, searchAssistantDeltas("Starting it.", 0, 0)...))
	writeSearchColonySession(t, home, "acme", "queen_tech", "session_20260602_090000_bbbb",
		append([]map[string]any{searchUserEvent("status of COLONY_NEEDLE?")}, searchAssistantDeltas("Done.", 0, 0)...))
	env := map[string]string{"HIVE_COLONY_NAME": "acme"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "COLONY_NEEDLE"})
	if result["error"] != nil {
		t.Fatalf("unexpected error: %v", result)
	}
	matches := result["matches"].([]SearchMessageMatch)
	sessions := map[string]bool{}
	for _, match := range matches {
		sessions[match.Session] = true
	}
	if len(sessions) != 2 {
		t.Fatalf("sessions = %v", sessions)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_colony_search_finds_worker_tool_results
func TestColonySearchFindsWorkerToolResults(t *testing.T) {
	home := searchHome(t)
	workerResult := searchToolEvent("terminal_exec", "deployed build WORKER_NEEDLE-42 to staging")
	workerResult["stream_id"] = "worker:session_20260601_091500_cccc"
	writeSearchColonySession(t, home, "acme", "queen_ops", "session_20260601_090000_aaaa",
		[]map[string]any{searchUserEvent("ship it"), workerResult})
	env := map[string]string{"HIVE_COLONY_NAME": "acme"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "WORKER_NEEDLE-42", Role: "tool"})
	if result["total_matches"] != 1 {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_colony_and_queen_memories_stay_separate
func TestColonyAndQueenMemoriesStaySeparate(t *testing.T) {
	home := searchHome(t)
	writeSearchQueenSession(t, home, "queen_ops", "session_20260601_080000_dddd",
		[]map[string]any{searchUserEvent("private DM about DM_ONLY_NEEDLE")})
	writeSearchColonySession(t, home, "acme", "queen_ops", "session_20260601_090000_aaaa",
		[]map[string]any{searchUserEvent("colony work on COLONY_ONLY_NEEDLE")})

	colony := searchEnvelope(t, home, map[string]string{"HIVE_COLONY_NAME": "acme"}, SearchMessagesOptions{Pattern: "DM_ONLY_NEEDLE"})
	if colony["total_matches"] != 0 {
		t.Fatalf("colony saw DM-only text: %v", colony)
	}
	colony = searchEnvelope(t, home, map[string]string{"HIVE_COLONY_NAME": "acme"}, SearchMessagesOptions{Pattern: "COLONY_ONLY_NEEDLE"})
	if colony["total_matches"] != 1 {
		t.Fatalf("colony missed its own text: %v", colony)
	}

	queen := searchEnvelope(t, home, map[string]string{"HIVE_QUEEN_ID": "queen_ops"}, SearchMessagesOptions{Pattern: "COLONY_ONLY_NEEDLE"})
	if queen["total_matches"] != 0 {
		t.Fatalf("queen saw colony text: %v", queen)
	}
	queen = searchEnvelope(t, home, map[string]string{"HIVE_QUEEN_ID": "queen_ops"}, SearchMessagesOptions{Pattern: "DM_ONLY_NEEDLE"})
	if queen["total_matches"] != 1 {
		t.Fatalf("queen missed its own text: %v", queen)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_since_until_filter_results_not_just_indexing
func TestSinceUntilFilterResultsNotJustIndexing(t *testing.T) {
	home := searchHome(t)
	for _, session := range []string{"session_20260110_100000_aaaa", "session_20260505_100000_bbbb"} {
		writeSearchQueenSession(t, home, "queen_x", session, []map[string]any{searchUserEvent("NEEDLE_DATED")})
	}
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	all := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_DATED"})
	if all["total_matches"] != 2 {
		t.Fatalf("all = %v", all["total_matches"])
	}
	after := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_DATED", Since: "2026-04-01"})
	matches := after["matches"].([]SearchMessageMatch)
	if len(matches) != 1 || matches[0].Session != "session_20260505_100000_bbbb" {
		t.Fatalf("after = %+v", matches)
	}
	before := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "NEEDLE_DATED", Until: "2026-04-01"})
	matches = before["matches"].([]SearchMessageMatch)
	if len(matches) != 1 || matches[0].Session != "session_20260110_100000_aaaa" {
		t.Fatalf("before = %+v", matches)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_search_messages_finds_the_last_thing_the_assistant_said
func TestSearchMessagesFindsLastAssistantReply(t *testing.T) {
	home := searchHome(t)
	events := []map[string]any{searchUserEvent("write me a children's book about dinosaurs")}
	events = append(events, searchAssistantDeltas("Chapter three: the Plesiosaur, with its blue scaly body, glided past.", 0, 0)...)
	writeSearchQueenSession(t, home, "queen_books", "session_20230523_064400_dddd", events)
	env := map[string]string{"HIVE_QUEEN_ID": "queen_books"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "(?i)plesiosaur"})
	if result["total_matches"] != 1 {
		t.Fatalf("result = %v", result)
	}
	payload, err := json.Marshal(result["matches"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "blue scaly body") {
		t.Fatalf("matches lack the reply body: %s", payload)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_sync_excludes_out_of_scope_fields
func TestSearchMessagesExcludesOutOfScopeFields(t *testing.T) {
	home := searchHome(t)
	events := []map[string]any{
		{"type": "tool_call_started", "data": map[string]any{
			"tool_use_id": "call_1", "tool_name": "ZZZZ_secret_tool_name", "tool_input": map[string]any{"hidden_arg": "MUST_NOT_LEAK"},
		}},
		{"type": "llm_turn_complete", "data": map[string]any{
			"stop_reason": "tool_calls", "model": "x", "input_tokens": 999, "output_tokens": 999, "reasoning_details": "MUST_NOT_LEAK",
		}},
		{"type": "tool_call_started", "data": map[string]any{
			"tool_use_id": "call_2", "tool_name": "MUST_NOT_LEAK", "tool_input": map[string]any{"arg": "ZZZZ_secret_tool_name"},
		}},
		searchUserEvent("real user message"),
	}
	writeSearchQueenSession(t, home, "queen_x", "session_20260501_100000_aaaa", events)
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	for _, pattern := range []string{"ZZZZ_secret_tool_name", "MUST_NOT_LEAK", "input_tokens", "reasoning_details"} {
		result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: pattern})
		if result["total_matches"] != 0 {
			t.Fatalf("out-of-scope field %q was searchable: %v", pattern, result["matches"])
		}
	}
	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "real user message"})
	if result["total_matches"] != 1 {
		t.Fatalf("in-scope text missing: %v", result)
	}
}

// Parity: tools/tests/test_memory_tools.py::test_sync_indexes_spilled_tool_result
func TestSearchMessagesIndexesSpilledToolResult(t *testing.T) {
	home := searchHome(t)
	dir := filepath.Join(home, "queens", "queen_x", "sessions", "session_20260501_100000_aaaa")
	mkdirAll(t, filepath.Join(dir, "data"))
	spillPath := filepath.Join(dir, "data", "bash_1.txt")
	writeFile(t, spillPath, "the full body holds SPILL_NEEDLE deep inside")
	placeholder := "Tool `bash` returned 4,096 characters but only the first 2,000 were shown.\n\nFull result saved at: " + spillPath + "\n"
	writeSearchQueenSession(t, home, "queen_x", "session_20260501_100000_aaaa",
		[]map[string]any{searchUserEvent("run it"), searchToolEvent("bash", placeholder)})
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "SPILL_NEEDLE", Role: "tool"})
	if result["total_matches"] != 1 {
		t.Fatalf("result = %v", result)
	}
	payload, _ := json.Marshal(result["matches"])
	if !strings.Contains(string(payload), "SPILL_NEEDLE deep inside") {
		t.Fatalf("spill body not returned: %s", payload)
	}
}

// Parity-exempt: max_matches is not covered by a dedicated Python test; the
// slice semantics mirror tool.py's hits[:max_matches].
func TestSearchMessagesCapsMatches(t *testing.T) {
	home := searchHome(t)
	var events []map[string]any
	for i := 0; i < 5; i++ {
		events = append(events, searchUserEvent("CAP_NEEDLE in turn"))
	}
	writeSearchQueenSession(t, home, "queen_x", "session_20260501_100000_aaaa", events)
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "CAP_NEEDLE", MaxMatches: 2})
	if result["total_matches"] != 5 {
		t.Fatalf("total = %v", result["total_matches"])
	}
	if matches := result["matches"].([]SearchMessageMatch); len(matches) != 2 {
		t.Fatalf("kept = %d", len(matches))
	}
}

// Parity-exempt: the character caps are internal to enrich.py and have no
// dedicated Python test; this pins the observable marker behavior.
func TestSearchMessagesTrimsTurnToCaps(t *testing.T) {
	home := searchHome(t)
	longReply := strings.Repeat("prefix ", 400) + "TRIM_NEEDLE" + strings.Repeat(" suffix", 400)
	events := []map[string]any{searchUserEvent("please expand")}
	events = append(events, searchAssistantDeltas(longReply, 0, 0)...)
	writeSearchQueenSession(t, home, "queen_x", "session_20260501_100000_aaaa", events)
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{
		Pattern: "TRIM_NEEDLE", Context: "narrow", PerMessageCharCap: 200,
	})
	matches := result["matches"].([]SearchMessageMatch)
	if len(matches) != 1 || matches[0].Turn == nil {
		t.Fatalf("matches = %+v", matches)
	}
	content := (*matches[0].Turn)[0].Content
	if !strings.Contains(content, "TRIM_NEEDLE") {
		t.Fatalf("match trimmed away: %q", content)
	}
	if !strings.Contains(content, "chars omitted") {
		t.Fatalf("expected an omitted marker: %q", content)
	}
}

func TestSearchMessagesRejectsInvalidFilter(t *testing.T) {
	home := searchHome(t)
	buildSearchTestSession(t, home, "queen_x", "session_20260501_100000_aaaa")
	env := map[string]string{"HIVE_QUEEN_ID": "queen_x"}

	result := searchEnvelope(t, home, env, SearchMessagesOptions{Pattern: "x", Since: "not-a-date"})
	if result["error"] != "filter_invalid" {
		t.Fatalf("error = %v", result["error"])
	}
}

func TestSearchSessionMessagesOrdinals(t *testing.T) {
	home := searchHome(t)
	dir := writeSearchQueenSession(t, home, "queen_x", "session_20260501_100000_aaaa", []map[string]any{
		searchUserEvent("hello"),
	})
	events := []string{
		`{"type":"client_output_delta","data":{"snapshot":"partial","iteration":0,"inner_turn":0}}`,
		`{"type":"client_output_delta","data":{"snapshot":"complete answer","iteration":0,"inner_turn":0}}`,
		`{"type":"tool_call_completed","data":{"result":"tool body"}}`,
		`{"type":"client_input_received","data":{"content":"second"}}`,
	}
	writeFile(t, filepath.Join(dir, "events.jsonl"),
		`{"type":"client_input_received","data":{"content":"hello"}}`+"\n"+strings.Join(events, "\n")+"\n")

	messages := SearchSessionMessages(dir)
	if len(messages) != 4 {
		t.Fatalf("messages = %+v", messages)
	}
	want := []struct{ role, content string }{
		{"user", "hello"},
		{"assistant", "complete answer"},
		{"tool", "tool body"},
		{"user", "second"},
	}
	for i, expected := range want {
		if messages[i].Ordinal != i || messages[i].Role != expected.role || messages[i].Content != expected.content {
			t.Fatalf("message %d = %+v, want %+v", i, messages[i], expected)
		}
	}
}

func TestSearchSessionMessagesSkipsMalformedLines(t *testing.T) {
	home := searchHome(t)
	dir := filepath.Join(home, "queens", "queen_x", "sessions", "session_20260501_100000_aaaa")
	mkdirAll(t, dir)
	writeFile(t, filepath.Join(dir, "events.jsonl"),
		"not json\n"+`{"type":"client_input_received","data":{"content":"kept"}}`+"\n{}\n")

	messages := SearchSessionMessages(dir)
	if len(messages) != 1 || messages[0].Content != "kept" {
		t.Fatalf("messages = %+v", messages)
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
}
