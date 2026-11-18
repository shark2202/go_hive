package senders

// The send log — the audit trail and the local enforcement point for
// daily_limit, duplicate suppression and the suppression list.
//
// Upstream reserves a slot in the cloud inside one transaction under a
// per-sender lock (sendlog.py). Local mode cannot see a teammate's laptop, but
// it must still fail closed within this process: a reservation is written
// BEFORE the provider call, a failed send releases the slot, and an
// unconfirmed reservation keeps consuming budget (assumed sent). The
// asymmetry is deliberate: over-sending from a cold domain cannot be undone,
// while failing to send can be retried.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

const sendLogFile = "sends.json"

// Send statuses.
const (
	StatusReserved = "reserved"
	StatusSent     = "sent"
	StatusFailed   = "failed"
)

// SendRecord is one append-only row in the send log.
type SendRecord struct {
	ID                string `json:"id"`
	SenderID          string `json:"sender_id"`
	SenderName        string `json:"sender_name,omitempty"`
	ToEmail           string `json:"to_email"`
	Subject           string `json:"subject,omitempty"`
	Status            string `json:"status"`
	ProviderMessageID string `json:"provider_message_id,omitempty"`
	Error             string `json:"error,omitempty"`
	ColonyID          string `json:"colony_id,omitempty"`
	CampaignID        string `json:"campaign_id,omitempty"`
	Step              int    `json:"step,omitempty"`
	ConversationToken string `json:"conversation_token,omitempty"`
	MessageID         string `json:"message_id,omitempty"`
	IdempotencyKey    string `json:"idempotency_key,omitempty"`
	ReservedAt        string `json:"reserved_at"`
	CompletedAt       string `json:"completed_at,omitempty"`
}

type sendLogFileState struct {
	Version int          `json:"version"`
	Sends   []SendRecord `json:"sends"`
}

// ReserveRequest describes the slot to claim.
type ReserveRequest struct {
	SenderID          string
	SenderName        string
	ToEmail           string
	Subject           string
	ColonyID          string
	CampaignID        string
	Step              int
	ConversationToken string
	MessageID         string
	IdempotencyKey    string
	// DailyLimit is the sender's cap; nil means unlimited.
	DailyLimit *int
	// Suppressed is the result of the do-not-contact lookup. Passing it in
	// keeps the refusal in one response shape (guidance included) instead of
	// scattering the check across callers.
	Suppressed bool
}

// Reservation is the send-time verdict. Allowed=false means do not send.
type Reservation struct {
	Allowed       bool
	SendID        string
	SentToday     int
	DailyLimit    *int
	Remaining     *int
	Reason        string
	DeniedBecause string // "suppressed" | "duplicate" | "daily_limit"
	Duplicate     bool
	Suppressed    bool
}

// SendLog owns the append-only log plus in-process reservation counters.
type SendLog struct {
	dir string
	now func() time.Time

	mu      sync.Mutex
	pending map[string]int
}

// OpenSendLog binds the log to the senders directory.
func OpenSendLog(dir string, now func() time.Time) *SendLog {
	if now == nil {
		now = time.Now
	}
	return &SendLog{dir: dir, now: now, pending: map[string]int{}}
}

func (l *SendLog) path() string { return filepath.Join(l.dir, sendLogFile) }

func (l *SendLog) load() ([]SendRecord, error) {
	var state sendLogFileState
	if err := atomicfs.ReadJSON(l.path(), &state); err != nil {
		if os.IsNotExist(err) {
			return []SendRecord{}, nil
		}
		return nil, fmt.Errorf("read send log: %w", err)
	}
	if state.Sends == nil {
		state.Sends = []SendRecord{}
	}
	return state.Sends, nil
}

func (l *SendLog) persist(rows []SendRecord) error {
	if err := os.MkdirAll(l.dir, 0o700); err != nil {
		return fmt.Errorf("create senders dir: %w", err)
	}
	data, err := json.MarshalIndent(sendLogFileState{Version: 1, Sends: rows}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode send log: %w", err)
	}
	data = append(data, '\n')
	return atomicfs.WriteFile(l.path(), data, 0o600)
}

func (l *SendLog) today() string { return l.now().UTC().Format("2006-01-02") }

func recordDay(record SendRecord) string {
	if len(record.ReservedAt) < 10 {
		return ""
	}
	return record.ReservedAt[:10]
}

// usageFrom counts today's per-sender sends. Reserved rows count too: an
// unconfirmed send is assumed to have gone out, which is the safe direction.
func usageFrom(rows []SendRecord, today string) map[string]int {
	usage := map[string]int{}
	for _, row := range rows {
		if row.Status != StatusSent && row.Status != StatusReserved {
			continue
		}
		if recordDay(row) != today {
			continue
		}
		usage[row.SenderID]++
	}
	return usage
}

// UsageToday returns today's per-sender send counts from the log.
func (l *SendLog) UsageToday() (map[string]int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.load()
	if err != nil {
		return nil, err
	}
	return usageFrom(rows, l.today()), nil
}

