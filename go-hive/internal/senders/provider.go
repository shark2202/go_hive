package senders

// Provider adapter seam for senders whose transport is not a local SMTP
// mailbox. Upstream providers.py dispatches one send to Gmail/SendGrid/
// Mailjet/HubSpot over the Aden cloud connector, resolving OAuth tokens from
// the cloud credential store. That connector needs team credentials and
// network access, so this build does not ship it: by default a non-SMTP
// sender keeps returning the explicit "requires the Aden cloud connector"
// refusal instead of pretending to send.
//
// The interface exists so the delivery gap is testable and so a real cloud
// connector (or a deterministic fake) can be injected without touching the
// engine: quota reservation, the send log, suppression and conversation
// tracking all stay local and keep working around it.

import "context"

// ProviderRequest is one fully-resolved message handed to an adapter. It is
// the Go equivalent of the arguments providers.send_one receives: the sender
// row (with its secret/integration id), the two body alternatives, and the
// threading headers for a reply.
type ProviderRequest struct {
	Sender         Sender
	ToEmail        string
	Subject        string
	HTML           string
	Text           string
	MessageID      string
	InReplyTo      string
	References     string
	HubspotEmailID int
}

// ProviderResult mirrors providers.send_one's normalized dict:
// {"success": true, "provider": ..., "id": ...} or {"error": ...}.
type ProviderResult struct {
	Success  bool
	Provider string
	ID       string
	Error    string
}

// ProviderAdapter delivers one message through a provider family (ESP API or
// OAuth mailbox). Send must be side-effect-free apart from the delivery
// attempt: the engine has already reserved the quota slot and will record the
// verdict. A Go error means the adapter itself was unavailable (transport,
// credentials); Success=false means the provider refused the message.
type ProviderAdapter interface {
	// Name identifies the adapter in diagnostics ("aden-cloud", "fake").
	Name() string
	Send(ctx context.Context, req ProviderRequest) (ProviderResult, error)
}
