package integrations

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
)

// ------------------------------------------------------------- fake server ---

// batch5PGTestColumn is one RowDescription field served by the fake.
type batch5PGTestColumn struct {
	name    string
	typeOID uint32
}

// batch5PGTestResult is what the fake returns for one SQL statement.
type batch5PGTestResult struct {
	columns []batch5PGTestColumn
	rows    [][]any
	code    string
	message string
}

func (r *batch5PGTestResult) failed() bool { return r != nil && r.code != "" }

// batch5PGTestServer speaks enough of the PostgreSQL v3 protocol to exercise
// the real client: startup, optional MD5/SCRAM authentication, simple queries
// and the extended Parse/Bind/Describe/Execute flow.
type batch5PGTestServer struct {
	listener  net.Listener
	auth      string
	user      string
	password  string
	handler   func(query string) *batch5PGTestResult
	mu        sync.Mutex
	queries   []string
	params    [][]string
	serveErrs []error
}

func batch5StartPGServer(t *testing.T, auth, user, password string, handler func(string) *batch5PGTestResult) *batch5PGTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := &batch5PGTestServer{
		listener: listener,
		auth:     auth,
		user:     user,
		password: password,
		handler:  handler,
	}
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

func (s *batch5PGTestServer) dsn() string {
	return fmt.Sprintf("postgresql://%s:%s@%s/postgres?sslmode=prefer", s.user, s.password, s.listener.Addr().String())
}

func (s *batch5PGTestServer) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serveErrs = append(s.serveErrs, err)
}

func (s *batch5PGTestServer) record(query string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, query)
}

func (s *batch5PGTestServer) recordParams(params []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.params = append(s.params, params)
}

func (s *batch5PGTestServer) recordedQueries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.queries...)
}

func (s *batch5PGTestServer) recordedParams() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]string, len(s.params))
	copy(out, s.params)
	return out
}

func (s *batch5PGTestServer) errors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.serveErrs...)
}

func (s *batch5PGTestServer) result(query string) *batch5PGTestResult {
	if s.handler == nil {
		return &batch5PGTestResult{}
	}
	return s.handler(strings.TrimSpace(query))
}

func batch5PGTestWriteMessage(w *bufio.Writer, typ byte, payload []byte) error {
	header := make([]byte, 5)
	header[0] = typ
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)+4))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return w.Flush()
}

