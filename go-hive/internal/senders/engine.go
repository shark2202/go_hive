package senders

// Engine is the senders tool's execution surface: the nine actions from
// aden_tools/tools/senders_tool, backed by the local store, rotation,
// suppression list and send log. Expected failures are values ({"error": ...})
// exactly as upstream returns dicts; only infrastructure faults surface as Go
// errors.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Engine owns the sender pool and its delivery path.
type Engine struct {
	dir          string
	store        *Store
	log          *SendLog
	suppressions *SuppressionStore
	cursors      *CursorStore
	threads      *ThreadStore
	mailer       Mailer
	provider     ProviderAdapter
	now          func() time.Time
}

// Option mutates the engine during Open.
type Option func(*Engine)

// WithMailer swaps the delivery transport (tests, proxies).
func WithMailer(mailer Mailer) Option {
	return func(e *Engine) { e.mailer = mailer }
}

// WithProviderAdapter installs the cloud/ESP delivery seam. Without one, a
// non-SMTP sender keeps the explicit "requires the Aden cloud connector"
// refusal; with one, quota, suppression, send-log and conversation tracking
// still run locally around the adapter.
func WithProviderAdapter(adapter ProviderAdapter) Option {
	return func(e *Engine) { e.provider = adapter }
}

// WithClock overrides the clock (deterministic day rollover in tests).
func WithClock(now func() time.Time) Option {
	return func(e *Engine) { e.now = now }
}

// Open binds an engine to a Hive home directory ($HIVE_HOME). State lives in
// <home>/senders/{senders,usage,sends,suppressions}.json.
func Open(home string, opts ...Option) (*Engine, error) {
	trimmed := strings.TrimSpace(home)
	if trimmed == "" {
		return nil, errors.New("senders: empty home directory")
	}
	dir := filepath.Join(trimmed, "senders")
	engine := &Engine{dir: dir, now: time.Now}
	for _, opt := range opts {
		opt(engine)
	}
	if engine.now == nil {
		engine.now = time.Now
	}
	if engine.mailer == nil {
		engine.mailer = &SMTPMailer{}
	}
	engine.store = OpenStore(dir)
	engine.log = OpenSendLog(dir, engine.now)
	engine.suppressions = OpenSuppressionStore(dir, engine.now)
	engine.cursors = OpenCursorStore(dir, engine.now)
	engine.threads = OpenThreadStore(dir, engine.now)
	return engine, nil
}

// Dir is the state directory (diagnostics and tests).
func (e *Engine) Dir() string { return e.dir }

// Threads exposes the conversation store to a reply poller.
func (e *Engine) Threads() *ThreadStore { return e.threads }

// RecordThreadReply maps an inbound reply's threading headers to the stored
// conversation and increments its reply count. A token we never minted
// returns nil, so a stranger cannot forge one to wake an agent.
func (e *Engine) RecordThreadReply(inReplyTo, references string) (*ThreadRecord, error) {
	token := ExtractThreadToken(inReplyTo, references)
	if token == "" {
		return nil, nil
	}
	return e.threads.RecordReply(token)
}

func (e *Engine) availableNames() ([]string, error) {
	rows, err := e.store.List(true)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names, nil
}

// ListSenders ports list_senders.
func (e *Engine) ListSenders() (map[string]any, error) {
	rows, err := e.store.List(true)
	if err != nil {
		return nil, err
	}
	usage, err := e.log.UsageToday()
	if err != nil {
		return nil, err
	}
	senders := make([]map[string]any, 0, len(rows))
	for _, sender := range rows {
		sent := usage[sender.ID]
		view := sender.PublicView()
		view["sent_today"] = sent
		if sender.DailyLimit == nil {
			view["remaining_today"] = nil
		} else {
			remaining := *sender.DailyLimit - sent
			if remaining < 0 {
				remaining = 0
			}
			view["remaining_today"] = remaining
		}
		senders = append(senders, view)
	}
	return map[string]any{"count": len(senders), "senders": senders}, nil
}

