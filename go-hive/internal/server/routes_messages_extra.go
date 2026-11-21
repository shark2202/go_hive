package server

// Parity: framework/server/routes_messages.py

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/queen"
)

const (
	defaultQueenID    = "queen_growth"
	defaultColonyName = "new_colony"
)

// colonyNamePatternExtra is the slug validator owned by the message routes;
// named with the Extra suffix so it does not collide with the colony-name
// error message constant declared in routes_colonies.go.
var colonyNamePatternExtra = regexp.MustCompile(`^[a-z0-9_]+$`)

var colonyNameInvalidRuns = regexp.MustCompile(`[^a-z0-9]+`)

var queenSelectorDescriptions = map[string]string{
	"queen_technology":          "Technical architecture, software engineering, infrastructure, DevOps, system design",
	"queen_growth":              "User acquisition, retention, growth experiments, PLG, marketing funnels, analytics, brand and demand",
	"queen_market_research":     "Market sizing (TAM/SAM), competitive analysis, customer interviews, surveys, trend forecasting, positioning research",
	"queen_content_creation":    "Content strategy, copywriting, short-form video, social media, brand storytelling, editorial calendars",
	"queen_lead_gen":            "Lead generation, offer design, paid acquisition, list building, inbound/demand capture, ICP, lead qualification",
	"queen_outbound":            "Outbound prospecting on LinkedIn/Twitter/email, cold sequences, ICP definition, deliverability, reply-rate optimization",
	"queen_product_strategy":    "Product vision, roadmapping, user research, feature prioritization, go-to-market",
	"queen_finance_fundraising": "Financial modeling, fundraising, investor relations, cap tables, unit economics, budgeting",
	"queen_legal":               "Contracts, IP, compliance, corporate governance, employment law, regulatory matters",
	"queen_brand_design":        "Brand identity, visual design, UX, design systems, creative direction, messaging",
	"queen_sales":               "The CRM itself — pipeline stages, fields, the shape of the sales process — plus pipeline and forecast ownership, deal qualification, enterprise/mid-market selling, sales coaching, comp design, channel and partnership sales",
	"queen_talent":              "Hiring, recruiting, team building, culture, compensation, organizational design",
	"queen_operations":          "Founder coaching, strategic decisions, leadership challenges, company growth, pivots",
}

type QueenSelection struct {
	QueenID string
	Reason  string
}

type ColonySessionSelection struct {
	QueenID    string
	ColonyName string
	Reason     string
}

type MessageRouteDeps struct {
	Home         hivehome.Home
	BuildLLM     func() llm.Completer
	AuxMaxTokens func() int
}

func RegisterMessageRoutes(mux *http.ServeMux, deps MessageRouteDeps) {
	mux.HandleFunc("POST /api/messages/classify", func(w http.ResponseWriter, r *http.Request) {
		handleClassifyMessage(w, r, deps)
	})
	mux.HandleFunc("POST /api/messages/classify-colony", func(w http.ResponseWriter, r *http.Request) {
		handleClassifyColony(w, r, deps)
	})
}

func readOptionalJSONObject(r *http.Request) (map[string]any, error) {
	body := map[string]any{}
	if r.Body == nil {
		return body, nil
	}
	data, err := readLimitedBody(r, maxJSONBodyBytes)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return body, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%T object has no attribute 'get'", parsed)
	}
	return object, nil
}

func messageText(body map[string]any) (string, bool) {
	raw, present := body["message"]
	if !present {
		return "", false
	}
	text, ok := raw.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func handleClassifyMessage(w http.ResponseWriter, r *http.Request, deps MessageRouteDeps) {
	body, err := readOptionalJSONObject(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	message, ok := messageText(body)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}
	queen.EnsureDefaultQueens()
	provider, err := buildMessageLLM(deps)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	selection := SelectQueenWithReason(r.Context(), message, provider, deps.auxMaxTokens())
	writeJSON(w, http.StatusOK, map[string]any{"queen_id": selection.QueenID})
}

func handleClassifyColony(w http.ResponseWriter, r *http.Request, deps MessageRouteDeps) {
	body, err := readOptionalJSONObject(r)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	message, ok := messageText(body)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message is required"})
		return
	}

	var activeQueenIDs []string
	if raw, present := body["active_queen_ids"]; present {
		if items, isList := raw.([]any); isList {
			activeQueenIDs = make([]string, 0, len(items))
			for _, item := range items {
				if text, isString := item.(string); isString {
					activeQueenIDs = append(activeQueenIDs, text)
				}
			}
		}
	}

	queen.EnsureDefaultQueens()
	provider, err := buildMessageLLM(deps)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	selection := SelectQueenAndColony(r.Context(), message, provider, activeQueenIDs, deps.Home.QueensDir(), deps.auxMaxTokens())
	writeJSON(w, http.StatusOK, map[string]any{
		"queen_id":    selection.QueenID,
		"colony_name": selection.ColonyName,
		"reason":      selection.Reason,
	})
}

