package server

// Parity: framework/server/routes_models.py::handle_overview
// Parity: framework/server/routes_models.py::handle_put_slot
// Parity: framework/server/routes_models.py::handle_delete_slot
// Parity: framework/server/routes_models.py::handle_endpoint_models
// Parity: framework/server/routes_models.py::handle_put_endpoint
// Parity: framework/server/routes_models.py::handle_delete_endpoint
//
// The Models page: the four paths upstream registers carry six method
// handlers (GET/PUT/DELETE on the slot and endpoint resources).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
)

var modelRoleSlots = []string{"llm", "worker_llm", "vision_fallback"}

const modelQueenPrefix = "queen:"

// ModelCredentialStore is the slice of the credential store the endpoint
// key writes need.
type ModelCredentialStore interface {
	SaveCredential(*credentials.CredentialObject) error
	DeleteCredential(string) (bool, error)
}

// SessionModelReconfigurer mirrors the upstream provider.reconfigure call:
// the model, key, address and extra_body are replaced in place.
type SessionModelReconfigurer interface {
	Reconfigure(fullModel string, apiKey, apiBase *string, extraBody any)
}

type ModelRouteDeps struct {
	Home hivehome.Home
	LLM  LLMRouteDeps
	// Credentials returns the store endpoint keys are written to. Nil tries
	// the encrypted store when HIVE_CREDENTIAL_KEY is set, otherwise reports
	// the store unavailable (upstream's read-only fallback).
	Credentials func() ModelCredentialStore
	Manager     *SessionManager

	Endpoints       func() map[string]llm.Endpoint
	ListModels      func(name string, fresh bool) ([]map[string]any, error)
	Probe           func(name string) bool
	StoredKey       func(name string) (string, bool)
	EndpointKey     func(name string) string
	Forget          func(name string)
	ModelsCatalogue func() map[string][]llm.ModelEntry
	Subscriptions   func() ([]Subscription, error)
	QueenProfile    func(queenID string) (map[string]any, error)

	// HotSwap switches running sessions to a main-model section. Nil uses the
	// default walker over Manager sessions.
	HotSwap func(fullModel string, apiKey, apiBase *string, extraBody any) int
	// ApplyQueenModel switches one session to a queen's own section. Nil uses
	// the default section-based reconfigure.
	ApplyQueenModel func(session *Session, queenID string, section map[string]any) bool
}

func (deps ModelRouteDeps) withDefaults() ModelRouteDeps {
	if deps.Home.Root == "" {
		if home, err := hivehome.Resolve(); err == nil {
			deps.Home = home
		}
	}
	deps.LLM = deps.LLM.withDefaults(deps.Home)
	if deps.Endpoints == nil {
		deps.Endpoints = llm.Endpoints
	}
	if deps.ListModels == nil {
		deps.ListModels = llm.ListModels
	}
	if deps.Probe == nil {
		deps.Probe = llm.Probe
	}
	if deps.StoredKey == nil {
		deps.StoredKey = llm.StoredKey
	}
	if deps.EndpointKey == nil {
		deps.EndpointKey = llm.KeyFor
	}
	if deps.Forget == nil {
		deps.Forget = llm.Forget
	}
	if deps.ModelsCatalogue == nil {
		deps.ModelsCatalogue = llm.GetModelsCatalogue
	}
	if deps.Subscriptions == nil {
		deps.Subscriptions = BuildSubscriptions
	}
	if deps.QueenProfile == nil {
		queensDir := deps.Home.QueensDir()
		deps.QueenProfile = func(queenID string) (map[string]any, error) {
			return queen.LoadQueenProfile(queensDir, queenID)
		}
	}
	return deps
}

func (deps ModelRouteDeps) credentialStore() ModelCredentialStore {
	if deps.Credentials != nil {
		return deps.Credentials()
	}
	if os.Getenv("HIVE_CREDENTIAL_KEY") == "" {
		return nil
	}
	store, err := credentials.WithEncryptedStorage("")
	if err != nil {
		return nil
	}
	return store
}

