package agentloop

import (
	"errors"
	"reflect"
	"regexp"
	"strings"
)

var contextTooLargeRE = regexp.MustCompile(
	`(?i)context.{0,20}(length|window|limit|size)` +
		`|too.{0,10}(long|large|many.{0,10}tokens)` +
		`|(exceed|exceeds|exceeded).{0,30}(limit|window|context|tokens)` +
		`|maximum.{0,20}token` +
		`|prompt.{0,20}too.{0,10}long`,
)

// Parity: framework/agent_loop/agent_loop.py _is_context_too_large_error.
func IsContextTooLargeError(err error) bool {
	if err == nil {
		return false
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if strings.Contains(errorTypeName(current), "ContextWindow") {
			return true
		}
		if contextTooLargeRE.MatchString(current.Error()) {
			return true
		}
	}
	return false
}

func errorTypeName(err error) string {
	t := reflect.TypeOf(err)
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return ""
	}
	return t.Name()
}
