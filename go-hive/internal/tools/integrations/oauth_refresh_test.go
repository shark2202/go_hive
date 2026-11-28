package integrations

// OAuth2 refresh and declarative pagination evidence: fake token endpoint,
// fake provider, no network. Every test asserts observed request sequences.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// oauthTokenServer is a fake standard OAuth2 token endpoint recording every
// grant request (form plus headers).
type oauthTokenServer struct {
	*httptest.Server

	mu      sync.Mutex
	forms   []url.Values
	headers []http.Header
	respond func(http.ResponseWriter, *http.Request)
}

func newOAuthTokenServer(t *testing.T, respond func(http.ResponseWriter, *http.Request)) *oauthTokenServer {
	t.Helper()
	server := &oauthTokenServer{respond: respond}
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		server.mu.Lock()
		server.forms = append(server.forms, r.PostForm)
		server.headers = append(server.headers, r.Header.Clone())
		handle := server.respond
		server.mu.Unlock()
		if handle != nil {
			handle(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fresh-token","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	return server
}

func (s *oauthTokenServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.forms)
}

func (s *oauthTokenServer) form(t *testing.T, index int) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.forms) {
		t.Fatalf("token request %d missing (have %d)", index, len(s.forms))
	}
	return s.forms[index]
}

func (s *oauthTokenServer) header(t *testing.T, index int, name string) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= len(s.headers) {
		t.Fatalf("token request %d missing (have %d)", index, len(s.headers))
	}
	return s.headers[index].Get(name)
}

// oauthGetenv builds the environment lookup a registry resolves credentials
// through; anything absent is empty.
func oauthGetenv(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// oauthGoogleEnv is the google OAuth2 credential as environment variables.
func oauthGoogleEnv(tokenServerURL string, extra map[string]string) map[string]string {
	env := map[string]string{
		"GOOGLE_CLIENT_ID":     "google-client",
		"GOOGLE_CLIENT_SECRET": "google-secret",
		"GOOGLE_REFRESH_TOKEN": "google-refresh",
		"GOOGLE_TOKEN_URL":     tokenServerURL,
	}
	for name, value := range extra {
		env[name] = value
	}
	return env
}

// oauthCalendarRegistry wires the calendar module to a fake provider and the
// fake token endpoint, with no credentials injected outside the environment.
func oauthCalendarRegistry(t *testing.T, env map[string]string) (*Registry, *batch4Recorder) {
	t.Helper()
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":"primary","summary":"Primary"}]}`)
	}))
	t.Cleanup(server.Close)
	registry := newBatch5Lane3Registry(
		WithBaseURL(batch5Lane3CalModule, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(oauthGetenv(env)),
	)
	return registry, recorder
}

// Parity-exempt: go-side OAuth2 refresh primitive; upstream refreshes inside
// core/framework/credentials with provider doubles, so no pytest exercises the
// HTTP refresh grant or its request shape.
func TestOAuthRefreshExpiredTokenRefreshesBeforeCall(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, nil)
	expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	registry, provider := oauthCalendarRegistry(t, oauthGoogleEnv(tokenServer.URL, map[string]string{
		"GOOGLE_ACCESS_TOKEN":            "stale-token",
		"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": expired,
	}))

	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if _, failed := result["error"]; failed {
		t.Fatalf("expired token refresh failed: %v", result)
	}
	if got := provider.last(t).Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Fatalf("provider Authorization = %q", got)
	}
	if tokenServer.count() != 1 {
		t.Fatalf("token requests = %d", tokenServer.count())
	}
	form := tokenServer.form(t, 0)
	if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "google-client" ||
		form.Get("client_secret") != "google-secret" || form.Get("refresh_token") != "google-refresh" {
		t.Fatalf("refresh form = %v", form)
	}
	if contentType := tokenServer.header(t, 0, "Content-Type"); contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("token Content-Type = %q", contentType)
	}
}

