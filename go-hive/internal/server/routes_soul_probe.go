package server

// Parity: framework/server/routes_soul.py::handle_probe
//
// The probe route is mounted by the integrator beside RegisterSoulRoutes; it
// shares the same soul folder, proposal files and busy flag, so the soul view
// reports the probe count and whether she is answering now.

import (
	"errors"
	"log"
	"net/http"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/memory"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	soulstore "github.com/shark2202/go_hive/go-hive/internal/soul"
)

type SoulProbeRouteDeps struct {
	Home hivehome.Home
	// AnswerFor supplies the queen's model for one probe run. Nil leaves the
	// route answering 503, like a server without a configured model.
	AnswerFor func(queenID string, cfg *soulstore.SoulConfig) (soulstore.ProbeAnswerFunc, error)
	// RunBackground schedules the probe; nil runs it on a fresh goroutine.
	RunBackground func(func())
}

type SoulProbeRoutes struct {
	deps SoulProbeRouteDeps
}

// RegisterSoulProbeRoutes mounts:
//
//	POST /api/queen/{queen_id}/soul/proposals/{proposal_id}/probe
func RegisterSoulProbeRoutes(mux *http.ServeMux, deps SoulProbeRouteDeps) {
	if deps.Home.Root == "" {
		if home, err := hivehome.Resolve(); err == nil {
			deps.Home = home
		}
	}
	routes := &SoulProbeRoutes{deps: deps}
	mux.HandleFunc("POST /api/queen/{queen_id}/soul/proposals/{proposal_id}/probe", routes.handleProbeSoulProposal)
}

func (rt *SoulProbeRoutes) queensDir() string {
	return rt.deps.Home.QueensDir()
}

// probeQueen resolves the same two failures as _queen: an unknown queen is
// 404, a queen without a configured soul is 409.
func (rt *SoulProbeRoutes) probeQueen(w http.ResponseWriter, queenID string) (*soulstore.SoulConfig, string, bool) {
	profile, err := queen.LoadQueenProfile(rt.queensDir(), queenID)
	if err != nil {
		var missing *queen.ProfileNotFoundError
		if errors.As(err, &missing) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Queen '" + queenID + "' not found"})
			return nil, "", false
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return nil, "", false
	}
	cfg := soulstore.GetSoulConfig(HiveConfig(rt.deps.Home), queenID)
	if cfg == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "Queen '" + queenID + "' has no soul (configuration.json: soul.queens)",
		})
		return nil, "", false
	}
	maxExamples := 0
	identity := queen.FormatQueenIdentityPrompt(profile, &maxExamples)
	return cfg, identity, true
}

// handleProbeSoulProposal validates the proposal and starts the probe in the
// background. The answers land in the proposal; this response only says it
// started, exactly like upstream.
//
// Parity: framework/server/routes_soul.py::handle_probe
func (rt *SoulProbeRoutes) handleProbeSoulProposal(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	cfg, identity, ok := rt.probeQueen(w, queenID)
	if !ok {
		return
	}

	proposal := soulstore.LoadProposal(rt.queensDir(), queenID, r.PathValue("proposal_id"))
	if proposal == nil || proposal.Status != "pending" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No pending proposal by that id"})
		return
	}
	if len(soulstore.LoadProbes(rt.queensDir(), queenID)) == 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "No probe questions: add them to " + soulstore.ProbesFile + " in her soul folder",
		})
		return
	}
	if soulstore.IsProbing(queenID) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Already trying her out"})
		return
	}
	if rt.deps.AnswerFor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "soul probing is not available"})
		return
	}

	// Claim the slot before scheduling so a second POST cannot start a
	// duplicate run while the goroutine is still being handed off.
	if !soulstore.TryBeginProbing(queenID) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Already trying her out"})
		return
	}
	answerFor := rt.deps.AnswerFor
	queensDir := rt.queensDir()
	// Recall brings a speaker's memory files beside a live message; the probe
	// reads the same two scopes, her own before global, so the answers are
	// about the same person the chat is about.
	// Parity: framework/agents/queen/soul_probes.py::related_memories
	memoryDirs := []string{
		memory.QueenMemoryDir(rt.deps.Home.Root, queenID),
		memory.GlobalMemoryDir(rt.deps.Home.Root),
	}

	run := func() {
		defer soulstore.EndProbing(queenID)
		answer, err := answerFor(queenID, cfg)
		if err != nil {
			log.Printf("soul: probing %s: no model: %v", queenID, err)
			return
		}
		if _, err := soulstore.Probe(soulstore.ProbeOptions{
			QueensDir:  queensDir,
			QueenID:    queenID,
			Config:     cfg,
			Proposal:   proposal,
			Identity:   identity,
			MemoryDirs: memoryDirs,
			Answer:     answer,
		}); err != nil {
			log.Printf("soul: probing %s failed: %v", queenID, err)
		}
	}

	if rt.deps.RunBackground != nil {
		rt.deps.RunBackground(run)
	} else {
		go run()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}
