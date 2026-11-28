package integrations

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A34: the six declarative REST integrations must return the upstream Python
// tool's success body and error text, not the provider's raw wire body.
//
// The targeted tests below mirror tools/tests/tools/test_{github,slack,
// telegram,trello,airtable,stripe}_tool.py; the coverage test at the bottom
// makes provider-raw behavior impossible unless an action is explicitly
// registered as raw or implemented locally.

func shapeTestRegistry(t *testing.T, module, token string, handler func(*http.Request) (int, string)) *Registry {
	t.Helper()
	provider := newFakeProvider(t, handler)
	return New(
		WithBaseURL(module+"_tool", provider.server.URL),
		WithCredential(module+"_tool", token),
		WithHTTPClient(provider.server.Client()),
	)
}

func shapeExecute(t *testing.T, registry *Registry, module string, input map[string]any) map[string]any {
	t.Helper()
	return batch6Execute(t, registry, module, input)
}

func shapeListField(t *testing.T, object map[string]any, key string) []any {
	t.Helper()
	items, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%s is %T, want array (%v)", key, object[key], object)
	}
	return items
}

func shapeMapField(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	nested, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want object (%v)", key, object[key], object)
	}
	return nested
}

// Parity: tools/tests/tools/test_github_tool.py::TestGitHubIssues::test_get_issue_success
func TestGitHubSuccessEnvelope(t *testing.T) {
	registry := shapeTestRegistry(t, "github", "ghp_fake", func(*http.Request) (int, string) {
		return 200, `{"id":7,"number":7,"title":"Fix"}`
	})
	result := shapeExecute(t, registry, "github", map[string]any{
		"action": "github_get_issue", "owner": "acme", "repo": "widgets", "issue_number": 7,
	})
	if result["success"] != true {
		t.Fatalf("result = %v", result)
	}
	data := shapeMapField(t, result, "data")
	if data["id"] != float64(7) || data["title"] != "Fix" {
		t.Fatalf("data = %v", data)
	}
	if _, raw := result["id"]; raw {
		t.Fatalf("provider body leaked at the top level: %v", result)
	}
}