func RegisterModelRoutes(mux *http.ServeMux, deps ModelRouteDeps) {
	deps = deps.withDefaults()
	mux.HandleFunc("GET /api/models/overview", func(w http.ResponseWriter, r *http.Request) {
		handleModelsOverview(w, r, deps)
	})
	mux.HandleFunc("PUT /api/models/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		handlePutModelSlot(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/models/slots/{slot}", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteModelSlot(w, r, deps)
	})
	mux.HandleFunc("GET /api/models/endpoints/{name}/models", func(w http.ResponseWriter, r *http.Request) {
		handleModelEndpointModels(w, r, deps)
	})
	mux.HandleFunc("PUT /api/models/endpoints/{name}", func(w http.ResponseWriter, r *http.Request) {
		handlePutModelEndpoint(w, r, deps)
	})
	mux.HandleFunc("DELETE /api/models/endpoints/{name}", func(w http.ResponseWriter, r *http.Request) {
		handleDeleteModelEndpoint(w, r, deps)
	})
}

// Parity: framework/server/routes_models.py::handle_overview
func handleModelsOverview(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	config := HiveConfig(deps.Home)
	endpoints := deps.Endpoints()
	names := orderedEndpointNames(endpoints)

	running := make([]bool, len(names))
	hasKey := make([]bool, len(names))
	var probes sync.WaitGroup
	for index, name := range names {
		probes.Add(1)
		go func(index int, name string) {
			defer probes.Done()
			running[index] = deps.Probe(name)
			_, hasKey[index] = deps.StoredKey(name)
		}(index, name)
	}
	probes.Wait()

	detected := map[string]struct{}{}
	for _, id := range deps.LLM.detectSubscriptions() {
		detected[id] = struct{}{}
	}

	llmSection, _ := config["llm"].(map[string]any)
	judgeSection, _ := config["judge"].(map[string]any)
	queenSections, _ := config["queen_llm"].(map[string]any)

	slots := map[string]any{}
	for _, role := range modelRoleSlots {
		slots[role] = modelSummary(config[role], deps)
	}

	queens := []map[string]any{}
	queenIDs := sortedKeys(queenSections)
	for _, queenID := range queenIDs {
		section, _ := queenSections[queenID].(map[string]any)
		summary := modelSummary(section, deps)
		if summary == nil {
			continue
		}
		entry := map[string]any{"id": queenID, "name": deps.queenName(queenID)}
		for key, value := range summary {
			entry[key] = value
		}
		queens = append(queens, entry)
	}

	catalogue := deps.ModelsCatalogue()
	providers := []map[string]any{}
	for _, providerID := range orderedCatalogueProviders(catalogue) {
		models := catalogue[providerID]
		rows := make([]map[string]any, 0, len(models))
		for _, model := range models {
			label := model.Label
			if label == "" {
				label = model.ID
			}
			supportsVision := true
			if model.SupportsVisionSet {
				supportsVision = model.SupportsVision
			}
			rows = append(rows, map[string]any{
				"id":                 model.ID,
				"label":              label,
				"recommended":        model.Recommended,
				"max_context_tokens": model.MaxContextTokens,
				"supports_vision":    supportsVision,
			})
		}
		key, _ := deps.LLM.resolveAPIKey(providerID)
		connected := key != nil && *key != ""
		providers = append(providers, map[string]any{
			"id":        providerID,
			"connected": connected,
			"models":    rows,
		})
	}

	endpointRows := make([]map[string]any, 0, len(names))
	for index, name := range names {
		endpoint := endpoints[name]
		endpointRows = append(endpointRows, map[string]any{
			"name":     endpoint.Name,
			"api_base": endpoint.APIBase,
			"builtin":  endpoint.Builtin,
			"has_key":  hasKey[index],
			"running":  running[index],
		})
	}

	subscriptions, err := deps.Subscriptions()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody())
		return
	}
	subscriptionRows := make([]map[string]any, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		_, isDetected := detected[subscription.ID]
		subscriptionRows = append(subscriptionRows, map[string]any{
			"id":          subscription.ID,
			"name":        subscription.Name,
			"description": subscription.Description,
			"model":       subscription.DefaultModel,
			"detected":    isDetected,
			"active":      pyTruthy(llmSection[subscription.Flag]),
			"own_risk":    true,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"slots": slots,
		"judge": map[string]any{
			"configured": pyTruthy(judgeSection["api_base"]) && pyTruthy(judgeSection["api_key"]),
			"model":      judgeSection["model"],
			"api_base":   judgeSection["api_base"],
		},
		"queens":        queens,
		"providers":     providers,
		"endpoints":     endpointRows,
		"subscriptions": subscriptionRows,
	})
}

