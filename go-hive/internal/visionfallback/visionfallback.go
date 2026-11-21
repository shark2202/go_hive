// Package visionfallback captions tool-result images for a text-only main
// model.
//
// When a tool returns image content and the main agent's model cannot accept
// image blocks, the framework would otherwise strip the images and leave the
// model blind to what they show. The configured "vision_fallback" slot is a
// vision model that describes the image in service of the agent's intent —
// including approximate viewport-fraction coordinates for the interactive
// elements it names, so a text-only agent can still drive its click / hover /
// key-press tools.
//
// Every helper here degrades rather than raises: a captioning failure must
// never kill the main agent's run.
//
// Parity: framework/agent_loop/internals/vision_fallback.py
package visionfallback

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/llm"
)

// CaptionTimeout bounds one captioning round trip. The upstream default is 30s;
// a slower vision model must not hold the agent's turn open indefinitely.
const CaptionTimeout = 30 * time.Second

// intentMaxChars caps the intent string handed to the vision subagent: it only
// needs the agent's recent reasoning plus the tool descriptor.
const intentMaxChars = 4096

// toolArgsMaxChars caps the tool-args JSON snippet inside the intent.
const toolArgsMaxChars = 4096

// systemPrompt is the vision subagent's instruction. Coordinate labeling is the
// important half: the main agent drives left_click / hover / key with VIEWPORT
// FRACTIONS in [0..1], so every interactive element the caption names carries
// its approximate centre as "(fx, fy)".
//
// Parity: framework/agent_loop/internals/vision_fallback.py::_VISION_SUBAGENT_SYSTEM
const systemPrompt = "You are a vision subagent for a text-only main agent. The main " +
	"agent invoked a tool that returned the image(s) attached. Their " +
	"intent (their reasoning + the tool call) is below. The intent " +
	"names a specific entity and target element — anchor your " +
	"description on that entity, locate the target element, and " +
	"provide its coordinates. Describe what the image shows in service " +
	"of their intent — concrete, factual, no speculation. If their " +
	"intent asks a yes/no question, answer it directly first.\n\n" +
	"Coordinate labeling: the main agent uses fractional viewport " +
	"coordinates (x, y) in [0..1] — (0, 0) is the top-left of the " +
	"image, (1, 1) is the bottom-right — to drive its click / hover / " +
	"key-press tools. For every interactive element you mention " +
	"(button, link, input, checkbox, radio, dropdown, tab, menu item, " +
	"dialog control, icon), append its approximate centre as " +
	"``(fx, fy)`` immediately after the element's name or label, e.g. " +
	"``\"Submit\" button (0.83, 0.92)`` or ``profile avatar icon " +
	"(0.05, 0.07)``. Use two decimal places — more is false precision. " +
	"Skip coordinates for pure body text and decorative elements that " +
	"aren't clickable. If an element is partially off-screen or you " +
	"cannot reliably locate its centre, omit the coordinate rather " +
	"than guessing.\n\n" +
	"Output plain text, no markdown, ≤ 600 words."

// CaptionMarker prefixes the caption spliced into a tool result, mirroring
// upstream's "[vision-fallback caption]" marker.
const CaptionMarker = "[vision-fallback caption]"

// ExhaustedMarker replaces the images when captioning could not produce a
// caption, so the model knows the image was dropped instead of silently
// believing it saw nothing.
const ExhaustedMarker = "[image stripped — vision fallback exhausted]"

// coordLabelRe matches the (fx, fy) labels the vision subagent appends to
// element names, e.g. “"Submit" button (0.83, 0.92)“. Both components are
// fractions in 0..1 (a bare 0 or 1 is allowed too).
var coordLabelRe = regexp.MustCompile(`\(\s*(\d?\.\d+|[01])\s*,\s*(\d?\.\d+|[01])\s*\)`)

// Captioner is a configured vision-fallback model.
type Captioner struct {
	// Provider is the completion transport for the fallback model.
	Provider llm.Completer
	// Model is the configured provider/model, reported in logs by callers.
	Model string
	// MaxTokens is the caption budget; <=0 uses the process aux budget.
	MaxTokens int
	// Timeout bounds one call; <=0 uses CaptionTimeout.
	Timeout time.Duration
	// Resolve, when set, resolves the live vision_fallback slot before every
	// caption. Upstream reads configuration.json per caption call, so a slot
	// PUT applies to the next tool image without a session rebuild.
	Resolve Resolver
}

