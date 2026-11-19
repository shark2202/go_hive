package server

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

// Parity-exempt: the sediment has no upstream counterpart, so a state.json-less
// soul route reporting the sediment's bookkeeping is Go-only compatibility.
func TestSoulRouteShowsSedimentBookkeeping(t *testing.T) {
	home := soulTestHome(t)
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
