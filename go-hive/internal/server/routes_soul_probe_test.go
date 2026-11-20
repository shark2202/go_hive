package server

// Parity: framework/server/tests/test_soul_routes.py::test_trying_her_out_needs_probe_questions_and_runs_in_the_background

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

func soulProbeTestMux(t *testing.T, home hivehome.Home, deps SoulProbeRouteDeps) *http.ServeMux {
	t.Helper()
	deps.Home = home
	mux := http.NewServeMux()
	RegisterSoulRoutes(mux, SoulRouteDeps{Home: home})
	RegisterSoulProbeRoutes(mux, deps)
	return mux
}

func writeSoulProbes(t *testing.T, home hivehome.Home, queenID, body string) {
	t.Helper()
	writeServerTestFile(t, filepath.Join(soulstore.SoulDir(home.QueensDir(), queenID), "probes.json"), body)
}

func saveSoulProposal(t *testing.T, home hivehome.Home, queenID, proposalID, status string) *soulstore.Proposal {
	t.Helper()
	proposal := &soulstore.Proposal{
		ID: proposalID, BaseVersion: 1, Status: status,
		Edits: []soulstore.Edit{{Op: "add", Section: "now", Text: "hiding a secret"}},
	}
	if err := soulstore.SaveProposal(home.QueensDir(), queenID, proposal); err != nil {
		t.Fatalf("save proposal: %v", err)
	}
	return proposal
}

// Parity: framework/server/tests/test_soul_routes.py::test_trying_her_out_needs_probe_questions_and_runs_in_the_background
func TestSoulProbeRoutesRunsInBackgroundAndStoresAnswers(t *testing.T) {
	home := soulTestHome(t)
	cfg := soulTestConfig(t, home)
	if _, err := soulstore.Save(home.QueensDir(), cfg, soulTestQueen,
		map[string][]soulstore.Item{"now": {{Text: "drawing a lantern", Date: "2026-10-20"}}},
		"owner", "v1", nil); err != nil {
		t.Fatalf("save soul: %v", err)
	}
	saveSoulProposal(t, home, soulTestQueen, "2026-10-21-040000", "pending")

	done := make(chan struct{})
	mux := soulProbeTestMux(t, home, SoulProbeRouteDeps{
		AnswerFor: func(queenID string, got *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error) {
			if queenID != soulTestQueen || got == nil || got.QueenID != soulTestQueen {
				t.Errorf("answerFor(%q, %#v), want queen %q", queenID, got, soulTestQueen)
			}
			return func(system, question string) (string, error) {
				if strings.Contains(system, "hiding a secret") {
					return "after: " + question, nil
				}
				return "before: " + question, nil
			}, nil
		},
		RunBackground: func(run func()) {
			go func() {
				run()
				close(done)
			}()
		},
	})

	// No probes.json yet: the route refuses before spawning anything.
	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusConflict || !strings.Contains(body["error"].(string), "probes.json") {
		t.Fatalf("without questions status = %d, body = %#v", recorder.Code, body)
	}

	writeSoulProbes(t, home, soulTestQueen, `[{"q":"what are you drawing?","anchor":"a lantern"}]`)
	_, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if soulTestInt(t, view["probes"]) != 1 || view["probing"] != false {
		t.Fatalf("view probes = %v, probing = %v", view["probes"], view["probing"])
	}

	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusAccepted || body["started"] != true {
		t.Fatalf("probe status = %d, body = %#v", recorder.Code, body)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not finish")
	}

	_, view = doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	proposal := credentialRouteJSONMap(t, view["proposal"])
	probes := credentialRouteJSONList(t, proposal["probes"])
	if len(probes) != 1 {
		t.Fatalf("stored probes = %#v", proposal["probes"])
	}
	answer := credentialRouteJSONMap(t, probes[0])
	if answer["q"] != "what are you drawing?" || answer["anchor"] != "a lantern" ||
		answer["before"] != "before: what are you drawing?" ||
		answer["after"] != "after: what are you drawing?" {
		t.Fatalf("answer = %#v", answer)
	}
	if view["probing"] != false {
		t.Fatalf("probing after finish = %v", view["probing"])
	}
}

// Parity: framework/server/tests/test_soul_routes.py::test_trying_her_out_needs_probe_questions_and_runs_in_the_background
func TestSoulProbeRoutesBusyAndMissingProposal(t *testing.T) {
	home := soulTestHome(t)
	saveSoulProposal(t, home, soulTestQueen, "2026-10-21-040000", "pending")
	writeSoulProbes(t, home, soulTestQueen, `[{"q":"hi"}]`)

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	mux := soulProbeTestMux(t, home, SoulProbeRouteDeps{
		AnswerFor: func(string, *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error) {
			return func(string, string) (string, error) {
				once.Do(func() { close(started) })
				<-release
				return "answered", nil
			}, nil
		},
		RunBackground: func(run func()) {
			go func() {
				run()
				close(done)
			}()
		},
	})

	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("first probe status = %d, body = %#v", recorder.Code, body)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("probe never started")
	}
	_, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if view["probing"] != true {
		t.Fatalf("probing = %v, want true", view["probing"])
	}
	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusConflict || body["error"] != "Already trying her out" {
		t.Fatalf("busy status = %d, body = %#v", recorder.Code, body)
	}

	// Missing and already-decided proposals are 404 regardless of probes.
	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/nope/probe", "")
	if recorder.Code != http.StatusNotFound || body["error"] != "No pending proposal by that id" {
		t.Fatalf("missing status = %d, body = %#v", recorder.Code, body)
	}
	saveSoulProposal(t, home, soulTestQueen, "2026-10-21-050000", "applied")
	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-050000/probe", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("applied status = %d, body = %#v", recorder.Code, body)
	}

	close(release)
	<-done
	_, view = doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if view["probing"] != false {
		t.Fatalf("probing after release = %v", view["probing"])
	}
}

// Parity: framework/server/routes_soul.py::handle_probe
func TestSoulProbeRoutesQueenErrorsAndUnavailable(t *testing.T) {
	home := soulTestHome(t)
	saveSoulProposal(t, home, soulTestQueen, "2026-10-21-040000", "pending")
	writeSoulProbes(t, home, soulTestQueen, `[{"q":"hi"}]`)

	mux := soulProbeTestMux(t, home, SoulProbeRouteDeps{})
	recorder, body := doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_missing/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusNotFound || !strings.Contains(body["error"].(string), "not found") {
		t.Fatalf("unknown queen status = %d, body = %#v", recorder.Code, body)
	}
	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_plain/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusConflict || !strings.Contains(body["error"].(string), "has no soul") {
		t.Fatalf("soulless queen status = %d, body = %#v", recorder.Code, body)
	}

	// A configured queen without model wiring answers 503, not a fake 202.
	recorder, body = doSessionJSON(t, mux, http.MethodPost,
		"/api/queen/queen_x/soul/proposals/2026-10-21-040000/probe", "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired status = %d, body = %#v", recorder.Code, body)
	}
}
