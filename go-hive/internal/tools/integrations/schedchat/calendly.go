package schedchat

// Calendly: scheduled events, invitees, event types and webhooks (8 actions).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ------------------------------------------------------------- calendly ---

// calendlyTool mirrors gen_catalog.py output for calendly (8).
var calendlyTool = &Tool{
	ID:          "calendly_tool",
	Name:        "calendly",
	DisplayName: "Calendly",
	Summary:     "Calendly integration (8 actions).",
	Actions: []Action{
		{
			Name:     "calendly_cancel_event",
			Summary:  "Cancel a scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
				"reason":    paramDefault("string", ""),
			},
		},
		{
			Name:    "calendly_get_current_user",
			Summary: "Get the current authenticated Calendly user.",
			Source:  "calendly_tool/calendly_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "calendly_get_event_type",
			Summary:  "Get details of a specific Calendly event type (meeting template).",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_type_uri"},
			Params: map[string]any{
				"event_type_uri": param("string"),
			},
		},
		{
			Name:     "calendly_get_scheduled_event",
			Summary:  "Get details of a specific scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
			},
		},
		{
			Name:     "calendly_list_event_types",
			Summary:  "List Calendly event types (meeting templates) for a user.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"user_uri"},
			Params: map[string]any{
				"user_uri": param("string"),
				"active":   paramDefault("boolean", true),
				"count":    paramDefault("integer", 20),
			},
		},
		{
			Name:     "calendly_list_invitees",
			Summary:  "List invitees for a scheduled Calendly event.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"event_uri"},
			Params: map[string]any{
				"event_uri": param("string"),
				"count":     paramDefault("integer", 25),
			},
		},
		{
			Name:     "calendly_list_scheduled_events",
			Summary:  "List scheduled Calendly events (booked meetings).",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"user_uri"},
			Params: map[string]any{
				"user_uri":       param("string"),
				"status":         paramDefault("string", "active"),
				"min_start_time": paramDefault("string", ""),
				"max_start_time": paramDefault("string", ""),
				"count":          paramDefault("integer", 20),
			},
		},
		{
			Name:     "calendly_list_webhooks",
			Summary:  "List webhook subscriptions for a Calendly organization or user.",
			Source:   "calendly_tool/calendly_tool.py",
			Required: []string{"organization_uri"},
			Params: map[string]any{
				"organization_uri": param("string"),
				"scope":            paramDefault("string", "organization"),
				"count":            paramDefault("integer", 20),
			},
		},
	},
}

// calendlyImpls binds every calendly action to its implementation.
func calendlyImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calendly_cancel_event":          calendlyCancelEvent,
		"calendly_get_current_user":      calendlyGetCurrentUser,
		"calendly_get_event_type":        calendlyGetEventType,
		"calendly_get_scheduled_event":   calendlyGetScheduledEvent,
		"calendly_list_event_types":      calendlyListEventTypes,
		"calendly_list_invitees":         calendlyListInvitees,
		"calendly_list_scheduled_events": calendlyListScheduledEvents,
		"calendly_list_webhooks":         calendlyListWebhooks,
	}
}

// calendlyGetCurrentUser reads /users/me.
func calendlyGetCurrentUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet, "/users/me", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	user := asMap(asMap(parsed)["resource"])
	return map[string]any{
		"uri":            user["uri"],
		"name":           user["name"],
		"email":          user["email"],
		"scheduling_url": user["scheduling_url"],
		"timezone":       user["timezone"],
		"organization":   user["current_organization"],
	}, nil
}