// Parity: tools/tests/tools/test_github_tool.py::TestErrorHandling (401/403/404)
// plus the 422 validation branch of _handle_response.
func TestGitHubErrorTexts(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"validation", 422, `{"message":"Validation failed"}`, "Validation error: Validation failed"},
		{"validation-default", 422, `{}`, "Validation error: Validation failed"},
		{"server", 500, `{"message":"Server exploded"}`, "GitHub API error (HTTP 500): Server exploded"},
		{"forbidden", 403, `{"message":"nope"}`, "Forbidden - check token permissions or rate limit"},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			registry := shapeTestRegistry(t, "github", "ghp_fake", func(*http.Request) (int, string) {
				return testCase.status, testCase.body
			})
			result := shapeExecute(t, registry, "github", map[string]any{
				"action": "github_get_repo", "owner": "acme", "repo": "widgets",
			})
			if result["error"] != testCase.want {
				t.Fatalf("error = %v, want %q", result["error"], testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackSendMessage::test_send_message_success
func TestSlackSendMessageEnvelope(t *testing.T) {
	registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(*http.Request) (int, string) {
		return 200, `{"ok":true,"channel":"C123","ts":"1234567890.123456","message":{"text":"Hello"}}`
	})
	result := shapeExecute(t, registry, "slack", map[string]any{
		"action": "slack_send_message", "channel": "C123", "text": "Hello",
	})
	if result["success"] != true || result["channel"] != "C123" || result["ts"] != "1234567890.123456" {
		t.Fatalf("result = %v", result)
	}
	if shapeMapField(t, result, "message")["text"] != "Hello" {
		t.Fatalf("message = %v", result["message"])
	}
	if _, raw := result["ok"]; raw {
		t.Fatalf("provider envelope leaked: %v", result)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackListChannels::test_list_channels_success
func TestSlackListChannelsProjection(t *testing.T) {
	registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(*http.Request) (int, string) {
		return 200, `{"ok":true,"channels":[
			{"id":"C001","name":"general","is_private":false,"num_members":50,"topic":{"value":"Team"}},
			{"id":"C002","name":"random"}
		]}`
	})
	result := shapeExecute(t, registry, "slack", map[string]any{"action": "slack_list_channels"})
	if result["success"] != true || result["count"] != 2 {
		t.Fatalf("result = %v", result)
	}
	channels := shapeListField(t, result, "channels")
	first := channels[0].(map[string]any)
	if first["id"] != "C001" || first["name"] != "general" || first["num_members"] != float64(50) || first["topic"] != "Team" {
		t.Fatalf("first channel = %v", first)
	}
	second := channels[1].(map[string]any)
	if second["is_private"] != false || second["num_members"] != float64(0) || second["topic"] != "" {
		t.Fatalf("defaults = %v", second)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackGetChannelInfo,
// TestSlackGetPresence, TestSlackListUsers (deleted members skipped).
func TestSlackChannelUserAndPresenceShapes(t *testing.T) {
	registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "conversations.info"):
			return 200, `{"ok":true,"channel":{"id":"C1","name":"general","is_channel":true,"topic":{"value":"T"},"purpose":{"value":"P"},"num_members":9}}`
		case strings.HasSuffix(r.URL.Path, "users.list"):
			return 200, `{"ok":true,"members":[{"id":"U1","name":"alice","is_admin":true},{"id":"U2","deleted":true}]}`
		default:
			return 200, `{"ok":true,"presence":"active","online":true,"auto_away":false}`
		}
	})
	info := shapeExecute(t, registry, "slack", map[string]any{"action": "slack_get_channel_info", "channel": "C1"})
	if info["topic"] != "T" || info["purpose"] != "P" || info["num_members"] != float64(9) {
		t.Fatalf("channel info = %v", info)
	}
	users := shapeExecute(t, registry, "slack", map[string]any{"action": "slack_list_users"})
	if users["count"] != 1 {
		t.Fatalf("users = %v", users)
	}
	if member := shapeListField(t, users, "users")[0].(map[string]any); member["id"] != "U1" || member["is_admin"] != true {
		t.Fatalf("member = %v", member)
	}
	presence := shapeExecute(t, registry, "slack", map[string]any{"action": "slack_get_presence", "user_id": "U1"})
	if presence["user_id"] != "U1" || presence["presence"] != "active" || presence["online"] != true {
		t.Fatalf("presence = %v", presence)
	}
	if _, present := presence["success"]; present {
		t.Fatalf("get_presence should not report success: %v", presence)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackSendMessage::test_send_message_invalid_auth,
// test_send_message_channel_not_found; missing_scope mirrors _handle_response.
func TestSlackErrorSpecialization(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"invalid-auth", 200, `{"ok":false,"error":"invalid_auth"}`, "Invalid Slack bot token"},
		{"revoked", 200, `{"ok":false,"error":"token_revoked"}`, "Slack bot token has been revoked"},
		{"channel", 200, `{"ok":false,"error":"channel_not_found"}`, "Channel not found or bot is not a member"},
		{"missing-scope", 200, `{"ok":false,"error":"missing_scope","needed":"channels:read"}`, "Missing required scope: channels:read"},
		{"http", 429, `{"ok":false}`, `HTTP error 429: {"ok":false}`},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(*http.Request) (int, string) {
				return testCase.status, testCase.body
			})
			result := shapeExecute(t, registry, "slack", map[string]any{
				"action": "slack_send_message", "channel": "C1", "text": "hi",
			})
			if result["error"] != testCase.want {
				t.Fatalf("error = %v, want %q", result["error"], testCase.want)
			}
		})
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackFindUserByEmail
// returns the provider body unchanged; this is the one registered raw Slack
// lookup reachable through the declarative specs.
func TestSlackRawLookupStaysRaw(t *testing.T) {
	registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(*http.Request) (int, string) {
		return 200, `{"ok":true,"user":{"id":"U1","name":"alice"}}`
	})
	result := shapeExecute(t, registry, "slack", map[string]any{
		"action": "slack_find_user_by_email", "email": "a@example.com",
	})
	if result["ok"] != true || shapeMapField(t, result, "user")["id"] != "U1" {
		t.Fatalf("result = %v", result)
	}
	if _, wrapped := result["success"]; wrapped {
		t.Fatalf("raw action was wrapped: %v", result)
	}
}

