// Zoom REST API v2 (upstream zoom_tool, 9 actions).
//
// Server-to-server OAuth bearer token only: ZOOM_ACCESS_TOKEN, or
// WithCredential("zoom_tool", ...). Endpoints mirror the upstream module:
//
//	users/{user}/meetings            list/create
//	meetings/{id}                    get/update/delete
//	users/{user}/recordings          cloud recordings by date range
//	past_meetings/{id}/participants  past meeting attendees
//	meetings/{id}/registrants        registration list
//
// Response shaping matches the upstream functions: a fixed field subset per
// resource, page_size clamped to 300, and next_page_token only when non-empty.
package integrations

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var batch7Lane9ZoomTool = &Tool{
	ID:          "zoom_tool",
	Name:        "zoom",
	DisplayName: "Zoom",
	Summary:     "Zoom meetings, recordings, participants and registrants (9 actions).",
	Actions: []Action{
		{
			Name:    "zoom_get_user",
			Summary: "Get Zoom user information.",
			Source:  "zoom_tool/zoom_tool.py",
			Params: map[string]any{
				"user_id": paramDefault("string", "me"),
			},
		},
		{
			Name:    "zoom_list_meetings",
			Summary: "List Zoom meetings for a user.",
			Source:  "zoom_tool/zoom_tool.py",
			Params: map[string]any{
				"user_id":         paramDefault("string", "me"),
				"type":            paramDefault("string", "upcoming"),
				"page_size":       paramDefault("integer", 30),
				"next_page_token": paramDefault("string", ""),
			},
		},
		{
			Name:     "zoom_get_meeting",
			Summary:  "Get details of a specific Zoom meeting.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"meeting_id"},
			Params: map[string]any{
				"meeting_id": param("string"),
			},
		},
		{
			Name:     "zoom_create_meeting",
			Summary:  "Create a new Zoom meeting.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"topic"},
			Params: map[string]any{
				"topic":      param("string"),
				"start_time": paramDefault("string", ""),
				"duration":   paramDefault("integer", 60),
				"timezone":   paramDefault("string", ""),
				"agenda":     paramDefault("string", ""),
				"user_id":    paramDefault("string", "me"),
			},
		},
		{
			Name:     "zoom_delete_meeting",
			Summary:  "Delete/cancel a Zoom meeting.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"meeting_id"},
			Params: map[string]any{
				"meeting_id": param("string"),
			},
		},
		{
			Name:     "zoom_list_recordings",
			Summary:  "List cloud recordings for a Zoom user within a date range.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"from_date", "to_date"},
			Params: map[string]any{
				"from_date":       param("string"),
				"to_date":         param("string"),
				"user_id":         paramDefault("string", "me"),
				"page_size":       paramDefault("integer", 30),
				"next_page_token": paramDefault("string", ""),
			},
		},
		{
			Name:     "zoom_update_meeting",
			Summary:  "Update an existing Zoom meeting.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"meeting_id"},
			Params: map[string]any{
				"meeting_id": param("string"),
				"topic":      paramDefault("string", ""),
				"start_time": paramDefault("string", ""),
				"duration":   paramDefault("integer", 0),
				"timezone":   paramDefault("string", ""),
				"agenda":     paramDefault("string", ""),
			},
		},
		{
			Name:     "zoom_list_meeting_participants",
			Summary:  "List participants from a past Zoom meeting.",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"meeting_id"},
			Params: map[string]any{
				"meeting_id":      param("string"),
				"page_size":       paramDefault("integer", 30),
				"next_page_token": paramDefault("string", ""),
			},
		},
		{
			Name:     "zoom_list_meeting_registrants",
			Summary:  "List registrants for a Zoom meeting (requires registration-enabled meeting).",
			Source:   "zoom_tool/zoom_tool.py",
			Required: []string{"meeting_id"},
			Params: map[string]any{
				"meeting_id":      param("string"),
				"status":          paramDefault("string", "approved"),
				"page_size":       paramDefault("integer", 30),
				"next_page_token": paramDefault("string", ""),
			},
		},
	},
}

