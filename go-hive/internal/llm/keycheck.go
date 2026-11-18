package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

const KeyCheckTimeout = 10.0

const HiveLLMEndpoint = "https://llm.open-hive.com"

var openRouterSeparatorTranslation = map[rune]rune{
	'\u2010': '-',
	'\u2011': '-',
	'\u2012': '-',
	'\u2013': '-',
	'\u2014': '-',
	'\u2015': '-',
	'\u2212': '-',
	'\u2044': '/',
	'\u2215': '/',
	'\u29f8': '/',
	'\uff0f': '/',
}

type keyCheckRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

type keyCheckResponse struct {
	StatusCode int
	Body       []byte
}

var keyCheckDo = func(request keyCheckRequest) (*keyCheckResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(KeyCheckTimeout*float64(time.Second)))
	defer cancel()
	var body io.Reader
	if request.Body != nil {
		body = bytes.NewReader(request.Body)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, request.Method, request.URL, body)
	if err != nil {
		return nil, err
	}
	for key, value := range request.Headers {
		httpRequest.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return &keyCheckResponse{StatusCode: response.StatusCode, Body: payload}, nil
}

func jsonBody(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func extractErrorMessage(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		text := strings.TrimSpace(string(body))
		if len(text) > 240 {
			text = text[:240]
		}
		return text
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return ""
	}
	if errorValue, ok := object["error"].(map[string]any); ok {
		if message, ok := errorValue["message"].(string); ok && strings.TrimSpace(message) != "" {
			return strings.TrimSpace(message)
		}
	}
	if errorValue, ok := object["error"].(string); ok && strings.TrimSpace(errorValue) != "" {
		return strings.TrimSpace(errorValue)
	}
	if message, ok := object["message"].(string); ok && strings.TrimSpace(message) != "" {
		return strings.TrimSpace(message)
	}
	return ""
}

func sanitizeOpenRouterModelID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if translated, ok := openRouterSeparatorTranslation[r]; ok {
			r = translated
		}
		if unicode.IsSpace(r) {
			continue
		}
		builder.WriteRune(r)
	}
	normalized := builder.String()
	if strings.HasPrefix(strings.ToLower(normalized), "openrouter/") {
		if index := strings.Index(normalized, "/"); index >= 0 {
			normalized = normalized[index+1:]
		}
	}
	return normalized
}

func normalizeOpenRouterModelID(value string) string {
	return strings.ToLower(sanitizeOpenRouterModelID(value))
}

type openRouterLookup struct {
	preferred map[string]string
	order     []string
}

func (l openRouterLookup) get(key string) (string, bool) {
	value, ok := l.preferred[key]
	return value, ok
}

