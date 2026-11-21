// PostgreSQL wire queries and decoding: simple/extended protocol,
// placeholder conversion, RowDescription/DataRow parsing and type-aware
// value decoding (split from pgwire.go).

package integrations

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

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
