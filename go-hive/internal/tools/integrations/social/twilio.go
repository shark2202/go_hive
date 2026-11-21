package social

// Twilio REST: Basic-auth request path, catalog entry and seven actions.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const twilioAPIVersionPath = "/2010-04-01/Accounts/"

var twilioTool = &Tool{
	ID:          "twilio_tool",
	Name:        "twilio",
	DisplayName: "Twilio",
	Summary:     "Twilio SMS and WhatsApp integration (7 actions).",
	Actions: []Action{
		{
			Name:     "twilio_send_sms",
			Summary:  "Send an SMS message via Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"to", "from_number", "body"},
			Params: map[string]any{
				"to":          param("string"),
				"from_number": param("string"),
				"body":        param("string"),
			},
		},
		{
			Name:     "twilio_send_whatsapp",
			Summary:  "Send a WhatsApp message via Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"to", "from_number", "body"},
			Params: map[string]any{
				"to":          param("string"),
				"from_number": param("string"),
				"body":        param("string"),
			},
		},
		{
			Name:    "twilio_list_messages",
			Summary: "List recent messages from your Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params: map[string]any{
				"to":          paramDefault("string", ""),
				"from_number": paramDefault("string", ""),
				"page_size":   paramDefault("integer", 20),
			},
		},
		{
			Name:     "twilio_get_message",
			Summary:  "Get details about a specific Twilio message.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"message_sid"},
			Params: map[string]any{
				"message_sid": param("string"),
			},
		},
		{
			Name:    "twilio_list_phone_numbers",
			Summary: "List phone numbers owned by the Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:    "twilio_list_calls",
			Summary: "List recent calls from your Twilio account.",
			Source:  "twilio_tool/twilio_tool.py",
			Params: map[string]any{
				"to":          paramDefault("string", ""),
				"from_number": paramDefault("string", ""),
				"status":      paramDefault("string", ""),
				"page_size":   paramDefault("integer", 20),
			},
		},
		{
			Name:     "twilio_delete_message",
			Summary:  "Delete a message from Twilio.",
			Source:   "twilio_tool/twilio_tool.py",
			Required: []string{"message_sid"},
			Params: map[string]any{
				"message_sid": param("string"),
			},
		},
	},
}

func twilioImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"twilio_send_sms":           twilioSendSMS,
		"twilio_send_whatsapp":      twilioSendWhatsApp,
		"twilio_list_messages":      twilioListMessages,
		"twilio_get_message":        twilioGetMessage,
		"twilio_list_phone_numbers": twilioListPhoneNumbers,
		"twilio_list_calls":         twilioListCalls,
		"twilio_delete_message":     twilioDeleteMessage,
	}
}

// ------------------------------------------------------------------ Twilio ---

// twilioAuth resolves the account SID and auth token.
func twilioAuth(rt *Runtime) (string, string, map[string]any) {
	sid := envValue(rt, "TWILIO_ACCOUNT_SID")
	token := envValue(rt, "TWILIO_AUTH_TOKEN")
	if sid == "" || token == "" {
		return "", "", map[string]any{
			"error": "TWILIO_ACCOUNT_SID and TWILIO_AUTH_TOKEN not set",
			"help":  "Get credentials from https://console.twilio.com/",
		}
	}
	return sid, token, nil
}

// twilioRequest performs one Basic-authenticated API call.
func twilioRequest(
	ctx context.Context,
	rt *Runtime,
	sid, token, method, suffix string,
	query, form url.Values,
) (providerResponse, map[string]any) {
	base := moduleBaseURL(rt, twilioModuleID)
	if base == "" {
		base = twilioDefaultBase
	}
	basic := base64.StdEncoding.EncodeToString([]byte(sid + ":" + token))
	spec := providerRequest{
		Method: method,
		URL:    base + twilioAPIVersionPath + url.PathEscape(sid) + suffix,
		Header: map[string]string{"Authorization": "Basic " + basic},
	}
	if method == http.MethodGet {
		spec.Query = query
	} else {
		spec.Form = form
	}
	res, err := doRequest(ctx, rt, spec)
	if err != nil {
		return res, networkFailure(twilioModuleID, ctx, err)
	}
	return res, nil
}

// twilioChecked maps the shared status contract.
func twilioChecked(res providerResponse) (any, map[string]any) {
	switch res.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Check your Twilio credentials."}
	case http.StatusNotFound:
		return nil, map[string]any{"error": "Resource not found."}
	case http.StatusTooManyRequests:
		return nil, map[string]any{"error": "Rate limited. Try again shortly."}
	}
	if res.Status != http.StatusOK && res.Status != http.StatusCreated {
		return nil, map[string]any{
			"error": fmt.Sprintf("Twilio API error %d: %s", res.Status, truncateTextClamped(string(res.Body), maxErrorBytes)),
		}
	}
	return res.Parsed, nil
}

// twilioMessage extracts the upstream message projection.
func twilioMessage(message map[string]any) map[string]any {
	return map[string]any{
		"sid":           asString(message["sid"]),
		"to":            asString(message["to"]),
		"from":          asString(message["from"]),
		"body":          asString(message["body"]),
		"status":        asString(message["status"]),
		"direction":     asString(message["direction"]),
		"date_sent":     message["date_sent"],
		"price":         message["price"],
		"error_code":    message["error_code"],
		"error_message": message["error_message"],
	}
}

