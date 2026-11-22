package llm

// Antigravity (Google internal Cloud Code Assist) LLM provider.
//
// Antigravity is Google's unified gateway API that routes requests to Gemini,
// Claude and GPT-OSS models through a single Gemini-style interface. It is NOT
// the public generativelanguage.googleapis.com API.
//
// Authentication uses Google OAuth2. Token refresh is done directly with the
// OAuth client secret — no local proxy required.
//
// Credential sources (checked in order):
//  1. $HIVE_HOME/antigravity-accounts.json (native OAuth implementation)
//  2. Antigravity IDE SQLite state DB (macOS / Linux)
//
// Parity: core/framework/llm/antigravity.py

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// AntigravityTokenURL is Google's OAuth2 token endpoint (_TOKEN_URL).
	AntigravityTokenURL = "https://oauth2.googleapis.com/token"

	// AntigravityDefaultProjectID is upstream's _DEFAULT_PROJECT_ID, used when
	// the account file names no project.
	AntigravityDefaultProjectID = "rising-fact-p41fc"

	// antigravityTokenRefreshBuffer is _TOKEN_REFRESH_BUFFER_SECS: a token
	// expiring inside this window is treated as absent so a refresh runs.
	antigravityTokenRefreshBuffer = 60 * time.Second

	// antigravityRequestTimeout mirrors upstream's urlopen(..., timeout=120)
	// for the non-streaming call. A stream uses the package's longer streaming
	// budget instead, because a Python socket timeout is per read while a Go
	// context timeout covers the whole response.
	antigravityRequestTimeout = 120 * time.Second

	antigravityAccountsFileName = "antigravity-accounts.json"

	antigravityIDEStateKey = "antigravityUnifiedStateSync.oauthToken"
)

// AntigravityEndpoints is the gateway fallback order: daily sandbox →
// autopush sandbox → production (_ENDPOINTS).
var AntigravityEndpoints = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://autopush-cloudcode-pa.sandbox.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

// antigravityBaseHeaders mimic the Antigravity Electron app so the API accepts
// the request. Google deprecates older client versions over time, so this needs
// periodic bumping to match the current desktop release (_BASE_HEADERS).
func antigravityBaseHeaders() map[string]string {
	return map[string]string{
		"User-Agent":        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Antigravity/1.23.2 Chrome/138.0.7204.235 Electron/39.2.3 Safari/537.36",
		"X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
		"Client-Metadata":   `{"ideType":"ANTIGRAVITY","platform":"MACOS","pluginType":"GEMINI"}`,
	}
}

// AntigravityOptions configures an AntigravityProvider. Every field is
// optional; zero values keep production behaviour (the hive home's account
// file, the IDE state DB, the Google endpoints and the shared HTTP client).
type AntigravityOptions struct {
	// Model is the model id; a "provider/" routing prefix is stripped like
	// upstream's __init__ ("openai/gemini-3-flash" → "gemini-3-flash").
	Model string
	// AccountsPath is the credentials JSON file. Empty resolves
	// $HIVE_HOME/antigravity-accounts.json.
	AccountsPath string
	// IDEDBPaths overrides the IDE SQLite candidates. nil keeps the macOS and
	// Linux defaults; an explicit empty slice disables the fallback.
	IDEDBPaths []string
	// ClientID and ClientSecret resolve the OAuth client credentials when a
	// refresh runs (upstream get_antigravity_client_id / _client_secret).
	ClientID     func() string
	ClientSecret func() string
	// TokenURL, Endpoints, HTTPClient and Now override the OAuth endpoint, the
	// gateway list, the transport and the clock (tests).
	TokenURL   string
	Endpoints  []string
	HTTPClient *http.Client
	Now        func() time.Time
}

