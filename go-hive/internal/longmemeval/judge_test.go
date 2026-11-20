package longmemeval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func judgeItem() Item {
	return Item{
		QuestionID:   "fixture_commute_01",
		QuestionType: "single-session-user",
		Question:     "How long is my commute?",
		Answer:       "45 minutes",
		QuestionDate: "2023/05/20 (Sat) 10:00",
	}
}

func fastClient(server *httptest.Server) *APIClient {
	client := NewAPIClient(server.URL, "test-key", server.Client())
	client.BaseDelay = time.Millisecond
	client.Sleep = func(context.Context, time.Duration) error { return nil }
	return client
}

// Parity-exempt: Go-side coverage for the verbatim templates in
// tests/e2e/longmemeval.py::judge_prompt (no Python unit test for them).
func TestJudgePromptUsesOfficialTemplates(t *testing.T) {
	cases := []struct {
		qtype string
		wants []string
	}{
		{"single-session-user", []string{"contains the correct answer", "Correct Answer: 45 minutes"}},
		{"single-session-assistant", []string{"contains the correct answer"}},
		{"multi-session", []string{"contains all the intermediate steps"}},
		{"temporal-reasoning", []string{"do not penalize off-by-one errors"}},
		{"knowledge-update", []string{"some previous information along with an updated answer"}},
		{"single-session-preference", []string{"Rubric:", "recalls and utilizes the user's personal information"}},
	}
	for _, tc := range cases {
		item := judgeItem()
		item.QuestionType = tc.qtype
		prompt, err := JudgePrompt(item, "It is 45 minutes each way.")
		if err != nil {
			t.Fatalf("%s: %v", tc.qtype, err)
		}
		if strings.Contains(prompt, "{}") {
			t.Fatalf("%s: placeholder left unfilled", tc.qtype)
		}
		if !strings.Contains(prompt, item.Question) || !strings.Contains(prompt, item.Answer) {
			t.Fatalf("%s: prompt lacks question/answer", tc.qtype)
		}
		for _, want := range tc.wants {
			if !strings.Contains(prompt, want) {
				t.Fatalf("%s: prompt missing %q", tc.qtype, want)
			}
		}
	}
	abstention := judgeItem()
	abstention.QuestionID = "fixture_abs_01_abs"
	prompt, err := JudgePrompt(abstention, "You never told me.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "unanswerable") || !strings.Contains(prompt, "Explanation: 45 minutes") {
		t.Fatalf("abstention prompt = %q", prompt)
	}
}

// Parity-exempt: pins the official request settings (temperature 0, 10-token
// cap for the official judge) and the "yes" substring rule.
func TestJudgeGradeUsesOfficialSettings(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "Yes."}}},
		})
	}))
	defer server.Close()

	judge := NewJudge(fastClient(server), OfficialJudgeModel)
	verdict, err := judge.Grade(context.Background(), judgeItem(), "It is 45 minutes each way.")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if !verdict.Correct || verdict.JudgeModel != OfficialJudgeModel {
		t.Fatalf("verdict = %+v", verdict)
	}
	body := bodies[0]
	if body["model"] != OfficialJudgeModel {
		t.Fatalf("model = %v", body["model"])
	}
	if body["temperature"] != float64(0) {
		t.Fatalf("temperature = %v", body["temperature"])
	}
	if body["max_tokens"] != float64(OfficialJudgeMaxTokens) {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}

	// A judge that is not the official model gets no 10-token cap.
	relaxed := NewJudge(fastClient(server), "some-other-model")
	if _, err := relaxed.Grade(context.Background(), judgeItem(), "answer"); err != nil {
		t.Fatal(err)
	}
	if _, present := bodies[1]["max_tokens"]; present {
		t.Fatalf("non-official judge got max_tokens = %v", bodies[1]["max_tokens"])
	}
}

// Parity-exempt: upstream matches "yes" as a substring of the lowercased
// reply; "yesterday" therefore counts as yes, and the port keeps that.
func TestJudgeMatchesSubstringProtocol(t *testing.T) {
	replies := map[string]bool{"Yes.": true, "YES": true, "no.": false, "yesterday": true}
	for reply, want := range replies {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"content": reply}}},
			})
		}))
		verdict, err := NewJudge(fastClient(server), "judge-model").Grade(context.Background(), judgeItem(), "answer")
		server.Close()
		if err != nil {
			t.Fatalf("%q: %v", reply, err)
		}
		if verdict.Correct != want {
			t.Fatalf("reply %q => correct %v, want %v", reply, verdict.Correct, want)
		}
	}
}

// Parity-exempt: reasoning models reject temperature; the client drops it and
// retries once, as upstream._chat does.
func TestJudgeDropsTemperatureOn400(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, present := body["temperature"]; present {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"message": "temperature is not supported"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "yes"}}},
		})
	}))
	defer server.Close()

	verdict, err := NewJudge(fastClient(server), "reasoning-model").Grade(context.Background(), judgeItem(), "answer")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if !verdict.Correct {
		t.Fatal("verdict not correct after the temperature retry")
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	if _, present := bodies[1]["temperature"]; present {
		t.Fatal("second request still carried temperature")
	}
}

// Parity-exempt: transient 5xx responses are retried with backoff.
func TestJudgeRetriesTransientServerErrors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "yes"}}},
		})
	}))
	defer server.Close()

	verdict, err := NewJudge(fastClient(server), "judge-model").Grade(context.Background(), judgeItem(), "answer")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if !verdict.Correct || calls != 2 {
		t.Fatalf("correct=%v calls=%d", verdict.Correct, calls)
	}
}

// Parity-exempt: a hard HTTP error surfaces with its status.
func TestJudgeSurfacesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "bad key"}}`))
	}))
	defer server.Close()

	_, err := NewJudge(fastClient(server), "judge-model").Grade(context.Background(), judgeItem(), "answer")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}
}

// Parity-exempt: prompts for an unknown question type fail loudly instead of
// grading with the wrong template.
func TestJudgePromptRejectsUnknownType(t *testing.T) {
	item := judgeItem()
	item.QuestionType = "made-up"
	if _, err := JudgePrompt(item, "answer"); err == nil {
		t.Fatal("unknown question type was graded")
	}
}
