// Minimal PostgreSQL v3 frontend/backend protocol client.
//
// The repository forbids new dependencies, so this file speaks the wire
// protocol directly on a TCP (or unix) connection. It supports the subset the
// read-only postgres tool needs:
//
//   - startup and parameter negotiation
//   - AuthenticationOk, cleartext, MD5 and SCRAM-SHA-256 (RFC 5802/7677)
//   - simple queries and the extended protocol (Parse/Bind/Describe/Execute)
//     with text-format parameters
//   - RowDescription/DataRow decoding with type-aware value conversion
//   - ErrorResponse surfacing including SQLSTATE
//
// Out of scope (returned as explicit errors, never silently ignored):
// Kerberos, GSSAPI/SSPI, SCM credentials, binary result formats, COPY,
// notifications and connection pooling.
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	batch5PGProtocolVersion = 196608   // 3.0
	batch5PGSSLRequestCode  = 80877103 // SSLRequest
	batch5PGMaxMessage      = 64 << 20 // hard cap on a single backend message
	batch5PGDialTimeout     = 10 * time.Second
	batch5PGReadTimeout     = 30 * time.Second
)

// batch5PGConfig is a parsed libpq-style connection string.
type batch5PGConfig struct {
	Host           string
	Port           string
	User           string
	Password       string
	Database       string
	SSLMode        string
	ConnectTimeout time.Duration
}

// batch5PGError is an ErrorResponse from the server.
type batch5PGError struct {
	Severity string
	Code     string
	Message  string
}

func (e *batch5PGError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s (SQLSTATE %s)", e.Message, e.Code)
	case e.Message != "":
		return e.Message
	default:
		return "postgres: server error"
	}
}

// batch5PGIsQueryCanceled reports statement_timeout cancellations.
func batch5PGIsQueryCanceled(err error) bool {
	var pgErr *batch5PGError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57014"
	}
	return false
}

// batch5PGField is one RowDescription field.
type batch5PGField struct {
	Name    string
	TypeOID uint32
}

// batch5PGResult is a decoded result set.
type batch5PGResult struct {
	Columns []string
	Rows    [][]any
}

// batch5PGConn is one live connection.
type batch5PGConn struct {
	conn   net.Conn
	r      *bufio.Reader
	w      *bufio.Writer
	cfg    batch5PGConfig
	params map[string]string

	scramClientNonce  string
	scramClientFirst  string
	scramServerFirst  string
	scramServerSigB64 string
}

// ------------------------------------------------------------ DSN parsing ---

// batch5PGParseDSN accepts URL form (postgres://..., postgresql://...) and
// libpq keyword/value form (host=... port=... dbname=...).
func batch5PGParseDSN(dsn string) (batch5PGConfig, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return batch5PGConfig{}, errors.New("empty PostgreSQL connection string")
	}
	cfg := batch5PGConfig{SSLMode: "prefer", ConnectTimeout: batch5PGDialTimeout}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return cfg, fmt.Errorf("invalid PostgreSQL URL: %w", err)
		}
		cfg.Host = parsed.Hostname()
		cfg.Port = parsed.Port()
		if parsed.User != nil {
			cfg.User = parsed.User.Username()
			if password, present := parsed.User.Password(); present {
				cfg.Password = password
			}
		}
		cfg.Database = strings.TrimPrefix(parsed.Path, "/")
		query := parsed.Query()
		cfg.applyParams(func(key string) string { return query.Get(key) })
	} else {
		fields, err := batch5PGSplitDSNFields(dsn)
		if err != nil {
			return cfg, err
		}
		cfg.applyParams(func(key string) string { return fields[key] })
	}
	cfg.applyDefaults()
	return cfg, nil
}

// applyParams overlays named connection parameters (URL query or keyword form).
func (c *batch5PGConfig) applyParams(get func(string) string) {
	if value := strings.TrimSpace(get("hostaddr")); value != "" {
		c.Host = value
	}
	if value := strings.TrimSpace(get("host")); value != "" {
		c.Host = value
	}
	if value := strings.TrimSpace(get("port")); value != "" {
		c.Port = value
	}
	if value := strings.TrimSpace(get("user")); value != "" {
		c.User = value
	}
	if value := get("password"); value != "" {
		c.Password = value
	}
	if value := strings.TrimSpace(get("dbname")); value != "" {
		c.Database = value
	}
	if value := strings.TrimSpace(get("database")); value != "" {
		c.Database = value
	}
	if value := strings.TrimSpace(get("sslmode")); value != "" {
		c.SSLMode = strings.ToLower(value)
	}
	if value := strings.TrimSpace(get("connect_timeout")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			c.ConnectTimeout = time.Duration(seconds) * time.Second
		}
	}
}

