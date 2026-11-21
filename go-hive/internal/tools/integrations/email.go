// Email senders (upstream email_tool, 2 actions).
//
// Two providers with different transports:
//
//	resend  POST https://api.resend.com/emails      (RESEND_API_KEY)
//	gmail   POST .../gmail/v1/users/me/messages/send (GOOGLE_ACCESS_TOKEN)
//
// The Gmail path builds MIME in-process and base64url-encodes it, matching the
// upstream message headers (To/Subject/From/Cc/Bcc plus In-Reply-To and
// References for replies). gmail_reply_email first fetches the original
// message to recover threadId, Message-ID, subject, sender and quoted body.
//
// EMAIL_FROM supplies a default sender and EMAIL_OVERRIDE_TO redirects all
// outbound mail to one address for testing, as upstream does. The optional
// account alias is accepted for parity; without a credential store it selects
// nothing.
//
// Two deliberate differences from upstream, both schema-driven: a recipient
// string is split on commas (the upstream signature is str | list[str], but
// this tool schema has no union type), and MIME headers are written verbatim
// as UTF-8 instead of RFC 2047 encoded-words.
package integrations

import (
	"context"
	"encoding/base64"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var batch7Lane9EmailTool = &Tool{
	ID:          "email_tool",
	Name:        "email",
	DisplayName: "Email",
	Summary:     "Send and reply to email via Resend or Gmail (2 actions).",
	Actions: []Action{
		{
			Name:     "send_email",
			Summary:  "Send an email through Resend or Gmail.",
			Source:   "email_tool/email_tool.py",
			Required: []string{"to", "subject", "html", "provider"},
			Params: map[string]any{
				"to":         param("string"),
				"subject":    param("string"),
				"html":       param("string"),
				"provider":   param("string"),
				"from_email": paramDefault("string", ""),
				"cc":         paramDefault("string", ""),
				"bcc":        paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
		{
			Name:     "gmail_reply_email",
			Summary:  "Reply to a Gmail message, keeping it in the same thread.",
			Source:   "email_tool/email_tool.py",
			Required: []string{"message_id", "html"},
			Params: map[string]any{
				"message_id": param("string"),
				"html":       param("string"),
				"cc":         paramDefault("string", ""),
				"bcc":        paramDefault("string", ""),
				"account":    paramDefault("string", ""),
			},
		},
	},
}

func batch7Lane9EmailImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"send_email":        batch7Lane9EmailSend,
		"gmail_reply_email": batch7Lane9EmailReply,
	}
}

// batch7Lane9EmailResendAuthError mirrors the upstream Resend credential map.
func batch7Lane9EmailResendAuthError() map[string]any {
	return map[string]any{
		"error": "Resend credentials not configured",
		"help":  "Set RESEND_API_KEY environment variable. Get a key at https://resend.com/api-keys",
	}
}

// batch7Lane9EmailNormalizeRecipients mirrors _normalize_recipients: a string
// becomes a one-element list, arrays drop blanks, an empty result is nil.
// Comma-separated values are also split so a single string can address many
// recipients.
func batch7Lane9EmailNormalizeRecipients(input map[string]any, name string) []string {
	value, present := input[name]
	if !present || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		out := make([]string, 0)
		for _, part := range strings.Split(typed, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

// batch7Lane9EmailMessage is one MIME message under construction.
type batch7Lane9EmailMessage struct {
	To         string
	Subject    string
	From       string
	Cc         string
	Bcc        string
	InReplyTo  string
	References string
	HTML       string
}

// batch7Lane9EmailRaw renders a multipart/alternative HTML message and returns
// its padded URL-safe base64 form.
func batch7Lane9EmailRaw(message batch7Lane9EmailMessage) string {
	const boundary = "batch7Lane9Boundary"
	var builder strings.Builder
	builder.WriteString("To: " + message.To + "\r\n")
	builder.WriteString("Subject: " + message.Subject + "\r\n")
	if message.From != "" {
		builder.WriteString("From: " + message.From + "\r\n")
	}
	if message.Cc != "" {
		builder.WriteString("Cc: " + message.Cc + "\r\n")
	}
	if message.Bcc != "" {
		builder.WriteString("Bcc: " + message.Bcc + "\r\n")
	}
	if message.InReplyTo != "" {
		builder.WriteString("In-Reply-To: " + message.InReplyTo + "\r\n")
		builder.WriteString("References: " + message.References + "\r\n")
	}
	builder.WriteString("MIME-Version: 1.0\r\n")
	builder.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n")
	builder.WriteString("\r\n")
	builder.WriteString("--" + boundary + "\r\n")
	builder.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\n")
	builder.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	builder.WriteString("\r\n")
	builder.WriteString(message.HTML + "\r\n")
	builder.WriteString("--" + boundary + "--\r\n")
	return base64.URLEncoding.EncodeToString([]byte(builder.String()))
}

// batch7Lane9EmailSendResend posts one message to the Resend API.
func batch7Lane9EmailSendResend(ctx context.Context, rt *Runtime, key string, to []string, subject, htmlBody, fromEmail string, cc, bcc []string) map[string]any {
	payload := map[string]any{
		"from":    fromEmail,
		"to":      to,
		"subject": subject,
		"html":    htmlBody,
	}
	if len(cc) > 0 {
		payload["cc"] = cc
	}
	if len(bcc) > 0 {
		payload["bcc"] = bcc
	}
	endpoint := marketingBase(rt, "email_tool", batch7Lane9ResendBase) + "/emails"
	resp, err := marketingDoJSON(ctx, rt, http.MethodPost, endpoint,
		map[string]string{"Authorization": "Bearer " + key}, payload, 30*time.Second)
	if err != nil {
		return map[string]any{"error": "Resend API error: " + err.Error()}
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return map[string]any{
			"error": "Resend API error: HTTP " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Resend API error: invalid JSON response"}
	}
	return map[string]any{
		"success":  true,
		"provider": "resend",
		"id":       stringOr(data, "id", ""),
		"to":       to,
		"subject":  subject,
	}
}

// batch7Lane9EmailSendGmail posts one base64url MIME message to the Gmail API.
func batch7Lane9EmailSendGmail(ctx context.Context, rt *Runtime, token, raw, threadID string, to []string, subject string) map[string]any {
	payload := map[string]any{"raw": raw}
	if threadID != "" {
		payload["threadId"] = threadID
	}
	endpoint := marketingBase(rt, "email_gmail_tool", batch7Lane9GmailBase) + "/users/me/messages/send"
	resp, err := marketingDoJSON(ctx, rt, http.MethodPost, endpoint,
		map[string]string{"Authorization": "Bearer " + token}, payload, 30*time.Second)
	if err != nil {
		return map[string]any{"error": "Gmail API error: " + err.Error()}
	}
	if resp.Status == http.StatusUnauthorized {
		return map[string]any{
			"error": "Gmail token expired or invalid",
			"help":  "Re-authorize via hive.adenhq.com",
		}
	}
	if resp.Status != http.StatusOK {
		return map[string]any{
			"error": "Gmail API error (HTTP " + strconv.Itoa(resp.Status) + "): " + string(resp.Body),
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Gmail API error: invalid JSON response"}
	}
	result := map[string]any{
		"success":  true,
		"provider": "gmail",
		"id":       stringOr(data, "id", ""),
		"to":       to,
		"subject":  subject,
	}
	if threadID != "" {
		result["threadId"] = stringOr(data, "threadId", threadID)
	}
	return result
}

func batch7Lane9EmailSend(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	fromEmail := stringArg(input, "from_email")
	if fromEmail == "" {
		fromEmail = runtimeEnv(rt, "EMAIL_FROM")
	}
	to := batch7Lane9EmailNormalizeRecipients(input, "to")
	if len(to) == 0 {
		return map[string]any{"error": "At least one recipient email is required"}, nil
	}
	subject := stringArg(input, "subject")
	if subject == "" || len([]rune(subject)) > 998 {
		return map[string]any{"error": "Subject must be 1-998 characters"}, nil
	}
	htmlBody := stringArg(input, "html")
	if htmlBody == "" {
		return map[string]any{"error": "Email body (html) is required"}, nil
	}
	cc := batch7Lane9EmailNormalizeRecipients(input, "cc")
	bcc := batch7Lane9EmailNormalizeRecipients(input, "bcc")

	if override := runtimeEnv(rt, "EMAIL_OVERRIDE_TO"); override != "" {
		subject = "[TEST -> " + strings.Join(to, ", ") + "] " + subject
		to = []string{override}
		cc = nil
		bcc = nil
	}

	provider := strings.ToLower(strings.TrimSpace(stringArg(input, "provider")))
	if provider != "resend" && provider != "gmail" {
		return map[string]any{"error": "provider must be 'gmail' or 'resend'"}, nil
	}
	if provider == "resend" && fromEmail == "" {
		return map[string]any{
			"error": "Sender email is required",
			"help":  "Pass from_email or set EMAIL_FROM environment variable",
		}, nil
	}

	if provider == "gmail" {
		token, _, problem := oauthModuleToken(ctx, rt, "email_gmail_tool")
		if problem != nil {
			return problem, nil
		}
		if token == "" {
			return gmailAuthError(), nil
		}
		raw := batch7Lane9EmailRaw(batch7Lane9EmailMessage{
			To:      strings.Join(to, ", "),
			Subject: subject,
			From:    fromEmail,
			Cc:      strings.Join(cc, ", "),
			Bcc:     strings.Join(bcc, ", "),
			HTML:    htmlBody,
		})
		return batch7Lane9EmailSendGmail(ctx, rt, token, raw, "", to, subject), nil
	}
	key := moduleToken(rt, "email_tool")
	if key == "" {
		return batch7Lane9EmailResendAuthError(), nil
	}
	return batch7Lane9EmailSendResend(ctx, rt, key, to, subject, htmlBody, fromEmail, cc, bcc), nil
}

// batch7Lane9EmailHeaders indexes a Gmail payload header list by lower-cased
// name, matching the upstream dict comprehension.
func batch7Lane9EmailHeaders(raw any) map[string]string {
	headers := map[string]string{}
	for _, entry := range asSlice(raw) {
		header := asMap(entry)
		name := stringOr(header, "name", "")
		if name != "" {
			headers[strings.ToLower(name)] = stringOr(header, "value", "")
		}
	}
	return headers
}

// batch7Lane9EmailDecodeBody decodes Gmail's URL-safe base64 payload.
func batch7Lane9EmailDecodeBody(data string) (string, bool) {
	if data == "" {
		return "", false
	}
	decoded, err := base64.URLEncoding.DecodeString(data)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
		if err != nil {
			return "", false
		}
	}
	return string(decoded), true
}

// batch7Lane9EmailExtractBody walks a MIME tree for the first part of the
// requested type, mirroring the upstream recursive helper.
func batch7Lane9EmailExtractBody(part map[string]any, mimeType string) string {
	if stringOr(part, "mimeType", "") == mimeType {
		if body := stringOr(asMap(part["body"]), "data", ""); body != "" {
			if decoded, ok := batch7Lane9EmailDecodeBody(body); ok {
				return decoded
			}
		}
	}
	for _, raw := range asSlice(part["parts"]) {
		if decoded := batch7Lane9EmailExtractBody(asMap(raw), mimeType); decoded != "" {
			return decoded
		}
	}
	return ""
}

// batch7Lane9EmailPlainToHTML wraps text in a <pre> block, as upstream does.
func batch7Lane9EmailPlainToHTML(text string) string {
	return "<pre>" + html.EscapeString(text) + "</pre>"
}

func batch7Lane9EmailReply(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	messageID := strings.TrimSpace(stringArg(input, "message_id"))
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	htmlBody := stringArg(input, "html")
	if htmlBody == "" {
		return map[string]any{"error": "Reply body (html) is required"}, nil
	}
	token, _, problem := oauthModuleToken(ctx, rt, "email_gmail_tool")
	if problem != nil {
		return problem, nil
	}
	if token == "" {
		return gmailAuthError(), nil
	}

	endpoint := marketingBase(rt, "email_gmail_tool", batch7Lane9GmailBase) +
		"/users/me/messages/" + pathEscape(messageID) + "?format=full"
	resp, err := marketingDoJSON(ctx, rt, http.MethodGet, endpoint,
		map[string]string{"Authorization": "Bearer " + token}, nil, 30*time.Second)
	if err != nil {
		return map[string]any{"error": "Failed to fetch original message: " + err.Error()}, nil
	}
	if resp.Status == http.StatusUnauthorized {
		return map[string]any{
			"error": "Gmail token expired or invalid",
			"help":  "Re-authorize via hive.adenhq.com",
		}, nil
	}
	if resp.Status == http.StatusNotFound {
		return map[string]any{"error": "Original message not found: " + messageID}, nil
	}
	if resp.Status != http.StatusOK {
		return map[string]any{
			"error": "Gmail API error (HTTP " + strconv.Itoa(resp.Status) + "): " + string(resp.Body),
		}, nil
	}
	original, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Failed to fetch original message: invalid JSON response"}, nil
	}
	payload := asMap(original["payload"])
	headers := batch7Lane9EmailHeaders(payload["headers"])
	threadID := stringOr(original, "threadId", "")
	originalMessageID := headers["message-id"]
	originalSubject := headers["subject"]
	replyTo := headers["from"]
	originalDate := headers["date"]

	subject := originalSubject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}
	originalBody := batch7Lane9EmailExtractBody(payload, "text/html")
	if originalBody == "" {
		originalBody = batch7Lane9EmailPlainToHTML(batch7Lane9EmailExtractBody(payload, "text/plain"))
	}
	quoted := "<br><br>" +
		`<div class="gmail_quote">` +
		"<div>On " + originalDate + ", " + replyTo + " wrote:</div>" +
		`<blockquote style="margin:0 0 0 .8ex;border-left:1px #ccc solid;padding-left:1ex">` +
		originalBody +
		"</blockquote>" +
		"</div>"
	fullHTML := htmlBody + quoted

	cc := batch7Lane9EmailNormalizeRecipients(input, "cc")
	bcc := batch7Lane9EmailNormalizeRecipients(input, "bcc")
	finalSubject := subject
	finalTo := replyTo
	if override := runtimeEnv(rt, "EMAIL_OVERRIDE_TO"); override != "" {
		finalTo = override
		cc = nil
		bcc = nil
		finalSubject = "[TEST -> " + replyTo + "] " + subject
	}

	raw := batch7Lane9EmailRaw(batch7Lane9EmailMessage{
		To:         finalTo,
		Subject:    finalSubject,
		Cc:         strings.Join(cc, ", "),
		Bcc:        strings.Join(bcc, ", "),
		InReplyTo:  originalMessageID,
		References: originalMessageID,
		HTML:       fullHTML,
	})
	payloadOut := map[string]any{"raw": raw, "threadId": threadID}
	base := marketingBase(rt, "email_gmail_tool", batch7Lane9GmailBase)
	postResp, err := marketingDoJSON(ctx, rt, http.MethodPost, base+"/users/me/messages/send",
		map[string]string{"Authorization": "Bearer " + token}, payloadOut, 30*time.Second)
	if err != nil {
		return map[string]any{"error": "Failed to send reply: " + err.Error()}, nil
	}
	if postResp.Status == http.StatusUnauthorized {
		return map[string]any{
			"error": "Gmail token expired or invalid",
			"help":  "Re-authorize via hive.adenhq.com",
		}, nil
	}
	if postResp.Status != http.StatusOK {
		return map[string]any{
			"error": "Gmail API error (HTTP " + strconv.Itoa(postResp.Status) + "): " + string(postResp.Body),
		}, nil
	}
	data, ok := jsonObject(postResp.Body)
	if !ok {
		return map[string]any{"error": "Gmail API error: invalid JSON response"}, nil
	}
	return map[string]any{
		"success":  true,
		"provider": "gmail",
		"id":       stringOr(data, "id", ""),
		"threadId": stringOr(data, "threadId", threadID),
		"to":       replyTo,
		"subject":  subject,
	}, nil
}
