package integrations

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// recordedRequest is one request captured by the fake provider.
type recordedRequest struct {
	method string
	path   string
	query  map[string]string
	header http.Header
	body   []byte
}

type fakeProvider struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []recordedRequest
	response func(r *http.Request) (int, string)
}

func newFakeProvider(t *testing.T, response func(r *http.Request) (int, string)) *fakeProvider {
	t.Helper()
	provider := &fakeProvider{response: response}
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		query := map[string]string{}
		for key, values := range r.URL.Query() {
			if len(values) > 0 {
				query[key] = values[0]
			}
		}
		provider.mu.Lock()
		provider.requests = append(provider.requests, recordedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  query,
			header: r.Header.Clone(),
			body:   body,
		})
		provider.mu.Unlock()
		status, payload := 200, `{}`
		if provider.response != nil {
			status, payload = provider.response(r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *fakeProvider) snapshot() []recordedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedRequest(nil), p.requests...)
}

func providerPayload(module string, r *http.Request) string {
	switch module {
	case "stripe_tool":
		if r.Method == http.MethodGet || r.Method == http.MethodDelete {
			return `{"object":"list","data":[{"id":"cus_1"}],"has_more":false}`
		}
		return `{"id":"obj_1","object":"customer"}`
	case "slack_tool":
		return `{"ok":true,"ts":"1700000000.000100","channel":"C1","messages":[],"channels":[],"users":[],"files":[{"id":"F1"}],"file":{"id":"F1"}}`
	case "github_tool":
		return `{"id":1,"items":[],"total_count":0,"workflow_runs":[],"message":"ok"}`
	case "telegram_tool":
		return `{"ok":true,"result":{"message_id":1}}`
	case "trello_tool":
		return `{"id":"card1","name":"Card"}`
	case "airtable_tool":
		return `{"records":[],"bases":[],"tables":[]}`
	default:
		return `{}`
	}
}

func sampleValue(schema map[string]any) any {
	switch schema["type"] {
	case "integer":
		return 1
	case "number":
		return 1.5
	case "boolean":
		return true
	case "array":
		return []any{"a"}
	case "object":
		return map[string]any{"k": "v"}
	default:
		return "v"
	}
}

// restModules maps a tool to its fake endpoint base and credential.
type restModule struct {
	tool  string
	token string
}

var restModules = []restModule{
	{tool: "stripe", token: "sk_test_fake"},
	{tool: "slack", token: "xoxb-fake"},
	{tool: "github", token: "ghp_fake"},
	{tool: "telegram", token: "123:fake"},
	{tool: "trello", token: "key|token"},
	{tool: "airtable", token: "pat_fake"},
}

// Parity-exempt: upstream has no equivalent registry-wide sweep; each action's shape is
// covered per tool in tools/tests/tools/test_*_tool.py -- this test pins that every
// declarative action executes through the Go registry.
func TestEveryDeclarativeActionExecutes(t *testing.T) {
	registry := New()
	for _, module := range restModules {
		module := module
		provider := newFakeProvider(t, func(r *http.Request) (int, string) { return 200, providerPayload(module.tool+"_tool", r) })
		scoped := New(
			WithBaseURL(module.tool+"_tool", provider.server.URL),
			WithCredential(module.tool+"_tool", module.token),
			WithHTTPClient(provider.server.Client()),
		)
		tool, _ := scoped.Lookup(module.tool)
		t.Run(module.tool, func(t *testing.T) {
			executed := 0
			for _, action := range tool.Actions {
				if _, ok := scoped.Specs(module.tool)[action.Name]; !ok {
					continue
				}
				input := map[string]any{"action": action.Name}
				for _, name := range action.Required {
					schema, _ := action.Params[name].(map[string]any)
					input[name] = sampleValue(schema)
				}
				result, err := scoped.Execute(context.Background(), module.tool, input)
				if err != nil {
					t.Fatalf("%s: %v", action.Name, err)
				}
				if problem, ok := result.(map[string]any); ok {
					if message, ok := problem["error"].(string); ok {
						t.Fatalf("%s returned error: %s", action.Name, message)
					}
				}
				executed++
			}
			if executed == 0 {
				t.Fatal("no declarative actions executed")
			}
			if len(provider.snapshot()) < executed {
				t.Fatalf("requests = %d, executed = %d", len(provider.snapshot()), executed)
			}
			for _, request := range provider.snapshot() {
				if strings.Contains(request.path, "{") {
					t.Fatalf("unfilled path placeholder: %s", request.path)
				}
			}
		})
	}
	_ = registry
}

