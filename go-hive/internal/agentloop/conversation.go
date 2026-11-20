package agentloop

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/aden-hive/hive-go/internal/llm"
)

const DefaultMaxContextTokens = 180_000

const DefaultCompactionThreshold = 0.8

type ConversationOptions struct {
	SystemPrompt                  string
	MaxContextTokens              int
	UnlimitedContext              bool
	CompactionThreshold           float64
	OutputKeys                    []string
	Store                         ConversationStore
	RunID                         *string
	CompactionBufferTokens        *int
	CompactionBufferRatio         *float64
	CompactionWarningBufferTokens *int
}

type NodeConversation struct {
	systemPrompt              string
	systemPromptStatic        string
	systemPromptDynamicSuffix string
	maxContextTokens          int
	compactionThreshold       float64
	compactionBufferTokens    *int
	compactionBufferRatio     *float64
	compactionWarningBuffer   *int
	outputKeys                []string
	store                     ConversationStore
	messages                  []Message
	nextSeq                   int
	metaPersisted             bool
	lastAPIInputTokens        *int
	currentPhase              *string
	runID                     *string
	microcompactInflight      bool
}

func NewNodeConversation(systemPrompt string) *NodeConversation {
	return NewNodeConversationWithOptions(ConversationOptions{SystemPrompt: systemPrompt})
}

func NewNodeConversationWithOptions(opts ConversationOptions) *NodeConversation {
	maxContext := opts.MaxContextTokens
	if opts.UnlimitedContext {
		maxContext = 0
	} else if maxContext == 0 {
		maxContext = DefaultMaxContextTokens
	}
	threshold := opts.CompactionThreshold
	if threshold == 0 {
		threshold = DefaultCompactionThreshold
	}
	return &NodeConversation{
		systemPrompt:            opts.SystemPrompt,
		systemPromptStatic:      opts.SystemPrompt,
		maxContextTokens:        maxContext,
		compactionThreshold:     threshold,
		compactionBufferTokens:  opts.CompactionBufferTokens,
		compactionBufferRatio:   opts.CompactionBufferRatio,
		compactionWarningBuffer: opts.CompactionWarningBufferTokens,
		outputKeys:              opts.OutputKeys,
		store:                   opts.Store,
		runID:                   opts.RunID,
		messages:                []Message{},
	}
}

func (c *NodeConversation) SystemPrompt() string { return c.systemPrompt }

func (c *NodeConversation) SystemPromptStatic() string { return c.systemPromptStatic }

func (c *NodeConversation) SystemPromptDynamicSuffix() string { return c.systemPromptDynamicSuffix }

func (c *NodeConversation) UpdateSystemPrompt(newPrompt string, dynamicSuffix *string) {
	if dynamicSuffix == nil {
		c.systemPrompt = newPrompt
		c.systemPromptStatic = newPrompt
		c.systemPromptDynamicSuffix = ""
	} else {
		c.systemPromptStatic = newPrompt
		c.systemPromptDynamicSuffix = *dynamicSuffix
		if *dynamicSuffix == "" {
			c.systemPrompt = newPrompt
		} else {
			c.systemPrompt = newPrompt + "\n\n" + *dynamicSuffix
		}
	}
	c.metaPersisted = false
}

func (c *NodeConversation) SetCurrentPhase(phaseID string) { c.currentPhase = &phaseID }

func (c *NodeConversation) ClearCurrentPhase() { c.currentPhase = nil }

func (c *NodeConversation) CurrentPhase() *string { return c.currentPhase }

func (c *NodeConversation) RunID() *string { return c.runID }

func (c *NodeConversation) Messages() []Message {
	out := make([]Message, len(c.messages))
	copy(out, c.messages)
	return out
}

func (c *NodeConversation) TurnCount() int {
	count := 0
	for _, m := range c.messages {
		if m.Role == RoleUser {
			count++
		}
	}
	return count
}

func (c *NodeConversation) MessageCount() int { return len(c.messages) }

func (c *NodeConversation) NextSeq() int { return c.nextSeq }

func (c *NodeConversation) Store() ConversationStore { return c.store }

type UserMessageOptions struct {
	IsTransitionMarker bool
	IsClientInput      bool
	ImageContent       []map[string]any
	IsSystemReminder   bool
	IsTrigger          bool
}

func (c *NodeConversation) AddUserMessage(content string, opts UserMessageOptions) (Message, error) {
	msg := Message{
		Seq:                c.nextSeq,
		Role:               RoleUser,
		Content:            content,
		PhaseID:            c.currentPhase,
		RunID:              c.runID,
		IsTransitionMarker: opts.IsTransitionMarker,
		IsClientInput:      opts.IsClientInput,
		ImageContent:       opts.ImageContent,
		IsSystemReminder:   opts.IsSystemReminder,
		IsTrigger:          opts.IsTrigger,
	}
	c.messages = append(c.messages, msg)
	c.nextSeq++
	c.lastAPIInputTokens = nil
	if err := c.persist(msg); err != nil {
		return msg, err
	}
	return msg, nil
}

type AssistantMessageOptions struct {
	Truncated        bool
	ThinkingBlocks   []map[string]any
	ReasoningContent *string
}

