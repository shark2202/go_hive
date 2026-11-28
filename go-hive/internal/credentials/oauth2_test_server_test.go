package credentials

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// oauth2TestRequest is one recorded request against the fake OAuth2 server.
type oauth2TestRequest struct {
	Method        string
	Path          string
	Query         url.Values
	Form          url.Values
	ContentType   string
	Accept        string
	Authorization string
	Headers       http.Header
}

// oauth2TestResponse is the scripted response for one request.
type oauth2TestResponse struct {
	Status      int
	ContentType string
	Body        string
}

// oauth2TestServer is a deterministic token/API endpoint used by the OAuth2
// provider tests. It records every request and lets each test script responses
// per request index.
type oauth2TestServer struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []oauth2TestRequest
	respond  func(request oauth2TestRequest, index int) oauth2TestResponse
}

const oauth2TestDefaultBody = `{"access_token":"fresh-access","token_type":"Bearer","expires_in":3600}`

func newOAuth2TestServer(t *testing.T, respond func(request oauth2TestRequest, index int) oauth2TestResponse) *oauth2TestServer {
	t.Helper()
	fake := &oauth2TestServer{respond: respond}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

// newOAuth2TestServerJSON scripts every request to return body with 200 JSON.
func newOAuth2TestServerJSON(t *testing.T, body string) *oauth2TestServer {
	t.Helper()
	return newOAuth2TestServer(t, func(oauth2TestRequest, int) oauth2TestResponse {
		return oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: body}
	})
}

func (s *oauth2TestServer) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()
	form, _ := url.ParseQuery(string(body))
	record := oauth2TestRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.Query(),
		Form:          form,
		ContentType:   r.Header.Get("Content-Type"),
		Accept:        r.Header.Get("Accept"),
		Authorization: r.Header.Get("Authorization"),
		Headers:       r.Header.Clone(),
	}

	s.mu.Lock()
	index := len(s.requests)
	s.requests = append(s.requests, record)
	respond := s.respond
	s.mu.Unlock()

	response := oauth2TestResponse{Status: http.StatusOK, ContentType: "application/json", Body: oauth2TestDefaultBody}
	if respond != nil {
		response = respond(record, index)
	}
	if response.ContentType != "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	w.WriteHeader(response.Status)
	_, _ = io.WriteString(w, response.Body)
}

// URL returns the fake server origin with an optional path.
func (s *oauth2TestServer) URL(path string) string { return s.server.URL + path }

// RequestCount returns how many requests the server received.
func (s *oauth2TestServer) RequestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// Request returns the recorded request at index.
func (s *oauth2TestServer) Request(index int) oauth2TestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[index]
}

func (s *oauth2TestServer) Close() { s.server.Close() }

// assertFormField fails the test unless the recorded form carries value.
func assertFormField(t *testing.T, form url.Values, key, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Fatalf("form[%q] = %q, want %q (form=%v)", key, got, want, form)
	}
}

// assertNoFormField fails the test when the recorded form carries key.
func assertNoFormField(t *testing.T, form url.Values, key string) {
	t.Helper()
	if _, ok := form[key]; ok {
		t.Fatalf("form unexpectedly carries %q (form=%v)", key, form)
	}
}
