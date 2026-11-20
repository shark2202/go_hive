package agentloop

import "strings"

// LastAssistantText is the most recent assistant message's text, walking
// newest to oldest. The vision-fallback intent uses it as the agent's reasoning;
// "" when no assistant message carries text yet.
//
// Parity: framework/agent_loop/internals/vision_fallback.py::extract_intent_for_tool
func (c *NodeConversation) LastAssistantText() string {
	if c == nil {
		return ""
	}
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Role != RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(c.messages[i].Content); text != "" {
			return text
		}
	}
	return ""
}
