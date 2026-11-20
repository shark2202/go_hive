// Google Calendar actions for batch 5, lane 3 (upstream calendar_tool,
// 8 actions). See google_workspace.go for the registry and shared helpers.
package integrations

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var batch5Lane3CalDateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func batch5Lane3CalendarImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calendar_list_events":        batch5Lane3CalListEvents,
		"calendar_get_event":          batch5Lane3CalGetEvent,
		"calendar_create_event":       batch5Lane3CalCreateEvent,
		"calendar_update_event":       batch5Lane3CalUpdateEvent,
		"calendar_delete_event":       batch5Lane3CalDeleteEvent,
		"calendar_list_calendars":     batch5Lane3CalListCalendars,
		"calendar_get_calendar":       batch5Lane3CalGetCalendar,
		"calendar_check_availability": batch5Lane3CalCheckAvailability,
	}
}

func batch5Lane3CalAuthError() map[string]any {
	return map[string]any{
		"error": "Calendar credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable",
	}
}

// batch5Lane3CalCredentialError mirrors _check_credentials, which every
// calendar action runs before validating its arguments.
func batch5Lane3CalCredentialError(rt *Runtime) map[string]any {
	if !batch5Lane3AccountCredential(rt, batch5Lane3CalModule) {
		return batch5Lane3CalAuthError()
	}
	return nil
}

// batch5Lane3CalResponse mirrors calendar_tool._handle_response.
func batch5Lane3CalResponse(status int, raw []byte) map[string]any {
	switch {
	case status == http.StatusUnauthorized:
		return map[string]any{
			"error": "Invalid or expired OAuth token",
			"help":  "Get a new token from https://developers.google.com/oauthplayground/",
		}
	case status == http.StatusForbidden:
		return map[string]any{
			"error": "Access denied. Check calendar permissions.",
			"help":  "Ensure the OAuth token has calendar.events scope",
		}
	case status == http.StatusNotFound:
		return map[string]any{"error": "Resource not found"}
	case status == http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limit exceeded. Try again later."}
	case status >= 400:
		if payload, ok := batch5Lane3JSONObject(raw); ok {
			if errorObject, ok := payload["error"].(map[string]any); ok {
				if message, ok := errorObject["message"].(string); ok {
					return map[string]any{"error": "API error: " + message}
				}
			}
		}
		return map[string]any{"error": fmt.Sprintf("API request failed: HTTP %d", status)}
	}
	payload, ok := batch5Lane3JSONObject(raw)
	if !ok {
		return map[string]any{"error": "API request failed: invalid JSON response"}
	}
	return payload
}

func batch5Lane3CalCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !batch5Lane3AccountCredential(rt, batch5Lane3CalModule) {
		return batch5Lane3CalAuthError(), nil
	}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3CalModule, method, path, query, body)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	return batch5Lane3CalResponse(status, raw), nil
}

// batch5Lane3CalValidateTimezone mirrors the available_timezones() check.
func batch5Lane3CalValidateTimezone(name string) map[string]any {
	if _, err := time.LoadLocation(name); err != nil {
		return map[string]any{
			"error": fmt.Sprintf("Invalid timezone '%s'. Use IANA format (e.g., 'America/New_York')", name),
		}
	}
	return nil
}

// batch5Lane3CalParseDT parses an ISO 8601 timestamp and treats naive values
// as UTC, like _parse_event_dt.
func batch5Lane3CalParseDT(text string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999-0700",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			if layout == "2006-01-02T15:04:05.999999" || layout == "2006-01-02T15:04:05" {
				return time.Date(parsed.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), parsed.Second(), parsed.Nanosecond(), time.UTC), true
			}
			return parsed, true
		}
	}
	return time.Time{}, false
}

// batch5Lane3CalFormatEvent shapes the event list output.
func batch5Lane3CalFormatEvent(item map[string]any) map[string]any {
	start := batch4AsMap(item["start"])
	end := batch4AsMap(item["end"])
	summary := "(No title)"
	if value, ok := item["summary"].(string); ok {
		summary = value
	}
	event := map[string]any{
		"id":          item["id"],
		"summary":     summary,
		"start":       lane3FirstNonEmpty(start["dateTime"], start["date"]),
		"end":         lane3FirstNonEmpty(end["dateTime"], end["date"]),
		"location":    item["location"],
		"status":      item["status"],
		"html_link":   item["htmlLink"],
		"description": item["description"],
		"hangoutLink": item["hangoutLink"],
	}
	if attendees := batch4AsSlice(item["attendees"]); len(attendees) > 0 {
		emails := make([]any, 0, len(attendees))
		for _, rawAttendee := range attendees {
			emails = append(emails, batch4AsMap(rawAttendee)["email"])
		}
		event["attendees"] = emails
	}
	return event
}