func (c *NodeConversation) AddAssistantMessage(content string, toolCalls []map[string]any, opts AssistantMessageOptions) (Message, error) {
	images := collectAttachFileChipURLs(c.messages)
	msg := Message{
		Seq:              c.nextSeq,
		Role:             RoleAssistant,
		Content:          content,
		ToolCalls:        toolCalls,
		PhaseID:          c.currentPhase,
		RunID:            c.runID,
		Truncated:        opts.Truncated,
		ThinkingBlocks:   opts.ThinkingBlocks,
		ReasoningContent: opts.ReasoningContent,
		Images:           images,
	}
	if len(msg.ThinkingBlocks) == 0 {
		msg.ThinkingBlocks = nil
	}
	if msg.ReasoningContent != nil && *msg.ReasoningContent == "" {
		msg.ReasoningContent = nil
	}
	if len(msg.Images) == 0 {
		msg.Images = nil
	}
	c.messages = append(c.messages, msg)
	c.nextSeq++
	c.lastAPIInputTokens = nil
	if err := c.persist(msg); err != nil {
		return msg, err
	}
	return msg, nil
}

type ToolResultOptions struct {
	IsError        bool
	ImageContent   []map[string]any
	IsSkillContent bool
	SpilloverPath  *string
}

func (c *NodeConversation) AddToolResult(toolUseID, content string, opts ToolResultOptions) (Message, error) {
	for i := len(c.messages) - 1; i >= 0; i-- {
		existing := c.messages[i]
		if existing.Role == RoleTool && existing.ToolUseID != nil && *existing.ToolUseID == toolUseID {
			log.Printf("add_tool_result: dropping duplicate result for tool_use_id=%s (first result preserved, %d chars; new result ignored, %d chars)",
				toolUseID, len(existing.Content), len(content))
			return existing, nil
		}
	}
	id := toolUseID
	msg := Message{
		Seq:            c.nextSeq,
		Role:           RoleTool,
		Content:        content,
		ToolUseID:      &id,
		IsError:        opts.IsError,
		PhaseID:        c.currentPhase,
		ImageContent:   opts.ImageContent,
		IsSkillContent: opts.IsSkillContent,
		RunID:          c.runID,
		SpilloverPath:  opts.SpilloverPath,
	}
	c.messages = append(c.messages, msg)
	c.nextSeq++
	c.lastAPIInputTokens = nil
	if err := c.persist(msg); err != nil {
		return msg, err
	}

	if !opts.IsError && !opts.IsSkillContent && !c.microcompactInflight {
		if toolName := c.findToolNameForUseID(toolUseID); toolName != "" {
			if CompactableTools[toolName] {
				c.microcompactInflight = true
				func() {
					defer func() {
						if recovered := recover(); recovered != nil {
							log.Printf("proactive microcompact failed for tool_use_id=%s: %v", toolUseID, recovered)
						}
					}()
					Microcompact(c)
				}()
				c.microcompactInflight = false
			}
		}
	}
	return msg, nil
}

func (c *NodeConversation) findToolNameForUseID(toolUseID string) string {
	for i := len(c.messages) - 1; i >= 0; i-- {
		for _, tc := range c.messages[i].ToolCalls {
			if id, _ := tc["id"].(string); id == toolUseID {
				if fn, ok := tc["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok {
						return name
					}
				}
				return ""
			}
		}
	}
	return ""
}

func (c *NodeConversation) FindCompletedToolCall(name string, toolInput map[string]any, withinLastTurns int) *Message {
	targetCanonical := canonicalJSON(toolInput)

	assistantTurnsSeen := 0
	for idx := len(c.messages) - 1; idx >= 0; idx-- {
		m := c.messages[idx]
		if m.Role != RoleAssistant {
			continue
		}
		assistantTurnsSeen++
		if assistantTurnsSeen > withinLastTurns {
			break
		}
		for _, tc := range m.ToolCalls {
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				continue
			}
			if tcName, _ := fn["name"].(string); tcName != name {
				continue
			}
			if canonicalArgs(fn["arguments"]) != targetCanonical {
				continue
			}
			tcID, _ := tc["id"].(string)
			for _, later := range c.messages[idx+1:] {
				if later.Role == RoleTool && later.ToolUseID != nil && *later.ToolUseID == tcID {
					if !later.IsError {
						copied := m
						return &copied
					}
					break
				}
			}
		}
	}
	return nil
}

func (c *NodeConversation) CountConsecutiveCompletedToolCalls(name string, toolInput map[string]any, withinLastTurns int, skipMostRecentAssistant bool) int {
	targetCanonical := canonicalJSON(toolInput)
	streak := 0
	assistantTurnsSeen := 0
	skippedInFlight := !skipMostRecentAssistant
	for idx := len(c.messages) - 1; idx >= 0; idx-- {
		m := c.messages[idx]
		if m.Role != RoleAssistant {
			continue
		}
		if !skippedInFlight {
			skippedInFlight = true
			continue
		}
		assistantTurnsSeen++
		if assistantTurnsSeen > withinLastTurns {
			break
		}
		if len(m.ToolCalls) == 0 {
			break
		}
		matchedInTurn := false
		for _, tc := range m.ToolCalls {
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				continue
			}
			if tcName, _ := fn["name"].(string); tcName != name {
				continue
			}
			if canonicalArgs(fn["arguments"]) != targetCanonical {
				continue
			}
			tcID, _ := tc["id"].(string)
			for _, later := range c.messages[idx+1:] {
				if later.Role == RoleTool && later.ToolUseID != nil && *later.ToolUseID == tcID {
					if !later.IsError {
						matchedInTurn = true
					}
					break
				}
			}
			if matchedInTurn {
				break
			}
		}
		if !matchedInTurn {
			break
		}
		streak++
	}
	return streak
}

