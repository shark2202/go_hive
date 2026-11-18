package server

// Parity: framework/server/routes_sessions.py::handle_session_events_history

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	eventsHistoryDefaultLimit              = 500
	eventsHistoryMaxLimit                  = 10000
	eventsHistoryReadTimeout               = 20 * time.Second
	eventsHistoryReverseTailThresholdBytes = 1 << 20
	eventsHistoryReverseTailChunkBytes     = 64 * 1024
	eventsHistoryMaxFieldBytes             = 64 * 1024
	eventsHistoryStrippedField             = "full_request"
)

// Parity: framework/server/routes_sessions.py::_truncate_large_event
func TruncateLargeEvent(event map[string]any) map[string]any {
	data, ok := event["data"].(map[string]any)
	if !ok {
		return event
	}
	var trimmed map[string]any
	for key, value := range data {
		if key == eventsHistoryStrippedField {
			if trimmed == nil {
				trimmed = cloneEventMap(data)
			}
			trimmed[key] = map[string]any{"_truncated": true, "_reason": "diagnostic_field"}
			continue
		}
		switch value.(type) {
		case map[string]any, []any:
		default:
			continue
		}
		size, err := pythonJSONLen(value)
		if err != nil {
			continue
		}
		if size > eventsHistoryMaxFieldBytes {
			if trimmed == nil {
				trimmed = cloneEventMap(data)
			}
			trimmed[key] = map[string]any{"_truncated": true, "_original_bytes": size}
		}
	}
	if trimmed == nil {
		return event
	}
	out := cloneEventMap(event)
	out["data"] = trimmed
	return out
}

func cloneEventMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func pythonJSONLen(value any) (int, error) {
	var buf bytes.Buffer
	if err := writePythonJSON(&buf, value); err != nil {
		return 0, err
	}
	return buf.Len(), nil
}

func writePythonJSON(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writePythonJSONString(buf, v)
	case float64:
		writePythonFloat(buf, v)
	case float32:
		writePythonFloat(buf, float64(v))
	case int:
		buf.WriteString(strconv.Itoa(v))
	case int64:
		buf.WriteString(strconv.FormatInt(v, 10))
	case json.Number:
		return writePythonNumber(buf, v)
	case []any:
		buf.WriteByte('[')
		for index, item := range v {
			if index > 0 {
				buf.WriteString(", ")
			}
			if err := writePythonJSON(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buf.WriteString(", ")
			}
			writePythonJSONString(buf, key)
			buf.WriteString(": ")
			if err := writePythonJSON(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		writePythonJSONString(buf, fmt.Sprint(v))
	}
	return nil
}

func writePythonNumber(buf *bytes.Buffer, number json.Number) error {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if integer, ok := new(big.Int).SetString(text, 10); ok {
			buf.WriteString(integer.String())
			return nil
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(value, 0) {
		return err
	}
	writePythonFloat(buf, value)
	return nil
}

func writePythonFloat(buf *bytes.Buffer, value float64) {
	switch {
	case math.IsNaN(value):
		buf.WriteString("NaN")
		return
	case math.IsInf(value, 1):
		buf.WriteString("Infinity")
		return
	case math.IsInf(value, -1):
		buf.WriteString("-Infinity")
		return
	case value == 0:
		if math.Signbit(value) {
			buf.WriteString("-0.0")
		} else {
			buf.WriteString("0.0")
		}
		return
	}
	if math.Signbit(value) {
		buf.WriteByte('-')
		value = -value
	}
	mantissa, exponentText, _ := strings.Cut(strconv.FormatFloat(value, 'e', -1, 64), "e")
	exponent, _ := strconv.Atoi(exponentText)
	digits := strings.Replace(mantissa, ".", "", 1)
	if exponent < -4 || exponent >= 16 {
		buf.WriteString(scientificFloat(digits, exponent))
		return
	}
	buf.WriteString(fixedFloat(digits, exponent+1))
}

func fixedFloat(digits string, decimalPoint int) string {
	switch {
	case decimalPoint <= 0:
		return "0." + strings.Repeat("0", -decimalPoint) + digits
	case decimalPoint >= len(digits):
		return digits + strings.Repeat("0", decimalPoint-len(digits)) + ".0"
	default:
		return digits[:decimalPoint] + "." + digits[decimalPoint:]
	}
}

func scientificFloat(digits string, exponent int) string {
	mantissa := digits[:1]
	if len(digits) > 1 {
		mantissa += "." + digits[1:]
	}
	sign := "+"
	if exponent < 0 {
		sign = "-"
		exponent = -exponent
	}
	return mantissa + "e" + sign + fmt.Sprintf("%02d", exponent)
}

func writePythonJSONString(buf *bytes.Buffer, text string) {
	buf.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		default:
			switch {
			case r < 0x20:
				fmt.Fprintf(buf, `\u%04x`, r)
			case r >= 0x20 && r <= 0x7e:
				buf.WriteRune(r)
			case r > 0xffff:
				value := r - 0x10000
				high := 0xd800 + (value >> 10)
				low := 0xdc00 + (value & 0x3ff)
				fmt.Fprintf(buf, `\u%04x\u%04x`, high, low)
			default:
				fmt.Fprintf(buf, `\u%04x`, r)
			}
		}
	}
	buf.WriteByte('"')
}

