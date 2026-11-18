package senders

// Outbound delivery. SMTP is the only provider with a local delivery path, and
// it goes through net/smtp against a real mailbox — the one sender type that
// can also receive, and therefore the only one that can hold a conversation.
//
// The Mailer seam exists so tests (and callers) can inject a fake transport.
// SMTPMailer also accepts an injectable dialer, which lets the tests point it at
// a fake SMTP server without any network access.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// FallbackPlainText matches upstream providers.send_via_smtp: an HTML body with
// no readable text alternative still ships a sane text/plain part.
const FallbackPlainText = "This message requires an HTML-capable email client."

// Outgoing is one fully-resolved message handed to a Mailer.
type Outgoing struct {
	Sender     Sender
	ToEmail    string
	Subject    string
	HTML       string
	Text       string
	MessageID  string
	InReplyTo  string
	References string
}

// MailOutcome is the provider's acceptance. ID is the handle a reply will
// quote back (the minted Message-ID for SMTP).
type MailOutcome struct {
	Provider string
	ID       string
}

// Mailer delivers one message. Errors are user-visible failure text.
type Mailer interface {
	Send(ctx context.Context, out Outgoing) (MailOutcome, error)
}

// SMTPMailer sends through a real mailbox. The zero value is usable.
type SMTPMailer struct {
	Timeout time.Duration
	// Dial is injectable for tests; nil uses net.Dialer / tls.DialWithDialer.
	Dial func(ctx context.Context, host string, port int, implicitTLS bool) (net.Conn, error)
}

func (m *SMTPMailer) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return 30 * time.Second
}

func (m *SMTPMailer) dial(ctx context.Context, host string, port int, implicitTLS bool) (net.Conn, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	if m.Dial != nil {
		return m.Dial(ctx, host, port, implicitTLS)
	}
	dialer := &net.Dialer{Timeout: m.timeout()}
	if implicitTLS {
		return tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	}
	return dialer.DialContext(ctx, "tcp", address)
}