func canonicalJSON(value map[string]any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func canonicalArgs(args any) string {
	switch v := args.(type) {
	case nil:
		return canonicalJSON(map[string]any{})
	case string:
		var parsed any
		if err := json.Unmarshal([]byte(v), &parsed); err != nil {
			return v
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			return v
		}
		return string(encoded)
	case map[string]any:
		return canonicalJSON(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(encoded)
	}
}

func (c *NodeConversation) ToLLMMessages(model string) []map[string]any {
	msgs := make([]map[string]any, 0, len(c.messages))
	for _, m := range c.messages {
		msgs = append(msgs, m.ToLLMDict())
	}
	msgs = reasoningForThisTurn(msgs)
	msgs = repairOrphanedToolCalls(msgs)
	if !supportsImagesInToolResults(model) {
		msgs = hoistToolResultImages(msgs)
	}
	msgs = gateUnsupportedMedia(msgs, model)
	return sanitizeForAPI(msgs)
}

func supportsImagesInToolResults(model string) bool {
	return llm.SupportsImagesInToolResults(model)
}

func reasoningForThisTurn(msgs []map[string]any) []map[string]any {
	lastUser := -1
	for i, m := range msgs {
		if m["role"] == RoleUser {
			lastUser = i
		}
	}
	for _, m := range msgs[:maxInt(lastUser, 0)] {
		delete(m, "reasoning_content")
	}
	return msgs
}

func hoistToolResultImages(msgs []map[string]any) []map[string]any {
	namesByID := map[string]string{}
	for _, m := range msgs {
		if m["role"] != RoleAssistant {
			continue
		}
		for _, tc := range asToolCalls(m["tool_calls"]) {
			tcID, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			if tcID != "" && name != "" {
				namesByID[tcID] = name
			}
		}
	}

	out := []map[string]any{}
	type pendingImages struct {
		name   string
		images []map[string]any
	}
	pending := []pendingImages{}
	flush := func() {
		if len(pending) == 0 {
			return
		}
		labels := make([]string, 0, len(pending))
		for _, p := range pending {
			labels = append(labels, fmt.Sprintf("%s (%d)", p.name, len(p.images)))
		}
		hoisted := []map[string]any{{
			"type": "text",
			"text": "[Image output of the tool call(s) above — " + strings.Join(labels, ", ") +
				". Delivered here because this model's API cannot carry images inside a tool result.]",
		}}
		for _, p := range pending {
			hoisted = append(hoisted, p.images...)
		}
		out = append(out, map[string]any{"role": RoleUser, "content": hoisted})
		pending = pending[:0]
	}

	for _, m := range msgs {
		if m["role"] != RoleTool {
			flush()
			out = append(out, m)
			continue
		}
		blocks := contentBlocks(m["content"])
		if blocks == nil {
			out = append(out, m)
			continue
		}
		textBlocks := []map[string]any{}
		imageBlocks := []map[string]any{}
		for _, b := range blocks {
			if b["type"] == "text" {
				textBlocks = append(textBlocks, b)
			} else {
				imageBlocks = append(imageBlocks, b)
			}
		}
		if len(imageBlocks) == 0 {
			out = append(out, m)
			continue
		}
		stripped := copyMap(m)
		texts := make([]string, 0, len(textBlocks))
		for _, b := range textBlocks {
			text, _ := b["text"].(string)
			texts = append(texts, text)
		}
		stripped["content"] = strings.Join(texts, "\n")
		out = append(out, stripped)
		tcID, _ := m["tool_call_id"].(string)
		name := namesByID[tcID]
		if name == "" {
			name = "tool"
		}
		pending = append(pending, pendingImages{name: name, images: imageBlocks})
	}
	flush()
	return out
}

func sanitizeForAPI(msgs []map[string]any) []map[string]any {
	cleaned := []map[string]any{}
	for _, m := range msgs {
		role, _ := m["role"].(string)

		if role == RoleAssistant && len(asToolCalls(m["tool_calls"])) > 0 {
			if content, ok := m["content"].(string); ok && content == "" {
				m["content"] = nil
			}
		}

		if role == RoleTool {
			if id, ok := m["tool_call_id"].(string); !ok || id == "" {
				continue
			}
		}

		if len(cleaned) > 0 && cleaned[len(cleaned)-1]["role"] == role && role == RoleUser {
			prevContent := cleaned[len(cleaned)-1]["content"]
			currContent := m["content"]
			if prevStr, ok := prevContent.(string); ok {
				if currStr, ok := currContent.(string); ok {
					cleaned[len(cleaned)-1]["content"] = prevStr + "\n" + currStr
					continue
				}
			}
			blocks := append(toContentBlocks(prevContent), toContentBlocks(currContent)...)
			cleaned[len(cleaned)-1]["content"] = blocks
			continue
		}

		cleaned = append(cleaned, m)
	}

	for len(cleaned) > 0 {
		role, _ := cleaned[0]["role"].(string)
		if role != RoleAssistant && role != RoleTool {
			break
		}
		cleaned = cleaned[1:]
	}
	return cleaned
}

func toContentBlocks(content any) []map[string]any {
	switch v := content.(type) {
	case []map[string]any:
		out := make([]map[string]any, len(v))
		copy(out, v)
		return out
	case []any:
		out := []map[string]any{}
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case string:
		if v == "" {
			return []map[string]any{}
		}
		return []map[string]any{{"type": "text", "text": v}}
	}
	return []map[string]any{}
}

func contentBlocks(content any) []map[string]any {
	switch content.(type) {
	case []map[string]any, []any:
		return toContentBlocks(content)
	}
	return nil
}

func repairOrphanedToolCalls(msgs []map[string]any) []map[string]any {
	allToolCallIDs := map[string]struct{}{}
	for _, m := range msgs {
		if m["role"] != RoleAssistant {
			continue
		}
		for _, tc := range asToolCalls(m["tool_calls"]) {
			if id, _ := tc["id"].(string); id != "" {
				allToolCallIDs[id] = struct{}{}
			}
		}
	}

	repaired := []map[string]any{}
	openToolCalls := []string{}
	openSet := map[string]bool{}
	seenToolIDs := map[string]bool{}

	for i, m := range msgs {
		role, _ := m["role"].(string)

		if role == RoleTool {
			tid, _ := m["tool_call_id"].(string)
			if _, ok := allToolCallIDs[tid]; !ok || tid == "" {
				continue
			}
			if seenToolIDs[tid] {
				continue
			}
			if !openSet[tid] {
				continue
			}
			delete(openSet, tid)
			for j, id := range openToolCalls {
				if id == tid {
					openToolCalls = append(openToolCalls[:j], openToolCalls[j+1:]...)
					break
				}
			}
			seenToolIDs[tid] = true
			repaired = append(repaired, m)
			continue
		}

		if len(openToolCalls) > 0 {
			for _, staleID := range append([]string{}, openToolCalls...) {
				var realResult map[string]any
				for _, later := range msgs[i:] {
					if later["role"] == RoleTool {
						if id, _ := later["tool_call_id"].(string); id == staleID {
							realResult = later
							break
						}
					}
				}
				if realResult != nil {
					repaired = append(repaired, realResult)
				} else {
					repaired = append(repaired, map[string]any{
						"role":         RoleTool,
						"tool_call_id": staleID,
						"content":      "ERROR: Tool execution was interrupted.",
					})
				}
				seenToolIDs[staleID] = true
			}
			openToolCalls = openToolCalls[:0]
			openSet = map[string]bool{}
		}

		repaired = append(repaired, m)

		if role == RoleAssistant {
			for _, tc := range asToolCalls(m["tool_calls"]) {
				tcID, _ := tc["id"].(string)
				if tcID != "" && !seenToolIDs[tcID] {
					if !openSet[tcID] {
						openSet[tcID] = true
						openToolCalls = append(openToolCalls, tcID)
					}
				}
			}
		}
	}

	for _, staleID := range openToolCalls {
		repaired = append(repaired, map[string]any{
			"role":         RoleTool,
			"tool_call_id": staleID,
			"content":      "ERROR: Tool execution was interrupted.",
		})
	}
	return repaired
}

func asToolCalls(value any) []map[string]any {
	return mapSlice(value)
}

func (c *NodeConversation) ConversationCharsAndImages() (int, int) {
	totalChars := 0
	imageBlocks := 0
	for _, m := range c.messages {
		totalChars += len(m.Content)
		for _, tc := range m.ToolCalls {
			if fn, ok := tc["function"].(map[string]any); ok {
				if args, ok := fn["arguments"].(string); ok {
					totalChars += len(args)
				}
				if name, ok := fn["name"].(string); ok {
					totalChars += len(name)
				}
			}
		}
		imageBlocks += len(m.ImageContent)
	}
	return totalChars, imageBlocks
}

func (c *NodeConversation) EstimateTokens() int {
	if c.lastAPIInputTokens != nil {
		return *c.lastAPIInputTokens
	}
	totalChars, imageBlocks := c.ConversationCharsAndImages()
	imageTokens := imageBlocks * 2000
	return (totalChars*4)/(3*4) + imageTokens
}

func (c *NodeConversation) UpdateTokenCount(actualInputTokens int) {
	c.lastAPIInputTokens = &actualInputTokens
}

func (c *NodeConversation) ClearTokenCount() { c.lastAPIInputTokens = nil }

func (c *NodeConversation) UsageRatio() float64 {
	if c.maxContextTokens <= 0 {
		return 0.0
	}
	return float64(c.EstimateTokens()) / float64(c.maxContextTokens)
}

func (c *NodeConversation) NeedsCompaction() bool {
	if c.maxContextTokens <= 0 {
		return false
	}
	if c.compactionBufferTokens != nil || c.compactionBufferRatio != nil {
		fixed := 0
		if c.compactionBufferTokens != nil {
			fixed = *c.compactionBufferTokens
		}
		ratio := 0.0
		if c.compactionBufferRatio != nil {
			ratio = *c.compactionBufferRatio
		}
		effectiveBuffer := float64(fixed) + ratio*float64(c.maxContextTokens)
		budget := float64(c.maxContextTokens) - effectiveBuffer
		if budget < 0 {
			budget = 0
		}
		return float64(c.EstimateTokens()) >= budget
	}
	return float64(c.EstimateTokens()) >= float64(c.maxContextTokens)*c.compactionThreshold
}

func (c *NodeConversation) CompactionWarning() bool {
	if c.maxContextTokens <= 0 || c.compactionWarningBuffer == nil {
		return false
	}
	warnAt := c.maxContextTokens - *c.compactionWarningBuffer
	if warnAt < 0 {
		warnAt = 0
	}
	return c.EstimateTokens() >= warnAt
}

func (c *NodeConversation) extractProtectedValues(messages []Message) map[string]string {
	if len(c.outputKeys) == 0 {
		return map[string]string{}
	}
	found := map[string]string{}
	remaining := map[string]bool{}
	for _, key := range c.outputKeys {
		remaining[key] = true
	}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != RoleAssistant || len(remaining) == 0 {
			continue
		}
		for key := range remaining {
			if value := tryExtractKey(msg.Content, key); value != nil {
				found[key] = *value
				delete(remaining, key)
			}
		}
	}
	return found
}

func (c *NodeConversation) PruneOldToolResults(protectTokens, minPruneTokens int) (int, error) {
	if len(c.messages) == 0 {
		return 0, nil
	}

	protectedTokens := 0
	pruneable := []int{}
	pruneableTokens := 0

	for i := len(c.messages) - 1; i >= 0; i-- {
		msg := c.messages[i]
		if msg.IsTransitionMarker {
			continue
		}
		if msg.Role != RoleTool {
			continue
		}
		if msg.IsError || msg.IsSkillContent {
			continue
		}
		if strings.HasPrefix(msg.Content, "Pruned tool result") ||
			strings.HasPrefix(msg.Content, "[Pruned tool result") {
			continue
		}
		if len(msg.Content) < 100 {
			continue
		}
		if c.currentPhase != nil && msg.PhaseID != nil && *msg.PhaseID == *c.currentPhase {
			continue
		}

		est := len(msg.Content) / 4
		recoverable := msg.SpilloverPath != nil || ExtractSpilloverFilename(msg.Content) != nil
		if protectedTokens < protectTokens || !recoverable {
			protectedTokens += est
		} else {
			pruneable = append(pruneable, i)
			pruneableTokens += est
		}
	}

	if pruneableTokens < minPruneTokens {
		return 0, nil
	}

	count := 0
	for _, i := range pruneable {
		msg := c.messages[i]
		origLen := len(msg.Content)
		spillover := ""
		if msg.SpilloverPath != nil {
			spillover = *msg.SpilloverPath
		} else if path := ExtractSpilloverFilename(msg.Content); path != nil {
			spillover = *path
		}
		placeholder := fmt.Sprintf(
			"Pruned tool result (%s chars) cleared from context. Full data saved at: %s\nRead the complete data with terminal_exec(\"cat %s\").",
			formatThousands(origLen), spillover, spillover,
		)

		c.messages[i] = Message{
			Seq:                msg.Seq,
			Role:               msg.Role,
			Content:            placeholder,
			ToolUseID:          msg.ToolUseID,
			ToolCalls:          msg.ToolCalls,
			IsError:            msg.IsError,
			PhaseID:            msg.PhaseID,
			IsTransitionMarker: msg.IsTransitionMarker,
			RunID:              msg.RunID,
			SpilloverPath:      msg.SpilloverPath,
		}
		count++
		if c.store != nil {
			if err := c.store.WritePart(msg.Seq, c.messages[i].ToStorageDict()); err != nil {
				return count, err
			}
		}
	}

	c.lastAPIInputTokens = nil
	return count, nil
}

func (c *NodeConversation) EvictOldImages(keepLatest int) (int, error) {
	if len(c.messages) == 0 || keepLatest < 0 {
		return 0, nil
	}

	imageIndices := []int{}
	for i := len(c.messages) - 1; i >= 0; i-- {
		if len(c.messages[i].ImageContent) > 0 {
			imageIndices = append(imageIndices, i)
		}
	}
	if len(imageIndices) <= keepLatest {
		return 0, nil
	}

	evicted := 0
	for _, idx := range imageIndices[keepLatest:] {
		msg := c.messages[idx]
		c.messages[idx] = Message{
			Seq:                msg.Seq,
			Role:               msg.Role,
			Content:            msg.Content,
			ToolUseID:          msg.ToolUseID,
			ToolCalls:          msg.ToolCalls,
			IsError:            msg.IsError,
			PhaseID:            msg.PhaseID,
			IsTransitionMarker: msg.IsTransitionMarker,
			IsClientInput:      msg.IsClientInput,
			ImageContent:       nil,
			IsSkillContent:     msg.IsSkillContent,
			RunID:              msg.RunID,
		}
		evicted++
		if c.store != nil {
			if err := c.store.WritePart(msg.Seq, c.messages[idx].ToStorageDict()); err != nil {
				return evicted, err
			}
		}
	}

	if evicted > 0 {
		c.lastAPIInputTokens = nil
		log.Printf("evict_old_images: dropped image_content from %d message(s), kept %d most recent", evicted, keepLatest)
	}
	return evicted, nil
}

type CompactOptions struct {
	KeepRecent        *int
	PhaseGraduated    bool
	MaxVerbatimClient *int
}

func (c *NodeConversation) Compact(summary string, opts CompactOptions) error {
	if len(c.messages) == 0 {
		return nil
	}
	total := len(c.messages)
	keepRecent := 2
	if opts.KeepRecent != nil {
		keepRecent = *opts.KeepRecent
	}

	var split *int
	if opts.PhaseGraduated && c.currentPhase != nil {
		split = c.findPhaseGraduatedSplit()
	}
	if split == nil {
		if keepRecent < 0 {
			keepRecent = 0
		}
		if keepRecent > total-1 {
			keepRecent = total - 1
		}
		value := total
		if keepRecent > 0 {
			value = total - keepRecent
		}
		split = &value
	}

	for *split < total && c.messages[*split].Role == RoleTool {
		*split++
	}
	if *split == 0 {
		return nil
	}

	oldMessages := append([]Message{}, c.messages[:*split]...)
	recentMessages := append([]Message{}, c.messages[*split:]...)

	preservedClient := []Message{}
	for _, m := range oldMessages {
		if m.Role == RoleUser && m.IsClientInput {
			preservedClient = append(preservedClient, m)
		}
	}
	if opts.MaxVerbatimClient != nil && len(preservedClient) > *opts.MaxVerbatimClient {
		preservedClient = preservedClient[len(preservedClient)-*opts.MaxVerbatimClient:]
	}

	if len(c.outputKeys) > 0 {
		protected := c.extractProtectedValues(oldMessages)
		if len(protected) > 0 {
			lines := []string{"PRESERVED VALUES (do not lose these):"}
			for _, key := range c.outputKeys {
				if value, ok := protected[key]; ok {
					lines = append(lines, fmt.Sprintf("- %s: %s", key, value))
				}
			}
			lines = append(lines, "", "CONVERSATION SUMMARY:", summary)
			summary = strings.Join(lines, "\n")
		}
	}

	var summarySeq int
	if len(recentMessages) > 0 {
		summarySeq = recentMessages[0].Seq - 1
	} else {
		summarySeq = c.nextSeq
		c.nextSeq++
	}

	filtered := preservedClient[:0]
	for _, m := range preservedClient {
		if m.Seq != summarySeq {
			filtered = append(filtered, m)
		}
	}
	preservedClient = filtered

	summaryMsg := Message{
		Seq:     summarySeq,
		Role:    RoleUser,
		Content: summary,
		RunID:   c.runID,
	}

	if c.store != nil {
		deleteBefore := c.nextSeq
		if len(recentMessages) > 0 {
			deleteBefore = recentMessages[0].Seq
		}
		if err := c.store.DeletePartsBefore(deleteBefore); err != nil {
			return err
		}
		for _, m := range preservedClient {
			if err := c.store.WritePart(m.Seq, m.ToStorageDict()); err != nil {
				return err
			}
		}
		if err := c.store.WritePart(summaryMsg.Seq, summaryMsg.ToStorageDict()); err != nil {
			return err
		}
		if err := c.writeNextSeq(); err != nil {
			return err
		}
	}

	combined := make([]Message, 0, len(preservedClient)+1+len(recentMessages))
	combined = append(combined, preservedClient...)
	combined = append(combined, summaryMsg)
	combined = append(combined, recentMessages...)
	c.messages = combined
	c.lastAPIInputTokens = nil
	return nil
}

func (c *NodeConversation) findPhaseGraduatedSplit() *int {
	phasesSeen := []string{}
	seen := map[string]bool{}
	for _, msg := range c.messages {
		if msg.PhaseID != nil && !seen[*msg.PhaseID] {
			seen[*msg.PhaseID] = true
			phasesSeen = append(phasesSeen, *msg.PhaseID)
		}
	}
	if len(phasesSeen) < 3 {
		return nil
	}
	protected := map[string]bool{
		phasesSeen[len(phasesSeen)-1]: true,
		phasesSeen[len(phasesSeen)-2]: true,
	}
	for i, msg := range c.messages {
		if msg.PhaseID != nil && protected[*msg.PhaseID] {
			return &i
		}
	}
	return nil
}

func (c *NodeConversation) Clear() error {
	if c.store != nil {
		if err := c.store.DeletePartsBefore(c.nextSeq); err != nil {
			return err
		}
		if err := c.writeNextSeq(); err != nil {
			return err
		}
	}
	c.messages = []Message{}
	c.lastAPIInputTokens = nil
	return nil
}

func (c *NodeConversation) ExportSummary() string {
	promptPreview := c.systemPrompt
	if len(promptPreview) > 80 {
		promptPreview = promptPreview[:80] + "..."
	}

	lines := []string{
		"[STATS]",
		fmt.Sprintf("turns: %d", c.TurnCount()),
		fmt.Sprintf("messages: %d", c.MessageCount()),
		fmt.Sprintf("estimated_tokens: %d", c.EstimateTokens()),
		"",
		"[CONFIG]",
		fmt.Sprintf("system_prompt: %s", pyRepr(promptPreview)),
	}
	if len(c.outputKeys) > 0 {
		lines = append(lines, "output_keys: "+strings.Join(c.outputKeys, ", "))
	}
	lines = append(lines, "", "[RECENT_MESSAGES]")
	recent := c.messages
	if len(recent) > 5 {
		recent = recent[len(recent)-5:]
	}
	for _, m := range recent {
		preview := m.Content
		if len(preview) > 60 {
			preview = preview[:60] + "..."
		}
		lines = append(lines, fmt.Sprintf("  [%s] %s", m.Role, preview))
	}
	return strings.Join(lines, "\n")
}

func pyRepr(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `'`, `\'`)
	escaped = strings.ReplaceAll(escaped, "\n", `\n`)
	escaped = strings.ReplaceAll(escaped, "\r", `\r`)
	escaped = strings.ReplaceAll(escaped, "\t", `\t`)
	return "'" + escaped + "'"
}

