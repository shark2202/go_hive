package server

// Parity: framework/server/session_titles.py

import (
	"context"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/llm"
	"github.com/aden-hive/hive-go/internal/memory"
)

const (
	TitleMaxChars     = 60
	titleExcerptChars = 1500
	titleSystemPrompt = "You name chat conversations. Reply with a title of at most six words saying what the " +
		"conversation is about, in the language the user writes in. No quotes, no trailing " +
		"punctuation, nothing else."
	titleMetaPerm = 0o644
)

const titleWrappers = "\"'`*#“”‘’「」『』《》 "

const titleTrailing = ".。!！?？:：,，;；"

var titleHiddenBlocks = regexp.MustCompile(`(?s)<think>.*?</think>|<system-reminder>.*?</system-reminder>`)

var TitleTokenBudget = func() int { return 8192 }

// Parity: framework/server/session_titles.py::clean_title
func CleanTitle(raw string) string {
	line := ""
	for _, candidate := range pySplitLines(stripTitleHiddenBlocks(raw)) {
		if strings.TrimFunc(candidate, pySpace) != "" {
			line = candidate
			break
		}
	}

	title := collapsePythonWhitespace(line)
	title = strings.TrimFunc(title, isTitleWrapper)
	title = strings.TrimRightFunc(title, isTitleTrailing)
	title = strings.TrimFunc(title, pySpace)
	title = stripTitleLabel(title)

	if utf8.RuneCountInString(title) > TitleMaxChars {
		runes := []rune(title)
		head := strings.TrimRightFunc(string(runes[:TitleMaxChars-1]), pySpace)
		title = head + "…"
	}
	return title
}

// Parity: framework/server/session_titles.py::read_title
func ReadTitle(sessionDir string) (string, bool) {
	meta := readJSONObjectOr(filepath.Join(sessionDir, "meta.json"), map[string]any{})
	title, ok := meta["title"].(string)
	if !ok || title == "" {
		return "", false
	}
	return title, true
}

// Parity: framework/server/session_titles.py::write_title
func WriteTitle(sessionDir, title, source string) (string, error) {
	meta := readJSONObjectOr(filepath.Join(sessionDir, "meta.json"), map[string]any{})
	cleaned := CleanTitle(title)
	if cleaned != "" {
		meta["title"] = cleaned
		meta["title_source"] = source
	} else {
		delete(meta, "title")
		delete(meta, "title_source")
	}
	if err := atomicfs.WriteJSON(filepath.Join(sessionDir, "meta.json"), meta, titleMetaPerm); err != nil {
		return "", err
	}
	return cleaned, nil
}

// Parity: framework/server/session_titles.py::generate_title
func GenerateTitle(ctx context.Context, completer llm.Completer, firstMessage, latestReply string) (string, error) {
	first := titleExcerpt(firstMessage)
	reply := titleExcerpt(latestReply)
	completion, err := completer.AComplete(ctx, llm.CompletionRequest{
		Messages: []llm.ChatMessage{{
			Role:    "user",
			Content: "User:\n" + first + "\n\nAssistant:\n" + reply,
		}},
		System:    titleSystemPrompt,
		MaxTokens: TitleTokenBudget(),
	})
	if err != nil {
		return "", err
	}
	return CleanTitle(completion.Content), nil
}

type TitleTrigger struct {
	bus        *eventbus.EventBus
	sessionDir string
	completer  llm.Completer

	mu            sync.Mutex
	latestReply   string
	running       bool
	subscriptions []string
	waiters       sync.WaitGroup
}

// Parity: framework/server/session_titles.py::subscribe_title_trigger
func SubscribeTitleTrigger(bus *eventbus.EventBus, sessionDir string, completer llm.Completer) *TitleTrigger {
	t := &TitleTrigger{bus: bus, sessionDir: sessionDir, completer: completer}
	t.subscriptions = []string{
		bus.Subscribe([]eventbus.EventType{eventbus.EventClientOutputDelta}, t.onOutput),
		bus.Subscribe([]eventbus.EventType{eventbus.EventLLMTurnComplete}, t.onTurnComplete),
	}
	return t
}

func (t *TitleTrigger) Subscriptions() []string {
	return append([]string(nil), t.subscriptions...)
}

