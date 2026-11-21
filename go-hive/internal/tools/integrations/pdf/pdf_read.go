package pdf

// pdf_read: the single-action tool definition and its execution path — input
// validation, multi-file iteration, page-range parsing and response shaping.
// The extraction stack lives in parse.go / streams.go / lexer.go / text.go /
// fonts.go.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var pdfReadTool = &Tool{
	ID:          "pdf_read_tool",
	Name:        "pdf_read",
	DisplayName: "Pdf Read",
	Summary:     "Read and extract text from one or many PDF files (local path or http(s) URL).",
	Actions: []Action{
		{
			Name: "pdf_read",
			Summary: "Extract per-page text and metadata from a text-layer PDF. " +
				"Scanned PDFs return an explicit needs_vision error; encrypted PDFs return an explicit error.",
			Source: "pdf_read_tool/pdf_read_tool.py",
			Params: map[string]any{
				"file_path":        param("string"),
				"files":            paramItems("array", "string"),
				"pages":            paramDefault("string", "all"),
				"max_pages":        paramDefault("integer", defaultMaxPages),
				"max_bytes_mb":     paramDefault("number", defaultMaxBytesMB),
				"password":         param("string"),
				"include_metadata": paramDefault("boolean", true),
			},
			Requires: nil,
		},
	},
}

type pdfOptions struct {
	pages           string
	maxPages        int
	maxBytes        int
	password        string
	includeMetadata bool
}

func pdfReadAction(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	opts := pdfOptions{
		pages:           stringArg(input, "pages"),
		maxPages:        intArg(input, "max_pages", defaultMaxPages),
		password:        stringArg(input, "password"),
		includeMetadata: boolArg(input, "include_metadata", true),
	}
	if opts.maxPages < 1 {
		opts.maxPages = 1
	}
	if opts.maxPages > hardMaxPages {
		opts.maxPages = hardMaxPages
	}
	opts.maxBytes = int(floatArg(input, "max_bytes_mb", defaultMaxBytesMB) * 1024 * 1024)
	if opts.maxBytes < 1 {
		opts.maxBytes = 1
	}

	filePath := strings.TrimSpace(stringArg(input, "file_path"))
	files := stringSliceArg(input, "files")
	if filePath != "" && len(files) > 0 {
		return map[string]any{"error": "Pass either file_path or files, not both."}, nil
	}
	if len(files) > maxFiles {
		return map[string]any{"error": fmt.Sprintf("Too many PDFs: %d provided, max %d.", len(files), maxFiles)}, nil
	}
	multiMode := len(files) > 0
	targets := files
	if !multiMode {
		if filePath == "" {
			return map[string]any{"error": "Provide file_path (single PDF) or files (list, up to 10)."}, nil
		}
		targets = []string{filePath}
	}

	results := make([]any, 0, len(targets))
	for _, ref := range targets {
		results = append(results, pdfReadOne(ctx, rt, ref, opts))
	}
	if multiMode {
		return map[string]any{"pdfs": results}, nil
	}
	return results[0], nil
}

