package integrations

// Dependency-free HTML model used by web_scrape: tokenizer, node tree,
// entities, node utils and CSS selector matching. Split out of scrape.go to
// keep both files inside the size budget.

import (
	"strings"
)

// ------------------------------------------------------------ HTML model ----

type batch3Node struct {
	tag      string
	attrs    map[string]string
	text     string
	raw      bool
	children []*batch3Node
	parent   *batch3Node
}

var batch3VoidTags = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

var batch3RawTextTags = map[string]bool{"script": true, "style": true, "textarea": true}

var batch3SkipTags = map[string]bool{
	"script": true, "style": true, "nav": true, "footer": true, "header": true,
	"aside": true, "noscript": true, "iframe": true, "head": true,
	"template": true, "svg": true, "canvas": true,
}

var batch3BlockTags = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"br": true, "dd": true, "div": true, "dl": true, "dt": true,
	"fieldset": true, "figcaption": true, "figure": true, "footer": true,
	"form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true,
	"h6": true, "header": true, "hr": true, "li": true, "main": true,
	"nav": true, "ol": true, "p": true, "pre": true, "section": true,
	"table": true, "tbody": true, "td": true, "tfoot": true, "th": true,
	"thead": true, "tr": true, "ul": true, "title": true,
}

func batch3ParseHTML(source string) *batch3Node {
	root := &batch3Node{tag: "#document"}
	stack := []*batch3Node{root}
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
				parent.children = append(parent.children, &batch3Node{tag: "#text", text: batch3Unescape(chunk), parent: parent})
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
		if cursor >= len(source) || !batch3IsTagNameStart(source[cursor]) {
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, &batch3Node{tag: "#text", text: "<", parent: parent})
			i++
			continue
		}
		for cursor < len(source) && batch3IsTagNameChar(source[cursor]) {
			cursor++
		}
		name := strings.ToLower(source[i+1 : cursor])
		attrs, selfClosing, next := batch3ParseAttrs(source, cursor)
		node := &batch3Node{tag: name, attrs: attrs, parent: stack[len(stack)-1]}
		stack[len(stack)-1].children = append(stack[len(stack)-1].children, node)
		i = next

		if batch3RawTextTags[name] {
			closing := "</" + name
			index := batch3IndexFold(source[i:], closing)
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
			node.children = append(node.children, &batch3Node{tag: "#text", text: raw, raw: true, parent: node})
			continue
		}
		if !selfClosing && !batch3VoidTags[name] {
			stack = append(stack, node)
		}
	}
	return root
}

