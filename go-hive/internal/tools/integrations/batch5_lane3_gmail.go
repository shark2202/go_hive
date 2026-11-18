// Gmail actions for batch 5, lane 3 (upstream gmail_tool, 10 actions).
// See batch5_lane3.go for the registry and shared helpers.
package integrations

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

func batch5Lane3GmailImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gmail_list_messages":         batch5Lane3GmailListMessages,
		"gmail_get_message":           batch5Lane3GmailGetMessage,
		"gmail_trash_message":         batch5Lane3GmailTrashMessage,
		"gmail_modify_message":        batch5Lane3GmailModifyMessage,
		"gmail_batch_modify_messages": batch5Lane3GmailBatchModifyMessages,
		"gmail_batch_get_messages":    batch5Lane3GmailBatchGetMessages,
		"gmail_create_draft":          batch5Lane3GmailCreateDraft,
		"gmail_get_signature":         batch5Lane3GmailGetSignature,
		"gmail_list_labels":           batch5Lane3GmailListLabels,
		"gmail_create_label":          batch5Lane3GmailCreateLabel,
	}
}

func batch5Lane3GmailAuthError() map[string]any {
	return map[string]any{
		"error": "Gmail credentials not configured",
		"help":  "Connect Gmail via hive.adenhq.com",
	}
}

