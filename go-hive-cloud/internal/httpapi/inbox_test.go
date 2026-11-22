package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestInboxMessagesAndResolve(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt1",
		"session_id":        "s1",
		"colony_id":         "c1",
		"kind":              "blocker",
		"title":             "Need approval",
		"body":              "Should I send the email?",
		"correlation_token": "ct-1",
		"deep_link":         nil,
	}, "Bearer", e.jwt)
	body := e.decodeJSON(resp, raw, http.StatusOK)
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("message id missing: %v", body)
	}

	// Invalid kind is a client error with the documented shape.
	resp, raw = e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"session_id": "s1",
		"kind":       "nope",
	}, "Bearer", e.jwt)
	badKind := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if badKind["error"] != "invalid kind (want blocker|heartbeat|progress|done)" {
		t.Fatalf("bad kind body = %v", badKind)
	}

	// Missing session id.
	resp, raw = e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"kind": "blocker",
	}, "Bearer", e.jwt)
	missing := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if missing["error"] != "session_id is required" {
		t.Fatalf("missing session body = %v", missing)
	}

	// Resolve closes the open message for the session.
	resp, raw = e.do(http.MethodPost, "/v1/inbox/resolve", map[string]any{
		"session_id":  "s1",
		"resolved_by": "u_test",
	}, "Bearer", e.jwt)
	resolve := e.decodeJSON(resp, raw, http.StatusOK)
	if resolve["resolved"].(float64) != 1 {
		t.Fatalf("resolve body = %v", resolve)
	}
	resp, raw = e.do(http.MethodPost, "/v1/inbox/resolve", map[string]any{
		"session_id":  "s1",
		"resolved_by": "u_test",
	}, "Bearer", e.jwt)
	resolve = e.decodeJSON(resp, raw, http.StatusOK)
	if resolve["resolved"].(float64) != 0 {
		t.Fatalf("second resolve body = %v", resolve)
	}
}

// TestInboxSSELiveReply covers the M1 reply path: an SSE subscriber on
// runtime_id receives `event: reply` with {correlation_token,text} when the
// private-deploy reply endpoint fires.
func TestInboxSSELiveReply(t *testing.T) {
	e := newEnv(t)
	stream := e.openStream(t, "rt-live", "smoke")
	defer stream.Body.Close()
	reader := newSSEReader(stream.Body)

	resp, raw := e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-live",
		"session_id":        "s-live",
		"colony_id":         "colony-1",
		"kind":              "blocker",
		"title":             "t",
		"body":              "b",
		"correlation_token": "corr-live",
	}, "Bearer", e.jwt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}

	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-live",
		"text":              "ship it",
	})
	reply := e.decodeJSON(resp, raw, http.StatusOK)
	if reply["ok"] != true || reply["delivered"].(float64) != 1 {
		t.Fatalf("internal reply body = %v", reply)
	}

	event, data := reader.nextFrame(t)
	if event != "reply" {
		t.Fatalf("event = %q want reply", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode data %q: %v", data, err)
	}
	if payload["correlation_token"] != "corr-live" || payload["text"] != "ship it" {
		t.Fatalf("reply payload = %v", payload)
	}
	if payload["colony_id"] != "colony-1" {
		t.Fatalf("colony_id = %v", payload["colony_id"])
	}
	masked, err := marshalCompactNoEscape(t, maskDynamic(payload))
	if err != nil {
		t.Fatalf("marshal masked payload: %v", err)
	}
	goldenText(t, "sse-reply-frame.txt", "event: "+event+"\ndata: "+string(masked)+"\n\n")
}

// TestInboxSSEQueuedReply proves replies are durable across a disconnected
// runtime: the reply is stored, then flushed on connect.
func TestInboxSSEQueuedReply(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.do(http.MethodPost, "/v1/inbox/messages", map[string]any{
		"runtime_id":        "rt-queued",
		"session_id":        "s-queued",
		"colony_id":         "c2",
		"kind":              "progress",
		"correlation_token": "corr-queued",
	}, "Bearer", e.jwt)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post message: %d %s", resp.StatusCode, raw)
	}
	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "corr-queued",
		"text":              "queued answer",
	})
	queued := e.decodeJSON(resp, raw, http.StatusOK)
	if queued["delivered"].(float64) != 0 {
		t.Fatalf("reply should have no live subscriber: %v", queued)
	}

	stream := e.openStream(t, "rt-queued", "smoke")
	defer stream.Body.Close()
	event, data := newSSEReader(stream.Body).nextFrame(t)
	if event != "reply" {
		t.Fatalf("event = %q", event)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload["correlation_token"] != "corr-queued" || payload["text"] != "queued answer" {
		t.Fatalf("payload = %v", payload)
	}
}

func TestInboxStreamValidation(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.do(http.MethodGet, "/v1/inbox/runtime/stream", nil, "Bearer", e.jwt)
	body := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if body["error"] != "runtime_id is required" {
		t.Fatalf("body = %v", body)
	}
}

func TestInternalInboxReplyErrors(t *testing.T) {
	e := newEnv(t)
	resp, raw := e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "missing",
		"text":              "x",
	})
	notFound := e.decodeJSON(resp, raw, http.StatusNotFound)
	if notFound["error"] != "no message with correlation_token missing" {
		t.Fatalf("not found body = %v", notFound)
	}

	otherJWT, err := e.signer.MintUserToken("u_other", "team_other", 2_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	resp, raw = e.do(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "x",
		"text":              "x",
		"team_id":           testTeam,
	}, "jwt", otherJWT)
	mismatch := e.decodeJSON(resp, raw, http.StatusForbidden)
	if mismatch["error"] != "team mismatch" {
		t.Fatalf("team mismatch body = %v", mismatch)
	}

	resp, raw = e.authed(http.MethodPost, "/internal/inbox/reply", map[string]any{
		"correlation_token": "x",
	})
	missingText := e.decodeJSON(resp, raw, http.StatusBadRequest)
	if missingText["error"] != "text is required" {
		t.Fatalf("missing text body = %v", missingText)
	}
}
