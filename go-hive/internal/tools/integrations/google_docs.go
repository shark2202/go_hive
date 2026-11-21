// Google Docs actions for batch 5, lane 3 (upstream google_docs_tool,
// 11 actions). See google_workspace.go for the registry and shared helpers.
package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// batch5Lane3URLPattern mirrors the upstream image-URI regex.
var batch5Lane3URLPattern = regexp.MustCompile(
	`(?i)^https?://` +
		`(?:(?:[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?\.)+[A-Z]{2,6}\.?|` +
		`localhost|` +
		`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})` +
		`(?::\d+)?` +
		`(?:/?|[/?]\S+)$`)

func batch5Lane3DocsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"google_docs_create_document":  batch5Lane3DocsCreateDocument,
		"google_docs_get_document":     batch5Lane3DocsGetDocument,
		"google_docs_insert_text":      batch5Lane3DocsInsertText,
		"google_docs_replace_all_text": batch5Lane3DocsReplaceAllText,
		"google_docs_insert_image":     batch5Lane3DocsInsertImage,
		"google_docs_format_text":      batch5Lane3DocsFormatText,
		"google_docs_batch_update":     batch5Lane3DocsBatchUpdate,
		"google_docs_create_list":      batch5Lane3DocsCreateList,
		"google_docs_add_comment":      batch5Lane3DocsAddComment,
		"google_docs_list_comments":    batch5Lane3DocsListComments,
		"google_docs_export_content":   batch5Lane3DocsExportContent,
	}
}

func batch5Lane3DocsAuthError() map[string]any {
	return map[string]any{
		"error": "Google Docs credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable or configure 'google' via credential store",
	}
}

// batch5Lane3DocsResponse mirrors _GoogleDocsClient._handle_response.
func batch5Lane3DocsResponse(status int, raw []byte) map[string]any {
	switch {
	case status == http.StatusUnauthorized:
		return map[string]any{"error": "Invalid or expired Google access token"}
	case status == http.StatusForbidden:
		return map[string]any{
			"error": "Insufficient permissions. Check your Google API scopes. Required scopes: https://www.googleapis.com/auth/documents",
		}
	case status == http.StatusNotFound:
		return map[string]any{"error": "Document not found"}
	case status == http.StatusTooManyRequests:
		return map[string]any{"error": "Google API rate limit exceeded. Try again later."}
	case status >= 400:
		return map[string]any{
			"error": fmt.Sprintf("Google Docs API error (HTTP %d): %s", status, batch5Lane3ErrorDetail(raw)),
		}
	}
	payload, ok := jsonObject(raw)
	if !ok {
		return map[string]any{"error": "Google Docs API error: invalid JSON response"}
	}
	return payload
}

func batch5Lane3DocsCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3DocsModule, method, path, query, body)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	return batch5Lane3DocsResponse(status, raw), nil
}

// batch5Lane3DriveCall is the same request against the Drive host used for
// comments and export.
func batch5Lane3DriveCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (map[string]any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3DriveModule, method, path, query, body)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	return batch5Lane3DocsResponse(status, raw), nil
}

// batch5Lane3DocsBatch posts a request list for a document id.
func batch5Lane3DocsBatch(ctx context.Context, rt *Runtime, documentID string, requests any) (any, error) {
	return batch5Lane3DocsCall(ctx, rt, http.MethodPost,
		batch5Lane3DocsPrefix+"/"+batch5Lane3Segments(documentID)+":batchUpdate",
		nil, map[string]any{"requests": requests})
}

// batch5Lane3ValidateImageURI mirrors _validate_image_uri.
func batch5Lane3ValidateImageURI(uri string) map[string]any {
	if strings.TrimSpace(uri) == "" {
		return map[string]any{"error": "Image URI cannot be empty"}
	}
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Scheme == "" {
		return map[string]any{"error": "Invalid image URI: missing scheme. Use https:// or http://"}
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return map[string]any{
			"error": fmt.Sprintf("Invalid image URI scheme: '%s'. Only https, http are allowed.", parsed.Scheme),
		}
	}
	if !batch5Lane3URLPattern.MatchString(uri) {
		return map[string]any{"error": fmt.Sprintf("Invalid image URI format: '%s'", uri)}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return map[string]any{"error": "Invalid image URI: missing domain"}
	}
	return nil
}

// batch5Lane3DocumentEndIndex mirrors _get_document_end_index.
func batch5Lane3DocumentEndIndex(doc map[string]any) int {
	body := asMap(doc["body"])
	content := asSlice(body["content"])
	if len(content) == 0 {
		return 1
	}
	endIndex := 1
	if value, present := optionalInt(asMap(content[len(content)-1]), "endIndex"); present {
		endIndex = value
	}
	return endIndex - 1
}

