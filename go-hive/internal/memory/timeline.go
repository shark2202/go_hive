package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

const (
	TimelineFile          = "timeline.jsonl"
	TimelineMetaFile      = "timeline_meta.json"
	TimelineSchemaVersion = 1

	TimelineBatchChars   = 12_000
	TimelineMessageChars = 2_000
)

var TimelineKinds = []string{"event", "fact", "plan"}

var (
	TimelineLiveBatchMessages = envInt("HIVE_TIMELINE_BATCH", 3)
	TimelineLiveCooldown      = time.Duration(envFloat("HIVE_TIMELINE_COOLDOWN", 300) * float64(time.Second))
)

const TimelineSystemPrompt = `You build a dated timeline of what a user has told an assistant about their life.

The input is one or more conversation sessions. Each has a label, the date it took place, and the user's messages, numbered.

Extract every concrete event, fact and plan the user states about themselves or the people and things in
their life: what they did, bought, sold, attended, visited, made, fixed, learned, received, spent, counted or
measured; facts about their situation (work, home, possessions, routines, health, relationships, pets,
preferences, quantities); and plans. One item per distinct occurrence: two separate weddings are two items.
Skip requests, questions and chit-chat that state nothing about the user.

For each item give:
- "session": the session label, e.g. "S2"
- "turn": the number of the message it came from
- "kind": "event" (it happened), "fact" (a state or preference as of that date) or "plan" (intended, future)
- "text": one self-contained sentence about "the user" (never "I") naming what, where, with whom, and any
  number, amount, price or duration exactly as stated
- "start", "end": the dates (YYYY-MM-DD) it happened or holds, resolved against the session date.
  "yesterday" is the day before; "last week" is the previous Monday to Sunday; "two weeks ago" is that week;
  "this month" is the 1st through the session date; "recently" or "just" is the 7 days up to the session date.
  With no time given, use the session date for both. For plans, use the intended date if stated, otherwise
  the session date.
- "aliases": 2 to 4 short rephrasings in different words (synonyms, the category it belongs to) so a keyword
  search finds it

Reply with JSON only: {"items": [...]}, or {"items": []} when there is nothing.`

type TimelineMessage struct {
	Turn int
	Text string
}

type SessionText struct {
	SessionID string
	StartedAt time.Time
	Messages  []TimelineMessage
}

type TimelineItem struct {
	Turn    int      `json:"turn"`
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Start   string   `json:"start"`
	End     string   `json:"end"`
	Aliases []string `json:"aliases"`
}

type TimelineMeta struct {
	Version   int    `json:"version"`
	UserTurns int    `json:"user_turns"`
	Model     string `json:"model"`
	UpdatedAt string `json:"updated_at"`
}

var sessionStartedRe = regexp.MustCompile(`^session_([0-9]{8})_([0-9]{6})_`)