func (c *NodeConversation) persist(message Message) error {
	if c.store == nil {
		return nil
	}
	if !c.metaPersisted {
		if err := c.persistMeta(); err != nil {
			return err
		}
	}
	if err := c.store.WritePart(message.Seq, message.ToStorageDict()); err != nil {
		return err
	}
	if err := c.writeNextSeq(); err != nil {
		return err
	}
	return c.store.ClearPartial(message.Seq)
}

func (c *NodeConversation) CheckpointPartialAssistant(accumulatedText string, toolCalls []map[string]any) error {
	if c.store == nil {
		return nil
	}
	if !c.metaPersisted {
		if err := c.persistMeta(); err != nil {
			return err
		}
	}
	payload := map[string]any{
		"seq":       c.nextSeq,
		"role":      RoleAssistant,
		"content":   accumulatedText,
		"phase_id":  ptrOrNil(c.currentPhase),
		"run_id":    ptrOrNil(c.runID),
		"truncated": true,
	}
	if len(toolCalls) > 0 {
		payload["tool_calls"] = toolCalls
	}
	return c.store.WritePartial(c.nextSeq, payload)
}

func (c *NodeConversation) persistMeta() error {
	if c.store == nil {
		return nil
	}
	meta := map[string]any{
		"system_prompt":                    c.systemPrompt,
		"max_context_tokens":               c.maxContextTokens,
		"compaction_threshold":             c.compactionThreshold,
		"compaction_buffer_tokens":         ptrOrNilInt(c.compactionBufferTokens),
		"compaction_buffer_ratio":          ptrOrNilFloat(c.compactionBufferRatio),
		"compaction_warning_buffer_tokens": ptrOrNilInt(c.compactionWarningBuffer),
		"output_keys":                      c.outputKeys,
	}
	if err := c.store.WriteMeta(meta); err != nil {
		return err
	}
	c.metaPersisted = true
	return nil
}