func batch5PGTestReadMessage(r *bufio.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 4 || length > 1<<20 {
		return 0, nil, fmt.Errorf("bad message length %d", length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func batch5PGTestAppendField(buf *bytes.Buffer, code byte, value string) {
	buf.WriteByte(code)
	buf.WriteString(value)
	buf.WriteByte(0)
}

func (s *batch5PGTestServer) writeError(w *bufio.Writer, code, message string) error {
	var payload bytes.Buffer
	batch5PGTestAppendField(&payload, 'S', "ERROR")
	batch5PGTestAppendField(&payload, 'C', code)
	batch5PGTestAppendField(&payload, 'M', message)
	payload.WriteByte(0)
	return batch5PGTestWriteMessage(w, 'E', payload.Bytes())
}

func (s *batch5PGTestServer) writeReady(w *bufio.Writer) error {
	return batch5PGTestWriteMessage(w, 'Z', []byte{'I'})
}

func (s *batch5PGTestServer) writeRowDescription(w *bufio.Writer, columns []batch5PGTestColumn) error {
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.BigEndian, int16(len(columns)))
	for _, column := range columns {
		payload.WriteString(column.name)
		payload.WriteByte(0)
		_ = binary.Write(&payload, binary.BigEndian, int32(0)) // table OID
		_ = binary.Write(&payload, binary.BigEndian, int16(0)) // attnum
		_ = binary.Write(&payload, binary.BigEndian, column.typeOID)
		_ = binary.Write(&payload, binary.BigEndian, int16(-1)) // typlen
		_ = binary.Write(&payload, binary.BigEndian, int32(-1)) // typmod
		_ = binary.Write(&payload, binary.BigEndian, int16(0))  // text format
	}
	return batch5PGTestWriteMessage(w, 'T', payload.Bytes())
}

func (s *batch5PGTestServer) writeDataRow(w *bufio.Writer, row []any) error {
	var payload bytes.Buffer
	_ = binary.Write(&payload, binary.BigEndian, int16(len(row)))
	for _, value := range row {
		if value == nil {
			_ = binary.Write(&payload, binary.BigEndian, int32(-1))
			continue
		}
		text := fmt.Sprint(value)
		_ = binary.Write(&payload, binary.BigEndian, int32(len(text)))
		payload.WriteString(text)
	}
	return batch5PGTestWriteMessage(w, 'D', payload.Bytes())
}

func (s *batch5PGTestServer) writeStartup(w *bufio.Writer, params map[string]string) error {
	if err := batch5PGTestWriteMessage(w, 'R', []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	for key, value := range params {
		var payload bytes.Buffer
		payload.WriteString(key)
		payload.WriteByte(0)
		payload.WriteString(value)
		payload.WriteByte(0)
		if err := batch5PGTestWriteMessage(w, 'S', payload.Bytes()); err != nil {
			return err
		}
	}
	if err := batch5PGTestWriteMessage(w, 'K', make([]byte, 8)); err != nil {
		return err
	}
	return s.writeReady(w)
}

func (s *batch5PGTestServer) serve(conn net.Conn) {
	defer conn.Close() //nolint:errcheck // test server
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	startup := map[string]string{}
	for {
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint32(header[:]))
		if length == 8 {
			var code [4]byte
			if _, err := io.ReadFull(reader, code[:]); err != nil {
				s.fail(err)
				return
			}
			if binary.BigEndian.Uint32(code[:]) == batch5PGSSLRequestCode {
				if _, err := conn.Write([]byte{'N'}); err != nil {
					s.fail(err)
					return
				}
				continue
			}
			s.fail(fmt.Errorf("unexpected startup code %d", binary.BigEndian.Uint32(code[:])))
			return
		}
		if length < 8 || length > 4096 {
			s.fail(fmt.Errorf("unexpected startup length %d", length))
			return
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(reader, body); err != nil {
			s.fail(err)
			return
		}
		pairs := body[4:]
		for len(pairs) > 0 && pairs[0] != 0 {
			keyEnd := bytes.IndexByte(pairs, 0)
			if keyEnd < 0 {
				break
			}
			key := string(pairs[:keyEnd])
			pairs = pairs[keyEnd+1:]
			valueEnd := bytes.IndexByte(pairs, 0)
			if valueEnd < 0 {
				break
			}
			startup[key] = string(pairs[:valueEnd])
			pairs = pairs[valueEnd+1:]
		}
		break
	}

	if err := s.authenticate(reader, writer, startup["user"]); err != nil {
		s.fail(err)
		return
	}
	if err := s.writeStartup(writer, map[string]string{"server_version": "16.0", "client_encoding": "UTF8"}); err != nil {
		s.fail(err)
		return
	}

	pendingQuery := ""
	for {
		typ, data, err := batch5PGTestReadMessage(reader)
		if err != nil {
			return
		}
		switch typ {
		case 'Q':
			query := strings.TrimSuffix(string(data), "\x00")
			s.record(query)
			if err := s.respondSimple(writer, query); err != nil {
				s.fail(err)
				return
			}
		case 'P':
			query, err := batch5PGTestParseQuery(data)
			if err != nil {
				s.fail(err)
				return
			}
			s.record(query)
			pendingQuery = query
			if err := batch5PGTestWriteMessage(writer, '1', nil); err != nil {
				s.fail(err)
				return
			}
		case 'B':
			s.recordParams(batch5PGTestParseBind(data))
			if err := batch5PGTestWriteMessage(writer, '2', nil); err != nil {
				s.fail(err)
				return
			}
		case 'D':
			result := s.result(pendingQuery)
			if result.failed() {
				if err := s.writeError(writer, result.code, result.message); err != nil {
					s.fail(err)
					return
				}
				continue
			}
			if len(result.columns) == 0 {
				if err := batch5PGTestWriteMessage(writer, 'n', []byte{0}); err != nil {
					s.fail(err)
					return
				}
				continue
			}
			if err := s.writeRowDescription(writer, result.columns); err != nil {
				s.fail(err)
				return
			}
		case 'E':
			result := s.result(pendingQuery)
			if result.failed() {
				if err := s.writeError(writer, result.code, result.message); err != nil {
					s.fail(err)
					return
				}
				continue
			}
			for _, row := range result.rows {
				if err := s.writeDataRow(writer, row); err != nil {
					s.fail(err)
					return
				}
			}
			if err := batch5PGTestWriteMessage(writer, 'C', []byte("SELECT 0\x00")); err != nil {
				s.fail(err)
				return
			}
		case 'S':
			if err := s.writeReady(writer); err != nil {
				s.fail(err)
				return
			}
		case 'X':
			return
		default:
			// Flush/Close/Parse-complete acks are irrelevant to the fake.
		}
	}
}

func (s *batch5PGTestServer) respondSimple(w *bufio.Writer, query string) error {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SET ") {
		if err := batch5PGTestWriteMessage(w, 'C', []byte("SET\x00")); err != nil {
			return err
		}
		return s.writeReady(w)
	}
	result := s.result(query)
	if result.failed() {
		if err := s.writeError(w, result.code, result.message); err != nil {
			return err
		}
		return s.writeReady(w)
	}
	if len(result.columns) > 0 {
		if err := s.writeRowDescription(w, result.columns); err != nil {
			return err
		}
		for _, row := range result.rows {
			if err := s.writeDataRow(w, row); err != nil {
				return err
			}
		}
	}
	if err := batch5PGTestWriteMessage(w, 'C', []byte("SELECT 0\x00")); err != nil {
		return err
	}
	return s.writeReady(w)
}

func (s *batch5PGTestServer) authenticate(reader *bufio.Reader, writer *bufio.Writer, user string) error {
	switch s.auth {
	case "md5":
		salt := []byte{9, 8, 7, 6}
		payload := make([]byte, 8)
		binary.BigEndian.PutUint32(payload[:4], 5)
		copy(payload[4:], salt)
		if err := batch5PGTestWriteMessage(writer, 'R', payload); err != nil {
			return err
		}
		typ, data, err := batch5PGTestReadMessage(reader)
		if err != nil {
			return err
		}
		if typ != 'p' {
			return fmt.Errorf("expected password message, got %q", string(typ))
		}
		inner := md5.Sum([]byte(s.password + user))
		outer := md5.Sum([]byte(hex.EncodeToString(inner[:]) + string(salt)))
		expected := "md5" + hex.EncodeToString(outer[:])
		if got := strings.TrimSuffix(string(data), "\x00"); got != expected {
			return fmt.Errorf("unexpected md5 password %q", got)
		}
		return batch5PGTestWriteMessage(writer, 'R', []byte{0, 0, 0, 0})
	case "scram":
		var mechanisms bytes.Buffer
		mechanisms.WriteString("SCRAM-SHA-256")
		mechanisms.WriteByte(0)
		mechanisms.WriteByte(0)
		payload := make([]byte, 4)
		binary.BigEndian.PutUint32(payload[:4], 10)
		if err := batch5PGTestWriteMessage(writer, 'R', append(payload, mechanisms.Bytes()...)); err != nil {
			return err
		}
		typ, data, err := batch5PGTestReadMessage(reader)
		if err != nil {
			return err
		}
		if typ != 'p' {
			return fmt.Errorf("expected SASLInitialResponse, got %q", string(typ))
		}
		mechanismEnd := bytes.IndexByte(data, 0)
		if mechanismEnd < 0 {
			return fmt.Errorf("malformed SASLInitialResponse")
		}
		mechanism := string(data[:mechanismEnd])
		if mechanism != "SCRAM-SHA-256" {
			return fmt.Errorf("unexpected SASL mechanism %q", mechanism)
		}
		rest := data[mechanismEnd+1:]
		if len(rest) < 4 {
			return fmt.Errorf("short SASLInitialResponse")
		}
		initialLength := int(binary.BigEndian.Uint32(rest[:4]))
		if initialLength > len(rest)-4 {
			return fmt.Errorf("bad SASLInitialResponse length")
		}
		initial := string(rest[4 : 4+initialLength])
		if !strings.HasPrefix(initial, "n,,") {
			return fmt.Errorf("unexpected SCRAM initial %q", initial)
		}
		clientFirstBare := initial[3:]
		attrs := batch5PGSCRAMAttributes(clientFirstBare)
		clientNonce := attrs["r"]
		if clientNonce == "" {
			return fmt.Errorf("missing client nonce")
		}
		serverNonce := clientNonce + "SRV"
		salt := []byte("0123456789abcdef")
		iterations := 4096
		serverFirst := fmt.Sprintf("r=%s,s=%s,i=%d", serverNonce, base64.StdEncoding.EncodeToString(salt), iterations)
		continuePayload := make([]byte, 4)
		binary.BigEndian.PutUint32(continuePayload[:4], 11)
		if err := batch5PGTestWriteMessage(writer, 'R', append(continuePayload, []byte(serverFirst)...)); err != nil {
			return err
		}
		typ, data, err = batch5PGTestReadMessage(reader)
		if err != nil {
			return err
		}
		if typ != 'p' {
			return fmt.Errorf("expected SASL final response, got %q", string(typ))
		}
		final := strings.TrimSuffix(string(data), "\x00")
		proofIndex := strings.LastIndex(final, ",p=")
		if proofIndex < 0 {
			return fmt.Errorf("missing SCRAM proof in %q", final)
		}
		withoutProof := final[:proofIndex]
		encodedProof := final[proofIndex+3:]
		salted := batch5PBKDF2SHA256([]byte(s.password), salt, iterations, sha256.Size)
		clientKey := batch5HMAC(salted, []byte("Client Key"))
		storedKey := sha256.Sum256(clientKey)
		authMessage := clientFirstBare + "," + serverFirst + "," + withoutProof
		clientSignature := batch5HMAC(storedKey[:], []byte(authMessage))
		expectedProof := make([]byte, len(clientKey))
		for index := range clientKey {
			expectedProof[index] = clientKey[index] ^ clientSignature[index]
		}
		if encodedProof != base64.StdEncoding.EncodeToString(expectedProof) {
			return fmt.Errorf("SCRAM proof mismatch")
		}
		serverKey := batch5HMAC(salted, []byte("Server Key"))
		serverSignature := batch5HMAC(serverKey, []byte(authMessage))
		finalPayload := make([]byte, 4)
		binary.BigEndian.PutUint32(finalPayload[:4], 12)
		finalPayload = append(finalPayload, []byte("v="+base64.StdEncoding.EncodeToString(serverSignature))...)
		if err := batch5PGTestWriteMessage(writer, 'R', finalPayload); err != nil {
			return err
		}
		return batch5PGTestWriteMessage(writer, 'R', []byte{0, 0, 0, 0})
	default:
		return batch5PGTestWriteMessage(writer, 'R', []byte{0, 0, 0, 0})
	}
}

func batch5PGTestParseQuery(payload []byte) (string, error) {
	if len(payload) < 1 {
		return "", fmt.Errorf("short Parse")
	}
	nameEnd := bytes.IndexByte(payload, 0)
	if nameEnd < 0 {
		return "", fmt.Errorf("malformed Parse statement name")
	}
	rest := payload[nameEnd+1:]
	queryEnd := bytes.IndexByte(rest, 0)
	if queryEnd < 0 {
		return "", fmt.Errorf("malformed Parse query")
	}
	return string(rest[:queryEnd]), nil
}

func batch5PGTestParseBind(payload []byte) []string {
	rest := payload
	for skip := 0; skip < 2; skip++ { // portal name, statement name
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil
		}
		rest = rest[end+1:]
	}
	if len(rest) < 2 {
		return nil
	}
	formatCount := int(binary.BigEndian.Uint16(rest[:2]))
	rest = rest[2+2*formatCount:]
	if len(rest) < 2 {
		return nil
	}
	paramCount := int(binary.BigEndian.Uint16(rest[:2]))
	rest = rest[2:]
	params := make([]string, 0, paramCount)
	for index := 0; index < paramCount; index++ {
		if len(rest) < 4 {
			return params
		}
		length := int(int32(binary.BigEndian.Uint32(rest[:4])))
		rest = rest[4:]
		if length < 0 {
			params = append(params, "<NULL>")
			continue
		}
		if length > len(rest) {
			return params
		}
		params = append(params, string(rest[:length]))
		rest = rest[length:]
	}
	return params
}

// ---------------------------------------------------------------- test kit ---

func batch5PGRegistry(t *testing.T, dsn string) *Registry {
	t.Helper()
	return newBatch5Lane2Registry(WithGetenv(func(name string) string {
		if name == "DATABASE_URL" {
			return dsn
		}
		return ""
	}))
}

func batch5ExecuteRegistry(t *testing.T, registry *Registry, tool string, input map[string]any) map[string]any {
	t.Helper()
	result, err := registry.Execute(context.Background(), tool, input)
	if err != nil {
		t.Fatalf("%s.Execute: %v", tool, err)
	}
	mapped, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("%s.Execute returned %T, want map[string]any", tool, result)
	}
	return mapped
}