func (deps ModelRouteDeps) queenName(queenID string) string {
	profile, err := deps.QueenProfile(queenID)
	if err == nil && profile != nil {
		if name, ok := profile["name"].(string); ok && name != "" {
			return name
		}
	}
	return queenID
}

// Parity: framework/server/routes_models.py::handle_put_slot
func handlePutModelSlot(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	slot := r.PathValue("slot")
	if problem, status := modelSlotProblem(slot, deps); problem != "" {
		writeJSON(w, status, map[string]any{"error": problem})
		return
	}

	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The body isn't JSON"})
		return
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The body must be a JSON object"})
		return
	}
	source, _ := body["source"].(string)
	model := ""
	if raw := body["model"]; pyTruthy(raw) {
		model = strings.TrimSpace(pyStr(raw))
	}
	if model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "'model' is required"})
		return
	}

	var section map[string]any
	switch source {
	case "catalog":
		provider := strings.TrimSpace(pyStrValue(firstTruthyValue(body["provider"], "")))
		info, found, err := findModelInfo(provider, model)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody())
			return
		}
		if !found {
			providerLabel := provider
			if providerLabel == "" {
				providerLabel = "?"
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": model + " isn't one of " + providerLabel + "'s models in the catalog",
			})
			return
		}
		apiKey, err := deps.LLM.resolveAPIKey(provider)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody())
			return
		}
		if apiKey == nil || *apiKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "There's no API key for " + provider + ": add one on the Models page",
			})
			return
		}
		var apiBase *string
		if strings.EqualFold(provider, "openrouter") {
			base := llm.OpenRouterAPIBase
			apiBase = &base
		}
		check := deps.LLM.Validate(provider, *apiKey, apiBase, &model)
		if valid, ok := check["valid"].(bool); ok && !valid {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": provider + " refused the key: " + validationMessage(check),
			})
			return
		}
		section = map[string]any{
			"provider":           provider,
			"model":              model,
			"max_tokens":         info.MaxTokens,
			"max_context_tokens": info.MaxContextTokens,
		}
		if envVar, found := providerEnvVarFor(provider); found {
			section["api_key_env_var"] = envVar
		}
		if apiBase != nil {
			section["api_base"] = *apiBase
		}
	case "endpoint":
		name := pyStrValue(firstTruthyValue(body["endpoint"], ""))
		endpoint, found := deps.Endpoints()[name]
		if !found {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "There's no endpoint " + modelPyRepr(name),
			})
			return
		}
		models, err := deps.ListModels(name, false)
		if err != nil {
			var endpointErr *llm.EndpointError
			if errors.As(err, &endpointErr) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": endpointErr.Message})
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorBody())
			return
		}
		var listed map[string]any
		for _, candidate := range models {
			if id, ok := candidate["id"].(string); ok && id == model {
				listed = candidate
				break
			}
		}
		if listed == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": name + " has no model " + modelPyRepr(model),
			})
			return
		}
		section = map[string]any{
			"provider": "openai",
			"endpoint": name,
			"model":    model,
			"api_base": endpoint.APIBase,
		}
		if pyTruthy(listed["max_context_tokens"]) {
			section["max_context_tokens"] = listed["max_context_tokens"]
		}
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "'source' must be \"catalog\" or \"endpoint\" (a subscription is switched with PUT /api/config/llm)",
		})
		return
	}

	// Within one provider or endpoint, fields only it understands (a reasoning
	// effort, say) carry over.
	if previous := modelSectionOf(slot, HiveConfig(deps.Home)); previous != nil && pyTruthy(previous["extra_body"]) {
		sameProvider := pyStrValue(previous["provider"]) == pyStrValue(section["provider"])
		sameEndpoint := pyStrValue(previous["endpoint"]) == pyStrValue(section["endpoint"])
		if sameProvider && sameEndpoint {
			section["extra_body"] = previous["extra_body"]
		}
	}
	writeModelSlot(w, deps, slot, section)
}

