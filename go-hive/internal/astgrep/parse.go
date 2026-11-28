package astgrep

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	gotreesitter "github.com/odvcencio/gotreesitter"
)

const defaultPatternTimeout = 2 * time.Second

// errParseStopped marks a parse that was cut short by the per-file timeout or
// a parser work limit. The partial tree is discarded so no match is claimed
// from an incomplete parse.
var errParseStopped = errors.New("parse stopped early")

// parseTree parses source with a fresh parser and an explicit timeout. The
// caller owns the returned tree and must Release it. Partial parses are
// reported as errParseStopped rather than matches.
func parseTree(lang *Lang, source []byte, timeout time.Duration) (*gotreesitter.Tree, error) {
	if lang == nil || lang.Grammar() == nil {
		return nil, NewError(ErrUnsupportedLanguage, "language has no grammar")
	}
	parser := gotreesitter.NewParser(lang.Grammar())
	if timeout > 0 {
		parser.SetTimeoutMicros(uint64(timeout.Microseconds()))
	}
	tree, err := parser.ParseStrict(source)
	if err != nil {
		if errors.Is(err, gotreesitter.ErrParseStoppedEarly) {
			if tree != nil {
				tree.Release()
			}
			return nil, fmt.Errorf("%w: %v", errParseStopped, err)
		}
		if tree != nil {
			tree.Release()
		}
		return nil, err
	}
	if tree == nil {
		return nil, fmt.Errorf("parser returned no tree")
	}
	return tree, nil
}

// firstParseIssue finds the first ERROR or missing node and turns it into a
// model-readable, deduplicable line. Files that only need a warning still keep
// scanning, mirroring ast-grep's additive syntax warning.
func firstParseIssue(root *gotreesitter.Node, lang *Lang, source []byte) (line int, message string) {
	if root == nil {
		return 0, "parse error"
	}
	var issue *gotreesitter.Node
	var walk func(*gotreesitter.Node) bool
	walk = func(node *gotreesitter.Node) bool {
		if node == nil {
			return false
		}
		if node.IsError() || node.IsMissing() {
			issue = node
			return true
		}
		for i := 0; i < node.ChildCount(); i++ {
			if walk(node.Child(i)) {
				return true
			}
		}
		return false
	}
	walk(root)
	if issue == nil {
		issue = root
	}
	line = lineOf(source, int(issue.StartByte()))
	text := strings.TrimSpace(issue.Text(source))
	if text == "" {
		return line, "syntax error"
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 60 {
		text = text[:60] + "..."
	}
	if issue.IsMissing() {
		return line, "missing " + issue.Type(lang.Grammar())
	}
	return line, fmt.Sprintf("syntax error near %q", text)
}

// lineOf returns the 1-based line number containing byteOffset.
func lineOf(source []byte, byteOffset int) int {
	if byteOffset < 0 {
		return 1
	}
	if byteOffset > len(source) {
		byteOffset = len(source)
	}
	return 1 + bytes.Count(source[:byteOffset], []byte{'\n'})
}

// lineNumberAt returns the 1-based line of a byte offset in a file already
// read into memory.
func lineNumberAt(source []byte, byteOffset int) int { return lineOf(source, byteOffset) }
