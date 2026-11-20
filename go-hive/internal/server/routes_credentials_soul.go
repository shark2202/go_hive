package server

// Parity: framework/server/routes_soul.py::register_routes

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/queen"
	soulstore "github.com/aden-hive/hive-go/internal/soul"
)

type SoulRouteDeps struct {
	Home          hivehome.Home
	Reflect       func(queenID string, cfg *soulstore.SoulConfig, bootstrap bool) error
	RunBackground func(func())
}

type SoulRoutes struct {
	deps       SoulRouteDeps
	mu         sync.Mutex
	reflecting map[string]bool
}

func RegisterSoulRoutes(mux *http.ServeMux, deps SoulRouteDeps) {
	if deps.Home.Root == "" {
		if home, err := hivehome.Resolve(); err == nil {
			deps.Home = home
		}
	}
	routes := &SoulRoutes{deps: deps, reflecting: map[string]bool{}}
	mux.HandleFunc("GET /api/queen/{queen_id}/soul", routes.handleGetSoul)
	mux.HandleFunc("PUT /api/queen/{queen_id}/soul", routes.handlePutSoul)
	mux.HandleFunc("POST /api/queen/{queen_id}/soul/proposals/{proposal_id}/decide", routes.handleDecideSoulProposal)
	mux.HandleFunc("POST /api/queen/{queen_id}/soul/rollback", routes.handleRollbackSoul)
	mux.HandleFunc("POST /api/queen/{queen_id}/soul/reflect", routes.handleReflectSoul)
}

func (rt *SoulRoutes) queenDirectory() string {
	return rt.deps.Home.QueensDir()
}

func (rt *SoulRoutes) queenConfig(w http.ResponseWriter, queenID string) (*soulstore.SoulConfig, bool) {
	if _, err := queen.LoadQueenProfile(rt.queenDirectory(), queenID); err != nil {
		var missing *queen.ProfileNotFoundError
		if errors.As(err, &missing) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Queen '" + queenID + "' not found"})
			return nil, false
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return nil, false
	}
	cfg := soulstore.GetSoulConfig(HiveConfig(rt.deps.Home), queenID)
	if cfg == nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "Queen '" + queenID + "' has no soul (configuration.json: soul.queens)",
		})
		return nil, false
	}
	return cfg, true
}

func (rt *SoulRoutes) soulView(queenID string, cfg *soulstore.SoulConfig) map[string]any {
	queensDir := rt.queenDirectory()
	current := soulstore.Load(queensDir, queenID)
	proposal := soulstore.PendingProposal(queensDir, queenID)
	state := loadSoulState(queensDir, queenID)
	lastRun, lastResult := state["last_run"], state["last_result"]
	// Compatibility read: the ported daily soul reflection now writes state.json
	// (last_run/last_result), so those keys win. Older homes only have the
	// memory-reflection sediment bookkeeping; fill missing keys from it.
	if lastRun == nil {
		if sediment := soulstore.LoadSedimentState(queensDir, queenID); sediment != nil && sediment.LastRun != "" {
			lastRun = sediment.LastRun
			lastResult = fmt.Sprintf("%d from memory reflection", sediment.LastAdded)
		}
	}

	sections := make([]any, 0, len(cfg.Sections))
	for _, section := range cfg.Sections {
		items := current.Items(section.Key)
		itemDicts := make([]any, 0, len(items))
		for _, item := range items {
			itemDicts = append(itemDicts, map[string]any{"text": item.Text, "date": item.Date})
		}
		var maxAge any
		if section.MaxAgeDays != nil {
			maxAge = *section.MaxAgeDays
		}
		sections = append(sections, map[string]any{
			"key":          section.Key,
			"title":        section.Title,
			"limit":        section.Limit,
			"used":         soulstore.SectionChars(items),
			"max_age_days": maxAge,
			"evidence":     section.Evidence,
			"items":        itemDicts,
		})
	}

	history := soulstore.History(queensDir, queenID)
	if history == nil {
		history = []map[string]any{}
	}
	if len(history) > 50 {
		history = history[:50]
	}

	var proposalValue any
	if proposal != nil {
		proposalValue = soulProposalJSON(proposal)
	}

	return map[string]any{
		"queen_id":    queenID,
		"enabled":     true,
		"mode":        cfg.Mode,
		"hour":        cfg.Hour,
		"version":     current.Version,
		"updated_at":  current.UpdatedAt,
		"sections":    sections,
		"proposal":    proposalValue,
		"history":     history,
		"reflecting":  rt.isReflecting(queenID),
		"probes":      len(soulstore.LoadProbes(queensDir, queenID)),
		"probing":     soulstore.IsProbing(queenID),
		"last_run":    lastRun,
		"last_result": lastResult,
	}
}

