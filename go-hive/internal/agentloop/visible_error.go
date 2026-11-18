package agentloop

// Parity: framework/agent_loop/visible_error.py

import (
	"strings"
	"unicode"
)

// Parity: framework/agent_loop/visible_error.py::PREFIX
const VisibleErrorPrefix = "[Error: LLM call failed: "

// Parity: framework/agent_loop/visible_error.py::visible_error
func VisibleError(errorMsg string) string {
	return "[Error: " + errorMsg + ". Please try again.]"
}

// Parity: framework/agent_loop/visible_error.py::is_visible_error
func IsVisibleError(text string) bool {
	return strings.HasPrefix(strings.TrimLeftFunc(text, isVisibleErrorSpace), VisibleErrorPrefix)
}

func isVisibleErrorSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x1c, 0x1d, 0x1e, 0x1f, 0x85:
		return true
	}
	return unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}