// Parity: framework/server/routes_models.py::handle_delete_slot
func handleDeleteModelSlot(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	slot := r.PathValue("slot")
	if problem, status := modelSlotProblem(slot, deps); problem != "" {
		writeJSON(w, status, map[string]any{"error": problem})
		return
	}
	if slot == "llm" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "The main model can be changed, not removed",
		})
		return
	}
	writeModelSlot(w, deps, slot, nil)
}

// Parity: framework/server/routes_models.py::_write_slot
func writeModelSlot(w http.ResponseWriter, deps ModelRouteDeps, slot string, section map[string]any) {
	queenID := ""
	isQueen := strings.HasPrefix(slot, modelQueenPrefix)
	if isQueen {
		queenID = strings.TrimPrefix(slot, modelQueenPrefix)
	}

	err := MutateHiveConfig(deps.Home, func(config map[string]any) bool {
		if isQueen {
			sections, _ := config["queen_llm"].(map[string]any)
			if sections == nil {
				sections = map[string]any{}
			}
			if section == nil {
				delete(sections, queenID)
			} else {
				sections[queenID] = section
			}
			if len(sections) > 0 {
				config["queen_llm"] = sections
			} else {
				delete(config, "queen_llm")
			}
			return true
		}
		if section == nil {
			delete(config, slot)
		} else {
			config[slot] = section
		}
		return true
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody())
		return
	}

	switched := 0
	if isQueen {
		switched = applyQueenModel(deps, queenID, section)
	} else if slot == "llm" && section != nil {
		switched = swapMainModel(deps, section)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"slot":              slot,
		"summary":           modelSummary(section, deps),
		"sessions_switched": switched,
	})
}

// Parity: framework/server/routes_models.py::_swap_main_model
func swapMainModel(deps ModelRouteDeps, section map[string]any) int {
	fullModel := sectionFullModel(section)
	apiKey := sectionAPIKey(deps, section)
	var apiBase *string
	if base := modelSectionString(section, "api_base"); base != "" {
		apiBase = &base
	}
	extraBody := section["extra_body"]
	if deps.HotSwap != nil {
		return deps.HotSwap(fullModel, apiKey, apiBase, extraBody)
	}
	return SwapMainModel(deps.Home, deps.Manager, fullModel, apiKey, apiBase, extraBody)
}

// SwapMainModel reconfigures every running session (and its colony worker
// slot) onto a new main-model provider, skipping queens with a model of their
// own. This is the shared walker behind PUT /api/models/slots/llm,
// PUT /api/config/llm and PUT /api/config/llm-sections.
//
// Parity: framework/server/routes_config.py::_hot_swap_sessions
func SwapMainModel(home hivehome.Home, manager *SessionManager, fullModel string, apiKey, apiBase *string, extraBody any) int {
	if manager == nil {
		return 0
	}
	ownQueens := map[string]struct{}{}
	if sections, ok := HiveConfig(home)["queen_llm"].(map[string]any); ok {
		for id, raw := range sections {
			if section, ok := raw.(map[string]any); ok && SectionConfigured(section) {
				ownQueens[id] = struct{}{}
			}
		}
	}
	swapped := 0
	for _, session := range manager.ListSessions() {
		if session == nil {
			continue
		}
		if session.QueenName != "" {
			if _, hasOwn := ownQueens[session.QueenName]; hasOwn {
				continue
			}
		}
		if reconfigureModel(session.LLM, fullModel, apiKey, apiBase, extraBody) {
			swapped++
		}
		if holder, ok := session.Colony.(interface{ WorkerLLM() any }); ok {
			if reconfigureModel(holder.WorkerLLM(), fullModel, apiKey, apiBase, extraBody) {
				swapped++
			}
		}
	}
	return swapped
}

