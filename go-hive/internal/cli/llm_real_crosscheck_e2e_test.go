package cli

// Real-LLM crosscheck, Go side. Opt-in: HIVE_REAL_LLM_E2E=1 plus a HIVE_HOME
// whose configuration.json llm slot points at the crosscheck recording proxy
// (go_hive/scripts/llm_real_crosscheck.py), which forwards to the real
// upstream and records the exact JSON payloads.
//
// The probe runs the real Go provider path (resolveChatProvider →
// llm.Provider.ACompleteStream, the same transport chat sessions stream
// through) for the scenarios driven by the orchestrator:
//
//	chat_minimal   N × "Reply with exactly: pong" (minimal system prompt)
//	frozen_system  1 × frozen queen system prompt (sha recheck)
//	tool_time      1 × get_current_time with the real model-visible tool def,
//	               then the follow-up turn after the tool result
//	unauthorized   1 × wrong API key → real upstream 401
//
// results JSON mirrors the Python runner's schema so the orchestrator can diff
// both sides field by field. Parity-exempt: this is an integration probe
// against a live model; there is no upstream unit test to reference.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tools/integrations"
)

const (
	crosscheckUserPongPrompt = "Reply with exactly: pong"
	crosscheckToolPrompt     = "What time is it? Use get_current_time"
	crosscheckMinimalSystem  = "You are a crosscheck probe. Follow the user's instruction exactly and reply with nothing else."
	crosscheckBadKey         = "sk-llm-crosscheck-invalid-000"
	crosscheckCallTimeout    = 5 * time.Minute
)

type crosscheckUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens"`
}

type crosscheckToolCall struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	ArgumentsRaw    string         `json:"arguments_raw"`
	ArgumentsParsed map[string]any `json:"arguments_parsed"`
}

type crosscheckError struct {
	Error          string `json:"error"`
	Recoverable    bool   `json:"recoverable"`
	ErrorType      any    `json:"error_type"`
	UpstreamStatus any    `json:"upstream_status"`
}

type crosscheckCall struct {
	Rep              int                  `json:"rep"`
	Events           []map[string]any     `json:"events"`
	EventTypes       []string             `json:"event_types"`
	FinalText        string               `json:"final_text"`
	FinishReason     string               `json:"finish_reason"`
	Usage            *crosscheckUsage     `json:"usage"`
	ToolCalls        []crosscheckToolCall `json:"tool_calls"`
	ReasoningContent string               `json:"reasoning_content"`
	Error            *crosscheckError     `json:"error"`
	WallMS           float64              `json:"wall_ms"`
}