// missingSenderOutput makes "nothing to send from" impossible to mistake for a
// success: an empty pool names the next step instead of producing a silent
// no-op or a fake message id.
func missingSenderOutput(reference string, names []string) map[string]any {
	if len(names) == 0 {
		return map[string]any{
			"error": "No email senders are configured. Add one with setup_email_sender " +
				"(an SMTP host and app password) before sending.",
			"available_senders": names,
		}
	}
	if strings.TrimSpace(reference) == "" {
		return map[string]any{
			"error":             "sender is required: pass a sender id or name from list_senders",
			"available_senders": names,
		}
	}
	return map[string]any{
		"error":             fmt.Sprintf("No sender named '%s'", reference),
		"available_senders": names,
	}
}

// SendRequest is one send_from_sender call.
type SendRequest struct {
	Sender         string
	ToEmail        string
	Subject        string
	HTML           string
	Text           *string
	Legacy         string
	HubspotEmailID int
	CampaignID     string
	ColonyID       string
	Step           int
}

// SendFromSender ports send_from_sender.
func (e *Engine) SendFromSender(ctx context.Context, req SendRequest) (map[string]any, error) {
	reference := strings.TrimSpace(req.Sender)
	if reference == "" {
		names, err := e.availableNames()
		if err != nil {
			return nil, err
		}
		return missingSenderOutput(reference, names), nil
	}
	sender, err := e.store.Get(reference)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		names, listErr := e.availableNames()
		if listErr != nil {
			return nil, listErr
		}
		return missingSenderOutput(reference, names), nil
	}
	if req.ToEmail == "" || !strings.Contains(req.ToEmail, "@") {
		return map[string]any{"error": "Invalid recipient email address"}, nil
	}

	htmlBody, textBody := ResolveBody(req.HTML, req.Text, req.Legacy)
	if sender.Provider != ProviderHubSpot {
		if req.Subject == "" {
			return map[string]any{"error": "subject cannot be empty"}, nil
		}
		if htmlBody == "" {
			return map[string]any{"error": "Provide a body: `html`, or `text` for a plain-text message."}, nil
		}
	}
	return e.deliver(ctx, deliveryRequest{
		sender:         *sender,
		toEmail:        req.ToEmail,
		subject:        req.Subject,
		html:           htmlBody,
		text:           textBody,
		campaignID:     req.CampaignID,
		colonyID:       req.ColonyID,
		step:           req.Step,
		hubspotEmailID: req.HubspotEmailID,
	})
}

// unsupportedProvider explains, per sender, why local mode cannot deliver it.
// Refusing is the honest option: pretending to send would poison the send log
// and the campaign audit trail.
func unsupportedProvider(sender Sender) string {
	switch sender.Provider {
	case ProviderGoogle, ProviderHubSpot:
		return fmt.Sprintf(
			"Sender %q uses %s over OAuth. This build can only deliver through an SMTP mailbox; OAuth delivery requires the Aden cloud connector.",
			sender.Name, sender.Provider,
		)
	default:
		return fmt.Sprintf(
			"Sender %q uses provider %q. This build can only deliver through an SMTP mailbox; ESP API delivery requires the Aden cloud connector. Reconfigure this sender as an SMTP mailbox to send from it.",
			sender.Name, sender.Provider,
		)
	}
}

func remainingOrDefault(reservation Reservation) any {
	if reservation.Remaining == nil {
		return 0
	}
	return *reservation.Remaining
}

func (e *Engine) deniedOutput(sender Sender, reservation Reservation) map[string]any {
	out := map[string]any{
		"error":           reservation.Reason,
		"sender":          sender.Name,
		"sent_today":      reservation.SentToday,
		"daily_limit":     nil,
		"remaining_today": remainingOrDefault(reservation),
	}
	if reservation.DailyLimit != nil {
		out["daily_limit"] = *reservation.DailyLimit
	}
	switch {
	case reservation.Duplicate:
		out["duplicate"] = true
		out["guidance"] = "Already sent to this recipient. Do NOT retry or use another sender."
	case reservation.DeniedBecause == "suppressed":
		out["suppressed"] = true
		out["guidance"] = "This person must not be contacted, by any sender. Remove them from the campaign; do not try again."
	case reservation.DeniedBecause == "daily_limit":
		out["guidance"] = "This sender is exhausted for today. A different sender may still have budget."
	}
	return out
}

// deliveryRequest is one send after recipient and body validation. Bundling
// the fields keeps the dispatch and quota path readable as providers grow.
type deliveryRequest struct {
	sender         Sender
	toEmail        string
	subject        string
	html           string
	text           string
	campaignID     string
	colonyID       string
	step           int
	hubspotEmailID int
}

