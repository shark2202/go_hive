package integrations

import (
	"net/http"
	"testing"
)

// Parity: tools/tests/tools/test_calendar_tool.py::TestMockedAPIResponses::test_list_events_success
func TestBatch5Lane3CalendarListEvents(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/calendar/v3/calendars/primary/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("singleEvents") != "true" || query.Get("orderBy") != "startTime" || query.Get("maxResults") != "10" {
			t.Fatalf("query = %v", query)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"items": []any{
			map[string]any{
				"id":       "evt-1",
				"summary":  "Standup",
				"start":    map[string]any{"dateTime": "2024-01-15T09:00:00Z"},
				"end":      map[string]any{"dateTime": "2024-01-15T09:30:00Z"},
				"location": "Room 1",
				"status":   "confirmed",
				"htmlLink": "https://calendar.google.com/event?eid=1",
				"attendees": []any{
					map[string]any{"email": "a@example.com"},
					map[string]any{"email": "b@example.com"},
				},
			},
			map[string]any{
				"id":      "evt-2",
				"summary": "Holiday",
				"start":   map[string]any{"date": "2024-01-16"},
				"end":     map[string]any{"date": "2024-01-17"},
			},
		}})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_list_events", "time_min": "2024-01-15T00:00:00Z",
	})
	if result["total"] != 2 || result["calendar_id"] != "primary" {
		t.Fatalf("result = %v", result)
	}
	events := batch4BodySlice(t, result["events"])
	first := batch4AsMap(events[0])
	if first["summary"] != "Standup" || first["start"] != "2024-01-15T09:00:00Z" ||
		first["html_link"] != "https://calendar.google.com/event?eid=1" {
		t.Fatalf("event = %v", first)
	}
	if len(batch4BodySlice(t, first["attendees"])) != 2 {
		t.Fatalf("attendees = %v", first["attendees"])
	}
	second := batch4AsMap(events[1])
	if second["start"] != "2024-01-16" {
		t.Fatalf("event = %v", second)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestParameterValidation::test_list_events_max_results_too_high
func TestBatch5Lane3CalendarListEventsValidatesLimit(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Fatal("no request expected for an out-of-range limit")
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_list_events", "max_results": 2501,
	})
	if message := batch4ErrorText(t, result); message != "max_results must be between 1 and 2500" {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity-exempt: upstream has no get_event success test; the path follows
// calendar_get_event's URL-encoded calendar and event ids.
func TestBatch5Lane3CalendarGetEvent(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/calendar/v3/calendars/team@example.com/events/evt-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "evt-1", "summary": "Standup"})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_get_event", "event_id": "evt-1", "calendar_id": "team@example.com",
	})
	if result["summary"] != "Standup" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestMockedAPIResponses::test_create_event_success
func TestBatch5Lane3CalendarCreateEvent(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPost || r.URL.Path != "/calendar/v3/calendars/primary/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("sendUpdates") != "all" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "evt-new", "summary": "Planning"})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_create_event", "summary": "Planning",
		"start_time": "2024-01-15T09:00:00", "end_time": "2024-01-15T10:00:00",
		"timezone": "UTC", "description": "Quarterly planning",
	})
	if result["id"] != "evt-new" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["summary"] != "Planning" || body["description"] != "Quarterly planning" {
		t.Fatalf("body = %v", body)
	}
	start := batch4AsMap(body["start"])
	if start["dateTime"] != "2024-01-15T09:00:00" || start["timeZone"] != "UTC" {
		t.Fatalf("start = %v", start)
	}
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestCreateEventWithAttendees::test_create_event_with_attendees_includes_conference_data
func TestBatch5Lane3CalendarCreateEventWithAttendees(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "evt-new"})
	})
	batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_create_event", "summary": "Sync",
		"start_time": "2024-01-15T09:00:00Z", "end_time": "2024-01-15T09:30:00Z",
		"attendees": []any{"a@example.com"},
	})
	body := recorder.last(t).Body
	attendees := batch4BodySlice(t, body["attendees"])
	if len(attendees) != 1 || batch4AsMap(attendees[0])["email"] != "a@example.com" {
		t.Fatalf("attendees = %v", attendees)
	}
	conference := batch4AsMap(batch4AsMap(body["conferenceData"])["createRequest"])
	if batch4AsMap(conference["conferenceSolutionKey"])["type"] != "hangoutsMeet" {
		t.Fatalf("conference = %v", conference)
	}
	if recorder.last(t).Query.Get("conferenceDataVersion") != "1" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestUpdateEventPatch::test_update_event_patch_success
