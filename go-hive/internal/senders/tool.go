package senders

// The model-facing tool: one tool ("senders") with nine actions, matching the
// nine @mcp.tool functions in aden_tools/tools/senders_tool. One tool with an
// action selector is the same shape the other ported integration modules use,
// so the registry, schema union and error conventions stay uniform.

import (
	"context"
	"strings"

	"github.com/aden-hive/hive-go/internal/tools"
)

// ToolName is the registry name of the senders suite.
const ToolName = "senders"

// Action names, exactly as upstream registers them.
const (
	ActionListSenders       = "list_senders"
	ActionSendFromSender    = "send_from_sender"
	ActionSetupSender       = "setup_email_sender"
	ActionAdjustSender      = "adjust_sender"
	ActionPickSender        = "pick_sender"
	ActionSendCampaign      = "send_campaign"
	ActionSuppressRecipient = "suppress_recipient"
	ActionListSuppressed    = "list_suppressed"
	ActionSenderHistory     = "sender_history"
)

var actionOrder = []string{
	ActionListSenders,
	ActionSendFromSender,
	ActionSetupSender,
	ActionAdjustSender,
	ActionPickSender,
	ActionSendCampaign,
	ActionSuppressRecipient,
	ActionListSuppressed,
	ActionSenderHistory,
}

// ActionNames returns the nine action names in catalog order.
func ActionNames() []string { return append([]string(nil), actionOrder...) }

// ToolDescription is the model-facing menu: what each action does and which
// arguments it requires.
func ToolDescription() string {
	return strings.Join([]string{
		"Send email from the team's configured sender pool, with rotation, daily limits and a do-not-contact list.",
		"",
		"This build stores senders locally and delivers through SMTP mailboxes. A sender with no SMTP host/password cannot send: send_from_sender returns an explicit error instead of a fake success.",
		"",
		"Actions:",
		"- list_senders (required: none): every sender plus today's usage and remaining budget.",
		"- send_from_sender (required: sender, to_email): send one email. Provide `text` for a plain-text body or `html` for markup; the other part is derived.",
		"- setup_email_sender (required: provider, from_email): add a sender. For SMTP pass smtp_host + password (username defaults to from_email). Anything needing OAuth returns status=needs_user with a setup_url.",
		"- adjust_sender (required: sender): change daily_limit (0 blocks all), weight, enabled ('true'/'false'), name or tags.",
		"- pick_sender (required: none): choose the next sender under round_robin, weighted or least_used without sending.",
		"- send_campaign (required: recipients): send to many recipients, rotating senders and re-checking every per-send guard.",
		"- suppress_recipient (required: email): add someone to the do-not-contact list immediately (reason: unsubscribed, complained, hard_bounce, manual).",
		"- list_suppressed (required: none): read the do-not-contact list.",
		"- sender_history (required: none): the outbound send log, newest first. Filter by sender, to_email and colony_id.",
	}, "\n")
}

