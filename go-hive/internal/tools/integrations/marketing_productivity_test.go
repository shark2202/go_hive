package integrations

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// --------------------------------------------------------------- test kit ---

// batch7Lane9Server starts a fake upstream and returns it with the request
// recorder so callers can wire more than one module to the same server.
func batch7Lane9Server(t *testing.T, handler func(http.ResponseWriter, *http.Request, map[string]any)) (*httptest.Server, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(handler))
	t.Cleanup(server.Close)
	return server, recorder
}

// batch7Lane9Fake points one module at a fake upstream, injects the environment
// map and supplies "test-token" as that module's credential.
func batch7Lane9Fake(t *testing.T, module string, env map[string]string, handler func(http.ResponseWriter, *http.Request, map[string]any), opts ...Option) (*Registry, *batch4Recorder) {
	t.Helper()
	server, recorder := batch7Lane9Server(t, handler)
	options := []Option{
		WithBaseURL(module, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(name string) string { return env[name] }),
		WithCredential(module, "test-token"),
	}
	return newBatch7Lane9Registry(append(options, opts...)...), recorder
}

// batch7Lane9EnvRegistry builds a registry with only an environment map, for
// missing-credential assertions.
func batch7Lane9EnvRegistry(env map[string]string, opts ...Option) *Registry {
	options := []Option{WithGetenv(func(name string) string { return env[name] })}
	return newBatch7Lane9Registry(append(options, opts...)...)
}

// batch7Lane9Numeric reads a numeric result field that arrived as JSON.
func batch7Lane9Numeric(t *testing.T, value any) int64 {
	t.Helper()
	parsed, ok := int64Value(value)
	if !ok {
		t.Fatalf("value = %T (%v), want numeric", value, value)
	}
	return parsed
}

// ---------------------------------------------------------------- catalog ---

// Parity-exempt: catalog/install invariants are Go-side contracts; upstream
// tests exercise MCP tool functions, not this registry surface.
func TestBatch7Lane9Catalog(t *testing.T) {
	registry := newBatch7Lane9Registry()
	wantNames := []string{"zoom", "google_analytics", "google_search_console", "cloudinary", "microsoft_graph", "email"}
	names := registry.Names()
	if len(names) != len(wantNames) {
		t.Fatalf("tools = %v, want %v", names, wantNames)
	}
	for index, name := range wantNames {
		if names[index] != name {
			t.Fatalf("tools = %v, want %v", names, wantNames)
		}
	}
	if got := registry.ActionCount(); got != 45 {
		t.Fatalf("actions = %d, want 45", got)
	}
	if got := registry.ImplementedActions(); got != 45 {
		t.Fatalf("implemented = %d, want 45", got)
	}
	wantCounts := map[string]int{
		"zoom": 9, "google_analytics": 7, "google_search_console": 8,
		"cloudinary": 8, "microsoft_graph": 11, "email": 2,
	}
	for _, tool := range registry.Tools() {
		if tool.ID == "" || tool.Name == "" || tool.Summary == "" {
			t.Fatalf("tool missing identity: %+v", tool)
		}
		if got := len(tool.Actions); got != wantCounts[tool.Name] {
			t.Fatalf("%s actions = %d, want %d", tool.Name, got, wantCounts[tool.Name])
		}
		for _, action := range tool.Actions {
			if action.Source == "" || action.Summary == "" {
				t.Fatalf("%s.%s missing source/summary", tool.Name, action.Name)
			}
			if registry.Impls(tool.Name)[action.Name] == nil {
				t.Fatalf("%s.%s has no implementation", tool.Name, action.Name)
			}
		}
	}
}