func TestBatch5Lane3CalendarUpdateEvent(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		if r.Method != http.MethodPatch || r.URL.Path != "/calendar/v3/calendars/primary/events/evt-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "evt-1", "summary": "Updated"})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_update_event", "event_id": "evt-1", "summary": "Updated",
	})
	if result["summary"] != "Updated" {
		t.Fatalf("result = %v", result)
	}
	body := recorder.last(t).Body
	if body["summary"] != "Updated" {
		t.Fatalf("body = %v", body)
	}
	if _, present := body["start"]; present {
		t.Fatalf("unexpected start in %v", body)
	}
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestEmptyPatchGuard::test_update_event_no_fields_returns_error
func TestBatch5Lane3CalendarUpdateEventRequiresFields(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		t.Fatal("no request expected without patch fields")
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_update_event", "event_id": "evt-1",
	})
	if message := batch4ErrorText(t, result); message != "No fields to update. Provide at least one field to change." {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 0 {
		t.Fatalf("requests = %d, want 0", recorder.count())
	}
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestRemoveAttendees::test_remove_single_attendee
func TestBatch5Lane3CalendarUpdateEventRemovesAttendee(t *testing.T) {
	calls := 0
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		calls++
		if calls == 1 {
			if r.Method != http.MethodGet {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "evt-1",
				"attendees": []any{
					map[string]any{"email": "keep@example.com"},
					map[string]any{"email": "drop@example.com"},
				},
			})
			return
		}
		if r.Method != http.MethodPatch {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "evt-1"})
	})
	batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_update_event", "event_id": "evt-1",
		"remove_attendees": []any{"drop@example.com"},
	})
	remaining := batch4BodySlice(t, recorder.last(t).Body["attendees"])
	if len(remaining) != 1 || batch4AsMap(remaining[0])["email"] != "keep@example.com" {
		t.Fatalf("attendees = %v", remaining)
	}
	if recorder.last(t).Query.Get("conferenceDataVersion") != "1" {
		t.Fatalf("query = %v", recorder.last(t).Query)
	}
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestMockedAPIResponses::test_delete_event_success
func TestBatch5Lane3CalendarDeleteEvent(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodDelete || r.URL.Path != "/calendar/v3/calendars/primary/events/evt-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("sendUpdates") != "none" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		w.WriteHeader(http.StatusNoContent)
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_delete_event", "event_id": "evt-1", "send_notifications": false,
	})
	if result["success"] != true || result["message"] != "Event evt-1 deleted" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestMockedAPIResponses::test_list_calendars_success
func TestBatch5Lane3CalendarListCalendars(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/calendar/v3/users/me/calendarList" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"items": []any{
			map[string]any{
				"id": "primary", "summary": "Me", "primary": true,
				"accessRole": "owner", "backgroundColor": "#ffffff",
			},
		}})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if result["total"] != 1 {
		t.Fatalf("result = %v", result)
	}
	calendar := batch4AsMap(batch4BodySlice(t, result["calendars"])[0])
	if calendar["id"] != "primary" || calendar["primary"] != true || calendar["access_role"] != "owner" {
		t.Fatalf("calendar = %v", calendar)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: upstream has no get_calendar success test; the path follows
// calendar_get_calendar's URL-encoded id.
func TestBatch5Lane3CalendarGetCalendar(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/calendar/v3/calendars/cal-1" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"id": "cal-1", "summary": "Team"})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action": "calendar_get_calendar", "calendar_id": "cal-1",
	})
	if result["summary"] != "Team" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity: tools/tests/tools/test_calendar_tool.py::TestMockedAPIResponses::test_check_availability_detects_conflicts