// ToolSchema is the union schema published to the model, matching the
// integrations registry convention (action selector plus per-action params).
func ToolSchema() map[string]any {
	properties := map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        ActionNames(),
			"description": "Action to invoke.",
		},
		"sender": map[string]any{
			"type":        "string",
			"description": "Sender id or name (from list_senders).",
		},
		"to_email": map[string]any{
			"type":        "string",
			"description": "Recipient email address.",
		},
		"recipients": map[string]any{
			"type":        "string",
			"description": "Recipient emails as a JSON array, or a comma/newline separated list.",
		},
		"subject": map[string]any{"type": "string", "description": "Subject line."},
		"html": map[string]any{
			"type":        "string",
			"description": "HTML version of the message. Generated from `text` when omitted.",
		},
		"text": map[string]any{
			"type":        []string{"string", "null"},
			"description": "Plain-text version. Generated from `html` when omitted; an empty string opts out of the plain part. Put plain-text bodies HERE, never in `html`.",
		},
		"html_content": map[string]any{
			"type":        "string",
			"description": "Deprecated alias for `html`; plain text passed here is detected and routed correctly.",
		},
		"provider": map[string]any{
			"type":        "string",
			"description": "'smtp' (mailbox), 'sendgrid', 'mailjet', 'google'/'gmail', or 'hubspot'. A bare mail host also works.",
		},
		"from_email": map[string]any{"type": "string", "description": "Address this sender sends from."},
		"name":       map[string]any{"type": "string", "description": "Label for the sender."},
		"from_name":  map[string]any{"type": "string", "description": "Display name on outgoing email."},
		"api_key":    map[string]any{"type": "string", "description": "ESP API key (SendGrid/Mailjet)."},
		"secret_key": map[string]any{"type": "string", "description": "Mailjet secret key."},
		"smtp_host":  map[string]any{"type": "string", "description": "Mail host for an SMTP sender (e.g. smtp.gmail.com)."},
		"smtp_port": map[string]any{
			"type":        "integer",
			"default":     587,
			"description": "Defaults to 587 (STARTTLS); 465 selects implicit TLS.",
		},
		"username":  map[string]any{"type": "string", "description": "Mailbox login; defaults to from_email."},
		"password":  map[string]any{"type": "string", "description": "Mailbox password — usually an app password."},
		"imap_host": map[string]any{"type": "string", "description": "Inbox host; inferred from smtp_host when omitted."},
		"imap_port": map[string]any{"type": "integer", "default": 993, "description": "Inbox port."},
		"weight": map[string]any{
			"type":        "integer",
			"default":     1,
			"description": "Rotation weight under the weighted policy.",
		},
		"daily_limit": map[string]any{
			"type":        "integer",
			"default":     0,
			"description": "Max sends/day; 0 means no limit when creating, and blocks all sends when adjusting.",
		},
		"tags": map[string]any{"type": "string", "description": "Comma-separated tags."},
		"enabled": map[string]any{
			"type":        "string",
			"description": "'true' to resume or 'false' to pause an existing sender.",
		},
		"policy": map[string]any{
			"type":        "string",
			"enum":        []string{PolicyRoundRobin, PolicyWeighted, PolicyLeastUsed},
			"default":     PolicyRoundRobin,
			"description": "Rotation policy.",
		},
		"pool": map[string]any{
			"type":        "string",
			"description": "Optional comma-separated sender ids/names to rotate among.",
		},
		"email":  map[string]any{"type": "string", "description": "Address to suppress."},
		"reason": map[string]any{"type": "string", "description": "unsubscribed | complained | hard_bounce | manual."},
		"note":   map[string]any{"type": "string", "description": "Optional free text (e.g. what the person wrote)."},
		"limit": map[string]any{
			"type":        "integer",
			"default":     50,
			"description": "Max history rows (newest first).",
		},
		"campaign_id": map[string]any{"type": "string", "description": "Optional campaign identifier for the send log."},
		"colony_id": map[string]any{
			"type":        "string",
			"description": "Colony to wake on a reply (injected by the framework, not chosen by the model).",
		},
		"hubspot_email_id": map[string]any{
			"type":        "integer",
			"default":     0,
			"description": "HubSpot marketing email id (HubSpot senders only; unsupported in the local SMTP build).",
		},
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   []string{"action"},
	}
}

