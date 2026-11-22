package crm

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseListJobs::test_successful_list
func TestBatch6Lane2GreenhouseListJobs(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id":          1,
				"name":        "Software Engineer",
				"status":      "open",
				"departments": []any{map[string]any{"name": "Engineering"}},
				"offices":     []any{map[string]any{"name": "SF"}},
				"created_at":  "2024-01-01T00:00:00Z",
				"updated_at":  "2024-01-15T00:00:00Z",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_jobs", "status": "open",
	})
	request := recorder.at(t, 0)
	if request.Path != "/jobs" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("per_page"); got != "50" {
		t.Fatalf("per_page = %q", got)
	}
	if got := request.Query.Get("status"); got != "open" {
		t.Fatalf("status = %q", got)
	}
	if got := request.Header.Get("Authorization"); got != "Basic "+
		base64.StdEncoding.EncodeToString([]byte("test-token:")) {
		t.Fatalf("Authorization = %q", got)
	}
	batch6Lane2EqualInt(t, result["count"], 1)
	job := batch6Lane2FieldSlice(t, result, "jobs")[0].(map[string]any)
	if job["name"] != "Software Engineer" {
		t.Fatalf("job = %v", job)
	}
	departments := job["departments"].([]string)
	if len(departments) != 1 || departments[0] != "Engineering" {
		t.Fatalf("departments = %v", job["departments"])
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseListJobs::test_missing_credentials
func TestBatch6Lane2GreenhouseMissingCredentials(t *testing.T) {
	registry := NewSupportRegistry(WithGetenv(func(string) string { return "" }))
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{"action": "greenhouse_list_jobs"})
	if got := batch6Lane2ErrorText(t, result); got != "GREENHOUSE_API_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseGetJob::test_successful_get
func TestBatch6Lane2GreenhouseGetJob(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id":           1,
				"name":         "Software Engineer",
				"status":       "open",
				"confidential": false,
				"departments":  []any{map[string]any{"name": "Engineering"}},
				"offices":      []any{map[string]any{"name": "SF"}},
				"openings":     []any{map[string]any{"id": 10, "status": "open"}},
				"created_at":   "2024-01-01T00:00:00Z",
				"updated_at":   "2024-01-15T00:00:00Z",
				"notes":        "Expanding team",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_get_job", "job_id": 1,
	})
	if recorder.at(t, 0).Path != "/jobs/1" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["name"] != "Software Engineer" {
		t.Fatalf("result = %v", result)
	}
	opening := batch6Lane2FieldSlice(t, result, "openings")[0].(map[string]any)
	if opening["status"] != "open" {
		t.Fatalf("openings = %v", result["openings"])
	}
	batch6Lane2EqualInt(t, opening["id"], 10)
	if result["notes"] != "Expanding team" {
		t.Fatalf("notes = %v", result["notes"])
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseGetJob::test_missing_job_id
func TestBatch6Lane2GreenhouseGetJobValidation(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			t.Error("unexpected request")
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_get_job", "job_id": 0,
	})
	if got := batch6Lane2ErrorText(t, result); got != "job_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseListCandidates::test_successful_list