func batch7Lane9ZoomImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"zoom_get_user":                  batch7Lane9ZoomGetUser,
		"zoom_list_meetings":             batch7Lane9ZoomListMeetings,
		"zoom_get_meeting":               batch7Lane9ZoomGetMeeting,
		"zoom_create_meeting":            batch7Lane9ZoomCreateMeeting,
		"zoom_delete_meeting":            batch7Lane9ZoomDeleteMeeting,
		"zoom_list_recordings":           batch7Lane9ZoomListRecordings,
		"zoom_update_meeting":            batch7Lane9ZoomUpdateMeeting,
		"zoom_list_meeting_participants": batch7Lane9ZoomListParticipants,
		"zoom_list_meeting_registrants":  batch7Lane9ZoomListRegistrants,
	}
}

// batch7Lane9ZoomAuthError mirrors upstream _get_token's missing-credential map.
func batch7Lane9ZoomAuthError() map[string]any {
	return map[string]any{
		"error": "Zoom credentials not configured",
		"help":  "Set ZOOM_ACCESS_TOKEN environment variable or configure via credential store",
	}
}

// batch7Lane9ZoomTokens resolves the module bearer token.
func batch7Lane9ZoomToken(rt *Runtime) string {
	return moduleToken(rt, "zoom_tool")
}

// batch7Lane9ZoomCall performs one Zoom request and maps status codes to the
// upstream error vocabulary. The second return value is non-nil on failure.
func batch7Lane9ZoomCall(ctx context.Context, rt *Runtime, method, endpoint string, query url.Values, payload any) (map[string]any, map[string]any) {
	headers := map[string]string{
		"Authorization": "Bearer " + batch7Lane9ZoomToken(rt),
		"Content-Type":  "application/json",
		"Accept":        "application/json",
	}
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	resp, err := marketingDoJSON(ctx, rt, method, endpoint, headers, payload, 30*time.Second)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	switch resp.Status {
	case http.StatusNoContent:
		return map[string]any{"success": true}, nil
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Invalid or expired Zoom access token"}
	case http.StatusForbidden:
		return nil, map[string]any{"error": "Insufficient Zoom API scopes for this operation"}
	case http.StatusNotFound:
		return nil, map[string]any{"error": "Zoom resource not found"}
	case http.StatusTooManyRequests:
		return nil, map[string]any{"error": "Zoom rate limit exceeded. Try again later."}
	}
	if resp.Status >= 400 {
		detail := string(resp.Body)
		if body, ok := jsonObject(resp.Body); ok {
			detail = stringOr(body, "message", detail)
		}
		return nil, map[string]any{
			"error": "Zoom API error (HTTP " + strconv.Itoa(resp.Status) + "): " + detail,
		}
	}
	if len(resp.Body) == 0 {
		return map[string]any{"success": true}, nil
	}
	body, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Zoom API error: invalid JSON response"}
	}
	return body, nil
}

// batch7Lane9ZoomQuery builds the shared page_size/next_page_token query.
func batch7Lane9ZoomQuery(input map[string]any) url.Values {
	query := url.Values{
		"page_size": []string{strconv.Itoa(clampInt(intArg(input, "page_size", 30), 1, 300))},
	}
	if token := stringArg(input, "next_page_token"); token != "" {
		query.Set("next_page_token", token)
	}
	return query
}

// batch7Lane9ZoomPage renders the shared {"total_records","count",key,["next_page_token"]} shape.
func batch7Lane9ZoomPage(data map[string]any, key string, items []any) map[string]any {
	output := map[string]any{
		"total_records": marketingIntOr(data, "total_records", 0),
		"count":         len(items),
		key:             items,
	}
	if token := stringOr(data, "next_page_token", ""); token != "" {
		output["next_page_token"] = token
	}
	return output
}

func batch7Lane9ZoomGetUser(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		userID = "me"
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) + "/users/" + pathEscape(userID)
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, nil, nil)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"id":           data["id"],
		"email":        data["email"],
		"first_name":   data["first_name"],
		"last_name":    data["last_name"],
		"display_name": data["display_name"],
		"type":         data["type"],
		"timezone":     data["timezone"],
		"status":       data["status"],
		"account_id":   data["account_id"],
		"created_at":   data["created_at"],
	}, nil
}