func soulProposalJSON(proposal *soulstore.Proposal) map[string]any {
	edits := make([]any, 0, len(proposal.Edits))
	for _, edit := range proposal.Edits {
		evidence := make([]any, 0, len(edit.Evidence))
		for _, entry := range edit.Evidence {
			evidence = append(evidence, entry)
		}
		edits = append(edits, map[string]any{
			"op":       edit.Op,
			"section":  edit.Section,
			"text":     edit.Text,
			"old":      edit.Old,
			"date":     edit.Date,
			"evidence": evidence,
			"reason":   edit.Reason,
			"expired":  edit.Expired,
		})
	}
	applied := make([]any, 0, len(proposal.Applied))
	for _, index := range proposal.Applied {
		applied = append(applied, index)
	}
	sources := make([]any, 0, len(proposal.Sources))
	for _, source := range proposal.Sources {
		sources = append(sources, source)
	}
	probes := make([]any, 0, len(proposal.Probes))
	for _, probe := range proposal.Probes {
		probes = append(probes, probe)
	}
	return map[string]any{
		"id":           proposal.ID,
		"created_at":   proposal.CreatedAt,
		"base_version": proposal.BaseVersion,
		"edits":        edits,
		"summary":      proposal.Summary,
		"status":       proposal.Status,
		"applied":      applied,
		"sources":      sources,
		"bootstrap":    proposal.Bootstrap,
		"probes":       probes,
	}
}

func loadSoulState(queensDir, queenID string) map[string]any {
	path := filepath.Join(soulstore.SoulDir(queensDir, queenID), "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}
	value, err := decodeJSONBytes(data)
	if err != nil {
		return map[string]any{}
	}
	state, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return state
}

func (rt *SoulRoutes) isReflecting(queenID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.reflecting[queenID]
}

// beginReflecting claims the queen's reflecting slot. It reports false when
// another request already holds it, so two concurrent reflects cannot both
// start (exactly one 202, the rest 409).
func (rt *SoulRoutes) beginReflecting(queenID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.reflecting[queenID] {
		return false
	}
	rt.reflecting[queenID] = true
	return true
}

func (rt *SoulRoutes) endReflecting(queenID string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.reflecting, queenID)
}

