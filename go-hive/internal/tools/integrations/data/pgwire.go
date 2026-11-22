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
package data

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
	pgProtocolVersion = 196608   // 3.0
	pgSSLRequestCode  = 80877103 // SSLRequest
	pgMaxMessage      = 64 << 20 // hard cap on a single backend message
	pgDialTimeout     = 10 * time.Second
	pgReadTimeout     = 30 * time.Second
)

// pgConfig is a parsed libpq-style connection string.
type pgConfig struct {
	Host           string
	Port           string
	User           string
	Password       string
	Database       string
	SSLMode        string
	ConnectTimeout time.Duration
}

// pgError is an ErrorResponse from the server.
type pgError struct {
	Severity string
	Code     string
	Message  string
}

func (e *pgError) Error() string {
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("%s (SQLSTATE %s)", e.Message, e.Code)
	case e.Message != "":
		return e.Message
	default:
		return "postgres: server error"
	}
}

// pgIsQueryCanceled reports statement_timeout cancellations.
func pgIsQueryCanceled(err error) bool {
	var pgErr *pgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57014"
	}
	return false
}

// pgField is one RowDescription field.
type pgField struct {
	Name    string
	TypeOID uint32
}

// pgResult is a decoded result set.
type pgResult struct {
	Columns []string
	Rows    [][]any
}

// pgConn is one live connection.
type pgConn struct {
	conn   net.Conn
	r      *bufio.Reader
	w      *bufio.Writer
	cfg    pgConfig
	params map[string]string

	scramClientNonce  string
	scramClientFirst  string
	scramServerFirst  string
	scramServerSigB64 string
}

// ------------------------------------------------------------ DSN parsing ---

// pgParseDSN accepts URL form (postgres://..., postgresql://...) and
// libpq keyword/value form (host=... port=... dbname=...).
func pgParseDSN(dsn string) (pgConfig, error) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return pgConfig{}, errors.New("empty PostgreSQL connection string")
	}
	cfg := pgConfig{SSLMode: "prefer", ConnectTimeout: pgDialTimeout}
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
		fields, err := pgSplitDSNFields(dsn)
		if err != nil {
			return cfg, err
		}
		cfg.applyParams(func(key string) string { return fields[key] })
	}
	cfg.applyDefaults()
	return cfg, nil
}

// applyParams overlays named connection parameters (URL query or keyword form).
func (c *pgConfig) applyParams(get func(string) string) {
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

func (c *pgConfig) applyDefaults() {
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
		c.ConnectTimeout = pgDialTimeout
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

// pgSplitDSNFields tokenizes libpq keyword=value strings, honouring
// single-quoted values with backslash escapes.
func pgSplitDSNFields(dsn string) (map[string]string, error) {
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