// Send implements Mailer.
func (m *SMTPMailer) Send(ctx context.Context, out Outgoing) (MailOutcome, error) {
	cfg, err := out.Sender.SMTPConfig()
	if err != nil {
		return MailOutcome{}, err
	}
	implicitTLS := cfg.Security == "tls" || cfg.Port == 465

	conn, err := m.dial(ctx, cfg.Host, cfg.Port, implicitTLS)
	if err != nil {
		return MailOutcome{}, fmt.Errorf("Could not reach %s:%d — %v", cfg.Host, cfg.Port, err)
	}
	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	defer func() { _ = client.Close() }()

	if !implicitTLS && cfg.Security != "none" {
		if err := client.StartTLS(&tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
		}
	}

	if err := client.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
		return MailOutcome{}, authFailure(cfg, err)
	}
	if err := client.Mail(out.Sender.FromEmail); err != nil {
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	if err := client.Rcpt(out.ToEmail); err != nil {
		if code, ok := smtpErrorCode(err); ok && code >= 500 {
			return MailOutcome{}, fmt.Errorf("%s refused the recipient %s", cfg.Host, out.ToEmail)
		}
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	writer, err := client.Data()
	if err != nil {
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	if _, err := writer.Write(BuildMIME(out)); err != nil {
		_ = writer.Close()
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	if err := writer.Close(); err != nil {
		return MailOutcome{}, fmt.Errorf("SMTP error from %s: %v", cfg.Host, err)
	}
	_ = client.Quit()

	return MailOutcome{Provider: ProviderSMTP, ID: out.MessageID}, nil
}

// authFailure keeps the upstream hint: the most common cause on Gmail and
// Workspace is an account password used where an app password is required.
func authFailure(cfg SMTPConfig, err error) error {
	code, _ := smtpErrorCode(err)
	if code != 0 {
		return fmt.Errorf("%s rejected the login for %s (%d). %v", cfg.Host, cfg.Username, code, err)
	}
	return fmt.Errorf("%s rejected the login for %s. %v", cfg.Host, cfg.Username, err)
}

func smtpErrorCode(err error) (int, bool) {
	var protocolErr *textproto.Error
	if errors.As(err, &protocolErr) {
		return protocolErr.Code, true
	}
	return 0, false
}

// MintMessageID returns (<hive.TOKEN@domain>, TOKEN). The token is random and
// meaningless outside this store; the domain is the sender's own, so the
// Message-ID is well-formed for the sending host (threads.mint_message_id).
func MintMessageID(fromEmail string) (string, string) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		copy(buf, []byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	token := hex.EncodeToString(buf)
	return "<hive." + token + "@" + domainOf(fromEmail) + ">", token
}

// BuildMIME renders the multipart/alternative message. Plain text precedes
// HTML: a client that understands both picks the LAST part it can render, so
// attaching the plain part after the HTML would hide the real body.
func BuildMIME(out Outgoing) []byte {
	boundary := "=_hive_" + randomHex(12)
	plain := out.Text
	if plain == "" {
		plain = FallbackPlainText
	}

	var buffer bytes.Buffer
	writeHeader(&buffer, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeHeader(&buffer, "From", formatAddress(out.Sender.FromName, out.Sender.FromEmail))
	writeHeader(&buffer, "To", sanitizeHeader(out.ToEmail))
	writeHeader(&buffer, "Subject", encodeHeaderValue(out.Subject))
	if out.MessageID != "" {
		writeHeader(&buffer, "Message-ID", sanitizeHeader(out.MessageID))
	}
	if out.InReplyTo != "" {
		writeHeader(&buffer, "In-Reply-To", sanitizeHeader(out.InReplyTo))
	}
	if out.References != "" {
		writeHeader(&buffer, "References", sanitizeHeader(out.References))
	}
	writeHeader(&buffer, "MIME-Version", "1.0")
	writeHeader(&buffer, "Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	buffer.WriteString("\r\n")

	writeQuotedPrintablePart(&buffer, boundary, "text/plain", plain)
	writeQuotedPrintablePart(&buffer, boundary, "text/html", out.HTML)
	buffer.WriteString("--" + boundary + "--\r\n")

	return buffer.Bytes()
}

func writeHeader(buffer *bytes.Buffer, name, value string) {
	buffer.WriteString(name + ": " + value + "\r\n")
}

func writeQuotedPrintablePart(buffer *bytes.Buffer, boundary, contentType, body string) {
	buffer.WriteString("--" + boundary + "\r\n")
	buffer.WriteString("Content-Type: " + contentType + `; charset="utf-8"` + "\r\n")
	buffer.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")

	var encoded bytes.Buffer
	writer := quotedprintable.NewWriter(&encoded)
	_, _ = writer.Write([]byte(body))
	_ = writer.Close()
	buffer.Write(encoded.Bytes())
	buffer.WriteString("\r\n")
}

// sanitizeHeader strips CR/LF so a crafted subject or address cannot inject an
// extra header into the message.
func sanitizeHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", "")
	return strings.ReplaceAll(value, "\n", "")
}

// encodeHeaderValue Q-encodes non-ASCII header values (RFC 2047).
func encodeHeaderValue(value string) string {
	value = sanitizeHeader(value)
	for _, r := range value {
		if r > 127 {
			return mime.QEncoding.Encode("utf-8", value)
		}
	}
	return value
}

// formatAddress renders a From header. Only values that need quoting get it,
// so the common "Chris Voss <a@b>" form stays readable.
func formatAddress(name, email string) string {
	name = sanitizeHeader(name)
	if name == "" {
		return sanitizeHeader(email)
	}
	ascii := true
	for _, r := range name {
		if r > 127 {
			ascii = false
			break
		}
	}
	if !ascii {
		return mime.QEncoding.Encode("utf-8", name) + " <" + sanitizeHeader(email) + ">"
	}
	if strings.ContainsAny(name, `()<>[]:;@\,."`) {
		escaped := strings.ReplaceAll(name, `"`, `\"`)
		return `"` + escaped + `" <` + sanitizeHeader(email) + ">"
	}
	return name + " <" + sanitizeHeader(email) + ">"
}

func randomHex(bytesCount int) string {
	buf := make([]byte, bytesCount)
	if _, err := rand.Read(buf); err != nil {
		return strings.Repeat("0", bytesCount*2)
	}
	return hex.EncodeToString(buf)
}
