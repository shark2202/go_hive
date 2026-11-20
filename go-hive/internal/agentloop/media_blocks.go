package agentloop

import (
	"fmt"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

// multimodalContentBlocks returns the message's content block list when it
// carries at least one non-text block (image_url / file). Text-only content
// stays a plain string on ChatMessage.Content, so every existing caller keeps
// the exact payload it had before multimodal parts existed.
func multimodalContentBlocks(content any) []map[string]any {
	blocks := toContentBlocks(content)
	if len(blocks) == 0 {
		return nil
	}
	for _, block := range blocks {
		if blockType, _ := block["type"].(string); blockType != "" && blockType != "text" {
			return blocks
		}
	}
	return nil
}

// gateUnsupportedMedia replaces image/PDF content blocks the selected model
// cannot receive with an explicit text block naming the limitation. Shipping
// them anyway fails the request or gets them dropped in transit; dropping them
// here silently would leave the model believing it saw bytes it never got.
func gateUnsupportedMedia(msgs []map[string]any, model string) []map[string]any {
	if model == "" {
		return msgs
	}
	allowImages := llm.SupportsImageBlocks(model)
	allowDocuments := llm.SupportsDocumentBlocks(model)
	if allowImages && allowDocuments {
		return msgs
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, message := range msgs {
		blocks := contentBlocks(message["content"])
		if blocks == nil {
			out = append(out, message)
			continue
		}
		changed := false
		gated := make([]map[string]any, 0, len(blocks))
		for _, block := range blocks {
			blockType, _ := block["type"].(string)
			switch {
			case blockType == "image_url" && !allowImages:
				gated = append(gated, mediaNotDeliveredBlock(model, "image"))
				changed = true
			case blockType == "file" && !allowDocuments:
				gated = append(gated, mediaNotDeliveredBlock(model, "pdf"))
				changed = true
			default:
				gated = append(gated, block)
			}
		}
		if !changed {
			out = append(out, message)
			continue
		}
		copied := copyMap(message)
		copied["content"] = gated
		out = append(out, copied)
	}
	return out
}

// mediaNotDeliveredBlock is the placeholder that keeps an undeliverable
// attachment visible to the model as a limitation, never as content.
func mediaNotDeliveredBlock(model, kind string) map[string]any {
	return map[string]any{
		"type": "text",
		"text": fmt.Sprintf(
			"[%s attachment not delivered: model %q cannot receive %s input on this transport. "+
				"Tell the user the attachment could not be shown to you; do not describe its contents.]",
			strings.ToUpper(kind), model, kind),
	}
}
