package senders

// The send log is the local enforcement point for the daily cap, suppression
// and duplicate safety. Upstream sendlog.py delegates those decisions to the
// cloud, so these are parity-exempt but pin the documented failure policy:
// fail closed on a cap, release the slot on a provider failure.

import (
	"path/filepath"
	"testing"
)

func openTestLog(t *testing.T) *SendLog {
	t.Helper()
	return OpenSendLog(filepath.Join(t.TempDir(), "senders"), testClock())
}

func reserveFor(log *SendLog, senderID, to, subject string, limit *int) (Reservation, error) {
	return log.Reserve(ReserveRequest{
		SenderID:   senderID,
		SenderName: senderID,
		ToEmail:    to,
		Subject:    subject,
		DailyLimit: limit,
	})
}

// Parity-exempt: sendlog.py checks suppression in cloud; this pins the local
// refusal and its reason.
func TestReserveRefusesSuppressedRecipient(t *testing.T) {
	log := openTestLog(t)
	reservation, err := log.Reserve(ReserveRequest{
		SenderID:   "a",
		ToEmail:    "sarah@example.com",
		Suppressed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Allowed || !reservation.Suppressed || reservation.DeniedBecause != "suppressed" {
		t.Fatalf("reservation = %+v, want a suppressed refusal", reservation)
	}
	if reservation.SendID != "" {
		t.Fatalf("refused reservation got a send id: %q", reservation.SendID)
	}
}

// Parity-exempt: sendlog.py's duplicate key contract lives in cloud; this pins
// the local rule that a retry is refused but a failed attempt is retryable.
func TestReserveRefusesDuplicateUnlessPreviousFailed(t *testing.T) {
	log := openTestLog(t)
	request := ReserveRequest{
		SenderID:       "a",
		ToEmail:        "sarah@example.com",
		Subject:        "Hi",
		IdempotencyKey: "key-1",
	}
	first, err := log.Reserve(request)
	if err != nil || !first.Allowed {
		t.Fatalf("first reserve = %+v (err %v)", first, err)
	}
	duplicate, err := log.Reserve(request)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Allowed || !duplicate.Duplicate || duplicate.DeniedBecause != "duplicate" {
		t.Fatalf("second reserve = %+v, want a duplicate refusal", duplicate)
	}

	if err := log.Complete(first.SendID, StatusFailed, "", "provider said no"); err != nil {
		t.Fatal(err)
	}
	retry, err := log.Reserve(request)
	if err != nil || !retry.Allowed {
		t.Fatalf("retry after failure = %+v (err %v), want allowed", retry, err)
	}
}

// Parity-exempt: the cloud reservation is the upstream cap; this pins the local
// cap plus the "pending reservations count" rule and the failure release.
func TestReserveEnforcesDailyLimitAndReleasesFailedSlot(t *testing.T) {
	log := openTestLog(t)
	limit := 1

	first, err := reserveFor(log, "a", "one@example.com", "Hi", &limit)
	if err != nil || !first.Allowed {
		t.Fatalf("first = %+v (err %v)", first, err)
	}
	if first.Remaining == nil || *first.Remaining != 0 {
		t.Fatalf("remaining = %v, want 0", first.Remaining)
	}

	blocked, err := reserveFor(log, "a", "two@example.com", "Hi", &limit)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Allowed || blocked.DeniedBecause != "daily_limit" {
		t.Fatalf("second = %+v, want a daily-limit refusal", blocked)
	}

	// A provider rejection must not eat the domain's budget for the day.
	if err := log.Complete(first.SendID, StatusFailed, "", "rejected"); err != nil {
		t.Fatal(err)
	}
	retry, err := reserveFor(log, "a", "two@example.com", "Hi", &limit)
	if err != nil || !retry.Allowed {
		t.Fatalf("after failure = %+v (err %v), want allowed", retry, err)
	}
}

// Parity-exempt: no Python counterpart; pins "reserved is assumed sent" from
// the sender_history docstring.
func TestUsageCountsSentAndReservedOnly(t *testing.T) {
	log := openTestLog(t)
	reservation, err := reserveFor(log, "a", "one@example.com", "Hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveFor(log, "a", "two@example.com", "Hi", nil); err != nil {
		t.Fatal(err)
	}
	failed, err := reserveFor(log, "a", "three@example.com", "Hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Complete(reservation.SendID, StatusSent, "provider-id", ""); err != nil {
		t.Fatal(err)
	}
	if err := log.Complete(failed.SendID, StatusFailed, "", "no"); err != nil {
		t.Fatal(err)
	}

	usage, err := log.UsageToday()
	if err != nil {
		t.Fatal(err)
	}
	if usage["a"] != 2 {
		t.Fatalf("usage = %#v, want 2 (one sent + one reserved, failed excluded)", usage)
	}
	if log.Pending("a") != 1 {
		t.Fatalf("pending = %d, want 1 unconfirmed reservation", log.Pending("a"))
	}
}

// Parity-exempt: cloud list_sends ordering is not covered by a Python test;
// this pins newest-first plus the sender/recipient/colony filters.
func TestHistoryNewestFirstAndFilters(t *testing.T) {
	log := openTestLog(t)
	for _, to := range []string{"one@example.com", "two@example.com", "three@example.com"} {
		if _, err := log.Reserve(ReserveRequest{SenderID: "a", ToEmail: to, Subject: "Hi", ColonyID: "colony-1"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := log.Reserve(ReserveRequest{SenderID: "b", ToEmail: "other@example.com", Subject: "Hi", ColonyID: "colony-2"}); err != nil {
		t.Fatal(err)
	}

	rows, err := log.History(HistoryFilter{Limit: 10})
	if err != nil || len(rows) != 4 {
		t.Fatalf("history = %d rows (err %v), want 4", len(rows), err)
	}
	if rows[0].SenderID != "b" {
		t.Fatalf("newest row sender = %q, want b", rows[0].SenderID)
	}

	filtered, err := log.History(HistoryFilter{SenderID: "a", ToEmail: "TWO@example.com", Limit: 10})
	if err != nil || len(filtered) != 1 || filtered[0].ToEmail != "two@example.com" {
		t.Fatalf("filtered = %+v (err %v)", filtered, err)
	}
	byColony, err := log.History(HistoryFilter{ColonyID: "colony-2", Limit: 10})
	if err != nil || len(byColony) != 1 {
		t.Fatalf("by colony = %+v (err %v)", byColony, err)
	}
	limited, err := log.History(HistoryFilter{Limit: 2})
	if err != nil || len(limited) != 2 {
		t.Fatalf("limited = %d rows (err %v), want 2", len(limited), err)
	}
}

// Parity-exempt: sendlog.default_idempotency_key is untested upstream; this
// pins the "retry resolves to the same key, new message does not" contract.
func TestDefaultIdempotencyKeyIsStable(t *testing.T) {
	first := DefaultIdempotencyKey("colony", "campaign", "Sarah@Example.com", " Hi ", 1)
	second := DefaultIdempotencyKey("colony", "campaign", "sarah@example.com", "Hi", 1)
	if first != second {
		t.Fatalf("key drifted across case/space: %q vs %q", first, second)
	}
	if len(first) != 40 {
		t.Fatalf("key length = %d, want 40", len(first))
	}
	next := DefaultIdempotencyKey("colony", "campaign", "sarah@example.com", "Hi", 2)
	if next == first {
		t.Fatal("a new sequence step must get a new key")
	}
}
