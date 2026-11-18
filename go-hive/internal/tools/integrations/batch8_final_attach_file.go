// attach_file — surface local files to the user as chat attachment chips and
// put a bounded text preview into the model's context.
//
// Parity: tools/src/aden_tools/tools/attach_file_tool/attach_file_tool.py
// Parity: tools/src/aden_tools/utils/attachments.py
//
// This is the closest Go equivalent of the upstream MCP tool. The files it
// touches are ALREADY readable by the agent (they are paths the model just
// created or the user uploaded), so unlike the csv/doc writers it is not
// restricted to Runtime.Roots: attaching is a read-only reference, not a
// sandboxed write.
//
// Declared differences from upstream (no false success, no silent drop):
//
//  1. Upstream returns one TextContent summary plus one ImageContent block per
//     pdf/image and one TextContent block per text file. Go's tools.ToolResult
//     carries a single Content string plus ImageContent blocks, so:
//     - text previews are embedded in the summary JSON as "inline_preview"
//     instead of a trailing text block. A trailing body would break
//     agentloop.collectAttachFileChipURLs, which parses the whole content as
//     JSON to harvest hive_attachment_url.
//     - real images (png/jpg/jpeg/webp/gif) become ImageContent blocks.
//     - PDFs get NO content block: the Go LLM transport has no `file` block and
//     an `application/pdf` image block is rejected by Anthropic. Upstream
//     forwards the pdf bytes; here the pdf is chip + reference only.
//  2. The chat runtime behind `hive serve` transports tool results as text
//     only (server.ChatRuntimeDeps.Execute). ImageContent therefore reaches a
//     registry caller but NOT the model. Each entry says which path it took in
//     "inline" ("text_preview" | "image_block" | "none"), so the model is never
//     told it has seen bytes it cannot see.
//  3. "session_cwd"/"storage_path" are hidden context inputs injected by the
//     session catalogue (upstream passes session_cwd as a CONTEXT_PARAM); they
//     are deliberately absent from the model-facing schema.
//  4. Chip publishing is the framework's job upstream. Here
//     PublishAttachFileResult does the same copy + hive_attachment_url
//     injection so the runtime that owns the session directory is the single
//     publisher, and a total publish failure is rewritten as a tool error.
package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/attachments"
	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	attachFileMaxPaths          = 10
	attachFileDefaultMaxBytesMB = 25.0
	// Cap the inline text body. The summary JSON must stay well under the agent
	// loop's tool-result budget: an oversized result is spilled to disk and the
	// chip renderer loses the leading JSON envelope. The full file always
	// reaches the session attachments copy.
	attachFileTextInlineCapBytes = 8 * 1024
	attachFileSniffBytes         = 4096
)

// attachFileBinaryPreviewMIME mirrors _BINARY_PREVIEW_EXT_TO_MIME upstream.
var attachFileBinaryPreviewMIME = map[string]string{
	".pdf":  "application/pdf",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".gif":  "image/gif",
}

// attachFileClassify mirrors _classify_file: decide how a file is surfaced.
func attachFileClassify(ext string, rawBytes []byte) (string, string) {
	if mime, ok := attachFileBinaryPreviewMIME[ext]; ok {
		if mime == "application/pdf" {
			return "pdf", mime
		}
		return "image", mime
	}
	if mime, ok := attachments.TextExtToMIME[ext]; ok {
		return "text", mime
	}
	sample := rawBytes
	if len(sample) > attachFileSniffBytes {
		sample = sample[:attachFileSniffBytes]
	}
	if attachments.LooksLikeText(sample) {
		return "text", "text/plain"
	}
	return "blob", "application/octet-stream"
}

// attachFileResolve mirrors _resolve_path: absolute, then the caller's working
// directory, then $HIVE_STORAGE_PATH (or the injected storage_path), then the
// process working directory. Only regular files resolve.
func attachFileResolve(raw, sessionCwd, storagePath string) (string, bool) {
	var candidates []string
	if filepath.IsAbs(raw) {
		candidates = append(candidates, raw)
	} else {
		if sessionCwd != "" {
			candidates = append(candidates, filepath.Join(sessionCwd, raw))
		}
		if storagePath == "" {
			storagePath = os.Getenv("HIVE_STORAGE_PATH")
		}
		if storagePath != "" {
			candidates = append(candidates, filepath.Join(storagePath, raw))
		}
		if cwd, err := os.Getwd(); err == nil {
			candidates = append(candidates, filepath.Join(cwd, raw))
		}
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		resolved, err := filepath.Abs(candidate)
		if err != nil {
			return candidate, true
		}
		return resolved, true
	}
	return "", false
}