// deliver reserves, sends, and closes the loop. It is the only path that may
// put a row in the send log.
func (e *Engine) deliver(ctx context.Context, req deliveryRequest) (map[string]any, error) {
	sender := req.sender
	// Refuse before reserving. A provider with no transport cannot deliver,
	// and a reservation for it would burn the day's budget for nothing.
	if sender.Provider != ProviderSMTP && e.provider == nil {
		return map[string]any{"error": unsupportedProvider(sender), "sender": sender.Name}, nil
	}
	if sender.Provider == ProviderHubSpot && req.hubspotEmailID == 0 {
		return map[string]any{
			"error": fmt.Sprintf(
				"HubSpot sender %q needs hubspot_email_id (the id of a marketing email built in HubSpot); subject/html are ignored.",
				sender.Name,
			),
			"sender": sender.Name,
		}, nil
	}
	step := req.step
	if step <= 0 {
		step = 1
	}
	// Only a mailbox with an inbox can host a reply, and only a known colony
	// gives that reply somewhere to go. Otherwise the send is a dead end and we
	// say so instead of minting a conversation token that can never resolve.
	trackable := sender.CanReceive && req.colonyID != ""
	messageID := ""
	token := ""
	if trackable {
		messageID, token = MintMessageID(sender.FromEmail)
	}

	suppressed, err := e.suppressions.IsSuppressed(req.toEmail)
	if err != nil {
		return nil, err
	}
	reservation, err := e.log.Reserve(ReserveRequest{
		SenderID:          sender.ID,
		SenderName:        sender.Name,
		ToEmail:           req.toEmail,
		Subject:           req.subject,
		ColonyID:          req.colonyID,
		CampaignID:        req.campaignID,
		Step:              step,
		ConversationToken: token,
		MessageID:         messageID,
		DailyLimit:        sender.DailyLimit,
		Suppressed:        suppressed,
	})
	if err != nil {
		return nil, err
	}
	if !reservation.Allowed {
		return e.deniedOutput(sender, reservation), nil
	}

	outcome, sendErr := e.dispatch(ctx, sender, req, messageID)
	if sendErr != nil {
		_ = e.log.Complete(reservation.SendID, StatusFailed, "", truncate(sendErr.Error(), 2000))
		return map[string]any{"error": sendErr.Error(), "sender": sender.Name}, nil
	}
	if err := e.log.Complete(reservation.SendID, StatusSent, truncate(outcome.ID, 998), ""); err != nil {
		return nil, err
	}

	result := map[string]any{
		"success":  true,
		"provider": outcome.Provider,
		"id":       outcome.ID,
		"sender":   sender.Name,
	}
	if sender.DailyLimit != nil {
		result["remaining_today"] = remainingOrDefault(reservation)
		result["daily_limit"] = *sender.DailyLimit
	}
	if trackable {
		if _, threadErr := e.threads.RecordSend(ThreadSend{
			Token:     token,
			MessageID: messageID,
			ColonyID:  req.colonyID,
			SenderID:  sender.ID,
			FromEmail: sender.FromEmail,
			ToEmail:   req.toEmail,
			Subject:   req.subject,
		}); threadErr != nil {
			// The mail is already out. Never report a conversation token that
			// cannot resolve: surface the persistence failure instead.
			result["thread_error"] = truncate(threadErr.Error(), 500)
			result["reply_tracking"] = "unavailable — the conversation could not be recorded, so a reply will not resolve."
		} else {
			result["conversation"] = token
		}
	} else if !sender.CanReceive {
		result["reply_tracking"] = fmt.Sprintf(
			"none — '%s' has no inbox, so a reply from %s will not reach you. Use a mailbox sender for conversations.",
			sender.Name, req.toEmail,
		)
	}
	return result, nil
}