func batch5AssertNoError(t *testing.T, result map[string]any) {
	t.Helper()
	if message, failed := result["error"]; failed {
		t.Fatalf("unexpected error: %v", message)
	}
}

func batch5AssertErrorContains(t *testing.T, result map[string]any, want string) {
	t.Helper()
	message, failed := result["error"]
	if !failed {
		t.Fatalf("expected an error containing %q, got %v", want, result)
	}
	if !strings.Contains(fmt.Sprint(message), want) {
		t.Fatalf("error = %q, want substring %q", message, want)
	}
}

// ------------------------------------------------------------------- tests ---

// Parity: tools/tests/tools/test_postgres_tool.py::test_simple_select
func TestBatch5Lane2PGQuery(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", func(query string) *batch5PGTestResult {
		if strings.Contains(query, "FROM users") {
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "id", typeOID: 23},
					{name: "name", typeOID: 25},
					{name: "active", typeOID: 16},
					{name: "score", typeOID: 701},
					{name: "meta", typeOID: 3802},
					{name: "missing", typeOID: 25},
				},
				rows: [][]any{{"7", "Alice", "t", "42.5", `{"a":1}`, nil}},
			}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query",
		"sql":    "SELECT * FROM users",
	})
	batch5AssertNoError(t, result)
	if result["row_count"] != 1 || result["max_rows"] != 1000 || result["success"] != true {
		t.Fatalf("unexpected envelope: %v", result)
	}
	columns, _ := result["columns"].([]string)
	if len(columns) != 6 || columns[0] != "id" || columns[5] != "missing" {
		t.Fatalf("columns = %v", result["columns"])
	}
	rows, _ := result["rows"].([][]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", result["rows"])
	}
	if rows[0][0] != int64(7) || rows[0][1] != "Alice" || rows[0][2] != true || rows[0][3] != 42.5 {
		t.Fatalf("typed row = %#v", rows[0])
	}
	meta, _ := rows[0][4].(map[string]any)
	if meta["a"] != float64(1) || rows[0][5] != nil {
		t.Fatalf("json/null row = %#v", rows[0])
	}
	if _, hasDuration := result["duration_ms"]; !hasDuration {
		t.Fatalf("duration_ms missing: %v", result)
	}
	queries := strings.Join(server.recordedQueries(), "\n")
	for _, want := range []string{"SET statement_timeout TO 3000", "SET default_transaction_read_only = on", "SELECT * FROM users"} {
		if !strings.Contains(queries, want) {
			t.Fatalf("server did not receive %q; got:\n%s", want, queries)
		}
	}
	if errs := server.errors(); len(errs) > 0 {
		t.Fatalf("fake server errors: %v", errs)
	}
}