func batch5Lane3DocsCreateDocument(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch5Lane3DocsCall(ctx, rt, http.MethodPost, batch5Lane3DocsPrefix, nil,
		map[string]any{"title": stringArg(input, "title")})
	if err != nil {
		return nil, err
	}
	payload := asMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	documentID := asString(payload["documentId"])
	return map[string]any{
		"document_id":  documentID,
		"title":        payload["title"],
		"document_url": "https://docs.google.com/document/d/" + documentID + "/edit",
	}, nil
}

func batch5Lane3DocsGetDocument(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return batch5Lane3DocsCall(ctx, rt, http.MethodGet,
		batch5Lane3DocsPrefix+"/"+batch5Lane3Segments(stringArg(input, "document_id")), nil, nil)
}

func batch5Lane3DocsInsertText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	documentID := stringArg(input, "document_id")
	location := map[string]any{}
	index, present := optionalInt(input, "index")
	if present {
		location["index"] = index
	} else {
		docResult, err := batch5Lane3DocsCall(ctx, rt, http.MethodGet,
			batch5Lane3DocsPrefix+"/"+batch5Lane3Segments(documentID), nil, nil)
		if err != nil {
			return nil, err
		}
		doc := asMap(docResult)
		if _, failed := doc["error"]; failed {
			return doc, nil
		}
		location["index"] = batch5Lane3DocumentEndIndex(doc)
	}
	return batch5Lane3DocsBatch(ctx, rt, documentID, []any{
		map[string]any{
			"insertText": map[string]any{
				"location": location,
				"text":     stringArg(input, "text"),
			},
		},
	})
}

func batch5Lane3DocsReplaceAllText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	documentID := stringArg(input, "document_id")
	findText := stringArg(input, "find_text")
	if findText == "" {
		return map[string]any{"error": "find_text cannot be empty"}, nil
	}
	result, err := batch5Lane3DocsBatch(ctx, rt, documentID, []any{
		map[string]any{
			"replaceAllText": map[string]any{
				"containsText": map[string]any{
					"text":      findText,
					"matchCase": boolArg(input, "match_case", true),
				},
				"replaceText": stringArg(input, "replace_text"),
			},
		},
	})
	if err != nil {
		return nil, err
	}
	payload := asMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	occurrences := 0
	for _, rawReply := range asSlice(payload["replies"]) {
		replaceReply := asMap(asMap(rawReply)["replaceAllText"])
		if value, present := optionalInt(replaceReply, "occurrencesChanged"); present {
			occurrences += value
		}
	}
	return map[string]any{
		"document_id":          documentID,
		"find_text":            findText,
		"replace_text":         stringArg(input, "replace_text"),
		"occurrences_replaced": occurrences,
	}, nil
}

func batch5Lane3DocsInsertImage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	imageURI := stringArg(input, "image_uri")
	if failure := batch5Lane3ValidateImageURI(imageURI); failure != nil {
		return failure, nil
	}
	request := map[string]any{
		"insertInlineImage": map[string]any{
			"location": map[string]any{"index": intArg(input, "index", 1)},
			"uri":      imageURI,
		},
	}
	width, hasWidth := batch5Lane3OptionalFloat(input, "width_pt")
	height, hasHeight := batch5Lane3OptionalFloat(input, "height_pt")
	if hasWidth || hasHeight {
		objectSize := map[string]any{}
		if hasWidth {
			objectSize["width"] = map[string]any{"magnitude": width, "unit": "PT"}
		}
		if hasHeight {
			objectSize["height"] = map[string]any{"magnitude": height, "unit": "PT"}
		}
		request["insertInlineImage"].(map[string]any)["objectSize"] = objectSize
	}
	return batch5Lane3DocsBatch(ctx, rt, stringArg(input, "document_id"), []any{request})
}