// Parity-exempt: upstream refresh failures raise CredentialRefreshError inside
// the store; this asserts the tool-facing value payload the port produces.
func TestOAuthRefreshFailureIsAnExplicitError(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	})
	registry, provider := oauthCalendarRegistry(t, oauthGoogleEnv(tokenServer.URL, map[string]string{
		"GOOGLE_ACCESS_TOKEN":            "stale-token",
		"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}))

	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if result["error"] != "Refresh token for 'google' is invalid or revoked. Re-authorization required." {
		t.Fatalf("error = %v", result["error"])
	}
	if result["error_code"] != "invalid_grant" {
		t.Fatalf("error_code = %v", result["error_code"])
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "GOOGLE_REFRESH_TOKEN") {
		t.Fatalf("help = %v", result["help"])
	}
	if provider.count() != 0 {
		t.Fatalf("provider called %d times despite refresh failure", provider.count())
	}
}

// Parity-exempt: "static access token cannot be refreshed" is a Go-side
// contract; upstream would raise CredentialExpiredError from the store.
func TestOAuthRefreshStaticTokenExpiredNamesVariable(t *testing.T) {
	registry, provider := oauthCalendarRegistry(t, map[string]string{
		"GOOGLE_ACCESS_TOKEN":            "stale-token",
		"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})

	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if result["error_code"] != "no_refresh_token" {
		t.Fatalf("error_code = %v (result %v)", result["error_code"], result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, "google") || !strings.Contains(message, "GOOGLE_ACCESS_TOKEN") {
		t.Fatalf("error = %q", message)
	}
	if provider.count() != 0 {
		t.Fatalf("provider called %d times with an expired static token", provider.count())
	}
}

// Parity-exempt: the store's refresh dedupe is a store lock upstream; this
// asserts the shared TokenRefresher performs one grant for concurrent calls.
func TestOAuthRefreshConcurrentCallsShareOneGrant(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"fresh-token","token_type":"Bearer","expires_in":3600}`)
	})
	registry, _ := oauthCalendarRegistry(t, oauthGoogleEnv(tokenServer.URL, map[string]string{
		"GOOGLE_ACCESS_TOKEN":            "stale-token",
		"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}))

	const callers = 6
	start := make(chan struct{})
	var wg sync.WaitGroup
	failures := make(chan string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := registry.Execute(context.Background(), "calendar", map[string]any{"action": "calendar_list_calendars"})
			if err != nil {
				failures <- err.Error()
				return
			}
			object, ok := result.(map[string]any)
			if !ok || object["error"] != nil {
				failures <- "call failed"
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Fatalf("concurrent call failed: %s", failure)
	}
	if tokenServer.count() != 1 {
		t.Fatalf("token requests = %d for %d callers", tokenServer.count(), callers)
	}
}

// Parity-exempt: 401-triggered refresh and single retry are Go-side; upstream
// raises on access and never retries the provider call.
func TestOAuthRefreshRetriesOnceAfterUnauthorized(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, nil)
	var mu sync.Mutex
	calls := 0
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if first {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid Credentials","code":401}}`)
			return
		}
		_, _ = io.WriteString(w, `{"items":[{"id":"primary","summary":"Primary"}]}`)
	}))
	t.Cleanup(server.Close)
	registry := newBatch5Lane3Registry(
		WithBaseURL(batch5Lane3CalModule, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(oauthGetenv(oauthGoogleEnv(tokenServer.URL, map[string]string{
			"GOOGLE_ACCESS_TOKEN": "stale-token",
		}))),
	)

	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if _, failed := result["error"]; failed {
		t.Fatalf("retry failed: %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("provider requests = %d", recorder.count())
	}
	if got := recorder.at(t, 0).Header.Get("Authorization"); got != "Bearer stale-token" {
		t.Fatalf("first Authorization = %q", got)
	}
	if got := recorder.at(t, 1).Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Fatalf("retry Authorization = %q", got)
	}
	if tokenServer.count() != 1 {
		t.Fatalf("token requests = %d", tokenServer.count())
	}
}