func (c *batch5PGConfig) applyDefaults() {
	if c.Host == "" {
		c.Host = lane2FirstNonEmpty(os.Getenv("PGHOST"), "localhost")
	}
	if c.Port == "" {
		c.Port = lane2FirstNonEmpty(os.Getenv("PGPORT"), "5432")
	}
	if c.User == "" {
		c.User = lane2FirstNonEmpty(os.Getenv("PGUSER"), os.Getenv("USER"), "postgres")
	}
	if c.Password == "" {
		c.Password = os.Getenv("PGPASSWORD")
	}
	if c.Database == "" {
		c.Database = lane2FirstNonEmpty(os.Getenv("PGDATABASE"), c.User)
	}
	if c.SSLMode == "" {
		c.SSLMode = "prefer"
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = batch5PGDialTimeout
	}
}

func lane2FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// batch5PGSplitDSNFields tokenizes libpq keyword=value strings, honouring
// single-quoted values with backslash escapes.
func batch5PGSplitDSNFields(dsn string) (map[string]string, error) {
	fields := map[string]string{}
	index := 0
	for index < len(dsn) {
		for index < len(dsn) && (dsn[index] == ' ' || dsn[index] == '\t' || dsn[index] == '\n') {
			index++
		}
		if index >= len(dsn) {
			break
		}
		start := index
		for index < len(dsn) && dsn[index] != '=' && dsn[index] != ' ' && dsn[index] != '\t' {
			index++
		}
		key := strings.TrimSpace(dsn[start:index])
		if index >= len(dsn) || dsn[index] != '=' {
			return nil, fmt.Errorf("invalid connection string near %q", key)
		}
		index++ // consume '='
		var value strings.Builder
		if index < len(dsn) && dsn[index] == '\'' {
			index++
			closed := false
			for index < len(dsn) {
				ch := dsn[index]
				if ch == '\\' && index+1 < len(dsn) {
					value.WriteByte(dsn[index+1])
					index += 2
					continue
				}
				if ch == '\'' {
					index++
					closed = true
					break
				}
				value.WriteByte(ch)
				index++
			}
			if !closed {
				return nil, errors.New("unterminated quoted value in connection string")
			}
		} else {
			for index < len(dsn) && dsn[index] != ' ' && dsn[index] != '\t' && dsn[index] != '\n' {
				value.WriteByte(dsn[index])
				index++
			}
		}
		if key != "" {
			fields[key] = value.String()
		}
	}
	return fields, nil
}

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

// ---------------------------------------------------------------- queries ---

// SimpleExec runs a statement and discards any rows.
func (c *batch5PGConn) SimpleExec(ctx context.Context, sql string) error {
	_, err := c.simpleQuery(ctx, sql, 0)
	return err
}

// Query runs either the simple or extended protocol depending on params.
func (c *batch5PGConn) Query(ctx context.Context, sql string, params map[string]any, maxRows int) (*batch5PGResult, error) {
	if len(params) > 0 || batch5PGPlaceholderPattern.MatchString(sql) {
		return c.extendedQuery(ctx, sql, params, maxRows)
	}
	return c.simpleQuery(ctx, sql, maxRows)
}

func (c *batch5PGConn) simpleQuery(ctx context.Context, sql string, maxRows int) (*batch5PGResult, error) {
	if err := c.writeMessageFlush(ctx, 'Q', append([]byte(sql), 0)); err != nil {
		return nil, err
	}
	result := &batch5PGResult{}
	var fields []batch5PGField
	for {
		typ, data, err := c.readMessage(ctx)
		if err != nil {
			return nil, err
		}
		switch typ {
		case 'T':
			fields, err = batch5PGParseRowDescription(data)
			if err != nil {
				return nil, err
			}
			result.Columns = make([]string, len(fields))
			for index, field := range fields {
				result.Columns[index] = field.Name
			}
			result.Rows = nil
		case 'D':
			row, err := batch5PGParseDataRow(fields, data)
			if err != nil {
				return nil, err
			}
			if maxRows <= 0 || len(result.Rows) < maxRows {
				result.Rows = append(result.Rows, row)
			}
		case 'C', 'I', 'N', 'A', 'S':
			// CommandComplete / EmptyQueryResponse / notices.
		case 'E':
			return nil, batch5PGParseError(data)
		case 'Z':
			return result, nil
		default:
			return nil, fmt.Errorf("postgres: unexpected query message %q", string(typ))
		}
	}
}