// Resolver returns the currently configured fallback transport, its model and
// its caption budget. It reports ok=false when the slot is absent or unusable
// (no provider, no resolvable key), in which case the captioner reports no
// caption and the loop drops the images with the exhausted marker — the same
// outcome upstream's per-call “caption_tool_image“ produces.
type Resolver func() (provider llm.Completer, model string, maxTokens int, ok bool)

// New returns a captioner for a configured slot, or nil when there is no slot
// or no transport to reach it.
func New(provider llm.Completer, model string, maxTokens int) *Captioner {
	if provider == nil || strings.TrimSpace(model) == "" {
		return nil
	}
	return &Captioner{Provider: provider, Model: model, MaxTokens: maxTokens}
}

// NewResolver returns a captioner that re-resolves its slot per caption. It is
// the live-session form: the session keeps one captioner and a model-slot PUT
// is picked up by the next tool image.
func NewResolver(resolve Resolver) *Captioner {
	if resolve == nil {
		return nil
	}
	return &Captioner{Resolve: resolve}
}

// resolve returns the transport and budget this caption must use.
func (c *Captioner) resolve() (llm.Completer, int, bool) {
	provider, model, maxTokens := c.Provider, c.Model, c.MaxTokens
	if c.Resolve != nil {
		var ok bool
		provider, model, maxTokens, ok = c.Resolve()
		if !ok {
			return nil, 0, false
		}
	}
	if provider == nil || strings.TrimSpace(model) == "" {
		return nil, 0, false
	}
	return provider, maxTokens, true
}

// Caption asks the vision model to describe the images in service of intent.
// It reports false on any failure — no provider, no deliverable image, empty
// response, transport error or timeout — so the caller falls back to the
// exhausted marker instead of failing the turn.
func (c *Captioner) Caption(ctx context.Context, intent string, images []map[string]any) (string, bool) {
	if c == nil {
		return "", false
	}
	provider, configuredMaxTokens, ok := c.resolve()
	if !ok {
		return "", false
	}
	blocks := deliverableImageBlocks(images)
	if len(blocks) == 0 {
		return "", false
	}
	parts := make([]map[string]any, 0, len(blocks)+1)
	parts = append(parts, map[string]any{"type": "text", "text": intent})
	parts = append(parts, blocks...)

	maxTokens := configuredMaxTokens
	if maxTokens <= 0 {
		maxTokens = llm.AuxMaxTokens()
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CaptionTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	response, err := provider.AComplete(callCtx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: intent, ContentParts: parts}},
		System:    systemPrompt,
		MaxTokens: maxTokens,
	})
	if err != nil {
		return "", false
	}
	caption := strings.TrimSpace(response.Content)
	if caption == "" {
		return "", false
	}
	return caption, true
}

// deliverableImageBlocks keeps only the blocks a vision endpoint can actually
// fetch: data: URIs. A hive:// (or any other non-fetchable) URL is dropped
// rather than sent, because it would fail the whole request with a format
// error.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::caption_tool_image
func deliverableImageBlocks(images []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(images))
	for _, block := range images {
		switch blockType, _ := block["type"].(string); blockType {
		case "image_url":
			if url := imageURLOf(block); strings.HasPrefix(url, "data:image/") || strings.HasPrefix(url, "data:application/pdf") {
				out = append(out, block)
			}
		case "file":
			if fileDataOf(block); fileDataData(fileDataOf(block)) {
				out = append(out, block)
			}
		}
	}
	return out
}

func fileDataData(data string) bool { return strings.HasPrefix(data, "data:application/pdf") }

func imageURLOf(block map[string]any) string {
	imageURL, _ := block["image_url"].(map[string]any)
	url, _ := imageURL["url"].(string)
	return url
}

func fileDataOf(block map[string]any) string {
	file, _ := block["file"].(map[string]any)
	data, _ := file["file_data"].(string)
	return data
}

