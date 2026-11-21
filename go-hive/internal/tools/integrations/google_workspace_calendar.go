// Google Calendar catalog entry for the batch-5 lane-3 Google Workspace
// registry (split from google_workspace.go).

package integrations

var batch5Lane3CalendarTool = &Tool{
	ID:          batch5Lane3CalModule,
	Name:        "calendar",
	DisplayName: "Google Calendar",
	Summary:     "Google Calendar integration (8 actions).",
	Actions: []Action{
		{
			Name:    "calendar_list_events",
			Summary: "List upcoming calendar events.",
			Source:  "calendar_tool/calendar_tool.py",
			Params: map[string]any{
				"calendar_id": paramDefault("string", "primary"),
				"time_min":    param("string"),
				"time_max":    param("string"),
				"max_results": paramDefault("integer", 10),
				"query":       param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_get_event",
			Summary:  "Get details of a specific calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":    param("string"),
				"calendar_id": paramDefault("string", "primary"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_create_event",
			Summary:  "Create a new calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"summary", "start_time", "end_time"},
			Params: map[string]any{
				"summary":            param("string"),
				"start_time":         param("string"),
				"end_time":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"description":        param("string"),
				"location":           param("string"),
				"attendees":          param("array"),
				"send_notifications": paramDefault("boolean", true),
				"timezone":           param("string"),
				"all_day":            paramDefault("boolean", false),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_update_event",
			Summary:  "Update an existing calendar event. Only provided fields are changed.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"summary":            param("string"),
				"start_time":         param("string"),
				"end_time":           param("string"),
				"description":        param("string"),
				"location":           param("string"),
				"attendees":          param("array"),
				"remove_attendees":   param("array"),
				"send_notifications": paramDefault("boolean", true),
				"timezone":           param("string"),
				"all_day":            paramDefault("boolean", false),
				"add_meet_link":      paramDefault("boolean", false),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_delete_event",
			Summary:  "Delete a calendar event.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"event_id"},
			Params: map[string]any{
				"event_id":           param("string"),
				"calendar_id":        paramDefault("string", "primary"),
				"send_notifications": paramDefault("boolean", true),
				"account":            paramDefault("string", ""),
			},
		},
		{
			Name:    "calendar_list_calendars",
			Summary: "List all calendars accessible to the user.",
			Source:  "calendar_tool/calendar_tool.py",
			Params: map[string]any{
				"max_results": paramDefault("integer", 100),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_get_calendar",
			Summary:  "Get details of a specific calendar.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"calendar_id"},
			Params: map[string]any{
				"calendar_id": param("string"),
				"account":     paramDefault("string", ""),
			},
		},
		{
			Name:     "calendar_check_availability",
			Summary:  "Check availability by listing actual events in the time range.",
			Source:   "calendar_tool/calendar_tool.py",
			Required: []string{"time_min", "time_max"},
			Params: map[string]any{
				"time_min":  param("string"),
				"time_max":  param("string"),
				"calendars": param("array"),
				"timezone":  paramDefault("string", "UTC"),
				"account":   paramDefault("string", ""),
			},
		},
	},
}
