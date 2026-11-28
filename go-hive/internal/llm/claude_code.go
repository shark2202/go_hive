package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
)

// Claude Code OAuth subscription request shaping.
//
// Anthropic's OAuth path (Claude Code subscriptions, tokens prefixed
// "sk-ant-oat") rejects plain API requests: it requires the Claude Code
// User-Agent, the OAuth beta header, and a billing integrity system block
// carried in the request body. Upstream litellm.py adds all three; the Go
// transport used to send only the bearer credential, which made subscription
// calls fail.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.__init__
// (user-agent), ::litellm OAuth validate_environment patch (anthropic-beta),
// ::acomplete / ::stream (billing system block)
const (
	ClaudeCodeVersion         = "2.1.76"
	ClaudeCodeUserAgent       = "claude-code/" + ClaudeCodeVersion
	ClaudeCodeOAuthBetaHeader = "oauth-2025-04-20"

	claudeCodeBillingSalt = "59cf53e54c78"
)

// IsClaudeCodeOAuthKey reports whether the credential is a Claude Code
// subscription OAuth token rather than a plain Anthropic API key.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider._claude_code_oauth
func IsClaudeCodeOAuthKey(apiKey string) bool {
	return strings.HasPrefix(apiKey, anthropicOAuthPrefix)
}

// applyClaudeCodeHeaders adds the headers Anthropic's OAuth path requires.
// Explicit caller-supplied values win; existing anthropic-beta parts are kept.
//
// Parity: core/framework/llm/litellm.py (CLAUDE_CODE_USER_AGENT setdefault and
// the anthropic-beta merge in the litellm OAuth patch)
func applyClaudeCodeHeaders(headers map[string]string) {
	if _, ok := headerLookup(headers, "user-agent"); !ok {
		headers["User-Agent"] = ClaudeCodeUserAgent
	}
	key, existing, _ := headerLookupKey(headers, "anthropic-beta")
	parts := make([]string, 0, 2)
	for _, part := range strings.Split(existing, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	found := false
	for _, part := range parts {
		if part == ClaudeCodeOAuthBetaHeader {
			found = true
			break
		}
	}
	if !found {
		parts = append(parts, ClaudeCodeOAuthBetaHeader)
	}
	if key != "" {
		delete(headers, key)
	}
	headers["anthropic-beta"] = strings.Join(parts, ",")
}

// claudeCodeBillingHeader builds the billing integrity system block required
// by Anthropic's OAuth path. The sampled characters are taken at UTF-16 code
// unit offsets 4, 7 and 20 of the first user message, matching the JavaScript
// implementation Claude Code uses.
//
// Parity: core/framework/llm/litellm.py::_claude_code_billing_header
func claudeCodeBillingHeader(messages []map[string]any) string {
	firstText := firstUserMessageText(messages)
	sampled := claudeCodeSampleCharacters(firstText)
	sum := sha256.Sum256([]byte(claudeCodeBillingSalt + sampled + ClaudeCodeVersion))
	versionHash := hex.EncodeToString(sum[:])
	entrypoint := strings.TrimSpace(os.Getenv("CLAUDE_CODE_ENTRYPOINT"))
	if entrypoint == "" {
		entrypoint = "cli"
	}
	return fmt.Sprintf(
		"x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=%s; cch=00000;",
		ClaudeCodeVersion, versionHash[:3], entrypoint)
}

// withClaudeCodeBillingMessage prepends the billing system message to the
// request's message list. Anthropic expects it ahead of the caller's system
// prompt; both are folded into the Messages API `system` field afterwards.
//
// Parity: core/framework/llm/litellm.py::LiteLLMProvider.acomplete
// (`full_messages.append({"role": "system", "content": billing})`)
func withClaudeCodeBillingMessage(raw any) any {
	messages, ok := asMapSlice(raw)
	if !ok {
		return raw
	}
	billing := map[string]any{
		"role":    "system",
		"content": claudeCodeBillingHeader(messages),
	}
	out := make([]map[string]any, 0, len(messages)+1)
	out = append(out, billing)
	out = append(out, messages...)
	return out
}

// firstUserMessageText returns the first non-empty user text, whether the
// content is a plain string or a block list.
//
// Parity: core/framework/llm/litellm.py::_claude_code_billing_header
func firstUserMessageText(messages []map[string]any) string {
	for _, message := range messages {
		if role, _ := message["role"].(string); role != "user" {
			continue
		}
		switch content := message["content"].(type) {
		case string:
			return content
		case []any:
			for _, raw := range content {
				block, ok := raw.(map[string]any)
				if !ok || block["type"] != "text" {
					continue
				}
				if text, ok := block["text"].(string); ok && text != "" {
					return text
				}
			}
			// No text block in this user turn: keep looking at later user
			// messages, matching the upstream loop.
		}
	}
	return ""
}

// claudeCodeSampleCharacters samples three UTF-16 code units, returning "0"
// for an out-of-range index. A lone surrogate (only reachable with astral
// text split at a unit boundary) has no valid UTF-8 encoding; it is decoded
// as the replacement rune, matching the best effort of the printed string.
//
// Parity: core/framework/llm/litellm.py::_sample_js_code_unit
func claudeCodeSampleCharacters(text string) string {
	units := utf16.Encode([]rune(text))
	var sampled strings.Builder
	for _, index := range []int{4, 7, 20} {
		if index < 0 || index >= len(units) {
			sampled.WriteByte('0')
			continue
		}
		sampled.WriteString(string(utf16.Decode([]uint16{units[index]})))
	}
	return sampled.String()
}

// headerLookupKey is headerLookup with the stored key returned so a caller can
// replace the value without leaving a duplicate differently-cased entry.
func headerLookupKey(headers map[string]string, name string) (string, string, bool) {
	if value, ok := headers[name]; ok {
		return name, value, true
	}
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return key, value, true
		}
	}
	return "", "", false
}
