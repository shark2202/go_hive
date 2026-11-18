package senders

// The Go side of delivery has no upstream pytest counterpart: Python uses
// smtplib behind a 30s timeout. These tests run net/smtp against a fake SMTP
// server on loopback, so the real protocol path is exercised without network
// access or credentials.

import (
	"bufio"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal ESMTP server: EHLO, AUTH PLAIN, MAIL, RCPT, DATA, QUIT.
// It records the raw DATA payload per message.
type fakeSMTP struct {
	listener net.Listener
	messages chan string

	rejectAuth bool
	rejectRcpt bool

	mu   sync.Mutex
	addr string
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &fakeSMTP{
		listener: listener,
		messages: make(chan string, 8),
		addr:     listener.Addr().String(),
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (f *fakeSMTP) hostPort(t *testing.T) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(f.addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port %q: %v", portText, err)
	}
	return host, port
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	send := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}
	send("220 fake ESMTP ready")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(command)
		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			send("250-fake")
			send("250-AUTH PLAIN")
			send("250 OK")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			if f.rejectAuth {
				send("535 5.7.8 Authentication credentials invalid")
				continue
			}
			send("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM"), strings.HasPrefix(upper, "RSET"):
			send("250 OK")
		case strings.HasPrefix(upper, "RCPT TO"):
			if f.rejectRcpt {
				send("550 5.1.1 No such user here")
				continue
			}
			send("250 OK")
		case upper == "DATA":
			send("354 End data with <CR><LF>.<CR><LF>")
			var payload strings.Builder
			for {
				dataLine, readErr := reader.ReadString('\n')
				if readErr != nil {
					return
				}
				if dataLine == ".\r\n" || dataLine == ".\n" {
					break
				}
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				payload.WriteString(dataLine)
			}
			f.messages <- payload.String()
			send("250 OK queued")
		case upper == "QUIT":
			send("221 Bye")
			return
		default:
			send("250 OK")
		}
	}
}

func (f *fakeSMTP) sender(t *testing.T) Sender {
	t.Helper()
	host, port := f.hostPort(t)
	return Sender{
		ID:         "snd_test",
		Name:       "Test mailbox",
		Provider:   ProviderSMTP,
		AuthType:   AuthAPIKey,
		FromEmail:  "sender@example.com",
		FromName:   "Chris Voss",
		CanReceive: true,
		Secret: map[string]any{
			"host":     host,
			"port":     port,
			"username": "sender@example.com",
			"password": "app-password",
			"security": "none",
		},
	}
}

// Parity-exempt: Python sends through smtplib; the Go port has no upstream test
// for net/smtp. A fake SMTP server covers the real protocol dialogue.
func TestSMTPMailerSendsMultipartAlternative(t *testing.T) {
	server := newFakeSMTP(t)
	mailer := &SMTPMailer{Timeout: 5 * time.Second}
	messageID, token := MintMessageID("sender@example.com")

	outcome, err := mailer.Send(context.Background(), Outgoing{
		Sender:    server.sender(t),
		ToEmail:   "sarah@example.com",
		Subject:   "Worth a chat?",
		HTML:      "<p>Hi Sarah,</p><p>We ship fast.</p>",
		Text:      "Hi Sarah,\n\nWe ship fast.",
		MessageID: messageID,
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if outcome.Provider != ProviderSMTP || outcome.ID != messageID {
		t.Fatalf("outcome = %+v, want smtp/%s", outcome, messageID)
	}
	if token == "" || !strings.Contains(messageID, token) {
		t.Fatalf("message id %q does not carry token %q", messageID, token)
	}

	var raw string
	select {
	case raw = <-server.messages:
	case <-time.After(5 * time.Second):
		t.Fatal("fake SMTP server received no message")
	}

	parsed, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse message: %v\n%s", err, raw)
	}
	if got := parsed.Header.Get("To"); got != "sarah@example.com" {
		t.Fatalf("To = %q", got)
	}
	if got := parsed.Header.Get("Subject"); got != "Worth a chat?" {
		t.Fatalf("Subject = %q", got)
	}
	if got := parsed.Header.Get("Message-ID"); got != messageID {
		t.Fatalf("Message-ID = %q, want %q", got, messageID)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("Content-Type = %q (%v)", parsed.Header.Get("Content-Type"), err)
	}
	body, err := io.ReadAll(parsed.Body)
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
	order := []string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next part: %v", err)
		}
		order = append(order, part.Header.Get("Content-Type"))
	}
	if len(order) != 2 || !strings.HasPrefix(order[0], "text/plain") || !strings.HasPrefix(order[1], "text/html") {
		t.Fatalf("parts = %#v, want text/plain before text/html", order)
	}
	if !strings.Contains(raw, "Hi Sarah,") {
		t.Fatalf("plain part missing from message:\n%s", raw)
	}
}