// IntentForTool builds the intent string handed to the vision subagent.
//
// An explicit "intent" tool argument wins (the browser tools take a required
// --intent); a hive-browser command carrying --intent=… inside a terminal_exec
// argument is picked out next; otherwise the tool descriptor plus the most
// recent assistant text (truncated) carries the agent's reasoning.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::extract_intent_for_tool
func IntentForTool(toolName string, toolArgs map[string]any, lastAssistantText func() string) string {
	explicit := strings.TrimSpace(stringArg(toolArgs, "intent"))
	if explicit == "" {
		if command := stringArg(toolArgs, "command"); strings.Contains(command, "hive-browser") {
			if match := intentFlagRe.FindStringSubmatch(command); len(match) > 1 {
				explicit = strings.Trim(strings.TrimSpace(match[1]), "\"'")
			}
		}
	}
	if explicit != "" {
		return "Agent intent: " + explicit
	}

	argsJSON := jsonForIntent(toolArgs)
	toolLine := "Called: " + toolName + "(" + argsJSON + ")"

	assistantText := ""
	if lastAssistantText != nil {
		assistantText = strings.TrimSpace(lastAssistantText())
	}
	if assistantText == "" {
		assistantText = "<no preceding reasoning>"
	}

	// Lengths are counted in characters, as upstream's len() does, so a
	// multi-byte tool call or reasoning block truncates at the same point.
	head := toolLine + "\n\nReasoning before call:\n"
	budget := intentMaxChars - utf8.RuneCountInString(head)
	if budget < 100 {
		return truncateRunes(head, intentMaxChars)
	}
	if utf8.RuneCountInString(assistantText) > budget {
		assistantText = truncateRunes(assistantText, budget-1) + "…"
	}
	return head + assistantText
}

var intentFlagRe = regexp.MustCompile(`--intent(?:=|\s+)("[^"]*"|'[^']*'|\S+)`)

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

// jsonForIntent renders tool args as the reference implementation sends them:
// Python's json.dumps defaults — ", " / ": " separators and ensure_ascii=True.
// The intent is prompt text the vision model reads, so the bytes should match
// what upstream sends for the same tool call. Maps are emitted with sorted
// keys because Go's map iteration order is random; upstream preserves the
// model's key order, so a multi-key args object can still order differently.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::extract_intent_for_tool
func jsonForIntent(value any) string {
	var builder strings.Builder
	writePythonJSON(&builder, value)
	text := builder.String()
	if utf8.RuneCountInString(text) > toolArgsMaxChars {
		return truncateRunes(text, toolArgsMaxChars) + "…"
	}
	return text
}

func writePythonJSON(builder *strings.Builder, value any) {
	switch typed := value.(type) {
	case nil:
		builder.WriteString("null")
	case bool:
		if typed {
			builder.WriteString("true")
		} else {
			builder.WriteString("false")
		}
	case string:
		writePythonJSONString(builder, typed)
	case json.Number:
		builder.WriteString(typed.String())
	case float64:
		builder.WriteString(pythonNumber(typed))
	case float32:
		builder.WriteString(pythonNumber(float64(typed)))
	case int:
		builder.WriteString(strconv.Itoa(typed))
	case int64:
		builder.WriteString(strconv.FormatInt(typed, 10))
	case []any:
		builder.WriteString("[")
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonJSON(builder, item)
		}
		builder.WriteString("]")
	case []string:
		builder.WriteString("[")
		for index, item := range typed {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonJSONString(builder, item)
		}
		builder.WriteString("]")
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		builder.WriteString("{")
		for index, key := range keys {
			if index > 0 {
				builder.WriteString(", ")
			}
			writePythonJSONString(builder, key)
			builder.WriteString(": ")
			writePythonJSON(builder, typed[key])
		}
		builder.WriteString("}")
	default:
		// Python's json.dumps(default=str) coerces unknown objects with str().
		writePythonJSONString(builder, fmt.Sprint(value))
	}
}