var batch5PGPlaceholderPattern = regexp.MustCompile(`%\(([A-Za-z_][A-Za-z0-9_]*)\)s`)

// batch5PGConvertPlaceholders rewrites psycopg2-style %(name)s placeholders to
// positional $N markers and returns the bound text values.
func batch5PGConvertPlaceholders(sql string, params map[string]any) (string, [][]byte, error) {
	var (
		args     [][]byte
		index    = map[string]int{}
		firstErr error
	)
	converted := batch5PGPlaceholderPattern.ReplaceAllStringFunc(sql, func(match string) string {
		name := match[2 : len(match)-2]
		if position, present := index[name]; present {
			return "$" + strconv.Itoa(position)
		}
		value, present := params[name]
		if !present {
			if firstErr == nil {
				firstErr = fmt.Errorf("missing query parameter: %s", name)
			}
			return match
		}
		text, err := batch5PGParamText(value)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return match
		}
		args = append(args, text)
		index[name] = len(args)
		return "$" + strconv.Itoa(len(args))
	})
	if firstErr != nil {
		return "", nil, firstErr
	}
	if len(args) == 0 && len(params) > 0 {
		return "", nil, errors.New("query has no %(name)s placeholders for the supplied params")
	}
	return converted, args, nil
}

// batch5PGParamText renders a scalar parameter as text format. Composite
// values must be supplied as PostgreSQL literal strings by the caller.
func batch5PGParamText(value any) ([]byte, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []byte(typed), nil
	case []byte:
		return typed, nil
	case bool:
		if typed {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case int:
		return []byte(strconv.Itoa(typed)), nil
	case int64:
		return []byte(strconv.FormatInt(typed, 10)), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return []byte(strconv.FormatFloat(typed, 'g', -1, 64)), nil
		}
		if typed == math.Trunc(typed) && math.Abs(typed) < 1<<53 {
			return []byte(strconv.FormatInt(int64(typed), 10)), nil
		}
		return []byte(strconv.FormatFloat(typed, 'g', -1, 64)), nil
	case json.Number:
		return []byte(typed.String()), nil
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("unsupported query parameter type %T", value)
		}
		return raw, nil
	}
}

func (c *batch5PGConn) extendedQuery(ctx context.Context, sql string, params map[string]any, maxRows int) (*batch5PGResult, error) {
	converted, args, err := batch5PGConvertPlaceholders(sql, params)
	if err != nil {
		return nil, err
	}
	// Parse (unnamed statement, unspecified parameter types).
	var parse bytes.Buffer
	parse.WriteByte(0)
	parse.WriteString(converted)
	parse.WriteByte(0)
	batch5PGAppendInt16(&parse, len(args))
	for range args {
		batch5PGAppendInt32(&parse, 0) // type OID 0 = infer
	}
	if err := batch5PGWriteMessage(c.w, 'P', parse.Bytes()); err != nil {
		return nil, err
	}
	// Bind (unnamed portal/statement, all-text params and results).
	var bind bytes.Buffer
	bind.WriteByte(0)
	bind.WriteByte(0)
	batch5PGAppendInt16(&bind, 0) // all parameters use text format
	batch5PGAppendInt16(&bind, len(args))
	for _, arg := range args {
		if arg == nil {
			batch5PGAppendInt32(&bind, -1)
			continue
		}
		batch5PGAppendInt32(&bind, len(arg))
		bind.Write(arg)
	}
	batch5PGAppendInt16(&bind, 0) // all results use text format
	if err := batch5PGWriteMessage(c.w, 'B', bind.Bytes()); err != nil {
		return nil, err
	}
	// Describe portal.
	if err := batch5PGWriteMessage(c.w, 'D', []byte{'P', 0}); err != nil {
		return nil, err
	}
	// Execute portal.
	var execute bytes.Buffer
	execute.WriteByte(0)
	batch5PGAppendInt32(&execute, maxRows)
	if err := batch5PGWriteMessage(c.w, 'E', execute.Bytes()); err != nil {
		return nil, err
	}
	// Sync.
	if err := c.writeMessageFlush(ctx, 'S', nil); err != nil {
		return nil, err
	}

	result := &batch5PGResult{}
	var fields []batch5PGField
	for {
		typ, data, err := c.readMessage(ctx)
		if err != nil {
			return nil, err
		}
		switch typ {
		case '1', '2', '3':
			// ParseComplete / BindComplete / CloseComplete.
		case 'T':
			fields, err = batch5PGParseRowDescription(data)
			if err != nil {
				return nil, err
			}
			result.Columns = make([]string, len(fields))
			for index, field := range fields {
				result.Columns[index] = field.Name
			}
		case 'n':
			// NoData: statement returns no rows.
		case 'D':
			row, err := batch5PGParseDataRow(fields, data)
			if err != nil {
				return nil, err
			}
			result.Rows = append(result.Rows, row)
		case 's':
			// PortalSuspended: maxRows reached; Sync follows.
		case 'C':
			// CommandComplete.
		case 'E':
			return nil, batch5PGParseError(data)
		case 'Z':
			return result, nil
		case 'N', 'A', 'S':
			// Notices / notifications / parameter status.
		default:
			return nil, fmt.Errorf("postgres: unexpected extended-query message %q", string(typ))
		}
	}
}