// AntigravityProvider is the whole-session provider upstream swaps in when
// llm.use_antigravity_subscription is set. It speaks the Gemini-style Cloud
// Code Assist wire format instead of OpenAI chat completions, and refreshes its
// OAuth access token in place.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider
type AntigravityProvider struct {
	Model string

	accountsPath string
	ideDBPaths   []string
	clientID     func() string
	clientSecret func() string
	tokenURL     string
	endpoints    []string
	httpClient   *http.Client
	now          func() time.Time

	mu           sync.Mutex
	accessToken  string
	refreshToken string
	projectID    string
	expiresAt    time.Time
	// thoughtSigs maps a tool_use id to the Gemini thoughtSignature that rode
	// its streamed functionCall part. The next request must echo it back on the
	// same tool call, or Gemini rejects the continuation.
	thoughtSigs map[string]string
}

// NewAntigravityProvider builds the provider from its options. Credential
// loading is best-effort, exactly as upstream's __init__: a missing or
// malformed source leaves the provider without credentials and the first call
// reports the loud "No valid Antigravity credentials" error.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider.__init__
func NewAntigravityProvider(opts AntigravityOptions) *AntigravityProvider {
	model := opts.Model
	if index := strings.Index(model, "/"); index >= 0 {
		model = model[index+1:]
	}

	provider := &AntigravityProvider{
		Model:        model,
		accountsPath: opts.AccountsPath,
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		tokenURL:     opts.TokenURL,
		endpoints:    opts.Endpoints,
		httpClient:   opts.HTTPClient,
		now:          opts.Now,
		thoughtSigs:  map[string]string{},
		projectID:    AntigravityDefaultProjectID,
	}
	if provider.accountsPath == "" {
		provider.accountsPath = defaultAntigravityAccountsPath()
	}
	if opts.IDEDBPaths != nil {
		provider.ideDBPaths = opts.IDEDBPaths
	} else {
		provider.ideDBPaths = defaultAntigravityIDEDBPaths()
	}
	if provider.tokenURL == "" {
		provider.tokenURL = AntigravityTokenURL
	}
	if len(provider.endpoints) == 0 {
		provider.endpoints = AntigravityEndpoints
	}
	if provider.httpClient == nil {
		provider.httpClient = httpCompletionClient
	}
	if provider.now == nil {
		provider.now = time.Now
	}
	provider.initCredentials()
	return provider
}

func (p *AntigravityProvider) ModelName() string { return p.Model }

// HasCredentials reports whether any credential (access or refresh token) is
// available. Upstream's pipeline stage uses it to fall back to LiteLLM when the
// Antigravity login is absent.
func (p *AntigravityProvider) HasCredentials() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accessToken != "" || p.refreshToken != ""
}