func extractOpenRouterModelLookup(body []byte) (openRouterLookup, error) {
	lookup := openRouterLookup{preferred: map[string]string{}}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return lookup, err
	}
	object, ok := payload.(map[string]any)
	if !ok {
		return lookup, nil
	}
	data, ok := object["data"].([]any)
	if !ok {
		return lookup, nil
	}
	for _, item := range data {
		details, ok := item.(map[string]any)
		if !ok {
			continue
		}
		candidates := make([]string, 0, 2)
		for _, field := range []string{"id", "canonical_slug"} {
			value, ok := details[field].(string)
			if !ok {
				continue
			}
			sanitized := sanitizeOpenRouterModelID(value)
			if sanitized != "" {
				candidates = append(candidates, sanitized)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		preferredSlug := candidates[len(candidates)-1]
		for _, candidate := range candidates {
			key := strings.ToLower(candidate)
			if _, exists := lookup.preferred[key]; !exists {
				lookup.order = append(lookup.order, key)
			}
			lookup.preferred[key] = preferredSlug
		}
	}
	return lookup, nil
}

func sequenceRatio(a, b string) float64 {
	arunes := []rune(a)
	brunes := []rune(b)
	matches := 0
	for _, block := range matchingBlocks(arunes, brunes) {
		matches += block.size
	}
	total := len(arunes) + len(brunes)
	if total == 0 {
		return 1.0
	}
	return 2.0 * float64(matches) / float64(total)
}

type matchBlock struct {
	a, b, size int
}

func findLongestMatch(a, b []rune, alo, ahi, blo, bhi int) matchBlock {
	best := matchBlock{a: alo, b: blo, size: 0}
	previous := map[int]int{}
	for i := alo; i < ahi; i++ {
		current := map[int]int{}
		for j := blo; j < bhi; j++ {
			if a[i] != b[j] {
				continue
			}
			size := previous[j-1] + 1
			current[j] = size
			if size > best.size {
				best = matchBlock{a: i - size + 1, b: j - size + 1, size: size}
			}
		}
		previous = current
	}
	for best.a > alo && best.b > blo && a[best.a-1] == b[best.b-1] {
		best.a--
		best.b--
		best.size++
	}
	for best.a+best.size < ahi && best.b+best.size < bhi && a[best.a+best.size] == b[best.b+best.size] {
		best.size++
	}
	return best
}

func matchingBlocks(a, b []rune) []matchBlock {
	type rectangle struct{ alo, ahi, blo, bhi int }
	queue := []rectangle{{0, len(a), 0, len(b)}}
	blocks := make([]matchBlock, 0)
	for len(queue) > 0 {
		rect := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		block := findLongestMatch(a, b, rect.alo, rect.ahi, rect.blo, rect.bhi)
		if block.size == 0 {
			continue
		}
		blocks = append(blocks, block)
		if rect.alo < block.a && rect.blo < block.b {
			queue = append(queue, rectangle{rect.alo, block.a, rect.blo, block.b})
		}
		if block.a+block.size < rect.ahi && block.b+block.size < rect.bhi {
			queue = append(queue, rectangle{block.a + block.size, rect.ahi, block.b + block.size, rect.bhi})
		}
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].a != blocks[j].a {
			return blocks[i].a < blocks[j].a
		}
		return blocks[i].b < blocks[j].b
	})
	return blocks
}

func closeMatches(word string, possibilities []string, cutoff float64) []string {
	bestScore := -1.0
	best := ""
	for _, candidate := range possibilities {
		score := sequenceRatio(candidate, word)
		if score < cutoff {
			continue
		}
		if score > bestScore || (score == bestScore && candidate > best) {
			bestScore = score
			best = candidate
		}
	}
	if bestScore < 0 {
		return nil
	}
	return []string{best}
}

func formatOpenRouterModelUnavailable(model string, lookup openRouterLookup) string {
	suggestions := make([]string, 0, 1)
	for _, key := range closeMatches(normalizeOpenRouterModelID(model), lookup.order, 0.6) {
		suggestions = append(suggestions, lookup.preferred[key])
	}
	base := "OpenRouter model is not available for this key/settings: " + model
	if len(suggestions) > 0 {
		return base + ". Closest matches: " + strings.Join(suggestions, ", ")
	}
	return base
}

func checkAnthropic(apiKey string) (map[string]any, error) {
	response, err := keyCheckDo(keyCheckRequest{
		Method: http.MethodPost,
		URL:    "https://api.anthropic.com/v1/messages",
		Headers: map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": "2023-06-01",
			"Content-Type":      "application/json",
		},
		Body: jsonBody(map[string]any{"model": "claude-sonnet-5-5", "max_tokens": 1, "messages": []any{}}),
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 400, 429:
		return checkResult(true, "API key valid"), nil
	case 401:
		return checkResult(false, "Invalid API key"), nil
	case 403:
		return checkResult(false, "API key lacks permissions"), nil
	default:
		return checkResult(false, fmt.Sprintf("Unexpected status %d", response.StatusCode)), nil
	}
}

