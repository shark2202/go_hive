package social

// Pushover: form/query token auth, catalog entry and seven actions.

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"
)

var pushoverTool = &Tool{
	ID:          "pushover_tool",
	Name:        "pushover",
	DisplayName: "Pushover",
	Summary:     "Pushover push-notification integration (7 actions).",
	Actions: []Action{
		{
			Name:     "pushover_send",
			Summary:  "Send a push notification via Pushover.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key", "message"},
			Params: map[string]any{
				"user_key":  param("string"),
				"message":   param("string"),
				"title":     paramDefault("string", ""),
				"priority":  paramDefault("integer", 0),
				"sound":     paramDefault("string", ""),
				"device":    paramDefault("string", ""),
				"url":       paramDefault("string", ""),
				"url_title": paramDefault("string", ""),
				"html":      paramDefault("boolean", false),
				"ttl":       paramDefault("integer", 0),
			},
		},
		{
			Name:     "pushover_validate_user",
			Summary:  "Validate a Pushover user or group key.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key"},
			Params: map[string]any{
				"user_key": param("string"),
				"device":   paramDefault("string", ""),
			},
		},
		{
			Name:    "pushover_list_sounds",
			Summary: "List available notification sounds.",
			Source:  "pushover_tool/pushover_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "pushover_check_receipt",
			Summary:  "Check the status of an emergency-priority notification receipt.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"receipt"},
			Params: map[string]any{
				"receipt": param("string"),
			},
		},
		{
			Name:     "pushover_cancel_receipt",
			Summary:  "Cancel emergency-priority notification retries for a receipt.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"receipt"},
			Params: map[string]any{
				"receipt": param("string"),
			},
		},
		{
			Name:     "pushover_send_glance",
			Summary:  "Update Pushover Glance data on a user's device widget.",
			Source:   "pushover_tool/pushover_tool.py",
			Required: []string{"user_key"},
			Params: map[string]any{
				"user_key": param("string"),
				"title":    paramDefault("string", ""),
				"text":     paramDefault("string", ""),
				"subtext":  paramDefault("string", ""),
				"count":    param("integer"),
				"percent":  param("integer"),
				"device":   paramDefault("string", ""),
			},
		},
		{
			Name:    "pushover_get_limits",
			Summary: "Get Pushover application message limits and usage.",
			Source:  "pushover_tool/pushover_tool.py",
			Params:  map[string]any{},
		},
	},
}

func pushoverImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pushover_send":           pushoverSend,
		"pushover_validate_user":  pushoverValidateUser,
		"pushover_list_sounds":    pushoverListSounds,
		"pushover_check_receipt":  pushoverCheckReceipt,
		"pushover_cancel_receipt": pushoverCancelReceipt,
		"pushover_send_glance":    pushoverSendGlance,
		"pushover_get_limits":     pushoverGetLimits,
	}
}

// ---------------------------------------------------------------- Pushover ---

// pushoverAuth resolves the application token.
func pushoverAuth(rt *Runtime) (string, map[string]any) {
	token := moduleToken(rt, pushoverModuleID)
	if token == "" {
		return "", map[string]any{
			"error": "PUSHOVER_API_TOKEN not set",
			"help":  "Create an app at https://pushover.net/apps/build to get a token",
		}
	}
	return token, nil
}

// pushoverPost performs one form-encoded POST.
func pushoverPost(
	ctx context.Context,
	rt *Runtime,
	suffix string,
	form url.Values,
) (providerResponse, error) {
	base := moduleBaseURL(rt, pushoverModuleID)
	if base == "" {
		base = pushoverDefaultBase
	}
	return doRequest(ctx, rt, providerRequest{
		Method: http.MethodPost,
		URL:    base + suffix,
		Form:   form,
	})
}

// pushoverGet performs one token-query GET.
func pushoverGet(
	ctx context.Context,
	rt *Runtime,
	suffix string,
	query url.Values,
) (providerResponse, error) {
	base := moduleBaseURL(rt, pushoverModuleID)
	if base == "" {
		base = pushoverDefaultBase
	}
	return doRequest(ctx, rt, providerRequest{
		Method: http.MethodGet,
		URL:    base + suffix,
		Query:  query,
	})
}

func pushoverSend(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey, message := stringArg(input, "user_key"), stringArg(input, "message")
	if userKey == "" || message == "" {
		return map[string]any{"error": "user_key and message are required"}, nil
	}
	if utf8.RuneCountInString(message) > 1024 {
		return map[string]any{"error": "message must be 1024 characters or fewer"}, nil
	}
	priority := intArg(input, "priority", 0)
	switch priority {
	case -2, -1, 0, 1, 2:
	default:
		return map[string]any{"error": "priority must be -2, -1, 0, 1, or 2"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}, "message": {message}}
	if title := stringArg(input, "title"); title != "" {
		form.Set("title", truncateTextClamped(title, 250))
	}
	if priority != 0 {
		form.Set("priority", strconv.Itoa(priority))
	}
	if priority == 2 {
		form.Set("retry", "60")
		form.Set("expire", "3600")
	}
	if sound := stringArg(input, "sound"); sound != "" {
		form.Set("sound", sound)
	}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	if link := stringArg(input, "url"); link != "" {
		form.Set("url", truncateTextClamped(link, 512))
	}
	if title := stringArg(input, "url_title"); title != "" {
		form.Set("url_title", truncateTextClamped(title, 100))
	}
	if boolArg(input, "html", false) {
		form.Set("html", "1")
	}
	if ttl := intArg(input, "ttl", 0); ttl > 0 {
		form.Set("ttl", strconv.Itoa(ttl))
	}
	res, err := pushoverPost(ctx, rt, "/messages.json", form)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Pushover timed out"}, nil
		}
		return map[string]any{"error": "Pushover request failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		detail := joinErrors(result["errors"])
		if detail == "" {
			detail = truncateTextClamped(string(res.Body), 300)
		}
		return map[string]any{"error": "Pushover error: " + detail}, nil
	}
	out := map[string]any{"status": "sent", "request": asString(result["request"])}
	if receipt, present := result["receipt"]; present {
		out["receipt"] = receipt
	}
	return out, nil
}

func pushoverValidateUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey := stringArg(input, "user_key")
	if userKey == "" {
		return map[string]any{"error": "user_key is required"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	res, err := pushoverPost(ctx, rt, "/users/validate.json", form)
	if err != nil {
		return map[string]any{"error": "Validation failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	return map[string]any{
		"is_valid": intValue(result["status"]) == 1,
		"devices":  sliceOrEmpty(result["devices"]),
		"is_group": intValue(result["group"]) == 1,
	}, nil
}

func pushoverListSounds(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, err := pushoverGet(ctx, rt, "/sounds.json", url.Values{"token": {token}})
	if err != nil {
		return map[string]any{"error": "List sounds failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		return map[string]any{"error": "Failed to list sounds: " + truncateTextClamped(string(res.Body), 300)}, nil
	}
	return map[string]any{"sounds": mapOrEmpty(result["sounds"])}, nil
}

func pushoverCheckReceipt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	receipt := stringArg(input, "receipt")
	if receipt == "" {
		return map[string]any{"error": "receipt is required"}, nil
	}
	res, err := pushoverGet(ctx, rt, "/receipts/"+receipt+".json", url.Values{"token": {token}})
	if err != nil {
		return map[string]any{"error": "Receipt check failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		return map[string]any{"error": "Receipt check failed: " + truncateTextClamped(string(res.Body), 300)}, nil
	}
	return map[string]any{
		"acknowledged":      intValue(result["acknowledged"]) == 1,
		"acknowledged_by":   asString(result["acknowledged_by"]),
		"acknowledged_at":   valueOrDefault(result["acknowledged_at"], 0),
		"last_delivered_at": valueOrDefault(result["last_delivered_at"], 0),
		"expired":           intValue(result["expired"]) == 1,
		"called_back":       intValue(result["called_back"]) == 1,
	}, nil
}

func pushoverCancelReceipt(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	receipt := stringArg(input, "receipt")
	if receipt == "" {
		return map[string]any{"error": "receipt is required"}, nil
	}
	res, err := pushoverPost(ctx, rt, "/receipts/"+receipt+"/cancel.json", url.Values{"token": {token}})
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Cancel request timed out"}, nil
		}
		return map[string]any{"error": "Cancel failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		return map[string]any{"error": "Cancel failed: " + truncateTextClamped(string(res.Body), 300)}, nil
	}
	return map[string]any{"status": "cancelled", "receipt": receipt}, nil
}

func pushoverSendGlance(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	userKey := stringArg(input, "user_key")
	if userKey == "" {
		return map[string]any{"error": "user_key is required"}, nil
	}
	title, text, subtext := stringArg(input, "title"), stringArg(input, "text"), stringArg(input, "subtext")
	count, hasCount := optionalInt(input, "count")
	percent, hasPercent := optionalInt(input, "percent")
	if title == "" && text == "" && subtext == "" && !hasCount && !hasPercent {
		return map[string]any{"error": "At least one of title, text, subtext, count, or percent is required"}, nil
	}
	form := url.Values{"token": {token}, "user": {userKey}}
	if title != "" {
		form.Set("title", truncateTextClamped(title, 100))
	}
	if text != "" {
		form.Set("text", truncateTextClamped(text, 100))
	}
	if subtext != "" {
		form.Set("subtext", truncateTextClamped(subtext, 100))
	}
	if hasCount {
		form.Set("count", strconv.Itoa(clampInt(count, -999, 999)))
	}
	if hasPercent {
		form.Set("percent", strconv.Itoa(clampInt(percent, 0, 100)))
	}
	if device := stringArg(input, "device"); device != "" {
		form.Set("device", device)
	}
	res, err := pushoverPost(ctx, rt, "/glances.json", form)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Glance request timed out"}, nil
		}
		return map[string]any{"error": "Glance update failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		detail := joinErrors(result["errors"])
		if detail == "" {
			detail = truncateTextClamped(string(res.Body), 300)
		}
		return map[string]any{"error": "Glance error: " + detail}, nil
	}
	return map[string]any{"status": "updated", "request": asString(result["request"])}, nil
}

func pushoverGetLimits(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, fail := pushoverAuth(rt)
	if fail != nil {
		return fail, nil
	}
	res, err := pushoverGet(ctx, rt, "/apps/limits.json", url.Values{"token": {token}})
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Limits request timed out"}, nil
		}
		return map[string]any{"error": "Limits check failed: " + err.Error()}, nil
	}
	result := asMap(res.Parsed)
	if intValue(result["status"]) != 1 {
		return map[string]any{"error": "Limits check failed: " + truncateTextClamped(string(res.Body), 300)}, nil
	}
	return map[string]any{
		"limit":     intValue(result["limit"]),
		"remaining": intValue(result["remaining"]),
		"reset":     valueOrDefault(result["reset"], 0),
	}, nil
}