func batch5Lane3DocsFormatText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	textStyle := map[string]any{}
	fields := make([]string, 0, 5)
	if value, present := notionOptionalBool(input, "bold"); present {
		textStyle["bold"] = value
		fields = append(fields, "bold")
	}
	if value, present := notionOptionalBool(input, "italic"); present {
		textStyle["italic"] = value
		fields = append(fields, "italic")
	}
	if value, present := notionOptionalBool(input, "underline"); present {
		textStyle["underline"] = value
		fields = append(fields, "underline")
	}
	if value, present := batch5Lane3OptionalFloat(input, "font_size_pt"); present {
		textStyle["fontSize"] = map[string]any{"magnitude": value, "unit": "PT"}
		fields = append(fields, "fontSize")
	}
	red, hasRed := batch5Lane3OptionalFloat(input, "foreground_color_red")
	green, hasGreen := batch5Lane3OptionalFloat(input, "foreground_color_green")
	blue, hasBlue := batch5Lane3OptionalFloat(input, "foreground_color_blue")
	if hasRed || hasGreen || hasBlue {
		textStyle["foregroundColor"] = map[string]any{
			"color": map[string]any{
				"rgbColor": map[string]any{"red": red, "green": green, "blue": blue},
			},
		}
		fields = append(fields, "foregroundColor")
	}
	if len(fields) == 0 {
		return map[string]any{"error": "No formatting options specified"}, nil
	}
	return batch5Lane3DocsBatch(ctx, rt, stringArg(input, "document_id"), []any{
		map[string]any{
			"updateTextStyle": map[string]any{
				"range": map[string]any{
					"startIndex": intArg(input, "start_index", 1),
					"endIndex":   intArg(input, "end_index", 1),
				},
				"textStyle": textStyle,
				"fields":    strings.Join(fields, ","),
			},
		},
	})
}

func batch5Lane3DocsBatchUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	var requests any
	if err := json.Unmarshal([]byte(stringArg(input, "requests_json")), &requests); err != nil {
		return map[string]any{"error": "Invalid JSON: " + err.Error()}, nil
	}
	if _, ok := requests.([]any); !ok {
		return map[string]any{"error": "requests_json must be a JSON array of request objects"}, nil
	}
	return batch5Lane3DocsBatch(ctx, rt, stringArg(input, "document_id"), requests)
}

func batch5Lane3DocsCreateList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	preset := "BULLET_DISC_CIRCLE_SQUARE"
	if strings.ToLower(stringArg(input, "list_type")) == "numbered" {
		preset = "NUMBERED_DECIMAL_ALPHA_ROMAN"
	}
	return batch5Lane3DocsBatch(ctx, rt, stringArg(input, "document_id"), []any{
		map[string]any{
			"createParagraphBullets": map[string]any{
				"range": map[string]any{
					"startIndex": intArg(input, "start_index", 1),
					"endIndex":   intArg(input, "end_index", 1),
				},
				"bulletPreset": preset,
			},
		},
	})
}

func batch5Lane3DocsAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"content": stringArg(input, "content")}
	if quoted := stringArg(input, "quoted_text"); quoted != "" {
		body["quotedFileContent"] = map[string]any{"value": quoted}
	}
	return batch5Lane3DriveCall(ctx, rt, http.MethodPost,
		batch5Lane3DrivePrefix+"/files/"+batch5Lane3Segments(stringArg(input, "document_id"))+"/comments",
		map[string]string{"fields": "*"}, body)
}

func batch5Lane3DocsListComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	documentID := stringArg(input, "document_id")
	pageSize := clampInt(intArg(input, "page_size", 20), 1, 100)
	query := map[string]string{
		"fields":         "comments(*),nextPageToken",
		"pageSize":       fmt.Sprint(pageSize),
		"includeDeleted": fmt.Sprint(boolArg(input, "include_deleted", false)),
	}
	if token := stringArg(input, "page_token"); token != "" {
		query["pageToken"] = token
	}
	result, err := batch5Lane3DriveCall(ctx, rt, http.MethodGet,
		batch5Lane3DrivePrefix+"/files/"+batch5Lane3Segments(documentID)+"/comments", query, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	comments := asSlice(result["comments"])
	if comments == nil {
		comments = []any{}
	}
	return map[string]any{
		"document_id":     documentID,
		"comments":        comments,
		"next_page_token": result["nextPageToken"],
	}, nil
}

func batch5Lane3DocsExportContent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	documentID := stringArg(input, "document_id")
	mimeTypes := map[string]string{
		"pdf":  "application/pdf",
		"docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"txt":  "text/plain",
		"html": "text/html",
		"odt":  "application/vnd.oasis.opendocument.text",
		"rtf":  "application/rtf",
		"epub": "application/epub+zip",
	}
	mimeType := mimeTypes[strings.ToLower(stringArg(input, "format"))]
	if mimeType == "" {
		mimeType = "application/pdf"
	}
	if !batch5Lane3AccountCredential(rt, batch5Lane3DocsModule) {
		return batch5Lane3DocsAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3DriveModule, http.MethodGet,
		batch5Lane3DrivePrefix+"/files/"+batch5Lane3Segments(documentID)+"/export",
		map[string]string{"mimeType": mimeType}, nil)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	if status == http.StatusOK {
		return map[string]any{
			"document_id":    documentID,
			"mime_type":      mimeType,
			"content_base64": base64.StdEncoding.EncodeToString(raw),
			"size_bytes":     len(raw),
		}, nil
	}
	return batch5Lane3DocsResponse(status, raw), nil
}