// DefaultIdempotencyKey derives a stable key for "this logical message to this
// person". A retry after a network blip must not deliver a second copy, so the
// key is derived from the message's identity, never random.
func DefaultIdempotencyKey(colonyID, campaignID, toEmail, subject string, step int) string {
	if step == 0 {
		step = 1
	}
	raw := strings.Join([]string{
		colonyID,
		campaignID,
		strings.ToLower(strings.TrimSpace(toEmail)),
		strings.TrimSpace(subject),
		fmt.Sprint(step),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:40]
}

func newSendID() string {
	buf := make([]byte, 10)
	if _, err := rand.Read(buf); err != nil {
		return "send_" + strings.Repeat("0", 20)
	}
	return "send_" + hex.EncodeToString(buf)
}

// Reserve claims a slot. Order matters: suppression first (safety), then
// duplicate (a retry must not double-send), then the daily cap.
func (l *SendLog) Reserve(req ReserveRequest) (Reservation, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rows, err := l.load()
	if err != nil {
		return Reservation{}, err
	}
	usage := usageFrom(rows, l.today())
	sentToday := usage[req.SenderID]

	if req.Suppressed {
		return Reservation{
			Allowed:       false,
			DeniedBecause: "suppressed",
			Suppressed:    true,
			SentToday:     sentToday,
			DailyLimit:    req.DailyLimit,
			Reason:        fmt.Sprintf("%s is on the team's do-not-contact list.", req.ToEmail),
		}, nil
	}

	key := req.IdempotencyKey
	if key == "" {
		key = DefaultIdempotencyKey(req.ColonyID, req.CampaignID, req.ToEmail, req.Subject, req.Step)
	}
	for _, row := range rows {
		if row.IdempotencyKey != key {
			continue
		}
		if row.Status == StatusSent || row.Status == StatusReserved {
			return Reservation{
				Allowed:       false,
				DeniedBecause: "duplicate",
				Duplicate:     true,
				SentToday:     sentToday,
				DailyLimit:    req.DailyLimit,
				Reason:        fmt.Sprintf("This message was already sent to %s.", req.ToEmail),
			}, nil
		}
	}

	if req.DailyLimit != nil {
		remaining := *req.DailyLimit - sentToday - l.pending[req.SenderID]
		if remaining <= 0 {
			limit := *req.DailyLimit
			zero := 0
			return Reservation{
				Allowed:       false,
				DeniedBecause: "daily_limit",
				SentToday:     sentToday,
				DailyLimit:    &limit,
				Remaining:     &zero,
				Reason:        fmt.Sprintf("Sender %q has reached its daily limit of %d.", req.SenderID, limit),
			}, nil
		}
	}

	record := SendRecord{
		ID:                newSendID(),
		SenderID:          req.SenderID,
		SenderName:        req.SenderName,
		ToEmail:           req.ToEmail,
		Subject:           req.Subject,
		Status:            StatusReserved,
		ColonyID:          req.ColonyID,
		CampaignID:        req.CampaignID,
		Step:              req.Step,
		ConversationToken: req.ConversationToken,
		MessageID:         req.MessageID,
		IdempotencyKey:    key,
		ReservedAt:        l.now().UTC().Format(time.RFC3339),
	}
	rows = append(rows, record)
	if err := l.persist(rows); err != nil {
		return Reservation{}, err
	}
	l.pending[req.SenderID]++

	reservation := Reservation{
		Allowed:    true,
		SendID:     record.ID,
		SentToday:  sentToday,
		DailyLimit: req.DailyLimit,
	}
	if req.DailyLimit != nil {
		remaining := *req.DailyLimit - sentToday - l.pending[req.SenderID]
		if remaining < 0 {
			remaining = 0
		}
		reservation.Remaining = &remaining
	}
	return reservation, nil
}

// Complete records the provider's verdict. A failed send releases the slot so
// a provider rejection does not eat the domain's budget for the day.
func (l *SendLog) Complete(sendID, status, providerMessageID, errText string) error {
	if sendID == "" {
		return nil
	}
	if status != StatusSent && status != StatusFailed {
		return fmt.Errorf("invalid send status %q", status)
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	rows, err := l.load()
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].ID != sendID {
			continue
		}
		rows[i].Status = status
		rows[i].ProviderMessageID = providerMessageID
		rows[i].Error = errText
		rows[i].CompletedAt = l.now().UTC().Format(time.RFC3339)
		if err := l.persist(rows); err != nil {
			return err
		}
		if l.pending[rows[i].SenderID] > 0 {
			l.pending[rows[i].SenderID]--
		}
		return nil
	}
	return errors.New("send log: reservation not found")
}

// HistoryFilter selects rows for sender_history.
type HistoryFilter struct {
	SenderID string
	ToEmail  string
	ColonyID string
	Limit    int
}

// History returns matching rows newest-first, capped at Limit.
func (l *SendLog) History(filter HistoryFilter) ([]SendRecord, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.load()
	if err != nil {
		return nil, err
	}
	matched := make([]SendRecord, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if filter.SenderID != "" && row.SenderID != filter.SenderID {
			continue
		}
		if filter.ToEmail != "" && NormalizeAddress(row.ToEmail) != NormalizeAddress(filter.ToEmail) {
			continue
		}
		if filter.ColonyID != "" && row.ColonyID != filter.ColonyID {
			continue
		}
		matched = append(matched, row)
	}
	sort.SliceStable(matched, func(a, b int) bool {
		return matched[a].ReservedAt > matched[b].ReservedAt
	})
	if filter.Limit > 0 && len(matched) > filter.Limit {
		matched = matched[:filter.Limit]
	}
	return matched, nil
}

// Pending reports the number of in-flight reservations for a sender (tests and
// diagnostics).
func (l *SendLog) Pending(senderID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.pending[senderID]
}
