package server

// Input validation for the AI set_trigger tool. Upstream's Python set_trigger
// is stricter than a plain JSON decode: interval_minutes must be a real number
// (not a numeric string), webhook methods must be a list of strings, and an
// explicit port must convert to an integer before it is used. These tables pin
// the binding-level behavior the tool exposes.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
)

// newTriggerValidationBinding builds a fresh session per table row so an
// activation in one row cannot leak an active trigger into the next.
func newTriggerValidationBinding(t *testing.T) (*Session, *QueenLifecycleBinding) {
	t.Helper()
	session, binding, _ := lifecycleTestSession(t)
	session.WorkerPath = t.TempDir()
	t.Cleanup(session.StopAllTriggers)
	return session, binding
}

// installFakeTriggerWebhookServer swaps the webhook server factory for the
// duration of one subtest and reports the port StartTriggerWebhook asked for.
func installFakeTriggerWebhookServer(t *testing.T, onPort func(int)) *fakeTriggerWebhookServer {
	t.Helper()
	previous := NewTriggerWebhookServer
	fake := &fakeTriggerWebhookServer{}
	NewTriggerWebhookServer = func(_ *eventbus.EventBus, _ string, port int) TriggerWebhookServer {
		if onPort != nil {
			onPort(port)
		}
		return fake
	}
	t.Cleanup(func() { NewTriggerWebhookServer = previous })
	return fake
}

func requireTriggerToolError(t *testing.T, content string, isError bool, want string) {
	t.Helper()
	if !isError {
		t.Fatalf("expected error containing %q, got success: %s", want, content)
	}
	payload := decodeToolPayload(t, content)
	message, ok := payload["error"].(string)
	if !ok || !strings.Contains(message, want) {
		t.Fatalf("error = %#v, want substring %q", payload["error"], want)
	}
	if len(payload) != 1 {
		t.Fatalf("error payload has extra keys: %#v", payload)
	}
}

func requireTriggerActivated(t *testing.T, content string, isError bool) map[string]any {
	t.Helper()
	if isError {
		t.Fatalf("activation failed: %s", content)
	}
	payload := decodeToolPayload(t, content)
	if payload["status"] != "activated" {
		t.Fatalf("payload = %#v, want status activated", payload)
	}
	return payload
}

// Parity-exempt: upstream has no test for set_trigger input validation; this
// pins the isinstance(interval, (int, float)) gate traced to
// framework/tools/queen_lifecycle_tools.py::set_trigger. A numeric string is
// truthy in Python, so it reaches the "must be > 0" error instead of being
// parsed as a number.
func TestSetTriggerIntervalRequiresNumber(t *testing.T) {
	cases := []struct {
		name      string
		interval  any
		wantError string
	}{
		{name: "integer", interval: 5},
		{name: "float", interval: 0.5},
		{name: "numeric string", interval: "5", wantError: "interval_minutes must be > 0, got 5"},
		{name: "decimal string", interval: "2.5", wantError: "interval_minutes must be > 0, got 2.5"},
		{name: "nonnumeric string", interval: "soon", wantError: "interval_minutes must be > 0, got soon"},
		{name: "list", interval: []any{5}, wantError: "interval_minutes must be > 0, got [5]"},
		{name: "empty string falls through to the missing-config error", interval: "", wantError: "needs 'cron' or 'interval_minutes'"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session, binding := newTriggerValidationBinding(t)
			content, isError := binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
				"trigger_id":     "interval-check",
				"trigger_type":   "timer",
				"trigger_config": map[string]any{"interval_minutes": testCase.interval},
				"task":           "check the inbox",
			})
			if testCase.wantError != "" {
				requireTriggerToolError(t, content, isError, testCase.wantError)
				if session.IsTriggerActive("interval-check") {
					t.Fatal("rejected timer must not be active")
				}
				return
			}
			requireTriggerActivated(t, content, isError)
			if !session.IsTriggerActive("interval-check") {
				t.Fatal("accepted timer must be active")
			}
		})
	}
}

