package server

// Parity: framework/server/tests/test_soul_routes.py

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

const soulTestQueen = "queen_x"

func soulTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	home := hivehome.Home{Root: t.TempDir()}
	writeServerTestFile(t, home.ConfigurationFile(), `{
  "soul": {"queens": {"queen_x": {"enabled": true, "sections": [
    {"key": "now", "title": "Now", "limit": 40},
    {"key": "people", "title": "Me and them", "limit": 60, "evidence": 2}
  ]}}}
}`)
	for _, queenID := range []string{soulTestQueen, "queen_plain"} {
		writeServerTestFile(t, filepath.Join(home.QueensDir(), queenID, "profile.yaml"), "name: Fox\n")
	}
	return home
}

func soulTestConfig(t *testing.T, home hivehome.Home) *soulstore.SoulConfig {
	t.Helper()
	cfg := soulstore.GetSoulConfig(HiveConfig(home), soulTestQueen)
	if cfg == nil {
		t.Fatal("soul config is missing")
	}
	return cfg
}

func soulTestMux(deps SoulRouteDeps) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterSoulRoutes(mux, deps)
	return mux
}

func soulTestInt(t *testing.T, value any) int {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("value is %T, want number", value)
	}
	return int(number)
}

func soulTestSection(t *testing.T, view map[string]any, index int) map[string]any {
	t.Helper()
	sections, ok := view["sections"].([]any)
	if !ok || len(sections) <= index {
		t.Fatalf("sections = %#v", view["sections"])
	}
	return credentialRouteJSONMap(t, sections[index])
}

