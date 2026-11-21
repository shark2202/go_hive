// PostgreSQL wire connection lifecycle: dial/TLS negotiation, startup,
// authentication (cleartext, MD5, SCRAM-SHA-256) and message framing
// (split from pgwire.go; DSN parsing and types stay there).

package integrations

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ------------------------------------------------------------ connection ---

// batch5PGConnect opens, negotiates TLS when requested, authenticates and waits
// for ReadyForQuery. The caller owns the returned connection.
func batch5PGConnect(ctx context.Context, dsn string) (*batch5PGConn, error) {
	cfg, err := batch5PGParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	raw, err := batch5PGDial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	conn := &batch5PGConn{
		conn:   raw,
		r:      bufio.NewReader(raw),
		w:      bufio.NewWriter(raw),
		cfg:    cfg,
		params: map[string]string{},
	}
	if err := conn.startup(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func batch5PGDial(ctx context.Context, cfg batch5PGConfig) (net.Conn, error) {
	dialer := net.Dialer{Timeout: cfg.ConnectTimeout}
	var (
		conn net.Conn
		err  error
	)
	if strings.HasPrefix(cfg.Host, "/") {
		socket := filepath.Join(cfg.Host, ".s.PGSQL."+cfg.Port)
		conn, err = dialer.DialContext(ctx, "unix", socket)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, cfg.Port))
	}
	if err != nil {
		return nil, err
	}
	switch cfg.SSLMode {
	case "disable", "allow":
		return conn, nil
	case "prefer", "require", "verify-ca", "verify-full":
	default:
		conn.Close()
		return nil, fmt.Errorf("unsupported sslmode %q", cfg.SSLMode)
	}
	// SSLRequest: length 8, code 80877103.
	request := make([]byte, 8)
	binary.BigEndian.PutUint32(request[0:4], 8)
	binary.BigEndian.PutUint32(request[4:8], batch5PGSSLRequestCode)
	if _, err := conn.Write(request); err != nil {
		conn.Close()
		return nil, err
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		conn.Close()
		return nil, err
	}
	if reply[0] != 'S' {
		if cfg.SSLMode == "prefer" {
			return conn, nil
		}
		conn.Close()
		return nil, errors.New("postgres: server does not support SSL but sslmode requires it")
	}
	verify := cfg.SSLMode == "verify-ca" || cfg.SSLMode == "verify-full"
	tlsConn := tls.Client(conn, &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: !verify, //nolint:gosec // matches libpq sslmode=prefer/require
		MinVersion:         tls.VersionTLS12,
	})
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, err
	}
	return tlsConn, nil
}

// Close releases the connection.
func (c *batch5PGConn) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

// startup sends StartupMessage and processes authentication until
// ReadyForQuery.
func (c *batch5PGConn) startup(ctx context.Context) error {
	var payload bytes.Buffer
	batch5PGAppendInt32(&payload, batch5PGProtocolVersion)
	for _, pair := range [][2]string{
		{"user", c.cfg.User},
		{"database", c.cfg.Database},
		{"client_encoding", "UTF8"},
		{"application_name", "hive-go"},
	} {
		payload.WriteString(pair[0])
		payload.WriteByte(0)
		payload.WriteString(pair[1])
		payload.WriteByte(0)
	}
	payload.WriteByte(0)
	if err := c.writeRaw(payload.Bytes()); err != nil {
		return err
	}
	for {
		typ, data, err := c.readMessage(ctx)
		if err != nil {
			return err
		}
		switch typ {
		case 'R':
			if err := c.handleAuth(ctx, data); err != nil {
				return err
			}
		case 'S':
			if key, value, ok := batch5PGParseParameterStatus(data); ok {
				c.params[key] = value
			}
		case 'K':
			// BackendKeyData: not needed without cancel support.
		case 'N', 'A':
			// NoticeResponse / NotificationResponse.
		case 'E':
			return batch5PGParseError(data)
		case 'Z':
			return nil
		default:
			return fmt.Errorf("postgres: unexpected startup message %q", string(typ))
		}
	}
}

