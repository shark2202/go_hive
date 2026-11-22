// Google Docs actions for batch 5, lane 3 (upstream google_docs_tool,
// 11 actions). See google_workspace.go for the registry and shared helpers.
package gworkspace

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

// urlpattern mirrors the upstream image-URI regex.
var urlpattern = regexp.MustCompile(
	`(?i)^https?://` +
		`(?:(?:[A-Z0-9](?:[A-Z0-9-]{0,61}[A-Z0-9])?\.)+[A-Z]{2,6}\.?|` +
		`localhost|` +
		`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})` +
		`(?::\d+)?` +
		`(?:/?|[/?]\S+)$`)

func docsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"google_docs_create_document":  docsCreateDocument,
		"google_docs_get_document":     docsGetDocument,
		"google_docs_insert_text":      docsInsertText,
		"google_docs_replace_all_text": docsReplaceAllText,
		"google_docs_insert_image":     docsInsertImage,
		"google_docs_format_text":      docsFormatText,
		"google_docs_batch_update":     docsBatchUpdate,
		"google_docs_create_list":      docsCreateList,
		"google_docs_add_comment":      docsAddComment,
		"google_docs_list_comments":    docsListComments,
		"google_docs_export_content":   docsExportContent,
	}
}

func docsAuthError() map[string]any {
	return map[string]any{
		"error": "Google Docs credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable or configure 'google' via credential store",
	}
}

// docsResponse mirrors _GoogleDocsClient._handle_response.
func docsResponse(status int, raw []byte) map[string]any {
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
			"error": fmt.Sprintf("Google Docs API error (HTTP %d): %s", status, errorDetail(raw)),
		}
	}
	payload, ok := jsonObject(raw)
	if !ok {
		return map[string]any{"error": "Google Docs API error: invalid JSON response"}
	}
	return payload
}

func docsCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	status, raw, err := do(ctx, rt, docsModule, method, path, query, body)
	if err != nil {
		return transportError(ctx, err), nil
	}
	return docsResponse(status, raw), nil
}

// driveCall is the same request against the Drive host used for
// comments and export.
func driveCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (map[string]any, error) {
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	status, raw, err := do(ctx, rt, driveModule, method, path, query, body)
	if err != nil {
		return transportError(ctx, err), nil
	}
	return docsResponse(status, raw), nil
}

// docsBatch posts a request list for a document id.
func docsBatch(ctx context.Context, rt *Runtime, documentID string, requests any) (any, error) {
	return docsCall(ctx, rt, http.MethodPost,
		docsPrefix+"/"+segments(documentID)+":batchUpdate",
		nil, map[string]any{"requests": requests})
}

// validateImageURI mirrors _validate_image_uri.
func validateImageURI(uri string) map[string]any {
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
	if !urlpattern.MatchString(uri) {
		return map[string]any{"error": fmt.Sprintf("Invalid image URI format: '%s'", uri)}
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return map[string]any{"error": "Invalid image URI: missing domain"}
	}
	return nil
}

// documentEndIndex mirrors _get_document_end_index.
func documentEndIndex(doc map[string]any) int {
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

func docsCreateDocument(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := docsCall(ctx, rt, http.MethodPost, docsPrefix, nil,
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

func docsGetDocument(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return docsCall(ctx, rt, http.MethodGet,
		docsPrefix+"/"+segments(stringArg(input, "document_id")), nil, nil)
}

func docsInsertText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	documentID := stringArg(input, "document_id")
	location := map[string]any{}
	index, present := optionalInt(input, "index")
	if present {
		location["index"] = index
	} else {
		docResult, err := docsCall(ctx, rt, http.MethodGet,
			docsPrefix+"/"+segments(documentID), nil, nil)
		if err != nil {
			return nil, err
		}
		doc := asMap(docResult)
		if _, failed := doc["error"]; failed {
			return doc, nil
		}
		location["index"] = documentEndIndex(doc)
	}
	return docsBatch(ctx, rt, documentID, []any{
		map[string]any{
			"insertText": map[string]any{
				"location": location,
				"text":     stringArg(input, "text"),
			},
		},
	})
}

func docsReplaceAllText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	documentID := stringArg(input, "document_id")
	findText := stringArg(input, "find_text")
	if findText == "" {
		return map[string]any{"error": "find_text cannot be empty"}, nil
	}
	result, err := docsBatch(ctx, rt, documentID, []any{
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

func docsInsertImage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	imageURI := stringArg(input, "image_uri")
	if failure := validateImageURI(imageURI); failure != nil {
		return failure, nil
	}
	request := map[string]any{
		"insertInlineImage": map[string]any{
			"location": map[string]any{"index": intArg(input, "index", 1)},
			"uri":      imageURI,
		},
	}
	width, hasWidth := optionalFloat(input, "width_pt")
	height, hasHeight := optionalFloat(input, "height_pt")
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
	return docsBatch(ctx, rt, stringArg(input, "document_id"), []any{request})
}

func docsFormatText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	if value, present := optionalFloat(input, "font_size_pt"); present {
		textStyle["fontSize"] = map[string]any{"magnitude": value, "unit": "PT"}
		fields = append(fields, "fontSize")
	}
	red, hasRed := optionalFloat(input, "foreground_color_red")
	green, hasGreen := optionalFloat(input, "foreground_color_green")
	blue, hasBlue := optionalFloat(input, "foreground_color_blue")
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
	return docsBatch(ctx, rt, stringArg(input, "document_id"), []any{
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

func docsBatchUpdate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	var requests any
	if err := json.Unmarshal([]byte(stringArg(input, "requests_json")), &requests); err != nil {
		return map[string]any{"error": "Invalid JSON: " + err.Error()}, nil
	}
	if _, ok := requests.([]any); !ok {
		return map[string]any{"error": "requests_json must be a JSON array of request objects"}, nil
	}
	return docsBatch(ctx, rt, stringArg(input, "document_id"), requests)
}

func docsCreateList(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	preset := "BULLET_DISC_CIRCLE_SQUARE"
	if strings.ToLower(stringArg(input, "list_type")) == "numbered" {
		preset = "NUMBERED_DECIMAL_ALPHA_ROMAN"
	}
	return docsBatch(ctx, rt, stringArg(input, "document_id"), []any{
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

func docsAddComment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	body := map[string]any{"content": stringArg(input, "content")}
	if quoted := stringArg(input, "quoted_text"); quoted != "" {
		body["quotedFileContent"] = map[string]any{"value": quoted}
	}
	return driveCall(ctx, rt, http.MethodPost,
		drivePrefix+"/files/"+segments(stringArg(input, "document_id"))+"/comments",
		map[string]string{"fields": "*"}, body)
}

func docsListComments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	result, err := driveCall(ctx, rt, http.MethodGet,
		drivePrefix+"/files/"+segments(documentID)+"/comments", query, nil)
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

func docsExportContent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
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
	if !accountCredential(rt, docsModule) {
		return docsAuthError(), nil
	}
	status, raw, err := do(ctx, rt, driveModule, http.MethodGet,
		drivePrefix+"/files/"+segments(documentID)+"/export",
		map[string]string{"mimeType": mimeType}, nil)
	if err != nil {
		return transportError(ctx, err), nil
	}
	if status == http.StatusOK {
		return map[string]any{
			"document_id":    documentID,
			"mime_type":      mimeType,
			"content_base64": base64.StdEncoding.EncodeToString(raw),
			"size_bytes":     len(raw),
		}, nil
	}
	return docsResponse(status, raw), nil
}