// Parity: tools/tests/tools/test_telegram_tool.py::TestTelegramClient::test_send_message
// returns the full {"ok":true,"result":...} envelope; get_chat_member_count is
// the one projection.
func TestTelegramEnvelopeAndMemberCount(t *testing.T) {
	registry := shapeTestRegistry(t, "telegram", "123:fake", func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "getChatMemberCount") {
			return 200, `{"ok":true,"result":42}`
		}
		return 200, `{"ok":true,"result":{"message_id":7,"text":"hi"}}`
	})
	sent := shapeExecute(t, registry, "telegram", map[string]any{
		"action": "telegram_send_message", "chat_id": 99, "text": "hi",
	})
	if sent["ok"] != true || shapeMapField(t, sent, "result")["message_id"] != float64(7) {
		t.Fatalf("result = %v", sent)
	}
	count := shapeExecute(t, registry, "telegram", map[string]any{
		"action": "telegram_get_chat_member_count", "chat_id": 99,
	})
	if count["chat_id"] != 99 || count["member_count"] != float64(42) {
		t.Fatalf("member count = %v", count)
	}
	if _, envelope := count["result"]; envelope {
		t.Fatalf("member count kept the provider envelope: %v", count)
	}
}

// Parity: tools/tests/tools/test_telegram_tool.py::TestErrorHandling::test_handle_response_generic_error
// and the 401/400/403/404/429 branches of _handle_response.
func TestTelegramErrorTexts(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"ok":false,"error_code":401,"description":"Unauthorized"}`, "Invalid Telegram bot token"},
		{400, `{"ok":false,"error_code":400,"description":"chat not found"}`, "Bad request: chat not found"},
		{403, `{"ok":false,"error_code":403,"description":"Forbidden"}`, "Bot was blocked by the user or lacks permissions"},
		{404, `{"ok":false,"error_code":404,"description":"Not Found"}`, "Chat not found"},
		{429, `{"ok":false,"error_code":429,"description":"Too Many Requests"}`, "Rate limit exceeded. Try again later."},
		{500, `{"ok":false,"error_code":500,"description":"Internal server error"}`, "Telegram API error (HTTP 500): Internal server error"},
	}
	for _, testCase := range cases {
		testCase := testCase
		registry := shapeTestRegistry(t, "telegram", "123:fake", func(*http.Request) (int, string) {
			return testCase.status, testCase.body
		})
		result := shapeExecute(t, registry, "telegram", map[string]any{
			"action": "telegram_send_message", "chat_id": 99, "text": "hi",
		})
		if result["error"] != testCase.want {
			t.Fatalf("status %d error = %v, want %q", testCase.status, result["error"], testCase.want)
		}
	}
}

// Parity: tools/tests/tools/test_trello_tool.py::TestTrelloTools::test_list_boards_success,
// test_get_member_success; the list/search projections mirror trello_tool.py.
func TestTrelloListShapes(t *testing.T) {
	registry := shapeTestRegistry(t, "trello", "key|token", func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/boards"):
			return 200, `[{"id":"b1","name":"Board"}]`
		case strings.HasSuffix(r.URL.Path, "/lists"):
			return 200, `[{"id":"l1","name":"List"}]`
		case strings.HasSuffix(r.URL.Path, "/cards"):
			return 200, `[{"id":"c1","name":"Card"}]`
		case strings.HasSuffix(r.URL.Path, "/search"):
			return 200, `{"cards":[{"id":"c1","name":"Card"}]}`
		default:
			return 200, `{"id":"m1","fullName":"Alice"}`
		}
	})
	boards := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_list_boards"})
	if len(shapeListField(t, boards, "boards")) != 1 {
		t.Fatalf("boards = %v", boards)
	}
	lists := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_list_lists", "board_id": "b1"})
	if len(shapeListField(t, lists, "lists")) != 1 {
		t.Fatalf("lists = %v", lists)
	}
	cards := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_list_cards", "list_id": "l1"})
	if len(shapeListField(t, cards, "cards")) != 1 {
		t.Fatalf("cards = %v", cards)
	}
	found := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_search_cards", "query": "Card"})
	if found["count"] != 1 || len(shapeListField(t, found, "cards")) != 1 {
		t.Fatalf("search = %v", found)
	}
	member := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_get_member"})
	if member["id"] != "m1" {
		t.Fatalf("member = %v", member)
	}
}

// Parity: tools/tests/tools/test_trello_tool.py::TestTrelloClientErrorHandling::test_not_found,
// TestTrelloTools::test_rate_limit_error.
func TestTrelloErrorTexts(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{401, `{"message":"bad"}`, "Invalid Trello API key or token"},
		{403, `{"message":"bad"}`, "Insufficient permissions. Check your Trello token scopes."},
		{404, `{"message":"nope"}`, "Resource not found"},
		{429, `{"message":"rate"}`, "Trello rate limit exceeded. Try again later."},
		{400, `{"message":"bad request"}`, "Trello API error (HTTP 400): bad request"},
	}
	for _, testCase := range cases {
		testCase := testCase
		registry := shapeTestRegistry(t, "trello", "key|token", func(*http.Request) (int, string) {
			return testCase.status, testCase.body
		})
		result := shapeExecute(t, registry, "trello", map[string]any{"action": "trello_list_boards"})
		if result["error"] != testCase.want {
			t.Fatalf("status %d error = %v, want %q", testCase.status, result["error"], testCase.want)
		}
	}
}

// Parity: tools/tests/tools/test_airtable_tool.py::TestAirtableListRecords::test_successful_list,
// test_pagination; TestAirtableCreateRecords::test_successful_create.
func TestAirtableRecordShapes(t *testing.T) {
	pages := 0
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return 200, `{"records":[{"id":"rec1","fields":{"Name":"Project Alpha"},"createdTime":"2026-01-01T00:00:00.000Z"}]}`
		}
		pages++
		if pages == 1 {
			return 200, `{"records":[{"id":"rec1","fields":{"Name":"Project Alpha"},"createdTime":"2026-01-01T00:00:00.000Z"}],"offset":"itrXXX/recXXX"}`
		}
		return 200, `{"records":[{"id":"rec2","fields":{"Name":"Project Beta"}}]}`
	})
	registry := New(
		WithBaseURL("airtable_tool", provider.server.URL),
		WithCredential("airtable_tool", "pat_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	listed := shapeExecute(t, registry, "airtable", map[string]any{
		"action": "airtable_list_records", "base_id": "appXXX", "table_name": "Tasks",
	})
	if listed["count"] != 2 {
		t.Fatalf("list = %v", listed)
	}
	if _, offset := listed["offset"]; offset {
		t.Fatalf("offset still advertised after the walk: %v", listed)
	}
	record := shapeListField(t, listed, "records")[0].(map[string]any)
	if record["id"] != "rec1" || shapeMapField(t, record, "fields")["Name"] != "Project Alpha" {
		t.Fatalf("record = %v", record)
	}
	if record["created_time"] != "2026-01-01T00:00:00.000Z" {
		t.Fatalf("created_time = %v", record["created_time"])
	}
	if pages != 2 {
		t.Fatalf("pages = %d, want the merged walk (Go-side offset following)", pages)
	}

	created := shapeExecute(t, registry, "airtable", map[string]any{
		"action": "airtable_create_records", "base_id": "appXXX", "table_name": "Tasks",
		"records": `[{"fields":{"Name":"Project Alpha"}}]`,
	})
	if created["result"] != "created" || created["count"] != 1 {
		t.Fatalf("create = %v", created)
	}
	body := provider.snapshot()[2].body
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("create body: %v (%s)", err, body)
	}
	if _, isArray := payload["records"].([]any); !isArray {
		t.Fatalf("records not embedded structurally: %s", body)
	}
}

// Parity: tools/tests/tools/test_airtable_tool.py::TestAirtableDeleteRecords,
// TestAirtableListBases, TestAirtableGetBaseSchema; the raw HTTP error text is
// airtable_tool.py's `HTTP {status}: {text[:500]}`.
func TestAirtableDeleteMetadataAndError(t *testing.T) {
	registry := shapeTestRegistry(t, "airtable", "pat_fake", func(r *http.Request) (int, string) {
		switch {
		case r.Method == http.MethodDelete:
			return 200, `{"records":[{"id":"rec1","deleted":true},{"id":"rec2","deleted":false}]}`
		case strings.HasSuffix(r.URL.Path, "/meta/bases"):
			return 200, `{"bases":[{"id":"app1","name":"Base","permissionLevel":"create"}]}`
		case strings.HasSuffix(r.URL.Path, "/tables"):
			return 200, `{"tables":[{"id":"tbl1","name":"Tasks","fields":[{"id":"fld1","name":"Name","type":"singleLineText"}]}]}`
		default:
			return 200, `{"collaborators":[{"userId":"usr1","email":"a@example.com","permissionLevel":"edit"}]}`
		}
	})
	deleted := shapeExecute(t, registry, "airtable", map[string]any{
		"action": "airtable_delete_records", "base_id": "app1", "table_name": "Tasks", "record_ids": "rec1,rec2",
	})
	if deleted["result"] != "deleted" || deleted["count"] != 2 {
		t.Fatalf("delete = %v", deleted)
	}
	ids := shapeListField(t, deleted, "deleted_ids")
	if len(ids) != 1 || ids[0] != "rec1" {
		t.Fatalf("deleted_ids = %v", ids)
	}
	bases := shapeExecute(t, registry, "airtable", map[string]any{"action": "airtable_list_bases"})
	if bases["count"] != 1 || shapeListField(t, bases, "bases")[0].(map[string]any)["permission_level"] != "create" {
		t.Fatalf("bases = %v", bases)
	}
	schema := shapeExecute(t, registry, "airtable", map[string]any{"action": "airtable_get_base_schema", "base_id": "app1"})
	table := shapeListField(t, schema, "tables")[0].(map[string]any)
	if table["name"] != "Tasks" || shapeListField(t, table, "fields")[0].(map[string]any)["type"] != "singleLineText" {
		t.Fatalf("schema = %v", schema)
	}
	collaborators := shapeExecute(t, registry, "airtable", map[string]any{"action": "airtable_list_collaborators", "base_id": "app1"})
	if collaborators["count"] != 1 || shapeListField(t, collaborators, "collaborators")[0].(map[string]any)["user_id"] != "usr1" {
		t.Fatalf("collaborators = %v", collaborators)
	}

	errorRegistry := shapeTestRegistry(t, "airtable", "pat_fake", func(*http.Request) (int, string) {
		return 401, `{"error":"AUTHENTICATION_REQUIRED"}`
	})
	failed := shapeExecute(t, errorRegistry, "airtable", map[string]any{"action": "airtable_list_bases"})
	if failed["error"] != `HTTP 401: {"error":"AUTHENTICATION_REQUIRED"}` {
		t.Fatalf("error = %v", failed["error"])
	}
}

// Parity: tools/tests/tools/test_stripe_tool.py::TestStripeClientCustomers::test_get_customer,
// test_list_customers.
func TestStripeCustomerShapes(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/v1/customers/cus_1") {
			return 200, `{"id":"cus_1","object":"customer","email":"a@example.com","name":"Alice","phone":null,"description":"d","created":1700000000,"currency":"usd","delinquent":false,"metadata":{"k":"v"},"livemode":false}`
		}
		if r.URL.Query().Get("email") == "nobody@example.com" {
			return 200, `{"object":"list","data":[],"has_more":false}`
		}
		return 200, `{"object":"list","data":[{"id":"cus_1","object":"customer","email":"a@example.com"},{"id":"cus_2","object":"customer"}],"has_more":false}`
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	customer := shapeExecute(t, registry, "stripe", map[string]any{
		"action": "stripe_get_customer", "customer_id": "cus_1",
	})
	if customer["id"] != "cus_1" || customer["email"] != "a@example.com" || customer["metadata"].(map[string]any)["k"] != "v" {
		t.Fatalf("customer = %v", customer)
	}
	if _, leaked := customer["object"]; leaked {
		t.Fatalf("provider object field leaked: %v", customer)
	}
	listed := shapeExecute(t, registry, "stripe", map[string]any{"action": "stripe_list_customers"})
	if listed["has_more"] != false || len(shapeListField(t, listed, "customers")) != 2 {
		t.Fatalf("list = %v", listed)
	}
	notFound := shapeExecute(t, registry, "stripe", map[string]any{
		"action": "stripe_get_customer_by_email", "email": "nobody@example.com",
	})
	if notFound["error"] != "No customer found with email: nobody@example.com" {
		t.Fatalf("missing = %v", notFound)
	}
}

// Parity: tools/tests/tools/test_stripe_tool.py::TestStripeClientPrices::test_get_price,
// test_list_balance_transactions, TestStripeClientPaymentIntents/list_events.
func TestStripeResourceFormatters(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/v1/prices/price_1"):
			return 200, `{"id":"price_1","product":"prod_1","currency":"usd","unit_amount":999,"nickname":null,"active":true,"type":"recurring","recurring":{"interval":"month","interval_count":1},"created":1700000000,"metadata":{}}`
		case strings.HasSuffix(r.URL.Path, "/v1/balance"):
			return 200, `{"available":[{"amount":10000,"currency":"usd"}],"pending":[{"amount":5000,"currency":"usd"}]}`
		case strings.HasSuffix(r.URL.Path, "/v1/events"):
			return 200, `{"object":"list","data":[{"id":"evt_1","type":"charge.succeeded","created":1700000000,"data":{"object":{"id":"ch_1","object":"charge"}}}],"has_more":false}`
		default:
			return 200, `{"id":"cs_1","url":"https://checkout","mode":"payment","status":"open","payment_status":"unpaid","customer":"cus_1","amount_total":999,"currency":"usd","created":1700000000}`
		}
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	price := shapeExecute(t, registry, "stripe", map[string]any{"action": "stripe_get_price", "price_id": "price_1"})
	recurring := shapeMapField(t, price, "recurring")
	if price["unit_amount"] != float64(999) || recurring["interval"] != "month" || recurring["interval_count"] != float64(1) {
		t.Fatalf("price = %v", price)
	}
	balance := shapeExecute(t, registry, "stripe", map[string]any{"action": "stripe_get_balance"})
	available := shapeListField(t, balance, "available")[0].(map[string]any)
	if available["amount"] != float64(10000) || available["currency"] != "usd" {
		t.Fatalf("balance = %v", balance)
	}
	events := shapeExecute(t, registry, "stripe", map[string]any{"action": "stripe_list_events"})
	event := shapeListField(t, events, "events")[0].(map[string]any)
	if event["object_id"] != "ch_1" || event["type"] != "charge.succeeded" {
		t.Fatalf("event = %v", event)
	}
	checkout := shapeExecute(t, registry, "stripe", map[string]any{
		"action": "stripe_create_checkout_session", "line_items_json": `[{"price":"price_1","quantity":1}]`,
	})
	if checkout["id"] != "cs_1" || checkout["payment_status"] != "unpaid" || checkout["amount_total"] != float64(999) {
		t.Fatalf("checkout = %v", checkout)
	}
}

// Parity: tools/tests/tools/test_stripe_tool.py::TestStripeClientSubscriptions::test_get_subscription_status_active
// and test_cancel_subscription_immediately format the subscription like get_subscription.
func TestStripeLocalSubscriptionShapes(t *testing.T) {
	provider := newFakeProvider(t, func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "/v1/subscriptions") && r.Method == http.MethodGet {
			return 200, `{"object":"list","data":[{"id":"sub_1","customer":"cus_1","status":"active","items":{"data":[{"id":"si_1","price":{"id":"price_1"},"quantity":2}]},"metadata":{}}],"has_more":false}`
		}
		return 200, `{"id":"sub_1","customer":"cus_1","status":"canceled","items":{"data":[]},"metadata":{}}`
	})
	registry := New(
		WithBaseURL("stripe_tool", provider.server.URL),
		WithCredential("stripe_tool", "sk_test_fake"),
		WithHTTPClient(provider.server.Client()),
	)
	status := shapeExecute(t, registry, "stripe", map[string]any{
		"action": "stripe_get_subscription_status", "customer_id": "cus_1",
	})
	if status["status"] != "active" {
		t.Fatalf("status = %v", status)
	}
	subscription := shapeListField(t, status, "subscriptions")[0].(map[string]any)
	item := shapeListField(t, subscription, "items")[0].(map[string]any)
	if subscription["id"] != "sub_1" || item["price_id"] != "price_1" || item["quantity"] != float64(2) {
		t.Fatalf("subscription = %v", subscription)
	}
	cancelled := shapeExecute(t, registry, "stripe", map[string]any{
		"action": "stripe_cancel_subscription", "subscription_id": "sub_1",
	})
	if cancelled["id"] != "sub_1" || cancelled["status"] != "canceled" {
		t.Fatalf("cancelled = %v", cancelled)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackUploadFile returns the
// projected {success,file:{id,name,title,permalink}}.
func TestSlackUploadFileShape(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "files.getUploadURLExternal"):
			_, _ = io.WriteString(w, `{"ok":true,"upload_url":"`+server.URL+`/upload","file_id":"F1"}`)
		case r.URL.Path == "/upload":
			_, _ = io.WriteString(w, "")
		case strings.HasSuffix(r.URL.Path, "files.completeUploadExternal"):
			_, _ = io.WriteString(w, `{"ok":true,"files":[{"id":"F1","name":"report.txt","title":"Report","permalink":"https://slack/F1"}]}`)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	registry := New(
		WithBaseURL("slack_tool", server.URL),
		WithCredential("slack_tool", "xoxb-fake"),
		WithHTTPClient(server.Client()),
	)
	result := shapeExecute(t, registry, "slack", map[string]any{
		"action": "slack_upload_file", "channel": "C1", "content": "hello", "filename": "report.txt", "title": "Report",
	})
	file := shapeMapField(t, result, "file")
	if result["success"] != true || file["id"] != "F1" || file["name"] != "report.txt" || file["permalink"] != "https://slack/F1" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_slack_tool.py::TestSlackGetTeamStats.
func TestSlackTeamStatsShape(t *testing.T) {
	registry := shapeTestRegistry(t, "slack", "xoxb-fake", func(r *http.Request) (int, string) {
		if strings.HasSuffix(r.URL.Path, "team.info") {
			return 200, `{"ok":true,"team":{"id":"T1","name":"Acme","domain":"acme"}}`
		}
		return 200, `{"ok":true,"members":[{"id":"U1"}]}`
	})
	result := shapeExecute(t, registry, "slack", map[string]any{"action": "slack_get_team_stats"})
	if result["team_name"] != "Acme" || result["team_domain"] != "acme" || result["member_count_sample"] != 1 {
		t.Fatalf("result = %v", result)
	}
}

// TestSixRESTModuleShapeCoverage enforces A34's rule: every one of the 158
// actions in github/slack/telegram/airtable/trello/stripe either has an
// explicit response formatter or is a registered local implementation whose
// output is shaped in Go code. Provider-raw passthrough must be an explicit
// rawShape registration, never the default.
// Parity-exempt: Go-only 158-action shape coverage gate.
func TestSixRESTModuleShapeCoverage(t *testing.T) {
	registry := New()
	localShaped := map[string]map[string]bool{
		"github": {
			"github_list_repos":         true,
			"github_get_user_emails":    true,
			"github_list_workflow_runs": true,
		},
		"slack": {
			"slack_send_dm":                   true,
			"slack_upload_file":               true,
			"slack_get_messages_for_analysis": true,
			"slack_get_conversation_context":  true,
			"slack_get_team_stats":            true,
			"slack_trigger_workflow":          true,
		},
		"stripe": {
			"stripe_get_subscription_status": true,
			"stripe_cancel_subscription":     true,
		},
	}
	total := 0
	for _, module := range []string{"github", "slack", "telegram", "airtable", "trello", "stripe"} {
		tool, ok := registry.Lookup(module)
		if !ok {
			t.Fatalf("%s missing from registry", module)
		}
		specs := registry.Specs(module)
		for _, action := range tool.Actions {
			total++
			if spec, ok := specs[action.Name]; ok && spec.Shape != nil {
				continue
			}
			if localShaped[module][action.Name] {
				continue
			}
			t.Errorf("%s.%s has no response formatter", module, action.Name)
		}
	}
	if total != 158 {
		t.Fatalf("six-module action count = %d, want 158", total)
	}
}