// pythonNumber mirrors Python's float/int repr for the values a JSON-parsed
// tool call carries: an integral float prints without a trailing ".0" (the
// common case — the model writes 10, not 10.0), everything else uses the
// shortest round-trip form.
func pythonNumber(value float64) string {
	if math.IsInf(value, 0) || math.IsNaN(value) {
		return "null"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e16 {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// writePythonJSONString escapes like Python's json.dumps(ensure_ascii=True):
// the JSON shortcuts for \n 	 \r \b \f, \u00XX for other control characters,
// and \uXXXX (lowercase, surrogate pairs above the BMP) for every non-ASCII
// rune. Go's json.Marshal would keep UTF-8 and escape <, > and & instead.
func writePythonJSONString(builder *strings.Builder, text string) {
	builder.WriteByte('"')
	for _, r := range text {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '	':
			builder.WriteString("\\t")
		case '\b':
			builder.WriteString(`\b`)
		case '\f':
			builder.WriteString(`\f`)
		default:
			switch {
			case r < 0x20 || r == 0x7f:
				// Python's C encoder escapes every non-printable ASCII byte,
				// including DEL (0x7f).
				fmt.Fprintf(builder, `\u%04x`, r)
			case r < 0x80:
				builder.WriteRune(r)
			case r <= 0xFFFF:
				fmt.Fprintf(builder, `\u%04x`, r)
			default:
				r -= 0x10000
				high := 0xD800 + (r >> 10)
				low := 0xDC00 + (r & 0x3FF)
				fmt.Fprintf(builder, `\u%04x\u%04x`, high, low)
			}
		}
	}
	builder.WriteByte('"')
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// CropBoxFromToolResult pulls a crop_box ([vx0, vy0, vx1, vy1]) out of an image
// tool's JSON text envelope: the viewport-fraction rectangle a non-viewport
// image (zoom crop, element clip or full_page screenshot) spans. It reports
// false for a plain viewport screenshot (no crop_box, or one covering the whole
// viewport) and for any parse failure.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::crop_box_from_tool_result
func CropBoxFromToolResult(toolResultText string) ([4]float64, bool) {
	if strings.TrimSpace(toolResultText) == "" {
		return [4]float64{}, false
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(toolResultText)), &meta); err != nil {
		return [4]float64{}, false
	}
	raw, ok := meta["crop_box"].([]any)
	if !ok || len(raw) != 4 {
		return [4]float64{}, false
	}
	box := [4]float64{}
	for i, item := range raw {
		value, ok := numberAsFloat(item)
		if !ok {
			return [4]float64{}, false
		}
		box[i] = value
	}
	if box[2]-box[0] <= 0 || box[3]-box[1] <= 0 {
		return [4]float64{}, false
	}
	if math.Abs(box[0]) < 1e-6 && math.Abs(box[1]) < 1e-6 && math.Abs(box[2]-1) < 1e-6 && math.Abs(box[3]-1) < 1e-6 {
		return [4]float64{}, false
	}
	return box, true
}

func numberAsFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		parsed, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

// RemapCaptionForCrop rewrites the subagent's (fx, fy) labels from
// crop-relative fractions into full-viewport fractions.
//
// The subagent only ever sees "an image"; when that image was a crop, its
// coordinates are relative to the crop. A coordinate click expects viewport
// fractions, so without this remap the agent would click the right fraction of
// the wrong rectangle. A no-op when the result carries no crop_box or the
// caption has no coordinate labels.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::remap_caption_for_crop
func RemapCaptionForCrop(caption, toolResultText string) string {
	box, ok := CropBoxFromToolResult(toolResultText)
	if !ok {
		return caption
	}
	width := box[2] - box[0]
	height := box[3] - box[1]
	return coordLabelRe.ReplaceAllStringFunc(caption, func(label string) string {
		match := coordLabelRe.FindStringSubmatch(label)
		if len(match) < 3 {
			return label
		}
		fx, errX := strconv.ParseFloat(match[1], 64)
		fy, errY := strconv.ParseFloat(match[2], 64)
		if errX != nil || errY != nil {
			return label
		}
		if fx < 0 || fx > 1 || fy < 0 || fy > 1 {
			return label
		}
		return "(" + round4(box[0]+fx*width) + ", " + round4(box[1]+fy*height) + ")"
	})
}

func round4(value float64) string {
	return strconv.FormatFloat(math.Round(value*10000)/10000, 'f', -1, 64)
}