func applyQueenModel(deps ModelRouteDeps, queenID string, section map[string]any) int {
	if deps.Manager == nil {
		return 0
	}
	// Removing a queen's own section puts her running sessions back on the main
	// model, exactly as queen_models.apply does with default_model upstream.
	if section == nil {
		main, ok := HiveConfig(deps.Home)["llm"].(map[string]any)
		if !ok || main == nil {
			return 0
		}
		section = main
	}
	swapped := 0
	for _, session := range deps.Manager.ListSessions() {
		if session == nil || session.QueenName != queenID {
			continue
		}
		if deps.ApplyQueenModel != nil {
			if deps.ApplyQueenModel(session, queenID, section) {
				swapped++
			}
			continue
		}
		if ReconfigureSection(session.LLM, deps.Home, section) {
			swapped++
		}
		// The colony's overseer transport is separate in Go; upstream's
		// colony shares the session provider object when worker_llm is unset,
		// so her new model has to reach it for a colony-bound session to
		// switch at all.
		if holder, ok := session.Colony.(interface{ LLM() any }); ok {
			ReconfigureSection(holder.LLM(), deps.Home, section)
		}
	}
	return swapped
}

func reconfigureModel(holder any, fullModel string, apiKey, apiBase *string, extraBody any) bool {
	reconfigurable, ok := holder.(SessionModelReconfigurer)
	if !ok {
		return false
	}
	reconfigurable.Reconfigure(fullModel, apiKey, apiBase, extraBody)
	return true
}

// Parity: framework/server/routes_models.py::_slot_problem
func modelSlotProblem(slot string, deps ModelRouteDeps) (string, int) {
	for _, role := range modelRoleSlots {
		if slot == role {
			return "", 0
		}
	}
	if strings.HasPrefix(slot, modelQueenPrefix) {
		queenID := strings.TrimPrefix(slot, modelQueenPrefix)
		if _, err := deps.QueenProfile(queenID); err != nil {
			return "There's no queen " + modelPyRepr(queenID), http.StatusNotFound
		}
		return "", 0
	}
	return fmt.Sprintf("There's no slot %s: it is one of %s, or queen:<id>",
		modelPyRepr(slot), strings.Join(modelRoleSlots, ", ")), http.StatusBadRequest
}

// Parity: framework/server/routes_models.py::_section_of
func modelSectionOf(slot string, config map[string]any) map[string]any {
	if strings.HasPrefix(slot, modelQueenPrefix) {
		queenID := strings.TrimPrefix(slot, modelQueenPrefix)
		if sections, ok := config["queen_llm"].(map[string]any); ok {
			section, _ := sections[queenID].(map[string]any)
			return section
		}
		return nil
	}
	section, _ := config[slot].(map[string]any)
	return section
}

// Parity: framework/server/routes_models.py::_summary
func modelSummary(section any, deps ModelRouteDeps) map[string]any {
	block, ok := section.(map[string]any)
	if !ok || !pyTruthy(block["model"]) {
		return nil
	}
	subscriptions, _ := deps.Subscriptions()
	var subscription any
	for _, candidate := range subscriptions {
		if pyTruthy(block[candidate.Flag]) {
			subscription = candidate.ID
			break
		}
	}
	provider := block["provider"]
	source := "catalog"
	switch {
	case pyTruthy(block["endpoint"]):
		source = "endpoint"
	case subscription != nil:
		source = "subscription"
	case pyTruthy(block["api_base"]) && !catalogueHasProvider(deps, provider):
		source = "custom"
	}
	return map[string]any{
		"source":             source,
		"provider":           provider,
		"model":              block["model"],
		"endpoint":           block["endpoint"],
		"subscription":       subscription,
		"api_base":           block["api_base"],
		"max_context_tokens": block["max_context_tokens"],
		"has_extra_body":     pyTruthy(block["extra_body"]),
	}
}

func catalogueHasProvider(deps ModelRouteDeps, provider any) bool {
	name, ok := provider.(string)
	if !ok {
		return false
	}
	_, found := deps.ModelsCatalogue()[name]
	return found
}

