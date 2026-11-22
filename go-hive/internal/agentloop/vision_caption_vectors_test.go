//go:build e2e

package agentloop

// Dumps the bytes the production vision-fallback path produces for fixed
// inputs, so the Python reference can be byte-compared against it.
//
// The vectors avoid the two places Go cannot match Python by construction:
// map key order (Go sorts, Python keeps the model's order) and float repr
// corner cases. Every vector exercises the real code —
// visionfallback.IntentForTool, RemapCaptionForCrop and the storeToolBatch
// splice — not a reimplementation.
//
// Usage:
//
//	VC_VECTORS_IN=vectors.json VC_VECTORS_OUT=go.json \
//	  go test -tags e2e -run TestVisionCaptionParityVectors ./internal/agentloop

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

type parityVector struct {
	ID            string         `json:"id"`
	ToolName      string         `json:"tool_name"`
	ToolArgs      map[string]any `json:"tool_args"`
	AssistantText string         `json:"assistant_text"`
	ToolContent   string         `json:"tool_content"`
	Caption       string         `json:"caption"`
	CropBox       []float64      `json:"crop_box"`
}

type parityResult struct {
	ID            string `json:"id"`
	Intent        string `json:"intent"`
	StoredContent string `json:"stored_content"`
}

// vectorCompleter only names a text-only model so the caption gate fires.
type vectorCompleter struct{ model string }

func (c vectorCompleter) AComplete(context.Context, llm.CompletionRequest) (llm.Completion, error) {
	return llm.Completion{}, nil
}

func (c vectorCompleter) ModelName() string { return c.model }

// TestVisionCaptionParityVectors dumps the bytes the production caption path
// produces for fixed inputs so the Python reference can be byte-compared
// against them (driven by go_hive/scripts/e2e_vision_caption.py --vectors).
//
// Parity-exempt: this is the harness half of the Go/Python byte comparison —
// the vectors themselves are asserted against the reference's real functions
// by the driver, not against a named Python test.
func TestVisionCaptionParityVectors(t *testing.T) {
	in := os.Getenv("VC_VECTORS_IN")
	out := os.Getenv("VC_VECTORS_OUT")
	if in == "" || out == "" {
		t.Skip("VC_VECTORS_IN / VC_VECTORS_OUT not set")
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var payload struct {
		Vectors []parityVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}

	results := make([]parityResult, 0, len(payload.Vectors))
	for _, vector := range payload.Vectors {
		results = append(results, runParityVector(t, vector))
	}
	encoded, err := json.MarshalIndent(map[string]any{"vectors": results}, "", "  ")
	if err != nil {
		t.Fatalf("encode results: %v", err)
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		t.Fatalf("write results: %v", err)
	}
}

func runParityVector(t *testing.T, vector parityVector) parityResult {
	t.Helper()
	completer := vectorCompleter{model: "groq/llama-3.1-8b-instant"} // catalogued text-only
	cfg := DefaultLoopConfig()
	cfg.SpilloverDir = t.TempDir()
	loop := NewAgentLoop(cfg, func(ToolCallRequest) (ToolResult, error) {
		return ToolResult{Content: vector.ToolContent}, nil
	})
	loop.VisionCaptioner = &fakeCaptioner{caption: vector.Caption, ok: true}

	conv := NewNodeConversation("You are an agent.")
	if vector.AssistantText != "" {
		if _, err := conv.AddAssistantMessage(vector.AssistantText, nil, AssistantMessageOptions{}); err != nil {
			t.Fatalf("%s: add assistant message: %v", vector.ID, err)
		}
	}
	agentCtx := &AgentContext{
		AgentID:   "vision_vectors",
		AgentSpec: NewAgentSpec("vision_vectors", "Vectors", "parity vectors"),
		LLM:       completer,
		StreamID:  "vision_vectors",
	}

	arguments, err := json.Marshal(vector.ToolArgs)
	if err != nil {
		t.Fatalf("%s: marshal args: %v", vector.ID, err)
	}
	toolCalls := []llm.ToolCall{{ID: "vec_1", Name: vector.ToolName, Arguments: string(arguments)}}
	results := []ToolResult{{
		Content:      vector.ToolContent,
		ImageContent: []map[string]any{{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,AAAA"}}},
	}}
	if _, err := loop.storeToolBatch(context.Background(), agentCtx, conv, toolCalls, results, "", []bool{true}, nil, nil); err != nil {
		t.Fatalf("%s: store tool batch: %v", vector.ID, err)
	}
	message := toolMessage(t, conv)

	// The intent is what the captioner was handed — the production
	// IntentForTool output, not a recomputed copy.
	intent := ""
	if fake, ok := loop.VisionCaptioner.(*fakeCaptioner); ok && len(fake.requests) > 0 {
		intent = fake.requests[0]
	}
	return parityResult{ID: vector.ID, Intent: intent, StoredContent: message.Content}
}