// dispatch hands one already-reserved message to the transport that owns its
// provider: the local SMTP mailer, or the injected cloud/ESP adapter.
func (e *Engine) dispatch(ctx context.Context, sender Sender, req deliveryRequest, messageID string) (MailOutcome, error) {
	if sender.Provider == ProviderSMTP {
		return e.mailer.Send(ctx, Outgoing{
			Sender:    sender,
			ToEmail:   req.toEmail,
			Subject:   req.subject,
			HTML:      req.html,
			Text:      req.text,
			MessageID: messageID,
		})
	}
	result, err := e.provider.Send(ctx, ProviderRequest{
		Sender:         sender,
		ToEmail:        req.toEmail,
		Subject:        req.subject,
		HTML:           req.html,
		Text:           req.text,
		MessageID:      messageID,
		HubspotEmailID: req.hubspotEmailID,
	})
	if err != nil {
		return MailOutcome{}, err
	}
	if !result.Success {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = fmt.Sprintf("%s rejected the message", e.provider.Name())
		}
		return MailOutcome{}, errors.New(message)
	}
	provider := result.Provider
	if provider == "" {
		provider = sender.Provider
	}
	return MailOutcome{Provider: provider, ID: result.ID}, nil
}

// SetupRequest is one setup_email_sender call.
type SetupRequest struct {
	Provider   string
	FromEmail  string
	Name       string
	FromName   string
	APIKey     string
	SecretKey  string
	SMTPHost   string
	SMTPPort   int
	Username   string
	Password   string
	IMAPHost   string
	IMAPPort   int
	Weight     int
	DailyLimit *int
	Tags       []string
}

// SetupSender ports setup_email_sender. It saves what this build can use and
// hands everything else back as a needs_user deep link.
func (e *Engine) SetupSender(req SetupRequest) (map[string]any, error) {
	displayName := strings.TrimSpace(req.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(req.FromName)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(req.FromEmail)
	}

	if req.FromEmail == "" || !strings.Contains(req.FromEmail, "@") {
		return map[string]any{"status": "error", "error": fmt.Sprintf("Invalid from_email: %q", req.FromEmail)}, nil
	}

	resolved, ok := normalizeProvider(req.Provider)
	if !ok {
		return map[string]any{
			"status":     "needs_user",
			"provider":   req.Provider,
			"from_email": req.FromEmail,
			"reason": fmt.Sprintf(
				"'%s' is not a supported sender provider. Use 'smtp' with a mail host and app password, "+
					"or connect Gmail/HubSpot over OAuth.", req.Provider,
			),
			"setup_url": handoffURL("", req.FromEmail, displayName, req.FromName, req.Provider+" is not a supported sender provider"),
		}, nil
	}

	switch resolved {
	case ProviderSMTP:
		host := strings.TrimSpace(req.SMTPHost)
		if host == "" {
			host = strings.TrimSpace(req.Provider)
		}
		if host == "" || !strings.Contains(host, ".") {
			return map[string]any{"status": "error", "error": "SMTP senders need an smtp_host (e.g. smtp.gmail.com)."}, nil
		}
		username := req.Username
		if username == "" {
			username = req.FromEmail
		}
		if req.Password == "" {
			return map[string]any{
				"status":     "needs_user",
				"provider":   ProviderSMTP,
				"from_email": req.FromEmail,
				"reason": fmt.Sprintf(
					"No password supplied for the mailbox %s on %s. Most providers (Gmail/Workspace included) "+
						"require an app password, not the account password.", req.FromEmail, host,
				),
				"setup_url": handoffURL(ProviderSMTP, req.FromEmail, displayName, req.FromName,
					"paste the app password for "+req.FromEmail),
			}, nil
		}
		port := req.SMTPPort
		if port == 0 {
			port = 587
		}
		security := "starttls"
		if port == 465 {
			security = "tls"
		}
		inbox := strings.TrimSpace(req.IMAPHost)
		inferred := false
		if inbox == "" {
			inbox = defaultIMAPHost(host)
			inferred = inbox != ""
		}
		secret := map[string]any{
			"host":     host,
			"port":     port,
			"username": username,
			"password": req.Password,
			"security": security,
		}
		if inbox != "" {
			imapPort := req.IMAPPort
			if imapPort == 0 {
				imapPort = 993
			}
			secret["imap_host"] = inbox
			secret["imap_port"] = imapPort
			if inferred {
				secret["imap_inferred"] = true
			}
		}
		weight := req.Weight
		if weight <= 0 {
			weight = 1
		}
		sender, err := e.store.Create(Sender{
			Name:       displayName,
			Provider:   ProviderSMTP,
			AuthType:   AuthAPIKey,
			FromEmail:  req.FromEmail,
			FromName:   req.FromName,
			Secret:     secret,
			Weight:     weight,
			DailyLimit: req.DailyLimit,
			Enabled:    true,
			Tags:       req.Tags,
			CanReceive: inbox != "",
			CreatedAt:  e.now().UTC().Format(time.RFC3339),
		})
		if err != nil {
			return nil, err
		}
		result := map[string]any{
			"status":             "saved",
			"sender_id":          sender.ID,
			"name":               sender.Name,
			"provider":           ProviderSMTP,
			"from_email":         sender.FromEmail,
			"credential_valid":   nil,
			"credential_checked": false,
		}
		result["can_receive"] = sender.CanReceive
		if sender.CanReceive {
			result["imap_host"] = inbox
		} else {
			result["warning"] = fmt.Sprintf(
				"%s can send but has no IMAP inbox configured, so replies to it will NOT be seen by the agent. "+
					"Add imap_host to enable the reply loop.", sender.FromEmail,
			)
		}
		if _, hasWarning := result["warning"]; !hasWarning {
			result["warning"] = "The mailbox was saved but not verified; the first send will surface any authentication errors."
		}
		return result, nil

	case ProviderSendGrid, ProviderMailjet:
		return map[string]any{
			"status":     "needs_user",
			"provider":   resolved,
			"from_email": req.FromEmail,
			"reason": fmt.Sprintf(
				"The local runtime delivers mail through an SMTP mailbox only. %s API delivery needs the Aden "+
					"cloud connector, which is not part of this build. Add this mailbox as an SMTP sender instead.",
				resolved,
			),
			"setup_url": handoffURL(resolved, req.FromEmail, displayName, req.FromName, "configure an SMTP mailbox instead"),
		}, nil

	default: // google, hubspot — OAuth consent is a human step in every build.
		return map[string]any{
			"status":     "needs_user",
			"provider":   resolved,
			"from_email": req.FromEmail,
			"reason": fmt.Sprintf(
				"%s is not connected over OAuth yet. Connecting it requires signing in through the provider's "+
					"consent screen, which only you can do.", req.FromEmail,
			),
			"setup_url": handoffURL(resolved, req.FromEmail, displayName, req.FromName, "connect "+req.FromEmail+" over OAuth"),
		}, nil
	}
}

