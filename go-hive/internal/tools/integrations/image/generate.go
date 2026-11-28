package image

// imageGenerate posts one generation/edit request to the Hive image proxy and
// saves every returned image under the tool-artifact directory. It reuses the
// runtime's Hive proxy token (HIVE_API_KEY) and the proxy base URL
// (HIVE_LLM_BASE_URL, defaulting to the hosted proxy), exactly like the Python
// tool. Without a token the tool reports an explicit 503 instead of pretending
// an image was produced.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func imageGenerate(ctx context.Context, rt *Runtime, input map[string]any, deps Deps) (any, error) {
	token := strings.TrimSpace(envValue(rt, "HIVE_API_KEY"))
	if token == "" {
		// The Hive proxy bills generation to the user's credits and is the
		// only key path; without it the capability is unavailable (503), not
		// merely misconfigured.
		return map[string]any{
			"error":  "Image generation unavailable: missing Hive proxy token. Sign in to refresh credentials, then retry.",
			"status": http.StatusServiceUnavailable,
		}, nil
	}

	model := strings.TrimSpace(stringArg(input, "model"))
	if model == "" {
		model = defaultModel
	}
	size := strings.TrimSpace(stringArg(input, "size"))
	if size == "" {
		size = defaultSize
	}
	// Only "low" quality is enabled for cost control; every higher tier is
	// forced down, matching the upstream clamp.
	quality := strings.TrimSpace(stringArg(input, "quality"))
	if quality != "low" {
		quality = "low"
	}
	n := intArg(input, "n", 1)
	if n < 1 {
		n = 1
	}
	if n > maxImages {
		n = maxImages
	}
	format := strings.ToLower(strings.TrimSpace(stringArg(input, "output_format")))
	if format != "png" && format != "jpeg" && format != "webp" {
		format = "png"
	}

	body := map[string]any{
		"model":         model,
		"prompt":        stringArg(input, "prompt"),
		"size":          size,
		"quality":       quality,
		"n":             n,
		"output_format": format,
	}

	references := stringSliceArg(input, "reference_images")
	if len(references) > maxReferences {
		return map[string]any{"error": fmt.Sprintf("Too many reference images: %d (max %d).", len(references), maxReferences)}, nil
	}
	if len(references) > 0 {
		encoded := make([]string, 0, len(references))
		total := 0
		for _, source := range references {
			payload, sizeBytes, err := loadReference(ctx, rt, deps, source)
			if err != nil {
				return map[string]any{"error": err.Error()}, nil
			}
			total += sizeBytes
			if total > maxTotalReferenceBytes {
				return map[string]any{"error": fmt.Sprintf("Reference images total exceeds %dMB.", maxTotalReferenceBytes/(1024*1024))}, nil
			}
			encoded = append(encoded, payload)
		}
		body["image"] = encoded
	}

	base := strings.TrimRight(strings.TrimSpace(envValue(rt, "HIVE_LLM_BASE_URL")), "/")
	if base == "" {
		base = moduleBaseURL(rt, "image_gen_tool", imageProxyBase)
	}
	target := base + imagesPath

	payload, err := json.Marshal(body)
	if err != nil {
		return map[string]any{"error": "Failed to encode image request: " + err.Error()}, nil
	}
	requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return map[string]any{"error": "Failed to build image request: " + err.Error()}, nil
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	client := httpClient(rt, requestTimeout)
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if isTimeout(err) {
			// Do not auto-retry: the image may have been generated and billed.
			return map[string]any{"error": "Image generation timed out. Try again; do not retry repeatedly."}, nil
		}
		return map[string]any{"error": "Network error reaching image service: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if readErr != nil {
		return map[string]any{"error": "Failed to read image service response: " + readErr.Error()}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return proxyError(resp.StatusCode, raw), nil
	}

	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return map[string]any{"error": "Image service returned a non-JSON response."}, nil
	}
	items, _ := data["data"].([]any)
	if len(items) == 0 {
		return map[string]any{"error": "Image service returned no image data."}, nil
	}

	outDir, err := artifactDir(storageEnv{getenv: deps.Getenv, userHomeDir: deps.UserHomeDir})
	if err != nil {
		return map[string]any{"error": "Failed to resolve output directory: " + err.Error()}, nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return map[string]any{"error": "Failed to create output directory: " + err.Error()}, nil
	}

	ext := imageExtension(format)
	stamp := deps.Now().UnixMilli()
	saved := make([]map[string]any, 0, len(items))
	var previewBytes []byte
	for index, entry := range items {
		item, _ := entry.(map[string]any)
		encoded, _ := item["b64_json"].(string)
		if encoded == "" {
			continue
		}
		imageBytes, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		// Re-encode from pixels so ancillary chunks (the C2PA provenance
		// manifest) never reach disk. Best-effort: raw bytes survive any
		// decode/encode failure.
		imageBytes = postprocessImage(imageBytes, format)
		path := filepath.Join(outDir, fmt.Sprintf("image_gen_%d_%d.%s", stamp, index, ext))
		if err := os.WriteFile(path, imageBytes, 0o644); err != nil {
			return map[string]any{"error": "Failed to write generated image: " + err.Error()}, nil
		}
		// Reuse the cleaned bytes for the single inline preview, so the
		// preview matches what was saved and the remaining images stay on
		// disk.
		if previewBytes == nil {
			previewBytes = imageBytes
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			absolute = path
		}
		saved = append(saved, map[string]any{
			"path":    absolute,
			"size":    size,
			"quality": quality,
			"format":  format,
			"bytes":   len(imageBytes),
		})
	}
	if len(saved) == 0 {
		return map[string]any{"error": "Image service returned data but no decodable image."}, nil
	}

	summary := map[string]any{
		"images":    saved,
		"model":     model,
		"n":         len(saved),
		"usage":     data["usage"],
		"next_step": "Call attach_file(path) on an image path to show the user a downloadable chip.",
	}
	if len(saved) > 1 {
		summary["note"] = "Only the first image is previewed inline; the rest are on disk — attach_file them to show the user."
	}
	// Upstream returns one TextContent summary plus one ImageContent block for
	// the first image. The shared registry turns this map into exactly that
	// pair (`_text` → Content, `_images` → ToolResult.ImageContent); returning
	// the bare summary would silently drop the preview.
	return map[string]any{
		"_text":   pythonJSONDumps(summary),
		"_images": []map[string]any{imageBlock(imageMime(format), previewBytes)},
	}, nil
}