// Parity-exempt: integration probe against a live model; the Python suite
// mocks the provider, so there is no upstream test to reference.
func TestLLMRealCrosscheckE2E(t *testing.T) {
	if os.Getenv("HIVE_REAL_LLM_E2E") != "1" {
		t.Skip("real LLM crosscheck is opt-in: set HIVE_REAL_LLM_E2E=1")
	}
	outPath := os.Getenv("HIVE_LLM_CROSSCHECK_OUT")
	if outPath == "" {
		t.Fatal("HIVE_LLM_CROSSCHECK_OUT is required")
	}
	scenarios := splitNonEmpty(os.Getenv("HIVE_LLM_CROSSCHECK_SCENARIOS"))
	if len(scenarios) == 0 {
		scenarios = []string{"chat_minimal", "frozen_system", "tool_time", "unauthorized"}
	}
	reps := 3
	if raw := os.Getenv("HIVE_LLM_CROSSCHECK_REPS"); raw != "" {
		if _, err := fmt.Sscanf(raw, "%d", &reps); err != nil || reps < 1 {
			t.Fatalf("HIVE_LLM_CROSSCHECK_REPS=%q is not a positive integer", raw)
		}
	}

	home, err := hivehome.Resolve()
	if err != nil {
		t.Fatalf("resolve HIVE_HOME: %v", err)
	}
	provider, err := resolveChatProvider(home, "")
	if err != nil {
		t.Fatalf("resolve real LLM provider: %v", err)
	}
	if provider.ExtraKwargs == nil {
		provider.ExtraKwargs = map[string]any{}
	}
	// Same probe parameter on both sides; the agent path never sets one.
	provider.ExtraKwargs["temperature"] = 0.0

	result := map[string]any{
		"side": "go",
		"provider": map[string]any{
			"model":    provider.Model,
			"api_base": provider.APIBase,
		},
		"tenant": map[string]any{
			"hive_home": home.Root,
			"go":        runtime.Version(),
		},
		"scenarios": map[string]any{},
	}

	scenariosOut := result["scenarios"].(map[string]any)
	for _, scenario := range scenarios {
		t.Logf("scenario %s", scenario)
		switch scenario {
		case "chat_minimal":
			calls := make([]crosscheckCall, 0, reps)
			for rep := 0; rep < reps; rep++ {
				call := runCrosscheckStream(t, provider, crosscheckMinimalSystem, crosscheckUserPongPrompt, nil)
				call.Rep = rep
				calls = append(calls, call)
			}
			scenariosOut[scenario] = map[string]any{"reps": calls}
		case "frozen_system":
			systemPath := os.Getenv("HIVE_LLM_CROSSCHECK_FROZEN_SYSTEM")
			if systemPath == "" {
				systemPath = filepath.Join("..", "..", "..", "..", "go_hive", "docs", "e2e-assets", "prompt-catalog", "system-prompt.python.txt")
			}
			raw, err := os.ReadFile(systemPath)
			if err != nil {
				t.Fatalf("read frozen system prompt %s: %v", systemPath, err)
			}
			call := runCrosscheckStream(t, provider, string(raw), crosscheckUserPongPrompt, nil)
			sum := sha256.Sum256(raw)
			scenariosOut[scenario] = map[string]any{
				"system_source": systemPath,
				"system_sha256": hex.EncodeToString(sum[:]),
				"system_chars":  len(raw),
				"reps":          []crosscheckCall{call},
			}
		case "tool_time":
			runCrosscheckToolScenario(t, provider, home, scenariosOut)
		case "unauthorized":
			saved := provider.APIKey
			provider.APIKey = crosscheckBadKey
			call := runCrosscheckStream(t, provider, crosscheckMinimalSystem, crosscheckUserPongPrompt, nil)
			provider.APIKey = saved
			scenariosOut[scenario] = map[string]any{"reps": []crosscheckCall{call}}
		default:
			t.Fatalf("unknown scenario %q", scenario)
		}
	}

	encoded, err := json.MarshalIndent(result, "", " ")
	if err != nil {
		t.Fatalf("marshal results: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		t.Fatalf("create results dir: %v", err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
	t.Logf("wrote %s", outPath)
}

func splitNonEmpty(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// runCrosscheckStream performs one streaming call and folds the emitted
// framework events into the crosscheck result shape.
func runCrosscheckStream(
	t *testing.T, provider *llm.Provider, system, user string, tools []llm.ToolDef,
) crosscheckCall {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), crosscheckCallTimeout)
	defer cancel()
	events := []map[string]any{}
	eventTypes := []string{}
	started := time.Now()
	completion, err := provider.ACompleteStream(ctx, llm.CompletionRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: user}},
		System:   system,
		Tools:    tools,
	}, func(event llm.StreamEvent) {
		mapped, mapErr := llm.StreamEventToMap(event)
		if mapErr != nil {
			t.Errorf("StreamEventToMap: %v", mapErr)
			return
		}
		events = append(events, mapped)
		eventTypes = append(eventTypes, event.StreamEventType())
	})
	call := crosscheckCall{
		Events:       events,
		EventTypes:   eventTypes,
		FinalText:    completion.Content,
		FinishReason: completion.StopReason,
		Usage: &crosscheckUsage{
			PromptTokens:     completion.InputTokens,
			CompletionTokens: completion.OutputTokens,
			TotalTokens:      completion.InputTokens + completion.OutputTokens,
			CachedTokens:     completion.CachedTokens,
		},
		ToolCalls:        []crosscheckToolCall{},
		ReasoningContent: completion.ReasoningContent,
		WallMS:           float64(time.Since(started).Milliseconds()),
	}
	for _, callEvent := range events {
		if callEvent["type"] == "finish" {
			if reasoning, ok := callEvent["reasoning_content"].(string); ok {
				call.ReasoningContent = reasoning
			}
		}
	}
	for _, toolCall := range completion.ToolCalls {
		parsed, parseErr := llm.ParseToolCallArguments(toolCall.Arguments, toolCall.Name)
		if parseErr != nil {
			parsed = map[string]any{}
		}
		call.ToolCalls = append(call.ToolCalls, crosscheckToolCall{
			ID:              toolCall.ID,
			Name:            toolCall.Name,
			ArgumentsRaw:    toolCall.Arguments,
			ArgumentsParsed: parsed,
		})
	}
	if err != nil {
		call.Error = crosscheckErrorFrom(err)
		call.Usage = nil
	}
	return call
}

func crosscheckErrorFrom(err error) *crosscheckError {
	record := &crosscheckError{Error: err.Error()}
	var providerErr *llm.ProviderError
	if errors.As(err, &providerErr) {
		record.UpstreamStatus = providerErr.StatusCode
	}
	return record
}

