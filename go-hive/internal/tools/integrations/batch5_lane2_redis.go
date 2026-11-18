// Redis integration (upstream redis_tool, 11 actions) over the RESP2 protocol.
//
// The repository forbids new dependencies, so commands are written directly on
// a TCP or TLS connection instead of using a Redis client library.
//
// Deliberate limits:
//   - one connection per action (no pool), 10s command timeout like upstream
//   - RESP2 only; no pub/sub subscription (publish is supported), no cluster
//     redirection, no RESP3 push messages
//   - AUTH supports both the classic password form and ACL user+password
package integrations

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	batch5RedisDialTimeout = 10 * time.Second
	batch5RedisIOTimeout   = 10 * time.Second
	batch5RedisMaxBulk     = 64 << 20
	batch5RedisMaxArray    = 1 << 20
)

// batch5RedisValue is one RESP2 reply.
type batch5RedisValue struct {
	kind byte // '+', ':', '$', '*', 'n' (null)
	str  string
	num  int64
	arr  []batch5RedisValue
}

func (v batch5RedisValue) isNull() bool { return v.kind == 'n' }

func (v batch5RedisValue) text() (string, bool) {
	if v.kind == '$' || v.kind == '+' {
		return v.str, true
	}
	return "", false
}

// batch5RedisConn is one live connection.
type batch5RedisConn struct {
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

// batch5RedisConnect parses REDIS_URL (redis:// or rediss://), authenticates
// and selects the database from the URL path.
func batch5RedisConnect(ctx context.Context, rawURL string) (*batch5RedisConn, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL: %w", err)
	}
	if parsed.Scheme != "redis" && parsed.Scheme != "rediss" {
		return nil, fmt.Errorf("unsupported Redis URL scheme %q", parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		host = "localhost"
	}
	port := parsed.Port()
	if port == "" {
		port = "6379"
	}
	dialer := net.Dialer{Timeout: batch5RedisDialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "rediss" {
		tlsConn := tls.Client(raw, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		raw = tlsConn
	}
	conn := &batch5RedisConn{conn: raw, r: bufio.NewReader(raw), w: bufio.NewWriter(raw)}
	if parsed.User != nil {
		password, hasPassword := parsed.User.Password()
		username := parsed.User.Username()
		switch {
		case hasPassword && username != "" && username != "default":
			if _, err := conn.do(ctx, "AUTH", username, password); err != nil {
				conn.Close()
				return nil, err
			}
		case hasPassword:
			if _, err := conn.do(ctx, "AUTH", password); err != nil {
				conn.Close()
				return nil, err
			}
		}
	}
	if db := strings.TrimPrefix(parsed.Path, "/"); db != "" && db != "0" {
		if _, err := strconv.Atoi(db); err != nil {
			conn.Close()
			return nil, fmt.Errorf("invalid Redis database index %q", db)
		}
		if _, err := conn.do(ctx, "SELECT", db); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

// Close releases the connection.
func (c *batch5RedisConn) Close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close()
}

// do writes one command and reads its reply. A RESP error reply is returned as
// a Go error carrying the server message.
func (c *batch5RedisConn) do(ctx context.Context, args ...string) (batch5RedisValue, error) {
	if err := ctx.Err(); err != nil {
		return batch5RedisValue{}, err
	}
	deadline := time.Now().Add(batch5RedisIOTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return batch5RedisValue{}, err
	}
	var payload bytes.Buffer
	fmt.Fprintf(&payload, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&payload, "$%d\r\n", len(arg))
		payload.WriteString(arg)
		payload.WriteString("\r\n")
	}
	if _, err := c.w.Write(payload.Bytes()); err != nil {
		return batch5RedisValue{}, err
	}
	if err := c.w.Flush(); err != nil {
		return batch5RedisValue{}, err
	}
	return c.readReply()
}

func (c *batch5RedisConn) readReply() (batch5RedisValue, error) {
	line, err := c.readLine()
	if err != nil {
		return batch5RedisValue{}, err
	}
	if line == "" {
		return batch5RedisValue{}, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return batch5RedisValue{kind: '+', str: line[1:]}, nil
	case '-':
		return batch5RedisValue{}, errors.New(line[1:])
	case ':':
		number, err := strconv.ParseInt(line[1:], 10, 64)
		if err != nil {
			return batch5RedisValue{}, fmt.Errorf("redis: invalid integer reply %q", line[1:])
		}
		return batch5RedisValue{kind: ':', num: number}, nil
	case '$':
		length, err := strconv.Atoi(line[1:])
		if err != nil {
			return batch5RedisValue{}, fmt.Errorf("redis: invalid bulk length %q", line[1:])
		}
		if length == -1 {
			return batch5RedisValue{kind: 'n'}, nil
		}
		if length < 0 || length > batch5RedisMaxBulk {
			return batch5RedisValue{}, fmt.Errorf("redis: bulk length out of range: %d", length)
		}
		buffer := make([]byte, length+2)
		if _, err := io.ReadFull(c.r, buffer); err != nil {
			return batch5RedisValue{}, err
		}
		return batch5RedisValue{kind: '$', str: string(buffer[:length])}, nil
	case '*':
		count, err := strconv.Atoi(line[1:])
		if err != nil {
			return batch5RedisValue{}, fmt.Errorf("redis: invalid array length %q", line[1:])
		}
		if count == -1 {
			return batch5RedisValue{kind: 'n'}, nil
		}
		if count < 0 || count > batch5RedisMaxArray {
			return batch5RedisValue{}, fmt.Errorf("redis: array length out of range: %d", count)
		}
		items := make([]batch5RedisValue, 0, count)
		for index := 0; index < count; index++ {
			item, err := c.readReply()
			if err != nil {
				return batch5RedisValue{}, err
			}
			items = append(items, item)
		}
		return batch5RedisValue{kind: '*', arr: items}, nil
	default:
		return batch5RedisValue{}, fmt.Errorf("redis: unexpected reply %q", line)
	}
}

func (c *batch5RedisConn) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("redis: malformed reply line")
	}
	return line[:len(line)-2], nil
}

// ---------------------------------------------------------------- actions ---

func batch5RedisMissingURL() map[string]any {
	return map[string]any{
		"error": "REDIS_URL not set",
		"help":  "Set REDIS_URL (e.g. redis://localhost:6379 or redis://:password@host:6379/0)",
	}
}

// batch5RedisOpen resolves REDIS_URL and connects.
func batch5RedisOpen(ctx context.Context, rt *Runtime) (*batch5RedisConn, map[string]any, error) {
	rawURL := batch5Lane2ConfiguredToken(rt, "redis_tool", "REDIS_URL")
	if rawURL == "" {
		return nil, batch5RedisMissingURL(), nil
	}
	conn, err := batch5RedisConnect(ctx, rawURL)
	if err != nil {
		return nil, nil, err
	}
	return conn, nil, nil
}

// batch5RedisRun executes one command and maps failures to the upstream
// "<COMMAND> failed: <detail>" error shape.
func batch5RedisRun(ctx context.Context, rt *Runtime, command string, args ...string) (batch5RedisValue, map[string]any, error) {
	conn, response, err := batch5RedisOpen(ctx, rt)
	if response != nil || err != nil {
		return batch5RedisValue{}, response, err
	}
	defer conn.Close()
	value, cmdErr := conn.do(ctx, args...)
	if cmdErr != nil {
		return batch5RedisValue{}, batch5Lane2Err("Redis " + command + " failed: " + cmdErr.Error()), nil
	}
	return value, nil, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_get
func batch5Lane2RedisGet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key := stringArg(input, "key")
	if key == "" {
		return batch5Lane2Err("key is required"), nil
	}
	value, response, err := batch5RedisRun(ctx, rt, "GET", "GET", key)
	if response != nil || err != nil {
		return response, err
	}
	if value.isNull() {
		return map[string]any{"key": key, "value": nil}, nil
	}
	text, _ := value.text()
	return map[string]any{"key": key, "value": text}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_set
func batch5Lane2RedisSet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, value := stringArg(input, "key"), stringArg(input, "value")
	if key == "" {
		return batch5Lane2Err("key is required"), nil
	}
	ttl := intArg(input, "ttl", 0)
	var response map[string]any
	var err error
	if ttl > 0 {
		_, response, err = batch5RedisRun(ctx, rt, "SET", "SETEX", key, strconv.Itoa(ttl), value)
	} else {
		_, response, err = batch5RedisRun(ctx, rt, "SET", "SET", key, value)
	}
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"status": "ok", "key": key}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_delete
func batch5Lane2RedisDelete(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	rawKeys := stringArg(input, "keys")
	if rawKeys == "" {
		return batch5Lane2Err("keys is required"), nil
	}
	var keys []string
	for _, key := range strings.Split(rawKeys, ",") {
		if trimmed := strings.TrimSpace(key); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	args := append([]string{"DEL"}, keys...)
	value, response, err := batch5RedisRun(ctx, rt, "DELETE", args...)
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"deleted": value.num}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_keys
func batch5Lane2RedisKeys(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	pattern := stringArg(input, "pattern")
	if pattern == "" {
		pattern = "*"
	}
	count := intArg(input, "count", 100)
	if count < 1 {
		count = 1
	}
	if count > 1000 {
		count = 1000
	}
	conn, response, err := batch5RedisOpen(ctx, rt)
	if response != nil || err != nil {
		return response, err
	}
	defer conn.Close()
	perPage := count
	if perPage > 100 {
		perPage = 100
	}
	keys := make([]string, 0, count)
	cursor := "0"
	for len(keys) < count {
		reply, scanErr := conn.do(ctx, "SCAN", cursor, "MATCH", pattern, "COUNT", strconv.Itoa(perPage))
		if scanErr != nil {
			return batch5Lane2Err("Redis KEYS failed: " + scanErr.Error()), nil
		}
		if reply.kind != '*' || len(reply.arr) != 2 || reply.arr[1].kind != '*' {
			return batch5Lane2Err("Redis KEYS failed: unexpected SCAN reply"), nil
		}
		cursor = reply.arr[0].str
		for _, item := range reply.arr[1].arr {
			keys = append(keys, item.str)
		}
		if cursor == "0" {
			break
		}
	}
	if len(keys) > count {
		keys = keys[:count]
	}
	return map[string]any{"pattern": pattern, "keys": keys}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_hset
func batch5Lane2RedisHSet(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, field, value := stringArg(input, "key"), stringArg(input, "field"), stringArg(input, "value")
	if key == "" || field == "" {
		return batch5Lane2Err("key and field are required"), nil
	}
	reply, response, err := batch5RedisRun(ctx, rt, "HSET", "HSET", key, field, value)
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"status": "ok", "key": key, "field": field, "created": reply.num == 1}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_hgetall
func batch5Lane2RedisHGetAll(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key := stringArg(input, "key")
	if key == "" {
		return batch5Lane2Err("key is required"), nil
	}
	reply, response, err := batch5RedisRun(ctx, rt, "HGETALL", "HGETALL", key)
	if response != nil || err != nil {
		return response, err
	}
	data := map[string]any{}
	if reply.kind == '*' {
		for index := 0; index+1 < len(reply.arr); index += 2 {
			data[reply.arr[index].str] = reply.arr[index+1].str
		}
	}
	return map[string]any{"key": key, "data": data}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_lpush
func batch5Lane2RedisLPush(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key, rawValues := stringArg(input, "key"), stringArg(input, "values")
	if key == "" || rawValues == "" {
		return batch5Lane2Err("key and values are required"), nil
	}
	values := make([]string, 0, 4)
	for _, value := range strings.Split(rawValues, ",") {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	args := append([]string{"LPUSH", key}, values...)
	reply, response, err := batch5RedisRun(ctx, rt, "LPUSH", args...)
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"key": key, "length": reply.num}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_lrange
func batch5Lane2RedisLRange(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key := stringArg(input, "key")
	if key == "" {
		return batch5Lane2Err("key is required"), nil
	}
	start, stop := intArg(input, "start", 0), intArg(input, "stop", -1)
	reply, response, err := batch5RedisRun(ctx, rt, "LRANGE", "LRANGE", key, strconv.Itoa(start), strconv.Itoa(stop))
	if response != nil || err != nil {
		return response, err
	}
	items := make([]string, 0, len(reply.arr))
	for _, item := range reply.arr {
		items = append(items, item.str)
	}
	return map[string]any{"key": key, "items": items}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_publish
func batch5Lane2RedisPublish(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	channel, message := stringArg(input, "channel"), stringArg(input, "message")
	if channel == "" || message == "" {
		return batch5Lane2Err("channel and message are required"), nil
	}
	reply, response, err := batch5RedisRun(ctx, rt, "PUBLISH", "PUBLISH", channel, message)
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"channel": channel, "receivers": reply.num}, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_info
func batch5Lane2RedisInfo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	reply, response, err := batch5RedisRun(ctx, rt, "INFO", "INFO")
	if response != nil || err != nil {
		return response, err
	}
	text, _ := reply.text()
	fields := batch5RedisParseInfo(text)
	info := map[string]any{
		"redis_version":              fields["redis_version"],
		"connected_clients":          batch5RedisIntField(fields, "connected_clients"),
		"used_memory_human":          fields["used_memory_human"],
		"total_connections_received": batch5RedisIntField(fields, "total_connections_received"),
		"uptime_in_seconds":          batch5RedisIntField(fields, "uptime_in_seconds"),
		"db0":                        batch5RedisParseDBInfo(fields["db0"]),
	}
	return info, nil
}

// Parity: tools/src/aden_tools/tools/redis_tool/redis_tool.py::redis_ttl
func batch5Lane2RedisTTL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	key := stringArg(input, "key")
	if key == "" {
		return batch5Lane2Err("key is required"), nil
	}
	reply, response, err := batch5RedisRun(ctx, rt, "TTL", "TTL", key)
	if response != nil || err != nil {
		return response, err
	}
	return map[string]any{"key": key, "ttl": reply.num}, nil
}

// ---------------------------------------------------------------- parsing ---

// batch5RedisParseInfo flattens an INFO reply into key/value pairs. Section
// headers are ignored; later sections override earlier duplicate keys, which is
// what redis-py's info() dictionary ends up with.
func batch5RedisParseInfo(text string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		index := strings.IndexByte(line, ':')
		if index <= 0 {
			continue
		}
		fields[line[:index]] = line[index+1:]
	}
	return fields
}

func batch5RedisIntField(fields map[string]string, name string) int64 {
	if number, err := strconv.ParseInt(strings.TrimSpace(fields[name]), 10, 64); err == nil {
		return number
	}
	return 0
}

// batch5RedisParseDBInfo mirrors redis-py's keyspace dict parsing:
// "keys=1,expires=0,avg_ttl=0" becomes a map with integer values.
func batch5RedisParseDBInfo(raw string) map[string]any {
	out := map[string]any{}
	for _, part := range strings.Split(raw, ",") {
		index := strings.IndexByte(part, '=')
		if index <= 0 {
			continue
		}
		name, value := strings.TrimSpace(part[:index]), strings.TrimSpace(part[index+1:])
		if name == "" {
			continue
		}
		if number, err := strconv.ParseInt(value, 10, 64); err == nil {
			out[name] = number
		} else {
			out[name] = value
		}
	}
	return out
}

// batch5Lane2RedisImpls maps the upstream action names to handlers.
func batch5Lane2RedisImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"redis_get":     batch5Lane2RedisGet,
		"redis_set":     batch5Lane2RedisSet,
		"redis_delete":  batch5Lane2RedisDelete,
		"redis_keys":    batch5Lane2RedisKeys,
		"redis_hset":    batch5Lane2RedisHSet,
		"redis_hgetall": batch5Lane2RedisHGetAll,
		"redis_lpush":   batch5Lane2RedisLPush,
		"redis_lrange":  batch5Lane2RedisLRange,
		"redis_publish": batch5Lane2RedisPublish,
		"redis_info":    batch5Lane2RedisInfo,
		"redis_ttl":     batch5Lane2RedisTTL,
	}
}

// batch5Lane2RedisTool is the model-facing catalog entry.
var batch5Lane2RedisTool = &Tool{
	ID:          "redis_tool",
	Name:        "redis",
	DisplayName: "Redis",
	Summary:     "Redis key-value, hash, list and pub/sub integration (11 actions).",
	Actions: []Action{
		{
			Name:     "redis_get",
			Summary:  "Get the value of a key (null when the key does not exist).",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key"},
			Params:   map[string]any{"key": param("string")},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_set",
			Summary:  "Set a key-value pair with an optional TTL in seconds.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key", "value"},
			Params: map[string]any{
				"key":   param("string"),
				"value": param("string"),
				"ttl":   paramDefault("integer", 0),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_delete",
			Summary:  "Delete one or more comma-separated keys.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"keys"},
			Params:   map[string]any{"keys": param("string")},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:    "redis_keys",
			Summary: "List keys matching a glob pattern using SCAN.",
			Source:  "redis_tool/redis_tool.py",
			Params: map[string]any{
				"pattern": paramDefault("string", "*"),
				"count":   paramDefault("integer", 100),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_hset",
			Summary:  "Set a field in a hash.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key", "field", "value"},
			Params: map[string]any{
				"key":   param("string"),
				"field": param("string"),
				"value": param("string"),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_hgetall",
			Summary:  "Get all fields and values from a hash.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key"},
			Params:   map[string]any{"key": param("string")},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_lpush",
			Summary:  "Push comma-separated values to the head of a list.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key", "values"},
			Params: map[string]any{
				"key":    param("string"),
				"values": param("string"),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_lrange",
			Summary:  "Get a range of list elements (inclusive stop index).",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key"},
			Params: map[string]any{
				"key":   param("string"),
				"start": paramDefault("integer", 0),
				"stop":  paramDefault("integer", -1),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_publish",
			Summary:  "Publish a message and return the receiver count.",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"channel", "message"},
			Params: map[string]any{
				"channel": param("string"),
				"message": param("string"),
			},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_info",
			Summary:  "Get server version, clients, memory and keyspace statistics.",
			Source:   "redis_tool/redis_tool.py",
			Params:   map[string]any{},
			Requires: []string{"REDIS_URL"},
		},
		{
			Name:     "redis_ttl",
			Summary:  "Get a key's time-to-live in seconds (-1 no expiry, -2 missing).",
			Source:   "redis_tool/redis_tool.py",
			Required: []string{"key"},
			Params:   map[string]any{"key": param("string")},
			Requires: []string{"REDIS_URL"},
		},
	},
}