func batch7Lane9ZoomListMeetings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		userID = "me"
	}
	query := batch7Lane9ZoomQuery(input)
	meetingType := stringArg(input, "type")
	if meetingType == "" {
		meetingType = "upcoming"
	}
	query.Set("type", meetingType)
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) +
		"/users/" + pathEscape(userID) + "/meetings"
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, query, nil)
	if failure != nil {
		return failure, nil
	}
	meetings := make([]any, 0)
	for _, raw := range asSlice(data["meetings"]) {
		meeting := asMap(raw)
		meetings = append(meetings, map[string]any{
			"id":         meeting["id"],
			"uuid":       meeting["uuid"],
			"topic":      meeting["topic"],
			"type":       meeting["type"],
			"start_time": meeting["start_time"],
			"duration":   meeting["duration"],
			"timezone":   meeting["timezone"],
			"join_url":   meeting["join_url"],
			"created_at": meeting["created_at"],
		})
	}
	return batch7Lane9ZoomPage(data, "meetings", meetings), nil
}

func batch7Lane9ZoomGetMeeting(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	meetingID := stringArg(input, "meeting_id")
	if meetingID == "" {
		return map[string]any{"error": "meeting_id is required"}, nil
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) + "/meetings/" + pathEscape(meetingID)
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, nil, nil)
	if failure != nil {
		return failure, nil
	}
	settings := asMap(data["settings"])
	return map[string]any{
		"id":         data["id"],
		"uuid":       data["uuid"],
		"topic":      data["topic"],
		"type":       data["type"],
		"start_time": data["start_time"],
		"duration":   data["duration"],
		"timezone":   data["timezone"],
		"agenda":     data["agenda"],
		"join_url":   data["join_url"],
		"start_url":  data["start_url"],
		"password":   data["password"],
		"host_id":    data["host_id"],
		"created_at": data["created_at"],
		"settings": map[string]any{
			"host_video":        settings["host_video"],
			"participant_video": settings["participant_video"],
			"join_before_host":  settings["join_before_host"],
			"mute_upon_entry":   settings["mute_upon_entry"],
			"waiting_room":      settings["waiting_room"],
			"auto_recording":    settings["auto_recording"],
		},
	}, nil
}

func batch7Lane9ZoomCreateMeeting(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	topic := stringArg(input, "topic")
	if topic == "" {
		return map[string]any{"error": "topic is required"}, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		userID = "me"
	}
	startTime := stringArg(input, "start_time")
	body := map[string]any{
		"topic":    topic,
		"type":     1,
		"duration": intArg(input, "duration", 60),
	}
	if startTime != "" {
		body["type"] = 2
		body["start_time"] = startTime
	}
	if timezone := stringArg(input, "timezone"); timezone != "" {
		body["timezone"] = timezone
	}
	if agenda := stringArg(input, "agenda"); agenda != "" {
		body["agenda"] = agenda
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) +
		"/users/" + pathEscape(userID) + "/meetings"
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodPost, endpoint, nil, body)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"id":         data["id"],
		"uuid":       data["uuid"],
		"topic":      data["topic"],
		"start_time": data["start_time"],
		"duration":   data["duration"],
		"join_url":   data["join_url"],
		"start_url":  data["start_url"],
		"password":   data["password"],
		"created_at": data["created_at"],
	}, nil
}

func batch7Lane9ZoomDeleteMeeting(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	meetingID := stringArg(input, "meeting_id")
	if meetingID == "" {
		return map[string]any{"error": "meeting_id is required"}, nil
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) + "/meetings/" + pathEscape(meetingID)
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodDelete, endpoint, nil, nil)
	if failure != nil {
		return failure, nil
	}
	return data, nil
}