// calendlyListEventTypes lists event types for a user.
func calendlyListEventTypes(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	userURI := stringArg(input, "user_uri")
	if userURI == "" {
		return map[string]any{"error": "user_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("user", userURI)
	query.Set("count", strconv.Itoa(count))
	if boolArg(input, "active", true) {
		query.Set("active", "true")
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet, "/event_types", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := sliceOrEmpty(asMap(parsed)["collection"])
	eventTypes := make([]any, 0, len(items))
	for _, raw := range items {
		eventType := asMap(raw)
		eventTypes = append(eventTypes, map[string]any{
			"uri":            eventType["uri"],
			"name":           eventType["name"],
			"slug":           eventType["slug"],
			"active":         eventType["active"],
			"duration":       eventType["duration"],
			"kind":           eventType["kind"],
			"scheduling_url": eventType["scheduling_url"],
			"description":    eventType["description_plain"],
		})
	}
	return map[string]any{"count": len(eventTypes), "event_types": eventTypes}, nil
}

// calendlyListScheduledEvents lists booked meetings.
func calendlyListScheduledEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	userURI := stringArg(input, "user_uri")
	if userURI == "" {
		return map[string]any{"error": "user_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("user", userURI)
	query.Set("count", strconv.Itoa(count))
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	if minStart := stringArg(input, "min_start_time"); minStart != "" {
		query.Set("min_start_time", minStart)
	}
	if maxStart := stringArg(input, "max_start_time"); maxStart != "" {
		query.Set("max_start_time", maxStart)
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet, "/scheduled_events", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := sliceOrEmpty(asMap(parsed)["collection"])
	events := make([]any, 0, len(items))
	for _, raw := range items {
		event := asMap(raw)
		invitees := asMap(event["invitees_counter"])
		events = append(events, map[string]any{
			"uri":            event["uri"],
			"name":           event["name"],
			"status":         event["status"],
			"start_time":     event["start_time"],
			"end_time":       event["end_time"],
			"event_type":     event["event_type"],
			"location":       asMap(event["location"])["location"],
			"invitees_count": intFrom(invitees["total"], 0),
		})
	}
	return map[string]any{"count": len(events), "events": events}, nil
}

// calendlyGetScheduledEvent reads one scheduled event.
func calendlyGetScheduledEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet,
		"/scheduled_events/"+url.PathEscape(uriTail(eventURI)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	event := asMap(asMap(parsed)["resource"])
	return map[string]any{
		"uri":               event["uri"],
		"name":              event["name"],
		"status":            event["status"],
		"start_time":        event["start_time"],
		"end_time":          event["end_time"],
		"event_type":        event["event_type"],
		"location":          event["location"],
		"invitees_counter":  event["invitees_counter"],
		"event_memberships": event["event_memberships"],
		"created_at":        event["created_at"],
	}, nil
}

// calendlyListInvitees lists invitees for an event.
func calendlyListInvitees(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	count := intArg(input, "count", 25)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("count", strconv.Itoa(count))
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet,
		"/scheduled_events/"+url.PathEscape(uriTail(eventURI))+"/invitees", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := sliceOrEmpty(asMap(parsed)["collection"])
	invitees := make([]any, 0, len(items))
	for _, raw := range items {
		invitee := asMap(raw)
		invitees = append(invitees, map[string]any{
			"uri":                   invitee["uri"],
			"name":                  invitee["name"],
			"email":                 invitee["email"],
			"status":                invitee["status"],
			"timezone":              invitee["timezone"],
			"questions_and_answers": sliceOrEmpty(invitee["questions_and_answers"]),
			"created_at":            invitee["created_at"],
		})
	}
	return map[string]any{"count": len(invitees), "invitees": invitees}, nil
}

// calendlyCancelEvent cancels one scheduled event.
func calendlyCancelEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventURI := stringArg(input, "event_uri")
	if eventURI == "" {
		return map[string]any{"error": "event_uri is required"}, nil
	}
	body := map[string]any{}
	if reason := stringArg(input, "reason"); reason != "" {
		body["reason"] = reason
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodPost,
		"/scheduled_events/"+url.PathEscape(uriTail(eventURI))+"/cancellation", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	resource := asMap(asMap(parsed)["resource"])
	return map[string]any{
		"canceled_by": asString(resource["canceled_by"]),
		"reason":      asString(resource["reason"]),
		"created_at":  asString(resource["created_at"]),
		"status":      "canceled",
	}, nil
}

// calendlyListWebhooks lists webhook subscriptions.
func calendlyListWebhooks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	organizationURI := stringArg(input, "organization_uri")
	if organizationURI == "" {
		return map[string]any{"error": "organization_uri is required"}, nil
	}
	count := intArg(input, "count", 20)
	if count > 100 {
		count = 100
	}
	query := url.Values{}
	query.Set("organization", organizationURI)
	query.Set("scope", stringArg(input, "scope"))
	query.Set("count", strconv.Itoa(count))
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet, "/webhook_subscriptions", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	items := sliceOrEmpty(asMap(parsed)["collection"])
	webhooks := make([]any, 0, len(items))
	for _, raw := range items {
		webhook := asMap(raw)
		webhooks = append(webhooks, map[string]any{
			"uri":          asString(webhook["uri"]),
			"callback_url": asString(webhook["callback_url"]),
			"state":        asString(webhook["state"]),
			"events":       sliceOrEmpty(webhook["events"]),
			"scope":        asString(webhook["scope"]),
			"created_at":   asString(webhook["created_at"]),
		})
	}
	return map[string]any{"count": len(webhooks), "webhooks": webhooks}, nil
}

// calendlyGetEventType reads one event type.
func calendlyGetEventType(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calendly_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeURI := stringArg(input, "event_type_uri")
	if eventTypeURI == "" {
		return map[string]any{"error": "event_type_uri is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "calendly_tool", http.MethodGet,
		"/event_types/"+url.PathEscape(uriTail(eventTypeURI)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	eventType := asMap(asMap(parsed)["resource"])
	return map[string]any{
		"uri":              asString(eventType["uri"]),
		"name":             asString(eventType["name"]),
		"slug":             asString(eventType["slug"]),
		"active":           asBool(eventType["active"]),
		"duration":         eventType["duration"],
		"kind":             asString(eventType["kind"]),
		"type":             asString(eventType["type"]),
		"color":            asString(eventType["color"]),
		"scheduling_url":   asString(eventType["scheduling_url"]),
		"description":      asString(eventType["description_plain"]),
		"custom_questions": sliceOrEmpty(eventType["custom_questions"]),
	}, nil
}
