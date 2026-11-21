package queenmail

import "strings"

// ConversationPrefix names the conversations of queens run by Claude Code;
// their refs read claude_code:<queen id>:<session id>.
const ConversationPrefix = "claude_code:"

// IsConversationRef reports whether ref names a Claude Code queen's
// conversation rather than a Hive session.
func IsConversationRef(ref string) bool {
	return strings.HasPrefix(ref, ConversationPrefix)
}

// ConversationRef names one conversation of a Claude Code queen's.
func ConversationRef(queenID, sessionID string) string {
	return ConversationPrefix + queenID + ":" + sessionID
}

// QueenIDOfRef reads the queen id out of a conversation ref, splitting on the
// last colon the way upstream does (queen ids may contain colons).
func QueenIDOfRef(ref string) string {
	if !IsConversationRef(ref) {
		return ""
	}
	rest := ref[len(ConversationPrefix):]
	cut := strings.LastIndex(rest, ":")
	if cut < 0 {
		return rest
	}
	return rest[:cut]
}