// attachFilePaths normalises the paths argument. The second return value is a
// model-facing error when the shape is wrong.
func attachFilePaths(input map[string]any) ([]string, string) {
	raw, ok := input["paths"]
	if !ok || raw == nil {
		return nil, "paths must be a string or list, got null"
	}
	switch typed := raw.(type) {
	case string:
		return []string{typed}, ""
	case []string:
		return typed, ""
	case []any:
		paths := make([]string, 0, len(typed))
		for _, item := range typed {
			paths = append(paths, fmt.Sprint(item))
		}
		return paths, ""
	default:
		return nil, fmt.Sprintf("paths must be a string or list, got %s", attachFileTypeName(raw))
	}
}

func attachFileTypeName(value any) string {
	switch value.(type) {
	case int, int64, float64:
		return "int"
	case bool:
		return "bool"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", value)
	}
}

// attachFileTextPreview decodes the body as text and caps it, mirroring the
// upstream 8 KiB inline preview.
func attachFileTextPreview(rawBytes []byte) string {
	truncated := len(rawBytes) > attachFileTextInlineCapBytes
	sample := rawBytes
	if truncated {
		sample = rawBytes[:attachFileTextInlineCapBytes]
	}
	text := strings.ToValidUTF8(string(sample), "\uFFFD")
	if truncated {
		text += fmt.Sprintf("\n\n…[preview truncated at %d bytes — download the full file via the chip, or read it with a terminal tool]",
			attachFileTextInlineCapBytes)
	}
	return text
}

// attachFile is the upstream `attach_file` tool body.
func attachFile(_ context.Context, _ *Runtime, input map[string]any) (any, error) {
	paths, shapeError := attachFilePaths(input)
	if shapeError != "" {
		return tools.ToolResult{
			Content: jsonString(map[string]any{
				"attached": []any{},
				"errors":   []map[string]any{{"error": shapeError}},
			}),
		}, nil
	}
	if len(paths) > attachFileMaxPaths {
		return tools.ToolResult{
			Content: jsonString(map[string]any{
				"attached": []any{},
				"errors": []map[string]any{{"error": fmt.Sprintf(
					"Too many paths: %d provided, max %d per call.", len(paths), attachFileMaxPaths)}},
			}),
		}, nil
	}

	maxBytesMB := floatArg(input, "max_bytes_mb", attachFileDefaultMaxBytesMB)
	maxBytes := int(maxBytesMB * 1024 * 1024)
	if maxBytes < 1 {
		maxBytes = 1
	}
	sessionCwd := stringArg(input, "session_cwd")
	storagePath := stringArg(input, "storage_path")

	attached := make([]map[string]any, 0, len(paths))
	problems := make([]map[string]any, 0)
	refs := make([]tools.AttachmentRef, 0, len(paths))
	imageBlocks := make([]map[string]any, 0, len(paths))

	for _, rawPath := range paths {
		resolved, ok := attachFileResolve(rawPath, sessionCwd, storagePath)
		if !ok {
			problems = append(problems, map[string]any{"path": rawPath, "error": "file not found"})
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			problems = append(problems, map[string]any{"path": rawPath, "error": "stat failed: " + err.Error()})
			continue
		}
		size := info.Size()
		if size > int64(maxBytes) {
			problems = append(problems, map[string]any{"path": rawPath, "error": fmt.Sprintf(
				"file exceeds max_bytes (%d > %d); raise max_bytes_mb or shrink the file.", size, maxBytes)})
			continue
		}
		rawBytes, err := os.ReadFile(resolved)
		if err != nil {
			problems = append(problems, map[string]any{"path": rawPath, "error": "read failed: " + err.Error()})
			continue
		}

		ext := strings.ToLower(filepath.Ext(resolved))
		kind, mime := attachFileClassify(ext, rawBytes)

		entry := map[string]any{
			"path":     rawPath,
			"resolved": resolved,
			"kind":     kind,
			"mime":     mime,
			"bytes":    size,
			"filename": filepath.Base(resolved),
			"inline":   "none",
		}
		switch kind {
		case "text":
			entry["inline"] = "text_preview"
			entry["inline_preview"] = attachFileTextPreview(rawBytes)
		case "image":
			entry["inline"] = "image_block"
			imageBlocks = append(imageBlocks, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(rawBytes),
				},
			})
		}

		attached = append(attached, entry)
		refs = append(refs, tools.AttachmentRef{
			Path:     rawPath,
			Resolved: resolved,
			Kind:     kind,
			MIME:     mime,
			Bytes:    size,
			Filename: filepath.Base(resolved),
		})
	}

	// A PDF without a content block is still a valid attachment, but say so in
	// the model-facing payload (see the header's declared difference #1/#2).
	notes := make([]string, 0, 1)
	for _, entry := range attached {
		if entry["kind"] == "pdf" {
			notes = append(notes, "PDFs are delivered as an attachment reference; this build does not inline pdf bytes into the model context.")
			break
		}
	}
	payload := map[string]any{
		"attached": attached,
		"errors":   problems,
	}
	if len(imageBlocks) > 0 {
		payload["image_blocks"] = len(imageBlocks)
		notes = append(notes, "Image bytes were returned as image content blocks; a text-only chat transport will not show them to the model.")
	}
	if len(notes) > 0 {
		payload["note"] = strings.Join(notes, " ")
	}
	return tools.ToolResult{
		Content:      jsonString(payload),
		ImageContent: imageBlocks,
		Attachments:  refs,
	}, nil
}