// Parity: framework/server/routes_soul.py::handle_get_soul
func (rt *SoulRoutes) handleGetSoul(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	if _, err := queen.LoadQueenProfile(rt.queenDirectory(), queenID); err != nil {
		var missing *queen.ProfileNotFoundError
		if errors.As(err, &missing) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "Queen '" + queenID + "' not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	cfg := soulstore.GetSoulConfig(HiveConfig(rt.deps.Home), queenID)
	if cfg == nil {
		writeJSON(w, http.StatusOK, map[string]any{"queen_id": queenID, "enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, rt.soulView(queenID, cfg))
}

// Parity: framework/server/routes_soul.py::handle_put_soul
func (rt *SoulRoutes) handlePutSoul(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	cfg, ok := rt.queenConfig(w, queenID)
	if !ok {
		return
	}
	body, ok := soulJSONBody(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be {'sections': {key: [{'text', 'date'}]}}"})
		return
	}
	raw, ok := body["sections"].(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be {'sections': {key: [{'text', 'date'}]}}"})
		return
	}
	known := map[string]struct{}{}
	for _, section := range cfg.Sections {
		known[section.Key] = struct{}{}
	}
	unknown := make([]string, 0)
	for key := range raw {
		if _, exists := known[key]; !exists {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Unknown sections: " + strings.Join(unknown, ", ")})
		return
	}
	sections := map[string][]soulstore.Item{}
	for key, value := range raw {
		entries, isList := value.([]any)
		if !isList {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Section " + key + " must be a list"})
			return
		}
		items := make([]soulstore.Item, 0, len(entries))
		for _, entry := range entries {
			mapping, isMap := entry.(map[string]any)
			if !isMap {
				continue
			}
			text := ""
			if pythonTruthy(mapping["text"]) {
				text = pyStr(mapping["text"])
			}
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			day := ""
			if pythonTruthy(mapping["date"]) {
				day = pyStr(mapping["date"])
			}
			if day != "" {
				parsed, err := time.Parse("2006-01-02", day)
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("Bad date '%s': use YYYY-MM-DD", day)})
					return
				}
				day = parsed.Format("2006-01-02")
			}
			items = append(items, soulstore.Item{Text: strings.Join(strings.Fields(text), " "), Date: day})
		}
		sections[key] = items
	}
	if _, err := soulstore.Save(rt.queenDirectory(), cfg, queenID, sections, "owner", "edited by hand", nil); err != nil {
		var soulErr *soulstore.SoulError
		if errors.As(err, &soulErr) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": soulErr.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, rt.soulView(queenID, cfg))
}

// Parity: framework/server/routes_soul.py::handle_decide
func (rt *SoulRoutes) handleDecideSoulProposal(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	cfg, ok := rt.queenConfig(w, queenID)
	if !ok {
		return
	}
	proposal := soulstore.LoadProposal(rt.queenDirectory(), queenID, r.PathValue("proposal_id"))
	if proposal == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "No such proposal"})
		return
	}
	if proposal.Status != "pending" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "This proposal was already " + proposal.Status})
		return
	}
	body, _ := soulJSONBody(r)
	if body == nil {
		body = map[string]any{}
	}
	var accept *[]int
	if raw, present := body["accept"]; present && raw != nil {
		list, isList := raw.([]any)
		if !isList {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'accept' must be a list of change indexes, or null for all"})
			return
		}
		indexes := make([]int, 0, len(list))
		for _, entry := range list {
			index, isInt := pythonInteger(entry)
			if !isInt {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'accept' must be a list of change indexes, or null for all"})
				return
			}
			indexes = append(indexes, index)
		}
		accept = &indexes
	}
	if _, err := soulstore.Decide(rt.queenDirectory(), cfg, queenID, proposal, accept, "owner"); err != nil {
		var soulErr *soulstore.SoulError
		if errors.As(err, &soulErr) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": soulErr.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, rt.soulView(queenID, cfg))
}

// Parity: framework/server/routes_soul.py::handle_rollback
func (rt *SoulRoutes) handleRollbackSoul(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	cfg, ok := rt.queenConfig(w, queenID)
	if !ok {
		return
	}
	body, _ := soulJSONBody(r)
	if body == nil {
		body = map[string]any{}
	}
	version, isInt := pythonInteger(body["version"])
	if !isInt {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'version' must be a version number"})
		return
	}
	if _, err := soulstore.Rollback(rt.queenDirectory(), cfg, queenID, version, "owner"); err != nil {
		var soulErr *soulstore.SoulError
		if errors.As(err, &soulErr) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": soulErr.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, rt.soulView(queenID, cfg))
}

// Parity: framework/server/routes_soul.py::handle_reflect
func (rt *SoulRoutes) handleReflectSoul(w http.ResponseWriter, r *http.Request) {
	queenID := r.PathValue("queen_id")
	cfg, ok := rt.queenConfig(w, queenID)
	if !ok {
		return
	}
	if rt.isReflecting(queenID) || soulstore.IsReflecting(queenID) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "She is reflecting already"})
		return
	}
	if soulstore.PendingProposal(rt.queenDirectory(), queenID) != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "Decide on the waiting proposal first"})
		return
	}
	body, _ := soulJSONBody(r)
	if body == nil {
		body = map[string]any{}
	}
	if rt.deps.Reflect == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "soul reflection is not available"})
		return
	}
	bootstrap := pythonTruthy(body["bootstrap"])
	reflect := rt.deps.Reflect
	if !rt.beginReflecting(queenID) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "She is reflecting already"})
		return
	}
	run := func() {
		defer rt.endReflecting(queenID)
		if err := reflect(queenID, cfg, bootstrap); err != nil {
			log.Printf("soul: %s's reflection failed: %v", queenID, err)
		}
	}
	if rt.deps.RunBackground != nil {
		rt.deps.RunBackground(run)
	} else {
		go run()
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"started": true})
}

func soulJSONBody(r *http.Request) (map[string]any, bool) {
	value, err := decodeJSONValue(r)
	if err != nil {
		return nil, false
	}
	body, ok := value.(map[string]any)
	return body, ok
}

func pythonInteger(value any) (int, bool) {
	switch v := value.(type) {
	case bool:
		return 0, false
	case int:
		return v, true
	case int64:
		return int(v), true
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int(v), true
	}
	return 0, false
}