func (p *AntigravityProvider) nowTime() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// buildBody renders the Cloud Code Assist envelope. The request shape is
// identical for the streaming and non-streaming endpoints except for the URL.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._build_body
func (p *AntigravityProvider) buildBody(messages []map[string]any, system string, tools []ToolDef, maxTokens int) map[string]any {
	contents := toGeminiContents(messages, p.snapshotThoughtSigs())
	inner := map[string]any{
		"contents":         contents,
		"generationConfig": map[string]any{"maxOutputTokens": maxTokens},
	}
	if system != "" {
		inner["systemInstruction"] = map[string]any{
			"parts": []any{map[string]any{"text": system}},
		}
	}
	if len(tools) > 0 {
		declarations := make([]any, 0, len(tools))
		for _, tool := range tools {
			parameters := any(tool.Parameters)
			if tool.Parameters == nil || len(tool.Parameters) == 0 {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			sanitized, err := sanitizeSchemaForGemini(parameters)
			if err != nil {
				// One tool Gemini can't express (e.g. a multi-type union from an
				// MCP server) must not fail the whole request.
				continue
			}
			declarations = append(declarations, map[string]any{
				"name":        cleanGeminiToolName(tool.Name),
				"description": tool.Description,
				"parameters":  sanitized,
			})
		}
		if len(declarations) > 0 {
			inner["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		}
	}

	p.mu.Lock()
	projectID := p.projectID
	p.mu.Unlock()
	if projectID == "" {
		projectID = AntigravityDefaultProjectID
	}
	return map[string]any{
		"project":     projectID,
		"model":       p.Model,
		"request":     inner,
		"requestType": "agent",
		"userAgent":   "antigravity",
		"requestId":   "agent-" + antigravityUUID(),
	}
}

// AComplete performs one non-streaming Antigravity completion.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider.complete
func (p *AntigravityProvider) AComplete(ctx context.Context, req CompletionRequest) (Completion, error) {
	system := MergedSystemPrompt(req.System, req.SystemDynamicSuffix)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		// Parity: get_aux_max_tokens (the small-utility output budget).
		maxTokens = AuxMaxTokens()
	}
	if req.JSONMode {
		suffix := jsonInstruction
		if system != "" {
			system += suffix
		} else {
			system = strings.TrimSpace(suffix)
		}
	}

	body := p.buildBody(ChatMessagesToMaps(req.Messages), system, req.Tools, maxTokens)
	response, err := p.post(ctx, body, false)
	if err != nil {
		return Completion{}, err
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxCompletionBodyBytes))
	if err != nil {
		return Completion{}, fmt.Errorf("antigravity: read response: %w", err)
	}
	return parseAntigravityCompleteResponse(raw, p.Model)
}

// ACompleteStream streams the SSE form of the same request and returns the
// assembled Completion. Text deltas are forwarded as they arrive; tool calls,
// text end and finish are emitted in the runtime's terminal order.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider.stream
func (p *AntigravityProvider) ACompleteStream(ctx context.Context, req CompletionRequest, emit func(StreamEvent)) (Completion, error) {
	if emit == nil {
		emit = func(StreamEvent) {}
	}
	system := MergedSystemPrompt(req.System, req.SystemDynamicSuffix)
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		// Parity: get_max_tokens' built-in default. The live session passes the
		// configured queen budget explicitly, so this is the fallback only.
		maxTokens = DefaultMaxTokens
	}

	body := p.buildBody(ChatMessagesToMaps(req.Messages), system, req.Tools, maxTokens)
	response, err := p.post(ctx, body, true)
	if err != nil {
		emit(StreamErrorEvent{Error: err.Error()})
		return Completion{}, err
	}
	defer response.Body.Close()

	result, err := p.parseAntigravitySSE(ctx, response.Body, emit)
	if err != nil {
		emit(StreamErrorEvent{Error: err.Error()})
		return Completion{}, err
	}
	emitStreamTail(result, emit)
	return result.completion, nil
}

// post sends one gateway request, falling back through the endpoint list. A
// 401/403 refresh-and-retry happens once per endpoint; 5xx and transport
// failures move on to the next endpoint; any other status raises with the
// upstream body so the caller can see why Google rejected the request.
//
// Parity: core/framework/llm/antigravity.py::AntigravityProvider._post
func (p *AntigravityProvider) post(ctx context.Context, body map[string]any, streaming bool) (*http.Response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("antigravity: encode request: %w", err)
	}
	token, err := p.ensureToken(ctx)
	if err != nil {
		return nil, err
	}

	path := "/v1internal:generateContent"
	if streaming {
		path = "/v1internal:streamGenerateContent?alt=sse"
	}
	headers := antigravityBaseHeaders()
	headers["Authorization"] = "Bearer " + token
	headers["Content-Type"] = "application/json"
	if streaming {
		headers["Accept"] = "text/event-stream"
	}

	timeout := antigravityRequestTimeout
	if streaming {
		timeout = DefaultHTTPTimeout
	}

	var lastErr error
	for _, baseURL := range p.endpoints {
		url := strings.TrimRight(baseURL, "/") + path
		response, err := p.postOnce(ctx, url, headers, encoded, timeout)
		if err != nil {
			lastErr = err
			continue
		}

		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}

		status := response.StatusCode
		if (status == http.StatusUnauthorized || status == http.StatusForbidden) && p.currentRefreshToken() != "" {
			_ = response.Body.Close()
			refreshed, _, refreshErr := p.refreshForRetry(ctx)
			if refreshErr != nil {
				return nil, refreshErr
			}
			if refreshed != "" {
				headers["Authorization"] = "Bearer " + refreshed
				retry, retryErr := p.postOnce(ctx, url, headers, encoded, timeout)
				if retryErr != nil {
					lastErr = retryErr
					continue
				}
				if retry.StatusCode >= 200 && retry.StatusCode < 300 {
					return retry, nil
				}
				lastErr = fmt.Errorf("Antigravity HTTP %d from %s", retry.StatusCode, url)
				_ = retry.Body.Close()
				continue
			}
			lastErr = fmt.Errorf("Antigravity HTTP %d from %s", status, url)
			continue
		}

		if status >= 500 {
			_ = response.Body.Close()
			lastErr = fmt.Errorf("Antigravity HTTP %d from %s", status, url)
			continue
		}

		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		return nil, fmt.Errorf("Antigravity HTTP %d from %s: %s", status, url, strings.TrimSpace(string(detail)))
	}

	if lastErr == nil {
		lastErr = errors.New("no endpoints configured")
	}
	return nil, fmt.Errorf("All Antigravity endpoints failed. Last error: %v", lastErr)
}

