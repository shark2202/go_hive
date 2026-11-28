package astgrep

import (
	"strconv"
	"strings"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// Capture is one named metavariable binding on a match.
type Capture struct {
	Name     string
	Text     string
	Variadic bool
}

// SyntaxMatch is one structural match inside a parsed source buffer.
type SyntaxMatch struct {
	StartByte int
	EndByte   int
	Line      int
	Text      string
	Captures  []Capture
}

// Pattern is a compiled structural pattern for one language. It is immutable
// and safe to reuse for many files in the same language.
type Pattern struct {
	lang              *Lang
	source            string
	root              *irNode
	captures          []string
	placeholderPrefix string
}

// Language returns the grammar the pattern was compiled for.
func (p *Pattern) Language() *Lang {
	if p == nil {
		return nil
	}
	return p.lang
}

// Source returns the trimmed pattern text supplied by the caller.
func (p *Pattern) Source() string {
	if p == nil {
		return ""
	}
	return p.source
}

// Captures returns the metavariable names in pattern-source order, including
// names that were only captured on some successful matches.
func (p *Pattern) Captures() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.captures))
	copy(out, p.captures)
	return out
}

type metaVar struct {
	name     string
	variadic bool
	wildcard bool
}

// CompilePattern parses a pattern with the language grammar, reduces it to one
// effective AST node and compiles that node into the matcher IR.
func CompilePattern(lang *Lang, pattern string) (*Pattern, error) {
	if lang == nil {
		return nil, NewError(ErrUnsupportedLanguage, "compiling a pattern needs a language")
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, NewError(ErrInvalidInput, "pattern is required")
	}
	substituted, placeholders, prefix, err := substitutePattern(pattern)
	if err != nil {
		return nil, err
	}
	tree, err := parseTree(lang, []byte(substituted+"\n"), defaultPatternTimeout)
	if err != nil {
		return nil, NewError(ErrInvalidPattern, "pattern does not parse for %s: %v", lang.Name(), err)
	}
	defer tree.Release()
	root := tree.RootNode()
	if root == nil || hasErrorNode(root) {
		return nil, NewError(ErrInvalidPattern,
			"pattern does not parse as one AST node for %s; add context or fix the syntax", lang.Name())
	}
	effective := effectivePatternNode(root, lang)
	if effective == nil || !effective.IsNamed() {
		return nil, NewError(ErrInvalidPattern, "pattern has no named AST node for %s", lang.Name())
	}
	kind := effective.Type(lang.Grammar())
	if wrapperKinds[kind] {
		return nil, NewError(ErrInvalidPattern,
			"pattern parses as %d top-level nodes for %s; a pattern must be exactly one AST node", len(structuralChildren(effective, lang)), lang.Name())
	}
	compiled := &Pattern{lang: lang, source: pattern, placeholderPrefix: prefix}
	ir, err := compiled.buildIR(effective, "", placeholders, []byte(substituted+"\n"), map[string]bool{})
	if err != nil {
		return nil, err
	}
	if ir.variadic {
		return nil, NewError(ErrInvalidPattern,
			"a variadic metavariable cannot be the whole pattern; place it among siblings inside a node")
	}
	compiled.root = ir
	return compiled, nil
}

// wrapperKinds are the root node kinds tree-sitter grammars add around a
// source snippet. If the effective-node heuristic lands on one of them the
// snippet had zero or more than one top-level node.
var wrapperKinds = map[string]bool{
	"source_file":      true,
	"program":          true,
	"module":           true,
	"document":         true,
	"stream":           true,
	"translation_unit": true,
	"compilation_unit": true,
}

// stringContentKinds are leaf node kinds that live inside a quoted/string node.
// A metavariable there is a string fragment, not a whole AST node. JSON is
// handled by its parent string node before this check; other languages reject
// the pattern rather than silently capturing a fragment.
var stringContentKinds = map[string]bool{
	"string_content":                     true,
	"interpreted_string_literal_content": true,
	"raw_string_literal_content":         true,
	"template_string_content":            true,
	"string_fragment":                    true,
}

// substitutePattern replaces metavariables with valid identifier placeholders,
// because `$NAME` is not valid syntax in most left-hand-side grammars. The
// placeholder prefix is chosen so it cannot occur in the original pattern.
func substitutePattern(pattern string) (string, map[string]metaVar, string, error) {
	prefix := "zzastg"
	for strings.Contains(pattern, prefix) {
		prefix += "q"
	}
	placeholders := map[string]metaVar{}
	named := map[string]string{}
	namedVariadic := map[string]bool{}
	var b strings.Builder
	next := 0
	addPlaceholder := func(name string, variadic bool) (string, error) {
		if name != "" {
			if prior, ok := named[name]; ok {
				if namedVariadic[name] != variadic {
					return "", NewError(ErrInvalidPattern,
						"metavariable $%s is used with inconsistent arity", name)
				}
				return prior, nil
			}
		}
		placeholder := prefix + strconv.Itoa(next) + "zz"
		next++
		placeholders[placeholder] = metaVar{name: name, variadic: variadic, wildcard: name == ""}
		if name != "" {
			named[name] = placeholder
			namedVariadic[name] = variadic
		}
		return placeholder, nil
	}
	for i := 0; i < len(pattern); {
		if pattern[i] != '$' {
			b.WriteByte(pattern[i])
			i++
			continue
		}
		if i+2 < len(pattern) && pattern[i+1] == '$' && pattern[i+2] == '$' {
			j := i + 3
			k := j
			if k < len(pattern) && (isUpperMeta(pattern[k]) || pattern[k] == '_') {
				k++
				for k < len(pattern) && (isUpperMeta(pattern[k]) || pattern[k] == '_' || isDigit(pattern[k])) {
					k++
				}
			}
			name := pattern[j:k]
			placeholder, err := addPlaceholder(name, true)
			if err != nil {
				return "", nil, "", err
			}
			b.WriteString(placeholder)
			i = k
			continue
		}
		if i+1 < len(pattern) && (isUpperMeta(pattern[i+1]) || pattern[i+1] == '_') {
			j := i + 1
			k := j + 1
			for k < len(pattern) && (isUpperMeta(pattern[k]) || pattern[k] == '_' || isDigit(pattern[k])) {
				k++
			}
			name := pattern[j:k]
			if strings.HasPrefix(name, "_") {
				name = ""
			}
			placeholder, err := addPlaceholder(name, false)
			if err != nil {
				return "", nil, "", err
			}
			b.WriteString(placeholder)
			i = k
			continue
		}
		b.WriteByte(pattern[i])
		i++
	}
	return b.String(), placeholders, prefix, nil
}