// Parity-exempt: upstream has no test for set_trigger input validation; this
// pins the list-of-strings methods requirement traced to
// framework/tools/queen_lifecycle_tools.py::set_trigger, whose method loop
// iterates the supplied value (a bare string is iterated character by
// character and a non-string entry raises) instead of coercing it.
func TestSetTriggerWebhookMethodsRequireStringList(t *testing.T) {
	const wantURL = "http://127.0.0.1:8090/hooks/x"

	cases := []struct {
		name      string
		methods   any
		present   bool
		wantRoute string
		wantError string
	}{
		{name: "missing defaults to POST", wantRoute: "hook /hooks/x POST"},
		{name: "list of strings", methods: []any{"get", "POST"}, present: true, wantRoute: "hook /hooks/x GET,POST"},
		{name: "go string slice", methods: []string{"PATCH"}, present: true, wantRoute: "hook /hooks/x PATCH"},
		{name: "bare string is not one method", methods: "GET", present: true, wantError: "Invalid HTTP methods: [E G T]"},
		{name: "empty string", methods: "", present: true, wantError: `Invalid HTTP methods: [""]`},
		{name: "number", methods: 5, present: true, wantError: "Invalid HTTP methods: [5]"},
		{name: "nil", methods: nil, present: true, wantError: "Invalid HTTP methods: [None]"},
		{name: "list with non-string", methods: []any{"POST", 5}, present: true, wantError: "Invalid HTTP methods: [5]"},
		{name: "list with invalid verb", methods: []any{"POST", "fetch"}, present: true, wantError: "Invalid HTTP methods: [FETCH]"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session, binding := newTriggerValidationBinding(t)
			fake := installFakeTriggerWebhookServer(t, nil)
			config := map[string]any{"path": "/hooks/x"}
			if testCase.present {
				config["methods"] = testCase.methods
			}
			content, isError := binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
				"trigger_id":     "hook",
				"trigger_type":   "webhook",
				"trigger_config": config,
				"task":           "handle it",
			})
			if testCase.wantError != "" {
				requireTriggerToolError(t, content, isError, testCase.wantError)
				if session.IsTriggerActive("hook") {
					t.Fatal("rejected webhook must not be active")
				}
				if len(fake.routes) != 0 {
					t.Fatalf("rejected webhook registered routes: %#v", fake.routes)
				}
				return
			}
			payload := requireTriggerActivated(t, content, isError)
			if payload["webhook_url"] != wantURL {
				t.Fatalf("webhook_url = %v, want %s", payload["webhook_url"], wantURL)
			}
			if len(fake.routes) != 1 || fake.routes[0] != testCase.wantRoute {
				t.Fatalf("webhook routes = %#v, want [%q]", fake.routes, testCase.wantRoute)
			}
			if !session.IsTriggerActive("hook") {
				t.Fatal("accepted webhook must be active")
			}
		})
	}
}

// Parity-exempt: upstream has no test for set_trigger input validation; this
// pins int(t_config.get("port", 8090)) semantics traced to
// framework/tools/queen_lifecycle_tools.py::set_trigger (numeric strings are
// accepted, non-numeric values are rejected here instead of silently binding
// 8090), and proves the parsed port is what StartTriggerWebhook receives.
func TestSetTriggerWebhookPortValidation(t *testing.T) {
	cases := []struct {
		name      string
		port      any
		present   bool
		wantPort  int
		wantError string
	}{
		{name: "absent defaults to 8090", wantPort: 8090},
		{name: "integer", port: 8092, present: true, wantPort: 8092},
		{name: "numeric string", port: "8093", present: true, wantPort: 8093},
		{name: "padded numeric string", port: " 8094 ", present: true, wantPort: 8094},
		{name: "underscored numeric string", port: "80_95", present: true, wantPort: 8095},
		{name: "float truncates toward zero", port: 8096.9, present: true, wantPort: 8096},
		{name: "nonnumeric string", port: "abc", present: true, wantError: "must be a number between 1 and 65535, got abc"},
		{name: "empty string", port: "", present: true, wantError: "must be a number between 1 and 65535, got "},
		{name: "nil", port: nil, present: true, wantError: "must be a number between 1 and 65535, got <nil>"},
		{name: "list", port: []any{8092}, present: true, wantError: "must be a number between 1 and 65535, got [8092]"},
		{name: "zero", port: 0, present: true, wantError: "must be a number between 1 and 65535, got 0"},
		{name: "negative", port: -1, present: true, wantError: "must be a number between 1 and 65535, got -1"},
		{name: "above the TCP range", port: 70000, present: true, wantError: "must be a number between 1 and 65535, got 70000"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session, binding := newTriggerValidationBinding(t)
			gotPort := 0
			factoryCalls := 0
			fake := installFakeTriggerWebhookServer(t, func(port int) {
				factoryCalls++
				gotPort = port
			})
			config := map[string]any{"path": "/hooks/x"}
			if testCase.present {
				config["port"] = testCase.port
			}
			content, isError := binding.Execute(context.Background(), QueenSetTriggerToolName, map[string]any{
				"trigger_id":     "hook",
				"trigger_type":   "webhook",
				"trigger_config": config,
				"task":           "handle it",
			})
			if testCase.wantError != "" {
				requireTriggerToolError(t, content, isError, testCase.wantError)
				if session.IsTriggerActive("hook") {
					t.Fatal("rejected webhook must not be active")
				}
				if factoryCalls != 0 || len(fake.routes) != 0 {
					t.Fatalf("rejected port started the webhook server: calls=%d routes=%#v", factoryCalls, fake.routes)
				}
				return
			}
			payload := requireTriggerActivated(t, content, isError)
			wantURL := "http://127.0.0.1:" + strconv.Itoa(testCase.wantPort) + "/hooks/x"
			if payload["webhook_url"] != wantURL {
				t.Fatalf("webhook_url = %v, want %s", payload["webhook_url"], wantURL)
			}
			if factoryCalls != 1 || gotPort != testCase.wantPort {
				t.Fatalf("StartTriggerWebhook port = %d (calls=%d), want %d", gotPort, factoryCalls, testCase.wantPort)
			}
			if !session.IsTriggerActive("hook") {
				t.Fatal("accepted webhook must be active")
			}
		})
	}
}