func batch3IsTagNameStart(char byte) bool {
	return char == '!' || char == '?' || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func batch3IsTagNameChar(char byte) bool {
	return (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
		(char >= '0' && char <= '9') || char == '-' || char == ':' || char == '_'
}

func batch3ParseAttrs(source string, pos int) (map[string]string, bool, int) {
	attrs := map[string]string{}
	selfClosing := false
	for pos < len(source) {
		for pos < len(source) && batch3IsSpaceByte(source[pos]) {
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
		for pos < len(source) && !batch3IsSpaceByte(source[pos]) &&
			source[pos] != '=' && source[pos] != '>' && source[pos] != '/' {
			pos++
		}
		name := strings.ToLower(source[start:pos])
		for pos < len(source) && batch3IsSpaceByte(source[pos]) {
			pos++
		}
		value := ""
		if pos < len(source) && source[pos] == '=' {
			pos++
			for pos < len(source) && batch3IsSpaceByte(source[pos]) {
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
				for pos < len(source) && !batch3IsSpaceByte(source[pos]) && source[pos] != '>' {
					pos++
				}
				value = source[valueStart:pos]
			}
		}
		if name != "" {
			attrs[name] = batch3Unescape(value)
		}
	}
	return attrs, selfClosing, pos
}

func batch3IsSpaceByte(char byte) bool {
	return char == ' ' || char == '\t' || char == '\n' || char == '\r' || char == '\f'
}

func batch3IndexFold(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if strings.EqualFold(haystack[i:i+len(needle)], needle) {
			return i
		}
	}
	return -1
}

// --------------------------------------------------------------- entities ---

var batch3NamedEntities = map[string]string{
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

func batch3Unescape(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	text = htmlUnescape(text)
	return batch2EntityPattern.ReplaceAllStringFunc(text, func(match string) string {
		parts := batch2EntityPattern.FindStringSubmatch(match)
		if parts[3] == "" {
			return match
		}
		if value, ok := batch3NamedEntities[parts[3]]; ok {
			return value
		}
		if value, ok := batch3NamedEntities[strings.ToLower(parts[3])]; ok {
			return value
		}
		return match
	})
}

// ------------------------------------------------------------ node utils ----

func batch3RawText(node *batch3Node) string {
	var builder strings.Builder
	var walk func(*batch3Node)
	walk = func(current *batch3Node) {
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

func batch3PlainText(node *batch3Node) string {
	var builder strings.Builder
	var walk func(*batch3Node)
	walk = func(current *batch3Node) {
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

func batch3FindAll(root *batch3Node, tag string) []*batch3Node {
	found := []*batch3Node{}
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
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

func batch3FirstTag(root *batch3Node, tag string) *batch3Node {
	var found *batch3Node
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
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

func batch3WalkVisible(root *batch3Node, visit func(*batch3Node)) {
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if batch3SkipTags[node.tag] {
			return
		}
		visit(node)
		for _, child := range node.children {
			walk(child)
		}
	}
	walk(root)
}

func batch3InsideSkip(node *batch3Node) bool {
	for current := node; current != nil; current = current.parent {
		if batch3SkipTags[current.tag] {
			return true
		}
	}
	return false
}

func batch3FindByID(root *batch3Node, id string) *batch3Node {
	var found *batch3Node
	var walk func(*batch3Node)
	walk = func(node *batch3Node) {
		if found != nil {
			return
		}
		if node.attrs["id"] == id {
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

func batch3HasClass(node *batch3Node, class string) bool {
	for _, candidate := range strings.Fields(node.attrs["class"]) {
		if candidate == class {
			return true
		}
	}
	return false
}

// -------------------------------------------------------------- selector ----

func batch3SelectOne(root *batch3Node, selector string) *batch3Node {
	for _, alternative := range strings.Split(selector, ",") {
		compoundParts := strings.Fields(strings.TrimSpace(alternative))
		if len(compoundParts) == 0 {
			continue
		}
		if node := batch3SelectChain(root, compoundParts); node != nil {
			return node
		}
	}
	return nil
}

func batch3SelectChain(root *batch3Node, compounds []string) *batch3Node {
	matches := []*batch3Node{}
	batch3WalkVisible(root, func(node *batch3Node) {
		if batch3MatchCompound(node, compounds[len(compounds)-1]) {
			matches = append(matches, node)
		}
	})
	for _, match := range matches {
		if batch3MatchAncestors(match.parent, compounds[:len(compounds)-1]) {
			return match
		}
	}
	return nil
}

func batch3MatchAncestors(node *batch3Node, compounds []string) bool {
	if len(compounds) == 0 {
		return true
	}
	for current := node; current != nil; current = current.parent {
		if batch3MatchCompound(current, compounds[len(compounds)-1]) {
			if batch3MatchAncestors(current.parent, compounds[:len(compounds)-1]) {
				return true
			}
		}
	}
	return false
}

func batch3MatchCompound(node *batch3Node, compound string) bool {
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
			if !batch3HasClass(node, value) {
				return false
			}
		}
	}
	return true
}

func batch3DefaultContentRoot(doc *batch3Node) *batch3Node {
	if node := batch3FirstTag(doc, "main"); node != nil && !batch3InsideSkip(node) {
		return node
	}
	if node := batch3FindAttr(doc, "role", "main"); node != nil && !batch3InsideSkip(node) {
		return node
	}
	if node := batch3FirstVisibleTag(doc, "article"); node != nil {
		return node
	}
	for _, class := range []string{"content", "post", "entry", "article-body"} {
		if node := batch3FindByClass(doc, class); node != nil {
			return node
		}
	}
	if body := batch3FirstTag(doc, "body"); body != nil {
		return body
	}
	return doc
}

func batch3FirstVisibleTag(root *batch3Node, tag string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && node.tag == tag {
			found = node
		}
	})
	return found
}

func batch3FindByClass(root *batch3Node, class string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && batch3HasClass(node, class) {
			found = node
		}
	})
	return found
}

func batch3FindAttr(root *batch3Node, name, value string) *batch3Node {
	var found *batch3Node
	batch3WalkVisible(root, func(node *batch3Node) {
		if found == nil && strings.EqualFold(node.attrs[name], value) {
			found = node
		}
	})
	return found
}