func (c *NodeConversation) writeNextSeq() error {
	return writeCursorNextSeq(c.store, c.nextSeq)
}

func ptrOrNilInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func ptrOrNilFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func RestoreConversation(store ConversationStore, phaseID, runID *string) (*NodeConversation, error) {
	meta, err := store.ReadMeta()
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, nil
	}

	conv := NewNodeConversationWithOptions(ConversationOptions{
		SystemPrompt:                  stringOr(meta["system_prompt"], ""),
		MaxContextTokens:              intOr(meta["max_context_tokens"], DefaultMaxContextTokens),
		UnlimitedContext:              storedZero(meta["max_context_tokens"]),
		CompactionThreshold:           floatOr(meta["compaction_threshold"], DefaultCompactionThreshold),
		OutputKeys:                    stringSlice(meta["output_keys"]),
		Store:                         store,
		RunID:                         runID,
		CompactionBufferTokens:        intPtr(meta["compaction_buffer_tokens"]),
		CompactionBufferRatio:         floatPtrFromAny(meta["compaction_buffer_ratio"]),
		CompactionWarningBufferTokens: intPtr(meta["compaction_warning_buffer_tokens"]),
	})
	conv.metaPersisted = true

	parts, err := store.ReadParts()
	if err != nil {
		return nil, err
	}

	// Track the highest persisted sequence before any phase/run filtering.
	// A stale cursor (or one written by another writer sharing this store)
	// must never hand out a seq that already has a part file.
	maxPartSeq := -1
	for _, part := range parts {
		if seq := intOr(part["seq"], -1); seq > maxPartSeq {
			maxPartSeq = seq
		}
	}

	if phaseID != nil && *phaseID != "" {
		filtered := []map[string]any{}
		allUnphased := len(parts) > 0
		for _, part := range parts {
			if pid, ok := part["phase_id"]; ok && pid != nil && stringOr(pid, "") != "" {
				allUnphased = false
			}
			if stringOr(part["phase_id"], "") == *phaseID {
				filtered = append(filtered, part)
			}
		}
		if len(filtered) > 0 {
			parts = filtered
		} else if allUnphased {
			log.Printf("Restoring legacy unphased conversation without applying phase filter (phase_id=%s, parts=%d)", *phaseID, len(parts))
		} else {
			parts = filtered
		}
	}

	if runID != nil && !IsLegacyRunID(runID) {
		filtered := []map[string]any{}
		for _, part := range parts {
			if stringOr(part["run_id"], "") == *runID {
				filtered = append(filtered, part)
			}
		}
		parts = filtered
	}

	conv.messages = make([]Message, 0, len(parts))
	for _, part := range parts {
		conv.messages = append(conv.messages, MessageFromStorageDict(part))
	}

	cursor, err := store.ReadCursor()
	if err != nil {
		return nil, err
	}
	nextSeq := 0
	if cursorNext := GetCursorNextSeq(cursor); cursorNext != nil && *cursorNext > nextSeq {
		nextSeq = *cursorNext
	}
	if maxPartSeq+1 > nextSeq {
		nextSeq = maxPartSeq + 1
	}
	conv.nextSeq = nextSeq

	partials, err := store.ReadAllPartials()
	if err != nil {
		return nil, err
	}
	for _, p := range partials {
		pseq := intOr(p["seq"], -1)
		if pseq < conv.nextSeq {
			if err := store.ClearPartial(pseq); err != nil {
				return nil, err
			}
			continue
		}
		if runID != nil && !IsLegacyRunID(runID) && stringOr(p["run_id"], "") != *runID {
			continue
		}
		if phaseID != nil && p["phase_id"] != nil && stringOr(p["phase_id"], "") != *phaseID {
			continue
		}
		conv.messages = append(conv.messages, Message{
			Seq:       pseq,
			Role:      RoleAssistant,
			Content:   stringOr(p["content"], ""),
			ToolCalls: mapSlice(p["tool_calls"]),
			PhaseID:   stringPtr(p["phase_id"]),
			RunID:     stringPtr(p["run_id"]),
			Truncated: true,
		})
		if pseq+1 > conv.nextSeq {
			conv.nextSeq = pseq + 1
		}
		log.Printf("restore: resurrected truncated partial seq=%d (text=%d chars, tool_calls=%d)",
			pseq, len(stringOr(p["content"], "")), len(mapSlice(p["tool_calls"])))
	}

	return conv, nil
}

