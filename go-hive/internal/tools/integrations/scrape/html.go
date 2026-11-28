package scrape

// Dependency-free HTML model used by web_scrape: tokenizer, node tree,
// entities, node utils and CSS selector matching. Split out of scrape.go to
// keep both files inside the size budget.

import (
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// ------------------------------------------------------------ HTML model ----

type htmlNode struct {
	tag      string
	attrs    map[string]string
	text     string
	raw      bool
	children []*htmlNode
	parent   *htmlNode
}

var voidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var rawTextTags = map[string]bool{"script": true, "style": true, "textarea": true}

var skipTags = map[string]bool{
	"script": true, "style": true, "nav": true, "footer": true, "header": true,
	"aside": true, "noscript": true, "iframe": true, "head": true,
	"template": true, "svg": true, "canvas": true,
}

var blockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "dd": true, "div": true, "dl": true, "dt": true,
	"fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hr": true, "li": true, "main": true,
	"nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true,
	"thead": true, "tr": true, "ul": true, "title": true,
}

func parseHTML(source string) *htmlNode {
	root := &htmlNode{tag: "#document"}
	stack := []*htmlNode{root}
	i := 0
	for i < len(source) {
		if source[i] != '<' {
			next := strings.IndexByte(source[i:], '<')
			chunk := ""
			if next < 0 {
				chunk = source[i:]
				i = len(source)
			} else {
				chunk = source[i : i+next]
				i += next
			}
			if chunk != "" {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, &htmlNode{tag: "#text", text: unescape(chunk), parent: parent})
			}
			continue
		}
		rest := source[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				i = len(source)
			} else {
				i += 4 + end + 3
			}
			continue
		case strings.HasPrefix(rest, "<!") || strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				i = len(source)
			} else {
				i += end + 1
			}
			continue
		case strings.HasPrefix(rest, "</"):
			end := strings.IndexByte(rest, '>')
			name := ""
			if end < 0 {
				name = rest[2:]
				i = len(source)
			} else {
				name = rest[2:end]
				i += end + 1
			}
			name = strings.ToLower(strings.TrimSpace(name))
			for depth := len(stack) - 1; depth >= 1; depth-- {
				if stack[depth].tag == name {
					stack = stack[:depth]
					break
				}
			}
			continue
		}

		// Start tag.
		cursor := i + 1
		if cursor >= len(source) || !isTagNameStart(source[cursor]) {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, &htmlNode{tag: "#text", text: "<", parent: parent})
			i++
			continue
		}
		for cursor < len(source) && isTagNameChar(source[cursor]) {
			cursor++
		}
		name := strings.ToLower(source[i+1 : cursor])
		attrs, selfClosing, next := parseAttrs(source, cursor)
		node := &htmlNode{tag: name, attrs: attrs, parent: stack[len(stack)-1]}
		stack[len(stack)-1].children = append(stack[len(stack)-1].children, node)
		i = next

		if rawTextTags[name] {
			closing := "</" + name
			index := indexFold(source[i:], closing)
			raw := ""
			if index < 0 {
				raw = source[i:]
				i = len(source)
			} else {
				raw = source[i : i+index]
				end := strings.IndexByte(source[i+index:], '>')
				if end < 0 {
					i = len(source)
				} else {
					i += index + end + 1
				}
			}
			node.children = append(node.children, &htmlNode{tag: "#text", text: raw, raw: true, parent: node})
			continue
		}
		if !selfClosing && !voidTags[name] {
			stack = append(stack, node)
		}
	}
	return root
}