func buildMessageLLM(deps MessageRouteDeps) (llm.Completer, error) {
	if deps.BuildLLM == nil {
		return nil, fmt.Errorf("llm provider is not configured")
	}
	provider := deps.BuildLLM()
	if provider == nil {
		return nil, fmt.Errorf("llm provider is not configured")
	}
	return provider, nil
}

func (deps MessageRouteDeps) auxMaxTokens() int {
	if deps.AuxMaxTokens != nil {
		return llm.ResolveAuxMaxTokens(deps.AuxMaxTokens())
	}
	return llm.AuxMaxTokens()
}

// DefaultQueenID is the static fallback queen (_DEFAULT_QUEEN_ID upstream): the
// identity an empty initial message routes to, and the answer whenever the
// queen classifier fails. The create path binds it so a cold session carries
// the same queen as its Python counterpart.
func DefaultQueenID() string { return defaultQueenID }

func defaultQueenIDs() []string {
	catalog := queen.DefaultQueens()
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func queenSelectorSystemPrompt(allowedIDs []string) string {
	catalog := queen.DefaultQueens()
	lines := make([]string, 0, len(allowedIDs))
	for _, id := range allowedIDs {
		description := queenSelectorDescriptions[id]
		if description == "" {
			if profile, ok := catalog[id]; ok {
				if title, ok := profile["title"].(string); ok {
					description = title
				}
			}
		}
		if description == "" {
			description = id
		}
		lines = append(lines, "- "+id+": "+description)
	}
	return "You are a routing classifier acting as the CEO of the company.\n\n" +
		"Treat the incoming request as something you personally want to accomplish.\n" +
		"Select the single best-matching queen identity from the list below to take on that goal.\n\n" +
		"Queens:\n" + strings.Join(lines, "\n") + "\n\n" +
		"Reply with ONLY a valid JSON object — no markdown, no prose:\n" +
		`{"reason": "<reason and thinking of selecting who will take the request>", "queen_id": "<one of the IDs above>"}` + "\n\n" +
		"Rules:\n" +
		"- Think about the request from the CEO's perspective: this is your goal and you need the best queen to own it.\n" +
		"- Pick the queen whose domain most directly applies to the goal.\n" +
		"- If the request spans multiple domains, pick the one most central to the ask.\n" +
		"- The reason must briefly explain why that queen should take this request.\n"
}

func colonySelectorSystemPrompt(allowedIDs []string) string {
	replaced := strings.Replace(
		queenSelectorSystemPrompt(allowedIDs),
		"Reply with ONLY a valid JSON object — no markdown, no prose:\n"+
			`{"reason": "<reason and thinking of selecting who will take the request>", "queen_id": "<one of the IDs above>"}`,
		"You are also spinning up a dedicated colony (a project workspace) to own this goal.\n"+
			"Give the colony a short, human-friendly name that captures the goal.\n\n"+
			"Reply with ONLY a valid JSON object — no markdown, no prose:\n"+
			`{"reason": "<why this queen and colony name>", "queen_id": "<one of the IDs above>", "colony_name": "<lowercase_snake_case slug, e.g. fintech_competitor_research>"}`,
		1,
	)
	return replaced + "\n- colony_name must be lowercase letters, digits, and underscores only " +
		"(e.g. 'morning_hn_digest'), max 64 characters, derived from the goal."
}

func selectorFailureReason(err error) string {
	return "Selection failed because the classifier errored: " + err.Error()
}

func selectorJSONObject(raw string) (map[string]any, error) {
	jsonString := raw
	if !strings.HasPrefix(raw, "{") {
		start := strings.Index(raw, "{")
		end := strings.LastIndex(raw, "}")
		if start != -1 && end != -1 && end > start {
			jsonString = raw[start : end+1]
		}
	}
	var parsed any
	if err := json.Unmarshal([]byte(jsonString), &parsed); err != nil {
		return nil, err
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("selector returned a non-object JSON value")
	}
	return object, nil
}

func selectorString(object map[string]any, key string) string {
	value, present := object[key]
	if !present {
		return ""
	}
	return strings.TrimSpace(pyStr(value))
}

func SelectQueenWithReason(ctx context.Context, userMessage string, provider llm.Completer, maxTokens int) QueenSelection {
	if strings.TrimSpace(userMessage) == "" {
		return QueenSelection{
			QueenID: defaultQueenID,
			Reason:  "User message was empty, so routing defaulted to queen_technology.",
		}
	}

	response, err := provider.AComplete(ctx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: userMessage}},
		System:    queenSelectorSystemPrompt(defaultQueenIDs()),
		MaxTokens: maxTokens,
		JSONMode:  true,
	})
	if err != nil {
		return QueenSelection{QueenID: defaultQueenID, Reason: selectorFailureReason(err)}
	}

	parsed, err := selectorJSONObject(strings.TrimSpace(response.Content))
	if err != nil {
		return QueenSelection{
			QueenID: defaultQueenID,
			Reason:  "Selection failed because the classifier returned invalid JSON: " + err.Error(),
		}
	}

	queenID := selectorString(parsed, "queen_id")
	reason := selectorString(parsed, "reason")
	catalog := queen.DefaultQueens()
	if _, known := catalog[queenID]; !known {
		fallbackReason := reason
		if fallbackReason == "" {
			fallbackReason = "Selection failed because the classifier returned unknown queen_id " + pyReprString(queenID) + "."
		}
		return QueenSelection{QueenID: defaultQueenID, Reason: fallbackReason}
	}
	if reason == "" {
		reason = "Classifier selected " + queenID + " but did not provide an explicit reason."
	}
	return QueenSelection{QueenID: queenID, Reason: reason}
}