// PublishAttachFileResult is the framework-side chip publisher: it copies each
// attachment into <sessionDir>/data/attachments, points the summary entry at the
// copy and injects hive_attachment_url (hive-attachment://data/attachments/…),
// which collectAttachFileChipURLs turns into a renderable chip.
//
// Parity: core/tests/test_attach_file_publish.py::test_publishes_chip_and_injects_url
//
// Loud-failure policy (upstream `_attach_file_publish_failure`): when nothing can
// be published the content is rewritten into an error payload and isError is
// true, so the agent never claims a file was delivered when no chip exists.
func PublishAttachFileResult(content string, refs []tools.AttachmentRef, sessionDir string) (string, bool) {
	if len(refs) == 0 {
		return content, false
	}
	payload, ok := attachFileParseSummary(content)
	if !ok {
		return attachFilePublishFailure("tool result is not a JSON summary"), true
	}
	if sessionDir == "" {
		return attachFilePublishFailure("no session directory on this runtime"), true
	}
	attachmentsDir := filepath.Join(sessionDir, "data", "attachments")
	if err := os.MkdirAll(attachmentsDir, 0o755); err != nil {
		return attachFilePublishFailure("could not create attachments dir: " + err.Error()), true
	}
	entries, _ := payload["attached"].([]any)
	published := 0
	problems := make([]string, 0)
	for index, ref := range refs {
		entry := attachFileEntry(entries, index, ref)
		if entry == nil {
			problems = append(problems, "entry missing from summary: "+ref.Filename)
			continue
		}
		source := ref.Resolved
		info, err := os.Stat(source)
		if err != nil || info.IsDir() {
			problems = append(problems, "source vanished: "+source)
			continue
		}
		ext := strings.ToLower(filepath.Ext(source))
		base := attachments.SanitizeBasename(filepath.Base(source), ext, "attachment")
		destName := attachments.DisambiguateFilename(attachmentsDir, base)
		dest := filepath.Join(attachmentsDir, destName)
		if err := batch8CopyFile(source, dest); err != nil {
			problems = append(problems, "copy failed for "+filepath.Base(source)+": "+err.Error())
			continue
		}
		entry["resolved"] = dest
		entry["hive_attachment_url"] = "hive-attachment://data/attachments/" + destName
		published++
	}
	if published == 0 {
		reason := "could not publish any attachment"
		if len(problems) > 0 {
			reason += ": " + strings.Join(problems, "; ")
		}
		return attachFilePublishFailure(reason), true
	}
	return jsonString(payload), false
}

// attachFileParseSummary decodes the summary JSON that starts the content.
func attachFileParseSummary(content string) (map[string]any, bool) {
	leading := strings.TrimLeft(content, " \t\r\n")
	if !strings.HasPrefix(leading, "{") {
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(leading), &payload); err != nil {
		return nil, false
	}
	return payload, true
}

// attachFileEntry finds the summary entry that belongs to a ref. The tool emits
// one attached entry per successful ref in order, so index alignment is the
// primary match and the resolved/filename pair is the fallback.
func attachFileEntry(entries []any, index int, ref tools.AttachmentRef) map[string]any {
	if index < len(entries) {
		if entry, ok := entries[index].(map[string]any); ok {
			if resolved, _ := entry["resolved"].(string); resolved == ref.Resolved || resolved == "" {
				return entry
			}
			if filename, _ := entry["filename"].(string); filename == ref.Filename {
				return entry
			}
		}
	}
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if resolved, _ := entry["resolved"].(string); resolved == ref.Resolved {
			return entry
		}
		if filename, _ := entry["filename"].(string); filename == ref.Filename {
			return entry
		}
	}
	return nil
}

// attachFilePublishFailure mirrors _attach_file_publish_failure.
func attachFilePublishFailure(reason string) string {
	return jsonString(map[string]any{
		"attached": []any{},
		"errors": []map[string]any{{"error": fmt.Sprintf(
			"attach_file failed to publish the file as a chip: %s. The file was NOT delivered to the user. "+
				"Tell the user the delivery failed; do not claim the file was sent.", reason)}},
	})
}

func batch8CopyFile(source, dest string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0o600)
}
