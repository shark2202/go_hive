package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type judgeCall struct {
	model    any
	question string
	state    map[string]any
	criteria map[string]string
}

func judgeTestSlot(overrides map[string]any) map[string]any {
	slot := map[string]any{
		"protocol": "fake",
		"api_base": "https://judge.invalid/v1",
		"model":    "judge-1",
		"api_key":  "k",
	}
	for key, value := range overrides {
		slot[key] = value
	}
	return slot
}

func writeJudgeTestConfig(t *testing.T, home hivehome.Home, config map[string]any) {
	t.Helper()
	writeServerTestJSON(t, home.ConfigurationFile(), config)
}

// Parity: framework/server/tests/test_judge.py::test_a_channel_asks_the_configured_judge
func TestAChannelAsksTheConfiguredJudge(t *testing.T) {
	t.Setenv("HIVE_STALL_CHECK", "")
	home := serverTestHome(t)
	writeJudgeTestConfig(t, home, map[string]any{"judge": judgeTestSlot(nil)})

	criteria := map[string]string{"true": "someone talks to her", "false": "others chat among themselves"}
	var calls []judgeCall
	fake := func(slot map[string]any, question string, state map[string]any, criteria map[string]string) (float64, error) {
		calls = append(calls, judgeCall{slot["model"], question, state, criteria})
		return 0.8, nil
	}

	state := map[string]any{"new_messages": []any{"Cy: anyone draw?"}}
	p, ok := AskJudge(home, "Is now a good moment for her to speak?", state, criteria, map[string]JudgeProtocol{"fake": fake})
	if !ok {
		t.Fatal("AskJudge answered no answer, want 0.8")
	}
	if p != 0.8 {
		t.Fatalf("AskJudge = %v, want 0.8", p)
	}
	want := []judgeCall{{
		model:    "judge-1",
		question: "Is now a good moment for her to speak?",
		state:    map[string]any{"new_messages": []any{"Cy: anyone draw?"}},
		criteria: map[string]string{"true": "someone talks to her", "false": "others chat among themselves"},
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("recorded calls = %#v, want %#v", calls, want)
	}
}

// Parity: framework/server/tests/test_judge.py::test_no_answer_without_a_usable_judge
func TestNoAnswerWithoutAUsableJudge(t *testing.T) {
	t.Setenv("HIVE_STALL_CHECK", "")
	cases := []struct {
		name   string
		config map[string]any
	}{
		{"no slot", map[string]any{}},
		{"no key", map[string]any{"judge": judgeTestSlot(map[string]any{"api_key": ""})}},
		{"unsupported protocol", map[string]any{"judge": judgeTestSlot(map[string]any{"protocol": "unknown"})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := serverTestHome(t)
			writeJudgeTestConfig(t, home, tc.config)

			called := false
			fake := func(map[string]any, string, map[string]any, map[string]string) (float64, error) {
				called = true
				return 0.8, nil
			}
			if _, ok := AskJudge(home, "Is it raining?", map[string]any{}, nil, map[string]JudgeProtocol{"fake": fake}); ok {
				t.Fatal("AskJudge answered, want no answer")
			}
			if called {
				t.Fatal("protocol was called, want it untouched")
			}
		})
	}
}

// Parity: framework/server/tests/test_judge.py::test_no_answer_when_the_judge_fails
func TestNoAnswerWhenTheJudgeFails(t *testing.T) {
	t.Setenv("HIVE_STALL_CHECK", "")
	home := serverTestHome(t)
	writeJudgeTestConfig(t, home, map[string]any{"judge": judgeTestSlot(nil)})

	fake := func(_ map[string]any, _ string, state map[string]any, _ map[string]string) (float64, error) {
		if pyTruthy(state["fail"]) {
			return 0, errors.New("judge took too long")
		}
		return 0.8, nil
	}
	if _, ok := AskJudge(home, "Is it raining?", map[string]any{"fail": true}, nil, map[string]JudgeProtocol{"fake": fake}); ok {
		t.Fatal("AskJudge answered, want no answer")
	}
}

// Parity-exempt: upstream uses httpx, whose default follow_redirects=False plus
func TestTheTypesafeJudgeDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"answer":{"noul":0.9}}}`))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()

	slot := judgeTestSlot(map[string]any{"api_base": redirector.URL})
	if _, err := TypesafeJudge(slot, "Is it raining?", map[string]any{}, nil); err == nil {
		t.Fatal("TypesafeJudge followed the redirect, want an HTTP 302 error")
	}
	if hits.Load() != 0 {
		t.Fatalf("redirect target was hit %d times, want 0", hits.Load())
	}
}

// The real TypeSafe service renders the state in the order the JSON carries it
// (verified against startlux-decide: the same semantic state sorted gives a
// different noul than upstream's insertion order). The wire bytes must
// therefore keep the order framework/server/stall_check.py inserts.
// Parity: core/framework/server/judge.py::typesafe
func TestTheTypesafeJudgeKeepsTheUpstreamStateKeyOrder(t *testing.T) {
	var raw []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read judge request: %v", err)
		}
		raw = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"answer":{"noul":0.75}}}`))
	}))
	defer server.Close()

	slot := judgeTestSlot(map[string]any{"api_base": server.URL})
	state := map[string]any{
		"tools_called_this_turn":   []any{},
		"assistant_reply":          "On it.",
		"user_message":             "draw",
		"assistant_reasoning_tail": "I will draw.",
		"previous_turn":            map[string]any{"assistant_reply": "Hi.", "tools_called": []any{}},
		"zeta":                     "tail",
		"alpha":                    "head",
	}
	p, err := TypesafeJudge(slot, "question", state, nil)
	if err != nil {
		t.Fatalf("TypesafeJudge: %v", err)
	}
	if p != 0.75 {
		t.Fatalf("TypesafeJudge = %v, want 0.75", p)
	}

	var envelope struct {
		State json.RawMessage `json:"state"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode judge request: %v", err)
	}
	encoded := string(envelope.State)
	want := []string{
		"previous_turn",
		"user_message",
		"assistant_reasoning_tail",
		"assistant_reply",
		"tools_called_this_turn",
		"alpha",
		"zeta",
	}
	// Search from after the last hit: a key name can also appear nested
	// inside a value ("assistant_reply" inside previous_turn).
	cursor := 0
	for _, key := range want {
		at := strings.Index(encoded[cursor:], `"`+key+`":`)
		if at < 0 {
			t.Fatalf("state %s lacks key %q after offset %d", encoded, key, cursor)
		}
		cursor += at + len(key) + 3
	}
}