// firstNonEmpty mirrors `x.get("dateTime") or x.get("date")`.
func lane3FirstNonEmpty(values ...any) any {
	for _, value := range values {
		switch typed := value.(type) {
		case nil:
			continue
		case string:
			if typed != "" {
				return typed
			}
		default:
			return typed
		}
	}
	return nil
}

// batch5Lane3CalAvailabilityEvent shapes one event for availability math.
func batch5Lane3CalAvailabilityEvent(item map[string]any) map[string]any {
	start := batch4AsMap(item["start"])
	end := batch4AsMap(item["end"])
	summary := "(No title)"
	if value, ok := item["summary"].(string); ok {
		summary = value
	}
	status := "confirmed"
	if value, ok := item["status"].(string); ok {
		status = value
	}
	transparency := "opaque"
	if value, ok := item["transparency"].(string); ok {
		transparency = value
	}
	return map[string]any{
		"summary":      summary,
		"start":        lane3FirstNonEmpty(start["dateTime"], start["date"]),
		"end":          lane3FirstNonEmpty(end["dateTime"], end["date"]),
		"status":       status,
		"transparency": transparency,
	}
}

type batch5Lane3CalInterval struct {
	start time.Time
	end   time.Time
	name  string
}

// batch5Lane3CalBusyFree merges overlapping events into busy blocks and derives
// free slots and conflicts, mirroring _compute_busy_free_conflicts.
func batch5Lane3CalBusyFree(events []any, windowStart, windowEnd time.Time) ([]any, []any, []any) {
	intervals := make([]batch5Lane3CalInterval, 0, len(events))
	for _, rawEvent := range events {
		event := batch4AsMap(rawEvent)
		if batch4AsString(event["transparency"]) == "transparent" || batch4AsString(event["status"]) == "cancelled" {
			continue
		}
		startText := batch4AsString(event["start"])
		endText := batch4AsString(event["end"])
		if startText == "" || endText == "" {
			continue
		}
		if batch5Lane3CalDateOnly.MatchString(startText) || batch5Lane3CalDateOnly.MatchString(endText) {
			continue
		}
		start, startOK := batch5Lane3CalParseDT(startText)
		end, endOK := batch5Lane3CalParseDT(endText)
		if !startOK || !endOK {
			continue
		}
		intervals = append(intervals, batch5Lane3CalInterval{start: start, end: end, name: batch4AsString(event["summary"])})
	}
	sort.SliceStable(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })

	busy := make([]any, 0)
	conflicts := make([]any, 0)
	if len(intervals) > 0 {
		curStart, curEnd := intervals[0].start, intervals[0].end
		curNames := []any{intervals[0].name}
		flush := func() {
			if len(curNames) > 1 {
				conflicts = append(conflicts, map[string]any{
					"events":        curNames,
					"overlap_start": batch5Lane3PyISO(curStart),
					"overlap_end":   batch5Lane3PyISO(curEnd),
				})
			}
			busy = append(busy, map[string]any{
				"start": batch5Lane3PyISO(curStart),
				"end":   batch5Lane3PyISO(curEnd),
			})
		}
		for _, interval := range intervals[1:] {
			if interval.start.Before(curEnd) {
				curNames = append(curNames, interval.name)
				if interval.end.After(curEnd) {
					curEnd = interval.end
				}
				continue
			}
			flush()
			curStart, curEnd = interval.start, interval.end
			curNames = []any{interval.name}
		}
		flush()
	}

	freeSlots := make([]any, 0)
	cursor := windowStart
	for _, rawBlock := range busy {
		block := batch4AsMap(rawBlock)
		blockStart, okStart := batch5Lane3CalParseDT(batch4AsString(block["start"]))
		blockEnd, okEnd := batch5Lane3CalParseDT(batch4AsString(block["end"]))
		if !okStart || !okEnd {
			continue
		}
		if blockStart.After(cursor) {
			freeSlots = append(freeSlots, map[string]any{
				"start": batch5Lane3PyISO(cursor),
				"end":   batch5Lane3PyISO(blockStart),
			})
		}
		if blockEnd.After(cursor) {
			cursor = blockEnd
		}
	}
	if cursor.Before(windowEnd) {
		freeSlots = append(freeSlots, map[string]any{
			"start": batch5Lane3PyISO(cursor),
			"end":   batch5Lane3PyISO(windowEnd),
		})
	}
	return busy, freeSlots, conflicts
}