// AdjustPatch carries the optional adjust_sender fields. Nil means "leave
// unchanged"; 0 is meaningful for both daily_limit and weight, so it cannot
// double as the "unset" sentinel.
type AdjustPatch struct {
	DailyLimit *int
	Weight     *int
	Enabled    *bool
	Name       *string
	Tags       []string
}

// AdjustSender ports adjust_sender against the local store.
func (e *Engine) AdjustSender(reference string, patch AdjustPatch) (map[string]any, error) {
	sender, err := e.store.Get(reference)
	if err != nil {
		return nil, err
	}
	if sender == nil {
		names, listErr := e.availableNames()
		if listErr != nil {
			return nil, listErr
		}
		return map[string]any{
			"error":             fmt.Sprintf("No sender named '%s'", reference),
			"available_senders": names,
		}, nil
	}
	if patch.DailyLimit == nil && patch.Weight == nil && patch.Enabled == nil && patch.Name == nil && len(patch.Tags) == 0 {
		return map[string]any{"error": "Nothing to change — supply at least one of daily_limit, weight, enabled, name, tags."}, nil
	}

	before, after, err := e.store.Update(sender.ID, func(row *Sender) {
		if patch.DailyLimit != nil {
			limit := *patch.DailyLimit
			row.DailyLimit = &limit
		}
		if patch.Weight != nil {
			row.Weight = *patch.Weight
		}
		if patch.Enabled != nil {
			row.Enabled = *patch.Enabled
		}
		if patch.Name != nil {
			row.Name = *patch.Name
		}
		if len(patch.Tags) > 0 {
			row.Tags = append([]string(nil), patch.Tags...)
		}
	})
	if err != nil {
		return nil, err
	}

	changed := map[string]any{}
	if !optionalIntEqual(before.DailyLimit, after.DailyLimit) {
		changed["daily_limit"] = map[string]any{"from": optionalInt(before.DailyLimit), "to": optionalInt(after.DailyLimit)}
	}
	if before.Weight != after.Weight {
		changed["weight"] = map[string]any{"from": before.Weight, "to": after.Weight}
	}
	if before.Enabled != after.Enabled {
		changed["enabled"] = map[string]any{"from": before.Enabled, "to": after.Enabled}
	}
	if before.Name != after.Name {
		changed["name"] = map[string]any{"from": before.Name, "to": after.Name}
	}
	if strings.Join(before.Tags, ",") != strings.Join(after.Tags, ",") {
		changed["tags"] = map[string]any{"from": before.Tags, "to": after.Tags}
	}

	return map[string]any{
		"sender":      after.Name,
		"sender_id":   after.ID,
		"changed":     changed,
		"daily_limit": optionalInt(after.DailyLimit),
		"weight":      after.Weight,
		"enabled":     after.Enabled,
	}, nil
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalIntEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// SuppressRecipient ports suppress_recipient.
func (e *Engine) SuppressRecipient(email, reason, note string) (map[string]any, error) {
	row, err := e.suppressions.Add(email, reason, note)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	return map[string]any{"suppressed": row.Email, "reason": row.Reason}, nil
}

// ListSuppressed ports list_suppressed.
func (e *Engine) ListSuppressed() (map[string]any, error) {
	rows, err := e.suppressions.List()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"email":      row.Email,
			"reason":     row.Reason,
			"note":       row.Note,
			"created_at": row.CreatedAt,
		})
	}
	return map[string]any{"count": len(out), "suppressed": out}, nil
}