func (p *AntigravityProvider) postOnce(ctx context.Context, url string, headers map[string]string, encoded []byte, timeout time.Duration) (*http.Response, error) {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, url, strings.NewReader(string(encoded)))
	if err != nil {
		cancel()
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := p.httpClient.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = &cancelOnClose{ReadCloser: response.Body, cancel: cancel}
	return response, nil
}

// ---------------------------------------------------------------------------
// Request conversion
// ---------------------------------------------------------------------------

// toGeminiContents converts OpenAI-format messages to the Gemini “contents“
// array. Consecutive tool results are batched into one user turn (Gemini
// requires functionResponse parts to answer the preceding model turn), the
// tool_call_id → function name map names each response, and thought signatures
// ride the assistant's functionCall part.
//
// Parity: core/framework/llm/antigravity.py::_to_gemini_contents
func toGeminiContents(messages []map[string]any, thoughtSigs map[string]string) []any {
	tcIDToName := map[string]string{}
	for _, message := range messages {
		role, _ := message["role"].(string)
		if role != "assistant" {
			continue
		}
		calls, _ := asAnySlice(message["tool_calls"])
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			id, _ := call["id"].(string)
			function, _ := call["function"].(map[string]any)
			name, _ := function["name"].(string)
			if id != "" && name != "" {
				tcIDToName[id] = name
			}
		}
	}

	contents := make([]any, 0, len(messages))
	pendingToolParts := make([]any, 0, 2)
	flushToolResults := func() {
		if len(pendingToolParts) == 0 {
			return
		}
		contents = append(contents, map[string]any{"role": "user", "parts": pendingToolParts})
		pendingToolParts = nil
	}

	for _, message := range messages {
		role, _ := message["role"].(string)
		content := message["content"]

		if role == "system" {
			continue // Handled via systemInstruction, not in contents.
		}

		if role == "tool" {
			result := ""
			switch typed := content.(type) {
			case string:
				result = typed
			case nil:
			default:
				result = fmt.Sprint(typed)
			}
			tcID, _ := message["tool_call_id"].(string)
			fnName := tcIDToName[tcID]
			if fnName == "" {
				fnName, _ = message["name"].(string)
			}
			pendingToolParts = append(pendingToolParts, map[string]any{
				"functionResponse": map[string]any{
					"name":     fnName,
					"id":       tcID,
					"response": map[string]any{"content": result},
				},
			})
			continue
		}

		flushToolResults()

		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		parts := make([]any, 0, 2)

		switch typed := content.(type) {
		case string:
			if typed != "" {
				parts = append(parts, map[string]any{"text": typed})
			}
		case nil:
			// Assistant turns that only carry tool_calls have no text.
		default:
			blocks, ok := asAnySlice(typed)
			if ok {
				for _, rawBlock := range blocks {
					block, ok := rawBlock.(map[string]any)
					if !ok {
						continue
					}
					if blockType, _ := block["type"].(string); blockType != "text" {
						// Other block types (image_url etc.) are skipped.
						continue
					}
					text, _ := block["text"].(string)
					if text != "" {
						parts = append(parts, map[string]any{"text": text})
					}
				}
			}
		}

		calls, _ := asAnySlice(message["tool_calls"])
		for _, rawCall := range calls {
			call, ok := rawCall.(map[string]any)
			if !ok {
				continue
			}
			function, _ := call["function"].(map[string]any)
			fnName, _ := function["name"].(string)
			arguments := ""
			if function != nil {
				arguments, _ = function["arguments"].(string)
			}
			var args any
			if arguments == "" || json.Unmarshal([]byte(arguments), &args) != nil {
				args = map[string]any{}
			}
			tcID, present := call["id"].(string)
			if !present {
				tcID = antigravityUUID()
			}
			fcPart := map[string]any{
				"functionCall": map[string]any{
					"name": fnName,
					"args": args,
					"id":   tcID,
				},
			}
			if sig := thoughtSigs[tcID]; sig != "" {
				fcPart["thoughtSignature"] = sig // part-level, not inside functionCall
			}
			parts = append(parts, fcPart)
		}

		if len(parts) > 0 {
			contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
		}
	}
	flushToolResults()

	// Gemini requires the first turn to be a user turn. Drop any leading model
	// messages so the API doesn't reject the request with a 400.
	for len(contents) > 0 {
		first, ok := contents[0].(map[string]any)
		if !ok || first["role"] != "model" {
			break
		}
		contents = contents[1:]
	}
	return contents
}