// Parity: tools/tests/tools/test_stripe_tool.py::TestStripeClientCustomers::test_get_customer, tools/tests/tools/test_stripe_tool.py::TestStripeClientCustomers::test_create_customer
func TestStripeRequestShape(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"id":"cus_1","object":"customer"}`
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_get_customer", "customer_id": "cus_1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_create_customer", "email": "a@example.com", "name": "Alice",
	}); err != nil {
		t.Fatal(err)
	}
	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].method != http.MethodGet || requests[0].path != "/v1/customers/cus_1" {
		t.Fatalf("get = %s %s", requests[0].method, requests[0].path)
	}
	if got := requests[0].header.Get("Authorization"); got != "Bearer sk_test_fake" {
		t.Fatalf("auth = %q", got)
	}
	if requests[1].method != http.MethodPost || requests[1].path != "/v1/customers" {
		t.Fatalf("create = %s %s", requests[1].method, requests[1].path)
	}
	if got := requests[1].header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
		t.Fatalf("content type = %q", got)
	}
	body := string(requests[1].body)
	if !strings.Contains(body, "email=a%40example.com") || !strings.Contains(body, "name=Alice") {
		t.Fatalf("body = %s", body)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackSendMessage::test_send_message_success
func TestSlackRequestShape(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"ok":true,"channel":"C1","ts":"1.2"}`
	})
	registry := New(
		WithBaseURL("slack_tool", provider.server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "slack", map[string]any{
		"action": "slack_send_message", "channel": "C1", "text": "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["ts"] != "1.2" {
		t.Fatalf("result = %v", result)
	}
	request := provider.snapshot()[0]
	if request.path != "/chat.postMessage" || request.method != http.MethodPost {
		t.Fatalf("request = %s %s", request.method, request.path)
	}
	var body map[string]any
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatalf("body: %v (%s)", err, request.body)
	}
	if body["channel"] != "C1" || body["text"] != "hello" {
		t.Fatalf("body = %v", body)
	}
	if got := request.header.Get("Authorization"); got != "Bearer xoxb-fake" {
		t.Fatalf("auth = %q", got)
	}
}

// Parity: tools/tests/tools/test_github_tool.py::TestGitHubClient::test_headers, tools/tests/tools/test_github_tool.py::TestGitHubIssues::test_get_issue_success
func TestGitHubRequestShape(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"id":7,"number":7}`
	})
	registry := New(
		WithBaseURL("github_tool", provider.server.URL),
		WithCredential("github_tool", "ghp_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_get_issue", "owner": "acme", "repo": "widgets", "issue_number": 7,
	}); err != nil {
		t.Fatal(err)
	}
	request := provider.snapshot()[0]
	if request.path != "/repos/acme/widgets/issues/7" {
		t.Fatalf("path = %s", request.path)
	}
	if request.header.Get("Accept") != "application/vnd.github+json" {
		t.Fatalf("accept = %q", request.header.Get("Accept"))
	}
	if request.header.Get("X-GitHub-Api-Version") != "2022-11-28" {
		t.Fatalf("version = %q", request.header.Get("X-GitHub-Api-Version"))
	}
}

// Parity: tools/tests/tools/test_telegram_tool.py::TestTelegramClient::test_base_url, tools/tests/tools/test_telegram_tool.py::TestTelegramClient::test_send_message
func TestTelegramTokenInPathAndUnwrap(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"ok":true,"result":{"message_id":42}}`
	})
	registry := New(
		WithBaseURL("telegram_tool", provider.server.URL+"/bot{token}"),
		WithCredential("telegram_tool", "123:fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "telegram", map[string]any{
		"action": "telegram_send_message", "chat_id": 99, "text": "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["message_id"] != float64(42) {
		t.Fatalf("result = %v", result)
	}
	request := provider.snapshot()[0]
	if request.path != "/bot123:fake/sendMessage" {
		t.Fatalf("path = %s", request.path)
	}
}