// Parity: tools/tests/tools/test_postgres_tool.py::test_simple_select
func TestBatch5Lane2PGQueryParams(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", func(query string) *batch5PGTestResult {
		if strings.Contains(query, "FROM users") {
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "id", typeOID: 23}},
				rows:    [][]any{{"42"}},
			}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query",
		"sql":    "SELECT * FROM users WHERE id = %(id)s AND name = %(name)s",
		"params": map[string]any{"id": 42, "name": "Alice"},
	})
	batch5AssertNoError(t, result)
	queries := server.recordedQueries()
	joined := strings.Join(queries, "\n")
	if !strings.Contains(joined, "WHERE id = $1 AND name = $2") {
		t.Fatalf("placeholders not rewritten: %v", queries)
	}
	params := server.recordedParams()
	if len(params) != 1 || len(params[0]) != 2 || params[0][0] != "42" || params[0][1] != "Alice" {
		t.Fatalf("bound params = %#v", params)
	}
}

// Parity: tools/tests/tools/test_postgres_tool.py::test_invalid_sql_returns_error
func TestBatch5Lane2PGSQLGuard(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", nil)
	registry := batch5PGRegistry(t, server.dsn())
	cases := []struct {
		name string
		sql  string
		want string
	}{
		{name: "not select", sql: "DELETE FROM users", want: "Only SELECT queries are allowed"},
		{name: "multiple", sql: "SELECT 1; SELECT 2", want: "Multiple statements are not allowed"},
		{name: "keyword", sql: "SELECT * FROM t WHERE note = 'update'", want: "Forbidden SQL keyword detected"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
				"action": "pg_query",
				"sql":    testCase.sql,
			})
			batch5AssertErrorContains(t, result, testCase.want)
		})
	}
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_explain",
		"sql":    "UPDATE users SET name = 'x'",
	})
	batch5AssertErrorContains(t, result, "Only SELECT queries are allowed")
	if queries := server.recordedQueries(); len(queries) != 0 {
		t.Fatalf("invalid SQL reached the server: %v", queries)
	}
}

