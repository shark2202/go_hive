package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

func writeEventsJSONL(t *testing.T, path string, count, linePadding int, trailingNewline bool) {
	t.Helper()
	lines := make([]string, count)
	for i := range lines {
		if linePadding > 0 {
			lines[i] = fmt.Sprintf(`{"i": %d, "pad": "%s"}`, i, strings.Repeat("x", linePadding))
		} else {
			lines[i] = fmt.Sprintf(`{"i": %d}`, i)
		}
	}
	text := strings.Join(lines, "\n")
	if trailingNewline && count > 0 {
		text += "\n"
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func pageAllEvents(t *testing.T, path string, limit int) (int, [][]int, []int) {
	t.Helper()

	events, startOffset, total, err := ReadEventsPage(path, limit, nil)
	if err != nil {
		t.Fatalf("tail read: %v", err)
	}
	returned := len(events)
	startIndex := 1
	if returned > 0 {
		startIndex = total - returned + 1
		if startIndex < 1 {
			startIndex = 1
		}
	}
	pages := [][]int{eventIndexes(events)}
	seqsByPage := [][]int{seqRange(startIndex, returned)}

	beforeOffset, beforeIndex := startOffset, startIndex
	for beforeOffset > 0 {
		offset := beforeOffset
		events, startOffset, _, err = ReadEventsPage(path, limit, &offset)
		if err != nil {
			t.Fatalf("cursor read at %d: %v", offset, err)
		}
		returned := len(events)
		cursorIndex := beforeIndex - returned
		if cursorIndex < 1 {
			cursorIndex = 1
		}
		pages = append(pages, eventIndexes(events))
		seqsByPage = append(seqsByPage, seqRange(cursorIndex, returned))
		beforeOffset, beforeIndex = startOffset, cursorIndex
	}

	var flat []int
	for index := len(seqsByPage) - 1; index >= 0; index-- {
		flat = append(flat, seqsByPage[index]...)
	}
	return total, pages, flat
}

func eventIndexes(events []map[string]any) []int {
	out := make([]int, len(events))
	for index, event := range events {
		switch value := event["i"].(type) {
		case json.Number:
			parsed, _ := strconv.Atoi(value.String())
			out[index] = parsed
		case float64:
			out[index] = int(value)
		}
	}
	return out
}

func seqRange(start, count int) []int {
	out := make([]int, count)
	for index := range out {
		out[index] = start + index
	}
	return out
}

func stitchNewestFirst(pages [][]int) []int {
	var stitched []int
	for _, page := range pages {
		stitched = append(append([]int{}, page...), stitched...)
	}
	return stitched
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// Parity: tests/test_events_history_page.py::test_small_file_paging
func TestEventsHistorySmallFilePaging(t *testing.T) {
	cases := []struct{ count, limit int }{
		{0, 5}, {1, 5}, {5, 5}, {10, 5}, {12, 5}, {50, 10}, {3, 500},
	}
	for _, trailing := range []bool{true, false} {
		for _, tc := range cases {
			name := fmt.Sprintf("count=%d/limit=%d/trailing=%v", tc.count, tc.limit, trailing)
			t.Run(name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "events.jsonl")
				if tc.count == 0 {
					if err := os.WriteFile(path, nil, 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					writeEventsJSONL(t, path, tc.count, 0, trailing)
				}

				total, pages, seqs := pageAllEvents(t, path, tc.limit)

				wantStitched := make([]int, tc.count)
				for i := range wantStitched {
					wantStitched[i] = i
				}
				if stitched := stitchNewestFirst(pages); !equalInts(stitched, wantStitched) {
					t.Fatalf("stitched = %v, want %v", stitched, wantStitched)
				}
				if total != tc.count {
					t.Fatalf("total = %d, want %d", total, tc.count)
				}
				wantSeqs := make([]int, tc.count)
				for i := range wantSeqs {
					wantSeqs[i] = i + 1
				}
				if !equalInts(seqs, wantSeqs) {
					t.Fatalf("seqs = %v, want %v", seqs, wantSeqs)
				}
				if tc.count > 0 && seqs[len(seqs)-1] != tc.count {
					t.Fatalf("newest seq = %d, want %d", seqs[len(seqs)-1], tc.count)
				}
				for _, page := range pages {
					if len(page) > tc.limit {
						t.Fatalf("page size %d exceeds limit %d", len(page), tc.limit)
					}
				}
			})
		}
	}
}

// Parity: tests/test_events_history_page.py::test_large_file_paging_crosses_threshold
func TestEventsHistoryLargeFilePagingCrossesThreshold(t *testing.T) {
	for _, trailing := range []bool{true, false} {
		t.Run(fmt.Sprintf("trailing=%v", trailing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			writeEventsJSONL(t, path, 300, 8192, trailing)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Size() <= eventsHistoryReverseTailThresholdBytes {
				t.Fatalf("file size %d did not cross the threshold %d", info.Size(), eventsHistoryReverseTailThresholdBytes)
			}

			total, pages, seqs := pageAllEvents(t, path, 50)

			wantStitched := make([]int, 300)
			for i := range wantStitched {
				wantStitched[i] = i
			}
			if stitched := stitchNewestFirst(pages); !equalInts(stitched, wantStitched) {
				t.Fatalf("stitched = %v, want 0..299", stitched)
			}
			if total != 300 {
				t.Fatalf("total = %d, want 300", total)
			}
			wantSeqs := seqRange(1, 300)
			if !equalInts(seqs, wantSeqs) {
				t.Fatalf("seqs = %v, want 1..300", seqs)
			}
			if len(pages) != 6 {
				t.Fatalf("pages = %d, want 6", len(pages))
			}
			size := 0
			for _, page := range pages {
				size += len(page)
			}
			if size != 300 {
				t.Fatalf("event count across pages = %d, want 300", size)
			}
		})
	}
}

// Parity: tests/test_events_history_page.py::test_first_page_is_the_tail
func TestEventsHistoryFirstPageIsTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventsJSONL(t, path, 100, 0, true)

	events, startOffset, total, err := ReadEventsPage(path, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := seqRange(90, 10)
	if got := eventIndexes(events); !equalInts(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if total != 100 {
		t.Fatalf("total = %d, want 100", total)
	}
	if startOffset <= 0 {
		t.Fatalf("start_offset = %d, want > 0 so has_more_older", startOffset)
	}
}

// Parity: tests/test_events_history_page.py::test_total_not_recomputed_on_cursor_pages
func TestEventsHistoryTotalNotRecomputedOnCursorPages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventsJSONL(t, path, 20, 0, true)

	_, startOffset, total, err := ReadEventsPage(path, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 20 {
		t.Fatalf("tail total = %d, want 20", total)
	}

	offset := startOffset
	_, _, cursorTotal, err := ReadEventsPage(path, 5, &offset)
	if err != nil {
		t.Fatal(err)
	}
	if cursorTotal != -1 {
		t.Fatalf("cursor total = %d, want -1", cursorTotal)
	}
}

// Parity: tests/test_events_history_page.py::test_final_page_signals_no_more
func TestEventsHistoryFinalPageSignalsNoMore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventsJSONL(t, path, 12, 0, true)

	_, startOffset, _, err := ReadEventsPage(path, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	lastOffset := startOffset
	for startOffset > 0 {
		lastOffset = startOffset
		offset := lastOffset
		_, startOffset, _, err = ReadEventsPage(path, 5, &offset)
		if err != nil {
			t.Fatal(err)
		}
	}
	if startOffset != 0 {
		t.Fatalf("start_offset = %d, want 0 at the file start", startOffset)
	}
}

// Parity: tests/test_events_history_page.py::test_missing_offset_zero_is_empty
func TestEventsHistoryMissingOffsetZeroIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventsJSONL(t, path, 10, 0, true)

	zero := int64(0)
	events, startOffset, total, err := ReadEventsPage(path, 5, &zero)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %v, want none", eventIndexes(events))
	}
	if startOffset != 0 {
		t.Fatalf("start_offset = %d, want 0", startOffset)
	}
	if total != -1 {
		t.Fatalf("total = %d, want -1 on a cursor read", total)
	}
}

func eventsHistoryApp(t *testing.T, sid string, deps SessionRouteDeps) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	RegisterSessionRoutes(mux, deps)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/sessions/"+sid+"/events/history", nil)
	mux.ServeHTTP(recorder, request)
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder, body
}

