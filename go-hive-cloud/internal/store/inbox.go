package store

import (
	"context"
	"fmt"
)

// InboxMessage is one inbox_messages row (field set from
// docs/reverse-engineering.md §5).
type InboxMessage struct {
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	Body             string  `json:"body"`
	CorrelationToken string  `json:"correlation_token"`
	DeepLink         *string `json:"deep_link"`
	Status           string  `json:"status"`
	ResolvedBy       string  `json:"resolved_by"`
	ReplyText        string  `json:"reply_text"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	ResolvedAt       *string `json:"resolved_at"`
	RuntimeID        string  `json:"runtime_id"`
	SessionID        string  `json:"session_id"`
	ColonyID         string  `json:"colony_id"`
	UserID           string  `json:"user_id"`
	TeamID           string  `json:"team_id"`
}

// ValidKind reports whether kind is one of the client's escalation kinds.
func ValidKind(kind string) bool {
	switch kind {
	case "blocker", "heartbeat", "progress", "done":
		return true
	}
	return false
}

// InsertMessage stores an inbox message and returns its id.
func (t *Team) InsertMessage(ctx context.Context, message InboxMessage) (string, error) {
	if message.ID == "" {
		message.ID = NewID()
	}
	now := t.Now()
	_, err := t.rw.ExecContext(ctx, `
INSERT INTO inbox_messages
  (id, kind, title, body, correlation_token, deep_link, status, resolved_by, reply_text,
   created_at, updated_at, resolved_at, runtime_id, session_id, colony_id, user_id, team_id)
VALUES (?, ?, ?, ?, ?, ?, 'open', '', '', ?, ?, NULL, ?, ?, ?, ?, ?)`,
		message.ID, message.Kind, message.Title, message.Body, message.CorrelationToken,
		message.DeepLink, now, now, message.RuntimeID, message.SessionID, message.ColonyID,
		message.UserID, message.TeamID)
	if err != nil {
		return "", err
	}
	return message.ID, nil
}

// ResolveSession closes every open message of sessionID. Returns the number of
// rows touched.
func (t *Team) ResolveSession(ctx context.Context, sessionID, resolvedBy string) (int64, error) {
	if sessionID == "" {
		return 0, fmt.Errorf("%w: session_id is required", ErrBadData)
	}
	now := t.Now()
	res, err := t.rw.ExecContext(ctx, `
UPDATE inbox_messages SET status = 'resolved', resolved_by = ?, resolved_at = ?, updated_at = ?
WHERE session_id = ? AND status != 'resolved'`, resolvedBy, now, now, sessionID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanMessage(row map[string]any) *InboxMessage {
	return &InboxMessage{
		ID:               asString(row["id"]),
		Kind:             asString(row["kind"]),
		Title:            asString(row["title"]),
		Body:             asString(row["body"]),
		CorrelationToken: asString(row["correlation_token"]),
		DeepLink:         optionalString(row["deep_link"]),
		Status:           asString(row["status"]),
		ResolvedBy:       asString(row["resolved_by"]),
		ReplyText:        asString(row["reply_text"]),
		CreatedAt:        asString(row["created_at"]),
		UpdatedAt:        asString(row["updated_at"]),
		ResolvedAt:       optionalString(row["resolved_at"]),
		RuntimeID:        asString(row["runtime_id"]),
		SessionID:        asString(row["session_id"]),
		ColonyID:         asString(row["colony_id"]),
		UserID:           asString(row["user_id"]),
		TeamID:           asString(row["team_id"]),
	}
}

func optionalString(v any) *string {
	if v == nil {
		return nil
	}
	text := asString(v)
	if text == "" {
		return nil
	}
	return &text
}

// FindMessageByCorrelationToken returns the newest message carrying token.
func (t *Team) FindMessageByCorrelationToken(ctx context.Context, token string) (*InboxMessage, error) {
	if token == "" {
		return nil, fmt.Errorf("%w: correlation_token is required", ErrBadData)
	}
	rows, err := t.QueryRaw(ctx,
		`SELECT * FROM inbox_messages WHERE correlation_token = ? ORDER BY created_at DESC LIMIT 1`, token)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: no message with correlation_token %s", ErrNotFound, token)
	}
	return scanMessage(rows[0]), nil
}

// Reply is one inbox_replies row (a queued/emitted reply for an SSE stream).
type Reply struct {
	ID               int64  `json:"id"`
	MessageID        string `json:"message_id"`
	CorrelationToken string `json:"correlation_token"`
	RuntimeID        string `json:"runtime_id"`
	SessionID        string `json:"session_id"`
	Text             string `json:"text"`
	CreatedAt        string `json:"created_at"`
}

// InsertReply records a reply for a message and returns it.
func (t *Team) InsertReply(ctx context.Context, message *InboxMessage, text string) (*Reply, error) {
	if text == "" {
		return nil, fmt.Errorf("%w: text is required", ErrBadData)
	}
	now := t.Now()
	res, err := t.rw.ExecContext(ctx, `
INSERT INTO inbox_replies (message_id, correlation_token, runtime_id, session_id, text, created_at, delivered_at)
VALUES (?, ?, ?, ?, ?, ?, NULL)`,
		message.ID, message.CorrelationToken, message.RuntimeID, message.SessionID, text, now)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := t.rw.ExecContext(ctx,
		`UPDATE inbox_messages SET reply_text = ?, updated_at = ? WHERE id = ?`, text, now, message.ID); err != nil {
		return nil, err
	}
	return &Reply{
		ID:               id,
		MessageID:        message.ID,
		CorrelationToken: message.CorrelationToken,
		RuntimeID:        message.RuntimeID,
		SessionID:        message.SessionID,
		Text:             text,
		CreatedAt:        now,
	}, nil
}

// PendingReplies lists replies that have not been written to a stream yet,
// oldest first.
func (t *Team) PendingReplies(ctx context.Context, runtimeID string, limit int) ([]Reply, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := t.QueryRaw(ctx, `
SELECT id, message_id, correlation_token, runtime_id, session_id, text, created_at
FROM inbox_replies WHERE runtime_id = ? AND delivered_at IS NULL
ORDER BY id LIMIT ?`, runtimeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Reply, 0, len(rows))
	for _, row := range rows {
		out = append(out, Reply{
			ID:               asInt(row["id"]),
			MessageID:        asString(row["message_id"]),
			CorrelationToken: asString(row["correlation_token"]),
			RuntimeID:        asString(row["runtime_id"]),
			SessionID:        asString(row["session_id"]),
			Text:             asString(row["text"]),
			CreatedAt:        asString(row["created_at"]),
		})
	}
	return out, nil
}

// MarkReplyDelivered stamps a reply as written to a stream.
func (t *Team) MarkReplyDelivered(ctx context.Context, id int64) error {
	_, err := t.rw.ExecContext(ctx, `UPDATE inbox_replies SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL`, t.Now(), id)
	return err
}

// RegisterRuntimeConnection records a stream connection (runtime_connections).
func (t *Team) RegisterRuntimeConnection(ctx context.Context, runtimeID, kind string) error {
	now := t.Now()
	_, err := t.rw.ExecContext(ctx, `
INSERT INTO runtime_connections (runtime_id, kind, connected_at, last_seen_at) VALUES (?, ?, ?, ?)
ON CONFLICT (runtime_id, kind) DO UPDATE SET connected_at = excluded.connected_at, last_seen_at = excluded.last_seen_at`,
		runtimeID, kind, now, now)
	return err
}

// TouchRuntimeConnection refreshes last_seen_at (keepalive ticks).
func (t *Team) TouchRuntimeConnection(ctx context.Context, runtimeID, kind string) error {
	_, err := t.rw.ExecContext(ctx,
		`UPDATE runtime_connections SET last_seen_at = ? WHERE runtime_id = ? AND kind = ?`, t.Now(), runtimeID, kind)
	return err
}