func collectAttachFileChipURLs(messages []Message) []map[string]any {
	boundary := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleAssistant {
			boundary = i + 1
			break
		}
	}
	images := []map[string]any{}
	for _, m := range messages[boundary:] {
		if m.Role != RoleTool {
			continue
		}
		content := m.Content
		if !strings.Contains(content, "hive_attachment_url") || !strings.Contains(content, `"attached"`) {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(content), &data); err != nil {
			continue
		}
		attached, ok := data["attached"].([]any)
		if !ok {
			continue
		}
		for _, entry := range attached {
			obj, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			url, ok := obj["hive_attachment_url"].(string)
			if !ok || url == "" {
				continue
			}
			var filename any
			if name, ok := obj["filename"].(string); ok {
				filename = name
			} else {
				filename = nil
			}
			images = append(images, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
				"filename":  filename,
			})
		}
	}
	return images
}

func ExtractToolCallHistory(messages []Message, maxEntries int) string {
	toolCallsDetail := map[string][]string{}
	order := []string{}
	outputsSet := []string{}
	errorsList := []string{}

	for _, msg := range messages {
		if msg.Role == RoleAssistant {
			for _, tc := range msg.ToolCalls {
				fn, _ := tc["function"].(map[string]any)
				name, _ := fn["name"].(string)
				if name == "" {
					name = "unknown"
				}
				var args map[string]any
				if raw, ok := fn["arguments"].(string); ok && raw != "" {
					_ = json.Unmarshal([]byte(raw), &args)
				}
				summary := ""
				switch name {
				case "web_search":
					summary, _ = args["query"].(string)
				case "web_scrape":
					summary, _ = args["url"].(string)
				case "terminal_exec":
					summary, _ = args["command"].(string)
				}
				if _, seen := toolCallsDetail[name]; !seen {
					order = append(order, name)
				}
				toolCallsDetail[name] = append(toolCallsDetail[name], summary)
				if name == "set_output" {
					if key, ok := args["key"].(string); ok && key != "" {
						outputsSet = append(outputsSet, key)
					}
				}
			}
		}
		if msg.Role == RoleTool && msg.IsError {
			preview := msg.Content
			if len(preview) > 120 {
				preview = preview[:120]
			}
			preview = strings.ReplaceAll(preview, "\n", " ")
			errorsList = append(errorsList, preview)
		}
	}

	parts := []string{}
	if len(toolCallsDetail) > 0 {
		lines := []string{}
		for _, name := range order {
			if len(lines) >= maxEntries {
				break
			}
			inputs := toolCallsDetail[name]
			nonEmpty := []string{}
			for _, s := range inputs {
				if s != "" {
					nonEmpty = append(nonEmpty, s)
				}
			}
			if len(nonEmpty) > 0 {
				detailLines := []string{}
				for _, s := range nonEmpty {
					if len(detailLines) >= 8 {
						break
					}
					if len(s) > 120 {
						s = s[:120]
					}
					detailLines = append(detailLines, "    - "+s)
				}
				lines = append(lines, fmt.Sprintf("  %s (%dx):\n%s", name, len(inputs), strings.Join(detailLines, "\n")))
			} else {
				lines = append(lines, fmt.Sprintf("  %s (%dx)", name, len(inputs)))
			}
		}
		parts = append(parts, "TOOLS ALREADY CALLED:\n"+strings.Join(lines, "\n"))
	}
	if len(outputsSet) > 0 {
		parts = append(parts, "OUTPUTS SET: "+strings.Join(uniqueStrings(outputsSet), ", "))
	}
	if len(errorsList) > 0 {
		shown := errorsList
		if len(shown) > 10 {
			shown = shown[:10]
		}
		lines := make([]string, 0, len(shown))
		for _, e := range shown {
			lines = append(lines, "  - "+e)
		}
		parts = append(parts, "ERRORS (do NOT retry these):\n"+strings.Join(lines, "\n"))
	}
	return strings.Join(parts, "\n\n")
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func formatThousands(n int) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	digits := fmt.Sprintf("%d", n)
	var b strings.Builder
	offset := len(digits) % 3
	if offset == 0 {
		offset = 3
	}
	b.WriteString(sign)
	b.WriteString(digits[:offset])
	for i := offset; i < len(digits); i += 3 {
		b.WriteString(",")
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func floatOr(value any, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	if f := floatPtrFromAny(value); f != nil {
		return *f
	}
	return fallback
}

func stringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := []string{}
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

var errNotImplemented = errors.New("not implemented")