// Parity: tests/test_events_history_page.py::test_events_history_handler_success
func TestEventsHistoryHandlerSuccess(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sid := "session_20260101_ok"
	queenDir := filepath.Join(home.QueensDir(), "q", "sessions", sid)
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEventsJSONL(t, filepath.Join(queenDir, "events.jsonl"), 3, 0, true)

	recorder, body := eventsHistoryApp(t, sid, SessionRouteDeps{Home: home})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body["returned"] != float64(3) || body["total"] != float64(3) {
		t.Fatalf("body = %#v, want 3 returned / 3 total", body)
	}
}

// Parity: tests/test_events_history_page.py::test_events_history_times_out_instead_of_hanging
func TestEventsHistoryTimesOutInsteadOfHanging(t *testing.T) {
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sid := "session_20260101_timeout"
	queenDir := filepath.Join(home.QueensDir(), "q", "sessions", sid)
	if err := os.MkdirAll(queenDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEventsJSONL(t, filepath.Join(queenDir, "events.jsonl"), 1, 0, true)

	slowRead := func(string, int, *int64) ([]map[string]any, int64, int, error) {
		time.Sleep(200 * time.Millisecond)
		return nil, 0, 0, nil
	}
	recorder, body := eventsHistoryApp(t, sid, SessionRouteDeps{
		Home:              home,
		EventsReadPage:    slowRead,
		EventsReadTimeout: 20 * time.Millisecond,
	})
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if body["error"] != "events_history_timeout" {
		t.Fatalf("error = %#v, want events_history_timeout", body["error"])
	}
}

// Parity: tests/test_events_history_tail.py::test_truncate_strips_full_request_field
func TestTruncateStripsFullRequestField(t *testing.T) {
	event := map[string]any{
		"type": "context_usage_updated",
		"data": map[string]any{
			"usage_pct":    42,
			"full_request": map[string]any{"messages": []any{"small"}},
		},
	}
	out := TruncateLargeEvent(event)
	data := out["data"].(map[string]any)
	if data["usage_pct"] != 42 {
		t.Fatalf("usage_pct = %#v, want 42", data["usage_pct"])
	}
	marker, ok := data["full_request"].(map[string]any)
	if !ok || marker["_truncated"] != true || marker["_reason"] != "diagnostic_field" {
		t.Fatalf("full_request = %#v, want the diagnostic marker", data["full_request"])
	}
	original := event["data"].(map[string]any)["full_request"].(map[string]any)
	if len(original) != 1 {
		t.Fatalf("input full_request = %#v, want it unmutated", original)
	}
}

// Parity: tests/test_events_history_tail.py::test_truncate_replaces_oversized_field
func TestTruncateReplacesOversizedField(t *testing.T) {
	big := map[string]any{"blob": strings.Repeat("x", eventsHistoryMaxFieldBytes+1000)}
	event := map[string]any{
		"type": "tool_call_completed",
		"data": map[string]any{"tool_name": "t", "result": big},
	}
	out := TruncateLargeEvent(event)
	data := out["data"].(map[string]any)
	if data["tool_name"] != "t" {
		t.Fatalf("tool_name = %#v, want t", data["tool_name"])
	}
	marker, ok := data["result"].(map[string]any)
	if !ok || marker["_truncated"] != true {
		t.Fatalf("result = %#v, want the oversized marker", data["result"])
	}
	if size, ok := marker["_original_bytes"].(int); !ok || size <= eventsHistoryMaxFieldBytes {
		t.Fatalf("_original_bytes = %#v, want > %d", marker["_original_bytes"], eventsHistoryMaxFieldBytes)
	}

	payload := make([]any, 9000)
	for index := range payload {
		payload[index] = json.Number("1000000")
	}
	numeric := TruncateLargeEvent(map[string]any{"data": map[string]any{"payload": payload}})
	numericMarker, ok := numeric["data"].(map[string]any)["payload"].(map[string]any)
	if !ok || numericMarker["_truncated"] != true {
		t.Fatalf("numeric payload = %#v, want the oversized marker", numeric["data"].(map[string]any)["payload"])
	}
	if numericMarker["_original_bytes"] != 81000 {
		t.Fatalf("_original_bytes = %#v, want 81000 (Python json.dumps length)", numericMarker["_original_bytes"])
	}
}

// Parity: tests/test_events_history_tail.py::test_truncate_leaves_small_events_untouched
func TestTruncateLeavesSmallEventsUntouched(t *testing.T) {
	event := map[string]any{
		"type": "client_input_received",
		"data": map[string]any{"content": "hello"},
	}
	out := TruncateLargeEvent(event)
	out["type"] = "mutated"
	if event["type"] != "mutated" {
		t.Fatal("small event was copied instead of returned as the same object")
	}
}

// Parity: tests/test_events_history_tail.py::test_truncate_handles_missing_or_nondict_data
func TestTruncateHandlesMissingOrNonDictData(t *testing.T) {
	missing := TruncateLargeEvent(map[string]any{"type": "x"})
	if len(missing) != 1 || missing["type"] != "x" {
		t.Fatalf("missing data = %#v, want unchanged", missing)
	}
	nilData := TruncateLargeEvent(map[string]any{"type": "x", "data": nil})
	if len(nilData) != 2 || nilData["type"] != "x" || nilData["data"] != nil {
		t.Fatalf("nil data = %#v, want unchanged", nilData)
	}
}

// Parity-exempt: no upstream test distinguishes Python's int() query coercion
func TestQueryIntParamFollowsPythonInt(t *testing.T) {
	cases := []struct {
		raw   string
		want  int
		valid bool
	}{
		{"5", 5, true},
		{" 5 ", 5, true},
		{"\t7\n", 7, true},
		{"+5", 5, true},
		{"-5", -5, true},
		{"1_0", 10, true},
		{"007", 7, true},
		{"\u00a05", 5, true},
		{"1__0", 0, false},
		{"_1", 0, false},
		{"1_", 0, false},
		{"0x10", 0, false},
		{"1.5", 0, false},
		{"abc", 0, false},
		{"", 0, false},
		{"  ", 0, false},
	}
	for _, tc := range cases {
		request := httptest.NewRequest(http.MethodGet, "/?limit="+url.QueryEscape(tc.raw), nil)
		value, ok := queryIntParam(request, "limit", 500)
		if tc.valid {
			if !ok || value != tc.want {
				t.Fatalf("limit=%q → (%d, %v), want (%d, true)", tc.raw, value, ok, tc.want)
			}
			continue
		}
		if ok || value != 500 {
			t.Fatalf("limit=%q → (%d, %v), want (500, false)", tc.raw, value, ok)
		}
	}
}

// Parity-exempt: no upstream test pins Python's ASCII-only bytes.strip(). A
func TestEventsHistoryOnlyASCIIBlankLinesAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeEventsJSONL(t, path, 12, 0, true)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\u00a0\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	events, startOffset, total, err := ReadEventsPage(path, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 13 {
		t.Fatalf("total = %d, want 13 (the NBSP line still counts)", total)
	}
	if got := eventIndexes(events); !equalInts(got, []int{8, 9, 10, 11}) {
		t.Fatalf("tail events = %v, want [8 9 10 11]", got)
	}
	if startOffset != 72 {
		t.Fatalf("tail start_offset = %d, want 72", startOffset)
	}

	offset := startOffset
	events, startOffset, _, err = ReadEventsPage(path, 5, &offset)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventIndexes(events); !equalInts(got, []int{3, 4, 5, 6, 7}) {
		t.Fatalf("middle events = %v, want [3..7]", got)
	}
	if startOffset != 27 {
		t.Fatalf("middle start_offset = %d, want 27", startOffset)
	}

	offset = startOffset
	events, startOffset, _, err = ReadEventsPage(path, 5, &offset)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventIndexes(events); !equalInts(got, []int{0, 1, 2}) {
		t.Fatalf("first events = %v, want [0 1 2]", got)
	}
	if startOffset != 0 {
		t.Fatalf("first start_offset = %d, want 0", startOffset)
	}
}

// Parity-exempt: locks the json.dumps byte estimate for numbers, which no
func TestPythonJSONLenMatchesPythonDumps(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  int
	}{
		{"int literal", json.Number("1000000"), 7},
		{"negative zero literal", json.Number("-0"), 1},
		{"exponent literal", json.Number("1e5"), 8},
		{"float integral", float64(100000), 8},
		{"float large integral", float64(1e15), 18},
		{"float scientific", float64(1e16), 5},
		{"float small", float64(1.5e-07), 7},
		{"float threshold", float64(0.0001), 6},
		{"float negative zero", math.Copysign(0, -1), 4},
		{"float subnormal", float64(5e-324), 6},
		{"float infinity", math.Inf(1), 8},
		{"int list", []any{json.Number("1000000"), json.Number("1000000")}, 18},
		{"non-ascii string", "é", 8},
	}
	for _, tc := range cases {
		got, err := pythonJSONLen(tc.value)
		if err != nil || got != tc.want {
			t.Fatalf("%s: pythonJSONLen = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}