func batch5Lane3CalEventPath(calendarID, eventID string) string {
	return batch5Lane3CalPrefix + "/calendars/" + batch5Lane3Segments(calendarID) + "/events/" + batch5Lane3Segments(eventID)
}

func batch5Lane3CalListEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	maxResults := intArg(input, "max_results", 10)
	if maxResults < 1 || maxResults > 2500 {
		return map[string]any{"error": "max_results must be between 1 and 2500"}, nil
	}
	timeMin := stringArg(input, "time_min")
	if timeMin == "" {
		now := time.Now
		if rt != nil && rt.Now != nil {
			now = rt.Now
		}
		timeMin = now().UTC().Format(time.RFC3339)
	}
	query := map[string]string{
		"maxResults":   strconv.Itoa(maxResults),
		"singleEvents": "true",
		"orderBy":      "startTime",
		"timeMin":      timeMin,
	}
	if timeMax := stringArg(input, "time_max"); timeMax != "" {
		query["timeMax"] = timeMax
	}
	if search := stringArg(input, "query"); search != "" {
		query["q"] = search
	}
	calendarID := stringArg(input, "calendar_id")
	result, err := batch5Lane3CalCall(ctx, rt, http.MethodGet,
		batch5Lane3CalPrefix+"/calendars/"+batch5Lane3Segments(calendarID)+"/events", query, nil)
	if err != nil {
		return nil, err
	}
	payload := batch4AsMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	events := make([]any, 0)
	for _, rawItem := range batch4AsSlice(payload["items"]) {
		events = append(events, batch5Lane3CalFormatEvent(batch4AsMap(rawItem)))
	}
	return map[string]any{"calendar_id": calendarID, "events": events, "total": len(events)}, nil
}

func batch5Lane3CalGetEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	eventID := stringArg(input, "event_id")
	if eventID == "" {
		return map[string]any{"error": "event_id is required"}, nil
	}
	return batch5Lane3CalCall(ctx, rt, http.MethodGet,
		batch5Lane3CalEventPath(stringArg(input, "calendar_id"), eventID), nil, nil)
}

func batch5Lane3CalCreateEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	summary := stringArg(input, "summary")
	startTime := stringArg(input, "start_time")
	endTime := stringArg(input, "end_time")
	if summary == "" {
		return map[string]any{"error": "summary is required"}, nil
	}
	if startTime == "" {
		return map[string]any{"error": "start_time is required"}, nil
	}
	if endTime == "" {
		return map[string]any{"error": "end_time is required"}, nil
	}
	timezone := stringArg(input, "timezone")
	allDay := boolArg(input, "all_day", false)
	if timezone != "" && !allDay {
		if failure := batch5Lane3CalValidateTimezone(timezone); failure != nil {
			return failure, nil
		}
	}
	var eventBody map[string]any
	if allDay {
		if !batch5Lane3CalDateOnly.MatchString(startTime) {
			return map[string]any{"error": "all-day events require date-only format for start_time (YYYY-MM-DD)"}, nil
		}
		if !batch5Lane3CalDateOnly.MatchString(endTime) {
			return map[string]any{"error": "all-day events require date-only format for end_time (YYYY-MM-DD)"}, nil
		}
		eventBody = map[string]any{
			"summary": summary,
			"start":   map[string]any{"date": startTime},
			"end":     map[string]any{"date": endTime},
		}
	} else {
		start := map[string]any{"dateTime": startTime}
		end := map[string]any{"dateTime": endTime}
		if timezone != "" {
			start["timeZone"] = timezone
			end["timeZone"] = timezone
		}
		eventBody = map[string]any{"summary": summary, "start": start, "end": end}
	}
	if value, present := batch4OptionalString(input, "description"); present {
		eventBody["description"] = value
	}
	if value, present := batch4OptionalString(input, "location"); present {
		eventBody["location"] = value
	}
	attendees := stringSliceArg(input, "attendees")
	if len(attendees) > 0 {
		people := make([]any, 0, len(attendees))
		for _, email := range attendees {
			people = append(people, map[string]any{"email": email})
		}
		eventBody["attendees"] = people
		eventBody["conferenceData"] = map[string]any{
			"createRequest": map[string]any{
				"requestId": "meet-" + batch5Lane3RequestID(),
				"conferenceSolutionKey": map[string]any{
					"type": "hangoutsMeet",
				},
			},
		}
	}
	query := map[string]string{"sendUpdates": batch5Lane3CalSendUpdates(boolArg(input, "send_notifications", true))}
	if len(attendees) > 0 {
		query["conferenceDataVersion"] = "1"
	}
	return batch5Lane3CalCall(ctx, rt, http.MethodPost,
		batch5Lane3CalPrefix+"/calendars/"+batch5Lane3Segments(stringArg(input, "calendar_id"))+"/events",
		query, eventBody)
}