func (t *TitleTrigger) Wait() {
	t.waiters.Wait()
}

func (t *TitleTrigger) Close() {
	for _, id := range t.subscriptions {
		t.bus.Unsubscribe(id)
	}
	t.waiters.Wait()
}

func (t *TitleTrigger) onOutput(event *eventbus.AgentEvent) {
	if event.StreamID != "queen" {
		return
	}
	snapshot, ok := event.Data["snapshot"].(string)
	if !ok || snapshot == "" {
		return
	}
	t.mu.Lock()
	t.latestReply = snapshot
	t.mu.Unlock()
}

func (t *TitleTrigger) onTurnComplete(event *eventbus.AgentEvent) {
	if event.StreamID != "queen" {
		return
	}
	if reason, _ := event.Data["stop_reason"].(string); reason == "tool_use" || reason == "tool_calls" {
		return
	}

	t.mu.Lock()
	reply := t.latestReply
	if strings.TrimFunc(reply, pySpace) == "" || t.running {
		t.mu.Unlock()
		return
	}
	if _, titled := ReadTitle(t.sessionDir); titled {
		t.mu.Unlock()
		return
	}
	t.running = true
	t.waiters.Add(1)
	t.mu.Unlock()

	go func() {
		defer t.waiters.Done()
		defer func() {
			t.mu.Lock()
			t.running = false
			t.mu.Unlock()
		}()
		t.nameSession(reply)
	}()
}

func (t *TitleTrigger) nameSession(reply string) {
	name := filepath.Base(t.sessionDir)
	messages := memory.UserMessages(t.sessionDir)
	if len(messages) == 0 {
		return
	}
	title, err := GenerateTitle(context.Background(), t.completer, messages[0], reply)
	if err != nil {
		log.Printf("session title: naming %s failed: %v", name, err)
		return
	}
	if title == "" {
		return
	}
	if _, titled := ReadTitle(t.sessionDir); titled {
		return
	}
	stored, err := WriteTitle(t.sessionDir, title, "auto")
	if err != nil {
		log.Printf("session title: naming %s failed: %v", name, err)
		return
	}

	var payload any = stored
	if stored == "" {
		payload = nil
	}
	event := eventbus.NewAgentEvent(eventbus.EventSessionTitleUpdated, "queen")
	event.Data = map[string]any{"session_id": name, "title": payload}
	t.bus.Publish(event)
}

func stripTitleHiddenBlocks(raw string) string {
	return titleHiddenBlocks.ReplaceAllString(raw, "")
}

func titleExcerpt(text string) string {
	stripped := strings.TrimFunc(stripTitleHiddenBlocks(text), pySpace)
	runes := []rune(stripped)
	if len(runes) > titleExcerptChars {
		runes = runes[:titleExcerptChars]
	}
	return string(runes)
}

func stripTitleLabel(s string) string {
	runes := []rune(s)
	if len(runes) < len("title") || !strings.EqualFold(string(runes[:len("title")]), "title") {
		return s
	}
	i := len("title")
	for i < len(runes) && pySpace(runes[i]) {
		i++
	}
	if i >= len(runes) || (runes[i] != ':' && runes[i] != '：') {
		return s
	}
	i++
	for i < len(runes) && pySpace(runes[i]) {
		i++
	}
	return string(runes[i:])
}

func collapsePythonWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inRun := false
	for _, r := range s {
		if pySpace(r) {
			inRun = true
			continue
		}
		if inRun {
			b.WriteByte(' ')
			inRun = false
		}
		b.WriteRune(r)
	}
	if inRun {
		b.WriteByte(' ')
	}
	return b.String()
}

func pySplitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !isPyLineBoundary(r) {
			i += size
			continue
		}
		out = append(out, s[start:i])
		if r == '\r' && i+size < len(s) && s[i+size] == '\n' {
			i += size + 1
		} else {
			i += size
		}
		start = i
	}
	return append(out, s[start:])
}

func isPyLineBoundary(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func pySpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	return unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

func isTitleWrapper(r rune) bool {
	return strings.ContainsRune(titleWrappers, r)
}

func isTitleTrailing(r rune) bool {
	return strings.ContainsRune(titleTrailing, r)
}
