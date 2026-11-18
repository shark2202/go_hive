package integrations

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ------------------------------------------------------------- fake server ---

// batch5RedisTestServer is a minimal RESP2 server. The handler receives the
// decoded command argv and returns a reply value: string, int64, nil, []string,
// []any or error.
type batch5RedisTestServer struct {
	listener net.Listener
	handler  func(args []string) any
	mu       sync.Mutex
	commands [][]string
}

func batch5StartRedisServer(t *testing.T, handler func([]string) any) *batch5RedisTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &batch5RedisTestServer{listener: listener, handler: handler}
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return server
}

func (s *batch5RedisTestServer) url() string {
	return "redis://" + s.listener.Addr().String()
}

func (s *batch5RedisTestServer) commandsSeen() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.commands))
	copy(out, s.commands)
	return out
}

func (s *batch5RedisTestServer) serve(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // test server
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if !strings.HasPrefix(line, "*") {
			return
		}
		count, err := strconv.Atoi(line[1:])
		if err != nil || count < 0 {
			return
		}
		args := make([]string, 0, count)
		for index := 0; index < count; index++ {
			header, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			header = strings.TrimSuffix(strings.TrimSuffix(header, "\n"), "\r")
			if !strings.HasPrefix(header, "$") {
				return
			}
			length, err := strconv.Atoi(header[1:])
			if err != nil || length < 0 {
				return
			}
			buffer := make([]byte, length+2)
			if _, err := io.ReadFull(reader, buffer); err != nil {
				return
			}
			args = append(args, string(buffer[:length]))
		}
		s.mu.Lock()
		s.commands = append(s.commands, args)
		s.mu.Unlock()
		if err := batch5RedisTestWrite(writer, s.handler(args)); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
	}
}