// cleanGeminiToolName sanitizes a tool name for the function-calling schema:
// "/" and whitespace become "_", a leading non-letter gets "_", and the name is
// capped at 64 characters.
//
// Parity: core/framework/llm/antigravity.py::_clean_tool_name
func cleanGeminiToolName(name string) string {
	var builder strings.Builder
	for _, char := range name {
		if char == '/' || unicode.IsSpace(char) {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(char)
	}
	cleaned := builder.String()
	if cleaned != "" {
		first := []rune(cleaned)[0]
		if !unicode.IsLetter(first) && first != '_' {
			cleaned = "_" + cleaned
		}
	}
	runes := []rune(cleaned)
	if len(runes) > 64 {
		cleaned = string(runes[:64])
	}
	return cleaned
}

// sanitizeSchemaForGemini converts JSON Schema 2020-12 features to the OpenAPI
// 3.0 dialect Gemini accepts. A union “"type": ["string", "null"]“ becomes a
// single type plus “"nullable": true“. Multi-type non-null unions have no
// faithful Gemini equivalent, so they fail loud — the caller leaves that one
// tool out of the request instead of changing its contract silently.
//
// Parity: core/framework/llm/antigravity.py::_sanitize_schema_for_gemini
func sanitizeSchemaForGemini(schema any) (any, error) {
	switch typed := schema.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			sanitized, err := sanitizeSchemaForGemini(item)
			if err != nil {
				return nil, err
			}
			out = append(out, sanitized)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(typed)+1)
		for key, value := range typed {
			out[key] = value
		}

		if rawType, ok := out["type"].([]any); ok {
			nonNull := make([]string, 0, len(rawType))
			hasNull := false
			valid := true
			for _, item := range rawType {
				name, ok := item.(string)
				if !ok {
					valid = false
					break
				}
				if name == "null" {
					hasNull = true
					continue
				}
				nonNull = append(nonNull, name)
			}
			if valid {
				switch {
				case len(nonNull) == 1:
					out["type"] = nonNull[0]
					if hasNull {
						out["nullable"] = true
					}
				case len(nonNull) == 0 && hasNull:
					// Pure null type: fall back to string-nullable.
					out["type"] = "string"
					out["nullable"] = true
				default:
					return nil, fmt.Errorf(
						"Unsupported Gemini schema union: %v. Gemini accepts a single primitive type plus optional 'nullable: true'. Rewrite as anyOf or pick a single type.",
						rawType)
				}
			}
		}

		if properties, ok := out["properties"].(map[string]any); ok {
			sanitized := make(map[string]any, len(properties))
			for key, value := range properties {
				converted, err := sanitizeSchemaForGemini(value)
				if err != nil {
					return nil, err
				}
				sanitized[key] = converted
			}
			out["properties"] = sanitized
		}
		if items, present := out["items"]; present {
			converted, err := sanitizeSchemaForGemini(items)
			if err != nil {
				return nil, err
			}
			out["items"] = converted
		}
		if additional, ok := out["additionalProperties"].(map[string]any); ok {
			converted, err := sanitizeSchemaForGemini(additional)
			if err != nil {
				return nil, err
			}
			out["additionalProperties"] = converted
		}
		for _, combinator := range []string{"anyOf", "oneOf", "allOf"} {
			if value, present := out[combinator]; present {
				converted, err := sanitizeSchemaForGemini(value)
				if err != nil {
					return nil, err
				}
				out[combinator] = converted
			}
		}
		return out, nil
	}
	return schema, nil
}