func SessionStartedAt(sessionID string) (time.Time, bool) {
	m := sessionStartedRe.FindStringSubmatch(sessionID)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("20060102_150405", m[1]+"_"+m[2], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func UserMessages(sessionDir string) []string {
	f, err := os.Open(filepath.Join(sessionDir, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()

	out := []string{}
	reader := bufio.NewReader(f)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 && strings.Contains(line, `"client_input_received"`) {
			var event map[string]any
			if json.Unmarshal([]byte(line), &event) == nil {
				data, _ := event["data"].(map[string]any)
				content, isString := data["content"].(string)
				if event["type"] == "client_input_received" && isString && strings.TrimSpace(content) != "" {
					out = append(out, content)
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return out
}

func ReadTimelineMeta(sessionDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(sessionDir, TimelineMetaFile))
	if err != nil {
		return map[string]any{}
	}
	meta := map[string]any{}
	if err := json.Unmarshal(data, &meta); err != nil {
		return map[string]any{}
	}
	return meta
}

func MarkExtracted(sessionDir string) error {
	return appendTimelineItems(sessionDir, nil, len(UserMessages(sessionDir)), "")
}

func appendTimelineItems(sessionDir string, items []TimelineItem, userTurns int, model string) error {
	if len(items) > 0 {
		path := filepath.Join(sessionDir, TimelineFile)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		for _, item := range items {
			data, err := marshalTimelineJSON(item)
			if err != nil {
				_ = f.Close()
				return err
			}
			if _, err := f.Write(append(data, '\n')); err != nil {
				_ = f.Close()
				return err
			}
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	meta := TimelineMeta{
		Version:   TimelineSchemaVersion,
		UserTurns: userTurns,
		Model:     model,
		UpdatedAt: time.Now().Format("2006-01-02T15:04:05"),
	}
	data, err := marshalTimelineJSON(meta)
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(filepath.Join(sessionDir, TimelineMetaFile), data, 0o644)
}

func marshalTimelineJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func TimelineBatches(sessions []SessionText) [][]SessionText {
	batches := [][]SessionText{}
	current := []SessionText{}
	size := 0
	for _, s := range sessions {
		part := []TimelineMessage{}
		partSize := 0
		for _, msg := range s.Messages {
			text := truncateRunes(msg.Text, TimelineMessageChars)
			textLen := utf8.RuneCountInString(text)
			if len(part) > 0 && size+partSize+textLen > TimelineBatchChars {
				current = append(current, SessionText{SessionID: s.SessionID, StartedAt: s.StartedAt, Messages: part})
				batches = append(batches, current)
				current, size, part, partSize = []SessionText{}, 0, []TimelineMessage{}, 0
			}
			part = append(part, TimelineMessage{Turn: msg.Turn, Text: text})
			partSize += textLen
		}
		if len(part) > 0 {
			current = append(current, SessionText{SessionID: s.SessionID, StartedAt: s.StartedAt, Messages: part})
			size += partSize
		}
		if size >= TimelineBatchChars {
			batches = append(batches, current)
			current, size = []SessionText{}, 0
		}
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func TimelinePrompt(batch []SessionText) (string, map[string]SessionText) {
	labels := make(map[string]SessionText, len(batch))
	blocks := make([]string, 0, len(batch))
	for i, s := range batch {
		label := "S" + itoa(i+1)
		labels[label] = s
		lines := []string{
			"Session " + label + ", " + s.StartedAt.Format("2006-01-02") +
				" (" + s.StartedAt.Weekday().String() + ")",
		}
		for _, msg := range s.Messages {
			lines = append(lines, "["+itoa(msg.Turn)+"] "+strings.Join(strings.Fields(msg.Text), " "))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n"), labels
}

var timelineFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func ParseTimelineJSON(text string) map[string]any {
	text = strings.TrimSpace(text)
	if m := timelineFenceRe.FindStringSubmatch(text); m != nil {
		text = m[1]
	}
	runes := []rune(text)
	start := -1
	for i, r := range runes {
		if r == '{' {
			start = i
			break
		}
	}
	if start == -1 {
		return map[string]any{}
	}
	end := -1
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '}' {
			end = i
			break
		}
	}
	if end <= start {
		return map[string]any{}
	}
	var data any
	if err := json.Unmarshal([]byte(string(runes[start:end+1])), &data); err != nil {
		return map[string]any{}
	}
	if obj, ok := data.(map[string]any); ok {
		return obj
	}
	return map[string]any{}
}

func parseISODatePrefix(value string) (time.Time, bool) {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 10 {
		runes = runes[:10]
	}
	text := string(runes)
	if t, err := time.Parse("2006-01-02", text); err == nil {
		return t, true
	}
	if t, err := time.Parse("20060102", text); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func asTimelineDate(value any, fallback time.Time) time.Time {
	if s, ok := value.(string); ok {
		if d, ok := parseISODatePrefix(s); ok {
			return d
		}
	}
	return fallback
}

func timelineKindValid(kind string) bool {
	for _, k := range TimelineKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func cleanTimelineItem(raw any, labels map[string]SessionText) (string, TimelineItem, bool) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", TimelineItem{}, false
	}

	sessionLabel := ""
	if v, exists := obj["session"]; exists {
		sessionLabel = pyStr(v)
	}
	session, found := labels[strings.TrimSpace(sessionLabel)]

	text := ""
	if textRaw, exists := obj["text"]; exists && pyTruthy(textRaw) {
		text = strings.TrimSpace(pyStr(textRaw))
	}
	if !found || text == "" || len(session.Messages) == 0 {
		return "", TimelineItem{}, false
	}

	y, m, d := session.StartedAt.Date()
	saidOn := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	start := asTimelineDate(obj["start"], saidOn)
	end := asTimelineDate(obj["end"], start)
	if end.Before(start) {
		start, end = end, start
	}

	kind := "event"
	if kindRaw, exists := obj["kind"]; exists && pyTruthy(kindRaw) {
		kind = strings.ToLower(strings.TrimSpace(pyStr(kindRaw)))
	}
	if !timelineKindValid(kind) {
		kind = "event"
	}

	turns := make(map[int]struct{}, len(session.Messages))
	minTurn := session.Messages[0].Turn
	for _, msg := range session.Messages {
		turns[msg.Turn] = struct{}{}
		if msg.Turn < minTurn {
			minTurn = msg.Turn
		}
	}
	turn := pyIntOrFallback(obj["turn"], minTurn)
	if _, valid := turns[turn]; !valid {
		turn = minTurn
	}

	aliases := []string{}
	appendAlias := func(v any) {
		s := strings.TrimSpace(pyStr(v))
		if s != "" && len(aliases) < 4 {
			aliases = append(aliases, s)
		}
	}
	switch list := obj["aliases"].(type) {
	case []any:
		for _, a := range list {
			appendAlias(a)
		}
	case []string:
		for _, a := range list {
			appendAlias(a)
		}
	case string:
		for _, r := range list {
			appendAlias(string(r))
		}
	}

	return session.SessionID, TimelineItem{
		Turn:    turn,
		Kind:    kind,
		Text:    text,
		Start:   start.Format("2006-01-02"),
		End:     end.Format("2006-01-02"),
		Aliases: aliases,
	}, true
}

func extractTimelineBatch(ctx context.Context, completer llm.Completer, batch []SessionText) (map[string][]TimelineItem, error) {
	prompt, labels := TimelinePrompt(batch)
	resp, err := completer.AComplete(ctx, llm.CompletionRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: prompt}},
		System:    TimelineSystemPrompt,
		MaxTokens: 8000,
		JSONMode:  true,
	})
	if err != nil {
		return nil, err
	}
	data := ParseTimelineJSON(resp.Content)
	out := make(map[string][]TimelineItem, len(batch))
	for _, s := range batch {
		out[s.SessionID] = []TimelineItem{}
	}
	items, _ := data["items"].([]any)
	for _, raw := range items {
		if sessionID, item, ok := cleanTimelineItem(raw, labels); ok {
			out[sessionID] = append(out[sessionID], item)
		}
	}
	return out, nil
}

func ExtractTimeline(ctx context.Context, completer llm.Completer, sessions []SessionText, concurrency int) (map[string][]TimelineItem, error) {
	out := make(map[string][]TimelineItem, len(sessions))
	for _, s := range sessions {
		out[s.SessionID] = []TimelineItem{}
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	batchable := make([]SessionText, 0, len(sessions))
	for _, s := range sessions {
		if len(s.Messages) > 0 {
			batchable = append(batchable, s)
		}
	}
	batches := TimelineBatches(batchable)
	results := make([]map[string][]TimelineItem, len(batches))
	errs := make([]error, len(batches))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range batches {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i], errs[i] = extractTimelineBatch(ctx, completer, batches[i])
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	for i := range batches {
		for sessionID, items := range results[i] {
			out[sessionID] = append(out[sessionID], items...)
		}
	}
	return out, nil
}

func TimelinePending(sessionDir string) (*SessionText, int) {
	started, ok := SessionStartedAt(filepath.Base(sessionDir))
	messages := UserMessages(sessionDir)
	done := metaUserTurns(ReadTimelineMeta(sessionDir))
	if !ok || len(messages) <= done {
		return nil, len(messages)
	}
	start := done
	if start < 0 {
		start = len(messages) + start
		if start < 0 {
			start = 0
		}
	}
	numbered := make([]TimelineMessage, 0, len(messages)-start)
	for i := start; i < len(messages); i++ {
		numbered = append(numbered, TimelineMessage{Turn: i + 1, Text: messages[i]})
	}
	return &SessionText{SessionID: filepath.Base(sessionDir), StartedAt: started, Messages: numbered}, len(messages)
}

var timelineSessionLocks sync.Map

func UpdateSessionTimeline(ctx context.Context, completer llm.Completer, sessionDir string) (int, error) {
	lockValue, _ := timelineSessionLocks.LoadOrStore(sessionDir, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	pending, total := TimelinePending(sessionDir)
	if pending == nil {
		return 0, nil
	}
	extracted, err := ExtractTimeline(ctx, completer, []SessionText{*pending}, 4)
	if err != nil {
		return 0, err
	}
	items := extracted[pending.SessionID]
	if err := appendTimelineItems(sessionDir, items, total, completerModel(completer)); err != nil {
		return 0, err
	}
	return len(items), nil
}

func TimelineCacheKey(session SessionText, model string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d|%s|%s", TimelineSchemaVersion, model, pyISOSeconds(session.StartedAt))
	for _, msg := range session.Messages {
		fmt.Fprintf(h, "\n%d\x00%s", msg.Turn, msg.Text)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

type BackfillOptions struct {
	Limit       int
	Exclude     map[string]struct{}
	CacheDir    string
	Concurrency int
}

func BackfillTimelines(ctx context.Context, completer llm.Completer, sessionsDir string, opts BackfillOptions) (int, error) {
	info, err := os.Stat(sessionsDir)
	if err != nil || !info.IsDir() {
		return 0, nil
	}
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		return 0, err
	}

	type todoEntry struct {
		dir     string
		pending SessionText
		total   int
	}
	todo := []todoEntry{}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.IsDir() {
			continue
		}
		if _, skip := opts.Exclude[entry.Name()]; skip {
			continue
		}
		dir := filepath.Join(sessionsDir, entry.Name())
		pending, total := TimelinePending(dir)
		if pending != nil {
			todo = append(todo, todoEntry{dir: dir, pending: *pending, total: total})
		}
		if opts.Limit > 0 && len(todo) >= opts.Limit {
			break
		}
	}
	if len(todo) == 0 {
		return 0, nil
	}

	model := completerModel(completer)
	results := make(map[string][]TimelineItem, len(todo))
	uncached := []SessionText{}
	for _, entry := range todo {
		if opts.CacheDir != "" {
			path := filepath.Join(opts.CacheDir, TimelineCacheKey(entry.pending, model)+".json")
			var cached []TimelineItem
			if data, readErr := os.ReadFile(path); readErr == nil {
				if json.Unmarshal(data, &cached) == nil {
					results[entry.pending.SessionID] = cached
					continue
				}
			}
		}
		uncached = append(uncached, entry.pending)
	}
	if len(uncached) > 0 {
		fresh, err := ExtractTimeline(ctx, completer, uncached, opts.Concurrency)
		if err != nil {
			return 0, err
		}
		for _, pending := range uncached {
			items := fresh[pending.SessionID]
			if items == nil {
				items = []TimelineItem{}
			}
			results[pending.SessionID] = items
			if opts.CacheDir != "" {
				if err := os.MkdirAll(opts.CacheDir, 0o755); err != nil {
					return 0, err
				}
				data, err := marshalTimelineJSON(items)
				if err != nil {
					return 0, err
				}
				path := filepath.Join(opts.CacheDir, TimelineCacheKey(pending, model)+".json")
				if err := os.WriteFile(path, data, 0o644); err != nil {
					return 0, err
				}
			}
		}
	}

	for _, entry := range todo {
		if err := appendTimelineItems(entry.dir, results[entry.pending.SessionID], entry.total, model); err != nil {
			return 0, err
		}
	}
	return len(todo), nil
}

func MemoryTimelineEnabled() bool {
	if raw, ok := os.LookupEnv("HIVE_MEMORY_TIMELINE"); ok && strings.TrimSpace(raw) != "" {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "0", "false", "no", "off":
			return false
		default:
			return true
		}
	}
	home, err := hivehome.Resolve()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return false
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		return false
	}
	value, ok := config["memory_timeline"].(bool)
	return ok && value
}

type TimelineTurnEvents interface {
	SubscribeLLMTurnComplete(handler func(streamID, stopReason string)) string
}

func SubscribeTimelineTriggers(
	ctx context.Context,
	bus TimelineTurnEvents,
	sessionDir string,
	completer llm.Completer,
	backfillSessions int,
) []string {
	if !MemoryTimelineEnabled() {
		return nil
	}

	var mu sync.Mutex
	var lastRun time.Time
	onTurnComplete := func(streamID, stopReason string) {
		if streamID != "queen" {
			return
		}
		if stopReason == "tool_use" || stopReason == "tool_calls" {
			return
		}
		pending, _ := TimelinePending(sessionDir)
		if pending == nil {
			return
		}
		mu.Lock()
		waited := time.Since(lastRun)
		mu.Unlock()
		if len(pending.Messages) < TimelineLiveBatchMessages && waited < TimelineLiveCooldown {
			return
		}
		mu.Lock()
		lastRun = time.Now()
		mu.Unlock()
		go func() {
			_, _ = UpdateSessionTimeline(ctx, completer, sessionDir)
		}()
	}

	if backfillSessions > 0 {
		go func() {
			_, _ = BackfillTimelines(ctx, completer, filepath.Dir(sessionDir), BackfillOptions{
				Limit:       backfillSessions,
				Exclude:     map[string]struct{}{filepath.Base(sessionDir): {}},
				Concurrency: 2,
			})
		}()
	}
	return []string{bus.SubscribeLLMTurnComplete(onTurnComplete)}
}

func completerModel(completer llm.Completer) string {
	if namer, ok := completer.(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

func metaUserTurns(meta map[string]any) int {
	value, ok := meta["user_turns"]
	if !ok || !pyTruthy(value) {
		return 0
	}
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0
		}
		return int(v)
	case int:
		return v
	case string:
		text := strings.TrimSpace(strings.ReplaceAll(v, "_", ""))
		if n, err := strconv.Atoi(text); err == nil {
			return n
		}
		return 0
	case bool:
		if v {
			return 1
		}
		return 0
	}
	return 0
}

func pyIntOrFallback(value any, fallback int) int {
	switch v := value.(type) {
	case nil:
		return fallback
	case bool:
		if v {
			return 1
		}
		return 0
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return fallback
		}
		return int(v)
	case int:
		return v
	case string:
		text := strings.TrimSpace(strings.ReplaceAll(v, "_", ""))
		if text == "" {
			return fallback
		}
		if n, err := strconv.Atoi(text); err == nil {
			return n
		}
		return fallback
	}
	return fallback
}

func pyTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	case []any:
		return len(v) > 0
	case map[string]any:
		return len(v) > 0
	}
	return true
}

func pyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		return pyFloatRepr(v)
	case int:
		return itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprint(value)
}

func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

func pyISOSeconds(t time.Time) string {
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05")
	}
	return t.Format("2006-01-02T15:04:05.000000")
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func envInt(name string, fallback int) int {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(name string, fallback float64) float64 {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fallback
	}
	return f
}