// runCrosscheckToolScenario asks the model to use get_current_time with the
// real model-visible definition, executes the tool with the registered local
// implementation, and sends the follow-up turn.
func runCrosscheckToolScenario(
	t *testing.T, provider *llm.Provider, home hivehome.Home, out map[string]any,
) {
	t.Helper()
	catalogue := newSessionToolCatalogue(home.Root, builtin.New(home.Root), home)
	enableQueenLifecycleTools(catalogue, &hiveserver.Session{}, home)
	var tool llm.ToolDef
	found := false
	for _, def := range catalogue.ModelDefs() {
		if def.Name == "get_current_time" {
			tool = def
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("get_current_time missing from the model-visible tool surface")
	}

	round1 := runCrosscheckStream(t, provider, crosscheckMinimalSystem, crosscheckToolPrompt, []llm.ToolDef{tool})
	round2 := (*crosscheckCall)(nil)
	if len(round1.ToolCalls) > 0 {
		call := round1.ToolCalls[0]
		registry := integrations.New(integrations.WithRoots(home.Root))
		rawOutput, execErr := registry.Execute(context.Background(), call.Name, call.ArgumentsParsed)
		toolContent := ""
		if execErr != nil {
			toolContent = fmt.Sprintf("%T: %v", execErr, execErr)
		} else {
			encoded, marshalErr := json.Marshal(rawOutput)
			if marshalErr != nil {
				t.Fatalf("marshal tool result: %v", marshalErr)
			}
			toolContent = string(encoded)
		}

		// The runtime's own serialization for the assistant turn
		// (agentloop.Message.ToLLMDict), then the request projection the loop
		// applies (agentloop.toChatMessages, loop.go — mirrored here because
		// it is unexported and this probe lives in package cli). The reasoning
		// the transport surfaced rides the assistant step, exactly like the
		// loop stores it.
		assistant := agentloop.Message{
			Seq:     1,
			Role:    agentloop.RoleAssistant,
			Content: round1.FinalText,
			ToolCalls: []map[string]any{
				{
					"id":   call.ID,
					"type": "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": call.ArgumentsRaw,
					},
				},
			},
		}
		if round1.ReasoningContent != "" {
			reasoning := round1.ReasoningContent
			assistant.ReasoningContent = &reasoning
		}
		assistantDict := assistant.ToLLMDict()
		requestMessages := []llm.ChatMessage{
			{Role: "user", Content: crosscheckToolPrompt},
			crosscheckChatMessage(assistantDict),
			{Role: "tool", ToolCallID: call.ID, Content: toolContent},
		}
		ctx, cancel := context.WithTimeout(context.Background(), crosscheckCallTimeout)
		defer cancel()
		events := []map[string]any{}
		eventTypes := []string{}
		started := time.Now()
		completion, err := provider.ACompleteStream(ctx, llm.CompletionRequest{
			Messages: requestMessages,
			System:   crosscheckMinimalSystem,
			Tools:    []llm.ToolDef{tool},
		}, func(event llm.StreamEvent) {
			mapped, mapErr := llm.StreamEventToMap(event)
			if mapErr != nil {
				t.Errorf("StreamEventToMap: %v", mapErr)
				return
			}
			events = append(events, mapped)
			eventTypes = append(eventTypes, event.StreamEventType())
		})
		followUp := crosscheckCall{
			Events:       events,
			EventTypes:   eventTypes,
			FinalText:    completion.Content,
			FinishReason: completion.StopReason,
			Usage: &crosscheckUsage{
				PromptTokens:     completion.InputTokens,
				CompletionTokens: completion.OutputTokens,
				TotalTokens:      completion.InputTokens + completion.OutputTokens,
				CachedTokens:     completion.CachedTokens,
			},
			ToolCalls: []crosscheckToolCall{},
			WallMS:    float64(time.Since(started).Milliseconds()),
		}
		if err != nil {
			followUp.Error = crosscheckErrorFrom(err)
			followUp.Usage = nil
		}
		round2 = &followUp
	}

	out["tool_time"] = map[string]any{
		"reps": []map[string]any{
			{
				"rep":             0,
				"round1":          round1,
				"round2":          round2,
				"tool_definition": map[string]any{"go": tool},
			},
		},
	}
}

// crosscheckChatMessage mirrors agentloop.toChatMessages: the projection from
// an LLM message dict to llm.ChatMessage that the real request path applies.
func crosscheckChatMessage(message map[string]any) llm.ChatMessage {
	role, _ := message["role"].(string)
	content, _ := message["content"].(string)
	chat := llm.ChatMessage{Role: role, Content: content}
	if id, ok := message["tool_call_id"].(string); ok {
		chat.ToolCallID = id
	}
	if rawCalls, ok := message["tool_calls"].([]map[string]any); ok {
		for _, rawCall := range rawCalls {
			call := llm.ToolCall{}
			if id, ok := rawCall["id"].(string); ok {
				call.ID = id
			}
			if function, ok := rawCall["function"].(map[string]any); ok {
				call.Name, _ = function["name"].(string)
				call.Arguments, _ = function["arguments"].(string)
			}
			chat.ToolCalls = append(chat.ToolCalls, call)
		}
	}
	if reasoning, ok := message["reasoning_content"].(string); ok {
		chat.ReasoningContent = reasoning
	}
	return chat
}