func batch5RedisTestWrite(w *bufio.Writer, value any) error {
	switch typed := value.(type) {
	case nil:
		_, err := w.WriteString("$-1\r\n")
		return err
	case error:
		_, err := w.WriteString("-" + typed.Error() + "\r\n")
		return err
	case string:
		if _, err := fmt.Fprintf(w, "$%d\r\n", len(typed)); err != nil {
			return err
		}
		_, err := w.WriteString(typed + "\r\n")
		return err
	case int:
		_, err := fmt.Fprintf(w, ":%d\r\n", typed)
		return err
	case int64:
		_, err := fmt.Fprintf(w, ":%d\r\n", typed)
		return err
	case []string:
		if _, err := fmt.Fprintf(w, "*%d\r\n", len(typed)); err != nil {
			return err
		}
		for _, item := range typed {
			if _, err := fmt.Fprintf(w, "$%d\r\n%s\r\n", len(item), item); err != nil {
				return err
			}
		}
		return nil
	case []any:
		if _, err := fmt.Fprintf(w, "*%d\r\n", len(typed)); err != nil {
			return err
		}
		for _, item := range typed {
			// Nested writes go through the same encoder; flush is deferred.
			subWriter := w
			if err := batch5RedisTestWrite(subWriter, item); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported fake reply %T", value)
	}
}

// ------------------------------------------------------------------- tests ---

func batch5RedisRegistry(t *testing.T, rawURL string) *Registry {
	t.Helper()
	return newBatch5Lane2Registry(WithGetenv(func(name string) string {
		if name == "REDIS_URL" {
			return rawURL
		}
		return ""
	}))
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_get
func TestBatch5Lane2RedisGet(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		if args[0] != "GET" {
			return fmt.Errorf("ERR unexpected command %s", args[0])
		}
		if args[1] == "missing" {
			return nil
		}
		return "hello"
	})
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_get", "key": "greeting"})
	batch5AssertNoError(t, result)
	if result["value"] != "hello" || result["key"] != "greeting" {
		t.Fatalf("result = %v", result)
	}
	missing := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_get", "key": "missing"})
	batch5AssertNoError(t, missing)
	if missing["value"] != nil {
		t.Fatalf("missing value = %v, want nil", missing["value"])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_missing_key
func TestBatch5Lane2RedisValidation(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any { return nil })
	registry := batch5RedisRegistry(t, server.url())
	for _, call := range []struct {
		action string
		want   string
	}{
		{action: "redis_get", want: "key is required"},
		{action: "redis_delete", want: "keys is required"},
		{action: "redis_publish", want: "channel and message are required"},
		{action: "redis_ttl", want: "key is required"},
	} {
		result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": call.action})
		batch5AssertErrorContains(t, result, call.want)
	}
	empty := batch5ExecuteRegistry(t, newBatch5Lane2Registry(WithGetenv(func(string) string { return "" })), "redis", map[string]any{
		"action": "redis_get", "key": "k",
	})
	if empty["error"] != "REDIS_URL not set" {
		t.Fatalf("error = %v", empty["error"])
	}
	if help, _ := empty["help"].(string); !strings.Contains(help, "redis://") {
		t.Fatalf("help = %v", empty["help"])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_set_with_ttl
func TestBatch5Lane2RedisSet(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any { return "OK" })
	registry := batch5RedisRegistry(t, server.url())
	plain := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_set", "key": "k", "value": "v",
	})
	batch5AssertNoError(t, plain)
	if plain["status"] != "ok" || plain["key"] != "k" {
		t.Fatalf("plain = %v", plain)
	}
	ttl := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_set", "key": "k", "value": "v", "ttl": 30,
	})
	batch5AssertNoError(t, ttl)
	commands := server.commandsSeen()
	if len(commands) != 2 {
		t.Fatalf("commands = %v", commands)
	}
	if strings.Join(commands[0], " ") != "SET k v" {
		t.Fatalf("SET command = %v", commands[0])
	}
	if strings.Join(commands[1], " ") != "SETEX k 30 v" {
		t.Fatalf("SETEX command = %v", commands[1])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_delete
func TestBatch5Lane2RedisDelete(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		if args[0] == "DEL" {
			return int64(len(args) - 1)
		}
		return fmt.Errorf("ERR unexpected")
	})
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_delete", "keys": " a, b ,, c ",
	})
	batch5AssertNoError(t, result)
	if result["deleted"] != int64(3) {
		t.Fatalf("deleted = %v", result["deleted"])
	}
	if got := strings.Join(server.commandsSeen()[0], " "); got != "DEL a b c" {
		t.Fatalf("command = %q", got)
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_scan
func TestBatch5Lane2RedisKeys(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		if args[0] != "SCAN" {
			return fmt.Errorf("ERR unexpected")
		}
		switch args[1] {
		case "0":
			return []any{"7", []string{"a", "b"}}
		case "7":
			return []any{"0", []string{"c", "d"}}
		default:
			return []any{"0", []string{}}
		}
	})
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_keys", "pattern": "user:*", "count": 3,
	})
	batch5AssertNoError(t, result)
	keys, _ := result["keys"].([]string)
	if len(keys) != 3 || keys[0] != "a" || keys[2] != "c" {
		t.Fatalf("keys = %v", result["keys"])
	}
	commands := server.commandsSeen()
	if len(commands) != 2 {
		t.Fatalf("scan count = %d (%v)", len(commands), commands)
	}
	if got := strings.Join(commands[0], " "); got != "SCAN 0 MATCH user:* COUNT 3" {
		t.Fatalf("first scan = %q", got)
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_hset
func TestBatch5Lane2RedisHash(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		switch args[0] {
		case "HSET":
			return int64(1)
		case "HGETALL":
			return []string{"field", "value", "other", "second"}
		}
		return fmt.Errorf("ERR unexpected")
	})
	registry := batch5RedisRegistry(t, server.url())
	created := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_hset", "key": "h", "field": "field", "value": "value",
	})
	batch5AssertNoError(t, created)
	if created["created"] != true {
		t.Fatalf("created = %v", created["created"])
	}
	all := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_hgetall", "key": "h",
	})
	batch5AssertNoError(t, all)
	data, _ := all["data"].(map[string]any)
	if len(data) != 2 || data["field"] != "value" || data["other"] != "second" {
		t.Fatalf("data = %v", all["data"])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_lpush
func TestBatch5Lane2RedisList(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		switch args[0] {
		case "LPUSH":
			return int64(3)
		case "LRANGE":
			return []string{"c", "b", "a"}
		}
		return fmt.Errorf("ERR unexpected")
	})
	registry := batch5RedisRegistry(t, server.url())
	pushed := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_lpush", "key": "l", "values": "a,b,c",
	})
	batch5AssertNoError(t, pushed)
	if pushed["length"] != int64(3) {
		t.Fatalf("length = %v", pushed["length"])
	}
	rangeResult := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_lrange", "key": "l", "start": 0, "stop": -1,
	})
	batch5AssertNoError(t, rangeResult)
	items, _ := rangeResult["items"].([]string)
	if len(items) != 3 || items[0] != "c" {
		t.Fatalf("items = %v", rangeResult["items"])
	}
	if got := strings.Join(server.commandsSeen()[0], " "); got != "LPUSH l a b c" {
		t.Fatalf("command = %q", got)
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_publish
func TestBatch5Lane2RedisPublish(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any { return int64(2) })
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{
		"action": "redis_publish", "channel": "news", "message": "hello",
	})
	batch5AssertNoError(t, result)
	if result["receivers"] != int64(2) || result["channel"] != "news" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_info
