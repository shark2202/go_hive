package server

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
)

// attachmentChatLLM scripts one tool call per turn and records every request so
// the test can assert what the model actually received.
type attachmentChatLLM struct {
	model string

	mu        sync.Mutex
	requests  []llm.CompletionRequest
	responses []llm.Completion
	calls     int
}

func (f *attachmentChatLLM) ModelName() string { return f.model }

func (f *attachmentChatLLM) AComplete(_ context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	f.calls++
	if len(f.responses) == 0 {
		return llm.Completion{Content: "done", Model: f.model, StopReason: "end_turn"}, nil
	}
	next := f.responses[0]
	f.responses = f.responses[1:]
	if next.Model == "" {
		next.Model = f.model
	}
	if next.StopReason == "" {
		next.StopReason = "end_turn"
	}
	return next, nil
}

func (f *attachmentChatLLM) lastRequest(t *testing.T) llm.CompletionRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("model received no request")
	}
	return f.requests[len(f.requests)-1]
}

// attachmentRuntime builds a chat runtime whose tool returns provider-neutral
// content blocks, mirroring what sessionToolCatalogue.ExecuteBlocks provides
// for attach_file.
func attachmentRuntime(t *testing.T, completer llm.Completer, blocks []map[string]any, root string) (*ChatRuntime, *chatEventRecorder) {
	t.Helper()
	bus := eventbus.NewEventBus()
	runtime, err := NewChatRuntime(ChatRuntimeDeps{
		LLM:   completer,
		Tools: []llm.ToolDef{{Name: "attach_file", Description: "attach a file"}},
		ExecuteBlocks: func(context.Context, string, map[string]any) (string, bool, []map[string]any, error) {
			return `{"attached": [{"kind": "attachment"}]}`, false, blocks, nil
		},
		Root:      root,
		Bus:       bus,
		SessionID: "session_20261120_000000_attach01",
	})
	if err != nil {
		t.Fatalf("NewChatRuntime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, recordChatEvents(bus)
}

func attachmentToolCallTurn() llm.Completion {
	return llm.Completion{
		Content:   "attaching",
		ToolCalls: []llm.ToolCall{{ID: "call_attach_1", Name: "attach_file", Arguments: `{"paths":["pic.png"]}`}},
	}
}

func runAttachmentTurn(t *testing.T, runtime *ChatRuntime, recorder *chatEventRecorder, message string) {
	t.Helper()
	if err := runtime.Deliver(message); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)
}

func imageContentBlock(url string) map[string]any {
	return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
}

func pdfContentBlock(filename string) map[string]any {
	return map[string]any{
		"type": "file",
		"file": map[string]any{
			"file_data": "data:application/pdf;base64,JVBERi0=",
			"filename":  filename,
		},
	}
}

// requestContentParts collects every non-empty ContentParts list in a request,
// keyed by role, in message order.
func requestContentParts(req llm.CompletionRequest) []map[string]any {
	var blocks []map[string]any
	for _, message := range req.Messages {
		blocks = append(blocks, message.ContentParts...)
	}
	return blocks
}

// Parity-exempt: the chat runtime's ExecuteBlocks seam is Go-only; upstream
// proves the same path through test_attach_file_publish.py plus LiteLLM.
func TestChatRuntimeSendsImageBlockToModel(t *testing.T) {
	imageURL := "data:image/png;base64,AAAA"
	model := &attachmentChatLLM{
		model:     "openai/gpt-5.4",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}
	runtime, recorder := attachmentRuntime(t, model, []map[string]any{imageContentBlock(imageURL)}, t.TempDir())
	runAttachmentTurn(t, runtime, recorder, "show me the picture")

	req := model.lastRequest(t)
	blocks := requestContentParts(req)
	found := false
	for _, block := range blocks {
		if block["type"] == "image_url" {
			inner, _ := block["image_url"].(map[string]any)
			if inner["url"] == imageURL {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("model request carried no image block: %#v", blocks)
	}
	// The tool result text must still be present: blocks augment, not replace.
	textSeen := false
	for _, message := range req.Messages {
		if message.Role == "tool" && strings.Contains(message.Content, "attached") {
			textSeen = true
		}
	}
	if !textSeen {
		t.Fatalf("tool text projection missing: %#v", req.Messages)
	}
}

// Parity-exempt: PDF delivery through the chat runtime is Go-only; upstream
// relies on LiteLLM to remap the file block.
func TestChatRuntimeSendsPDFFileBlockToModel(t *testing.T) {
	model := &attachmentChatLLM{
		model:     "openai/gpt-5.4",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}
	runtime, recorder := attachmentRuntime(t, model, []map[string]any{pdfContentBlock("doc.pdf")}, t.TempDir())
	runAttachmentTurn(t, runtime, recorder, "read the pdf")

	blocks := requestContentParts(model.lastRequest(t))
	found := false
	for _, block := range blocks {
		if block["type"] != "file" {
			continue
		}
		file, _ := block["file"].(map[string]any)
		if file["filename"] == "doc.pdf" && strings.HasPrefix(file["file_data"].(string), "data:application/pdf;base64,") {
			found = true
		}
	}
	if !found {
		t.Fatalf("model request carried no pdf file block: %#v", blocks)
	}
}

// Parity-exempt: multi-attachment delivery through the chat runtime is Go-only.
func TestChatRuntimeSendsMultipleAttachments(t *testing.T) {
	blocks := []map[string]any{
		imageContentBlock("data:image/png;base64,AAAA"),
		pdfContentBlock("a.pdf"),
		imageContentBlock("data:image/jpeg;base64,BBBB"),
	}
	model := &attachmentChatLLM{
		model:     "openai/gpt-5.4",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}
	runtime, recorder := attachmentRuntime(t, model, blocks, t.TempDir())
	runAttachmentTurn(t, runtime, recorder, "attach all three")

	got := requestContentParts(model.lastRequest(t))
	media := make([]map[string]any, 0, len(got))
	for _, block := range got {
		if block["type"] != "text" {
			media = append(media, block)
		}
	}
	if len(media) != 3 {
		t.Fatalf("content parts = %d, want 3: %#v", len(media), got)
	}
	types := []string{media[0]["type"].(string), media[1]["type"].(string), media[2]["type"].(string)}
	if !reflect.DeepEqual(types, []string{"image_url", "file", "image_url"}) {
		t.Fatalf("block types = %v", types)
	}
}

// Parity-exempt: upstream's text-only path uses a caption subagent; the Go
// equivalent refuses the block explicitly and says so, which this pins.
func TestChatRuntimeGatesImagesForTextOnlyModel(t *testing.T) {
	model := &attachmentChatLLM{
		model:     "deepseek-v4-pro",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}
	runtime, recorder := attachmentRuntime(t, model,
		[]map[string]any{imageContentBlock("data:image/png;base64,AAAA")}, t.TempDir())
	runAttachmentTurn(t, runtime, recorder, "show me the picture")

	req := model.lastRequest(t)
	for _, block := range requestContentParts(req) {
		if block["type"] == "image_url" {
			t.Fatalf("text-only model received an image block: %#v", block)
		}
	}
	notDelivered := false
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "attachment not delivered") {
			notDelivered = true
		}
		for _, block := range message.ContentParts {
			if text, ok := block["text"].(string); ok && strings.Contains(text, "attachment not delivered") {
				notDelivered = true
			}
		}
	}
	if !notDelivered {
		t.Fatalf("model was not told the attachment was dropped: %#v", req.Messages)
	}
}

// Parity-exempt: cold-restore of a multimodal tool result is Go-only; the
// stored image_content shape mirrors upstream's Message.image_content.
func TestChatRuntimeRebuildsAttachmentBlocksFromHistory(t *testing.T) {
	root := t.TempDir()
	imageURL := "data:image/png;base64,AAAA"
	model := &attachmentChatLLM{
		model:     "openai/gpt-5.4",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}
	first, recorder := attachmentRuntime(t, model, []map[string]any{imageContentBlock(imageURL)}, root)
	runAttachmentTurn(t, first, recorder, "show me the picture")
	if err := first.Close(); err != nil {
		t.Fatalf("close first runtime: %v", err)
	}

	secondModel := &attachmentChatLLM{model: "openai/gpt-5.4"}
	second, secondRecorder := attachmentRuntime(t, secondModel, nil, root)
	if err := second.Deliver("what did I attach?"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, secondRecorder, eventbus.EventExecutionCompleted, 1)

	req := secondModel.lastRequest(t)
	for _, block := range requestContentParts(req) {
		if block["type"] != "image_url" {
			continue
		}
		inner, _ := block["image_url"].(map[string]any)
		if inner["url"] == imageURL {
			return
		}
	}
	t.Fatalf("restored history lost the image block: %#v", requestContentParts(req))
}

// streamingAttachmentLLM is an attachmentChatLLM that also implements
// llm.StreamCompleter, so the streaming chat path can be held to the same
// block-delivery guarantee as the non-streaming one.
type streamingAttachmentLLM struct {
	attachmentChatLLM
}

func (f *streamingAttachmentLLM) ACompleteStream(_ context.Context, req llm.CompletionRequest, emit func(llm.StreamEvent)) (llm.Completion, error) {
	completion, err := f.AComplete(context.Background(), req)
	if err != nil {
		return llm.Completion{}, err
	}
	emit(llm.FinishEvent{StopReason: completion.StopReason, Model: completion.Model})
	return completion, nil
}

// Parity-exempt: streaming transport block delivery is Go-only; upstream's
// streaming path shares the same LiteLLM request shape.
func TestChatRuntimeStreamingCarriesImageBlocks(t *testing.T) {
	model := &streamingAttachmentLLM{attachmentChatLLM{
		model:     "openai/gpt-5.4",
		responses: []llm.Completion{attachmentToolCallTurn()},
	}}
	runtime, recorder := attachmentRuntime(t, model,
		[]map[string]any{imageContentBlock("data:image/png;base64,AAAA")}, t.TempDir())

	if err := runtime.Deliver("show me the picture"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForChatEvents(t, recorder, eventbus.EventExecutionCompleted, 1)

	model.mu.Lock()
	requests := append([]llm.CompletionRequest(nil), model.requests...)
	model.mu.Unlock()
	if len(requests) == 0 {
		t.Fatal("streaming model received no request")
	}
	found := false
	for _, block := range requestContentParts(requests[len(requests)-1]) {
		if block["type"] == "image_url" {
			found = true
		}
	}
	if !found {
		t.Fatalf("streamed request carried no image block: %#v", requestContentParts(requests[len(requests)-1]))
	}
}