// Parity-exempt: the explicit "no refresh token" 401 error is a Go-side
// contract; upstream's store raises CredentialExpiredError instead.
func TestOAuthRefreshUnauthorizedWithoutRefreshTokenNamesVariable(t *testing.T) {
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"Invalid Credentials","code":401}}`)
	}))
	t.Cleanup(server.Close)
	registry := newBatch5Lane3Registry(
		WithBaseURL(batch5Lane3CalModule, server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(oauthGetenv(map[string]string{
			"GOOGLE_ACCESS_TOKEN":            "stale-token",
			"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			"GOOGLE_CLIENT_ID":               "google-client",
		})),
	)

	result := batch4Execute(t, registry, "calendar", map[string]any{"action": "calendar_list_calendars"})
	if result["error_code"] != "no_refresh_token" {
		t.Fatalf("error_code = %v (result %v)", result["error_code"], result)
	}
	message, _ := result["error"].(string)
	if !strings.Contains(message, "GOOGLE_ACCESS_TOKEN") {
		t.Fatalf("error = %q", message)
	}
	if recorder.count() != 1 {
		t.Fatalf("provider requests = %d (no retry without a refresh token)", recorder.count())
	}
}

// Parity-exempt: HubSpot's OAuth2 provider is Python-only; this is the
// ported module's refresh-and-retry evidence against a fake HubSpot API.
func TestOAuthRefreshHubSpotRetriesAfterUnauthorized(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, nil)
	var mu sync.Mutex
	calls := 0
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if first {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"status":"error","message":"Authentication credentials not found."}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"501","properties":{"email":"lead@example.com"}}`)
	}))
	t.Cleanup(server.Close)
	registry := newBatch5Lane4Registry(
		WithBaseURL("hubspot_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(oauthGetenv(map[string]string{
			"HUBSPOT_ACCESS_TOKEN":  "stale-hubspot-token",
			"HUBSPOT_CLIENT_ID":     "hubspot-client",
			"HUBSPOT_CLIENT_SECRET": "hubspot-secret",
			"HUBSPOT_REFRESH_TOKEN": "hubspot-refresh",
			"HUBSPOT_TOKEN_URL":     tokenServer.URL,
		})),
	)

	result := batch4Execute(t, registry, "hubspot", map[string]any{"action": "hubspot_get_contact", "contact_id": "501"})
	if _, failed := result["error"]; failed {
		t.Fatalf("hubspot retry failed: %v", result)
	}
	if recorder.count() != 2 {
		t.Fatalf("hubspot requests = %d", recorder.count())
	}
	if got := recorder.at(t, 1).Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Fatalf("retry Authorization = %q", got)
	}
	if form := tokenServer.form(t, 0); form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "hubspot-refresh" {
		t.Fatalf("hubspot refresh form = %v", form)
	}
}

