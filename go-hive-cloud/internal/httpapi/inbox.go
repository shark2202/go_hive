package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/store"
)

// replyPayload is the `event: reply` data frame. correlation_token and text are
// the two fields listeners.go requires; colony_id is read when present
// (listeners.go:549) and message_id is a private-deploy convenience.
type replyPayload struct {
	CorrelationToken string `json:"correlation_token"`
	Text             string `json:"text"`
	ColonyID         string `json:"colony_id,omitempty"`
	MessageID        string `json:"message_id,omitempty"`
}

// replyHub fans replies out to open SSE subscribers keyed by runtime_id.
type replyHub struct {
	mu   sync.Mutex
	subs map[string]map[chan replyPayload]struct{}
}

func newReplyHub() *replyHub {
	return &replyHub{subs: map[string]map[chan replyPayload]struct{}{}}
}

func (h *replyHub) subscribe(runtimeID string) chan replyPayload {
	ch := make(chan replyPayload, 16)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[runtimeID] == nil {
		h.subs[runtimeID] = map[chan replyPayload]struct{}{}
	}
	h.subs[runtimeID][ch] = struct{}{}
	return ch
}

func (h *replyHub) unsubscribe(runtimeID string, ch chan replyPayload) {
	h.mu.Lock()
	defer h.mu.Unlock()
	group, ok := h.subs[runtimeID]
	if !ok {
		return
	}
	delete(group, ch)
	if len(group) == 0 {
		delete(h.subs, runtimeID)
	}
	close(ch)
}

// broadcast delivers to every live subscriber of runtimeID and reports how many
// accepted the event. Slow consumers are skipped (their reply stays pending in
// the DB and is flushed on the next connect).
func (h *replyHub) broadcast(runtimeID string, payload replyPayload) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	delivered := 0
	for ch := range h.subs[runtimeID] {
		select {
		case ch <- payload:
			delivered++
		default:
		}
	}
	return delivered
}

// POST /v1/inbox/messages.
func (s *Server) handleInboxMessage(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	if !s.allowInboxWrite(w, reqAuth) {
		return
	}
	var body struct {
		RuntimeID        string  `json:"runtime_id"`
		SessionID        string  `json:"session_id"`
		ColonyID         string  `json:"colony_id"`
		Kind             string  `json:"kind"`
		Title            string  `json:"title"`
		Body             string  `json:"body"`
		CorrelationToken string  `json:"correlation_token"`
		DeepLink         *string `json:"deep_link"`
	}
	if err := decodeJSON(w, r, maxInboxBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	kind := body.Kind
	if kind == "" {
		kind = "blocker" // notifier.go defaults to blocker
	}
	if !store.ValidKind(kind) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid kind (want blocker|heartbeat|progress|done)"})
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id is required"})
		return
	}
	id, err := team.InsertMessage(r.Context(), store.InboxMessage{
		Kind:             kind,
		Title:            body.Title,
		Body:             body.Body,
		CorrelationToken: body.CorrelationToken,
		DeepLink:         body.DeepLink,
		RuntimeID:        body.RuntimeID,
		SessionID:        body.SessionID,
		ColonyID:         body.ColonyID,
		UserID:           reqAuth.UserID,
		TeamID:           reqAuth.TeamID,
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "inbox_message", "inbox_messages",
		fmt.Sprintf("id=%s kind=%s session=%s", id, kind, body.SessionID)); err != nil {
		s.logger.Printf("[cloud] inbox audit failed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// POST /v1/inbox/resolve.
func (s *Server) handleInboxResolve(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	if !s.allowInboxWrite(w, reqAuth) {
		return
	}
	var body struct {
		SessionID  string `json:"session_id"`
		ResolvedBy string `json:"resolved_by"`
	}
	if err := decodeJSON(w, r, maxInboxBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id is required"})
		return
	}
	resolved, err := team.ResolveSession(r.Context(), body.SessionID, body.ResolvedBy)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "inbox_resolve", "inbox_messages",
		fmt.Sprintf("session=%s resolved=%d", body.SessionID, resolved)); err != nil {
		s.logger.Printf("[cloud] inbox audit failed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": resolved})
}

// GET /v1/inbox/runtime/stream — SSE, event: reply,
// data {"correlation_token","text"}. No total timeout (the reference client
// sets none either).
func (s *Server) handleInboxRuntimeStream(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	runtimeID := r.URL.Query().Get("runtime_id")
	if strings.TrimSpace(runtimeID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "runtime_id is required"})
		return
	}
	kind := r.URL.Query().Get("kind")
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.internalError(w, r, fmt.Errorf("streaming unsupported"))
		return
	}
	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream; charset=utf-8")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")

	// Subscribe before the first byte so a client that has read the connected
	// comment is guaranteed to receive later replies.
	subscription := s.hub.subscribe(runtimeID)
	defer s.hub.unsubscribe(runtimeID, subscription)

	if err := team.RegisterRuntimeConnection(r.Context(), runtimeID, kind); err != nil {
		s.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	// Flush replies queued while the runtime was disconnected.
	pending, err := team.PendingReplies(r.Context(), runtimeID, 100)
	if err != nil {
		s.logger.Printf("[cloud] pending replies: %v", err)
		return
	}
	for _, reply := range pending {
		payload := replyPayload{
			CorrelationToken: reply.CorrelationToken,
			Text:             reply.Text,
			ColonyID:         reply.ColonyID,
			MessageID:        reply.MessageID,
		}
		if err := writeSSEEvent(w, "reply", payload); err != nil {
			return
		}
		flusher.Flush()
		// The client may close immediately after reading the frame. Mark the
		// delivery with a context that survives that disconnect; otherwise a
		// fast reconnect could replay the same queued reply.
		if err := team.MarkReplyDelivered(context.WithoutCancel(r.Context()), reply.ID); err != nil {
			s.logger.Printf("[cloud] mark delivered: %v", err)
		}
	}

	ticker := time.NewTicker(s.cfg.KeepAlive)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-subscription:
			if !open {
				return
			}
			if err := writeSSEEvent(w, "reply", payload); err != nil {
				return
			}
			flusher.Flush()
			if payload.MessageID != "" {
				if err := team.TouchRuntimeConnection(r.Context(), runtimeID, kind); err != nil {
					s.logger.Printf("[cloud] touch connection: %v", err)
				}
			}
		case <-ticker.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			_ = team.TouchRuntimeConnection(r.Context(), runtimeID, kind)
		}
	}
}