// Parity: tools/tests/tools/test_postgres_tool.py::test_query_timeout
func TestBatch5Lane2PGQueryTimeout(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", func(query string) *batch5PGTestResult {
		if strings.HasPrefix(strings.ToUpper(query), "SELECT") {
			return &batch5PGTestResult{code: "57014", message: "canceling statement due to statement timeout"}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query",
		"sql":    "SELECT pg_sleep(10)",
	})
	if result["error"] != "Query timed out" {
		t.Fatalf("error = %v, want Query timed out", result["error"])
	}
}

// Parity-exempt: connection/bootstrap behavior against a local wire fake has no
// Python counterpart (upstream tests mock psycopg2 itself).
func TestBatch5Lane2PGMissingCredential(t *testing.T) {
	registry := newBatch5Lane2Registry(WithGetenv(func(string) string { return "" }))
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query",
		"sql":    "SELECT 1",
	})
	batch5AssertErrorContains(t, result, "Missing required credential")
	if result["success"] != false {
		t.Fatalf("success = %v", result["success"])
	}
	if help, _ := result["help"].(string); !strings.Contains(help, "postgresql://") {
		t.Fatalf("help = %v", result["help"])
	}
}

// Parity: tools/tests/tools/test_postgres_tool.py::test_list_schemas_success
func TestBatch5Lane2PGIntrospection(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", func(query string) *batch5PGTestResult {
		switch {
		case strings.Contains(query, "information_schema.schemata"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "schema_name", typeOID: 19}},
				rows:    [][]any{{"app"}, {"public"}},
			}
		case strings.Contains(query, "information_schema.tables"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "table_schema", typeOID: 19}, {name: "table_name", typeOID: 19}},
				rows:    [][]any{{"public", "users"}, {"public", "orders"}},
			}
		case strings.Contains(query, "information_schema.columns"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "column_name", typeOID: 19},
					{name: "data_type", typeOID: 19},
					{name: "is_nullable", typeOID: 19},
					{name: "column_default", typeOID: 25},
				},
				rows: [][]any{{"id", "integer", "NO", nil}, {"name", "text", "YES", "'x'"}},
			}
		case strings.Contains(query, "EXPLAIN"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "QUERY PLAN", typeOID: 25}},
				rows:    [][]any{{"Seq Scan on users"}, {"  Filter: (id = 1)"}},
			}
		case strings.Contains(query, "pg_tables"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "table_name", typeOID: 19},
					{name: "estimated_rows", typeOID: 20},
					{name: "total_size", typeOID: 25},
					{name: "index_size", typeOID: 25},
					{name: "total_bytes", typeOID: 20},
				},
				rows: [][]any{{"users", "1200", "256 kB", "64 kB", "262144"}},
			}
		case strings.Contains(query, "pg_index"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "index_name", typeOID: 19},
					{name: "columns", typeOID: 25},
					{name: "is_unique", typeOID: 16},
					{name: "is_primary", typeOID: 16},
					{name: "index_type", typeOID: 19},
					{name: "index_size", typeOID: 25},
				},
				rows: [][]any{{"users_pkey", "id", "t", "t", "btree", "16 kB"}},
			}
		case strings.Contains(query, "referenced_column"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "constraint_name", typeOID: 19},
					{name: "source_schema", typeOID: 19},
					{name: "source_table", typeOID: 19},
					{name: "source_column", typeOID: 19},
					{name: "referenced_column", typeOID: 19},
				},
				rows: [][]any{{"orders_user_fk", "public", "orders", "user_id", "id"}},
			}
		case strings.Contains(query, "ccu.table_schema AS ref_schema"):
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{
					{name: "constraint_name", typeOID: 19},
					{name: "column_name", typeOID: 19},
					{name: "ref_schema", typeOID: 19},
					{name: "ref_table", typeOID: 19},
					{name: "ref_column", typeOID: 19},
				},
				rows: [][]any{{"orders_user_fk", "user_id", "public", "users", "id"}},
			}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	ctx := context.Background()

	schemas := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{"action": "pg_list_schemas"})
	batch5AssertNoError(t, schemas)
	if got := fmt.Sprint(schemas["result"]); got != "[app public]" {
		t.Fatalf("schemas = %v", schemas["result"])
	}

	tables := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_list_tables", "schema": "public",
	})
	batch5AssertNoError(t, tables)
	tableRows, _ := tables["result"].([]any)
	if len(tableRows) != 2 {
		t.Fatalf("tables = %v", tables["result"])
	}
	if first, _ := tableRows[0].(map[string]any); first["schema"] != "public" || first["table"] != "users" {
		t.Fatalf("table row = %v", tableRows[0])
	}

	describe := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_describe_table", "schema": "public", "table": "users",
	})
	batch5AssertNoError(t, describe)
	columns, _ := describe["result"].([]any)
	if len(columns) != 2 {
		t.Fatalf("describe = %v", describe["result"])
	}
	idColumn, _ := columns[0].(map[string]any)
	if idColumn["column"] != "id" || idColumn["nullable"] != "NO" || idColumn["default"] != nil {
		t.Fatalf("column = %v", idColumn)
	}

	explain := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_explain", "sql": "SELECT * FROM users WHERE id = 1",
	})
	batch5AssertNoError(t, explain)
	if got := fmt.Sprint(explain["result"]); got != "[Seq Scan on users   Filter: (id = 1)]" {
		t.Fatalf("plan = %v", explain["result"])
	}

	stats := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{"action": "pg_get_table_stats"})
	batch5AssertNoError(t, stats)
	if stats["schema"] != "public" {
		t.Fatalf("stats schema = %v", stats["schema"])
	}
	statRows, _ := stats["result"].([]any)
	firstStat, _ := statRows[0].(map[string]any)
	if firstStat["estimated_rows"] != int64(1200) || firstStat["total_bytes"] != int64(262144) {
		t.Fatalf("stat row = %v", firstStat)
	}

	indexes := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_list_indexes", "schema": "public", "table": "users",
	})
	batch5AssertNoError(t, indexes)
	indexRows, _ := indexes["result"].([]any)
	firstIndex, _ := indexRows[0].(map[string]any)
	if firstIndex["unique"] != true || firstIndex["primary"] != true || firstIndex["columns"] != "id" {
		t.Fatalf("index row = %v", firstIndex)
	}

	fks := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_get_foreign_keys", "schema": "public", "table": "users",
	})
	batch5AssertNoError(t, fks)
	outgoing, _ := fks["outgoing"].([]any)
	incoming, _ := fks["incoming"].([]any)
	if len(outgoing) != 1 || len(incoming) != 1 {
		t.Fatalf("foreign keys = %v / %v", fks["outgoing"], fks["incoming"])
	}
	out, _ := outgoing[0].(map[string]any)
	in, _ := incoming[0].(map[string]any)
	if out["references_table"] != "users" || in["source_table"] != "orders" {
		t.Fatalf("fk rows = %v / %v", out, in)
	}

	if err := ctx.Err(); err != nil {
		t.Fatalf("context: %v", err)
	}
	if errs := server.errors(); len(errs) > 0 {
		t.Fatalf("fake server errors: %v", errs)
	}
}