// Parity: framework/server/routes_models.py::_slots_using
type modelSlotUse struct {
	Slot    string
	Section map[string]any
}

func modelSlotsUsing(config map[string]any, endpointName string) []modelSlotUse {
	found := []modelSlotUse{}
	for _, role := range modelRoleSlots {
		section, ok := config[role].(map[string]any)
		if ok && pyStrValue(section["endpoint"]) == endpointName {
			found = append(found, modelSlotUse{Slot: role, Section: section})
		}
	}
	if queens, ok := config["queen_llm"].(map[string]any); ok {
		for _, queenID := range sortedKeys(queens) {
			section, ok := queens[queenID].(map[string]any)
			if ok && pyStrValue(section["endpoint"]) == endpointName {
				found = append(found, modelSlotUse{Slot: modelQueenPrefix + queenID, Section: section})
			}
		}
	}
	return found
}

// Parity: framework/server/routes_models.py::handle_endpoint_models
func handleModelEndpointModels(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	name := r.PathValue("name")
	if _, found := deps.Endpoints()[name]; !found {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "There's no endpoint " + modelPyRepr(name),
		})
		return
	}
	models, err := deps.ListModels(name, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		var endpointErr *llm.EndpointError
		if errors.As(err, &endpointErr) {
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": endpointErr.Message})
			return
		}
		writeJSON(w, http.StatusInternalServerError, errorBody())
		return
	}
	if models == nil {
		models = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

// Parity: framework/server/routes_models.py::handle_put_endpoint
func handlePutModelEndpoint(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	name := r.PathValue("name")
	if !llm.ValidName(name) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "An endpoint's name is lowercase letters, digits, '_' and '-'",
		})
		return
	}
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The body isn't JSON"})
		return
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "The body must be a JSON object"})
		return
	}

	existing, hasExisting := deps.Endpoints()[name]
	apiBase := ""
	if raw := body["api_base"]; pyTruthy(raw) {
		apiBase = strings.TrimSpace(pyStr(raw))
	} else if hasExisting {
		apiBase = existing.APIBase
	}
	apiBase = strings.TrimRight(apiBase, "/")
	if !strings.HasPrefix(apiBase, "http://") && !strings.HasPrefix(apiBase, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "'api_base' must be an http:// or https:// address",
		})
		return
	}

	if key, present := body["api_key"]; present {
		problem, err := storeModelEndpointKey(deps, name, key)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody())
			return
		}
		if problem != "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": problem})
			return
		}
	}

	movedMain := false
	err = MutateHiveConfig(deps.Home, func(config map[string]any) bool {
		entries, _ := config["model_endpoints"].(map[string]any)
		if entries == nil {
			entries = map[string]any{}
		}
		if name == llm.Magpie && apiBase == llm.MagpieAPIBase {
			delete(entries, name)
		} else {
			entries[name] = map[string]any{"api_base": apiBase}
		}
		if len(entries) > 0 {
			config["model_endpoints"] = entries
		} else {
			delete(config, "model_endpoints")
		}
		for _, use := range modelSlotsUsing(config, name) {
			if modelSectionString(use.Section, "api_base") != apiBase {
				if use.Slot == "llm" {
					movedMain = true
				}
			}
			use.Section["api_base"] = apiBase
		}
		return true
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody())
		return
	}
	deps.Forget(name)

	if movedMain {
		if section, ok := HiveConfig(deps.Home)["llm"].(map[string]any); ok && section != nil {
			swapMainModel(deps, section)
		}
	}

	_, keyPresent := deps.StoredKey(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":     name,
		"api_base": apiBase,
		"builtin":  name == llm.Magpie,
		"has_key":  keyPresent,
	})
}