// ---------------------------------------------------------------------------
// Response parsing
// ---------------------------------------------------------------------------

// mapAntigravityFinishReason maps Gemini's finishReason vocabulary onto the
// finish reasons the runtime consumes.
//
// Parity: core/framework/llm/antigravity.py::_map_finish_reason
func mapAntigravityFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "max_tokens"
	case "OTHER":
		return "tool_use"
	}
	return "stop"
}

// parseAntigravityCompleteResponse parses a non-streaming Cloud Code Assist
// response. The outer envelope is {"response": {...}, "traceId": "..."}; a
// gateway that answers with the bare payload is tolerated the same way.
//
// Parity: core/framework/llm/antigravity.py::_parse_complete_response
func parseAntigravityCompleteResponse(raw []byte, requestedModel string) (Completion, error) {
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Completion{}, fmt.Errorf("antigravity: decode response: %w", err)
	}
	payload := envelope
	if inner, ok := envelope["response"].(map[string]any); ok {
		payload = inner
	}

	textParts := make([]string, 0, 2)
	finishReason := ""
	if candidates, ok := payload["candidates"].([]any); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]any); ok {
			finishReason, _ = candidate["finishReason"].(string)
			if content, ok := candidate["content"].(map[string]any); ok {
				if parts, ok := content["parts"].([]any); ok {
					for _, rawPart := range parts {
						part, ok := rawPart.(map[string]any)
						if !ok {
							continue
						}
						text, ok := part["text"].(string)
						if !ok {
							continue
						}
						if thought, _ := part["thought"].(bool); !thought {
							textParts = append(textParts, text)
						}
					}
				}
			}
		}
	}

	inputTokens, outputTokens := 0, 0
	if usage, ok := payload["usageMetadata"].(map[string]any); ok {
		if value, ok := antigravityInt(usage["promptTokenCount"]); ok {
			inputTokens = value
		}
		if value, ok := antigravityInt(usage["candidatesTokenCount"]); ok {
			outputTokens = value
		}
	}

	model := requestedModel
	if version, present := payload["modelVersion"].(string); present {
		model = version
	}

	completion := Completion{
		Content:      strings.Join(textParts, ""),
		Model:        model,
		StopReason:   mapAntigravityFinishReason(finishReason),
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	completion.CostUSD = ExtractCost(&Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens}, nil, model)
	return completion, nil
}

