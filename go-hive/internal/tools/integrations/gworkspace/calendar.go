// Google Calendar actions for batch 5, lane 3 (upstream calendar_tool,
// 8 actions). See google_workspace.go for the registry and shared helpers.
package gworkspace

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

var calDateOnly = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func calendarImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calendar_list_events":        calListEvents,
		"calendar_get_event":          calGetEvent,
		"calendar_create_event":       calCreateEvent,
		"calendar_update_event":       calUpdateEvent,
		"calendar_delete_event":       calDeleteEvent,
		"calendar_list_calendars":     calListCalendars,
		"calendar_get_calendar":       calGetCalendar,
		"calendar_check_availability": calCheckAvailability,
	}
}

func calAuthError() map[string]any {
	return map[string]any{
		"error": "Calendar credentials not configured",
		"help":  "Set GOOGLE_ACCESS_TOKEN environment variable",
	}
}

// calCredentialError mirrors _check_credentials, which every
// calendar action runs before validating its arguments.
func calCredentialError(rt *Runtime) map[string]any {
	if !accountCredential(rt, calModule) {
		return calAuthError()
	}
	return nil
}

// calResponse mirrors calendar_tool._handle_response.
func calResponse(status int, raw []byte) map[string]any {
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
		if payload, ok := jsonObject(raw); ok {
			if errorObject, ok := payload["error"].(map[string]any); ok {
				if message, ok := errorObject["message"].(string); ok {
					return map[string]any{"error": "API error: " + message}
				}
			}
		}
		return map[string]any{"error": fmt.Sprintf("API request failed: HTTP %d", status)}
	}
	payload, ok := jsonObject(raw)
	if !ok {
		return map[string]any{"error": "API request failed: invalid JSON response"}
	}
	return payload
}

func calCall(ctx context.Context, rt *Runtime, method, path string, query map[string]string, body any) (any, error) {
	if !accountCredential(rt, calModule) {
		return calAuthError(), nil
	}
	status, raw, err := do(ctx, rt, calModule, method, path, query, body)
	if err != nil {
		return transportError(ctx, err), nil
	}
	return calResponse(status, raw), nil
}

// calValidateTimezone mirrors the available_timezones() check.
func calValidateTimezone(name string) map[string]any {
	if _, err := time.LoadLocation(name); err != nil {
		return map[string]any{
			"error": fmt.Sprintf("Invalid timezone '%s'. Use IANA format (e.g., 'America/New_York')", name),
		}
	}
	return nil
}