func isTagNameStart(char byte) bool {
	return char == '!' || char == '?' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isTagNameChar(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '-' || char == ':' || char == '_'
}

func parseAttrs(source string, pos int) (map[string]string, bool, int) {
	attrs := map[string]string{}
	selfClosing := false
	for pos < len(source) {
		for pos < len(source) && isSpaceByte(source[pos]) {
			pos++
		}
		if pos >= len(source) {
			return attrs, selfClosing, pos
		}
		if source[pos] == '/' {
			selfClosing = true
			pos++
			continue
		}
		if source[pos] == '>' {
			return attrs, selfClosing, pos + 1
		}
		start := pos
		for pos < len(source) && !isSpaceByte(source[pos]) &&
			source[pos] != '=' && source[pos] != '>' && source[pos] != '/' {
			pos++
		}
		name := strings.ToLower(source[start:pos])
		for pos < len(source) && isSpaceByte(source[pos]) {
			pos++
		}
		value := ""
		if pos < len(source) && source[pos] == '=' {
			pos++
			for pos < len(source) && isSpaceByte(source[pos]) {
				pos++
			}
			if pos < len(source) && (source[pos] == '"' || source[pos] == '\'') {
				quote := source[pos]
				pos++
				end := strings.IndexByte(source[pos:], quote)
				if end < 0 {
					value = source[pos:]
					pos = len(source)
				} else {
					value = source[pos : pos+end]
					pos += end + 1
				}
			} else {
				valueStart := pos
				for pos < len(source) && !isSpaceByte(source[pos]) && source[pos] != '>' {
					pos++
				}
				value = source[valueStart:pos]
			}
		}
		if name != "" {
			attrs[name] = unescape(value)
		}
	}
	return attrs, selfClosing, pos
}

func isSpaceByte(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\f'
}

func indexFold(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if strings.EqualFold(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// --------------------------------------------------------------- entities ---

var namedEntities = map[string]string{
	"nbsp": " ", "copy": "©", "reg": "®", "trade": "™", "hellip": "…",
	"mdash": "—", "ndash": "–", "rsquo": "’", "lsquo": "‘", "ldquo": "“",
	"rdquo": "”", "middot": "·", "bull": "•", "deg": "°", "plusmn": "±",
	"sup2": "²", "sup3": "³", "frac12": "½", "frac14": "¼", "frac34": "¾",
	"times": "×", "divide": "÷", "euro": "€", "pound": "£", "yen": "¥",
	"cent": "¢", "sect": "§", "para": "¶", "laquo": "«", "raquo": "»",
	"dagger": "†", "Dagger": "‡", "permil": "‰", "larr": "←", "rarr": "→",
	"uarr": "↑", "darr": "↓", "harr": "↔", "ensp": " ", "emsp": " ",
	"thinsp": " ", "shy": "", "apos": "'", "quot": "\"", "amp": "&",
	"lt": "<", "gt": ">",
}

func unescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	text = integrationkit.UnescapeHTML(text)
	return integrationkit.EntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := integrationkit.EntityPattern.FindStringSubmatch(match)
		if parts[3] == "" {
			return match
		}
		if value, ok := namedEntities[parts[3]]; ok {
			return value
		}
		if value, ok := namedEntities[strings.ToLower(parts[3])]; ok {
			return value
		}
		return match
	})
}

// ------------------------------------------------------------ node utils ----