// Parity-exempt: the Zoho OAuth2 provider is Python-only; this asserts the
// ported module mints its access token from the documented refresh variables.
func TestOAuthRefreshZohoMintsTokenFromRefreshTokenOnly(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"zoho-access","token_type":"Bearer","expires_in":3600,"api_domain":"https://www.zohoapis.com"}`)
	})
	recorder := &batch6Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"api_name":"Leads"}]}`)
	}))
	t.Cleanup(server.Close)
	registry := newBatch6Lane1Registry(
		WithBaseURL("zoho_crm_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(oauthGetenv(map[string]string{
			"ZOHO_CLIENT_ID":     "zoho-client",
			"ZOHO_CLIENT_SECRET": "zoho-secret",
			"ZOHO_REFRESH_TOKEN": "zoho-refresh",
			"ZOHO_TOKEN_URL":     tokenServer.URL,
		})),
	)

	result := batch6Execute(t, registry, "zoho_crm", map[string]any{"action": "zoho_crm_list_modules"})
	if _, failed := result["error"]; failed {
		t.Fatalf("zoho list_modules failed: %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Zoho-oauthtoken zoho-access" {
		t.Fatalf("zoho Authorization = %q", got)
	}
	if form := tokenServer.form(t, 0); form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "zoho-client" {
		t.Fatalf("zoho refresh form = %v", form)
	}
}

// Parity-exempt: the Gmail sender shares upstream's `google` OAuth2
// credential (credentials/email.py); this pins its pre-call refresh.
func TestOAuthRefreshGmailSenderRefreshesBeforeSend(t *testing.T) {
	tokenServer := newOAuthTokenServer(t, nil)
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, _ *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"gmail_msg_123"}`)
	}))
	t.Cleanup(server.Close)
	registry := batch7Lane9EnvRegistry(oauthGoogleEnv(tokenServer.URL, map[string]string{
		"GOOGLE_ACCESS_TOKEN":            "stale-token",
		"GOOGLE_ACCESS_TOKEN_EXPIRES_AT": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		"EMAIL_FROM":                     "user@gmail.com",
	}),
		WithBaseURL("email_gmail_tool", server.URL),
		WithHTTPClient(server.Client()),
	)

	result := batch4Execute(t, registry, "email", map[string]any{
		"action": "send_email", "to": "recipient@example.com", "subject": "Test Gmail",
		"html": "<p>Hello from Gmail</p>", "provider": "gmail",
	})
	if result["success"] != true {
		t.Fatalf("gmail send failed: %v", result)
	}
	if got := recorder.last(t).Header.Get("Authorization"); got != "Bearer fresh-token" {
		t.Fatalf("gmail Authorization = %q", got)
	}
	if tokenServer.count() != 1 {
		t.Fatalf("token requests = %d", tokenServer.count())
	}
}

// Parity-exempt: declarative pagination is Go-side; upstream airtable_tool
// surfaces has_more/offset for the model to continue instead of following it.
func TestAirtableListRecordsFollowsOffsetPages(t *testing.T) {
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("offset") == "" {
			_, _ = io.WriteString(w, `{"records":[{"id":"rec1"}],"offset":"itr2/second"}`)
			return
		}
		_, _ = io.WriteString(w, `{"records":[{"id":"rec2"}]}`)
	}))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("airtable_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("airtable_tool", "pat_fake"),
	)

	result := batch4Execute(t, registry, "airtable", map[string]any{
		"action": "airtable_list_records", "base_id": "app1", "table_name": "Tasks",
	})
	records, ok := result["records"].([]any)
	if !ok || len(records) != 2 {
		t.Fatalf("merged records = %v", result["records"])
	}
	if _, present := result["offset"]; present {
		t.Fatalf("offset still advertised after the walk: %v", result["offset"])
	}
	if recorder.count() != 2 {
		t.Fatalf("airtable requests = %d", recorder.count())
	}
	if got := recorder.at(t, 1).Query.Get("offset"); got != "itr2/second" {
		t.Fatalf("second page offset = %q", got)
	}
}

// Parity-exempt: declarative pagination is Go-side; upstream slack_list_users
// exposes a cursor parameter but never follows response_metadata.next_cursor.
func TestSlackListChannelsFollowsCursor(t *testing.T) {
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, body map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		if cursor, _ := body["cursor"].(string); cursor == "" {
			_, _ = io.WriteString(w, `{"ok":true,"channels":[{"id":"C1"}],"response_metadata":{"next_cursor":"cursor-2"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"channels":[{"id":"C2"}],"response_metadata":{"next_cursor":""}}`)
	}))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("slack_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("slack_tool", "xoxb-fake"),
	)

	result := batch4Execute(t, registry, "slack", map[string]any{"action": "slack_list_channels"})
	channels, ok := result["channels"].([]any)
	if !ok || len(channels) != 2 {
		t.Fatalf("merged channels = %v", result["channels"])
	}
	if recorder.count() != 2 {
		t.Fatalf("slack requests = %d", recorder.count())
	}
	if cursor, _ := recorder.at(t, 1).Body["cursor"].(string); cursor != "cursor-2" {
		t.Fatalf("second page cursor = %q", cursor)
	}
	if metadata, ok := result["response_metadata"].(map[string]any); ok {
		if _, present := metadata["next_cursor"]; present {
			t.Fatalf("next_cursor still advertised after the walk: %v", metadata)
		}
	}
}

// Parity-exempt: the page cap is a Go-side DoS guard on the pagination walk.
func TestSlackPaginationStopsAtMaxPages(t *testing.T) {
	var mu sync.Mutex
	page := 0
	recorder := &batch4Recorder{}
	server := httptest.NewServer(recorder.wrap(func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		mu.Lock()
		page++
		current := page
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"members":[{"id":"U`+strconv.Itoa(current)+`"}],"response_metadata":{"next_cursor":"cursor-`+strconv.Itoa(current)+`"}}`)
	}))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("slack_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithCredential("slack_tool", "xoxb-fake"),
	)

	result := batch4Execute(t, registry, "slack", map[string]any{"action": "slack_list_users"})
	members, ok := result["users"].([]any)
	if !ok || len(members) != DefaultMaxPages {
		t.Fatalf("merged members = %v", result["users"])
	}
	if recorder.count() != DefaultMaxPages {
		t.Fatalf("slack requests = %d, want %d", recorder.count(), DefaultMaxPages)
	}
}