func TestBatch6Lane2GreenhouseListCandidates(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id":              100,
				"first_name":      "John",
				"last_name":       "Smith",
				"company":         "Acme",
				"title":           "Developer",
				"tags":            []any{"senior"},
				"application_ids": []any{200},
				"created_at":      "2024-03-01T00:00:00Z",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_candidates", "job_id": 1, "email": "john@acme.test", "per_page": 900,
	})
	request := recorder.at(t, 0)
	if request.Path != "/candidates" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("job_id"); got != "1" {
		t.Fatalf("job_id = %q", got)
	}
	if got := request.Query.Get("email"); got != "john@acme.test" {
		t.Fatalf("email = %q", got)
	}
	if got := request.Query.Get("per_page"); got != "500" {
		t.Fatalf("per_page = %q", got)
	}
	candidate := batch6Lane2FieldSlice(t, result, "candidates")[0].(map[string]any)
	if candidate["first_name"] != "John" {
		t.Fatalf("candidate = %v", candidate)
	}
	ids := batch6Lane2FieldSlice(t, candidate, "application_ids")
	batch6Lane2EqualInt(t, ids[0], 200)
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseGetCandidate::test_successful_get
func TestBatch6Lane2GreenhouseGetCandidate(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id":         100,
				"first_name": "John",
				"last_name":  "Smith",
				"company":    "Acme",
				"title":      "Developer",
				"email_addresses": []any{
					map[string]any{"value": "john@example.com", "type": "personal"},
				},
				"phone_numbers":   []any{map[string]any{"value": "555-1234", "type": "mobile"}},
				"tags":            []any{"senior"},
				"application_ids": []any{200},
				"created_at":      "2024-03-01T00:00:00Z",
				"updated_at":      "2024-03-10T00:00:00Z",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_get_candidate", "candidate_id": 100,
	})
	if recorder.at(t, 0).Path != "/candidates/100" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["first_name"] != "John" {
		t.Fatalf("result = %v", result)
	}
	emails := result["emails"].([]string)
	if len(emails) != 1 || emails[0] != "john@example.com" {
		t.Fatalf("emails = %v", result["emails"])
	}
	phones := result["phones"].([]string)
	if len(phones) != 1 || phones[0] != "555-1234" {
		t.Fatalf("phones = %v", result["phones"])
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseListApplications::test_successful_list
func TestBatch6Lane2GreenhouseListApplications(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id":               200,
				"candidate_id":     100,
				"status":           "active",
				"current_stage":    map[string]any{"id": 3, "name": "Technical Interview"},
				"jobs":             []any{map[string]any{"id": 1, "name": "Software Engineer"}},
				"applied_at":       "2024-03-01T00:00:00Z",
				"last_activity_at": "2024-03-10T00:00:00Z",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_applications", "status": "active",
	})
	request := recorder.at(t, 0)
	if request.Path != "/applications" {
		t.Fatalf("path = %s", request.Path)
	}
	if got := request.Query.Get("status"); got != "active" {
		t.Fatalf("status = %q", got)
	}
	application := batch6Lane2FieldSlice(t, result, "applications")[0].(map[string]any)
	if application["current_stage"] != "Technical Interview" {
		t.Fatalf("application = %v", application)
	}
	jobs := application["jobs"].([]string)
	if len(jobs) != 1 || jobs[0] != "Software Engineer" {
		t.Fatalf("jobs = %v", application["jobs"])
	}
}

// Parity: tools/tests/tools/test_greenhouse_tool.py::TestGreenhouseGetApplication::test_successful_get
func TestBatch6Lane2GreenhouseGetApplication(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, map[string]any{
				"id":               200,
				"candidate_id":     100,
				"status":           "active",
				"current_stage":    map[string]any{"id": 3, "name": "Technical Interview"},
				"source":           map[string]any{"id": 5, "public_name": "LinkedIn"},
				"jobs":             []any{map[string]any{"id": 1, "name": "Software Engineer"}},
				"answers":          []any{map[string]any{"question": "Work authorized?", "answer": "Yes"}},
				"applied_at":       "2024-03-01T00:00:00Z",
				"rejected_at":      nil,
				"last_activity_at": "2024-03-10T00:00:00Z",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_get_application", "application_id": 200,
	})
	if recorder.at(t, 0).Path != "/applications/200" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
	if result["source"] != "LinkedIn" {
		t.Fatalf("result = %v", result)
	}
	answer := batch6Lane2FieldSlice(t, result, "answers")[0].(map[string]any)
	if answer["answer"] != "Yes" {
		t.Fatalf("answers = %v", result["answers"])
	}
	if value, present := result["rejected_at"]; !present || value != nil {
		t.Fatalf("rejected_at = %v (present %v)", value, present)
	}
}

// Parity-exempt: upstream greenhouse_tool tests stop at applications; offers,
// candidate notes, and scorecards are uncovered there.
func TestBatch6Lane2GreenhouseListOffers(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id": 300, "application_id": 200, "version": 1, "status": "sent",
				"starts_at": "2024-06-01", "created_at": "2024-05-01T00:00:00Z",
				"updated_at": "2024-05-02T00:00:00Z", "sent_at": "2024-05-02T00:00:00Z",
				"resolved_at": nil,
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_offers", "application_id": 200,
	})
	request := recorder.at(t, 0)
	if request.Path != "/applications/200/offers" {
		t.Fatalf("path = %s", request.Path)
	}
	offer := batch6Lane2FieldSlice(t, result, "offers")[0].(map[string]any)
	if offer["status"] != "sent" || offer["starts_at"] != "2024-06-01" {
		t.Fatalf("offer = %v", offer)
	}
	batch6Lane2EqualInt(t, offer["version"], 1)
}

// Parity-exempt: no upstream test for the workspace-wide offers listing.
func TestBatch6Lane2GreenhouseListOffersAll(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{})
		},
	)
	batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_offers",
	})
	if recorder.at(t, 0).Path != "/offers" {
		t.Fatalf("path = %s", recorder.at(t, 0).Path)
	}
}

