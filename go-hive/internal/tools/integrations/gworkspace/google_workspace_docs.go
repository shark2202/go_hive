// Google Docs catalog entry for the batch-5 lane-3 Google Workspace
// registry (split from google_workspace.go).

package gworkspace

var docsTool = &Tool{
	ID:          docsModule,
	Name:        "google_docs",
	DisplayName: "Google Docs",
	Summary:     "Google Docs integration (11 actions).",
	Actions: []Action{
		{
			Name:     "google_docs_create_document",
			Summary:  "Create a new blank Google Docs document with a specified title.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"title"},
			Params: map[string]any{
				"title":   param("string"),
				"account": paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_get_document",
			Summary:  "Retrieve the full structural content, metadata, and elements of a document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_insert_text",
			Summary:  "Insert text at a specific index or at the end of the document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "text"},
			Params: map[string]any{
				"document_id": param("string"),
				"text":        param("string"),
				"index":       param("integer"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_replace_all_text",
			Summary:  "Global find-and-replace (ideal for populating templates with dynamic data).",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "find_text", "replace_text"},
			Params: map[string]any{
				"document_id":  param("string"),
				"find_text":    param("string"),
				"replace_text": param("string"),
				"match_case":   paramDefault("boolean", true),
				"account":      paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_insert_image",
			Summary:  "Insert an image into the document body via URI.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "image_uri", "index"},
			Params: map[string]any{
				"document_id": param("string"),
				"image_uri":   param("string"),
				"index":       param("integer"),
				"width_pt":    param("number"),
				"height_pt":   param("number"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_format_text",
			Summary:  "Apply styling (bold, italic, font size, colors) to specific text ranges.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "start_index", "end_index"},
			Params: map[string]any{
				"document_id":            param("string"),
				"start_index":            param("integer"),
				"end_index":              param("integer"),
				"bold":                   param("boolean"),
				"italic":                 param("boolean"),
				"underline":              param("boolean"),
				"font_size_pt":           param("number"),
				"foreground_color_red":   param("number"),
				"foreground_color_green": param("number"),
				"foreground_color_blue":  param("number"),
				"account":                paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_batch_update",
			Summary:  "Execute multiple requests (inserts, deletes, formatting) in a single atomic operation.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "requests_json"},
			Params: map[string]any{
				"document_id":   param("string"),
				"requests_json": param("string"),
				"account":       paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_create_list",
			Summary:  "Create or modify bulleted and numbered lists within the document.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "start_index", "end_index"},
			Params: map[string]any{
				"document_id": param("string"),
				"start_index": param("integer"),
				"end_index":   param("integer"),
				"list_type":   paramDefault("string", "bullet"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_add_comment",
			Summary:  "Create a comment or anchor a discussion thread to a specific text segment.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id", "content"},
			Params: map[string]any{
				"document_id": param("string"),
				"content":     param("string"),
				"quoted_text": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_list_comments",
			Summary:  "Retrieve comments for a document, with pagination support.",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id":     param("string"),
				"page_size":       paramDefault("integer", 20),
				"page_token":      param("string"),
				"include_deleted": paramDefault("boolean", false),
				"account":         paramDefault("string", ""),
			},
		},
		{
			Name:     "google_docs_export_content",
			Summary:  "Export the document to different formats (PDF, DOCX, TXT).",
			Source:   "google_docs_tool/google_docs_tool.py",
			Required: []string{"document_id"},
			Params: map[string]any{
				"document_id": param("string"),
				"format":      paramDefault("string", "pdf"),
				"account":     paramDefault("string", ""),
			},
		},
	},
}
