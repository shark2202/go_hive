package soul

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aden-hive/hive-go/internal/llm"
)

// Parity-exempt: the upstream suite drives a stubbed model; this runs one
// reflection through the real HTTP transport to prove the tool schemas, the
// tool_call round trip and the applied proposal work end to end.
func TestReflectionThroughTheHTTPProvider(t *testing.T) {
	home := newReflectionTestHome(t)
	home.set(t, map[string]any{"mode": "auto"})
	home.profile(t, "Fox")
	home.session(t, "s_group", reflectionGroupEvents(), "Night owls", "x")

	var requests []map[string]any
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		decoded := map[string]any{}
		if err := json.NewDecoder(request.Body).Decode(&decoded); err == nil {
			requests = append(requests, decoded)
		}
		writer.Header().Set("Content-Type", "application/json")
		reply := `{"id":"chatcmpl-2","model":"gpt-4o-mini","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"nothing more"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
		if atomic.AddInt32(&calls, 1) == 1 {
			reply = `{"id":"chatcmpl-1","model":"gpt-4o-mini","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"add_item","arguments":"{\"section\":\"now\",\"text\":\"a cat next\",\"date\":\"2026-10-20\",\"evidence\":[\"the next one will be a cat\"]}"}}]}}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150}}`
		}
		if _, err := io.WriteString(writer, reply); err != nil {
			t.Errorf("write reply: %v", err)
		}
	}))
	defer server.Close()

	provider := llm.NewHTTPProvider(llm.ProviderOptions{Model: "gpt-4o-mini", APIKey: "test-key", APIBase: server.URL + "/v1"})
	proposal, err := Reflect(ReflectionOptions{
		HomeRoot:      home.root,
		QueensDir:     home.queensDir,
		QueenID:       reflectionTestQueen,
		Config:        home.soulConfig(t),
		Completer:     provider,
		ProfileSource: reflectionTestProfileSource{},
		Now:           reflectionTestNow(),
		Log:           func(string, ...any) {},
	})
	if err != nil {
		t.Fatalf("reflect: %v", err)
	}
	if proposal == nil || len(proposal.Edits) != 1 || proposal.Edits[0].Text != "a cat next" {
		t.Fatalf("proposal = %#v", proposal)
	}
	items := Load(home.queensDir, reflectionTestQueen).Items("now")
	if len(items) != 1 || items[0].Text != "a cat next" {
		t.Fatalf("soul = %#v", items)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("model calls = %d, want the tool turn and the finish turn", calls)
	}
	if len(requests) != 2 {
		t.Fatalf("captured %d requests", len(requests))
	}
	body, _ := json.Marshal(requests[0])
	if !strings.Contains(string(body), `"add_item"`) || !strings.Contains(string(body), `"finish"`) {
		t.Fatalf("the reflection tools were not sent: %s", body)
	}
	if !strings.Contains(string(body), "You are Fox.") {
		t.Fatalf("the system prompt was not sent: %s", body)
	}
	if !strings.Contains(string(body), "your lantern sketch is lovely") {
		t.Fatalf("the conversation was not sent: %s", body)
	}
}