// Parity-exempt: upstream maps smtplib.SMTPAuthenticationError to a diagnostic;
// this asserts the Go error mapping against a rejecting fake server.
func TestSMTPMailerReportsRejectedLogin(t *testing.T) {
	server := newFakeSMTP(t)
	server.rejectAuth = true
	mailer := &SMTPMailer{Timeout: 5 * time.Second}

	_, err := mailer.Send(context.Background(), Outgoing{
		Sender:  server.sender(t),
		ToEmail: "sarah@example.com",
		Subject: "Hi",
		HTML:    "<p>Hi</p>",
		Text:    "Hi",
	})
	if err == nil || !strings.Contains(err.Error(), "rejected the login") {
		t.Fatalf("error = %v, want a rejected-login diagnostic", err)
	}
}

// Parity-exempt: upstream maps smtplib.SMTPRecipientsRefused; this pins the Go
// equivalent for a 550 on RCPT TO.
func TestSMTPMailerReportsRefusedRecipient(t *testing.T) {
	server := newFakeSMTP(t)
	server.rejectRcpt = true
	mailer := &SMTPMailer{Timeout: 5 * time.Second}

	_, err := mailer.Send(context.Background(), Outgoing{
		Sender:  server.sender(t),
		ToEmail: "nobody@example.com",
		Subject: "Hi",
		HTML:    "<p>Hi</p>",
		Text:    "Hi",
	})
	if err == nil || !strings.Contains(err.Error(), "refused the recipient") {
		t.Fatalf("error = %v, want a refused-recipient diagnostic", err)
	}
}

// Parity-exempt: upstream maps OSError to "Could not reach host:port"; this pins
// the same failure against a closed port.
func TestSMTPMailerReportsUnreachableHost(t *testing.T) {
	sender := Sender{
		ID:        "snd_test",
		Name:      "Dead mailbox",
		Provider:  ProviderSMTP,
		FromEmail: "sender@example.com",
		Secret: map[string]any{
			"host":     "127.0.0.1",
			"port":     1,
			"username": "sender@example.com",
			"password": "app-password",
			"security": "none",
		},
	}
	mailer := &SMTPMailer{Timeout: 300 * time.Millisecond}
	_, err := mailer.Send(context.Background(), Outgoing{
		Sender:  sender,
		ToEmail: "sarah@example.com",
		Subject: "Hi",
		HTML:    "<p>Hi</p>",
		Text:    "Hi",
	})
	if err == nil || !strings.Contains(err.Error(), "Could not reach") {
		t.Fatalf("error = %v, want an unreachable-host diagnostic", err)
	}
}

// Parity-exempt: SMTPConfig has no Python counterpart (the secret is a dict
// there); this pins the "no host/username/password means no send" contract.
func TestSMTPMailerRequiresCompleteCredential(t *testing.T) {
	mailer := &SMTPMailer{}
	_, err := mailer.Send(context.Background(), Outgoing{
		Sender:  Sender{Name: "Incomplete", Provider: ProviderSMTP},
		ToEmail: "sarah@example.com",
		Subject: "Hi",
		HTML:    "<p>Hi</p>",
	})
	if err == nil || !strings.Contains(err.Error(), "missing host/username/password") {
		t.Fatalf("error = %v, want a missing-credential error", err)
	}
}

// Parity-exempt: MIME rendering is stdlib-side in Python (email.message), so
// there is no upstream unit test; these assertions pin the wire format.
func TestBuildMIMENormalizesHeadersAndEncoding(t *testing.T) {
	raw := string(BuildMIME(Outgoing{
		Sender:    Sender{FromEmail: "sender@example.com", FromName: "Chris Voss"},
		ToEmail:   "sarah@example.com",
		Subject:   "Héllo\r\nBcc: evil@example.com",
		HTML:      "<p>Héllo</p>",
		Text:      "Héllo",
		MessageID: "<hive.abc@example.com>",
	}))
	if strings.Contains(raw, "\r\nBcc:") {
		t.Fatalf("header injection escaped into the message:\n%s", raw)
	}
	if !strings.Contains(raw, "Subject: =?utf-8?") {
		t.Fatalf("non-ASCII subject was not RFC 2047 encoded:\n%s", raw)
	}
	if !strings.Contains(raw, "From: Chris Voss <sender@example.com>") {
		t.Fatalf("From header = %q", raw)
	}
	if !strings.Contains(raw, "Message-ID: <hive.abc@example.com>") {
		t.Fatalf("Message-ID missing:\n%s", raw)
	}
}