func batch7Lane9ZoomListRecordings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	fromDate, toDate := stringArg(input, "from_date"), stringArg(input, "to_date")
	if fromDate == "" || toDate == "" {
		return map[string]any{"error": "from_date and to_date are required (YYYY-MM-DD)"}, nil
	}
	userID := stringArg(input, "user_id")
	if userID == "" {
		userID = "me"
	}
	query := batch7Lane9ZoomQuery(input)
	query.Set("from", fromDate)
	query.Set("to", toDate)
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) +
		"/users/" + pathEscape(userID) + "/recordings"
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, query, nil)
	if failure != nil {
		return failure, nil
	}
	recordings := make([]any, 0)
	for _, raw := range asSlice(data["meetings"]) {
		meeting := asMap(raw)
		files := make([]any, 0)
		for _, rawFile := range asSlice(meeting["recording_files"]) {
			file := asMap(rawFile)
			files = append(files, map[string]any{
				"id":             file["id"],
				"file_type":      file["file_type"],
				"file_size":      file["file_size"],
				"recording_type": file["recording_type"],
				"status":         file["status"],
				"play_url":       file["play_url"],
			})
		}
		recordings = append(recordings, map[string]any{
			"meeting_id":      meeting["id"],
			"topic":           meeting["topic"],
			"start_time":      meeting["start_time"],
			"duration":        meeting["duration"],
			"recording_count": meeting["recording_count"],
			"total_size":      meeting["total_size"],
			"recording_files": files,
		})
	}
	return batch7Lane9ZoomPage(data, "recordings", recordings), nil
}

func batch7Lane9ZoomUpdateMeeting(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	meetingID := stringArg(input, "meeting_id")
	if meetingID == "" {
		return map[string]any{"error": "meeting_id is required"}, nil
	}
	body := map[string]any{}
	if topic := stringArg(input, "topic"); topic != "" {
		body["topic"] = topic
	}
	if startTime := stringArg(input, "start_time"); startTime != "" {
		body["start_time"] = startTime
	}
	if duration := intArg(input, "duration", 0); duration > 0 {
		body["duration"] = duration
	}
	if timezone := stringArg(input, "timezone"); timezone != "" {
		body["timezone"] = timezone
	}
	if agenda := stringArg(input, "agenda"); agenda != "" {
		body["agenda"] = agenda
	}
	if len(body) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) + "/meetings/" + pathEscape(meetingID)
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodPatch, endpoint, nil, body)
	if failure != nil {
		return failure, nil
	}
	return data, nil
}

func batch7Lane9ZoomListParticipants(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	meetingID := stringArg(input, "meeting_id")
	if meetingID == "" {
		return map[string]any{"error": "meeting_id is required"}, nil
	}
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) +
		"/past_meetings/" + pathEscape(meetingID) + "/participants"
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, batch7Lane9ZoomQuery(input), nil)
	if failure != nil {
		return failure, nil
	}
	participants := make([]any, 0)
	for _, raw := range asSlice(data["participants"]) {
		participant := asMap(raw)
		participants = append(participants, map[string]any{
			"id":         participant["id"],
			"name":       participant["name"],
			"user_email": participant["user_email"],
			"join_time":  participant["join_time"],
			"leave_time": participant["leave_time"],
			"duration":   participant["duration"],
		})
	}
	return batch7Lane9ZoomPage(data, "participants", participants), nil
}

func batch7Lane9ZoomListRegistrants(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9ZoomToken(rt) == "" {
		return batch7Lane9ZoomAuthError(), nil
	}
	meetingID := stringArg(input, "meeting_id")
	if meetingID == "" {
		return map[string]any{"error": "meeting_id is required"}, nil
	}
	query := batch7Lane9ZoomQuery(input)
	status := stringArg(input, "status")
	if status == "" {
		status = "approved"
	}
	query.Set("status", status)
	endpoint := marketingBase(rt, "zoom_tool", batch7Lane9ZoomBase) +
		"/meetings/" + pathEscape(meetingID) + "/registrants"
	data, failure := batch7Lane9ZoomCall(ctx, rt, http.MethodGet, endpoint, query, nil)
	if failure != nil {
		return failure, nil
	}
	registrants := make([]any, 0)
	for _, raw := range asSlice(data["registrants"]) {
		registrant := asMap(raw)
		registrants = append(registrants, map[string]any{
			"id":          registrant["id"],
			"email":       registrant["email"],
			"first_name":  registrant["first_name"],
			"last_name":   registrant["last_name"],
			"status":      registrant["status"],
			"create_time": registrant["create_time"],
			"join_url":    registrant["join_url"],
		})
	}
	return batch7Lane9ZoomPage(data, "registrants", registrants), nil
}