func batch5PGParseParameterStatus(data []byte) (string, string, bool) {
	first := bytes.IndexByte(data, 0)
	if first < 0 {
		return "", "", false
	}
	rest := data[first+1:]
	second := bytes.IndexByte(rest, 0)
	if second < 0 {
		return "", "", false
	}
	return string(data[:first]), string(rest[:second]), true
}

// ----------------------------------------------------------- auth methods ---

func (c *batch5PGConn) handleAuth(ctx context.Context, data []byte) error {
	if len(data) < 4 {
		return errors.New("postgres: short authentication message")
	}
	code := int(binary.BigEndian.Uint32(data[:4]))
	switch code {
	case 0: // AuthenticationOk
		return nil
	case 3: // cleartext password
		return c.sendPassword(ctx, c.cfg.Password)
	case 5: // MD5
		if len(data) < 8 {
			return errors.New("postgres: short MD5 challenge")
		}
		salt := data[4:8]
		inner := md5.Sum([]byte(c.cfg.Password + c.cfg.User))
		outer := md5.Sum([]byte(hex.EncodeToString(inner[:]) + string(salt)))
		return c.sendPassword(ctx, "md5"+hex.EncodeToString(outer[:]))
	case 10: // SASL mechanisms
		return c.scramStart(ctx, data[4:])
	case 11: // SASL continue
		return c.scramContinue(ctx, data[4:])
	case 12: // SASL final
		return c.scramFinal(data[4:])
	default:
		return fmt.Errorf("postgres: unsupported authentication method %d", code)
	}
}

func (c *batch5PGConn) sendPassword(ctx context.Context, password string) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}
	payload := append([]byte(password), 0)
	if err := batch5PGWriteMessage(c.w, 'p', payload); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *batch5PGConn) scramStart(ctx context.Context, data []byte) error {
	mechanisms := map[string]bool{}
	for len(data) > 0 {
		end := bytes.IndexByte(data, 0)
		if end < 0 {
			break
		}
		mechanisms[string(data[:end])] = true
		data = data[end+1:]
	}
	if !mechanisms["SCRAM-SHA-256"] {
		return errors.New("postgres: server requires an unsupported SASL mechanism")
	}
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("postgres: cannot generate SCRAM nonce: %w", err)
	}
	c.scramClientNonce = hex.EncodeToString(nonceBytes)
	bare := "n=" + batch5PGSCRAMEscape(c.cfg.User) + ",r=" + c.scramClientNonce
	c.scramClientFirst = bare
	initial := "n,," + bare
	var payload bytes.Buffer
	payload.WriteString("SCRAM-SHA-256")
	payload.WriteByte(0)
	batch5PGAppendInt32(&payload, len(initial))
	payload.WriteString(initial)
	return c.writeMessageFlush(ctx, 'p', payload.Bytes())
}