func batch5PGAppendInt16(buf *bytes.Buffer, value int) {
	var raw [2]byte
	binary.BigEndian.PutUint16(raw[:], uint16(value))
	buf.Write(raw[:])
}

func batch5PGAppendInt32(buf *bytes.Buffer, value int) {
	var raw [4]byte
	binary.BigEndian.PutUint32(raw[:], uint32(value))
	buf.Write(raw[:])
}

// ---------------------------------------------------------------- decoding ---

func batch5PGParseRowDescription(payload []byte) ([]batch5PGField, error) {
	if len(payload) < 2 {
		return nil, errors.New("postgres: short RowDescription")
	}
	count := int(binary.BigEndian.Uint16(payload[:2]))
	offset := 2
	fields := make([]batch5PGField, 0, count)
	for index := 0; index < count; index++ {
		end := bytes.IndexByte(payload[offset:], 0)
		if end < 0 {
			return nil, errors.New("postgres: truncated RowDescription field name")
		}
		name := string(payload[offset : offset+end])
		offset += end + 1
		// tableOID(4) attnum(2) typeOID(4) typlen(2) typmod(4) format(2)
		if offset+18 > len(payload) {
			return nil, errors.New("postgres: truncated RowDescription field")
		}
		typeOID := binary.BigEndian.Uint32(payload[offset+6 : offset+10])
		offset += 18
		fields = append(fields, batch5PGField{Name: name, TypeOID: typeOID})
	}
	return fields, nil
}

func batch5PGParseDataRow(fields []batch5PGField, payload []byte) ([]any, error) {
	if len(payload) < 2 {
		return nil, errors.New("postgres: short DataRow")
	}
	count := int(binary.BigEndian.Uint16(payload[:2]))
	offset := 2
	row := make([]any, count)
	for index := 0; index < count; index++ {
		if offset+4 > len(payload) {
			return nil, errors.New("postgres: truncated DataRow")
		}
		length := int32(binary.BigEndian.Uint32(payload[offset : offset+4]))
		offset += 4
		if length < 0 {
			row[index] = nil
			continue
		}
		if offset+int(length) > len(payload) {
			return nil, errors.New("postgres: truncated DataRow value")
		}
		raw := string(payload[offset : offset+int(length)])
		offset += int(length)
		var typeOID uint32
		if index < len(fields) {
			typeOID = fields[index].TypeOID
		}
		row[index] = batch5PGDecodeValue(typeOID, raw)
	}
	return row, nil
}

// batch5PGDecodeValue converts a text-format value using the column type OID.
// Integers stay exact (JSON numbers), floats/numeric become float64, booleans
// become bool, json/jsonb are decoded, everything else stays a string.
func batch5PGDecodeValue(typeOID uint32, raw string) any {
	switch typeOID {
	case 16: // bool
		return raw == "t" || raw == "true"
	case 20, 21, 23, 26, 28: // int8, int2, int4, oid, xid
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return parsed
		}
	case 700, 701, 1700: // float4, float8, numeric
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil {
			return parsed
		}
	case 114, 3802: // json, jsonb
		var decoded any
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			return decoded
		}
	}
	return raw
}
