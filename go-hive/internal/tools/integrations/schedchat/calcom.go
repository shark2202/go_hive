package schedchat

// Cal.com: bookings, event types, availability and schedules (9 actions).

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// --------------------------------------------------------------- calcom ---

// calcomTool mirrors gen_catalog.py output for calcom (9 actions).
var calcomTool = &Tool{
	ID:          "calcom_tool",
	Name:        "calcom",
	DisplayName: "Cal.com",
	Summary:     "Cal.com integration (9 actions).",
	Actions: []Action{
		{
			Name:     "calcom_cancel_booking",
			Summary:  "Cancel an existing booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"booking_id"},
			Params: map[string]any{
				"booking_id": param("integer"),
				"reason":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calcom_create_booking",
			Summary:  "Create a new booking for an event type.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id", "start", "name", "email"},
			Params: map[string]any{
				"event_type_id": param("integer"),
				"start":         param("string"),
				"name":          param("string"),
				"email":         param("string"),
				"timezone":      paramDefault("string", "UTC"),
				"language":      paramDefault("string", "en"),
				"notes":         paramDefault("string", ""),
				"guests":        param("array"),
			},
		},
		{
			Name:     "calcom_get_availability",
			Summary:  "Get available time slots for booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id", "start_time", "end_time"},
			Params: map[string]any{
				"event_type_id": param("integer"),
				"start_time":    param("string"),
				"end_time":      param("string"),
				"timezone":      paramDefault("string", "UTC"),
			},
		},
		{
			Name:     "calcom_get_booking",
			Summary:  "Get detailed information about a specific booking.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"booking_id"},
			Params: map[string]any{
				"booking_id": param("integer"),
			},
		},
		{
			Name:     "calcom_get_event_type",
			Summary:  "Get detailed information about an event type.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"event_type_id"},
			Params: map[string]any{
				"event_type_id": param("integer"),
			},
		},
		{
			Name:    "calcom_list_bookings",
			Summary: "List Cal.com bookings with optional filters.",
			Source:  "calcom_tool/calcom_tool.py",
			Params: map[string]any{
				"status":        paramDefault("string", ""),
				"event_type_id": paramDefault("integer", 0),
				"start_date":    paramDefault("string", ""),
				"end_date":      paramDefault("string", ""),
				"limit":         paramDefault("integer", 50),
			},
		},
		{
			Name:    "calcom_list_event_types",
			Summary: "List all configured event types.",
			Source:  "calcom_tool/calcom_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:    "calcom_list_schedules",
			Summary: "List all availability schedules for the authenticated user.",
			Source:  "calcom_tool/calcom_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "calcom_update_schedule",
			Summary:  "Update a user's availability schedule.",
			Source:   "calcom_tool/calcom_tool.py",
			Required: []string{"schedule_id"},
			Params: map[string]any{
				"schedule_id":  param("integer"),
				"name":         paramDefault("string", ""),
				"timezone":     paramDefault("string", ""),
				"availability": param("array"),
			},
		},
	},
}

// calcomImpls binds every calcom action to its implementation.
func calcomImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"calcom_cancel_booking":   calcomCancelBooking,
		"calcom_create_booking":   calcomCreateBooking,
		"calcom_get_availability": calcomGetAvailability,
		"calcom_get_booking":      calcomGetBooking,
		"calcom_get_event_type":   calcomGetEventType,
		"calcom_list_bookings":    calcomListBookings,
		"calcom_list_event_types": calcomListEventTypes,
		"calcom_list_schedules":   calcomListSchedules,
		"calcom_update_schedule":  calcomUpdateSchedule,
	}
}

// calcomListBookings lists bookings with filters.
func calcomListBookings(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	query := url.Values{}
	query.Set("limit", strconv.Itoa(intArg(input, "limit", 50)))
	if status := stringArg(input, "status"); status != "" {
		query.Set("status", status)
	}
	if eventTypeID := intArg(input, "event_type_id", 0); eventTypeID != 0 {
		query.Set("eventTypeId", strconv.Itoa(eventTypeID))
	}
	if start := stringArg(input, "start_date"); start != "" {
		query.Set("afterStart", start)
	}
	if end := stringArg(input, "end_date"); end != "" {
		query.Set("beforeEnd", end)
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet, "/bookings", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomGetBooking reads one booking.
func calcomGetBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet,
		"/bookings/"+strconv.Itoa(intArg(input, "booking_id", 0)), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomCreateBooking creates a booking.
func calcomCreateBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	start := stringArg(input, "start")
	if start == "" {
		return map[string]any{"error": "start time is required"}, nil
	}
	name := stringArg(input, "name")
	if name == "" {
		return map[string]any{"error": "name is required"}, nil
	}
	email := stringArg(input, "email")
	if email == "" {
		return map[string]any{"error": "email is required"}, nil
	}
	responses := map[string]any{"name": name, "email": email}
	if notes := stringArg(input, "notes"); notes != "" {
		responses["notes"] = notes
	}
	if guests := stringList(input["guests"]); guests != nil {
		responses["guests"] = guests
	}
	body := map[string]any{
		"eventTypeId": eventTypeID,
		"start":       start,
		"responses":   responses,
		"timeZone":    stringArg(input, "timezone"),
		"language":    stringArg(input, "language"),
		"metadata":    map[string]any{},
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodPost, "/bookings", nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomCancelBooking cancels a booking.
func calcomCancelBooking(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	bookingID := intArg(input, "booking_id", 0)
	if bookingID == 0 {
		return map[string]any{"error": "booking_id is required"}, nil
	}
	var body any
	if reason := stringArg(input, "reason"); reason != "" {
		body = map[string]any{"cancellationReason": reason}
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodDelete,
		"/bookings/"+strconv.Itoa(bookingID), nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomGetAvailability reads available slots.
func calcomGetAvailability(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	start := stringArg(input, "start_time")
	end := stringArg(input, "end_time")
	if start == "" || end == "" {
		return map[string]any{"error": "start_time and end_time are required"}, nil
	}
	query := url.Values{}
	query.Set("eventTypeId", strconv.Itoa(eventTypeID))
	query.Set("startTime", start)
	query.Set("endTime", end)
	query.Set("timeZone", stringArg(input, "timezone"))
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet, "/slots", query, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomUpdateSchedule updates a schedule.
func calcomUpdateSchedule(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	scheduleID := intArg(input, "schedule_id", 0)
	if scheduleID == 0 {
		return map[string]any{"error": "schedule_id is required"}, nil
	}
	body := map[string]any{}
	if name := stringArg(input, "name"); name != "" {
		body["name"] = name
	}
	if timezone := stringArg(input, "timezone"); timezone != "" {
		body["timeZone"] = timezone
	}
	if availability, ok := input["availability"].([]any); ok && len(availability) > 0 {
		body["availability"] = availability
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodPatch,
		"/schedules/"+strconv.Itoa(scheduleID), nil, body)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomListSchedules lists schedules.
func calcomListSchedules(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet, "/schedules", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomListEventTypes lists event types.
func calcomListEventTypes(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet, "/event-types", nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}

// calcomGetEventType reads one event type.
func calcomGetEventType(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if errDict := requireModule(rt, "calcom_tool"); errDict != nil {
		return errDict, nil
	}
	eventTypeID := intArg(input, "event_type_id", 0)
	if eventTypeID == 0 {
		return map[string]any{"error": "event_type_id is required"}, nil
	}
	parsed, errDict := call(ctx, rt, "calcom_tool", http.MethodGet,
		"/event-types/"+strconv.Itoa(eventTypeID), nil, nil)
	if errDict != nil {
		return errDict, nil
	}
	return parsed, nil
}