func checkOpenAICompatible(apiKey, endpoint, name string) (map[string]any, error) {
	response, err := keyCheckDo(keyCheckRequest{
		Method:  http.MethodGet,
		URL:     endpoint,
		Headers: map[string]string{"Authorization": "Bearer " + apiKey},
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 429:
		return checkResult(true, name+" API key valid"), nil
	case 401:
		return checkResult(false, "Invalid "+name+" API key"), nil
	case 403:
		return checkResult(false, name+" API key lacks permissions"), nil
	default:
		return checkResult(false, fmt.Sprintf("%s API returned status %d", name, response.StatusCode)), nil
	}
}

func checkOpenRouter(apiKey, apiBase string) (map[string]any, error) {
	endpoint := strings.TrimRight(apiBase, "/") + "/models"
	response, err := keyCheckDo(keyCheckRequest{
		Method:  http.MethodGet,
		URL:     endpoint,
		Headers: map[string]string{"Authorization": "Bearer " + apiKey},
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 429:
		return checkResult(true, "OpenRouter API key valid"), nil
	case 401:
		return checkResult(false, "Invalid OpenRouter API key"), nil
	case 403:
		return checkResult(false, "OpenRouter API key lacks permissions"), nil
	default:
		return checkResult(false, fmt.Sprintf("OpenRouter API returned status %d", response.StatusCode)), nil
	}
}

func checkOpenRouterModel(apiKey, model, apiBase string) (map[string]any, error) {
	requestedModel := sanitizeOpenRouterModelID(model)
	endpoint := strings.TrimRight(apiBase, "/") + "/models/user"
	response, err := keyCheckDo(keyCheckRequest{
		Method:  http.MethodGet,
		URL:     endpoint,
		Headers: map[string]string{"Authorization": "Bearer " + apiKey},
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode == 200 {
		lookup, err := extractOpenRouterModelLookup(response.Body)
		if err != nil {
			return nil, err
		}
		if matched, ok := lookup.get(normalizeOpenRouterModelID(requestedModel)); ok && matched != "" {
			return map[string]any{
				"valid":   true,
				"message": "OpenRouter model is available: " + matched,
				"model":   matched,
			}, nil
		}
		return checkResult(false, formatOpenRouterModelUnavailable(requestedModel, lookup)), nil
	}
	switch response.StatusCode {
	case 429:
		return checkResult(true, "OpenRouter model check rate-limited; assuming model is reachable"), nil
	case 401:
		return checkResult(false, "Invalid OpenRouter API key"), nil
	case 403:
		return checkResult(false, "OpenRouter API key lacks permissions"), nil
	}
	detail := extractErrorMessage(response.Body)
	if response.StatusCode == 400 || response.StatusCode == 404 || response.StatusCode == 422 {
		base := "OpenRouter model is not available for this key/settings: " + requestedModel
		if detail != "" {
			return checkResult(false, base+". "+detail), nil
		}
		return checkResult(false, base), nil
	}
	suffix := ""
	if detail != "" {
		suffix = ": " + detail
	}
	return checkResult(false, fmt.Sprintf("OpenRouter model check returned status %d%s", response.StatusCode, suffix)), nil
}

func checkMinimax(apiKey, apiBase string) (map[string]any, error) {
	response, err := keyCheckDo(keyCheckRequest{
		Method: http.MethodPost,
		URL:    strings.TrimRight(apiBase, "/") + "/text/chatcompletion_v2",
		Headers: map[string]string{
			"Authorization": "Bearer " + apiKey,
			"Content-Type":  "application/json",
		},
		Body: jsonBody(map[string]any{"model": "MiniMax-M2.5", "messages": []any{}}),
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 400, 422, 429:
		return checkResult(true, "MiniMax API key valid"), nil
	case 401:
		return checkResult(false, "Invalid MiniMax API key"), nil
	case 403:
		return checkResult(false, "MiniMax API key lacks permissions"), nil
	default:
		return checkResult(false, fmt.Sprintf("MiniMax API returned status %d", response.StatusCode)), nil
	}
}

func checkAnthropicCompatible(apiKey, endpoint, name, model string, bearerAuth bool) (map[string]any, error) {
	authHeader := map[string]string{"x-api-key": apiKey}
	if bearerAuth {
		authHeader = map[string]string{"Authorization": "Bearer " + apiKey}
	}
	headers := map[string]string{
		"anthropic-version": "2023-06-01",
		"Content-Type":      "application/json",
	}
	for key, value := range authHeader {
		headers[key] = value
	}
	response, err := keyCheckDo(keyCheckRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: headers,
		Body:    jsonBody(map[string]any{"model": model, "max_tokens": 1, "messages": []any{}}),
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 400, 429:
		return checkResult(true, name+" API key valid"), nil
	case 401:
		return checkResult(false, "Invalid "+name+" API key"), nil
	case 403:
		return checkResult(false, name+" API key lacks permissions"), nil
	default:
		return checkResult(false, fmt.Sprintf("%s API returned status %d", name, response.StatusCode)), nil
	}
}

func checkGemini(apiKey string) (map[string]any, error) {
	response, err := keyCheckDo(keyCheckRequest{
		Method: http.MethodGet,
		URL: "https://generativelanguage.googleapis.com/v1beta/models?" +
			url.Values{"key": {apiKey}}.Encode(),
		Headers: map[string]string{},
	})
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case 200, 429:
		return checkResult(true, "Gemini API key valid"), nil
	case 400, 401, 403:
		return checkResult(false, "Invalid Gemini API key"), nil
	default:
		return checkResult(false, fmt.Sprintf("Gemini API returned status %d", response.StatusCode)), nil
	}
}

func checkResult(valid bool, message string) map[string]any {
	return map[string]any{"valid": valid, "message": message}
}

func CheckKey(provider, apiKey string, apiBase, model *string) (map[string]any, error) {
	base := ""
	if apiBase != nil {
		base = strings.TrimSpace(*apiBase)
	}
	modelValue := ""
	if model != nil {
		modelValue = *model
	}
	if provider == "openrouter" && modelValue != "" {
		if base == "" {
			base = "https://openrouter.ai/api/v1"
		}
		return checkOpenRouterModel(apiKey, modelValue, base)
	}
	if base != "" && provider == "minimax" {
		return checkMinimax(apiKey, base)
	}
	if base != "" && provider == "openrouter" {
		return checkOpenRouter(apiKey, base)
	}
	if base != "" && provider == "kimi" {
		model := modelValue
		if model == "" {
			model = "kimi-k2.6"
		}
		return checkAnthropicCompatible(apiKey, strings.TrimRight(base, "/")+"/v1/messages", "Kimi", model, false)
	}
	if base != "" && provider == "hive" {
		model := modelValue
		if model == "" {
			model = "queen"
		}
		return checkAnthropicCompatible(apiKey, strings.TrimRight(base, "/")+"/v1/messages", "Hive", model, true)
	}
	if base != "" {
		name := "Custom provider"
		if provider == "zai" {
			name = "ZAI"
		}
		return checkOpenAICompatible(apiKey, strings.TrimRight(base, "/")+"/models", name)
	}
	switch provider {
	case "anthropic":
		return checkAnthropic(apiKey)
	case "openai":
		return checkOpenAICompatible(apiKey, "https://api.openai.com/v1/models", "OpenAI")
	case "gemini":
		return checkGemini(apiKey)
	case "groq":
		return checkOpenAICompatible(apiKey, "https://api.groq.com/openai/v1/models", "Groq")
	case "cerebras":
		return checkOpenAICompatible(apiKey, "https://api.cerebras.ai/v1/models", "Cerebras")
	case "openrouter":
		return checkOpenRouter(apiKey, "https://openrouter.ai/api/v1")
	case "deepseek":
		return checkOpenAICompatible(apiKey, "https://api.deepseek.com/v1/models", "DeepSeek")
	case "together":
		return checkOpenAICompatible(apiKey, "https://api.together.xyz/v1/models", "Together AI")
	case "mistral":
		return checkOpenAICompatible(apiKey, "https://api.mistral.ai/v1/models", "Mistral")
	case "xai":
		return checkOpenAICompatible(apiKey, "https://api.x.ai/v1/models", "xAI")
	case "perplexity":
		return checkOpenAICompatible(apiKey, "https://api.perplexity.ai/v1/models", "Perplexity")
	case "minimax":
		return checkMinimax(apiKey, "https://api.minimax.io/v1")
	case "kimi":
		model := modelValue
		if model == "" {
			model = "kimi-k2.6"
		}
		return checkAnthropicCompatible(apiKey, "https://api.kimi.com/coding/v1/messages", "Kimi", model, false)
	case "hive":
		model := modelValue
		if model == "" {
			model = "queen"
		}
		return checkAnthropicCompatible(apiKey, HiveLLMEndpoint+"/v1/messages", "Hive", model, true)
	}
	return checkResult(true, "No health check for "+provider), nil
}

func ValidateProviderKey(provider, apiKey string, apiBase, model *string) map[string]any {
	result, err := CheckKey(strings.ToLower(provider), apiKey, apiBase, model)
	if err != nil {
		return map[string]any{"valid": nil, "message": "Validation error: " + err.Error()}
	}
	return result
}