func isUpperMeta(c byte) bool { return c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool     { return c >= '0' && c <= '9' }

// buildIR compiles one parsed pattern node. parentKind distinguishes whole
// nodes from string-fragment leaves.
func (p *Pattern) buildIR(node *gotreesitter.Node, parentKind string, placeholders map[string]metaVar,
	source []byte, captured map[string]bool) (*irNode, error) {
	if node == nil {
		return nil, NewError(ErrInvalidPattern, "empty pattern node")
	}
	text := node.Text(source)
	kind := node.Type(p.lang.Grammar())
	kids := structuralChildren(node, p.lang)

	// JSON string values are the one supported string-fragment case: the
	// pattern keeps the quotes literal and captures the string content.
	if p.lang.Name() == "json" && kind == "string" && strings.HasPrefix(text, "\"") && strings.HasSuffix(text, "\"") {
		content := jsonStringContent(text)
		if meta, ok := placeholders[content]; ok {
			return p.newCaptureNode(kind, meta, captured, true), nil
		}
	}
	if len(kids) == 0 {
		if meta, ok := placeholders[text]; ok {
			if stringContentKinds[kind] && p.lang.Name() != "json" {
				return nil, NewError(ErrInvalidPattern,
					"metavariable inside a string fragment is not supported for %s; use a whole AST node", p.lang.Name())
			}
			return p.newCaptureNode(kind, meta, captured, false), nil
		}
		if containsPlaceholder(text, placeholders) {
			return nil, NewError(ErrInvalidPattern,
				"metavariable must be the whole AST node text, not part of %q", text)
		}
		return &irNode{kind: kind, text: text}, nil
	}
	children := make([]*irNode, 0, len(kids))
	for _, child := range kids {
		compiled, err := p.buildIR(child.node, kind, placeholders, source, captured)
		if err != nil {
			return nil, err
		}
		compiled.field = child.field
		children = append(children, compiled)
	}
	// A grammar often wraps a single expression in a statement/list node
	// (Go's expression_statement and statement_list are examples). When the
	// only content is a variadic metavariable, the wrapper is transparent:
	// collapsing it lets $$$BODY consume sibling statements instead of being
	// trapped inside one wrapper node.
	if len(children) == 1 && children[0].variadic {
		return children[0], nil
	}
	return &irNode{kind: kind, children: children}, nil
}

func (p *Pattern) newCaptureNode(kind string, meta metaVar, captured map[string]bool, stringContent bool) *irNode {
	if meta.name != "" && !captured[meta.name] {
		captured[meta.name] = true
		p.captures = append(p.captures, meta.name)
	}
	return &irNode{
		kind:          kind,
		capture:       meta.name,
		wildcard:      meta.wildcard,
		variadic:      meta.variadic && !stringContent,
		captureString: stringContent,
	}
}

func containsPlaceholder(text string, placeholders map[string]metaVar) bool {
	for placeholder := range placeholders {
		if strings.Contains(text, placeholder) {
			return true
		}
	}
	return false
}

// hasErrorNode reports a real recovery ERROR node. Missing nodes are ignored
// in patterns (ast-grep ignores the implicit trailing semicolon in grammars
// such as Rust and Java), while an ERROR node means the pattern did not parse
// to usable structure.
func hasErrorNode(root *gotreesitter.Node) bool {
	if root == nil {
		return false
	}
	if root.IsError() {
		return true
	}
	for i := 0; i < root.ChildCount(); i++ {
		if hasErrorNode(root.Child(i)) {
			return true
		}
	}
	return false
}

// FindAll parses source, walks it and returns all structural matches. A
// partial parse is an error; a complete parse with recovery ERROR nodes still
// returns matches, and Search reports the syntax issue separately.
func (p *Pattern) FindAll(source []byte) ([]SyntaxMatch, error) {
	if p == nil || p.root == nil {
		return nil, NewError(ErrInvalidPattern, "pattern is not compiled")
	}
	tree, err := parseTree(p.lang, source, defaultPatternTimeout)
	if err != nil {
		return nil, err
	}
	defer tree.Release()
	return p.findInTree(tree.RootNode(), source), nil
}

// MatchNode reports one match of this pattern at node, or nil.
func (p *Pattern) MatchNode(node *gotreesitter.Node, source []byte) *SyntaxMatch {
	if p == nil || p.root == nil || node == nil || !node.IsNamed() {
		return nil
	}
	st := newMatchState()
	if !p.matchIR(p.root, node, source, st) {
		return nil
	}
	return &SyntaxMatch{
		StartByte: int(node.StartByte()),
		EndByte:   int(node.EndByte()),
		Line:      lineNumberAt(source, int(node.StartByte())),
		Text:      node.Text(source),
		Captures:  p.capturesFrom(st),
	}
}
