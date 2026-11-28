package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
)

// Parity-exempt: the sediment has no upstream counterpart, so a state.json-less
// soul route reporting the sediment's bookkeeping is Go-only compatibility.
func TestSoulRouteShowsSedimentBookkeeping(t *testing.T) {
	home := soulTestHome(t)
	// The bookkeeping fallback belongs to the auto path: only an applied
	// sediment writes sediment.json. In review mode the pass stages a proposal
	// and the soul view reports that instead
	// (TestSoulRouteShowsReviewSedimentProposal).
	writeServerTestFile(t, home.ConfigurationFile(), `{
  "soul": {"queens": {"queen_x": {"enabled": true, "mode": "auto", "sections": [
    {"key": "now", "title": "Now", "limit": 40},
    {"key": "people", "title": "Me and them", "limit": 60, "evidence": 2}
  ]}}}
}`)
	cfg := soulTestConfig(t, home)
	if _, err := soulstore.Sediment(soulstore.SedimentOptions{
		QueensDir:   home.QueensDir(),
		QueenID:     soulTestQueen,
		Config:      cfg,
		Conclusions: []soulstore.SedimentConclusion{{Text: "learned the lantern trick"}},
		Now:         time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("Sediment: %v", err)
	}

	mux := soulTestMux(SoulRouteDeps{Home: home})
	recorder, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if view["last_run"] != "2026-11-19" || view["last_result"] != "1 from memory reflection" {
		t.Fatalf("view = %#v", view)
	}
	if soulTestInt(t, view["version"]) != 1 {
		t.Fatalf("version = %v, want 1", view["version"])
	}

	// A real state.json (a later soul reflection) still wins over the fallback.
	writeServerTestFile(t, filepath.Join(soulstore.SoulDir(home.QueensDir(), soulTestQueen), "state.json"),
		`{"last_run": "2026-01-02", "last_result": "2 changes"}`)
	_, view = doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if view["last_run"] != "2026-01-02" || view["last_result"] != "2 changes" {
		t.Fatalf("state.json was overridden: %#v", view)
	}
}

// Parity-exempt: the sediment has no upstream counterpart. Two-sided
// counterpart of TestSoulRouteShowsSedimentBookkeeping: in review mode (the
// soul default) the pass leaves version 0 and the view reports the pending
// proposal the owner must decide.
func TestSoulRouteShowsReviewSedimentProposal(t *testing.T) {
	home := soulTestHome(t)
	cfg := soulTestConfig(t, home)
	if cfg.Mode != "review" {
		t.Fatalf("soulTestHome mode = %q, want the review default", cfg.Mode)
	}
	result, err := soulstore.Sediment(soulstore.SedimentOptions{
		QueensDir:   home.QueensDir(),
		QueenID:     soulTestQueen,
		Config:      cfg,
		Conclusions: []soulstore.SedimentConclusion{{Text: "learned the lantern trick", Source: "lantern.md"}},
		Now:         time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if !result.Proposed {
		t.Fatalf("review sediment = %#v", result)
	}

	mux := soulTestMux(SoulRouteDeps{Home: home})
	recorder, view := doSessionJSON(t, mux, http.MethodGet, "/api/queen/queen_x/soul", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if soulTestInt(t, view["version"]) != 0 {
		t.Fatalf("version = %v, want the untouched 0", view["version"])
	}
	section := soulTestSection(t, view, 0)
	if items, _ := section["items"].([]any); len(items) != 0 {
		t.Fatalf("review-mode sediment reached the soul sections: %#v", items)
	}
	proposal, _ := view["proposal"].(map[string]any)
	if proposal == nil {
		t.Fatalf("view has no proposal: %#v", view)
	}
	if proposal["status"] != "pending" || proposal["summary"] != "1 from memory reflection" {
		t.Fatalf("proposal = %#v", proposal)
	}
	edits, _ := proposal["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("proposal edits = %#v", edits)
	}
	edit, _ := edits[0].(map[string]any)
	if edit["op"] != "add" || edit["text"] != "learned the lantern trick" || edit["section"] != "now" {
		t.Fatalf("proposal edit = %#v", edit)
	}
	if _, err := os.Stat(filepath.Join(home.QueensDir(), soulTestQueen, "soul", "soul.json")); !os.IsNotExist(err) {
		t.Fatalf("review-mode sediment wrote soul.json: %v", err)
	}
}