func allowedQueenIDs(activeQueenIDs []string, queensDir string) []string {
	removed := queen.RemovedQueenIDs(queensDir)
	catalog := make([]string, 0, len(queenSelectorDescriptions))
	for _, id := range defaultQueenIDs() {
		if !removed[id] {
			catalog = append(catalog, id)
		}
	}
	if len(catalog) == 0 {
		catalog = defaultQueenIDs()
	}
	allowed := make([]string, 0, len(activeQueenIDs))
	for _, id := range activeQueenIDs {
		for _, candidate := range catalog {
			if id == candidate {
				allowed = append(allowed, id)
				break
			}
		}
	}
	if len(allowed) == 0 {
		return catalog
	}
	return allowed
}

func fallbackQueenID(allowedIDs []string) string {
	for _, id := range allowedIDs {
		if id == defaultQueenID {
			return defaultQueenID
		}
	}
	if len(allowedIDs) == 0 {
		return defaultQueenID
	}
	return allowedIDs[0]
}

func sanitizeColonyName(raw string) string {
	slug := strings.ToLower(strings.TrimSpace(raw))
	slug = colonyNameInvalidRuns.ReplaceAllString(slug, "_")
	slug = strings.Trim(slug, "_")
	if len(slug) > 64 {
		slug = slug[:64]
	}
	slug = strings.Trim(slug, "_")
	if slug == "" || !colonyNamePatternExtra.MatchString(slug) {
		return defaultColonyName
	}
	return slug
}

func SelectQueenAndColony(ctx context.Context, userMessage string, provider llm.Completer, activeQueenIDs []string, queensDir string, maxTokens int) ColonySessionSelection {
	allowedIDs := allowedQueenIDs(activeQueenIDs, queensDir)
	fallbackID := fallbackQueenID(allowedIDs)

	if strings.TrimSpace(userMessage) == "" {
		return ColonySessionSelection{
			QueenID:    fallbackID,
			ColonyName: defaultColonyName,
			Reason:     "User message was empty, so routing defaulted to the default queen.",
		}
	}

	response, err := provider.AComplete(ctx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: userMessage}},
		System:    colonySelectorSystemPrompt(allowedIDs),
		MaxTokens: maxTokens,
		JSONMode:  true,
	})
	if err != nil {
		return ColonySessionSelection{
			QueenID:    fallbackID,
			ColonyName: defaultColonyName,
			Reason:     selectorFailureReason(err),
		}
	}

	parsed, err := selectorJSONObject(strings.TrimSpace(response.Content))
	if err != nil {
		return ColonySessionSelection{
			QueenID:    fallbackID,
			ColonyName: defaultColonyName,
			Reason:     "Selection failed because the classifier returned invalid JSON: " + err.Error(),
		}
	}

	queenID := selectorString(parsed, "queen_id")
	reason := selectorString(parsed, "reason")
	colonyName := sanitizeColonyName(selectorString(parsed, "colony_name"))

	allowed := false
	for _, candidate := range allowedIDs {
		if queenID == candidate {
			allowed = true
			break
		}
	}
	if !allowed {
		fallbackReason := reason
		if fallbackReason == "" {
			fallbackReason = "Selection failed because the classifier returned disallowed queen_id " + pyReprString(queenID) + "."
		}
		return ColonySessionSelection{
			QueenID:    fallbackID,
			ColonyName: colonyName,
			Reason:     fallbackReason,
		}
	}
	if reason == "" {
		reason = "Classifier selected " + queenID + " but did not provide an explicit reason."
	}
	return ColonySessionSelection{QueenID: queenID, ColonyName: colonyName, Reason: reason}
}
