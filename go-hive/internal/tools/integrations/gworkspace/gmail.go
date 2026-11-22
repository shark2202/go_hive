// Gmail actions for batch 5, lane 3 (upstream gmail_tool, 10 actions).
// See google_workspace.go for the registry and shared helpers.
package gworkspace

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

func gmailImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gmail_list_messages":         gmailListMessages,
		"gmail_get_message":           gmailGetMessage,
		"gmail_trash_message":         gmailTrashMessage,
		"gmail_modify_message":        gmailModifyMessage,
		"gmail_batch_modify_messages": gmailBatchModifyMessages,
		"gmail_batch_get_messages":    gmailBatchGetMessages,
		"gmail_create_draft":          gmailCreateDraft,
		"gmail_get_signature":         gmailGetSignature,
		"gmail_list_labels":           gmailListLabels,
		"gmail_create_label":          gmailCreateLabel,
	}
}

func gmailAuthError() map[string]any {
	return map[string]any{
		"error": "Gmail credentials not configured",
		"help":  "Connect Gmail via hive.adenhq.com",
	}
}

// gmailResponse mirrors _handle_error plus the caller-side JSON read.
func gmailResponse(status int, raw []byte) map[string]any {
	switch status {
	case http.StatusOK:
		payload, ok := jsonObject(raw)
		if !ok {
			return map[string]any{"error": "Gmail API error: invalid JSON response"}
		}
		return payload
	case http.StatusNoContent:
		return map[string]any{}
	case http.StatusUnauthorized:
		return map[string]any{
			"error": "Gmail token expired or invalid",
			"help":  "Re-authorize via hive.adenhq.com",
		}
	case http.StatusNotFound:
		return map[string]any{"error": "Message not found"}
	default:
		return map[string]any{
			"error": fmt.Sprintf("Gmail API error (HTTP %d): %s", status, string(raw)),
		}
	}
}

func gmailCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (map[string]any, error) {
	if !accountCredential(rt, gmailModule) {
		return gmailAuthError(), nil
	}
	status, raw, err := do(ctx, rt, gmailModule, method, path, query, body)
	if err != nil {
		return transportError(ctx, err), nil
	}
	return gmailResponse(status, raw), nil
}

// gmailPathParam rejects ids that would break out of a path segment.
func gmailPathParam(value, name string) (string, map[string]any) {
	if strings.Contains(value, "/") || strings.Contains(value, "..") {
		return "", map[string]any{
			"error": fmt.Sprintf("Invalid %s: cannot contain '/' or '..'", name),
		}
	}
	return value, nil
}

