package agentloop

// Vision-fallback captioning: a main model that cannot accept image blocks
// gets a text caption of each tool result's images (with crop coordinates
// remapped to viewport fractions) instead of the image blocks themselves.
//
// Parity: framework/agent_loop/agent_loop.py::_captioning_chain,
// framework/agent_loop/internals/vision_fallback.py::extract_intent_for_tool

import (
	"context"

	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/visionfallback"
)

// VisionCaptioner captions the image content of tool results for a main model
// that cannot accept image blocks. It reports false when no caption could be
// produced, in which case the loop records the exhausted marker instead.
//
// Parity: framework/agent_loop/agent_loop.py::_captioning_chain
type VisionCaptioner interface {
	Caption(ctx context.Context, intent string, images []map[string]any) (string, bool)
}

// captionToolImagesEnabled reports whether a batch's tool images must be
// captioned: a captioner is configured and the main model cannot take image
// blocks.
func (l *AgentLoop) captionToolImagesEnabled(agentCtx *AgentContext) bool {
	if l.VisionCaptioner == nil {
		return false
	}
	mainModel := ""
	if agentCtx != nil {
		mainModel = modelName(agentCtx)
	}
	return mainModel != "" && !llm.SupportsImageToolResults(mainModel)
}

// captionToolImages renders the vision-fallback line spliced into the tool
// result: the caption itself (with crop coordinates remapped to viewport
// fractions) or the exhausted marker when no caption could be produced. A
// captioning failure never fails the turn.
//
// input is the call's coerced input — upstream builds the caption intent from
// tc.tool_input, which the pre-dispatch coercion pass already healed.
//
// Parity: framework/agent_loop/agent_loop.py (vision_fallback_marker splice)
func (l *AgentLoop) captionToolImages(ctx context.Context, conv *NodeConversation, toolName string, input map[string]any, result ToolResult) string {
	intent := visionfallback.IntentForTool(toolName, input, func() string {
		return conv.LastAssistantText()
	})
	caption, ok := l.VisionCaptioner.Caption(ctx, intent, result.ImageContent)
	if !ok {
		return visionfallback.ExhaustedMarker
	}
	return visionfallback.CaptionMarker + "\n" + visionfallback.RemapCaptionForCrop(caption, result.Content)
}