func TestBatch5Lane2RedisInfo(t *testing.T) {
	info := "# Server\r\nredis_version:7.2.0\r\nuptime_in_seconds:120\r\n" +
		"# Clients\r\nconnected_clients:3\r\n" +
		"# Stats\r\ntotal_connections_received:42\r\n" +
		"# Memory\r\nused_memory_human:1.25M\r\n" +
		"# Keyspace\r\ndb0:keys=5,expires=1,avg_ttl=0\r\n"
	server := batch5StartRedisServer(t, func(args []string) any { return info })
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_info"})
	batch5AssertNoError(t, result)
	if result["redis_version"] != "7.2.0" || result["used_memory_human"] != "1.25M" {
		t.Fatalf("result = %v", result)
	}
	if result["connected_clients"] != int64(3) || result["total_connections_received"] != int64(42) {
		t.Fatalf("counters = %v", result)
	}
	if result["uptime_in_seconds"] != int64(120) {
		t.Fatalf("uptime = %v", result["uptime_in_seconds"])
	}
	db0, _ := result["db0"].(map[string]any)
	if db0["keys"] != int64(5) || db0["expires"] != int64(1) {
		t.Fatalf("db0 = %v", result["db0"])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_successful_ttl
func TestBatch5Lane2RedisTTL(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any { return int64(60) })
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_ttl", "key": "k"})
	batch5AssertNoError(t, result)
	if result["ttl"] != int64(60) {
		t.Fatalf("ttl = %v", result["ttl"])
	}
}

// Parity: tools/tests/tools/test_redis_tool.py::test_missing_url
func TestBatch5Lane2RedisServerError(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		return fmt.Errorf("WRONGTYPE Operation against a key holding the wrong kind of value")
	})
	registry := batch5RedisRegistry(t, server.url())
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_get", "key": "k"})
	batch5AssertErrorContains(t, result, "Redis GET failed: WRONGTYPE")
}

// Parity-exempt: AUTH/SELECT are transport concerns with no Python counterpart
// (upstream mocks redis-py).
func TestBatch5Lane2RedisAuthAndSelect(t *testing.T) {
	server := batch5StartRedisServer(t, func(args []string) any {
		switch args[0] {
		case "AUTH", "SELECT":
			return "OK"
		case "GET":
			return "value"
		}
		return fmt.Errorf("ERR unexpected")
	})
	rawURL := "redis://:secret@" + server.listener.Addr().String() + "/2"
	registry := batch5RedisRegistry(t, rawURL)
	result := batch5ExecuteRegistry(t, registry, "redis", map[string]any{"action": "redis_get", "key": "k"})
	batch5AssertNoError(t, result)
	commands := server.commandsSeen()
	if len(commands) != 3 {
		t.Fatalf("commands = %v", commands)
	}
	if strings.Join(commands[0], " ") != "AUTH secret" {
		t.Fatalf("auth = %v", commands[0])
	}
	if strings.Join(commands[1], " ") != "SELECT 2" {
		t.Fatalf("select = %v", commands[1])
	}
}