// calParseDT parses an ISO 8601 timestamp and treats naive values
// as UTC, like _parse_event_dt.
func calParseDT(text string) (time.Time, bool) {
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

// calFormatEvent shapes the event list output.
func calFormatEvent(item map[string]any) map[string]any {
	start := asMap(item["start"])
	end := asMap(item["end"])
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
	if attendees := asSlice(item["attendees"]); len(attendees) > 0 {
		emails := make([]any, 0, len(attendees))
		for _, rawAttendee := range attendees {
			emails = append(emails, asMap(rawAttendee)["email"])
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

// calAvailabilityEvent shapes one event for availability math.
func calAvailabilityEvent(item map[string]any) map[string]any {
	start := asMap(item["start"])
	end := asMap(item["end"])
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

type calInterval struct {
	start time.Time
	end   time.Time
	name  string
}

// calBusyFree merges overlapping events into busy blocks and derives
// free slots and conflicts, mirroring _compute_busy_free_conflicts.
func calBusyFree(events []any, windowStart, windowEnd time.Time) ([]any, []any, []any) {
	intervals := make([]calInterval, 0, len(events))
	for _, rawEvent := range events {
		event := asMap(rawEvent)
		if asString(event["transparency"]) == "transparent" || asString(event["status"]) == "cancelled" {
			continue
		}
		startText := asString(event["start"])
		endText := asString(event["end"])
		if startText == "" || endText == "" {
			continue
		}
		if calDateOnly.MatchString(startText) || calDateOnly.MatchString(endText) {
			continue
		}
		start, startOK := calParseDT(startText)
		end, endOK := calParseDT(endText)
		if !startOK || !endOK {
			continue
		}
		intervals = append(intervals, calInterval{start: start, end: end, name: asString(event["summary"])})
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
					"overlap_start": pyISO(curStart),
					"overlap_end":   pyISO(curEnd),
				})
			}
			busy = append(busy, map[string]any{
				"start": pyISO(curStart),
				"end":   pyISO(curEnd),
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
		block := asMap(rawBlock)
		blockStart, okStart := calParseDT(asString(block["start"]))
		blockEnd, okEnd := calParseDT(asString(block["end"]))
		if !okStart || !okEnd {
			continue
		}
		if blockStart.After(cursor) {
			freeSlots = append(freeSlots, map[string]any{
				"start": pyISO(cursor),
				"end":   pyISO(blockStart),
			})
		}
		if blockEnd.After(cursor) {
			cursor = blockEnd
		}
	}
	if cursor.Before(windowEnd) {
		freeSlots = append(freeSlots, map[string]any{
			"start": pyISO(cursor),
			"end":   pyISO(windowEnd),
		})
	}
	return busy, freeSlots, conflicts
}

func calEventPath(calendarID, eventID string) string {
	return calPrefix + "/calendars/" + segments(calendarID) + "/events/" + segments(eventID)
}

func calListEvents(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
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
	result, err := calCall(ctx, rt, http.MethodGet,
		calPrefix+"/calendars/"+segments(calendarID)+"/events", query, nil)
	if err != nil {
		return nil, err
	}
	payload := asMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	events := make([]any, 0)
	for _, rawItem := range asSlice(payload["items"]) {
		events = append(events, calFormatEvent(asMap(rawItem)))
	}
	return map[string]any{"calendar_id": calendarID, "events": events, "total": len(events)}, nil
}

func calGetEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
		return failure, nil
	}
	eventID := stringArg(input, "event_id")
	if eventID == "" {
		return map[string]any{"error": "event_id is required"}, nil
	}
	return calCall(ctx, rt, http.MethodGet,
		calEventPath(stringArg(input, "calendar_id"), eventID), nil, nil)
}

func calCreateEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
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
		if failure := calValidateTimezone(timezone); failure != nil {
			return failure, nil
		}
	}
	var eventBody map[string]any
	if allDay {
		if !calDateOnly.MatchString(startTime) {
			return map[string]any{"error": "all-day events require date-only format for start_time (YYYY-MM-DD)"}, nil
		}
		if !calDateOnly.MatchString(endTime) {
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
	if value, present := optionalString(input, "description"); present {
		eventBody["description"] = value
	}
	if value, present := optionalString(input, "location"); present {
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
				"requestId": "meet-" + requestID(),
				"conferenceSolutionKey": map[string]any{
					"type": "hangoutsMeet",
				},
			},
		}
	}
	query := map[string]string{"sendUpdates": calSendUpdates(boolArg(input, "send_notifications", true))}
	if len(attendees) > 0 {
		query["conferenceDataVersion"] = "1"
	}
	return calCall(ctx, rt, http.MethodPost,
		calPrefix+"/calendars/"+segments(stringArg(input, "calendar_id"))+"/events",
		query, eventBody)
}

func calSendUpdates(send bool) string {
	if send {
		return "all"
	}
	return "none"
}

func calUpdateEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
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
		if failure := calValidateTimezone(timezone); failure != nil {
			return failure, nil
		}
	}
	patch := map[string]any{}
	if value, present := optionalString(input, "summary"); present {
		patch["summary"] = value
	}
	if value, present := optionalString(input, "description"); present {
		patch["description"] = value
	}
	if value, present := optionalString(input, "location"); present {
		patch["location"] = value
	}
	removeAttendees, hasRemove := notionOptionalStrings(input, "remove_attendees")
	attendees, hasAttendees := notionOptionalStrings(input, "attendees")
	if hasRemove {
		current, err := calCall(ctx, rt, http.MethodGet,
			calEventPath(calendarID, eventID), nil, nil)
		if err != nil {
			return nil, err
		}
		event := asMap(current)
		if _, failed := event["error"]; failed {
			return event, nil
		}
		removeSet := map[string]bool{}
		for _, email := range removeAttendees {
			removeSet[strings.ToLower(email)] = true
		}
		remaining := make([]any, 0)
		for _, rawAttendee := range asSlice(event["attendees"]) {
			attendee := asMap(rawAttendee)
			if !removeSet[strings.ToLower(asString(attendee["email"]))] {
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
				"requestId": "meet-" + requestID(),
				"conferenceSolutionKey": map[string]any{
					"type": "hangoutsMeet",
				},
			},
		}
	}
	if value, present := optionalString(input, "start_time"); present {
		if allDay {
			if !calDateOnly.MatchString(value) {
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
	if value, present := optionalString(input, "end_time"); present {
		if allDay {
			if !calDateOnly.MatchString(value) {
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
	query := map[string]string{"sendUpdates": calSendUpdates(boolArg(input, "send_notifications", true))}
	if addMeetLink || hasAttendees || hasRemove {
		query["conferenceDataVersion"] = "1"
	}
	return calCall(ctx, rt, http.MethodPatch, calEventPath(calendarID, eventID), query, patch)
}

func calDeleteEvent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
		return failure, nil
	}
	eventID := stringArg(input, "event_id")
	if eventID == "" {
		return map[string]any{"error": "event_id is required"}, nil
	}
	query := map[string]string{"sendUpdates": calSendUpdates(boolArg(input, "send_notifications", true))}
	status, raw, err := do(ctx, rt, calModule, http.MethodDelete,
		calEventPath(stringArg(input, "calendar_id"), eventID), query, nil)
	if err != nil {
		return transportError(ctx, err), nil
	}
	if status == http.StatusNoContent {
		return map[string]any{"success": true, "message": "Event " + eventID + " deleted"}, nil
	}
	return calResponse(status, raw), nil
}

func calListCalendars(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
		return failure, nil
	}
	maxResults := intArg(input, "max_results", 100)
	if maxResults < 1 || maxResults > 250 {
		return map[string]any{"error": "max_results must be between 1 and 250"}, nil
	}
	result, err := calCall(ctx, rt, http.MethodGet, calPrefix+"/users/me/calendarList",
		map[string]string{"maxResults": strconv.Itoa(maxResults)}, nil)
	if err != nil {
		return nil, err
	}
	payload := asMap(result)
	if _, failed := payload["error"]; failed {
		return payload, nil
	}
	calendars := make([]any, 0)
	for _, rawItem := range asSlice(payload["items"]) {
		item := asMap(rawItem)
		calendars = append(calendars, map[string]any{
			"id":               item["id"],
			"summary":          item["summary"],
			"description":      item["description"],
			"primary":          asBool(item["primary"]),
			"access_role":      item["accessRole"],
			"background_color": item["backgroundColor"],
		})
	}
	return map[string]any{"calendars": calendars, "total": len(calendars)}, nil
}

func calGetCalendar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
		return failure, nil
	}
	calendarID := stringArg(input, "calendar_id")
	if calendarID == "" {
		return map[string]any{"error": "calendar_id is required"}, nil
	}
	return calCall(ctx, rt, http.MethodGet,
		calPrefix+"/calendars/"+segments(calendarID), nil, nil)
}

func calCheckAvailability(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if failure := calCredentialError(rt); failure != nil {
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
	windowStart, startOK := calParseDT(timeMin)
	windowEnd, endOK := calParseDT(timeMax)
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
		result, err := calCall(ctx, rt, http.MethodGet,
			calPrefix+"/calendars/"+segments(calendarID)+"/events", query, nil)
		if err != nil {
			formatted[calendarID] = map[string]any{"error": err.Error()}
			continue
		}
		payload := asMap(result)
		if message, failed := payload["error"]; failed {
			formatted[calendarID] = map[string]any{"error": message}
			continue
		}
		events := make([]any, 0)
		for _, rawItem := range asSlice(payload["items"]) {
			events = append(events, calAvailabilityEvent(asMap(rawItem)))
		}
		busy, freeSlots, conflicts := calBusyFree(events, windowStart, windowEnd)
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