func writeSSEEvent(w io.Writer, event string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
		return err
	}
	return nil
}

// POST /internal/inbox/reply
//
// Private-deploy extension (the real Inbox UI is closed-source and its reply
// endpoint is UNKNOWN). Authenticated with an operator JWT so the team is
// derived server-side; body {correlation_token, text, team_id?}.
func (s *Server) handleInternalInboxReply(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	if !s.allowInboxWrite(w, reqAuth) {
		return
	}
	var body struct {
		CorrelationToken string `json:"correlation_token"`
		Text             string `json:"text"`
		TeamID           string `json:"team_id"`
	}
	if err := decodeJSON(w, r, maxInboxBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if body.TeamID != "" && body.TeamID != reqAuth.TeamID {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "team mismatch"})
		return
	}
	if strings.TrimSpace(body.CorrelationToken) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "correlation_token is required"})
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "text is required"})
		return
	}
	message, err := team.FindMessageByCorrelationToken(r.Context(), body.CorrelationToken)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	reply, err := team.InsertReply(r.Context(), message, body.Text)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	delivered := s.hub.broadcast(message.RuntimeID, replyPayload{
		CorrelationToken: message.CorrelationToken,
		Text:             body.Text,
		ColonyID:         message.ColonyID,
		MessageID:        message.ID,
	})
	if delivered > 0 {
		// Live delivery is the delivery record for this reply; a reconnect
		// must not replay it. Use a context that survives a client disconnect:
		// the subscriber may close as soon as it reads the frame.
		if err := team.MarkReplyDelivered(context.WithoutCancel(r.Context()), reply.ID); err != nil {
			s.logger.Printf("[cloud] mark delivered: %v", err)
		}
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "inbox_reply", "inbox_replies",
		fmt.Sprintf("message=%s delivered=%d", message.ID, delivered)); err != nil {
		s.logger.Printf("[cloud] inbox audit failed: %v", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"message_id": message.ID,
		"reply_id":   reply.ID,
		"delivered":  delivered,
	})
}
