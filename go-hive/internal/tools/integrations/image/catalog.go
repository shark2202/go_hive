package image

// Image-generation catalog: constants, the image_generate tool definition and
// the small format mappings the request/response path shares. Values mirror the
// upstream image_gen_tool module and are frozen by the catalog digest test.

import "time"

const (
	// imageProxyBase mirrors framework.config.HIVE_LLM_ENDPOINT.
	imageProxyBase = "https://llm.open-hive.com"
	imagesPath     = "/v1/images/generations"

	defaultModel = "gpt-image-2"
	defaultSize  = "1024x1024"

	// maxImages caps runaway spend: every image is billed separately.
	maxImages = 4
	// maxReferences matches OpenAI's edits payload ceiling.
	maxReferences     = 10
	maxReferenceBytes = 10 * 1024 * 1024
	// maxTotalReferenceBytes bounds the whole base64 payload.
	maxTotalReferenceBytes = 20 * 1024 * 1024

	// Image generation can take minutes; keep the same ceiling as upstream
	// (under the background-tool and MCP-client timeouts).
	requestTimeout  = 225 * time.Second
	downloadTimeout = 30 * time.Second
)

// imageTool declares image_generate, one action with the upstream parameter
// list and defaults.
func imageTool() *Tool {
	return &Tool{
		ID:          "image_gen_tool",
		Name:        "image_generate",
		DisplayName: "Image Gen",
		Summary:     "Generate or edit an image from a text prompt. Billed to Hive credits.",
		Actions: []Action{
			{
				Name:    "image_generate",
				Summary: "Generate or edit an image from a text prompt, billed to Hive credits. Provide reference images to edit or compose from existing pictures.",
				Source:  "image_gen_tool/image_gen_tool.py",
				Required: []string{
					"prompt",
				},
				Params: map[string]any{
					"prompt":           param("string"),
					"reference_images": param("array"),
					"size":             paramDefault("string", defaultSize),
					"quality":          paramDefault("string", "low"),
					"n":                paramDefault("integer", 1),
					"output_format":    paramDefault("string", "png"),
					"model":            paramDefault("string", defaultModel),
				},
			},
		},
	}
}

// imageMime maps the requested format onto the preview block's MIME type.
func imageMime(format string) string {
	switch format {
	case "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// imageExtension maps the requested format onto the saved file suffix.
func imageExtension(format string) string {
	switch format {
	case "jpeg":
		return "jpg"
	case "webp":
		return "webp"
	default:
		return "png"
	}
}
