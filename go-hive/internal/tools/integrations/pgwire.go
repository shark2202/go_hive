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
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
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