// Parity-exempt: MD5 authentication is a transport concern; upstream tests
// mock psycopg2 instead of speaking the wire protocol.
func TestBatch5Lane2PGAuthMD5(t *testing.T) {
	server := batch5StartPGServer(t, "md5", "tester", "s3cret", func(query string) *batch5PGTestResult {
		if strings.HasPrefix(strings.ToUpper(query), "SELECT") {
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "ok", typeOID: 16}},
				rows:    [][]any{{"t"}},
			}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query", "sql": "SELECT 1",
	})
	batch5AssertNoError(t, result)
	if result["row_count"] != 1 {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: SCRAM-SHA-256 authentication is a transport concern; upstream
// tests mock psycopg2 instead of speaking the wire protocol.
func TestBatch5Lane2PGAuthSCRAM(t *testing.T) {
	server := batch5StartPGServer(t, "scram", "tester", "s3cret", func(query string) *batch5PGTestResult {
		if strings.HasPrefix(strings.ToUpper(query), "SELECT") {
			return &batch5PGTestResult{
				columns: []batch5PGTestColumn{{name: "value", typeOID: 23}},
				rows:    [][]any{{"1"}},
			}
		}
		return &batch5PGTestResult{}
	})
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query", "sql": "SELECT 1",
	})
	batch5AssertNoError(t, result)
	if rows, _ := result["rows"].([][]any); len(rows) != 1 || rows[0][0] != int64(1) {
		t.Fatalf("rows = %v", result["rows"])
	}
}

// Parity: tools/tests/tools/test_postgres_tool.py::test_describe_table_success
func TestBatch5Lane2PGParameterErrors(t *testing.T) {
	server := batch5StartPGServer(t, "trust", "tester", "secret", nil)
	registry := batch5PGRegistry(t, server.dsn())
	result := batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_query", "sql": "SELECT * FROM t WHERE id = %(id)s",
	})
	batch5AssertErrorContains(t, result, "missing query parameter: id")
	result = batch5ExecuteRegistry(t, registry, "postgres", map[string]any{
		"action": "pg_describe_table", "schema": "public",
	})
	batch5AssertErrorContains(t, result, "schema and table are required")
}