// parseAntigravitySSE reads the gateway's SSE stream:
//
//	data: {"response": {"candidates": [...], "usageMetadata": {...}}, "traceId": "..."}
//
// Text parts stream as deltas, functionCall parts become tool calls, and the
// part-level thoughtSignature is remembered for the next request's echo.
//
// Parity: core/framework/llm/antigravity.py::_parse_sse_stream
func (p *AntigravityProvider) parseAntigravitySSE(ctx context.Context, reader io.Reader, emit func(StreamEvent)) (streamResult, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSEEventBytes)

	var (
		text         strings.Builder
		inputTokens  int
		outputTokens int
		finishReason string
		toolCalls    []ToolCall
		toolInputs   []map[string]any
	)

	for scanner.Scan() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(dataStr), &frame); err != nil {
			continue // forward-compatible noise / keep-alive text
		}
		// The outer envelope is {"response": {...}, "traceId": "..."}.
		payload := frame
		if inner, ok := frame["response"].(map[string]any); ok {
			payload = inner
		}

		if usage, ok := payload["usageMetadata"].(map[string]any); ok {
			if value, ok := antigravityInt(usage["promptTokenCount"]); ok {
				inputTokens = value
			}
			if value, ok := antigravityInt(usage["candidatesTokenCount"]); ok {
				outputTokens = value
			}
		}

		candidates, _ := payload["candidates"].([]any)
		for _, rawCandidate := range candidates {
			candidate, ok := rawCandidate.(map[string]any)
			if !ok {
				continue
			}
			if reason, ok := candidate["finishReason"].(string); ok && reason != "" {
				finishReason = reason
			}
			content, _ := candidate["content"].(map[string]any)
			parts, _ := content["parts"].([]any)
			for _, rawPart := range parts {
				part, ok := rawPart.(map[string]any)
				if !ok {
					continue
				}
				thought, _ := part["thought"].(bool)
				if textValue, ok := part["text"].(string); ok && !thought {
					text.WriteString(textValue)
					emit(TextDeltaEvent{Content: textValue, Snapshot: text.String()})
					continue
				}
				fc, ok := part["functionCall"].(map[string]any)
				if !ok {
					continue
				}
				toolUseID, _ := fc["id"].(string)
				if toolUseID == "" {
					toolUseID = antigravityUUID()
				}
				if signature, ok := part["thoughtSignature"].(string); ok && signature != "" {
					p.rememberThoughtSignature(toolUseID, signature)
				}
				args := fc["args"]
				if argText, ok := args.(string); ok {
					var parsed any
					if json.Unmarshal([]byte(argText), &parsed) == nil {
						args = parsed
					} else {
						args = map[string]any{}
					}
				}
				name, _ := fc["name"].(string)
				arguments, err := json.Marshal(args)
				if err != nil {
					arguments = []byte("{}")
				}
				toolCalls = append(toolCalls, ToolCall{ID: toolUseID, Name: name, Arguments: string(arguments)})
				input, _ := args.(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				toolInputs = append(toolInputs, input)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return streamResult{}, ctxErr
		}
		return streamResult{}, fmt.Errorf("antigravity: read stream: %w", err)
	}

	completion := Completion{
		Content:      text.String(),
		Model:        p.Model,
		StopReason:   mapAntigravityFinishReason(finishReason),
		ToolCalls:    toolCalls,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	completion.CostUSD = ExtractCost(&Usage{PromptTokens: inputTokens, CompletionTokens: outputTokens}, nil, p.Model)
	return streamResult{completion: completion, toolInputs: toolInputs}, nil
}

// antigravityInt reads a JSON number the way Python's int()/float() access
// would for the gateway's token counters.
func antigravityInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	}
	return 0, false
}

// antigravityUUID returns a random RFC 4122 version 4 UUID. Upstream uses
// uuid.uuid4() for request ids and missing tool-call ids.
func antigravityUUID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1_000_000_000_000)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40 // version 4
	bytes[8] = (bytes[8] & 0x3f) | 0x80 // variant 10
	var out [36]byte
	hex.Encode(out[0:8], bytes[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], bytes[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], bytes[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], bytes[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], bytes[10:16])
	return string(out[:])
}
