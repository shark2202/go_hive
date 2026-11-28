package astgrep

import (
	"sort"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

// nodeChild is one structural child plus its field name. Extras (comments)
// and missing recovery nodes are filtered out: ast-grep ignores missing nodes
// in the pattern, and trivia is not part of the structure being compared.
type nodeChild struct {
	node  *gotreesitter.Node
	field string
}

func structuralChildren(n *gotreesitter.Node, lang *Lang) []nodeChild {
	if n == nil {
		return nil
	}
	out := make([]nodeChild, 0, n.ChildCount())
	for i := 0; i < n.ChildCount(); i++ {
		child := n.Child(i)
		if child == nil || child.IsExtra() || child.IsMissing() {
			continue
		}
		field := ""
		if lang != nil && child.IsNamed() {
			field = n.FieldNameForChild(i, lang.Grammar())
		}
		out = append(out, nodeChild{node: child, field: field})
	}
	return out
}

// effectivePatternNode follows ast-grep's built-in heuristic: descend while a
// node has exactly one child; stop at a leaf or at the innermost node with
// more than one child. That keeps `123` a number, `foo(bar)` a call_expression
// and `foo($A);` an expression_statement (the semicolon is structural).
func effectivePatternNode(root *gotreesitter.Node, lang *Lang) *gotreesitter.Node {
	node := root
	for node != nil {
		kids := structuralChildren(node, lang)
		if len(kids) != 1 {
			return node
		}
		node = kids[0].node
	}
	return nil
}

// irNode is the compiled pattern node. A capture/wildcard node matches one
// named AST node; a variadic node is consumed by the parent's sequence matcher.
type irNode struct {
	kind          string
	field         string
	text          string
	capture       string
	wildcard      bool
	variadic      bool
	captureString bool
	children      []*irNode
}

type captureValue struct {
	text     string
	variadic bool
}

type matchState struct {
	caps  map[string]captureValue
	order []string
}

func newMatchState() *matchState {
	return &matchState{caps: map[string]captureValue{}}
}

func (s *matchState) clone() *matchState {
	next := &matchState{caps: make(map[string]captureValue, len(s.caps)), order: append([]string(nil), s.order...)}
	for k, v := range s.caps {
		next.caps[k] = v
	}
	return next
}

func (s *matchState) restore(saved *matchState) {
	s.caps = saved.caps
	s.order = saved.order
}

func (s *matchState) bind(name, text string, variadic bool) bool {
	if name == "" {
		return true
	}
	if prior, ok := s.caps[name]; ok {
		return prior.text == text
	}
	s.caps[name] = captureValue{text: text, variadic: variadic}
	s.order = append(s.order, name)
	return true
}

// matchIR compares one compiled pattern node against one candidate node.
func (p *Pattern) matchIR(ir *irNode, node *gotreesitter.Node, source []byte, st *matchState) bool {
	if ir == nil || node == nil {
		return false
	}
	if ir.captureString {
		if node.Type(p.lang.Grammar()) != ir.kind {
			return false
		}
		content := jsonStringContent(node.Text(source))
		if ir.capture == "" {
			return true
		}
		return st.bind(ir.capture, content, false)
	}
	if ir.capture != "" || ir.wildcard {
		if !node.IsNamed() || node.IsError() || node.IsMissing() {
			return false
		}
		if ir.capture == "" {
			return true
		}
		return st.bind(ir.capture, node.Text(source), ir.variadic)
	}
	if ir.kind != node.Type(p.lang.Grammar()) {
		return false
	}
	if len(ir.children) == 0 {
		if ir.text == "" {
			return true
		}
		return ir.text == node.Text(source)
	}
	kids := structuralChildren(node, p.lang)
	return p.matchSeq(ir.children, kids, 0, 0, source, st)
}

// matchSeq matches a pattern child sequence against a candidate sibling
// sequence. Variadic captures try the longest consumption first and backtrack
// until the rest of the pattern is satisfiable.
func (p *Pattern) matchSeq(pat []*irNode, cand []nodeChild, pi, ci int, source []byte, st *matchState) bool {
	if pi == len(pat) {
		return ci == len(cand)
	}
	current := pat[pi]
	if current.variadic {
		for take := len(cand) - ci; take >= 0; take-- {
			trial := st.clone()
			text := spanText(cand, ci, ci+take, source)
			if !trial.bind(current.capture, text, true) {
				continue
			}
			if p.matchSeq(pat, cand, pi+1, ci+take, source, trial) {
				st.restore(trial)
				return true
			}
		}
		return false
	}
	if ci >= len(cand) {
		return false
	}
	if current.field != "" && cand[ci].field != current.field {
		return false
	}
	trial := st.clone()
	if !p.matchIR(current, cand[ci].node, source, trial) {
		return false
	}
	if p.matchSeq(pat, cand, pi+1, ci+1, source, trial) {
		st.restore(trial)
		return true
	}
	return false
}

func spanText(cand []nodeChild, from, to int, source []byte) string {
	if from >= to {
		return ""
	}
	start := int(cand[from].node.StartByte())
	end := int(cand[to-1].node.EndByte())
	if start < 0 || end > len(source) || end < start {
		return ""
	}
	return string(source[start:end])
}

// jsonStringContent returns the text between JSON string quotes. The pattern
// compiler only calls it for a whole-string metavariable, so escapes cannot
// hide the placeholder.
func jsonStringContent(text string) string {
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		return text[1 : len(text)-1]
	}
	return text
}

// findInTree walks every named candidate node and records structural matches.
// Matches are deduplicated by source range and sorted by byte offset.
func (p *Pattern) findInTree(root *gotreesitter.Node, source []byte) []SyntaxMatch {
	return p.findInTreeLimit(root, source, 0)
}

// findInTreeLimit is findInTree with an optional collection cap. A positive
// limit stops the walk once enough hits are retained; Search uses it to bound
// memory and work without inventing a model-visible limit field.
func (p *Pattern) findInTreeLimit(root *gotreesitter.Node, source []byte, limit int) []SyntaxMatch {
	if root == nil {
		return nil
	}
	var out []SyntaxMatch
	seen := map[[2]int]struct{}{}
	var walk func(*gotreesitter.Node)
	walk = func(node *gotreesitter.Node) {
		if node == nil || (limit > 0 && len(out) >= limit) {
			return
		}
		if node.IsNamed() {
			st := newMatchState()
			if p.matchIR(p.root, node, source, st) {
				start, end := int(node.StartByte()), int(node.EndByte())
				key := [2]int{start, end}
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					out = append(out, SyntaxMatch{
						StartByte: start,
						EndByte:   end,
						Line:      lineNumberAt(source, start),
						Text:      node.Text(source),
						Captures:  p.capturesFrom(st),
					})
				}
			}
		}
		for i := 0; i < node.ChildCount(); i++ {
			walk(node.Child(i))
		}
	}
	walk(root)
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartByte != out[j].StartByte {
			return out[i].StartByte < out[j].StartByte
		}
		return out[i].EndByte < out[j].EndByte
	})
	return out
}

func (p *Pattern) capturesFrom(st *matchState) []Capture {
	if len(st.order) == 0 {
		return nil
	}
	out := make([]Capture, 0, len(st.order))
	for _, name := range st.order {
		value, ok := st.caps[name]
		if !ok {
			continue
		}
		out = append(out, Capture{Name: name, Text: value.text, Variadic: value.variadic})
	}
	return out
}