// Execute runs one action. Expected failures are returned as maps with an
// "error" key (upstream convention); Go errors are reserved for I/O faults.
func (e *Engine) Execute(ctx context.Context, input map[string]any) (any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	action := stringArg(input, "action")
	if action == "" {
		return map[string]any{"error": "senders: action is required", "actions": ActionNames()}, nil
	}
	switch action {
	case ActionListSenders:
		return e.ListSenders()

	case ActionSendFromSender:
		return e.SendFromSender(ctx, SendRequest{
			Sender:         stringArg(input, "sender"),
			ToEmail:        stringArg(input, "to_email"),
			Subject:        stringArg(input, "subject"),
			HTML:           stringArg(input, "html"),
			Text:           optionalStringArg(input, "text"),
			Legacy:         stringArg(input, "html_content"),
			HubspotEmailID: intArg(input, "hubspot_email_id", 0),
			CampaignID:     stringArg(input, "campaign_id"),
			ColonyID:       stringArg(input, "colony_id"),
			Step:           1,
		})

	case ActionSetupSender:
		var dailyLimit *int
		if value := intArg(input, "daily_limit", 0); value > 0 {
			dailyLimit = &value
		}
		return e.SetupSender(SetupRequest{
			Provider:   stringArg(input, "provider"),
			FromEmail:  stringArg(input, "from_email"),
			Name:       stringArg(input, "name"),
			FromName:   stringArg(input, "from_name"),
			APIKey:     stringArg(input, "api_key"),
			SecretKey:  stringArg(input, "secret_key"),
			SMTPHost:   stringArg(input, "smtp_host"),
			SMTPPort:   intArg(input, "smtp_port", 0),
			Username:   stringArg(input, "username"),
			Password:   stringArg(input, "password"),
			IMAPHost:   stringArg(input, "imap_host"),
			IMAPPort:   intArg(input, "imap_port", 0),
			Weight:     intArg(input, "weight", 1),
			DailyLimit: dailyLimit,
			Tags:       SplitList(stringArg(input, "tags")),
		})

	case ActionAdjustSender:
		patch := AdjustPatch{}
		if value := intArg(input, "daily_limit", -1); value >= 0 {
			patch.DailyLimit = &value
		}
		if value := intArg(input, "weight", -1); value >= 0 {
			patch.Weight = &value
		}
		if raw := stringArg(input, "enabled"); raw != "" {
			value, _ := parseEnabled(raw) // unknown spellings pause, like upstream
			patch.Enabled = &value
		}
		if name := stringArg(input, "name"); name != "" {
			patch.Name = &name
		}
		if tags := stringArg(input, "tags"); tags != "" {
			patch.Tags = SplitList(tags)
		}
		return e.AdjustSender(stringArg(input, "sender"), patch)

	case ActionPickSender:
		return e.PickSender(stringArg(input, "pool"), stringArg(input, "policy"), stringArg(input, "tags"))

	case ActionSendCampaign:
		return e.SendCampaign(ctx, CampaignRequest{
			Recipients:     stringArg(input, "recipients"),
			Subject:        stringArg(input, "subject"),
			HTML:           stringArg(input, "html"),
			Text:           optionalStringArg(input, "text"),
			Legacy:         stringArg(input, "html_content"),
			Policy:         stringArg(input, "policy"),
			Pool:           stringArg(input, "pool"),
			Tags:           stringArg(input, "tags"),
			HubspotEmailID: intArg(input, "hubspot_email_id", 0),
			CampaignID:     stringArg(input, "campaign_id"),
			ColonyID:       stringArg(input, "colony_id"),
		})

	case ActionSuppressRecipient:
		return e.SuppressRecipient(
			stringArg(input, "email"),
			stringArgDefault(input, "reason", ReasonUnsubscribed),
			stringArg(input, "note"),
		)

	case ActionListSuppressed:
		return e.ListSuppressed()

	case ActionSenderHistory:
		return e.SenderHistory(HistoryQuery{
			Sender:  stringArg(input, "sender"),
			ToEmail: stringArg(input, "to_email"),
			Limit:   intArg(input, "limit", 50),
			Colony:  stringArg(input, "colony_id"),
		})
	}
	return map[string]any{"error": "senders: unknown action " + action, "actions": ActionNames()}, nil
}

func stringArgDefault(input map[string]any, name, fallback string) string {
	if value := stringArg(input, name); value != "" {
		return value
	}
	return fallback
}

// Register installs the tool into a shared tool registry and returns the number
// installed (1). The executor receives the raw input map, exactly like the
// other ported integrations.
func (e *Engine) Register(registry *tools.ToolRegistry) int {
	if registry == nil {
		return 0
	}
	registry.Register(ToolName, tools.Tool{
		Name:            ToolName,
		Description:     ToolDescription(),
		Parameters:      ToolSchema(),
		ConcurrencySafe: false,
	}, func(inputs map[string]any) (any, error) {
		return e.Execute(context.Background(), inputs)
	})
	return 1
}

// Install opens an engine under home and registers the senders tool.
func Install(registry *tools.ToolRegistry, home string, opts ...Option) (int, error) {
	engine, err := Open(home, opts...)
	if err != nil {
		return 0, err
	}
	return engine.Register(registry), nil
}