// Parity: framework/server/routes_models.py::handle_delete_endpoint
func handleDeleteModelEndpoint(w http.ResponseWriter, r *http.Request, deps ModelRouteDeps) {
	name := r.PathValue("name")
	if _, found := deps.Endpoints()[name]; !found {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": "There's no endpoint " + modelPyRepr(name),
		})
		return
	}
	config := HiveConfig(deps.Home)
	users := []string{}
	for _, use := range modelSlotsUsing(config, name) {
		users = append(users, use.Slot)
	}
	if len(users) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": name + " serves " + strings.Join(users, ", ") + ": pick another model there first",
		})
		return
	}
	err := MutateHiveConfig(deps.Home, func(config map[string]any) bool {
		entries, _ := config["model_endpoints"].(map[string]any)
		if entries != nil {
			delete(entries, name)
			if len(entries) > 0 {
				config["model_endpoints"] = entries
			} else {
				delete(config, "model_endpoints")
			}
		}
		return true
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorBody())
		return
	}
	_, _ = storeModelEndpointKey(deps, name, nil)
	deps.Forget(name)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// Parity: framework/server/routes_models.py::_store_key
func storeModelEndpointKey(deps ModelRouteDeps, name string, key any) (string, error) {
	store := deps.credentialStore()
	if store == nil {
		return "The credential store isn't available, so the key can't be kept", nil
	}
	credentialID := llm.CredentialID(name)
	if text, ok := key.(string); ok && strings.TrimSpace(text) != "" {
		credential := credentials.NewCredentialObject(credentialID)
		credential.SetKey("api_key", strings.TrimSpace(text), nil, nil)
		if err := store.SaveCredential(credential); err != nil {
			return "", err
		}
	} else {
		if _, err := store.DeleteCredential(credentialID); err != nil {
			return "", err
		}
	}
	deps.Forget(name)
	return "", nil
}

// sectionFullModel mirrors framework.config.section_model.
func sectionFullModel(section map[string]any) string {
	provider := pyStrValue(section["provider"])
	model := strings.TrimSpace(pyStrValue(section["model"]))
	if strings.EqualFold(provider, "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
		model = model[len("openrouter/"):]
	}
	return provider + "/" + model
}

// sectionAPIKey resolves the key a section's provider would actually use.
func sectionAPIKey(deps ModelRouteDeps, section map[string]any) *string {
	if name, ok := section["endpoint"].(string); ok && name != "" {
		key := deps.EndpointKey(name)
		return &key
	}
	if key, ok := section["api_key"].(string); ok && key != "" {
		return &key
	}
	if envVar, ok := section["api_key_env_var"].(string); ok && envVar != "" {
		if value, present := os.LookupEnv(envVar); present {
			return &value
		}
	}
	if provider, ok := section["provider"].(string); ok && provider != "" {
		if key, err := deps.LLM.resolveAPIKey(provider); err == nil && key != nil && *key != "" {
			return key
		}
	}
	return nil
}

func modelSectionString(section map[string]any, key string) string {
	raw, present := section[key]
	if !present || !pyTruthy(raw) {
		return ""
	}
	return strings.TrimSpace(pyStr(raw))
}

func orderedEndpointNames(endpoints map[string]llm.Endpoint) []string {
	names := make([]string, 0, len(endpoints))
	for name := range endpoints {
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		if names[i] == llm.Magpie {
			return true
		}
		if names[j] == llm.Magpie {
			return false
		}
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names
}

func orderedCatalogueProviders(catalogue map[string][]llm.ModelEntry) []string {
	order := []string{}
	seen := map[string]struct{}{}
	if loaded, err := llm.LoadModelCatalog(); err == nil {
		for _, provider := range loaded.ProviderOrder {
			if _, present := catalogue[provider]; present {
				order = append(order, provider)
				seen[provider] = struct{}{}
			}
		}
	}
	extra := []string{}
	for provider := range catalogue {
		if _, present := seen[provider]; !present {
			extra = append(extra, provider)
		}
	}
	sort.Strings(extra)
	return append(order, extra...)
}

func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// modelPyRepr mirrors Python repr() for the simple strings these messages
// carry: single-quoted unless the value itself contains a single quote.
func modelPyRepr(value string) string {
	if strings.Contains(value, "'") && !strings.Contains(value, "\"") {
		return "\"" + value + "\""
	}
	return "'" + value + "'"
}