func twilioSendSMS(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	to, from, body := stringArg(input, "to"), stringArg(input, "from_number"), stringArg(input, "body")
	if to == "" || from == "" || body == "" {
		return map[string]any{"error": "to, from_number, and body are required"}, nil
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodPost, "/Messages.json", nil, url.Values{
		"To":   {to},
		"From": {from},
		"Body": {body},
	})
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return twilioMessage(asMap(decoded)), nil
}

func twilioSendWhatsApp(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	to, from, body := stringArg(input, "to"), stringArg(input, "from_number"), stringArg(input, "body")
	if to == "" || from == "" || body == "" {
		return map[string]any{"error": "to, from_number, and body are required"}, nil
	}
	if !strings.HasPrefix(to, "whatsapp:") {
		to = "whatsapp:" + to
	}
	if !strings.HasPrefix(from, "whatsapp:") {
		from = "whatsapp:" + from
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodPost, "/Messages.json", nil, url.Values{
		"To":   {to},
		"From": {from},
		"Body": {body},
	})
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return twilioMessage(asMap(decoded)), nil
}

func twilioListMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := url.Values{
		"PageSize": {strconv.Itoa(clampInt(intArg(input, "page_size", 20), 1, 1000))},
	}
	if to := stringArg(input, "to"); to != "" {
		query.Set("To", to)
	}
	if from := stringArg(input, "from_number"); from != "" {
		query.Set("From", from)
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodGet, "/Messages.json", query, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	messages := make([]any, 0)
	for _, raw := range asSlice(asMap(decoded)["messages"]) {
		messages = append(messages, twilioMessage(asMap(raw)))
	}
	return map[string]any{"messages": messages, "count": len(messages)}, nil
}

func twilioGetMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	messageSID := stringArg(input, "message_sid")
	if messageSID == "" {
		return map[string]any{"error": "message_sid is required"}, nil
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodGet, "/Messages/"+url.PathEscape(messageSID)+".json", nil, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	return twilioMessage(asMap(decoded)), nil
}

func twilioListPhoneNumbers(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodGet, "/IncomingPhoneNumbers.json", url.Values{"PageSize": {"100"}}, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	numbers := make([]any, 0)
	for _, raw := range asSlice(asMap(decoded)["incoming_phone_numbers"]) {
		number := asMap(raw)
		capabilities := asMap(number["capabilities"])
		numbers = append(numbers, map[string]any{
			"sid":           asString(number["sid"]),
			"phone_number":  asString(number["phone_number"]),
			"friendly_name": asString(number["friendly_name"]),
			"sms_enabled":   asBool(capabilities["sms"]),
			"voice_enabled": asBool(capabilities["voice"]),
			"mms_enabled":   asBool(capabilities["mms"]),
			"date_created":  number["date_created"],
		})
	}
	return map[string]any{"phone_numbers": numbers, "count": len(numbers)}, nil
}

func twilioListCalls(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	query := url.Values{
		"PageSize": {strconv.Itoa(clampInt(intArg(input, "page_size", 20), 1, 1000))},
	}
	if to := stringArg(input, "to"); to != "" {
		query.Set("To", to)
	}
	if from := stringArg(input, "from_number"); from != "" {
		query.Set("From", from)
	}
	if status := stringArg(input, "status"); status != "" {
		query.Set("Status", status)
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodGet, "/Calls.json", query, nil)
	if fail != nil {
		return fail, nil
	}
	decoded, fail := twilioChecked(res)
	if fail != nil {
		return fail, nil
	}
	calls := make([]any, 0)
	for _, raw := range asSlice(asMap(decoded)["calls"]) {
		call := asMap(raw)
		calls = append(calls, map[string]any{
			"sid":        asString(call["sid"]),
			"to":         asString(call["to"]),
			"from":       asString(call["from"]),
			"status":     asString(call["status"]),
			"direction":  asString(call["direction"]),
			"duration":   call["duration"],
			"price":      call["price"],
			"start_time": call["start_time"],
			"end_time":   call["end_time"],
		})
	}
	return map[string]any{"calls": calls, "count": len(calls)}, nil
}

func twilioDeleteMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sid, token, fail := twilioAuth(rt)
	if fail != nil {
		return fail, nil
	}
	messageSID := stringArg(input, "message_sid")
	if messageSID == "" {
		return map[string]any{"error": "message_sid is required"}, nil
	}
	res, fail := twilioRequest(ctx, rt, sid, token, http.MethodDelete, "/Messages/"+url.PathEscape(messageSID)+".json", nil, nil)
	if fail != nil {
		return fail, nil
	}
	switch res.Status {
	case http.StatusNoContent:
		return map[string]any{"sid": messageSID, "status": "deleted"}, nil
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Twilio credentials."}, nil
	case http.StatusNotFound:
		return map[string]any{"error": "Message not found."}, nil
	}
	return map[string]any{
		"error": fmt.Sprintf("Twilio API error %d: %s", res.Status, truncateTextClamped(string(res.Body), maxErrorBytes)),
	}, nil
}