// Parity-exempt: no upstream test for greenhouse_add_candidate_note.
func TestBatch6Lane2GreenhouseAddCandidateNote(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusCreated, map[string]any{
				"id": 400, "body": "Strong candidate", "visibility": "public",
				"created_at": "2024-03-01T00:00:00Z",
			})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_add_candidate_note", "candidate_id": 100, "body": "Strong candidate",
	})
	request := recorder.at(t, 0)
	if request.Method != http.MethodPost || request.Path != "/candidates/100/activity_feed/notes" {
		t.Fatalf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["body"] != "Strong candidate" || request.Body["visibility"] != "public" {
		t.Fatalf("body = %v", request.Body)
	}
	if values := request.Header.Values("On-Behalf-Of"); len(values) == 0 {
		t.Fatal("On-Behalf-Of header missing")
	}
	if result["status"] != "created" || result["body"] != "Strong candidate" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: no upstream test for greenhouse_list_scorecards.
func TestBatch6Lane2GreenhouseListScorecards(t *testing.T) {
	registry, recorder := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{map[string]any{
				"id":                     500,
				"interviewer":            map[string]any{"id": 7, "name": "Interviewer One"},
				"overall_recommendation": "yes",
				"submitted_at":           "2024-03-05T00:00:00Z",
				"interview":              map[string]any{"name": "Technical Screen"},
				"created_at":             "2024-03-04T00:00:00Z",
				"updated_at":             "2024-03-05T00:00:00Z",
			}})
		},
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_list_scorecards", "application_id": 200,
	})
	request := recorder.at(t, 0)
	if request.Path != "/applications/200/scorecards" {
		t.Fatalf("path = %s", request.Path)
	}
	scorecard := batch6Lane2FieldSlice(t, result, "scorecards")[0].(map[string]any)
	if scorecard["interviewer_name"] != "Interviewer One" {
		t.Fatalf("scorecard = %v", scorecard)
	}
	if scorecard["interview"] != "Technical Screen" || scorecard["overall_recommendation"] != "yes" {
		t.Fatalf("scorecard = %v", scorecard)
	}
}

// Parity-exempt: upstream greenhouse_tool has no error-mapping unit test; the
// strings mirror greenhouse_tool._get / _post.
func TestBatch6Lane2GreenhouseErrorMapping(t *testing.T) {
	batch6Lane2ErrorTable(
		t, "greenhouse_tool", "greenhouse", nil,
		map[string]any{"action": "greenhouse_list_jobs"},
		map[int]string{
			http.StatusUnauthorized:    "Unauthorized. Check your Greenhouse API token.",
			http.StatusForbidden:       "Forbidden. Your API key may lack the required permissions.",
			http.StatusNotFound:        "Resource not found.",
			http.StatusTooManyRequests: "Rate limited. Try again shortly.",
		},
	)
}

// Parity-exempt: POST keeps the upstream narrower error split (no 404/429
// special-casing), so 429 surfaces as a generic provider error.
func TestBatch6Lane2GreenhousePostError(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		batch6Lane2StatusHandler(http.StatusTooManyRequests, map[string]any{"message": "slow down"}),
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{
		"action": "greenhouse_add_candidate_note", "candidate_id": 1, "body": "note",
	})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Greenhouse API error 429: ") {
		t.Fatalf("error = %q", text)
	}
}

// Parity-exempt: generic GET errors and the non-object guard have no upstream
// unit test.
func TestBatch6Lane2GreenhouseGenericErrors(t *testing.T) {
	registry, _ := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		batch6Lane2StatusHandler(http.StatusBadGateway, map[string]any{"message": "upstream"}),
	)
	result := batch6Lane2Execute(t, registry, "greenhouse", map[string]any{"action": "greenhouse_list_jobs"})
	text := batch6Lane2ErrorText(t, result)
	if !strings.HasPrefix(text, "Greenhouse API error 502: ") {
		t.Fatalf("error = %q", text)
	}

	malformed, _ := batch6Lane2TestRegistry(
		t, "greenhouse_tool", nil,
		func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
			batch6Lane2WriteJSON(w, http.StatusOK, []any{})
		},
	)
	guard := batch6Lane2Execute(t, malformed, "greenhouse", map[string]any{
		"action": "greenhouse_get_job", "job_id": 1,
	})
	if got := batch6Lane2ErrorText(t, guard); got != "Unexpected response format" {
		t.Fatalf("error = %q", got)
	}
}