// gmailEnsureList mirrors _ensure_list: a bare string becomes a
// single-element list instead of being rejected.
func gmailEnsureList(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

// gmailPyList renders a list argument like Python's repr, used only
// in the no-labels error message.
func gmailPyList(raw any, values []string) string {
	if raw == nil {
		return "None"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// gmailHeaders flattens the headers upstream keeps.
func gmailHeaders(headers any) map[string]string {
	out := map[string]string{}
	for _, rawHeader := range asSlice(headers) {
		header := asMap(rawHeader)
		name := strings.ToLower(asString(header["name"]))
		switch name {
		case "subject", "from", "to", "date", "cc":
			out[name] = asString(header["value"])
		}
	}
	return out
}

// gmailHeaderValue reads a header case-insensitively.
func gmailHeaderValue(headers any, name string) string {
	for _, rawHeader := range asSlice(headers) {
		header := asMap(rawHeader)
		if strings.EqualFold(asString(header["name"]), name) {
			return asString(header["value"])
		}
	}
	return ""
}

// gmailDecode decodes Gmail's padded urlsafe base64 payloads.
func gmailDecode(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	if padding := len(text) % 4; padding != 0 {
		text += strings.Repeat("=", 4-padding)
	}
	decoded, err := base64.URLEncoding.DecodeString(text)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// gmailBody extracts a plain-text body the way get_message does:
// direct payload body first, then top-level text/plain, then text/html.
func gmailBody(payload map[string]any) string {
	if text, ok := gmailDecode(asString(asMap(payload["body"])["data"])); ok {
		return text
	}
	parts := asSlice(payload["parts"])
	for _, mimeType := range []string{"text/plain", "text/html"} {
		for _, rawPart := range parts {
			part := asMap(rawPart)
			if asString(part["mimeType"]) != mimeType {
				continue
			}
			if text, ok := gmailDecode(asString(asMap(part["body"])["data"])); ok {
				return text
			}
		}
	}
	return ""
}

// gmailBodyType walks the MIME tree for one content type, like the
// recursive helper used when quoting a reply.
func gmailBodyType(part map[string]any, mimeType string) string {
	if asString(part["mimeType"]) == mimeType {
		if text, ok := gmailDecode(asString(asMap(part["body"])["data"])); ok {
			return text
		}
	}
	for _, rawChild := range asSlice(part["parts"]) {
		if text := gmailBodyType(asMap(rawChild), mimeType); text != "" {
			return text
		}
	}
	return ""
}

// gmailMessageShape builds the parsed message dict shared by
// gmail_get_message and gmail_batch_get_messages.
func gmailMessageShape(data map[string]any, full bool) map[string]any {
	result := map[string]any{
		"id":       data["id"],
		"threadId": data["threadId"],
		"labels":   data["labelIds"],
		"snippet":  data["snippet"],
	}
	if result["labels"] == nil {
		result["labels"] = []any{}
	}
	if result["snippet"] == nil {
		result["snippet"] = ""
	}
	payload := asMap(data["payload"])
	if headers := gmailHeaders(payload["headers"]); len(headers) > 0 {
		for name, value := range headers {
			result[name] = value
		}
	}
	if full {
		if body := gmailBody(payload); body != "" {
			result["body"] = body
		}
	}
	return result
}

// gmailRawMessage builds a minimal HTML MIME message and returns
// its urlsafe-base64 form.
func gmailRawMessage(to, subject, htmlBody, inReplyTo string) string {
	var builder strings.Builder
	builder.WriteString("To: " + to + "\r\n")
	builder.WriteString("Subject: " + subject + "\r\n")
	if inReplyTo != "" {
		builder.WriteString("In-Reply-To: " + inReplyTo + "\r\n")
		builder.WriteString("References: " + inReplyTo + "\r\n")
	}
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
	builder.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(htmlBody)
	return base64.URLEncoding.EncodeToString([]byte(builder.String()))
}

func gmailListMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxResults := clampInt(intArg(input, "max_results", 100), 1, 500)
	query := map[string]string{
		"q":          stringArg(input, "query"),
		"maxResults": strconv.Itoa(maxResults),
	}
	if pageToken := stringArg(input, "page_token"); pageToken != "" {
		query["pageToken"] = pageToken
	}
	result, err := gmailCall(ctx, rt, http.MethodGet, gmailPrefix+"/messages", query, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	messages := asSlice(result["messages"])
	if messages == nil {
		messages = []any{}
	}
	return map[string]any{
		"messages":             messages,
		"result_size_estimate": result["resultSizeEstimate"],
		"next_page_token":      result["nextPageToken"],
	}, nil
}

func gmailGetMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := gmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	format := stringArg(input, "format")
	result, err := gmailCall(ctx, rt, http.MethodGet,
		gmailPrefix+"/messages/"+segments(messageID),
		map[string]string{"format": format}, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return gmailMessageShape(result, format == "full"), nil
}

func gmailTrashMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := gmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	result, err := gmailCall(ctx, rt, http.MethodPost,
		gmailPrefix+"/messages/"+segments(messageID)+"/trash", nil, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return map[string]any{"success": true, "message_id": messageID}, nil
}

func gmailModifyMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := gmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	if !accountCredential(rt, gmailModule) {
		return gmailAuthError(), nil
	}
	addLabels := gmailEnsureList(input["add_labels"])
	removeLabels := gmailEnsureList(input["remove_labels"])
	if len(addLabels) == 0 && len(removeLabels) == 0 {
		return map[string]any{"error": fmt.Sprintf(
			"At least one of add_labels or remove_labels is required. "+
				"Received add_labels=%s, remove_labels=%s. "+
				`Pass label IDs like add_labels=["STARRED"] or remove_labels=["INBOX"].`,
			gmailPyList(input["add_labels"], addLabels),
			gmailPyList(input["remove_labels"], removeLabels),
		)}, nil
	}
	body := map[string]any{}
	if len(addLabels) > 0 {
		body["addLabelIds"] = addLabels
	}
	if len(removeLabels) > 0 {
		body["removeLabelIds"] = removeLabels
	}
	result, err := gmailCall(ctx, rt, http.MethodPost,
		gmailPrefix+"/messages/"+segments(messageID)+"/modify", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labels := asSlice(result["labelIds"])
	if labels == nil {
		labels = []any{}
	}
	return map[string]any{"success": true, "message_id": messageID, "labels": labels}, nil
}

func gmailBatchModifyMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageIDs := gmailEnsureList(input["message_ids"])
	if len(messageIDs) == 0 {
		return map[string]any{"error": "message_ids list is required and must not be empty"}, nil
	}
	if !accountCredential(rt, gmailModule) {
		return gmailAuthError(), nil
	}
	addLabels := gmailEnsureList(input["add_labels"])
	removeLabels := gmailEnsureList(input["remove_labels"])
	if len(addLabels) == 0 && len(removeLabels) == 0 {
		return map[string]any{"error": fmt.Sprintf(
			"At least one of add_labels or remove_labels is required. "+
				"Received add_labels=%s, remove_labels=%s. "+
				`Pass label IDs like add_labels=["STARRED"] or remove_labels=["INBOX"].`,
			gmailPyList(input["add_labels"], addLabels),
			gmailPyList(input["remove_labels"], removeLabels),
		)}, nil
	}
	body := map[string]any{"ids": messageIDs}
	if len(addLabels) > 0 {
		body["addLabelIds"] = addLabels
	}
	if len(removeLabels) > 0 {
		body["removeLabelIds"] = removeLabels
	}
	result, err := gmailCall(ctx, rt, http.MethodPost,
		gmailPrefix+"/messages/batchModify", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return map[string]any{"success": true, "count": len(messageIDs)}, nil
}

func gmailBatchGetMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageIDs := gmailEnsureList(input["message_ids"])
	if len(messageIDs) == 0 {
		return map[string]any{"error": "message_ids list is required and must not be empty"}, nil
	}
	if len(messageIDs) > 50 {
		return map[string]any{"error": "Maximum 50 message IDs per call"}, nil
	}
	format := stringArg(input, "format")
	messages := make([]any, 0, len(messageIDs))
	errors := make([]any, 0)
	for _, rawID := range messageIDs {
		messageID, failure := gmailPathParam(rawID, "message_id")
		if failure != nil {
			errors = append(errors, map[string]any{"message_id": rawID, "error": failure["error"]})
			continue
		}
		result, err := gmailCall(ctx, rt, http.MethodGet,
			gmailPrefix+"/messages/"+segments(messageID),
			map[string]string{"format": format}, nil)
		if err != nil {
			return nil, err
		}
		if _, failed := result["error"]; failed {
			entry := map[string]any{"message_id": messageID}
			for key, value := range result {
				entry[key] = value
			}
			errors = append(errors, entry)
			continue
		}
		messages = append(messages, gmailMessageShape(result, format == "full"))
	}
	return map[string]any{"messages": messages, "count": len(messages), "errors": errors}, nil
}

// gmailQuote builds the quoted-reply block upstream appends.
func gmailQuote(original map[string]any) string {
	payload := asMap(original["payload"])
	originalHTML := gmailBodyType(payload, "text/html")
	if originalHTML == "" {
		plain := gmailBodyType(payload, "text/plain")
		originalHTML = "<pre>" + html.EscapeString(plain) + "</pre>"
	}
	from := gmailHeaderValue(payload["headers"], "From")
	date := gmailHeaderValue(payload["headers"], "Date")
	return "<br><br>" +
		`<div class="gmail_quote">` +
		"<div>On " + date + ", " + from + " wrote:</div>" +
		`<blockquote style="margin:0 0 0 .8ex;border-left:1px #ccc solid;padding-left:1ex">` +
		originalHTML +
		"</blockquote>" +
		"</div>"
}

func gmailCreateDraft(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	htmlBody := stringArg(input, "html")
	if htmlBody == "" {
		return map[string]any{"error": "Email body (html) is required"}, nil
	}
	if !accountCredential(rt, gmailModule) {
		return gmailAuthError(), nil
	}
	to := stringArg(input, "to")
	subject := stringArg(input, "subject")
	signature := stringArg(input, "signature")
	threadID := ""
	inReplyTo := ""
	fullHTML := htmlBody

	if replyTo := stringArg(input, "reply_to_message_id"); replyTo != "" {
		replyToID, failure := gmailPathParam(replyTo, "reply_to_message_id")
		if failure != nil {
			return failure, nil
		}
		status, raw, err := do(ctx, rt, gmailModule, http.MethodGet,
			gmailPrefix+"/messages/"+segments(replyToID),
			map[string]string{"format": "full"}, nil)
		if err != nil {
			var valueErr *valueError
			if errors.As(err, &valueErr) {
				return valueErr.dict, nil
			}
			return map[string]any{"error": "Failed to fetch original message: " + err.Error()}, nil
		}
		original := gmailResponse(status, raw)
		if _, failed := original["error"]; failed {
			return original, nil
		}
		threadID = asString(original["threadId"])
		payload := asMap(original["payload"])
		inReplyTo = gmailHeaderValue(payload["headers"], "Message-ID")
		if inReplyTo == "" {
			inReplyTo = gmailHeaderValue(payload["headers"], "Message-Id")
		}
		originalSubject := gmailHeaderValue(payload["headers"], "Subject")
		originalFrom := gmailHeaderValue(payload["headers"], "From")
		if originalFrom != "" {
			to = originalFrom
		}
		if strings.HasPrefix(strings.ToLower(originalSubject), "re:") {
			subject = originalSubject
		} else {
			subject = "Re: " + originalSubject
		}
		signatureBlock := ""
		if signature != "" {
			signatureBlock = "<br><br>" + signature
		}
		fullHTML = htmlBody + signatureBlock + gmailQuote(original)
	} else {
		if strings.TrimSpace(to) == "" {
			return map[string]any{"error": "Recipient email (to) is required"}, nil
		}
		if strings.TrimSpace(subject) == "" {
			return map[string]any{"error": "Subject is required"}, nil
		}
		if signature != "" {
			fullHTML = htmlBody + "<br><br>" + signature
		}
	}

	messageBody := map[string]any{"raw": gmailRawMessage(to, subject, fullHTML, inReplyTo)}
	if threadID != "" {
		messageBody["threadId"] = threadID
	}
	result, err := gmailCall(ctx, rt, http.MethodPost,
		gmailPrefix+"/drafts", nil, map[string]any{"message": messageBody})
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	draft := map[string]any{
		"success":    true,
		"draft_id":   asString(result["id"]),
		"message_id": asString(asMap(result["message"])["id"]),
	}
	if threadID != "" {
		draft["thread_id"] = threadID
	}
	return draft, nil
}

func gmailGetSignature(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sendAsEmail := stringArg(input, "send_as_email")
	path := gmailPrefix + "/settings/sendAs"
	if sendAsEmail != "" {
		sanitized, failure := gmailPathParam(sendAsEmail, "send_as_email")
		if failure != nil {
			return failure, nil
		}
		path += "/" + segments(sanitized)
	}
	if !accountCredential(rt, gmailModule) {
		return gmailAuthError(), nil
	}
	status, raw, err := do(ctx, rt, gmailModule, http.MethodGet, path, nil, nil)
	if err != nil {
		return transportError(ctx, err), nil
	}
	if status == http.StatusForbidden {
		return map[string]any{
			"error": "Missing OAuth scope: gmail.settings.basic",
			"help": "Reading Gmail signatures requires the gmail.settings.basic scope. " +
				"Reconnect Gmail via hive.adenhq.com to grant it, or pass the " +
				"signature directly to gmail_create_draft.",
		}, nil
	}
	result := gmailResponse(status, raw)
	if _, failed := result["error"]; failed {
		return result, nil
	}
	var alias map[string]any
	if sendAsEmail != "" {
		alias = result
	} else {
		aliases := asSlice(result["sendAs"])
		for _, rawAlias := range aliases {
			candidate := asMap(rawAlias)
			if asBool(candidate["isPrimary"]) {
				alias = candidate
				break
			}
		}
		if alias == nil && len(aliases) > 0 {
			alias = asMap(aliases[0])
		}
		if alias == nil {
			return map[string]any{"error": "No send-as aliases found on this account"}, nil
		}
	}
	return map[string]any{
		"signature":     asString(alias["signature"]),
		"send_as_email": asString(alias["sendAsEmail"]),
		"display_name":  asString(alias["displayName"]),
		"is_primary":    asBool(alias["isPrimary"]),
	}, nil
}

func gmailListLabels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := gmailCall(ctx, rt, http.MethodGet, gmailPrefix+"/labels", nil, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labels := asSlice(result["labels"])
	if labels == nil {
		labels = []any{}
	}
	return map[string]any{"labels": labels}, nil
}

func gmailCreateLabel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	name := stringArg(input, "name")
	if strings.TrimSpace(name) == "" {
		return map[string]any{"error": "Label name is required"}, nil
	}
	body := map[string]any{
		"name":                  name,
		"labelListVisibility":   stringArg(input, "label_list_visibility"),
		"messageListVisibility": stringArg(input, "message_list_visibility"),
	}
	result, err := gmailCall(ctx, rt, http.MethodPost, gmailPrefix+"/labels", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labelType := asString(result["type"])
	if labelType == "" {
		labelType = "user"
	}
	return map[string]any{
		"success": true,
		"id":      asString(result["id"]),
		"name":    asString(result["name"]),
		"type":    labelType,
	}, nil
}
