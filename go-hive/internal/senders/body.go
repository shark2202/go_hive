package senders

// Body handling ports the three helpers from senders_tool.py that guard the
// 652-email incident: the message body is TWO fields (html and text), both are
// sent, and neither may silently mangle the other. An agent holding plain text
// must not be able to jam newlines into an HTML-only field.

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strings"
)

var (
	// htmlTagRe is the sniff used on the deprecated html_content parameter:
	// old colonies passed plain text into it, so its NAME must not be trusted.
	htmlTagRe = regexp.MustCompile(`(?i)<(br|p|div|table|html|body|ul|ol|li|h[1-6]|span|strong|em|b|i|a|img)\b[^>]*>`)

	lineBreakRe    = regexp.MustCompile(`(?i)<br\s*/?>`)
	blockCloseRe   = regexp.MustCompile(`(?i)</(p|div|tr|li|h[1-6])\s*>`)
	listItemOpenRe = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	// RE2 has no backreferences, so script and style are two alternatives
	// rather than upstream's `<(script|style)\b.*?</\1>`.
	scriptStyleRe = regexp.MustCompile(`(?is)<script\b.*?</script>|<style\b.*?</style>`)
	anyTagRe      = regexp.MustCompile(`<[^>]+>`)

	spacesBeforeNewlineRe = regexp.MustCompile(`[ \t]+\n`)
	excessNewlinesRe      = regexp.MustCompile(`\n{3,}`)
	paragraphSplitRe      = regexp.MustCompile(`\n\s*\n`)
)

// SplitList splits a comma/newline/space separated string into non-empty
// items, matching senders_tool._split.
func SplitList(value string) []string {
	if value == "" {
		return nil
	}
	replacer := strings.NewReplacer("\n", ",", " ", ",")
	out := make([]string, 0, 4)
	for _, chunk := range strings.Split(replacer.Replace(value), ",") {
		item := strings.TrimSpace(chunk)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

// ParseRecipients accepts a JSON array or a comma/newline separated list,
// matching senders_tool._parse_recipients.
func ParseRecipients(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []any
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil {
			out := make([]string, 0, len(list))
			for _, item := range list {
				text := strings.TrimSpace(toString(item))
				if text != "" {
					out = append(out, text)
				}
			}
			return out
		}
	}
	return SplitList(trimmed)
}

// escapeText escapes the three HTML-significant characters. Deliberately not
// html.EscapeString: Python's html.escape(quote=False) leaves quotes alone, and
// a From line with an apostrophe must round-trip unchanged.
func escapeText(text string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(text)
}

// TextToHTML renders a plain-text body as HTML paragraphs with real line
// breaks. Without this, plain text sent as text/html arrives as one jammed
// line — the 2026-07-13 regression.
func TextToHTML(text string) string {
	var out strings.Builder
	for _, paragraph := range paragraphSplitRe.Split(strings.TrimSpace(text), -1) {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		out.WriteString("<p>")
		for index, line := range strings.Split(paragraph, "\n") {
			if index > 0 {
				out.WriteString("<br>")
			}
			out.WriteString(escapeText(line))
		}
		out.WriteString("</p>")
	}
	return out.String()
}

// HTMLToText derives the readable plain-text alternative. Deliberately dumb:
// block tags become newlines, the rest is stripped. This is the part text-only
// clients and spam filters read, so it must never be empty tag soup.
func HTMLToText(markup string) string {
	out := lineBreakRe.ReplaceAllString(markup, "\n")
	out = blockCloseRe.ReplaceAllString(out, "\n\n")
	out = listItemOpenRe.ReplaceAllString(out, "• ")
	out = scriptStyleRe.ReplaceAllString(out, "")
	out = anyTagRe.ReplaceAllString(out, "")
	out = html.UnescapeString(out)
	out = spacesBeforeNewlineRe.ReplaceAllString(out, "\n")
	out = excessNewlinesRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

// ResolveBody settles the (html, text) pair a send actually goes out with.
//
//	html only          -> text is generated from the html.
//	text only          -> html is generated from the text.
//	both               -> both used verbatim.
//	text = ""          -> explicit opt-out; html-only, no plain part.
//	legacy html_content-> sniffed and routed to whichever field it really is.
func ResolveBody(htmlBody string, text *string, legacy string) (string, string) {
	if legacy != "" && htmlBody == "" && text == nil {
		if htmlTagRe.MatchString(legacy) {
			htmlBody = legacy
		} else {
			plain := legacy
			text = &plain
		}
	}

	if htmlBody == "" && text != nil && *text != "" {
		htmlBody = TextToHTML(*text)
	} else if htmlBody != "" && text == nil {
		derived := HTMLToText(htmlBody)
		text = &derived
	}

	if text == nil {
		return htmlBody, ""
	}
	return htmlBody, *text
}

// toString is the loose scalar coercion used for JSON-decoded input.
func toString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