func batch5Lane3CalSendUpdates(send bool) string {
	if send {
		return "all"
	}
	return "none"
}

func batch5Lane3CalUpdateEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	eventID := stringArg(input, "event_id")
	if eventID == "" {
		return map[string]any{"error": "event_id is required"}, nil
	}
	calendarID := stringArg(input, "calendar_id")
	timezone := stringArg(input, "timezone")
	allDay := boolArg(input, "all_day", false)
	if timezone != "" && !allDay {
		if failure := batch5Lane3CalValidateTimezone(timezone); failure != nil {
			return failure, nil
		}
	}
	patch := map[string]any{}
	if value, present := batch4OptionalString(input, "summary"); present {
		patch["summary"] = value
	}
	if value, present := batch4OptionalString(input, "description"); present {
		patch["description"] = value
	}
	if value, present := batch4OptionalString(input, "location"); present {
		patch["location"] = value
	}
	removeAttendees, hasRemove := batch4OptionalStrings(input, "remove_attendees")
	attendees, hasAttendees := batch4OptionalStrings(input, "attendees")
	if hasRemove {
		current, err := batch5Lane3CalCall(ctx, rt, http.MethodGet,
			batch5Lane3CalEventPath(calendarID, eventID), nil, nil)
		if err != nil {
			return nil, err
		}
		event := batch4AsMap(current)
		if _, failed := event["error"]; failed {
			return event, nil
		}
		removeSet := map[string]bool{}
		for _, email := range removeAttendees {
			removeSet[strings.ToLower(email)] = true
		}
		remaining := make([]any, 0)
		for _, rawAttendee := range batch4AsSlice(event["attendees"]) {
			attendee := batch4AsMap(rawAttendee)
			if !removeSet[strings.ToLower(batch4AsString(attendee["email"]))] {
				remaining = append(remaining, attendee)
			}
		}
		patch["attendees"] = remaining
	} else if hasAttendees {
		people := make([]any, 0, len(attendees))
		for _, email := range attendees {
			people = append(people, map[string]any{"email": email})
		}
		patch["attendees"] = people
	}
	addMeetLink := boolArg(input, "add_meet_link", false)
	if addMeetLink {
		patch["conferenceData"] = map[string]any{
			"createRequest": map[string]any{
				"requestId": "meet-" + batch5Lane3RequestID(),
				"conferenceSolutionKey": map[string]any{
					"type": "hangoutsMeet",
				},
			},
		}
	}
	if value, present := batch4OptionalString(input, "start_time"); present {
		if allDay {
			if !batch5Lane3CalDateOnly.MatchString(value) {
				return map[string]any{"error": "all-day events require date-only format for start_time (YYYY-MM-DD)"}, nil
			}
			patch["start"] = map[string]any{"date": value}
		} else {
			start := map[string]any{"dateTime": value}
			if timezone != "" {
				start["timeZone"] = timezone
			}
			patch["start"] = start
		}
	}
	if value, present := batch4OptionalString(input, "end_time"); present {
		if allDay {
			if !batch5Lane3CalDateOnly.MatchString(value) {
				return map[string]any{"error": "all-day events require date-only format for end_time (YYYY-MM-DD)"}, nil
			}
			patch["end"] = map[string]any{"date": value}
		} else {
			end := map[string]any{"dateTime": value}
			if timezone != "" {
				end["timeZone"] = timezone
			}
			patch["end"] = end
		}
	}
	if len(patch) == 0 {
		return map[string]any{"error": "No fields to update. Provide at least one field to change."}, nil
	}
	query := map[string]string{"sendUpdates": batch5Lane3CalSendUpdates(boolArg(input, "send_notifications", true))}
	if addMeetLink || hasAttendees || hasRemove {
		query["conferenceDataVersion"] = "1"
	}
	return batch5Lane3CalCall(ctx, rt, http.MethodPatch, batch5Lane3CalEventPath(calendarID, eventID), query, patch)
}