// HistoryQuery is one sender_history call.
type HistoryQuery struct {
	Sender  string
	ToEmail string
	Limit   int
	Colony  string
}

// SenderHistory ports sender_history.
func (e *Engine) SenderHistory(query HistoryQuery) (map[string]any, error) {
	senderID := ""
	if strings.TrimSpace(query.Sender) != "" {
		resolved, err := e.store.Get(query.Sender)
		if err != nil {
			return nil, err
		}
		// Upstream resolves an unknown name to None and then applies no sender
		// filter; keep that behaviour rather than hiding the whole log.
		if resolved != nil {
			senderID = resolved.ID
		}
	}
	limit := query.Limit
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := e.log.History(HistoryFilter{
		SenderID: senderID,
		ToEmail:  query.ToEmail,
		ColonyID: query.Colony,
		Limit:    limit,
	})
	if err != nil {
		return nil, err
	}
	sends := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		sentAt := row.CompletedAt
		if sentAt == "" {
			sentAt = row.ReservedAt
		}
		view := map[string]any{
			"to_email":    row.ToEmail,
			"subject":     row.Subject,
			"status":      row.Status,
			"sent_at":     sentAt,
			"sender_id":   row.SenderID,
			"sender_name": row.SenderName,
		}
		if row.ConversationToken != "" {
			view["conversation_token"] = row.ConversationToken
		}
		if row.Error != "" {
			view["error"] = row.Error
		}
		if row.CampaignID != "" {
			view["campaign_id"] = row.CampaignID
		}
		if row.ColonyID != "" {
			view["colony_id"] = row.ColonyID
		}
		if row.MessageID != "" {
			view["message_id"] = row.MessageID
		}
		sends = append(sends, view)
	}
	return map[string]any{"count": len(sends), "sends": sends}, nil
}

// candidatePool ports _candidate_pool: enabled senders, optionally narrowed by
// ids/names and tags.
func (e *Engine) candidatePool(pool, tags string) ([]Sender, error) {
	rows, err := e.store.List(false)
	if err != nil {
		return nil, err
	}
	wantedIDs := map[string]struct{}{}
	for _, item := range SplitList(pool) {
		wantedIDs[strings.ToLower(item)] = struct{}{}
	}
	if len(wantedIDs) > 0 {
		filtered := make([]Sender, 0, len(rows))
		for _, sender := range rows {
			if _, ok := wantedIDs[strings.ToLower(sender.ID)]; ok {
				filtered = append(filtered, sender)
				continue
			}
			if _, ok := wantedIDs[strings.ToLower(sender.Name)]; ok {
				filtered = append(filtered, sender)
			}
		}
		rows = filtered
	}
	wantedTags := map[string]struct{}{}
	for _, tag := range SplitList(tags) {
		wantedTags[strings.ToLower(tag)] = struct{}{}
	}
	if len(wantedTags) > 0 {
		filtered := make([]Sender, 0, len(rows))
		for _, sender := range rows {
			for _, tag := range sender.Tags {
				if _, ok := wantedTags[strings.ToLower(tag)]; ok {
					filtered = append(filtered, sender)
					break
				}
			}
		}
		rows = filtered
	}
	return rows, nil
}

