package senders

// The sender model mirrors upstream tools/src/aden_tools/senders/registry.py.
// The pool is local in this build: upstream pulls it from Aden cloud, while the
// Go runtime stores the same rows under $HIVE_HOME/senders/senders.json and
// sends through SMTP only. Everything else (rotation, daily limits,
// suppression, send log) keeps the upstream semantics.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Providers the sender pool models. Only ProviderSMTP has a delivery path in
// this build; the ESP/OAuth providers are still representable so a pasted
// config can be diagnosed instead of silently mis-sent.
const (
	ProviderSMTP     = "smtp"
	ProviderGoogle   = "google"
	ProviderHubSpot  = "hubspot"
	ProviderSendGrid = "sendgrid"
	ProviderMailjet  = "mailjet"
)

// Auth types mirror SenderConfig.auth_type upstream.
const (
	AuthOAuth  = "oauth"
	AuthAPIKey = "api_key"
)

// Sender is one "from" identity the agent may send email from.
type Sender struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Provider      string         `json:"provider"`
	AuthType      string         `json:"auth_type"`
	FromEmail     string         `json:"from_email"`
	FromName      string         `json:"from_name,omitempty"`
	IntegrationID string         `json:"integration_id,omitempty"`
	Secret        map[string]any `json:"secret,omitempty"`
	Weight        int            `json:"weight"`
	DailyLimit    *int           `json:"daily_limit"`
	Enabled       bool           `json:"enabled"`
	Tags          []string       `json:"tags"`
	CanReceive    bool           `json:"can_receive"`
	CreatedAt     string         `json:"created_at,omitempty"`
}

// PublicView is the secret-free row handed to the model and the UI. Field
// names and nullability match SenderConfig.public_view upstream.
func (s Sender) PublicView() map[string]any {
	var fromName any
	if s.FromName != "" {
		fromName = s.FromName
	}
	tags := s.Tags
	if tags == nil {
		tags = []string{}
	}
	var limit any
	if s.DailyLimit != nil {
		limit = *s.DailyLimit
	}
	return map[string]any{
		"id":          s.ID,
		"name":        s.Name,
		"provider":    s.Provider,
		"auth_type":   s.AuthType,
		"from_email":  s.FromEmail,
		"from_name":   fromName,
		"weight":      s.Weight,
		"daily_limit": limit,
		"enabled":     s.Enabled,
		"tags":        tags,
		"can_receive": s.CanReceive,
	}
}

// providerAliases maps a pasted provider label to the modelled provider.
// The two ESP hostnames map to the ESP's API rather than a generic mailbox:
// the same SendGrid key authenticates both, and upstream sends over the API.
var providerAliases = map[string]string{
	"google":            ProviderGoogle,
	"gmail":             ProviderGoogle,
	"hubspot":           ProviderHubSpot,
	"sendgrid":          ProviderSendGrid,
	"smtp.sendgrid.net": ProviderSendGrid,
	"mailjet":           ProviderMailjet,
	"in-v3.mailjet.com": ProviderMailjet,
	"smtp":              ProviderSMTP,
	"imap":              ProviderSMTP,
	"mailbox":           ProviderSMTP,
}

// normalizeProvider mirrors setup.normalize_provider: an unknown label that
// looks like a mail host is a generic mailbox, not an unknown provider.
func normalizeProvider(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if provider, ok := providerAliases[key]; ok {
		return provider, true
	}
	if strings.Contains(key, ".") && !strings.ContainsAny(key, " @") {
		return ProviderSMTP, true
	}
	return "", false
}

func authTypeFor(provider string) string {
	switch provider {
	case ProviderGoogle, ProviderHubSpot:
		return AuthOAuth
	case ProviderSendGrid, ProviderMailjet, ProviderSMTP:
		return AuthAPIKey
	}
	return ""
}

// imapHostBySMTP is the well-known inbox host for a given mail host, so a
// pasted SMTP-only config still ends up with a receiving mailbox.
var imapHostBySMTP = map[string]string{
	"smtp.gmail.com":      "imap.gmail.com",
	"smtp.office365.com":  "outlook.office365.com",
	"smtp.mail.yahoo.com": "imap.mail.yahoo.com",
	"smtp.zoho.com":       "imap.zoho.com",
	"smtp.fastmail.com":   "imap.fastmail.com",
}

// defaultIMAPHost mirrors setup.default_imap_host.
func defaultIMAPHost(smtpHost string) string {
	host := strings.ToLower(strings.TrimSpace(smtpHost))
	if known, ok := imapHostBySMTP[host]; ok {
		return known
	}
	if strings.HasPrefix(host, "smtp.") {
		return "imap." + strings.TrimPrefix(host, "smtp.")
	}
	return ""
}

// handoffURL is the hive:// deep link that opens the desktop Add-Sender form
// pre-filled (setup.handoff_url upstream). A failure to save must degrade into
// a one-click handoff rather than a dead end.
func handoffURL(provider, fromEmail, name, fromName, reason string) string {
	values := url.Values{}
	for key, value := range map[string]string{
		"provider":   provider,
		"from_email": fromEmail,
		"name":       name,
		"from_name":  fromName,
		"reason":     reason,
	} {
		if value != "" {
			values.Set(key, value)
		}
	}
	if encoded := values.Encode(); encoded != "" {
		return "hive://senders/add?" + encoded
	}
	return "hive://senders/add"
}

// SMTPConfig is the normalized mailbox credential used by SMTPMailer.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	// Security is "starttls" (default), "tls" (implicit TLS, port 465), or
	// "none" (plain connection; local fake servers and legacy relays only).
	Security string
}

// SMTPConfig reads the mailbox credential out of the sender row. It never
// guesses a password: a sender without one is a configuration error, not an
// invitation to send unauthenticated.
func (s Sender) SMTPConfig() (SMTPConfig, error) {
	cfg := SMTPConfig{
		Host:     strings.TrimSpace(secretString(s.Secret, "host")),
		Port:     secretInt(s.Secret, "port"),
		Username: strings.TrimSpace(secretString(s.Secret, "username")),
		Password: secretString(s.Secret, "password"),
		Security: strings.ToLower(strings.TrimSpace(secretString(s.Secret, "security"))),
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Security == "" {
		cfg.Security = "starttls"
	}
	if cfg.Host == "" || cfg.Username == "" || cfg.Password == "" {
		return SMTPConfig{}, fmt.Errorf("SMTP sender %q is missing host/username/password", s.Name)
	}
	return cfg, nil
}

func secretString(secret map[string]any, key string) string {
	if secret == nil {
		return ""
	}
	value, ok := secret[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func secretInt(secret map[string]any, key string) int {
	if secret == nil {
		return 0
	}
	switch value := secret[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil {
			return parsed
		}
	}
	return 0
}

// newSenderID mints a stable identifier for a locally-created sender. The
// upstream cloud owns ids; locally we only need uniqueness within one home.
func newSenderID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "snd_" + strings.Repeat("0", 12)
	}
	return "snd_" + hex.EncodeToString(buf)
}

// domainOf returns the domain part of an address, for a well-formed
// Message-ID that the sending host can legitimately claim (threads.py).
func domainOf(email string) string {
	if at := strings.LastIndex(email, "@"); at >= 0 && at+1 < len(email) {
		return email[at+1:]
	}
	return "localhost"
}