// batch5Lane3GmailResponse mirrors _handle_error plus the caller-side JSON read.
func batch5Lane3GmailResponse(status int, raw []byte) map[string]any {
	switch status {
	case http.StatusOK:
		payload, ok := batch5Lane3JSONObject(raw)
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

func batch5Lane3GmailCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (map[string]any, error) {
	if batch5Lane3Token(rt, batch5Lane3GmailModule) == "" {
		return batch5Lane3GmailAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3GmailModule, method, path, query, body)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	return batch5Lane3GmailResponse(status, raw), nil
}

// batch5Lane3GmailPathParam rejects ids that would break out of a path segment.
func batch5Lane3GmailPathParam(value, name string) (string, map[string]any) {
	if strings.Contains(value, "/") || strings.Contains(value, "..") {
		return "", map[string]any{
			"error": fmt.Sprintf("Invalid %s: cannot contain '/' or '..'", name),
		}
	}
	return value, nil
}

// batch5Lane3GmailEnsureList mirrors _ensure_list: a bare string becomes a
// single-element list instead of being rejected.
func batch5Lane3GmailEnsureList(value any) []string {
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

// batch5Lane3GmailPyList renders a list argument like Python's repr, used only
// in the no-labels error message.
func batch5Lane3GmailPyList(raw any, values []string) string {
	if raw == nil {
		return "None"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// batch5Lane3GmailHeaders flattens the headers upstream keeps.
func batch5Lane3GmailHeaders(headers any) map[string]string {
	out := map[string]string{}
	for _, rawHeader := range batch4AsSlice(headers) {
		header := batch4AsMap(rawHeader)
		name := strings.ToLower(batch4AsString(header["name"]))
		switch name {
		case "subject", "from", "to", "date", "cc":
			out[name] = batch4AsString(header["value"])
		}
	}
	return out
}

// batch5Lane3GmailHeaderValue reads a header case-insensitively.
func batch5Lane3GmailHeaderValue(headers any, name string) string {
	for _, rawHeader := range batch4AsSlice(headers) {
		header := batch4AsMap(rawHeader)
		if strings.EqualFold(batch4AsString(header["name"]), name) {
			return batch4AsString(header["value"])
		}
	}
	return ""
}

// batch5Lane3GmailDecode decodes Gmail's padded urlsafe base64 payloads.
func batch5Lane3GmailDecode(text string) (string, bool) {
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

// batch5Lane3GmailBody extracts a plain-text body the way get_message does:
// direct payload body first, then top-level text/plain, then text/html.
func batch5Lane3GmailBody(payload map[string]any) string {
	if text, ok := batch5Lane3GmailDecode(batch4AsString(batch4AsMap(payload["body"])["data"])); ok {
		return text
	}
	parts := batch4AsSlice(payload["parts"])
	for _, mimeType := range []string{"text/plain", "text/html"} {
		for _, rawPart := range parts {
			part := batch4AsMap(rawPart)
			if batch4AsString(part["mimeType"]) != mimeType {
				continue
			}
			if text, ok := batch5Lane3GmailDecode(batch4AsString(batch4AsMap(part["body"])["data"])); ok {
				return text
			}
		}
	}
	return ""
}

// batch5Lane3GmailBodyType walks the MIME tree for one content type, like the
// recursive helper used when quoting a reply.
func batch5Lane3GmailBodyType(part map[string]any, mimeType string) string {
	if batch4AsString(part["mimeType"]) == mimeType {
		if text, ok := batch5Lane3GmailDecode(batch4AsString(batch4AsMap(part["body"])["data"])); ok {
			return text
		}
	}
	for _, rawChild := range batch4AsSlice(part["parts"]) {
		if text := batch5Lane3GmailBodyType(batch4AsMap(rawChild), mimeType); text != "" {
			return text
		}
	}
	return ""
}

// batch5Lane3GmailMessageShape builds the parsed message dict shared by
// gmail_get_message and gmail_batch_get_messages.
func batch5Lane3GmailMessageShape(data map[string]any, full bool) map[string]any {
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
	payload := batch4AsMap(data["payload"])
	if headers := batch5Lane3GmailHeaders(payload["headers"]); len(headers) > 0 {
		for name, value := range headers {
			result[name] = value
		}
	}
	if full {
		if body := batch5Lane3GmailBody(payload); body != "" {
			result["body"] = body
		}
	}
	return result
}

// batch5Lane3GmailRawMessage builds a minimal HTML MIME message and returns
// its urlsafe-base64 form.
func batch5Lane3GmailRawMessage(to, subject, htmlBody, inReplyTo string) string {
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

func batch5Lane3GmailListMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxResults := batch4Clamp(intArg(input, "max_results", 100), 1, 500)
	query := map[string]string{
		"q":          stringArg(input, "query"),
		"maxResults": strconv.Itoa(maxResults),
	}
	if pageToken := stringArg(input, "page_token"); pageToken != "" {
		query["pageToken"] = pageToken
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodGet, batch5Lane3GmailPrefix+"/messages", query, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	messages := batch4AsSlice(result["messages"])
	if messages == nil {
		messages = []any{}
	}
	return map[string]any{
		"messages":             messages,
		"result_size_estimate": result["resultSizeEstimate"],
		"next_page_token":      result["nextPageToken"],
	}, nil
}

func batch5Lane3GmailGetMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := batch5Lane3GmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	format := stringArg(input, "format")
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodGet,
		batch5Lane3GmailPrefix+"/messages/"+batch5Lane3Segments(messageID),
		map[string]string{"format": format}, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return batch5Lane3GmailMessageShape(result, format == "full"), nil
}

func batch5Lane3GmailTrashMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := batch5Lane3GmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodPost,
		batch5Lane3GmailPrefix+"/messages/"+batch5Lane3Segments(messageID)+"/trash", nil, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return map[string]any{"success": true, "message_id": messageID}, nil
}

func batch5Lane3GmailModifyMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID, failure := batch5Lane3GmailPathParam(stringArg(input, "message_id"), "message_id")
	if failure != nil {
		return failure, nil
	}
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	if batch5Lane3Token(rt, batch5Lane3GmailModule) == "" {
		return batch5Lane3GmailAuthError(), nil
	}
	addLabels := batch5Lane3GmailEnsureList(input["add_labels"])
	removeLabels := batch5Lane3GmailEnsureList(input["remove_labels"])
	if len(addLabels) == 0 && len(removeLabels) == 0 {
		return map[string]any{"error": fmt.Sprintf(
			"At least one of add_labels or remove_labels is required. "+
				"Received add_labels=%s, remove_labels=%s. "+
				`Pass label IDs like add_labels=["STARRED"] or remove_labels=["INBOX"].`,
			batch5Lane3GmailPyList(input["add_labels"], addLabels),
			batch5Lane3GmailPyList(input["remove_labels"], removeLabels),
		)}, nil
	}
	body := map[string]any{}
	if len(addLabels) > 0 {
		body["addLabelIds"] = addLabels
	}
	if len(removeLabels) > 0 {
		body["removeLabelIds"] = removeLabels
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodPost,
		batch5Lane3GmailPrefix+"/messages/"+batch5Lane3Segments(messageID)+"/modify", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labels := batch4AsSlice(result["labelIds"])
	if labels == nil {
		labels = []any{}
	}
	return map[string]any{"success": true, "message_id": messageID, "labels": labels}, nil
}

func batch5Lane3GmailBatchModifyMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageIDs := batch5Lane3GmailEnsureList(input["message_ids"])
	if len(messageIDs) == 0 {
		return map[string]any{"error": "message_ids list is required and must not be empty"}, nil
	}
	if batch5Lane3Token(rt, batch5Lane3GmailModule) == "" {
		return batch5Lane3GmailAuthError(), nil
	}
	addLabels := batch5Lane3GmailEnsureList(input["add_labels"])
	removeLabels := batch5Lane3GmailEnsureList(input["remove_labels"])
	if len(addLabels) == 0 && len(removeLabels) == 0 {
		return map[string]any{"error": fmt.Sprintf(
			"At least one of add_labels or remove_labels is required. "+
				"Received add_labels=%s, remove_labels=%s. "+
				`Pass label IDs like add_labels=["STARRED"] or remove_labels=["INBOX"].`,
			batch5Lane3GmailPyList(input["add_labels"], addLabels),
			batch5Lane3GmailPyList(input["remove_labels"], removeLabels),
		)}, nil
	}
	body := map[string]any{"ids": messageIDs}
	if len(addLabels) > 0 {
		body["addLabelIds"] = addLabels
	}
	if len(removeLabels) > 0 {
		body["removeLabelIds"] = removeLabels
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodPost,
		batch5Lane3GmailPrefix+"/messages/batchModify", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	return map[string]any{"success": true, "count": len(messageIDs)}, nil
}

func batch5Lane3GmailBatchGetMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageIDs := batch5Lane3GmailEnsureList(input["message_ids"])
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
		messageID, failure := batch5Lane3GmailPathParam(rawID, "message_id")
		if failure != nil {
			errors = append(errors, map[string]any{"message_id": rawID, "error": failure["error"]})
			continue
		}
		result, err := batch5Lane3GmailCall(ctx, rt, http.MethodGet,
			batch5Lane3GmailPrefix+"/messages/"+batch5Lane3Segments(messageID),
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
		messages = append(messages, batch5Lane3GmailMessageShape(result, format == "full"))
	}
	return map[string]any{"messages": messages, "count": len(messages), "errors": errors}, nil
}

// batch5Lane3GmailQuote builds the quoted-reply block upstream appends.
func batch5Lane3GmailQuote(original map[string]any) string {
	payload := batch4AsMap(original["payload"])
	originalHTML := batch5Lane3GmailBodyType(payload, "text/html")
	if originalHTML == "" {
		plain := batch5Lane3GmailBodyType(payload, "text/plain")
		originalHTML = "<pre>" + html.EscapeString(plain) + "</pre>"
	}
	from := batch5Lane3GmailHeaderValue(payload["headers"], "From")
	date := batch5Lane3GmailHeaderValue(payload["headers"], "Date")
	return "<br><br>" +
		`<div class="gmail_quote">` +
		"<div>On " + date + ", " + from + " wrote:</div>" +
		`<blockquote style="margin:0 0 0 .8ex;border-left:1px #ccc solid;padding-left:1ex">` +
		originalHTML +
		"</blockquote>" +
		"</div>"
}

func batch5Lane3GmailCreateDraft(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	htmlBody := stringArg(input, "html")
	if htmlBody == "" {
		return map[string]any{"error": "Email body (html) is required"}, nil
	}
	if batch5Lane3Token(rt, batch5Lane3GmailModule) == "" {
		return batch5Lane3GmailAuthError(), nil
	}
	to := stringArg(input, "to")
	subject := stringArg(input, "subject")
	signature := stringArg(input, "signature")
	threadID := ""
	inReplyTo := ""
	fullHTML := htmlBody

	if replyTo := stringArg(input, "reply_to_message_id"); replyTo != "" {
		replyToID, failure := batch5Lane3GmailPathParam(replyTo, "reply_to_message_id")
		if failure != nil {
			return failure, nil
		}
		status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3GmailModule, http.MethodGet,
			batch5Lane3GmailPrefix+"/messages/"+batch5Lane3Segments(replyToID),
			map[string]string{"format": "full"}, nil)
		if err != nil {
			return map[string]any{"error": "Failed to fetch original message: " + err.Error()}, nil
		}
		original := batch5Lane3GmailResponse(status, raw)
		if _, failed := original["error"]; failed {
			return original, nil
		}
		threadID = batch4AsString(original["threadId"])
		payload := batch4AsMap(original["payload"])
		inReplyTo = batch5Lane3GmailHeaderValue(payload["headers"], "Message-ID")
		if inReplyTo == "" {
			inReplyTo = batch5Lane3GmailHeaderValue(payload["headers"], "Message-Id")
		}
		originalSubject := batch5Lane3GmailHeaderValue(payload["headers"], "Subject")
		originalFrom := batch5Lane3GmailHeaderValue(payload["headers"], "From")
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
		fullHTML = htmlBody + signatureBlock + batch5Lane3GmailQuote(original)
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

	messageBody := map[string]any{"raw": batch5Lane3GmailRawMessage(to, subject, fullHTML, inReplyTo)}
	if threadID != "" {
		messageBody["threadId"] = threadID
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodPost,
		batch5Lane3GmailPrefix+"/drafts", nil, map[string]any{"message": messageBody})
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	draft := map[string]any{
		"success":    true,
		"draft_id":   batch4AsString(result["id"]),
		"message_id": batch4AsString(batch4AsMap(result["message"])["id"]),
	}
	if threadID != "" {
		draft["thread_id"] = threadID
	}
	return draft, nil
}

func batch5Lane3GmailGetSignature(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sendAsEmail := stringArg(input, "send_as_email")
	path := batch5Lane3GmailPrefix + "/settings/sendAs"
	if sendAsEmail != "" {
		sanitized, failure := batch5Lane3GmailPathParam(sendAsEmail, "send_as_email")
		if failure != nil {
			return failure, nil
		}
		path += "/" + batch5Lane3Segments(sanitized)
	}
	if batch5Lane3Token(rt, batch5Lane3GmailModule) == "" {
		return batch5Lane3GmailAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3GmailModule, http.MethodGet, path, nil, nil)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	if status == http.StatusForbidden {
		return map[string]any{
			"error": "Missing OAuth scope: gmail.settings.basic",
			"help": "Reading Gmail signatures requires the gmail.settings.basic scope. " +
				"Reconnect Gmail via hive.adenhq.com to grant it, or pass the " +
				"signature directly to gmail_create_draft.",
		}, nil
	}
	result := batch5Lane3GmailResponse(status, raw)
	if _, failed := result["error"]; failed {
		return result, nil
	}
	var alias map[string]any
	if sendAsEmail != "" {
		alias = result
	} else {
		aliases := batch4AsSlice(result["sendAs"])
		for _, rawAlias := range aliases {
			candidate := batch4AsMap(rawAlias)
			if batch4AsBool(candidate["isPrimary"]) {
				alias = candidate
				break
			}
		}
		if alias == nil && len(aliases) > 0 {
			alias = batch4AsMap(aliases[0])
		}
		if alias == nil {
			return map[string]any{"error": "No send-as aliases found on this account"}, nil
		}
	}
	return map[string]any{
		"signature":     batch4AsString(alias["signature"]),
		"send_as_email": batch4AsString(alias["sendAsEmail"]),
		"display_name":  batch4AsString(alias["displayName"]),
		"is_primary":    batch4AsBool(alias["isPrimary"]),
	}, nil
}

func batch5Lane3GmailListLabels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodGet, batch5Lane3GmailPrefix+"/labels", nil, nil)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labels := batch4AsSlice(result["labels"])
	if labels == nil {
		labels = []any{}
	}
	return map[string]any{"labels": labels}, nil
}

func batch5Lane3GmailCreateLabel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	name := stringArg(input, "name")
	if strings.TrimSpace(name) == "" {
		return map[string]any{"error": "Label name is required"}, nil
	}
	body := map[string]any{
		"name":                  name,
		"labelListVisibility":   stringArg(input, "label_list_visibility"),
		"messageListVisibility": stringArg(input, "message_list_visibility"),
	}
	result, err := batch5Lane3GmailCall(ctx, rt, http.MethodPost, batch5Lane3GmailPrefix+"/labels", nil, body)
	if err != nil {
		return nil, err
	}
	if _, failed := result["error"]; failed {
		return result, nil
	}
	labelType := batch4AsString(result["type"])
	if labelType == "" {
		labelType = "user"
	}
	return map[string]any{
		"success": true,
		"id":      batch4AsString(result["id"]),
		"name":    batch4AsString(result["name"]),
		"type":    labelType,
	}, nil
}