func batch5Lane3CalDeleteEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	eventID := stringArg(input, "event_id")
	if eventID == "" {
		return map[string]any{"error": "event_id is required"}, nil
	}
	query := map[string]string{"sendUpdates": batch5Lane3CalSendUpdates(boolArg(input, "send_notifications", true))}
	status, raw, err := batch5Lane3Do(ctx, rt, batch5Lane3CalModule, http.MethodDelete,
		batch5Lane3CalEventPath(stringArg(input, "calendar_id"), eventID), query, nil)
	if err != nil {
		return batch5Lane3TransportError(ctx, err), nil
	}
	if status == http.StatusNoContent {
		return map[string]any{"success": true, "message": "Event " + eventID + " deleted"}, nil
	}
	return batch5Lane3CalResponse(status, raw), nil
}

func batch5Lane3CalListCalendars(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	maxResults := intArg(input, "max_results", 100)
	if maxResults < 1 || maxResults > 250 {
		return map[string]any{"error": "max_results must be between 1 and 250"}, nil
	}
	result, err := batch5Lane3CalCall(ctx, rt, http.MethodGet, batch5Lane3CalPrefix+"/users/me/calendarList",
		map[string]string{"maxResults": strconv.Itoa(maxResults)}, nil)
	if err != nil {
		return nil, err
	}
	payload := batch4AsMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	calendars := make([]any, 0)
	for _, rawItem := range batch4AsSlice(payload["items"]) {
		item := batch4AsMap(rawItem)
		calendars = append(calendars, map[string]any{
			"id":               item["id"],
			"summary":          item["summary"],
			"description":      item["description"],
			"primary":          batch4AsBool(item["primary"]),
			"access_role":      item["accessRole"],
			"background_color": item["backgroundColor"],
		})
	}
	return map[string]any{"calendars": calendars, "total": len(calendars)}, nil
}

func batch5Lane3CalGetCalendar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	calendarID := stringArg(input, "calendar_id")
	if calendarID == "" {
		return map[string]any{"error": "calendar_id is required"}, nil
	}
	return batch5Lane3CalCall(ctx, rt, http.MethodGet,
		batch5Lane3CalPrefix+"/calendars/"+batch5Lane3Segments(calendarID), nil, nil)
}

func batch5Lane3CalCheckAvailability(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := batch5Lane3CalCredentialError(rt); failure != nil {
		return failure, nil
	}
	timeMin := stringArg(input, "time_min")
	timeMax := stringArg(input, "time_max")
	if timeMin == "" {
		return map[string]any{"error": "time_min is required"}, nil
	}
	if timeMax == "" {
		return map[string]any{"error": "time_max is required"}, nil
	}
	calendarIDs := []string{"primary"}
	if raw, present := input["calendars"]; present && raw != nil {
		calendarIDs = stringSliceArg(input, "calendars")
	}
	windowStart, startOK := batch5Lane3CalParseDT(timeMin)
	windowEnd, endOK := batch5Lane3CalParseDT(timeMax)
	if !startOK || !endOK {
		return map[string]any{"error": "time_min and time_max must be ISO 8601 timestamps"}, nil
	}
	formatted := map[string]any{}
	for _, calendarID := range calendarIDs {
		query := map[string]string{
			"timeMin":      timeMin,
			"timeMax":      timeMax,
			"singleEvents": "true",
			"orderBy":      "startTime",
			"maxResults":   "250",
		}
		result, err := batch5Lane3CalCall(ctx, rt, http.MethodGet,
			batch5Lane3CalPrefix+"/calendars/"+batch5Lane3Segments(calendarID)+"/events", query, nil)
		if err != nil {
			formatted[calendarID] = map[string]any{"error": err.Error()}
			continue
		}
		payload := batch4AsMap(result)
		if message, failed := payload["error"]; failed {
			formatted[calendarID] = map[string]any{"error": message}
			continue
		}
		events := make([]any, 0)
		for _, rawItem := range batch4AsSlice(payload["items"]) {
			events = append(events, batch5Lane3CalAvailabilityEvent(batch4AsMap(rawItem)))
		}
		busy, freeSlots, conflicts := batch5Lane3CalBusyFree(events, windowStart, windowEnd)
		formatted[calendarID] = map[string]any{
			"events":     events,
			"busy":       busy,
			"free_slots": freeSlots,
			"conflicts":  conflicts,
		}
	}
	return map[string]any{
		"time_min":  timeMin,
		"time_max":  timeMax,
		"timezone":  stringArg(input, "timezone"),
		"calendars": formatted,
	}, nil
}