func TestBatch5Lane3CalendarCheckAvailability(t *testing.T) {
	registry, recorder := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		if r.Method != http.MethodGet || r.URL.Path != "/calendar/v3/calendars/primary/events" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("maxResults") != "250" {
			t.Fatalf("query = %v", r.URL.Query())
		}
		batch4WriteJSON(w, http.StatusOK, map[string]any{"items": []any{
			map[string]any{
				"summary": "Standup",
				"start":   map[string]any{"dateTime": "2024-01-15T09:00:00Z"},
				"end":     map[string]any{"dateTime": "2024-01-15T10:00:00Z"},
			},
			map[string]any{
				"summary": "Review",
				"start":   map[string]any{"dateTime": "2024-01-15T09:30:00Z"},
				"end":     map[string]any{"dateTime": "2024-01-15T10:30:00Z"},
			},
			map[string]any{
				"summary": "Holiday",
				"start":   map[string]any{"date": "2024-01-15"},
				"end":     map[string]any{"date": "2024-01-16"},
			},
			map[string]any{
				"summary":      "Focus",
				"transparency": "transparent",
				"start":        map[string]any{"dateTime": "2024-01-15T10:45:00Z"},
				"end":          map[string]any{"dateTime": "2024-01-15T10:55:00Z"},
			},
		}})
	})
	result := batch4Execute(t, registry, "calendar", map[string]any{
		"action":   "calendar_check_availability",
		"time_min": "2024-01-15T09:00:00Z", "time_max": "2024-01-15T11:00:00Z",
	})
	calendars := batch4AsMap(result["calendars"])
	day := batch4AsMap(calendars["primary"])
	if len(batch4BodySlice(t, day["events"])) != 4 {
		t.Fatalf("events = %v", day["events"])
	}
	busy := batch4BodySlice(t, day["busy"])
	if len(busy) != 1 {
		t.Fatalf("busy = %v", busy)
	}
	block := batch4AsMap(busy[0])
	if block["start"] != "2024-01-15T09:00:00+00:00" || block["end"] != "2024-01-15T10:30:00+00:00" {
		t.Fatalf("busy = %v", block)
	}
	conflicts := batch4BodySlice(t, day["conflicts"])
	if len(conflicts) != 1 || len(batch4BodySlice(t, batch4AsMap(conflicts[0])["events"])) != 2 {
		t.Fatalf("conflicts = %v", conflicts)
	}
	free := batch4BodySlice(t, day["free_slots"])
	if len(free) != 1 || batch4AsMap(free[0])["start"] != "2024-01-15T10:30:00+00:00" {
		t.Fatalf("free = %v", free)
	}
	if result["timezone"] != "UTC" {
		t.Fatalf("result = %v", result)
	}
	batch5Lane3RequireBearer(t, recorder)
}

// Parity-exempt: Go-side response mapping; upstream tests mock one status at a
// time and do not cover an empty 2xx body.
func TestBatch5Lane3CalendarErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		message string
	}{
		{"unauthorized", http.StatusUnauthorized, `{}`, "Invalid or expired OAuth token"},
		{"forbidden", http.StatusForbidden, `{}`, "Access denied. Check calendar permissions."},
		{"not found", http.StatusNotFound, `{}`, "Resource not found"},
		{"rate limit", http.StatusTooManyRequests, `{}`, "Rate limit exceeded"},
		{"api error", http.StatusBadRequest, `{"error":{"message":"bad time"}}`, "API error: bad time"},
		{"empty", http.StatusOK, ``, "invalid JSON response"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			registry, _ := batch5Lane3TestRegistry(t, func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(testCase.body))
			})
			result := batch4Execute(t, registry, "calendar", map[string]any{
				"action": "calendar_get_event", "event_id": "evt-1",
			})
			if message := batch4ErrorText(t, result); !containsAll(message, testCase.message) {
				t.Fatalf("error = %q, want %q", message, testCase.message)
			}
		})
	}
}