// Parity-exempt: installation into the shared registry is a Go-side contract.
func TestBatch7Lane9Install(t *testing.T) {
	shared := tools.NewToolRegistry()
	if installed := InstallBatch7Lane9(shared); installed != 6 {
		t.Fatalf("installed = %d, want 6", installed)
	}
	for _, name := range []string{"zoom", "google_analytics", "google_search_console", "cloudinary", "microsoft_graph", "email"} {
		if !shared.HasTool(name) {
			t.Fatalf("shared registry missing %q", name)
		}
	}
	tool, ok := shared.GetTool("zoom")
	if !ok {
		t.Fatal("zoom tool missing")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v", tool.Parameters["properties"])
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("zoom missing action selector: %v", properties)
	}
	if enum, _ := action["enum"].([]string); len(enum) != 9 {
		t.Fatalf("zoom action enum = %v", action["enum"])
	}
	email, ok := shared.GetTool("email")
	if !ok {
		t.Fatal("email tool missing")
	}
	emailProperties, _ := email.Parameters["properties"].(map[string]any)
	emailAction, ok := emailProperties["action"].(map[string]any)
	if !ok {
		t.Fatalf("email missing action selector: %v", emailProperties)
	}
	if enum, _ := emailAction["enum"].([]string); len(enum) != 2 {
		t.Fatalf("email action enum = %v", emailAction["enum"])
	}
}

// ------------------------------------------------------------------- zoom ---

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomGetUser::test_successful_get
func TestBatch7Lane9ZoomGetUser(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/users/me" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "abc123", "email": "user@example.com", "first_name": "Jane",
				"last_name": "Doe", "display_name": "Jane Doe", "type": 2,
				"timezone": "America/New_York", "status": "active",
				"account_id": "acc123", "created_at": "2024-01-01T00:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_user"})
	if result["email"] != "user@example.com" || result["display_name"] != "Jane Doe" {
		t.Fatalf("result = %v", result)
	}
	if batch7Lane9Numeric(t, result["type"]) != 2 || result["status"] != "active" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomGetUser::test_missing_credentials