// proxyError maps a non-200 proxy response onto the upstream error dict,
// keeping the HTTP status for callers that surface it.
func proxyError(status int, raw []byte) map[string]any {
	detail := proxyErrorDetail(raw)
	switch status {
	case http.StatusPaymentRequired:
		return map[string]any{
			"error":  "Out of Hive credits (or subscription inactive): " + detail,
			"status": http.StatusPaymentRequired,
		}
	case http.StatusUnauthorized:
		return map[string]any{
			"error":  "Authentication failed reaching the image service (proxy token rejected).",
			"status": http.StatusUnauthorized,
		}
	case http.StatusForbidden:
		return map[string]any{
			"error":  "Image model unavailable (provider access / org verification): " + detail,
			"status": http.StatusForbidden,
		}
	case http.StatusTooManyRequests:
		return map[string]any{
			"error":  "Image service rate-limited. Wait a moment, then retry once.",
			"status": http.StatusTooManyRequests,
		}
	case http.StatusBadRequest:
		return map[string]any{
			"error":  "Image request rejected: " + detail + ". Rephrase the prompt if it was moderated.",
			"status": http.StatusBadRequest,
		}
	default:
		return map[string]any{
			"error":  fmt.Sprintf("Image service error (HTTP %d): %s", status, detail),
			"status": status,
		}
	}
}

// proxyErrorDetail extracts the human-readable message from a proxy or OpenAI
// error body, both of which nest it under "error".
func proxyErrorDetail(raw []byte) string {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err == nil {
		if object, ok := body["error"].(map[string]any); ok {
			if message, ok := object["message"].(string); ok && message != "" {
				return message
			}
			if kind, ok := object["type"].(string); ok && kind != "" {
				return kind
			}
		}
		if message, ok := body["error"].(string); ok && message != "" {
			return message
		}
	}
	detail := strings.TrimSpace(string(raw))
	if len(detail) > 300 {
		detail = detail[:300]
	}
	return detail
}