func rawText(node *htmlNode) string {
	var builder strings.Builder
	var walk func(*htmlNode)
	walk = func(current *htmlNode) {
		if current.tag == "#text" {
			builder.WriteString(current.text)
			return
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(node)
	return builder.String()
}

func plainText(node *htmlNode) string {
	var builder strings.Builder
	var walk func(*htmlNode)
	walk = func(current *htmlNode) {
		if current.tag == "#text" {
			builder.WriteString(current.text)
			return
		}
		if current.tag == "script" || current.tag == "style" {
			return
		}
		for _, child := range current.children {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func findAll(root *htmlNode, tag string) []*htmlNode {
	found := []*htmlNode{}
	var walk func(*htmlNode)
	walk = func(node *htmlNode) {
		if node.tag == tag {
			found = append(found, node)
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return found
}

func firstTag(root *htmlNode, tag string) *htmlNode {
	var found *htmlNode
	var walk func(*htmlNode)
	walk = func(node *htmlNode) {
		if found != nil {
			return
		}
		if node.tag == tag {
			found = node
			return
		}
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
	return found
}

func walkVisible(root *htmlNode, visit func(*htmlNode)) {
	var walk func(*htmlNode)
	walk = func(node *htmlNode) {
		if skipTags[node.tag] {
			return
		}
		visit(node)
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
}

func insideSkip(node *htmlNode) bool {
	for current := node; current != nil; current = current.parent {
		if skipTags[current.tag] {
			return true
		}
	}
	return false
}

func hasClass(node *htmlNode, class string) bool {
	for _, candidate := range strings.Fields(node.attrs["class"]) {
		if candidate == class {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------- selector ----

func selectOne(root *htmlNode, selector string) *htmlNode {
	for _, alternative := range strings.Split(selector, ",") {
		compoundParts := strings.Fields(strings.TrimSpace(alternative))
		if len(compoundParts) == 0 {
			continue
		}
		if node := selectChain(root, compoundParts); node != nil {
			return node
		}
	}
	return nil
}

func selectChain(root *htmlNode, compounds []string) *htmlNode {
	matches := []*htmlNode{}
	walkVisible(root, func(node *htmlNode) {
		if matchCompound(node, compounds[len(compounds)-1]) {
			matches = append(matches, node)
		}
	})
	for _, match := range matches {
		if matchAncestors(match.parent, compounds[:len(compounds)-1]) {
			return match
		}
	}
	return nil
}

func matchAncestors(node *htmlNode, compounds []string) bool {
	if len(compounds) == 0 {
		return true
	}
	for current := node; current != nil; current = current.parent {
		if matchCompound(current, compounds[len(compounds)-1]) {
			if matchAncestors(current.parent, compounds[:len(compounds)-1]) {
				return true
			}
		}
	}
	return false
}

func matchCompound(node *htmlNode, compound string) bool {
	index := 0
	for index < len(compound) && compound[index] != '.' && compound[index] != '#' {
		index++
	}
	tag := compound[:index]
	if tag != "" && tag != "*" && !strings.EqualFold(node.tag, tag) {
		return false
	}
	for index < len(compound) {
		marker := compound[index]
		index++
		start := index
		for index < len(compound) && compound[index] != '.' && compound[index] != '#' {
			index++
		}
		value := compound[start:index]
		if value == "" {
			return false
		}
		switch marker {
		case '#':
			if node.attrs["id"] != value {
				return false
			}
		case '.':
			if !hasClass(node, value) {
				return false
			}
		}
	}
	return true
}

func defaultContentRoot(doc *htmlNode) *htmlNode {
	if node := firstTag(doc, "main"); node != nil && !insideSkip(node) {
		return node
	}
	if node := findAttr(doc, "role", "main"); node != nil && !insideSkip(node) {
		return node
	}
	if node := firstVisibleTag(doc, "article"); node != nil {
		return node
	}
	for _, class := range []string{"content", "post", "entry", "article-body"} {
		if node := findByClass(doc, class); node != nil {
			return node
		}
	}
	if body := firstTag(doc, "body"); body != nil {
		return body
	}
	return doc
}

func firstVisibleTag(root *htmlNode, tag string) *htmlNode {
	var found *htmlNode
	walkVisible(root, func(node *htmlNode) {
		if found == nil && node.tag == tag {
			found = node
		}
	})
	return found
}

func findByClass(root *htmlNode, class string) *htmlNode {
	var found *htmlNode
	walkVisible(root, func(node *htmlNode) {
		if found == nil && hasClass(node, class) {
			found = node
		}
	})
	return found
}

func findAttr(root *htmlNode, name, value string) *htmlNode {
	var found *htmlNode
	walkVisible(root, func(node *htmlNode) {
		if found == nil && strings.EqualFold(node.attrs[name], value) {
			found = node
		}
	})
	return found
}