func pdfReadOne(ctx context.Context, rt *Runtime, ref string, opts pdfOptions) any {
	displayPath := ref
	name := ref
	isURL := strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://")
	var data []byte
	if isURL {
		payload, problem := pdfDownload(ctx, rt, ref, opts.maxBytes)
		if problem != nil {
			return problem
		}
		data = payload
		name = pdfURLName(ref)
	} else {
		path, problem := pdfLocalPath(ref)
		if problem != nil {
			return problem
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrPermission) {
				return map[string]any{"error": "Permission denied: " + ref}
			}
			return pdfOpenFailure(path, err)
		}
		data = payload
		displayPath = path
		name = filepath.Base(path)
	}

	doc, err := pdfOpen(data)
	if err != nil {
		if errors.Is(err, errPDFEncrypted) {
			out := pdfTerminalYield(displayPath, "pdf_encrypted", "")
			out["error"] = "Cannot read encrypted PDF: password decryption is not supported by the Go port. " +
				"Decrypt it first (for example qpdf --decrypt file.pdf out.pdf) and retry."
			return out
		}
		return pdfOpenFailure(displayPath, err)
	}

	pages, err := doc.pages()
	if err != nil {
		return pdfOpenFailure(displayPath, err)
	}
	indices, truncated, requested, problem := pdfParsePageRange(opts.pages, len(pages), opts.maxPages)
	if problem != "" {
		return map[string]any{"error": problem}
	}

	pageEntries := make([]map[string]any, 0, len(indices))
	needsVisionPages := []int{}
	contentParts := make([]string, 0, len(indices))
	anyText := false
	for _, index := range indices {
		text, extractErr := doc.pageText(pages[index])
		if extractErr != nil {
			text = ""
		}
		text = pdfNormalizeText(text)
		if text != "" {
			anyText = true
		}
		number := index + 1
		needsVision := len(text) < visionThreshold
		if needsVision {
			needsVisionPages = append(needsVisionPages, number)
		}
		pageEntries = append(pageEntries, map[string]any{
			"number":       number,
			"text":         text,
			"needs_vision": needsVision,
		})
		contentParts = append(contentParts, fmt.Sprintf("--- Page %d ---\n%s", number, text))
	}

	if !anyText {
		// Every requested page is image-only. Returning empty content would be
		// indistinguishable from a broken parser, so make the scan explicit.
		return map[string]any{
			"error": "pdf_scanned_no_text: this PDF has no extractable text layer (scanned or image-only). " +
				"Render the pages and use OCR or a vision-capable model.",
			"needs_vision":       true,
			"needs_vision_pages": needsVisionPages,
			"path":               displayPath,
			"name":               name,
			"total_pages":        len(pages),
			"pages_extracted":    len(indices),
			"next_step":          "Attach the original PDF to a vision-capable model, or run OCR (for example pdftoppm + tesseract).",
			"terminal_hint":      terminalHint,
		}
	}

	content := strings.Join(contentParts, "\n\n")
	result := map[string]any{
		"path":            displayPath,
		"name":            name,
		"total_pages":     len(pages),
		"pages_extracted": len(indices),
		"content":         content,
		"char_count":      len(content),
		"pages":           pageEntries,
	}
	if len(needsVisionPages) > 0 {
		result["needs_vision_pages"] = needsVisionPages
		result["next_step"] = "Pages with little or no text were flagged needs_vision. Attach the original PDF to a vision-capable model for those pages."
		result["terminal_hint"] = terminalHint
	}
	if truncated {
		result["truncated"] = true
		result["truncation_warning"] = fmt.Sprintf(
			"Requested %d page(s), but max_pages=%d. Only the first %d page(s) were processed.",
			requested, opts.maxPages, len(indices))
	}
	if opts.includeMetadata {
		result["metadata"] = doc.metadata()
	}
	return result
}

// pdfParsePageRange mirrors the upstream "all" / "5" / "1-10" / "1,3,5" syntax.
func pdfParsePageRange(spec string, total, max int) (indices []int, truncated bool, requested int, problem string) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	if spec == "" || spec == "all" {
		limited := total
		if limited > max {
			limited = max
		}
		indices = make([]int, limited)
		for i := range indices {
			indices[i] = i
		}
		return indices, total > max, total, ""
	}
	if !strings.Contains(spec, "-") && !strings.Contains(spec, ",") {
		value, err := strconv.Atoi(spec)
		if err != nil {
			return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
		}
		if value < 1 || value > total {
			return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", value, total)
		}
		return []int{value - 1}, false, 1, ""
	}
	if strings.Contains(spec, "-") && !strings.Contains(spec, ",") {
		parts := strings.SplitN(spec, "-", 2)
		start, errStart := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, errEnd := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errStart != nil || errEnd != nil {
			return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
		}
		if start > end {
			return nil, false, 0, fmt.Sprintf("Invalid page range: %s. Start must be less than end.", spec)
		}
		if start < 1 {
			return nil, false, 0, fmt.Sprintf("Page numbers start at 1, got %d.", start)
		}
		if end > total {
			return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", end, total)
		}
		requested := end - start + 1
		limitedEnd := end
		if limitedEnd > start-1+max {
			limitedEnd = start - 1 + max
		}
		indices = make([]int, 0, limitedEnd-start+1)
		for page := start; page <= limitedEnd; page++ {
			indices = append(indices, page-1)
		}
		return indices, requested > max, requested, ""
	}
	if strings.Contains(spec, ",") {
		values := strings.Split(spec, ",")
		numbers := make([]int, 0, len(values))
		for _, value := range values {
			number, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
			}
			if number < 1 || number > total {
				return nil, false, 0, fmt.Sprintf("Page %d out of range. PDF has %d pages.", number, total)
			}
			numbers = append(numbers, number)
		}
		requested = len(numbers)
		if requested > max {
			numbers = numbers[:max]
		}
		indices = make([]int, 0, len(numbers))
		for _, number := range numbers {
			indices = append(indices, number-1)
		}
		return indices, requested > max, requested, ""
	}
	return nil, false, 0, fmt.Sprintf("Invalid page format: '%s'. Use 'all', '5', '1-10', or '1,3,5'.", spec)
}