// Parity: framework/server/tests/test_soul_routes.py::test_reading_a_soul_shows_its_sections_and_their_room
func TestSoulRoutesReadSectionsAndRoom(t *testing.T) {
	home := soulTestHome(t)
	cfg := soulTestConfig(t, home)
	if _, err := soulstore.Save(home.QueensDir(), cfg, soulTestQueen,
		map[string][]soulstore.Item{"now": {{Text: "drawing a lantern", Date: "2026-10-20"}}},
		"owner", "v1", nil); err != nil {
		t.Fatalf("save soul: %v", err)
	}
	mux := soulTestMux(SoulRouteDeps{Home: home})

	recorder, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if view["enabled"] != true || soulTestInt(t, view["version"]) != 1 || view["mode"] != "review" {
		t.Fatalf("view = %#v", view)
	}
	section := soulTestSection(t, view, 0)
	if section["key"] != "now" || section["title"] != "Now" || soulTestInt(t, section["limit"]) != 40 ||
		soulTestInt(t, section["used"]) != 17 || section["max_age_days"] != nil || soulTestInt(t, section["evidence"]) != 1 {
		t.Fatalf("section = %#v", section)
	}
	items := credentialRouteJSONList(t, section["items"])
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := credentialRouteJSONMap(t, items[0])
	if item["text"] != "drawing a lantern" || item["date"] != "2026-10-20" {
		t.Fatalf("item = %#v", item)
	}
	if view["proposal"] != nil {
		t.Fatalf("proposal = %#v", view["proposal"])
	}
	history := credentialRouteJSONList(t, view["history"])
	if len(history) == 0 || credentialRouteJSONMap(t, history[0])["note"] != "v1" {
		t.Fatalf("history = %#v", history)
	}

	_, plain := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_plain/soul", "")
	if len(plain) != 2 || plain["queen_id"] != "queen_plain" || plain["enabled"] != false {
		t.Fatalf("plain = %#v", plain)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_nobody/soul", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing queen status = %d", recorder.Code)
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_a_bad_edit_is_refused_and_nothing_changes
func TestSoulRoutesBadEditRefused(t *testing.T) {
	home := soulTestHome(t)
	mux := soulTestMux(SoulRouteDeps{Home: home})
	cases := []struct {
		name    string
		payload string
		error   string
	}{
		{"over limit", `{"sections":{"now":[{"text":"` + strings.Repeat("x", 41) + `"}]}}`, "Now: 41/40 characters"},
		{"unknown section", `{"sections":{"later":[]}}`, "Unknown sections: later"},
		{"bad date", `{"sections":{"now":[{"text":"ok","date":"tomorrow"}]}}`, "Bad date 'tomorrow': use YYYY-MM-DD"},
	}
	for _, testCase := range cases {
		recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_x/soul", testCase.payload)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, body = %s", testCase.name, recorder.Code, recorder.Body.String())
		}
		message, _ := body["error"].(string)
		if !strings.Contains(message, testCase.error) {
			t.Fatalf("%s: error = %q", testCase.name, message)
		}
	}
	if version := soulstore.Load(home.QueensDir(), soulTestQueen).Version; version != 0 {
		t.Fatalf("version = %d, want 0", version)
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_editing_by_hand_makes_a_new_version
func TestSoulRoutesEditingByHand(t *testing.T) {
	home := soulTestHome(t)
	mux := soulTestMux(SoulRouteDeps{Home: home})
	recorder, view := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_x/soul",
		`{"sections":{"now":[{"text":"  a  cat next ","date":"2026-10-21"},{"text":" "}]}}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if soulTestInt(t, view["version"]) != 1 {
		t.Fatalf("version = %v", view["version"])
	}
	items := credentialRouteJSONList(t, soulTestSection(t, view, 0)["items"])
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := credentialRouteJSONMap(t, items[0])
	if item["text"] != "a cat next" || item["date"] != "2026-10-21" {
		t.Fatalf("item = %#v", item)
	}
	history := credentialRouteJSONList(t, view["history"])
	if credentialRouteJSONMap(t, history[0])["author"] != "owner" {
		t.Fatalf("history = %#v", history)
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_approving_part_of_a_proposal_and_deciding_twice
func TestSoulRoutesApprovePartOfProposal(t *testing.T) {
	home := soulTestHome(t)
	proposal := &soulstore.Proposal{
		ID:          "2026-10-21-040000",
		CreatedAt:   "2026-10-21T04:00:00+08:00",
		BaseVersion: 0,
		Edits: []soulstore.Edit{
			{Op: "add", Section: "now", Text: "a cat next", Date: "2026-10-21"},
			{Op: "add", Section: "now", Text: "and a fox"},
		},
		Status: "pending",
	}
	if err := soulstore.SaveProposal(home.QueensDir(), soulTestQueen, proposal); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	mux := soulTestMux(SoulRouteDeps{Home: home})

	_, before := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if credentialRouteJSONMap(t, before["proposal"])["id"] != proposal.ID {
		t.Fatalf("before proposal = %#v", before["proposal"])
	}
	recorder, view := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/decide", `{"accept":[0]}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := credentialRouteJSONList(t, soulTestSection(t, view, 0)["items"])
	if len(items) != 1 {
		t.Fatalf("items = %#v", items)
	}
	item := credentialRouteJSONMap(t, items[0])
	if item["text"] != "a cat next" || item["date"] != "2026-10-21" {
		t.Fatalf("item = %#v", item)
	}
	if view["proposal"] != nil {
		t.Fatalf("proposal = %#v", view["proposal"])
	}

	if recorder, _ := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/decide", `{"accept":null}`); recorder.Code != http.StatusConflict {
		t.Fatalf("second decide status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/decide", `{"accept":"all"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("bad accept status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/nope/decide", `{}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("missing proposal status = %d", recorder.Code)
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_rolling_back_to_an_earlier_version
func TestSoulRoutesRollback(t *testing.T) {
	home := soulTestHome(t)
	cfg := soulTestConfig(t, home)
	for _, version := range []struct{ text, note string }{{"one", "v1"}, {"two", "v2"}} {
		if _, err := soulstore.Save(home.QueensDir(), cfg, soulTestQueen,
			map[string][]soulstore.Item{"now": {{Text: version.text}}}, "owner", version.note, nil); err != nil {
			t.Fatalf("save soul: %v", err)
		}
	}
	mux := soulTestMux(SoulRouteDeps{Home: home})

	recorder, view := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/rollback", `{"version":1}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if soulTestInt(t, view["version"]) != 3 {
		t.Fatalf("version = %v", view["version"])
	}
	items := credentialRouteJSONList(t, soulTestSection(t, view, 0)["items"])
	item := credentialRouteJSONMap(t, items[0])
	if item["text"] != "one" || item["date"] != "" {
		t.Fatalf("item = %#v", item)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/rollback", `{"version":7}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing version status = %d", recorder.Code)
	}
	if recorder, _ := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/rollback", `{"version":"1"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("string version status = %d", recorder.Code)
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_reflecting_now_runs_in_the_background_and_waits_for_a_pending_proposal
func TestSoulRoutesReflectNow(t *testing.T) {
	home := soulTestHome(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	bootstrapValues := make(chan bool, 1)
	var once sync.Once
	mux := soulTestMux(SoulRouteDeps{
		Home: home,
		Reflect: func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error {
			bootstrapValues <- bootstrap
			once.Do(func() { close(started) })
			<-release
			close(done)
			return nil
		},
	})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/reflect", `{"bootstrap":true}`)
	if recorder.Code != http.StatusAccepted || body["started"] != true {
		t.Fatalf("status = %d, body = %#v", recorder.Code, body)
	}
	<-started
	if bootstrap := <-bootstrapValues; bootstrap != true {
		t.Fatalf("bootstrap = %v, want true", bootstrap)
	}
	_, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if view["reflecting"] != true {
		t.Fatalf("reflecting = %v", view["reflecting"])
	}
	if recorder, held := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/reflect", `{}`); recorder.Code != http.StatusConflict ||
		!strings.Contains(held["error"].(string), "reflecting already") {
		t.Fatalf("second reflect status = %d, body = %#v", recorder.Code, held)
	}
	if err := soulstore.SaveProposal(home.QueensDir(), soulTestQueen, &soulstore.Proposal{
		ID: "2026-10-21-040000", CreatedAt: "", BaseVersion: 0, Status: "pending",
		Edits: []soulstore.Edit{{Op: "add", Section: "now", Text: "x"}},
	}); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	close(release)
	<-done
	if recorder, held := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/reflect", `{}`); recorder.Code != http.StatusConflict ||
		!strings.Contains(held["error"].(string), "waiting proposal") {
		t.Fatalf("pending reflect status = %d, body = %#v", recorder.Code, held)
	}
	_, after := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if after["reflecting"] != false {
		t.Fatalf("reflecting after = %v", after["reflecting"])
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_a_queen_without_a_soul_cannot_be_edited
func TestSoulRoutesQueenWithoutSoulRejected(t *testing.T) {
	home := soulTestHome(t)
	mux := soulTestMux(SoulRouteDeps{Home: home})
	recorder, body := doSessionJSON(t, mux, http.MethodPut, "/api/queen/queen_plain/soul", `{"sections":{}}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	message, _ := body["error"].(string)
	if !strings.Contains(message, "has no soul") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: no upstream route test pins the unwired-model answer; the Go
// port keeps answering 503 (an explicit "not available") instead of a fake 202.
func TestSoulRoutesReflectWithoutModelAnswers503(t *testing.T) {
	home := soulTestHome(t)
	mux := soulTestMux(SoulRouteDeps{Home: home})

	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/queen/queen_x/soul/reflect", `{}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if message, _ := body["error"].(string); !strings.Contains(message, "not available") {
		t.Fatalf("error = %q", message)
	}
}

// Parity-exempt: upstream keeps one lock per queen inside the reflection
// module; the Go route claims its reflecting slot atomically, so a burst of
// concurrent reflects admits exactly one run and answers the rest 409.
func TestSoulRoutesReflectConcurrentCallsReturn409(t *testing.T) {
	home := soulTestHome(t)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{}, 1)
	var once sync.Once
	mux := soulTestMux(SoulRouteDeps{
		Home: home,
		Reflect: func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error {
			once.Do(func() { close(started) })
			<-release
			done <- struct{}{}
			return nil
		},
	})

	post := func() int {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/queen/queen_x/soul/reflect", strings.NewReader("{}"))
		mux.ServeHTTP(recorder, request)
		return recorder.Code
	}

	const attempts = 8
	codes := make(chan int, attempts)
	var wait sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			codes <- post()
		}()
	}
	wait.Wait()
	close(codes)

	accepted, conflicted := 0, 0
	for code := range codes {
		switch code {
		case http.StatusAccepted:
			accepted++
		case http.StatusConflict:
			conflicted++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if accepted != 1 || conflicted != attempts-1 {
		t.Fatalf("accepted = %d, conflicted = %d, want 1 and %d", accepted, conflicted, attempts-1)
	}
	<-started
	close(release)
	<-done
}