// PickSender ports pick_sender.
func (e *Engine) PickSender(pool, policy, tags string) (map[string]any, error) {
	candidates, err := e.candidatePool(pool, tags)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return map[string]any{"error": "No senders match the requested pool/tags"}, nil
	}
	usage, err := e.log.UsageToday()
	if err != nil {
		return nil, err
	}
	chosen, err := Pick(candidates, policy, usage, e.cursors)
	if err != nil {
		return nil, err
	}
	if chosen == nil {
		return map[string]any{"error": "No eligible sender (all disabled or over daily limit)"}, nil
	}
	return map[string]any{
		"id":         chosen.ID,
		"name":       chosen.Name,
		"provider":   chosen.Provider,
		"from_email": chosen.FromEmail,
	}, nil
}

// CampaignRequest is one send_campaign call.
type CampaignRequest struct {
	Recipients     string
	Subject        string
	HTML           string
	Text           *string
	Legacy         string
	Policy         string
	Pool           string
	Tags           string
	HubspotEmailID int
	CampaignID     string
	ColonyID       string
}

// SendCampaign ports send_campaign: one sender per recipient under policy,
// with every per-send guard (suppression, duplicate, daily cap) still applied.
func (e *Engine) SendCampaign(ctx context.Context, req CampaignRequest) (map[string]any, error) {
	recipients := ParseRecipients(req.Recipients)
	if len(recipients) == 0 {
		return map[string]any{"error": "No recipients provided"}, nil
	}
	candidates, err := e.candidatePool(req.Pool, req.Tags)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return map[string]any{"error": "No senders match the requested pool/tags"}, nil
	}

	htmlBody, textBody := ResolveBody(req.HTML, req.Text, req.Legacy)
	if htmlBody == "" {
		onlyHubSpot := true
		for _, sender := range candidates {
			if sender.Provider != ProviderHubSpot {
				onlyHubSpot = false
				break
			}
		}
		if !onlyHubSpot {
			return map[string]any{"error": "Provide a body: `html`, or `text` for a plain-text message."}, nil
		}
	}

	usage, err := e.log.UsageToday()
	if err != nil {
		return nil, err
	}
	results := make([]map[string]any, 0, len(recipients))
	sent := 0
	failed := 0
	for _, recipient := range recipients {
		chosen, pickErr := Pick(candidates, req.Policy, usage, e.cursors)
		if pickErr != nil {
			return nil, pickErr
		}
		if chosen == nil {
			results = append(results, map[string]any{"to": recipient, "error": "No eligible sender (daily limits reached)"})
			failed++
			continue
		}
		if recipient == "" || !strings.Contains(recipient, "@") {
			results = append(results, map[string]any{"to": recipient, "sender": chosen.Name, "error": "Invalid recipient"})
			failed++
			continue
		}
		outcome, sendErr := e.deliver(ctx, deliveryRequest{
			sender:         *chosen,
			toEmail:        recipient,
			subject:        req.Subject,
			html:           htmlBody,
			text:           textBody,
			campaignID:     req.CampaignID,
			colonyID:       req.ColonyID,
			step:           1,
			hubspotEmailID: req.HubspotEmailID,
		})
		if sendErr != nil {
			return nil, sendErr
		}
		if outcome["success"] == true {
			row := map[string]any{"to": recipient, "sender": chosen.Name, "success": true, "id": outcome["id"]}
			if conversation, ok := outcome["conversation"]; ok {
				row["conversation"] = conversation
			}
			if threadError, ok := outcome["thread_error"]; ok {
				row["thread_error"] = threadError
			}
			results = append(results, row)
			sent++
			continue
		}
		results = append(results, map[string]any{
			"to":     recipient,
			"sender": chosen.Name,
			"error":  outcome["error"],
		})
		failed++
	}

	tracked := 0
	for _, row := range results {
		if _, ok := row["conversation"]; ok {
			tracked++
		}
	}
	out := map[string]any{
		"sent":                sent,
		"failed":              failed,
		"total":               len(recipients),
		"tracked_for_replies": tracked,
		"results":             results,
	}
	if sent > 0 && tracked == 0 {
		out["warning"] = "No recipient is tracked for replies — every sender used is send-only (no inbox). " +
			"Responses to this campaign will not reach you. Use a mailbox sender to hold a conversation."
	}
	return out, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