// Parity: framework/server/routes_sessions.py::_count_events_total
func countEventsTotal(eventsPath string, fileSize int64) (int, error) {
	file, err := os.Open(eventsPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	total := 0
	buf := make([]byte, 1<<20)
	for {
		read, err := file.Read(buf)
		if read > 0 {
			total += bytes.Count(buf[:read], []byte{'\n'})
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if fileSize > 0 {
		if _, err := file.Seek(-1, io.SeekEnd); err == nil {
			last := make([]byte, 1)
			if _, err := io.ReadFull(file, last); err == nil && last[0] != '\n' {
				total++
			}
		}
	}
	return total, nil
}

// Parity: framework/server/routes_sessions.py::_read_events_page
func ReadEventsPage(eventsPath string, limit int, beforeOffset *int64) ([]map[string]any, int64, int, error) {
	info, err := os.Stat(eventsPath)
	if err != nil {
		return nil, 0, 0, err
	}
	fileSize := info.Size()

	upper := fileSize
	if beforeOffset != nil {
		upper = *beforeOffset
		if upper < 0 {
			upper = 0
		}
		if upper > fileSize {
			upper = fileSize
		}
	}
	if upper == 0 {
		if beforeOffset != nil {
			return nil, 0, -1, nil
		}
		total, err := countEventsTotal(eventsPath, fileSize)
		if err != nil {
			return nil, 0, 0, err
		}
		return nil, 0, total, nil
	}

	file, err := os.Open(eventsPath)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()

	var pieces [][]byte
	newlineCount := 0
	pos := upper
	for pos > 0 && newlineCount <= limit {
		readSize := int64(eventsHistoryReverseTailChunkBytes)
		if pos < readSize {
			readSize = pos
		}
		pos -= readSize
		chunk := make([]byte, readSize)
		if _, err := file.ReadAt(chunk, pos); err != nil && err != io.EOF {
			return nil, 0, 0, err
		}
		newlineCount += bytes.Count(chunk, []byte{'\n'})
		pieces = append(pieces, chunk)
	}
	var blob []byte
	for index := len(pieces) - 1; index >= 0; index-- {
		blob = append(blob, pieces[index]...)
	}

	var lineStarts []int64
	if pos == 0 {
		lineStarts = append(lineStarts, 0)
	}
	for index, b := range blob {
		if b == '\n' {
			start := pos + int64(index) + 1
			if start < upper {
				lineStarts = append(lineStarts, start)
			}
		}
	}

	type line struct {
		start int64
		text  []byte
	}
	lines := make([]line, 0, len(lineStarts))
	for index, start := range lineStarts {
		end := upper
		if index+1 < len(lineStarts) {
			end = lineStarts[index+1] - 1
		}
		lines = append(lines, line{start: start, text: blob[start-pos : end-pos]})
	}

	nonBlank := lines[:0]
	for _, candidate := range lines {
		if len(bytes.Trim(candidate.text, " \t\n\r\v\f")) > 0 {
			nonBlank = append(nonBlank, candidate)
		}
	}
	lines = nonBlank
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}

	events := make([]map[string]any, 0, len(lines))
	for _, candidate := range lines {
		event, ok := decodeEventLine(candidate.text)
		if !ok {
			continue
		}
		events = append(events, event)
	}

	var startOffset int64
	if len(lines) > 0 {
		startOffset = lines[0].start
	}
	total := -1
	if beforeOffset == nil {
		total, err = countEventsTotal(eventsPath, fileSize)
		if err != nil {
			return nil, 0, 0, err
		}
	}
	return events, startOffset, total, nil
}

func decodeEventLine(text []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	var event map[string]any
	if err := decoder.Decode(&event); err != nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return event, true
}

func queryIntParam(r *http.Request, name string, fallback int) (int, bool) {
	value, ok := parsePythonInt(r.URL.Query().Get(name))
	if !ok {
		return fallback, false
	}
	return value, true
}

func parsePythonInt(raw string) (int, bool) {
	text := strings.TrimFunc(raw, unicode.IsSpace)
	negative := false
	if text != "" {
		switch text[0] {
		case '+':
			text = text[1:]
		case '-':
			negative = true
			text = text[1:]
		}
	}
	if text == "" {
		return 0, false
	}
	var digits strings.Builder
	for index := 0; index < len(text); index++ {
		c := text[index]
		if c == '_' {
			if index == 0 || index == len(text)-1 {
				return 0, false
			}
			if !isASCIIDigit(text[index-1]) || !isASCIIDigit(text[index+1]) {
				return 0, false
			}
			continue
		}
		if !isASCIIDigit(c) {
			return 0, false
		}
		digits.WriteByte(c)
	}
	value, err := strconv.Atoi(digits.String())
	if err != nil {
		return 0, false
	}
	if negative {
		value = -value
	}
	return value, true
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func handleSessionEventsHistory(w http.ResponseWriter, r *http.Request, deps SessionRouteDeps) {
	sessionID := r.PathValue("session_id")

	limit, _ := queryIntParam(r, "limit", eventsHistoryDefaultLimit)
	if limit == 0 {
		limit = eventsHistoryDefaultLimit
	}
	if limit < 1 {
		limit = 1
	}
	if limit > eventsHistoryMaxLimit {
		limit = eventsHistoryMaxLimit
	}

	var beforeOffset *int64
	if value, ok := queryIntParam(r, "before_offset", 0); ok {
		offset := int64(value)
		beforeOffset = &offset
	}
	beforeIndex, hasBeforeIndex := queryIntParam(r, "before_index", 0)

	empty := func() {
		writeJSON(w, http.StatusOK, map[string]any{
			"events":         []map[string]any{},
			"session_id":     sessionID,
			"total":          0,
			"returned":       0,
			"truncated":      false,
			"limit":          limit,
			"start_index":    0,
			"start_offset":   0,
			"has_more_older": false,
		})
	}

	queenDir, err := FindQueenSessionDir(deps.Home, sessionID)
	if err != nil {
		empty()
		return
	}
	eventsPath := filepath.Join(queenDir, "events.jsonl")
	if _, statErr := os.Stat(eventsPath); statErr != nil {
		empty()
		return
	}

	readPage := deps.EventsReadPage
	if readPage == nil {
		readPage = ReadEventsPage
	}
	timeout := deps.EventsReadTimeout
	if timeout <= 0 {
		timeout = eventsHistoryReadTimeout
	}

	type pageResult struct {
		events      []map[string]any
		startOffset int64
		total       int
		err         error
	}
	results := make(chan pageResult, 1)
	go func() {
		events, startOffset, total, err := readPage(eventsPath, limit, beforeOffset)
		results <- pageResult{events: events, startOffset: startOffset, total: total, err: err}
	}()

	var result pageResult
	select {
	case result = <-results:
	case <-time.After(timeout):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":      "events_history_timeout",
			"session_id": sessionID,
		})
		return
	}
	if result.err != nil {
		empty()
		return
	}

	events := make([]map[string]any, 0, len(result.events))
	for _, event := range result.events {
		events = append(events, TruncateLargeEvent(event))
	}
	returned := len(events)

	startIndex := 1
	switch {
	case beforeOffset == nil:
		if returned > 0 {
			startIndex = result.total - returned + 1
			if startIndex < 1 {
				startIndex = 1
			}
		}
	case hasBeforeIndex:
		startIndex = beforeIndex - returned
		if startIndex < 1 {
			startIndex = 1
		}
	}

	for index, event := range events {
		event["seq"] = startIndex + index
	}

	truncated := false
	if result.total >= 0 {
		truncated = result.total > returned
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":         events,
		"session_id":     sessionID,
		"total":          result.total,
		"returned":       returned,
		"truncated":      truncated,
		"limit":          limit,
		"start_index":    startIndex,
		"start_offset":   result.startOffset,
		"has_more_older": result.startOffset > 0,
	})
}