// Parity-exempt: upstream only validates trello_create_card arguments
// (test_create_card_requires_name, test_create_card_desc_too_long); the key/token
// query auth and the POST /cards shape are pinned here only.
func TestTrelloQueryAuth(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"id":"card1"}`
	})
	registry := New(
		WithBaseURL("trello_tool", provider.server.URL),
		WithCredential("trello_tool", "key|token"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "trello", map[string]any{
		"action": "trello_create_card", "list_id": "L1", "name": "Card",
	}); err != nil {
		t.Fatal(err)
	}
	request := provider.snapshot()[0]
	if request.query["key"] != "key" || request.query["token"] != "token" {
		t.Fatalf("query = %v", request.query)
	}
	if request.path != "/cards" || request.method != http.MethodPost {
		t.Fatalf("request = %s %s", request.method, request.path)
	}
	if !strings.Contains(string(request.body), "idList=L1") {
		t.Fatalf("body = %s", request.body)
	}
}

// Parity: tools/tests/tools/test_airtable_tool.py::TestAirtableListRecords::test_successful_list, tools/tests/tools/test_airtable_tool.py::TestAirtableListRecords::test_pagination
func TestAirtableRequestShape(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 200, `{"records":[]}`
	})
	registry := New(
		WithBaseURL("airtable_tool", provider.server.URL),
		WithCredential("airtable_tool", "pat_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	if _, err := registry.Execute(context.Background(), "airtable", map[string]any{
		"action": "airtable_list_records", "base_id": "app1", "table_name": "Tasks",
		"filter_formula": "{Done}=0", "sort_field": "Name", "sort_direction": "asc", "max_records": 5,
	}); err != nil {
		t.Fatal(err)
	}
	request := provider.snapshot()[0]
	if request.path != "/app1/Tasks" {
		t.Fatalf("path = %s", request.path)
	}
	if request.query["filterByFormula"] != "{Done}=0" || request.query["maxRecords"] != "5" {
		t.Fatalf("query = %v", request.query)
	}
	if request.query["sort[0][field]"] != "Name" {
		t.Fatalf("sort = %v", request.query)
	}
}

// Parity: tools/tests/tools/test_stripe_tool.py::TestToolRegistration::test_no_credentials_returns_error
func TestCredentialMissingIsValueError(t *testing.T) {
	registry := New()
	result, err := registry.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_get_customer", "customer_id": "cus_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	problem := result.(map[string]any)
	if problem["error"] != "Stripe credentials not configured" {
		t.Fatalf("error = %v", problem)
	}
	if !strings.Contains(problem["help"].(string), "STRIPE_API_KEY") {
		t.Fatalf("help = %v", problem["help"])
	}
}

// Parity: tools/tests/tools/test_github_tool.py::TestGitHubClient::test_handle_response_401, tools/tests/tools/test_stripe_tool.py::TestToolRegistration::test_stripe_error_is_caught
func TestProviderErrorMapping(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 401, `{"message":"Bad credentials"}`
	})
	registry := New(
		WithBaseURL("github_tool", provider.server.URL),
		WithCredential("github_tool", "ghp_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	result, err := registry.Execute(context.Background(), "github", map[string]any{
		"action": "github_get_repo", "owner": "acme", "repo": "widgets",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["error"] != "Invalid or expired GitHub token" {
		t.Fatalf("result = %v", result)
	}

	stripeProvider := newFakeProvider(t, func(r *http.Request) (int, string) {
		return 402, `{"error":{"message":"Your card was declined","type":"card_error"}}`
	})
	stripe := New(
		WithBaseURL("stripe_tool", stripeProvider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(stripeProvider.server.Client()),
	)
	failed, err := stripe.Execute(context.Background(), "stripe", map[string]any{
		"action": "stripe_create_customer", "email": "a@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	mapped := failed.(map[string]any)
	if mapped["error"] != "Your card was declined" || mapped["type"] != "card_error" {
		t.Fatalf("stripe error = %v", mapped)
	}
}