func TestBatch7Lane9ZoomMissingCredential(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_user"})
	if got := batch4ErrorText(t, result); got != "Zoom credentials not configured" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomListMeetings::test_successful_list
func TestBatch7Lane9ZoomListMeetings(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/users/me/meetings" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.URL.Query().Get("type"); got != "upcoming" {
				t.Fatalf("type = %q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "30" {
				t.Fatalf("page_size = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"total_records": 1,
				"meetings": []any{map[string]any{
					"id": 78475495050, "uuid": "abc123==", "topic": "Weekly Standup",
					"type": 2, "start_time": "2025-01-21T09:20:00Z", "duration": 30,
					"timezone": "America/New_York", "join_url": "https://zoom.us/j/78475495050",
					"created_at": "2025-01-20T09:08:12Z",
				}},
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_list_meetings"})
	if batch7Lane9Numeric(t, result["total_records"]) != 1 || result["count"] != 1 {
		t.Fatalf("result = %v", result)
	}
	meetings := batch4BodySlice(t, result["meetings"])
	first := batch4BodyMap(t, meetings[0])
	if first["topic"] != "Weekly Standup" || batch7Lane9Numeric(t, first["id"]) != 78475495050 {
		t.Fatalf("meeting = %v", first)
	}
	if _, present := result["next_page_token"]; present {
		t.Fatalf("unexpected next_page_token: %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomListMeetings::test_pagination
func TestBatch7Lane9ZoomListMeetingsPagination(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if got := r.URL.Query().Get("next_page_token"); got != "token123" {
				t.Fatalf("next_page_token = %q", got)
			}
			if got := r.URL.Query().Get("page_size"); got != "300" {
				t.Fatalf("page_size = %q (want clamp to 300)", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"total_records":   50,
				"next_page_token": "token456",
				"meetings":        []any{},
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{
		"action": "zoom_list_meetings", "next_page_token": "token123", "page_size": 900,
	})
	if result["next_page_token"] != "token456" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomGetMeeting::test_successful_get
func TestBatch7Lane9ZoomGetMeeting(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/meetings/78475495050" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": 78475495050, "uuid": "abc123==", "topic": "Project Review",
				"type": 2, "start_time": "2025-03-15T14:00:00Z", "duration": 60,
				"timezone": "America/New_York", "agenda": "Review Q1",
				"join_url": "https://zoom.us/j/78475495050", "start_url": "https://zoom.us/s/78475495050",
				"password": "abc123", "host_id": "host1", "created_at": "2025-03-10T10:00:00Z",
				"settings": map[string]any{
					"host_video": true, "participant_video": true, "join_before_host": false,
					"mute_upon_entry": true, "waiting_room": true, "auto_recording": "cloud",
				},
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_meeting", "meeting_id": "78475495050"})
	if result["topic"] != "Project Review" {
		t.Fatalf("result = %v", result)
	}
	settings := batch4BodyMap(t, result["settings"])
	if settings["waiting_room"] != true || settings["auto_recording"] != "cloud" {
		t.Fatalf("settings = %v", settings)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomGetMeeting::test_missing_id
func TestBatch7Lane9ZoomMissingMeetingID(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_meeting", "meeting_id": ""})
	if got := batch4ErrorText(t, result); got != "meeting_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomCreateMeeting::test_successful_create
func TestBatch7Lane9ZoomCreateMeeting(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/users/me/meetings" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["topic"] != "New Meeting" || body["type"] != float64(2) {
				t.Fatalf("body = %v", body)
			}
			if body["start_time"] != "2025-03-15T14:00:00Z" || body["duration"] != float64(60) {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"id": 78475495050, "uuid": "abc123==", "topic": "New Meeting",
				"start_time": "2025-03-15T14:00:00Z", "duration": 60,
				"join_url": "https://zoom.us/j/78475495050", "start_url": "https://zoom.us/s/78475495050",
				"password": "abc123", "created_at": "2025-03-10T10:00:00Z",
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{
		"action": "zoom_create_meeting", "topic": "New Meeting", "start_time": "2025-03-15T14:00:00Z",
	})
	if result["topic"] != "New Meeting" || result["join_url"] != "https://zoom.us/j/78475495050" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomCreateMeeting::test_missing_topic
func TestBatch7Lane9ZoomCreateMeetingDefaults(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if body["type"] != float64(1) {
				t.Fatalf("type = %v, want instant meeting (1)", body["type"])
			}
			if _, present := body["start_time"]; present {
				t.Fatalf("body = %v", body)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"id": 1, "topic": "Instant"})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_create_meeting", "topic": "Instant"})
	if result["topic"] != "Instant" {
		t.Fatalf("result = %v", result)
	}
	missing := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_create_meeting"})
	if got := batch4ErrorText(t, missing); got != "topic is required" {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomDeleteMeeting::test_successful_delete
func TestBatch7Lane9ZoomDeleteMeeting(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodDelete || r.URL.Path != "/meetings/78475495050" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_delete_meeting", "meeting_id": "78475495050"})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomListRecordings::test_successful_list
func TestBatch7Lane9ZoomListRecordings(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/users/me/recordings" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if r.URL.Query().Get("from") != "2025-01-01" || r.URL.Query().Get("to") != "2025-01-31" {
				t.Fatalf("query = %v", r.URL.Query())
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"total_records": 1,
				"meetings": []any{map[string]any{
					"id": 78475495050, "topic": "Weekly Standup", "start_time": "2025-01-21T09:20:00Z",
					"duration": 30, "recording_count": 1, "total_size": 52428800,
					"recording_files": []any{map[string]any{
						"id": "file1", "file_type": "MP4", "file_size": 41943040,
						"recording_type": "shared_screen_with_speaker_view",
						"status":         "completed", "play_url": "https://zoom.us/rec/play/test",
					}},
				}},
			})
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{
		"action": "zoom_list_recordings", "from_date": "2025-01-01", "to_date": "2025-01-31",
	})
	recordings := batch4BodySlice(t, result["recordings"])
	first := batch4BodyMap(t, recordings[0])
	if first["topic"] != "Weekly Standup" {
		t.Fatalf("recording = %v", first)
	}
	files := batch4BodySlice(t, first["recording_files"])
	if file := batch4BodyMap(t, files[0]); file["file_type"] != "MP4" || file["play_url"] != "https://zoom.us/rec/play/test" {
		t.Fatalf("file = %v", file)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_zoom_tool.py::TestZoomListRecordings::test_missing_dates
func TestBatch7Lane9ZoomListRecordingsMissingDates(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_list_recordings"})
	if got := batch4ErrorText(t, result); got != "from_date and to_date are required (YYYY-MM-DD)" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: upstream update_meeting has no dedicated unit test; the
// partial-body behavior is asserted here to pin the Go port.
func TestBatch7Lane9ZoomUpdateMeeting(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPatch || r.URL.Path != "/meetings/123" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if body["topic"] != "Renamed" || body["duration"] != float64(45) {
				t.Fatalf("body = %v", body)
			}
			if _, present := body["agenda"]; present {
				t.Fatalf("unexpected agenda: %v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{
		"action": "zoom_update_meeting", "meeting_id": "123", "topic": "Renamed", "duration": 45,
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	empty := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_update_meeting", "meeting_id": "123"})
	if got := batch4ErrorText(t, empty); got != "At least one field to update is required" {
		t.Fatalf("error = %q", got)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: participants/registrants have no upstream unit tests; their
// field shaping is pinned here.
func TestBatch7Lane9ZoomParticipantsAndRegistrants(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch r.URL.Path {
			case "/past_meetings/abc==/participants":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"total_records": 1,
					"participants": []any{map[string]any{
						"id": "p1", "name": "Alice", "user_email": "alice@example.com",
						"join_time": "2025-01-21T09:20:00Z", "leave_time": "2025-01-21T09:50:00Z", "duration": 30,
					}},
				})
			case "/meetings/123/registrants":
				if got := r.URL.Query().Get("status"); got != "approved" {
					t.Fatalf("status = %q", got)
				}
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"total_records": 1,
					"registrants": []any{map[string]any{
						"id": "r1", "email": "bob@example.com", "first_name": "Bob", "last_name": "B",
						"status": "approved", "create_time": "2025-01-01T00:00:00Z", "join_url": "https://zoom.us/j/1",
					}},
				})
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
		})
	participants := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_list_meeting_participants", "meeting_id": "abc=="})
	first := batch4BodyMap(t, batch4BodySlice(t, participants["participants"])[0])
	if first["name"] != "Alice" || first["user_email"] != "alice@example.com" {
		t.Fatalf("participant = %v", first)
	}
	registrants := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_list_meeting_registrants", "meeting_id": "123"})
	registrant := batch4BodyMap(t, batch4BodySlice(t, registrants["registrants"])[0])
	if registrant["email"] != "bob@example.com" || registrant["status"] != "approved" {
		t.Fatalf("registrant = %v", registrant)
	}
	if recorder.count() != 2 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: upstream tests mock httpx responses and never exercise the
// provider status codes; this table pins the Zoom error vocabulary.
func TestBatch7Lane9ZoomStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Invalid or expired Zoom access token"},
		{http.StatusForbidden, "Insufficient Zoom API scopes for this operation"},
		{http.StatusNotFound, "Zoom resource not found"},
		{http.StatusTooManyRequests, "Zoom rate limit exceeded. Try again later."},
	}
	for _, testCase := range cases {
		registry, _ := batch7Lane9Fake(t, "zoom_tool", nil,
			func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(`{"message":"provider detail"}`))
			})
		result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_meeting", "meeting_id": "1"})
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("status %d: error = %q, want %q", testCase.status, got, testCase.want)
		}
	}
	registry, _ := batch7Lane9Fake(t, "zoom_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"message":"Invalid meeting id"}`))
		})
	result := batch4Execute(t, registry, "zoom", map[string]any{"action": "zoom_get_meeting", "meeting_id": "1"})
	if got := batch4ErrorText(t, result); got != "Zoom API error (HTTP 400): Invalid meeting id" {
		t.Fatalf("error = %q", got)
	}
}