func (c *batch5PGConn) scramContinue(ctx context.Context, data []byte) error {
	serverFirst := string(data)
	attrs := batch5PGSCRAMAttributes(serverFirst)
	serverNonce := attrs["r"]
	if !strings.HasPrefix(serverNonce, c.scramClientNonce) {
		return errors.New("postgres: SCRAM server nonce does not extend the client nonce")
	}
	salt, err := base64.StdEncoding.DecodeString(attrs["s"])
	if err != nil {
		return fmt.Errorf("postgres: invalid SCRAM salt: %w", err)
	}
	iterations, err := strconv.Atoi(attrs["i"])
	if err != nil || iterations <= 0 {
		return errors.New("postgres: invalid SCRAM iteration count")
	}
	salted := batch5PBKDF2SHA256([]byte(c.cfg.Password), salt, iterations, sha256.Size)
	clientKey := batch5HMAC(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	withoutProof := "c=biws,r=" + serverNonce
	authMessage := c.scramClientFirst + "," + serverFirst + "," + withoutProof
	clientSignature := batch5HMAC(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for index := range clientKey {
		proof[index] = clientKey[index] ^ clientSignature[index]
	}
	serverKey := batch5HMAC(salted, []byte("Server Key"))
	serverSignature := batch5HMAC(serverKey, []byte(authMessage))
	c.scramServerFirst = serverFirst
	c.scramServerSigB64 = base64.StdEncoding.EncodeToString(serverSignature)
	final := withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	return c.writeMessageFlush(ctx, 'p', []byte(final))
}

func (c *batch5PGConn) scramFinal(data []byte) error {
	attrs := batch5PGSCRAMAttributes(string(data))
	if signature := attrs["v"]; signature != "" && c.scramServerSigB64 != "" && signature != c.scramServerSigB64 {
		return errors.New("postgres: SCRAM server signature mismatch")
	}
	return nil
}

// batch5PGSCRAMAttributes parses comma-separated SCRAM attributes.
func batch5PGSCRAMAttributes(message string) map[string]string {
	attrs := map[string]string{}
	for _, part := range strings.Split(message, ",") {
		if index := strings.IndexByte(part, '='); index > 0 {
			attrs[part[:index]] = part[index+1:]
		}
	}
	return attrs
}

// batch5PGSCRAMEscape encodes '=' and ',' in SCRAM usernames.
func batch5PGSCRAMEscape(value string) string {
	value = strings.ReplaceAll(value, "=", "=3D")
	return strings.ReplaceAll(value, ",", "=2C")
}

// batch5PBKDF2SHA256 is PBKDF2-HMAC-SHA256 (RFC 8018) without a dependency.
func batch5PBKDF2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	var counter [4]byte
	for block := 1; block <= blocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(counter[:], uint32(block))
		prf.Write(counter[:])
		previous := prf.Sum(nil)
		accumulated := make([]byte, len(previous))
		copy(accumulated, previous)
		for round := 1; round < iterations; round++ {
			prf.Reset()
			prf.Write(previous)
			previous = prf.Sum(nil)
			for index := range accumulated {
				accumulated[index] ^= previous[index]
			}
		}
		out = append(out, accumulated...)
	}
	return out[:keyLen]
}

func batch5HMAC(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// --------------------------------------------------------------- messages ---

// setReadDeadline applies the context deadline or the default read timeout.
func (c *batch5PGConn) setReadDeadline(ctx context.Context) {
	deadline := time.Now().Add(batch5PGReadTimeout)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	_ = c.conn.SetReadDeadline(deadline)
}

func (c *batch5PGConn) setWriteDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(batch5PGDialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	return c.conn.SetWriteDeadline(deadline)
}

// writeRaw writes an untyped startup-style message (int32 length + payload).
func (c *batch5PGConn) writeRaw(payload []byte) error {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(payload)+4))
	if _, err := c.w.Write(header); err != nil {
		return err
	}
	if _, err := c.w.Write(payload); err != nil {
		return err
	}
	return c.w.Flush()
}

// batch5PGWriteMessage writes one typed frontend message.
func batch5PGWriteMessage(w *bufio.Writer, typ byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = typ
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)+4))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (c *batch5PGConn) writeMessageFlush(ctx context.Context, typ byte, payload []byte) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}
	if err := batch5PGWriteMessage(c.w, typ, payload); err != nil {
		return err
	}
	return c.w.Flush()
}

// readMessage reads one typed backend message.
func (c *batch5PGConn) readMessage(ctx context.Context) (byte, []byte, error) {
	c.setReadDeadline(ctx)
	var header [5]byte
	if _, err := io.ReadFull(c.r, header[:]); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 4 || length > batch5PGMaxMessage {
		return 0, nil, fmt.Errorf("postgres: invalid message length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func batch5PGParseError(payload []byte) *batch5PGError {
	pgErr := &batch5PGError{}
	for len(payload) > 0 && payload[0] != 0 {
		code := payload[0]
		payload = payload[1:]
		end := bytes.IndexByte(payload, 0)
		if end < 0 {
			break
		}
		value := string(payload[:end])
		payload = payload[end+1:]
		switch code {
		case 'S':
			pgErr.Severity = value
		case 'C':
			pgErr.Code = value
		case 'M':
			pgErr.Message = value
		}
	}
	if pgErr.Message == "" {
		pgErr.Message = "postgres: server error"
	}
	return pgErr
}
