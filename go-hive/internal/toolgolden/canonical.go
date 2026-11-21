// Package toolgolden canonicalises tool *result* text so the two runtimes can
// be compared.
//
// The model sees a tool result as one string: `ERROR: ` when the loop marked it
// is_error, then the tool's own body. The same logical result cannot be
// byte-compared across two processes because it carries session ids, clock
// readings, absolute paths and pids. The canonical form replaces exactly those
// values with placeholders and keeps everything else verbatim — key sets, key
// order (JSON bodies are compared as sorted trees), types, wording.
//
// Rules (docs/tool-output-contract.md §2) are mirrored by
// go_hive/scripts/tool_output_golden.py, which produced the committed
// goldens. Keep the two in sync.
package toolgolden

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
)

// Envelope is the canonical form of one tool result.
type Envelope struct {
	// IsError is the loop's marker (the leading "ERROR: ").
	IsError bool
	// Form is "json" when the body is a JSON document (optionally followed by
	// a text trailer), "text" otherwise.
	Form string
	// Value is the masked JSON tree for Form=="json" (json.Number numbers).
	Value any
	// Text is the masked body for Form=="text".
	Text string
	// Trailer is the masked text after a leading JSON document (attach_file
	// returns its text preview blocks that way).
	Trailer string
	// Reminder records whether a trailing <system-reminder> block was present.
	Reminder bool
}

// Options carries the per-capture path masks (the disposable HIVE_HOME and
// fixture workspace differ between the capture and a test run).
type Options struct {
	// Paths maps a literal directory to its placeholder, longest first is
	// applied by the caller's insertion order.
	Paths [][2]string
}

// keyMasks replaces values produced by the runtime rather than by the tool
// contract. Everything else is compared verbatim.
var keyMasks = map[string]string{
	"session_id":     "<SESSION_ID>",
	"datetime":       "<DATETIME>",
	"date":           "<DATE>",
	"time":           "<TIME>",
	"day_of_week":    "<DAY_OF_WEEK>",
	"unix_timestamp": "<UNIX_TS>",
	"created_at":     "<CREATED_AT>",
	"updated_at":     "<UPDATED_AT>",
	"started_at":     "<STARTED_AT>",
	"resolved":       "<RESOLVED_PATH>",
	"pid":            "<PID>",
	"runtime_ms":     "<RUNTIME_MS>",
	"output_handle":  "<HANDLE>",
	"job_id":         "<JOB_ID>",
	// Runtime-specific recovery advice: Python names pdfplumber/poppler, Go
	// names qpdf. The key set and the surrounding payload are the contract.
	"next_step":     "<ADVISORY>",
	"terminal_hint": "<ADVISORY>",
}

var (
	sessionIDRe   = regexp.MustCompile(`session_\d{8}_\d{6}_[0-9a-f]{8}`)
	callIDRe      = regexp.MustCompile(`call_\d+`)
	isoRe         = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	dateRe        = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
	timeRe        = regexp.MustCompile(`\b\d{2}:\d{2}:\d{2}\b`)
	unixRe        = regexp.MustCompile(`\b1[0-9]{9}\b`)
	charCountRe   = regexp.MustCompile(`returned [\d,]+ characters`)
	reminderRe    = regexp.MustCompile(`\n*<system-reminder>[\s\S]*?</system-reminder>\s*$`)
	scratchHomeRe = regexp.MustCompile(`/tmp/[^\s"']*?/(?:python|go)-home`)
	scratchWSRe   = regexp.MustCompile(`/tmp/[^\s"']*?/ws`)
)

// DefaultOptions applies the capture layout's masks (a /tmp scratch root with
// `<side>-home` and `ws` children).
func DefaultOptions() Options { return Options{} }

// WithPath adds a literal path mask, e.g. the test's TempDir → "<WS>".
func (o Options) WithPath(path, placeholder string) Options {
	o.Paths = append(o.Paths, [2]string{path, placeholder})
	return o
}

// Canonicalize returns the canonical envelope of one model-visible tool result.
func Canonicalize(content string, opts Options) Envelope {
	envelope := Envelope{}
	for strings.HasPrefix(content, "ERROR: ") {
		envelope.IsError = true
		content = strings.TrimPrefix(content, "ERROR: ")
	}
	content, envelope.Reminder = stripReminder(content)
	value, trailer, ok := splitLeadingJSON(content)
	if !ok {
		envelope.Form = "text"
		envelope.Text = maskText(strings.TrimRight(content, "\n"), opts)
		return envelope
	}
	envelope.Form = "json"
	envelope.Value = maskTree("", value, opts)
	envelope.Trailer = maskText(strings.Trim(trailer, "\n"), opts)
	return envelope
}

// Equal reports whether two envelopes are the same contract. The trailing
// <system-reminder> block is loop noise (task reminders), recorded for
// diagnostics but never part of a tool's contract.
func (e Envelope) Equal(other Envelope) bool {
	if e.IsError != other.IsError || e.Form != other.Form || e.Trailer != other.Trailer {
		return false
	}
	if e.Form == "text" {
		return e.Text == other.Text
	}
	return reflect.DeepEqual(e.Value, other.Value)
}

// FromGoldenPayload rebuilds an envelope from the canonical fields the capture
// script committed (payload is canonical JSON for Form=="json").
func FromGoldenPayload(isError bool, form string, payload string, trailer string, reminder bool) Envelope {
	envelope := Envelope{IsError: isError, Form: form, Trailer: trailer, Reminder: reminder}
	if form == "json" {
		value, _, ok := splitLeadingJSON(payload)
		if ok {
			envelope.Value = value
		}
		return envelope
	}
	envelope.Text = payload
	return envelope
}

func stripReminder(content string) (string, bool) {
	location := reminderRe.FindStringIndex(content)
	if location == nil {
		return content, false
	}
	return content[:location[0]], true
}

// splitLeadingJSON decodes one JSON document from the start of body and
// returns it with the remaining text. Numbers stay json.Number so a value's
// literal spelling is part of the comparison.
func splitLeadingJSON(body string) (any, string, bool) {
	trimmed := strings.TrimLeft(body, " \t\r\n")
	if !strings.HasPrefix(trimmed, "{") {
		return nil, "", false
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, "", false
	}
	return value, trimmed[decoder.InputOffset():], true
}

func maskTree(key string, value any, opts Options) any {
	if placeholder, masked := keyMasks[key]; masked {
		return placeholder
	}
	switch typed := value.(type) {
	case string:
		return maskText(typed, opts)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = maskTree(key, item, opts)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for member, memberValue := range typed {
			out[maskText(member, opts)] = maskTree(member, memberValue, opts)
		}
		return out
	default:
		return value
	}
}

func maskText(text string, opts Options) string {
	for _, pair := range opts.Paths {
		if pair[0] == "" {
			continue
		}
		text = strings.ReplaceAll(text, pair[0], pair[1])
	}
	text = strings.ReplaceAll(text, "/private/tmp", "/tmp")
	text = sessionIDRe.ReplaceAllString(text, "session_<ID>")
	text = callIDRe.ReplaceAllString(text, "call_<ID>")
	text = isoRe.ReplaceAllString(text, "<TIMESTAMP>")
	text = dateRe.ReplaceAllString(text, "<DATE>")
	text = timeRe.ReplaceAllString(text, "<TIME>")
	text = unixRe.ReplaceAllString(text, "<UNIX_TS>")
	text = charCountRe.ReplaceAllString(text, "returned <CHARS> characters")
	text = scratchHomeRe.ReplaceAllString(text, "<HOME>")
	text = scratchWSRe.ReplaceAllString(text, "<WS>")
	return text
}
