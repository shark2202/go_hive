package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

type queenMailStub struct {
	tokens         map[string]string
	queens         map[string]string
	correspondents map[string][]map[string]any
	letter         map[string]any
	letterErr      error
	lastLetter     []string
}

func (stub *queenMailStub) Authorizes(ref, token string) bool {
	if ref == "" {
		return false
	}
	expected, ok := stub.tokens[ref]
	return ok && token == expected
}

func (stub *queenMailStub) QueenOf(ref string) (string, bool) {
	queenID, ok := stub.queens[ref]
	return queenID, ok
}

func (stub *queenMailStub) Correspondents(queenID string) []map[string]any {
	return stub.correspondents[queenID]
}

func (stub *queenMailStub) SendFromConversation(ref, to, message string) (map[string]any, error) {
	stub.lastLetter = []string{ref, to, message}
	if stub.letterErr != nil {
		return nil, stub.letterErr
	}
	return stub.letter, nil
}

func queenMailTestMux(stub QueenMailBackend) *http.ServeMux {
	mux := http.NewServeMux()
	RegisterQueenMailRoutes(mux, QueenMailRouteDeps{QueenMail: stub})
	return mux
}

func doQueenMailRequest(t *testing.T, mux *http.ServeMux, method, target, body, ref, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		request.Header.Set(QueenMailTokenHeader, token)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func queenDecodeResponse(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

func containsSubstring(text, want string) bool {
	return strings.Contains(text, want)
}

// Parity: framework/server/tests/test_claude_code_queens.py::test_the_routes_want_the_token_and_one_of_her_conversations
func TestQueenMailRoutesWantTheTokenAndOneOfHerConversations(t *testing.T) {
	ref := "claude_code:queen_claude:abc"
	stub := &queenMailStub{
		tokens: map[string]string{ref: "t0ken"},
		queens: map[string]string{ref: "queen_claude"},
		correspondents: map[string][]map[string]any{
			"queen_claude": {
				{"id": "queen_ava", "name": "Ava", "title": "Cartographer"},
				{"id": "queen_ivy", "name": "Ivy", "title": "Cartographer"},
			},
		},
		letter: map[string]any{"sent": "hop_1", "to": "queen_ivy", "depth": 1},
	}
	mux := queenMailTestMux(stub)

	if recorder := doQueenMailRequest(t, mux, http.MethodGet, "/api/queen-mail/queens?from="+ref, "", ref, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no token status = %d", recorder.Code)
	}
	posedAva := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"queen_ava","to":"queen_ivy","message":"Hi"}`, ref, "t0ken")
	if posedAva.Code != http.StatusUnauthorized {
		t.Fatalf("posing as ava status = %d", posedAva.Code)
	}
	posedAnother := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"claude_code:queen_claude:another","to":"queen_ivy","message":"Hi"}`, ref, "t0ken")
	if posedAnother.Code != http.StatusUnauthorized {
		t.Fatalf("posing as another status = %d", posedAnother.Code)
	}

	recorder := doQueenMailRequest(t, mux, http.MethodGet, "/api/queen-mail/queens?from="+ref, "", ref, "t0ken")
	if recorder.Code != http.StatusOK {
		t.Fatalf("queens status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	expected := map[string]any{"queens": []any{
		map[string]any{"id": "queen_ava", "name": "Ava", "title": "Cartographer"},
		map[string]any{"id": "queen_ivy", "name": "Ivy", "title": "Cartographer"},
	}}
	if body := queenDecodeResponse(t, recorder); !reflect.DeepEqual(body, expected) {
		t.Fatalf("body = %#v", body)
	}

	letter := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters",
		`{"from":"`+ref+`","to":"queen_ivy","message":"Hi"}`, ref, "t0ken")
	if letter.Code != http.StatusOK {
		t.Fatalf("letter status = %d, body = %s", letter.Code, letter.Body.String())
	}
	if sent := queenDecodeResponse(t, letter); sent["to"] != "queen_ivy" {
		t.Fatalf("sent = %#v", sent)
	}
	if !reflect.DeepEqual(stub.lastLetter, []string{ref, "queen_ivy", "Hi"}) {
		t.Fatalf("lastLetter = %#v", stub.lastLetter)
	}
}

// Parity-exempt: the unknown-conversation branch has no Python route test; pins the 404 envelope.
func TestQueenMailQueensUnknownConversationIs404(t *testing.T) {
	ref := "claude_code:queen_claude:abc"
	stub := &queenMailStub{tokens: map[string]string{ref: "t0ken"}, queens: map[string]string{}}
	recorder := doQueenMailRequest(t, queenMailTestMux(stub), http.MethodGet, "/api/queen-mail/queens?from="+ref, "", ref, "t0ken")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

// Parity-exempt: letter body/refusal branches have no direct Python route test; pins the 400 envelopes.
func TestQueenMailLetterValidatesBodyAndRefusals(t *testing.T) {
	ref := "claude_code:queen_claude:abc"
	stub := &queenMailStub{
		tokens:    map[string]string{ref: "t0ken"},
		queens:    map[string]string{ref: "queen_claude"},
		letterErr: &QueenMailError{Message: "There's no queen 'queen_zed' to write to"},
	}
	mux := queenMailTestMux(stub)

	cases := []struct {
		body   string
		token  string
		status int
		error  string
	}{
		{`not-json`, "t0ken", http.StatusBadRequest, "The body isn't JSON"},
		{`[]`, "t0ken", http.StatusBadRequest, "The body must be a JSON object"},
		{`{"from":"` + ref + `","to":"queen_zed","message":"Hi"}`, "", http.StatusUnauthorized, "unauthorized"},
		{`{"from":"` + ref + `","to":"queen_zed","message":"Hi"}`, "t0ken", http.StatusBadRequest, "There's no queen"},
	}
	for _, testCase := range cases {
		recorder := doQueenMailRequest(t, mux, http.MethodPost, "/api/queen-mail/letters", testCase.body, ref, testCase.token)
		if recorder.Code != testCase.status {
			t.Fatalf("%s: status = %d, body = %s", testCase.body, recorder.Code, recorder.Body.String())
		}
		body := queenDecodeResponse(t, recorder)
		if text, _ := body["error"].(string); !containsSubstring(text, testCase.error) {
			t.Fatalf("%s: error = %#v", testCase.body, body["error"])
		}
	}
}
