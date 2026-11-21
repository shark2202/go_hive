package queenmail

import "testing"

// Parity: framework/server/tests/test_queen_mail.py::test_a_reply_past_max_depth_isnt_delivered
func TestADroppedReplyIsAudited(t *testing.T) {
	home := newMailHome(t)
	config := testConfig()
	config.MaxDepth = 1
	deliverer := &fakeDeliverer{}
	mail := New(home, config, deliverer)

	if _, err := mail.Send(ava, "session_ava", ivy, "Can you map the north shore?", nil); err != nil {
		t.Fatal(err)
	}
	// The reply the first hop set off would be hop 2, past max_depth=1.
	if err := mail.DeliverReply("session_ava", ivy, ava, "session_ivy", "Done.", &Cause{Hop: "hop_first", Depth: 1}); err != nil {
		t.Fatal(err)
	}
	records := mail.Store().ReadAudit()
	last := records[len(records)-1]
	if last.Kind != KindReply || last.From != ivy || last.To != ava || last.Depth != 2 || last.Dropped != "max_depth" {
		t.Fatalf("audit = %#v", last)
	}
	deliverer.mu.Lock()
	defer deliverer.mu.Unlock()
	if len(deliverer.replies) != 0 {
		t.Fatalf("a reply past max_depth must not be delivered: %#v", deliverer.replies)
	}
}
