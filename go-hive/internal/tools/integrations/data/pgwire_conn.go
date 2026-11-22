// PostgreSQL wire connection lifecycle: dial/TLS negotiation, startup,
// authentication (cleartext, MD5, SCRAM-SHA-256) and message framing
// (split from pgwire.go; DSN parsing and types stay there).

package data

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

// pgConnect opens, negotiates TLS when requested, authenticates and waits
// for ReadyForQuery. The caller owns the returned connection.
func pgConnect(ctx context.Context, dsn string) (*pgConn, error) {
	cfg, err := pgParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	raw, err := pgDial(ctx, cfg)
	if err != nil {
		return nil, err
	}
	conn := &pgConn{
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

func pgDial(ctx context.Context, cfg pgConfig) (net.Conn, error) {
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
	binary.BigEndian.PutUint32(request[4:8], pgSSLRequestCode)
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
func (c *pgConn) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

// startup sends StartupMessage and processes authentication until
// ReadyForQuery.
func (c *pgConn) startup(ctx context.Context) error {
	var payload bytes.Buffer
	pgAppendInt32(&payload, pgProtocolVersion)
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
			if key, value, ok := pgParseParameterStatus(data); ok {
				c.params[key] = value
			}
		case 'K':
			// BackendKeyData: not needed without cancel support.
		case 'N', 'A':
			// NoticeResponse / NotificationResponse.
		case 'E':
			return pgParseError(data)
		case 'Z':
			return nil
		default:
			return fmt.Errorf("postgres: unexpected startup message %q", string(typ))
		}
	}
}

func pgParseParameterStatus(data []byte) (string, string, bool) {
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

func (c *pgConn) handleAuth(ctx context.Context, data []byte) error {
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

func (c *pgConn) sendPassword(ctx context.Context, password string) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}
	payload := append([]byte(password), 0)
	if err := pgWriteMessage(c.w, 'p', payload); err != nil {
		return err
	}
	return c.w.Flush()
}

func (c *pgConn) scramStart(ctx context.Context, data []byte) error {
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
	bare := "n=" + pgScramEscape(c.cfg.User) + ",r=" + c.scramClientNonce
	c.scramClientFirst = bare
	initial := "n,," + bare
	var payload bytes.Buffer
	payload.WriteString("SCRAM-SHA-256")
	payload.WriteByte(0)
	pgAppendInt32(&payload, len(initial))
	payload.WriteString(initial)
	return c.writeMessageFlush(ctx, 'p', payload.Bytes())
}

func (c *pgConn) scramContinue(ctx context.Context, data []byte) error {
	serverFirst := string(data)
	attrs := pgScramAttributes(serverFirst)
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
	salted := pbkdf2SHA256([]byte(c.cfg.Password), salt, iterations, sha256.Size)
	clientKey := scramHMAC(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	withoutProof := "c=biws,r=" + serverNonce
	authMessage := c.scramClientFirst + "," + serverFirst + "," + withoutProof
	clientSignature := scramHMAC(storedKey[:], []byte(authMessage))
	proof := make([]byte, len(clientKey))
	for index := range clientKey {
		proof[index] = clientKey[index] ^ clientSignature[index]
	}
	serverKey := scramHMAC(salted, []byte("Server Key"))
	serverSignature := scramHMAC(serverKey, []byte(authMessage))
	c.scramServerFirst = serverFirst
	c.scramServerSigB64 = base64.StdEncoding.EncodeToString(serverSignature)
	final := withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	return c.writeMessageFlush(ctx, 'p', []byte(final))
}

func (c *pgConn) scramFinal(data []byte) error {
	attrs := pgScramAttributes(string(data))
	if signature := attrs["v"]; signature != "" && c.scramServerSigB64 != "" && signature != c.scramServerSigB64 {
		return errors.New("postgres: SCRAM server signature mismatch")
	}
	return nil
}

// pgScramAttributes parses comma-separated SCRAM attributes.
func pgScramAttributes(message string) map[string]string {
	attrs := map[string]string{}
	for _, part := range strings.Split(message, ",") {
		if index := strings.IndexByte(part, '='); index > 0 {
			attrs[part[:index]] = part[index+1:]
		}
	}
	return attrs
}

// pgScramEscape encodes '=' and ',' in SCRAM usernames.
func pgScramEscape(value string) string {
	value = strings.ReplaceAll(value, "=", "=3D")
	return strings.ReplaceAll(value, ",", "=2C")
}

// pbkdf2SHA256 is PBKDF2-HMAC-SHA256 (RFC 8018) without a dependency.
func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
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

func scramHMAC(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}

// --------------------------------------------------------------- messages ---

// setReadDeadline applies the context deadline or the default read timeout.
func (c *pgConn) setReadDeadline(ctx context.Context) {
	deadline := time.Now().Add(pgReadTimeout)
	if ctx != nil {
		if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
			deadline = ctxDeadline
		}
	}
	_ = c.conn.SetReadDeadline(deadline)
}

func (c *pgConn) setWriteDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(pgDialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	return c.conn.SetWriteDeadline(deadline)
}

// writeRaw writes an untyped startup-style message (int32 length + payload).
func (c *pgConn) writeRaw(payload []byte) error {
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

// pgWriteMessage writes one typed frontend message.
func pgWriteMessage(w *bufio.Writer, typ byte, payload []byte) error {
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

func (c *pgConn) writeMessageFlush(ctx context.Context, typ byte, payload []byte) error {
	if err := c.setWriteDeadline(ctx); err != nil {
		return err
	}
	if err := pgWriteMessage(c.w, typ, payload); err != nil {
		return err
	}
	return c.w.Flush()
}

// readMessage reads one typed backend message.
func (c *pgConn) readMessage(ctx context.Context) (byte, []byte, error) {
	c.setReadDeadline(ctx)
	var header [5]byte
	if _, err := io.ReadFull(c.r, header[:]); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 4 || length > pgMaxMessage {
		return 0, nil, fmt.Errorf("postgres: invalid message length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func pgParseError(payload []byte) *pgError {
	pgErr := &pgError{}
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
