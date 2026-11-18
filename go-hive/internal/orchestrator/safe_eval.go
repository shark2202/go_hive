package orchestrator

import (
	"fmt"
	"math"
	"math/big"
	"math/bits"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxPowerAbsExponent = 1000
	MaxPowerResultBits  = 4096
	DefaultTimeoutMS    = 100
)

type ValueError struct{ Message string }

func (e *ValueError) Error() string { return e.Message }

type NameError struct{ Message string }

func (e *NameError) Error() string { return e.Message }

type SyntaxError struct{ Message string }

func (e *SyntaxError) Error() string { return e.Message }

type TimeoutError struct{ Message string }

func (e *TimeoutError) Error() string { return e.Message }

type KeyError struct{ Message string }

func (e *KeyError) Error() string { return e.Message }

type AttributeError struct{ Message string }

func (e *AttributeError) Error() string { return e.Message }

type IndexError struct{ Message string }

func (e *IndexError) Error() string { return e.Message }

type TypeError struct{ Message string }

func (e *TypeError) Error() string { return e.Message }

type ZeroDivisionError struct{ Message string }

func (e *ZeroDivisionError) Error() string { return e.Message }

type Tuple []any

type Dict struct {
	keys   []any
	values []any
}

func NewDict() *Dict { return &Dict{} }

func (d *Dict) Len() int {
	if d == nil {
		return 0
	}
	return len(d.keys)
}

func (d *Dict) Set(key, value any) {
	if d == nil {
		return
	}
	key = normalizeValue(key)
	value = normalizeValue(value)
	for i, existing := range d.keys {
		if pyEqual(existing, key) {
			d.values[i] = value
			return
		}
	}
	d.keys = append(d.keys, key)
	d.values = append(d.values, value)
}

func (d *Dict) Get(key any) (any, bool) {
	if d == nil {
		return nil, false
	}
	for i, existing := range d.keys {
		if pyEqual(existing, key) {
			return d.values[i], true
		}
	}
	return nil, false
}

func (d *Dict) Keys() []any {
	if d == nil {
		return nil
	}
	out := make([]any, len(d.keys))
	copy(out, d.keys)
	return out
}

func (d *Dict) Values() []any {
	if d == nil {
		return nil
	}
	out := make([]any, len(d.values))
	copy(out, d.values)
	return out
}

func (d *Dict) Items() []Tuple {
	if d == nil {
		return nil
	}
	out := make([]Tuple, len(d.keys))
	for i := range d.keys {
		out[i] = Tuple{d.keys[i], d.values[i]}
	}
	return out
}

type Set struct {
	items []any
}

func SetOf(values ...any) *Set {
	set := &Set{}
	for _, value := range values {
		set.Add(value)
	}
	return set
}

func (s *Set) Add(value any) {
	if s == nil {
		return
	}
	value = normalizeValue(value)
	for _, existing := range s.items {
		if pyEqual(existing, value) {
			return
		}
	}
	s.items = append(s.items, value)
}

func (s *Set) Contains(value any) bool {
	if s == nil {
		return false
	}
	for _, existing := range s.items {
		if pyEqual(existing, value) {
			return true
		}
	}
	return false
}

func (s *Set) Len() int {
	if s == nil {
		return 0
	}
	return len(s.items)
}

func (s *Set) Items() []any {
	if s == nil {
		return nil
	}
	out := make([]any, len(s.items))
	copy(out, s.items)
	return out
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case nil:
		return nil
	case bool:
		return v
	case string:
		return v
	case int64:
		return v
	case float64:
		return v
	case int:
		return int64(v)
	case int8:
		return int64(v)
	case int16:
		return int64(v)
	case int32:
		return int64(v)
	case uint:
		return int64(v)
	case uint8:
		return int64(v)
	case uint16:
		return int64(v)
	case uint32:
		return int64(v)
	case uint64:
		return int64(v)
	case float32:
		return float64(v)
	case Tuple:
		out := make(Tuple, len(v))
		for i, element := range v {
			out[i] = normalizeValue(element)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, element := range v {
			out[i] = normalizeValue(element)
		}
		return out
	case []string:
		out := make([]any, len(v))
		for i, element := range v {
			out[i] = element
		}
		return out
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		dict := NewDict()
		for _, key := range keys {
			dict.Set(key, v[key])
		}
		return dict
	case *Dict:
		dict := NewDict()
		for i := range v.keys {
			dict.Set(v.keys[i], v.values[i])
		}
		return dict
	case *Set:
		set := &Set{}
		for _, element := range v.items {
			set.Add(element)
		}
		return set
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = normalizeValue(rv.Index(i).Interface())
		}
		return out
	case reflect.Map:
		keys := rv.MapKeys()
		sort.Slice(keys, func(i, j int) bool {
			return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface())
		})
		dict := NewDict()
		for _, key := range keys {
			dict.Set(key.Interface(), rv.MapIndex(key).Interface())
		}
		return dict
	}
	return value
}

func numericValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

type pyNumber struct {
	isInt bool
	i     int64
	f     float64
}

func toPyNumber(value any) (pyNumber, bool) {
	switch v := value.(type) {
	case int64:
		return pyNumber{isInt: true, i: v, f: float64(v)}, true
	case float64:
		return pyNumber{f: v}, true
	case bool:
		if v {
			return pyNumber{isInt: true, i: 1, f: 1}, true
		}
		return pyNumber{isInt: true, f: 0}, true
	}
	return pyNumber{}, false
}

func pyEqual(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if ln, ok := numericValue(left); ok {
		rn, ok := numericValue(right)
		return ok && ln == rn
	}
	switch l := left.(type) {
	case string:
		r, ok := right.(string)
		return ok && l == r
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !pyEqual(l[i], r[i]) {
				return false
			}
		}
		return true
	case Tuple:
		r, ok := right.(Tuple)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !pyEqual(l[i], r[i]) {
				return false
			}
		}
		return true
	case *Dict:
		r, ok := right.(*Dict)
		if !ok || l.Len() != r.Len() {
			return false
		}
		for i := range l.keys {
			other, present := r.Get(l.keys[i])
			if !present || !pyEqual(l.values[i], other) {
				return false
			}
		}
		return true
	case *Set:
		r, ok := right.(*Set)
		if !ok || l.Len() != r.Len() {
			return false
		}
		for _, item := range l.items {
			if !r.Contains(item) {
				return false
			}
		}
		return true
	}
	return false
}

func pyTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case int64:
		return v != 0
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) > 0
	case Tuple:
		return len(v) > 0
	case *Dict:
		return v.Len() > 0
	case *Set:
		return v.Len() > 0
	}
	return true
}

func pyTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int64:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []any:
		return "list"
	case Tuple:
		return "tuple"
	case *Dict:
		return "dict"
	case *Set:
		return "set"
	case builtinFunc, boundMethod:
		return "builtin_function_or_method"
	}
	return "object"
}

func formatPyFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e16 {
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func pyStr(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return formatPyFloat(v)
	case string:
		return v
	case []any:
		parts := make([]string, len(v))
		for i, element := range v {
			parts[i] = pyRepr(element)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Tuple:
		parts := make([]string, len(v))
		for i, element := range v {
			parts[i] = pyRepr(element)
		}
		if len(parts) == 1 {
			return "(" + parts[0] + ",)"
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case *Dict:
		parts := make([]string, 0, v.Len())
		for i := range v.keys {
			parts = append(parts, pyRepr(v.keys[i])+": "+pyRepr(v.values[i]))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case *Set:
		if v.Len() == 0 {
			return "set()"
		}
		parts := make([]string, 0, v.Len())
		for _, element := range v.items {
			parts = append(parts, pyRepr(element))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case builtinFunc:
		return "<built-in function " + v.name + ">"
	case boundMethod:
		return "<built-in method " + v.name + ">"
	}
	return fmt.Sprintf("%v", value)
}

func pyRepr(value any) string {
	if str, ok := value.(string); ok {
		escaped := strings.ReplaceAll(str, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "'", "\\'")
		return "'" + escaped + "'"
	}
	return pyStr(value)
}

type tokenKind int

const (
	tokenEOF tokenKind = iota
	tokenInt
	tokenFloat
	tokenString
	tokenName
	tokenOperator
)

type token struct {
	kind        tokenKind
	text        string
	intValue    int64
	floatValue  float64
	stringValue string
	position    int
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func isIdentifierStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentifierPart(c byte) bool { return isIdentifierStart(c) || isASCIIDigit(c) }

func lexExpression(source string) ([]token, error) {
	tokens := make([]token, 0, len(source)/2+1)
	i := 0
	for i < len(source) {
		c := source[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v' {
			i++
			continue
		}
		if c == '#' {
			for i < len(source) && source[i] != '\n' {
				i++
			}
			continue
		}
		if isASCIIDigit(c) || (c == '.' && i+1 < len(source) && isASCIIDigit(source[i+1])) {
			tok, next, err := lexNumber(source, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			i = next
			continue
		}
		if c == '\'' || c == '"' {
			tok, next, err := lexString(source, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			i = next
			continue
		}
		if isIdentifierStart(c) {
			start := i
			i++
			for i < len(source) && isIdentifierPart(source[i]) {
				i++
			}
			tokens = append(tokens, token{kind: tokenName, text: source[start:i], position: start})
			continue
		}
		if i+1 < len(source) {
			two := source[i : i+2]
			switch two {
			case "**", "//", "<<", ">>", "<=", ">=", "==", "!=":
				tokens = append(tokens, token{kind: tokenOperator, text: two, position: i})
				i += 2
				continue
			}
		}
		if strings.ContainsRune("+-*/%<>&|^~()[]{},:.=", rune(c)) {
			tokens = append(tokens, token{kind: tokenOperator, text: string(c), position: i})
			i++
			continue
		}
		return nil, &SyntaxError{Message: fmt.Sprintf("Invalid syntax in expression: invalid character %q", c)}
	}
	tokens = append(tokens, token{kind: tokenEOF, position: len(source)})
	return tokens, nil
}

func lexNumber(source string, start int) (token, int, error) {
	i := start
	isFloat := false
	for i < len(source) && isASCIIDigit(source[i]) {
		i++
	}
	if i < len(source) && source[i] == '.' {
		isFloat = true
		i++
		for i < len(source) && isASCIIDigit(source[i]) {
			i++
		}
	}
	if i < len(source) && (source[i] == 'e' || source[i] == 'E') {
		exponent := i + 1
		if exponent < len(source) && (source[exponent] == '+' || source[exponent] == '-') {
			exponent++
		}
		if exponent < len(source) && isASCIIDigit(source[exponent]) {
			isFloat = true
			i = exponent
			for i < len(source) && isASCIIDigit(source[i]) {
				i++
			}
		}
	}
	text := source[start:i]
	if isFloat {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: invalid float literal " + text}
		}
		return token{kind: tokenFloat, floatValue: value, text: text, position: start}, i, nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: invalid integer literal " + text}
	}
	return token{kind: tokenInt, intValue: value, text: text, position: start}, i, nil
}

func lexString(source string, start int) (token, int, error) {
	quote := source[start]
	var builder strings.Builder
	i := start + 1
	for i < len(source) {
		c := source[i]
		if c == quote {
			return token{kind: tokenString, stringValue: builder.String(), position: start}, i + 1, nil
		}
		if c == '\n' {
			return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: unterminated string literal"}
		}
		if c == '\\' {
			i++
			if i >= len(source) {
				return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: unterminated string literal"}
			}
			switch esc := source[i]; esc {
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			case 'r':
				builder.WriteByte('\r')
			case '0':
				builder.WriteByte(0)
			case '\\':
				builder.WriteByte('\\')
			case '\'':
				builder.WriteByte('\'')
			case '"':
				builder.WriteByte('"')
			case 'x':
				if i+2 >= len(source) {
					return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: truncated \\x escape"}
				}
				value, err := strconv.ParseUint(source[i+1:i+3], 16, 8)
				if err != nil {
					return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: invalid \\x escape"}
				}
				builder.WriteByte(byte(value))
				i += 2
			case 'u':
				if i+4 >= len(source) {
					return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: truncated \\u escape"}
				}
				value, err := strconv.ParseUint(source[i+1:i+5], 16, 32)
				if err != nil {
					return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: invalid \\u escape"}
				}
				builder.WriteRune(rune(value))
				i += 4
			default:
				builder.WriteByte('\\')
				builder.WriteByte(esc)
			}
			i++
			continue
		}
		builder.WriteByte(c)
		i++
	}
	return token{}, 0, &SyntaxError{Message: "Invalid syntax in expression: unterminated string literal"}
}

type exprNode interface{ expressionNode() }

type constantNode struct{ value any }
type nameNode struct{ id string }
type listNode struct{ elements []exprNode }
type tupleNode struct{ elements []exprNode }
type dictNode struct {
	keys   []exprNode
	values []exprNode
}
type setNode struct{ elements []exprNode }
type binaryOpNode struct {
	op          string
	left, right exprNode
}
type unaryOpNode struct {
	op      string
	operand exprNode
}
type booleanOpNode struct {
	op     string
	values []exprNode
}
type comparisonNode struct {
	left        exprNode
	ops         []string
	comparators []exprNode
}
type conditionalNode struct {
	test, body, alternate exprNode
}
type subscriptNode struct {
	value, index exprNode
}
type attributeNode struct {
	value exprNode
	attr  string
}
type keywordArgument struct {
	name  string
	value exprNode
}
type callNode struct {
	function  exprNode
	arguments []exprNode
	keywords  []keywordArgument
}

func (*constantNode) expressionNode()    {}
func (*nameNode) expressionNode()        {}
func (*listNode) expressionNode()        {}
func (*tupleNode) expressionNode()       {}
func (*dictNode) expressionNode()        {}
func (*setNode) expressionNode()         {}
func (*binaryOpNode) expressionNode()    {}
func (*unaryOpNode) expressionNode()     {}
func (*booleanOpNode) expressionNode()   {}
func (*comparisonNode) expressionNode()  {}
func (*conditionalNode) expressionNode() {}
func (*subscriptNode) expressionNode()   {}
func (*attributeNode) expressionNode()   {}
func (*callNode) expressionNode()        {}

var (
	additiveOperators       = map[string]string{"+": "Add", "-": "Sub"}
	multiplicativeOperators = map[string]string{"*": "Mult", "/": "Div", "//": "FloorDiv", "%": "Mod"}
	shiftOperators          = map[string]string{"<<": "LShift", ">>": "RShift"}
	bitOrOperators          = map[string]string{"|": "BitOr"}
	bitXorOperators         = map[string]string{"^": "BitXor"}
	bitAndOperators         = map[string]string{"&": "BitAnd"}
	unaryOperators          = map[string]string{"-": "USub", "+": "UAdd", "~": "Invert"}
)

type expressionParser struct {
	tokens []token
	pos    int
}

func parseExpressionTokens(tokens []token) (exprNode, error) {
	parser := &expressionParser{tokens: tokens}
	node, err := parser.parseConditional()
	if err != nil {
		return nil, err
	}
	if parser.peek().kind != tokenEOF {
		return nil, parser.syntaxError("unexpected " + describeToken(parser.peek()))
	}
	return node, nil
}

func describeToken(tok token) string {
	switch tok.kind {
	case tokenEOF:
		return "end of expression"
	case tokenName, tokenOperator:
		return "'" + tok.text + "'"
	case tokenString:
		return "string literal"
	case tokenInt, tokenFloat:
		return "number"
	}
	return "token"
}

func (p *expressionParser) peek() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos]
}

func (p *expressionParser) peekAt(offset int) token {
	index := p.pos + offset
	if index >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[index]
}

func (p *expressionParser) advance() token {
	tok := p.peek()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *expressionParser) isOperator(op string) bool {
	tok := p.peek()
	return tok.kind == tokenOperator && tok.text == op
}

func (p *expressionParser) matchOperator(op string) bool {
	if p.isOperator(op) {
		p.advance()
		return true
	}
	return false
}

func (p *expressionParser) expectOperator(op string) error {
	if !p.matchOperator(op) {
		return p.syntaxError("expected '" + op + "'")
	}
	return nil
}

func (p *expressionParser) isName(name string) bool {
	tok := p.peek()
	return tok.kind == tokenName && tok.text == name
}

func (p *expressionParser) syntaxError(detail string) error {
	return &SyntaxError{Message: "Invalid syntax in expression: " + detail}
}

func (p *expressionParser) parseConditional() (exprNode, error) {
	body, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if !p.isName("if") {
		return body, nil
	}
	p.advance()
	test, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if !p.isName("else") {
		return nil, p.syntaxError("expected 'else'")
	}
	p.advance()
	alternate, err := p.parseConditional()
	if err != nil {
		return nil, err
	}
	return &conditionalNode{test: test, body: body, alternate: alternate}, nil
}

func (p *expressionParser) parseOr() (exprNode, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	values := []exprNode{first}
	for p.isName("or") {
		p.advance()
		value, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if len(values) == 1 {
		return first, nil
	}
	return &booleanOpNode{op: "Or", values: values}, nil
}

func (p *expressionParser) parseAnd() (exprNode, error) {
	first, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	values := []exprNode{first}
	for p.isName("and") {
		p.advance()
		value, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if len(values) == 1 {
		return first, nil
	}
	return &booleanOpNode{op: "And", values: values}, nil
}

func (p *expressionParser) parseNot() (exprNode, error) {
	if p.isName("not") {
		p.advance()
		operand, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &unaryOpNode{op: "Not", operand: operand}, nil
	}
	return p.parseComparison()
}

func (p *expressionParser) parseComparison() (exprNode, error) {
	left, err := p.parseBitOr()
	if err != nil {
		return nil, err
	}
	var ops []string
	var comparators []exprNode
	for {
		op, ok := p.comparisonOperator()
		if !ok {
			break
		}
		right, err := p.parseBitOr()
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
		comparators = append(comparators, right)
	}
	if len(ops) == 0 {
		return left, nil
	}
	return &comparisonNode{left: left, ops: ops, comparators: comparators}, nil
}

func (p *expressionParser) comparisonOperator() (string, bool) {
	tok := p.peek()
	if tok.kind == tokenOperator {
		switch tok.text {
		case "<":
			p.advance()
			return "Lt", true
		case ">":
			p.advance()
			return "Gt", true
		case "<=":
			p.advance()
			return "LtE", true
		case ">=":
			p.advance()
			return "GtE", true
		case "==":
			p.advance()
			return "Eq", true
		case "!=":
			p.advance()
			return "NotEq", true
		}
		return "", false
	}
	if tok.kind != tokenName {
		return "", false
	}
	switch tok.text {
	case "in":
		p.advance()
		return "In", true
	case "is":
		p.advance()
		if p.isName("not") {
			p.advance()
			return "IsNot", true
		}
		return "Is", true
	case "not":
		next := p.peekAt(1)
		if next.kind == tokenName && next.text == "in" {
			p.advance()
			p.advance()
			return "NotIn", true
		}
	}
	return "", false
}

func (p *expressionParser) parseBinaryLevel(next func() (exprNode, error), operators map[string]string) (exprNode, error) {
	left, err := next()
	if err != nil {
		return nil, err
	}
	for {
		tok := p.peek()
		if tok.kind != tokenOperator {
			break
		}
		op, ok := operators[tok.text]
		if !ok {
			break
		}
		p.advance()
		right, err := next()
		if err != nil {
			return nil, err
		}
		left = &binaryOpNode{op: op, left: left, right: right}
	}
	return left, nil
}

func (p *expressionParser) parseBitOr() (exprNode, error) {
	return p.parseBinaryLevel(p.parseBitXor, bitOrOperators)
}

func (p *expressionParser) parseBitXor() (exprNode, error) {
	return p.parseBinaryLevel(p.parseBitAnd, bitXorOperators)
}

func (p *expressionParser) parseBitAnd() (exprNode, error) {
	return p.parseBinaryLevel(p.parseShift, bitAndOperators)
}

func (p *expressionParser) parseShift() (exprNode, error) {
	return p.parseBinaryLevel(p.parseAdditive, shiftOperators)
}

func (p *expressionParser) parseAdditive() (exprNode, error) {
	return p.parseBinaryLevel(p.parseMultiplicative, additiveOperators)
}

func (p *expressionParser) parseMultiplicative() (exprNode, error) {
	return p.parseBinaryLevel(p.parseFactor, multiplicativeOperators)
}

func (p *expressionParser) parseFactor() (exprNode, error) {
	if tok := p.peek(); tok.kind == tokenOperator {
		if op, ok := unaryOperators[tok.text]; ok {
			p.advance()
			operand, err := p.parseFactor()
			if err != nil {
				return nil, err
			}
			return &unaryOpNode{op: op, operand: operand}, nil
		}
	}
	return p.parsePower()
}

func (p *expressionParser) parsePower() (exprNode, error) {
	base, err := p.parseAtomExpression()
	if err != nil {
		return nil, err
	}
	if !p.matchOperator("**") {
		return base, nil
	}
	exponent, err := p.parseFactor()
	if err != nil {
		return nil, err
	}
	return &binaryOpNode{op: "Pow", left: base, right: exponent}, nil
}

func (p *expressionParser) parseAtomExpression() (exprNode, error) {
	node, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.isOperator("("):
			call, err := p.parseCall(node)
			if err != nil {
				return nil, err
			}
			node = call
		case p.isOperator("["):
			p.advance()
			index, err := p.parseConditional()
			if err != nil {
				return nil, err
			}
			if p.isOperator(":") {
				return nil, &ValueError{Message: "Use of Slice is not allowed"}
			}
			if err := p.expectOperator("]"); err != nil {
				return nil, err
			}
			node = &subscriptNode{value: node, index: index}
		case p.isOperator("."):
			p.advance()
			name := p.peek()
			if name.kind != tokenName {
				return nil, p.syntaxError("expected attribute name")
			}
			p.advance()
			node = &attributeNode{value: node, attr: name.text}
		default:
			return node, nil
		}
	}
}

func (p *expressionParser) parseCall(function exprNode) (exprNode, error) {
	p.advance()
	call := &callNode{function: function}
	if p.matchOperator(")") {
		return call, nil
	}
	for {
		if p.isOperator("*") || p.isOperator("**") {
			return nil, p.syntaxError("starred arguments are not allowed")
		}
		if p.peek().kind == tokenName && p.peekAt(1).kind == tokenOperator && p.peekAt(1).text == "=" {
			name := p.advance().text
			p.advance()
			value, err := p.parseConditional()
			if err != nil {
				return nil, err
			}
			call.keywords = append(call.keywords, keywordArgument{name: name, value: value})
		} else {
			value, err := p.parseConditional()
			if err != nil {
				return nil, err
			}
			call.arguments = append(call.arguments, value)
		}
		if p.matchOperator(",") {
			if p.matchOperator(")") {
				return call, nil
			}
			continue
		}
		if p.matchOperator(")") {
			return call, nil
		}
		return nil, p.syntaxError("expected ',' or ')' in call")
	}
}

func (p *expressionParser) parseAtom() (exprNode, error) {
	tok := p.peek()
	switch tok.kind {
	case tokenInt:
		p.advance()
		return &constantNode{value: tok.intValue}, nil
	case tokenFloat:
		p.advance()
		return &constantNode{value: tok.floatValue}, nil
	case tokenString:
		p.advance()
		return &constantNode{value: tok.stringValue}, nil
	case tokenName:
		switch tok.text {
		case "True":
			p.advance()
			return &constantNode{value: true}, nil
		case "False":
			p.advance()
			return &constantNode{value: false}, nil
		case "None":
			p.advance()
			return &constantNode{value: nil}, nil
		case "lambda":
			return nil, &ValueError{Message: "Use of Lambda is not allowed"}
		case "and", "or", "not", "if", "else", "in", "is", "for":
			return nil, p.syntaxError("unexpected keyword '" + tok.text + "'")
		}
		p.advance()
		return &nameNode{id: tok.text}, nil
	case tokenOperator:
		switch tok.text {
		case "(":
			return p.parseParenthesized()
		case "[":
			return p.parseListLiteral()
		case "{":
			return p.parseDictOrSetLiteral()
		}
		return nil, p.syntaxError("unexpected " + describeToken(tok))
	case tokenEOF:
		return nil, p.syntaxError("unexpected end of expression")
	}
	return nil, p.syntaxError("unexpected " + describeToken(tok))
}

func (p *expressionParser) parseParenthesized() (exprNode, error) {
	p.advance()
	if p.matchOperator(")") {
		return &tupleNode{}, nil
	}
	first, err := p.parseConditional()
	if err != nil {
		return nil, err
	}
	if p.isName("for") {
		return nil, &ValueError{Message: "Use of GeneratorExp is not allowed"}
	}
	if p.matchOperator(")") {
		return first, nil
	}
	if !p.matchOperator(",") {
		return nil, p.syntaxError("expected ')'")
	}
	elements := []exprNode{first}
	for {
		if p.matchOperator(")") {
			return &tupleNode{elements: elements}, nil
		}
		element, err := p.parseConditional()
		if err != nil {
			return nil, err
		}
		if p.isName("for") {
			return nil, &ValueError{Message: "Use of GeneratorExp is not allowed"}
		}
		elements = append(elements, element)
		if p.matchOperator(",") {
			continue
		}
		if p.matchOperator(")") {
			return &tupleNode{elements: elements}, nil
		}
		return nil, p.syntaxError("expected ',' or ')'")
	}
}

func (p *expressionParser) parseListLiteral() (exprNode, error) {
	p.advance()
	if p.matchOperator("]") {
		return &listNode{}, nil
	}
	first, err := p.parseConditional()
	if err != nil {
		return nil, err
	}
	if p.isName("for") {
		return nil, &ValueError{Message: "Use of ListComp is not allowed"}
	}
	elements := []exprNode{first}
	for p.matchOperator(",") {
		if p.matchOperator("]") {
			return &listNode{elements: elements}, nil
		}
		element, err := p.parseConditional()
		if err != nil {
			return nil, err
		}
		if p.isName("for") {
			return nil, &ValueError{Message: "Use of ListComp is not allowed"}
		}
		elements = append(elements, element)
	}
	if err := p.expectOperator("]"); err != nil {
		return nil, err
	}
	return &listNode{elements: elements}, nil
}

func (p *expressionParser) parseDictOrSetLiteral() (exprNode, error) {
	p.advance()
	if p.matchOperator("}") {
		return &dictNode{}, nil
	}
	first, err := p.parseConditional()
	if err != nil {
		return nil, err
	}
	if p.matchOperator(":") {
		value, err := p.parseConditional()
		if err != nil {
			return nil, err
		}
		if p.isName("for") {
			return nil, &ValueError{Message: "Use of DictComp is not allowed"}
		}
		keys := []exprNode{first}
		values := []exprNode{value}
		for p.matchOperator(",") {
			if p.matchOperator("}") {
				return &dictNode{keys: keys, values: values}, nil
			}
			key, err := p.parseConditional()
			if err != nil {
				return nil, err
			}
			if err := p.expectOperator(":"); err != nil {
				return nil, err
			}
			entryValue, err := p.parseConditional()
			if err != nil {
				return nil, err
			}
			if p.isName("for") {
				return nil, &ValueError{Message: "Use of DictComp is not allowed"}
			}
			keys = append(keys, key)
			values = append(values, entryValue)
		}
		if err := p.expectOperator("}"); err != nil {
			return nil, err
		}
		return &dictNode{keys: keys, values: values}, nil
	}
	if p.isName("for") {
		return nil, &ValueError{Message: "Use of SetComp is not allowed"}
	}
	elements := []exprNode{first}
	for p.matchOperator(",") {
		if p.matchOperator("}") {
			return &setNode{elements: elements}, nil
		}
		element, err := p.parseConditional()
		if err != nil {
			return nil, err
		}
		if p.isName("for") {
			return nil, &ValueError{Message: "Use of SetComp is not allowed"}
		}
		elements = append(elements, element)
	}
	if err := p.expectOperator("}"); err != nil {
		return nil, err
	}
	return &setNode{elements: elements}, nil
}

var (
	safeFunctionNames = keySet([]string{
		"len", "int", "float", "str", "bool", "list", "dict", "tuple", "set",
		"min", "max", "sum", "abs", "round", "all", "any",
	})
	safeMethodNames = keySet([]string{
		"get", "keys", "values", "items", "lower", "upper", "strip", "split",
	})
)

type builtinFunc struct{ name string }

type boundMethod struct {
	receiver any
	name     string
}

var safeEvalClock = time.Now

type evaluator struct {
	context     map[string]any
	deadline    time.Time
	hasDeadline bool
	timeoutMS   int
}

func (e *evaluator) checkTimeout() error {
	if !e.hasDeadline {
		return nil
	}
	if !safeEvalClock().Before(e.deadline) {
		return &TimeoutError{Message: fmt.Sprintf("safe_eval exceeded %dms execution timeout", e.timeoutMS)}
	}
	return nil
}

func (e *evaluator) evaluate(node exprNode) (any, error) {
	if err := e.checkTimeout(); err != nil {
		return nil, err
	}
	switch n := node.(type) {
	case *constantNode:
		return n.value, nil
	case *nameNode:
		return e.evaluateName(n)
	case *listNode:
		values := make([]any, len(n.elements))
		for i, element := range n.elements {
			value, err := e.evaluate(element)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	case *tupleNode:
		values := make(Tuple, len(n.elements))
		for i, element := range n.elements {
			value, err := e.evaluate(element)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		return values, nil
	case *dictNode:
		dict := NewDict()
		for i := range n.keys {
			key, err := e.evaluate(n.keys[i])
			if err != nil {
				return nil, err
			}
			if key == nil {
				continue
			}
			value, err := e.evaluate(n.values[i])
			if err != nil {
				return nil, err
			}
			dict.Set(key, value)
		}
		return dict, nil
	case *setNode:
		set := &Set{}
		for _, element := range n.elements {
			value, err := e.evaluate(element)
			if err != nil {
				return nil, err
			}
			set.Add(value)
		}
		return set, nil
	case *binaryOpNode:
		left, err := e.evaluate(n.left)
		if err != nil {
			return nil, err
		}
		right, err := e.evaluate(n.right)
		if err != nil {
			return nil, err
		}
		return applyBinaryOperator(n.op, left, right)
	case *unaryOpNode:
		operand, err := e.evaluate(n.operand)
		if err != nil {
			return nil, err
		}
		return applyUnaryOperator(n.op, operand)
	case *booleanOpNode:
		return e.evaluateBoolean(n)
	case *comparisonNode:
		left, err := e.evaluate(n.left)
		if err != nil {
			return nil, err
		}
		for i, op := range n.ops {
			right, err := e.evaluate(n.comparators[i])
			if err != nil {
				return nil, err
			}
			ok, err := applyComparisonOperator(op, left, right)
			if err != nil {
				return nil, err
			}
			if !ok {
				return false, nil
			}
			left = right
		}
		return true, nil
	case *conditionalNode:
		test, err := e.evaluate(n.test)
		if err != nil {
			return nil, err
		}
		if pyTruthy(test) {
			return e.evaluate(n.body)
		}
		return e.evaluate(n.alternate)
	case *subscriptNode:
		container, err := e.evaluate(n.value)
		if err != nil {
			return nil, err
		}
		index, err := e.evaluate(n.index)
		if err != nil {
			return nil, err
		}
		return subscriptValue(container, index)
	case *attributeNode:
		if strings.HasPrefix(n.attr, "_") {
			return nil, &ValueError{Message: fmt.Sprintf("Access to private attribute '%s' is not allowed", n.attr)}
		}
		value, err := e.evaluate(n.value)
		if err != nil {
			return nil, err
		}
		return attributeValue(value, n.attr)
	case *callNode:
		return e.evaluateCall(n)
	}
	return nil, &ValueError{Message: fmt.Sprintf("Use of %T is not allowed", node)}
}

func (e *evaluator) evaluateName(node *nameNode) (any, error) {
	if ok := hasKey(safeFunctionNames, node.id); ok {
		return builtinFunc{name: node.id}, nil
	}
	if value, ok := e.context[node.id]; ok {
		return value, nil
	}
	return nil, &NameError{Message: fmt.Sprintf("Name '%s' is not defined", node.id)}
}

func (e *evaluator) evaluateBoolean(node *booleanOpNode) (any, error) {
	switch node.op {
	case "And":
		var result any = true
		for _, valueNode := range node.values {
			value, err := e.evaluate(valueNode)
			if err != nil {
				return nil, err
			}
			result = value
			if !pyTruthy(value) {
				return value, nil
			}
		}
		return result, nil
	case "Or":
		var result any = false
		for _, valueNode := range node.values {
			value, err := e.evaluate(valueNode)
			if err != nil {
				return nil, err
			}
			result = value
			if pyTruthy(value) {
				return value, nil
			}
		}
		return result, nil
	}
	return nil, &ValueError{Message: "Boolean operator " + node.op + " is not allowed"}
}

func (e *evaluator) evaluateCall(node *callNode) (any, error) {
	function, err := e.evaluate(node.function)
	if err != nil {
		return nil, err
	}

	var builtin builtinFunc
	var method boundMethod
	isBuiltin := false
	isMethod := false
	switch f := function.(type) {
	case builtinFunc:
		builtin = f
		isBuiltin = true
	case boundMethod:
		if !hasKey(safeMethodNames, f.name) {
			return nil, &ValueError{Message: "Call to function/method is not allowed"}
		}
		method = f
		isMethod = true
	default:
		return nil, &ValueError{Message: "Call to function/method is not allowed"}
	}

	arguments := make([]any, len(node.arguments))
	for i, argumentNode := range node.arguments {
		value, err := e.evaluate(argumentNode)
		if err != nil {
			return nil, err
		}
		arguments[i] = value
	}
	keywords := make([]keywordValue, len(node.keywords))
	for i, keyword := range node.keywords {
		value, err := e.evaluate(keyword.value)
		if err != nil {
			return nil, err
		}
		keywords[i] = keywordValue{name: keyword.name, value: value}
	}

	if err := e.checkTimeout(); err != nil {
		return nil, err
	}

	if isBuiltin {
		return callBuiltinFunction(builtin.name, arguments, keywords)
	}
	if isMethod {
		return callBoundMethod(method, arguments, keywords)
	}
	return nil, &ValueError{Message: "Call to function/method is not allowed"}
}

type keywordValue struct {
	name  string
	value any
}

func subscriptValue(container, index any) (any, error) {
	switch value := container.(type) {
	case []any:
		position, ok := integerIndex(index)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("list indices must be integers, not %s", pyTypeName(index))}
		}
		return sequenceItem(value, position)
	case Tuple:
		position, ok := integerIndex(index)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("tuple indices must be integers, not %s", pyTypeName(index))}
		}
		return sequenceItem([]any(value), position)
	case *Dict:
		result, ok := value.Get(index)
		if !ok {
			return nil, &KeyError{Message: pyRepr(index)}
		}
		return result, nil
	case string:
		position, ok := integerIndex(index)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("string indices must be integers, not %s", pyTypeName(index))}
		}
		runes := []rune(value)
		item, err := sequenceItem(toAnySlice(runes), position)
		if err != nil {
			return nil, err
		}
		return item.(string), nil
	}
	return nil, &TypeError{Message: fmt.Sprintf("'%s' object is not subscriptable", pyTypeName(container))}
}

func toAnySlice(runes []rune) []any {
	values := make([]any, len(runes))
	for i, value := range runes {
		values[i] = string(value)
	}
	return values
}

func sequenceItem(values []any, position int64) (any, error) {
	length := int64(len(values))
	index := position
	if index < 0 {
		index += length
	}
	if index < 0 || index >= length {
		return nil, &IndexError{Message: "index out of range"}
	}
	return values[index], nil
}

func integerIndex(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func attributeValue(value any, attr string) (any, error) {
	switch v := value.(type) {
	case *Dict:
		switch attr {
		case "get", "keys", "values", "items":
			return boundMethod{receiver: v, name: attr}, nil
		}
	case string:
		return boundMethod{receiver: v, name: attr}, nil
	}
	return nil, &AttributeError{Message: fmt.Sprintf("Object has no attribute '%s'", attr)}
}

func applyBinaryOperator(op string, left, right any) (any, error) {
	switch op {
	case "Add":
		return addValues(left, right)
	case "Sub", "Mult", "Div", "FloorDiv", "Mod":
		return arithmeticValues(op, left, right)
	case "Pow":
		return powerValues(left, right)
	case "LShift", "RShift":
		return shiftValues(op, left, right)
	case "BitOr", "BitXor", "BitAnd":
		return bitwiseValues(op, left, right)
	}
	return nil, &ValueError{Message: "Operator " + op + " is not allowed"}
}

func addValues(left, right any) (any, error) {
	if l, ok := left.(string); ok {
		r, ok := right.(string)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("can only concatenate str (not \"%s\") to str", pyTypeName(right))}
		}
		return l + r, nil
	}
	if l, ok := left.([]any); ok {
		r, ok := right.([]any)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("can only concatenate list (not \"%s\") to list", pyTypeName(right))}
		}
		out := make([]any, 0, len(l)+len(r))
		out = append(out, l...)
		out = append(out, r...)
		return out, nil
	}
	if l, ok := left.(Tuple); ok {
		r, ok := right.(Tuple)
		if !ok {
			return nil, &TypeError{Message: fmt.Sprintf("can only concatenate tuple (not \"%s\") to tuple", pyTypeName(right))}
		}
		out := make(Tuple, 0, len(l)+len(r))
		out = append(out, l...)
		out = append(out, r...)
		return out, nil
	}
	return arithmeticValues("Add", left, right)
}

func arithmeticValues(op string, left, right any) (any, error) {
	if op == "Mult" {
		if result, ok, err := repeatSequence(left, right); ok || err != nil {
			return result, err
		}
	}
	ln, lok := toPyNumber(left)
	rn, rok := toPyNumber(right)
	if !lok || !rok {
		return nil, &TypeError{Message: fmt.Sprintf(
			"unsupported operand type(s) for %s: '%s' and '%s'",
			operatorSymbol(op), pyTypeName(left), pyTypeName(right),
		)}
	}
	switch op {
	case "Add":
		if ln.isInt && rn.isInt {
			return ln.i + rn.i, nil
		}
		return ln.f + rn.f, nil
	case "Sub":
		if ln.isInt && rn.isInt {
			return ln.i - rn.i, nil
		}
		return ln.f - rn.f, nil
	case "Mult":
		if ln.isInt && rn.isInt {
			return ln.i * rn.i, nil
		}
		return ln.f * rn.f, nil
	case "Div":
		if rn.f == 0 {
			return nil, &ZeroDivisionError{Message: "division by zero"}
		}
		return ln.f / rn.f, nil
	case "FloorDiv":
		if ln.isInt && rn.isInt {
			if rn.i == 0 {
				return nil, &ZeroDivisionError{Message: "integer division or modulo by zero"}
			}
			quotient := ln.i / rn.i
			if ln.i%rn.i != 0 && (ln.i < 0) != (rn.i < 0) {
				quotient--
			}
			return quotient, nil
		}
		if rn.f == 0 {
			return nil, &ZeroDivisionError{Message: "float floor division by zero"}
		}
		return math.Floor(ln.f / rn.f), nil
	case "Mod":
		if ln.isInt && rn.isInt {
			if rn.i == 0 {
				return nil, &ZeroDivisionError{Message: "integer division or modulo by zero"}
			}
			remainder := ln.i % rn.i
			if remainder != 0 && (remainder < 0) != (rn.i < 0) {
				remainder += rn.i
			}
			return remainder, nil
		}
		if rn.f == 0 {
			return nil, &ZeroDivisionError{Message: "float modulo"}
		}
		remainder := math.Mod(ln.f, rn.f)
		if remainder != 0 && (remainder < 0) != (rn.f < 0) {
			remainder += rn.f
		}
		return remainder, nil
	}
	return nil, &ValueError{Message: "Operator " + op + " is not allowed"}
}

func operatorSymbol(op string) string {
	switch op {
	case "Add":
		return "+"
	case "Sub":
		return "-"
	case "Mult":
		return "*"
	case "Div":
		return "/"
	case "FloorDiv":
		return "//"
	case "Mod":
		return "%"
	case "Pow":
		return "**"
	case "LShift":
		return "<<"
	case "RShift":
		return ">>"
	case "BitOr":
		return "|"
	case "BitXor":
		return "^"
	case "BitAnd":
		return "&"
	}
	return op
}

const maxRepetitionCount = 1 << 20

func repeatSequence(left, right any) (any, bool, error) {
	if count, ok := integerIndex(right); ok {
		return repeatWithCount(left, count)
	}
	if count, ok := integerIndex(left); ok {
		return repeatWithCount(right, count)
	}
	return nil, false, nil
}

func repeatWithCount(value any, count int64) (any, bool, error) {
	switch v := value.(type) {
	case string:
		if count > maxRepetitionCount {
			return nil, true, &ValueError{Message: "Repetition count exceeds safe size limit"}
		}
		if count <= 0 {
			return "", true, nil
		}
		return strings.Repeat(v, int(count)), true, nil
	case []any:
		if count > maxRepetitionCount {
			return nil, true, &ValueError{Message: "Repetition count exceeds safe size limit"}
		}
		if count <= 0 {
			return []any{}, true, nil
		}
		out := make([]any, 0, len(v)*int(count))
		for i := int64(0); i < count; i++ {
			out = append(out, v...)
		}
		return out, true, nil
	case Tuple:
		if count > maxRepetitionCount {
			return nil, true, &ValueError{Message: "Repetition count exceeds safe size limit"}
		}
		if count <= 0 {
			return Tuple{}, true, nil
		}
		out := make(Tuple, 0, len(v)*int(count))
		for i := int64(0); i < count; i++ {
			out = append(out, v...)
		}
		return out, true, nil
	}
	return nil, false, nil
}

func powerValues(base, exponent any) (any, error) {
	if magnitude, ok := numericValue(exponent); ok && math.Abs(magnitude) > MaxPowerAbsExponent {
		return nil, &ValueError{Message: fmt.Sprintf("Power exponent exceeds safe limit (%d)", MaxPowerAbsExponent)}
	}
	bn, bok := toPyNumber(base)
	en, eok := toPyNumber(exponent)
	if !bok || !eok {
		return nil, &TypeError{Message: fmt.Sprintf(
			"unsupported operand type(s) for ** or pow(): '%s' and '%s'",
			pyTypeName(base), pyTypeName(exponent),
		)}
	}
	if bn.isInt && en.isInt && en.i > 0 {
		absoluteBase := bn.i
		if absoluteBase < 0 {
			absoluteBase = -absoluteBase
		}
		if absoluteBase > 1 {
			estimatedBits := en.i * int64(intBitLength(absoluteBase))
			if estimatedBits > MaxPowerResultBits {
				return nil, &ValueError{Message: "Power operation exceeds safe size limit"}
			}
		}
		result := new(big.Int).Exp(big.NewInt(bn.i), big.NewInt(en.i), nil)
		if !result.IsInt64() {
			return nil, &ValueError{Message: "Power operation exceeds safe size limit"}
		}
		return result.Int64(), nil
	}
	if bn.isInt && en.isInt && en.i < 0 {
		if bn.i == 0 {
			return nil, &ZeroDivisionError{Message: "0.0 cannot be raised to a negative power"}
		}
		return math.Pow(float64(bn.i), float64(en.i)), nil
	}
	result := math.Pow(bn.f, en.f)
	if math.IsNaN(result) {
		return nil, &ValueError{Message: "math domain error"}
	}
	return result, nil
}

func intBitLength(value int64) int {
	if value < 0 {
		value = -value
	}
	return bits.Len64(uint64(value))
}

func integerValue(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func shiftValues(op string, left, right any) (any, error) {
	value, lok := integerValue(left)
	count, rok := integerValue(right)
	if !lok || !rok {
		return nil, &TypeError{Message: fmt.Sprintf(
			"unsupported operand type(s) for %s: '%s' and '%s'",
			operatorSymbol(op), pyTypeName(left), pyTypeName(right),
		)}
	}
	if count < 0 {
		return nil, &ValueError{Message: "negative shift count"}
	}
	if count > MaxPowerResultBits {
		return nil, &ValueError{Message: "Shift operation exceeds safe size limit"}
	}
	if op == "LShift" {
		return value << uint(count), nil
	}
	return value >> uint(count), nil
}

func bitwiseValues(op string, left, right any) (any, error) {
	value, lok := integerValue(left)
	other, rok := integerValue(right)
	if !lok || !rok {
		return nil, &TypeError{Message: fmt.Sprintf(
			"unsupported operand type(s) for %s: '%s' and '%s'",
			operatorSymbol(op), pyTypeName(left), pyTypeName(right),
		)}
	}
	switch op {
	case "BitOr":
		return value | other, nil
	case "BitXor":
		return value ^ other, nil
	case "BitAnd":
		return value & other, nil
	}
	return nil, &ValueError{Message: "Operator " + op + " is not allowed"}
}

func applyUnaryOperator(op string, value any) (any, error) {
	switch op {
	case "Not":
		return !pyTruthy(value), nil
	case "USub":
		if number, ok := toPyNumber(value); ok {
			if number.isInt {
				return -number.i, nil
			}
			return -number.f, nil
		}
		return nil, &TypeError{Message: fmt.Sprintf("bad operand type for unary -: '%s'", pyTypeName(value))}
	case "UAdd":
		if number, ok := toPyNumber(value); ok {
			if number.isInt {
				return number.i, nil
			}
			return number.f, nil
		}
		return nil, &TypeError{Message: fmt.Sprintf("bad operand type for unary +: '%s'", pyTypeName(value))}
	case "Invert":
		if integer, ok := integerValue(value); ok {
			return ^integer, nil
		}
		return nil, &TypeError{Message: fmt.Sprintf("bad operand type for unary ~: '%s'", pyTypeName(value))}
	}
	return nil, &ValueError{Message: "Operator " + op + " is not allowed"}
}

func applyComparisonOperator(op string, left, right any) (bool, error) {
	switch op {
	case "Eq":
		return pyEqual(left, right), nil
	case "NotEq":
		return !pyEqual(left, right), nil
	case "Lt":
		result, err := orderedCompare(left, right)
		return result < 0, err
	case "LtE":
		result, err := orderedCompare(left, right)
		return result <= 0, err
	case "Gt":
		result, err := orderedCompare(left, right)
		return result > 0, err
	case "GtE":
		result, err := orderedCompare(left, right)
		return result >= 0, err
	case "Is":
		return pyIdentity(left, right), nil
	case "IsNot":
		return !pyIdentity(left, right), nil
	case "In":
		return pyContains(right, left)
	case "NotIn":
		contained, err := pyContains(right, left)
		if err != nil {
			return false, err
		}
		return !contained, nil
	}
	return false, &ValueError{Message: "Operator " + op + " is not allowed"}
}

func orderedCompare(left, right any) (int, error) {
	if ln, ok := toPyNumber(left); ok {
		rn, ok := toPyNumber(right)
		if !ok {
			return 0, unorderableTypeError(left, right)
		}
		if ln.isInt && rn.isInt {
			switch {
			case ln.i < rn.i:
				return -1, nil
			case ln.i > rn.i:
				return 1, nil
			}
			return 0, nil
		}
		switch {
		case ln.f < rn.f:
			return -1, nil
		case ln.f > rn.f:
			return 1, nil
		}
		return 0, nil
	}
	if l, ok := left.(string); ok {
		r, ok := right.(string)
		if !ok {
			return 0, unorderableTypeError(left, right)
		}
		return strings.Compare(l, r), nil
	}
	if l, ok := left.([]any); ok {
		r, ok := right.([]any)
		if !ok {
			return 0, unorderableTypeError(left, right)
		}
		return compareSequences(l, r)
	}
	if l, ok := left.(Tuple); ok {
		r, ok := right.(Tuple)
		if !ok {
			return 0, unorderableTypeError(left, right)
		}
		return compareSequences([]any(l), []any(r))
	}
	return 0, unorderableTypeError(left, right)
}

func compareSequences(left, right []any) (int, error) {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for i := 0; i < limit; i++ {
		if pyEqual(left[i], right[i]) {
			continue
		}
		return orderedCompare(left[i], right[i])
	}
	switch {
	case len(left) < len(right):
		return -1, nil
	case len(left) > len(right):
		return 1, nil
	}
	return 0, nil
}

func unorderableTypeError(left, right any) error {
	return &TypeError{Message: fmt.Sprintf(
		"'<' not supported between instances of '%s' and '%s'",
		pyTypeName(left), pyTypeName(right),
	)}
}

func pyIdentity(left, right any) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	switch l := left.(type) {
	case bool:
		r, ok := right.(bool)
		return ok && l == r
	case int64:
		r, ok := right.(int64)
		return ok && l == r
	case float64:
		r, ok := right.(float64)
		return ok && l == r
	case string:
		r, ok := right.(string)
		return ok && l == r
	case *Dict:
		r, ok := right.(*Dict)
		return ok && l == r
	case *Set:
		r, ok := right.(*Set)
		return ok && l == r
	}
	return false
}

func pyContains(container, item any) (bool, error) {
	switch c := container.(type) {
	case []any:
		for _, element := range c {
			if pyEqual(element, item) {
				return true, nil
			}
		}
		return false, nil
	case Tuple:
		for _, element := range c {
			if pyEqual(element, item) {
				return true, nil
			}
		}
		return false, nil
	case *Set:
		return c.Contains(item), nil
	case *Dict:
		_, ok := c.Get(item)
		return ok, nil
	case string:
		needle, ok := item.(string)
		if !ok {
			return false, &TypeError{Message: "'in <string>' requires string as left operand"}
		}
		return strings.Contains(c, needle), nil
	}
	return false, &TypeError{Message: fmt.Sprintf("argument of type '%s' is not iterable", pyTypeName(container))}
}

func callBuiltinFunction(name string, arguments []any, keywords []keywordValue) (any, error) {
	if len(keywords) > 0 && name != "dict" {
		return nil, &TypeError{Message: name + "() takes no keyword arguments"}
	}
	switch name {
	case "len":
		return builtinLen(arguments)
	case "int":
		return builtinInt(arguments)
	case "float":
		return builtinFloat(arguments)
	case "str":
		return builtinStr(arguments)
	case "bool":
		return builtinBool(arguments)
	case "list":
		return builtinSequence(arguments, "list")
	case "tuple":
		return builtinSequence(arguments, "tuple")
	case "set":
		return builtinSequence(arguments, "set")
	case "dict":
		return builtinDict(arguments, keywords)
	case "min":
		return builtinMinMax("min", arguments)
	case "max":
		return builtinMinMax("max", arguments)
	case "sum":
		return builtinSum(arguments)
	case "abs":
		return builtinAbs(arguments)
	case "round":
		return builtinRound(arguments)
	case "all":
		return builtinAllAny("all", arguments)
	case "any":
		return builtinAllAny("any", arguments)
	}
	return nil, &ValueError{Message: "Call to function/method is not allowed"}
}

func arityError(name string, given, minimum, maximum int) error {
	expected := strconv.Itoa(minimum)
	if minimum != maximum {
		expected = fmt.Sprintf("%d to %d", minimum, maximum)
	}
	return &TypeError{Message: fmt.Sprintf("%s() takes %s positional arguments (%d given)", name, expected, given)}
}

func builtinLen(arguments []any) (any, error) {
	if len(arguments) != 1 {
		return nil, arityError("len", len(arguments), 1, 1)
	}
	switch value := arguments[0].(type) {
	case string:
		return int64(len([]rune(value))), nil
	case []any:
		return int64(len(value)), nil
	case Tuple:
		return int64(len(value)), nil
	case *Dict:
		return int64(value.Len()), nil
	case *Set:
		return int64(value.Len()), nil
	}
	return nil, &TypeError{Message: fmt.Sprintf("object of type '%s' has no len()", pyTypeName(arguments[0]))}
}

func builtinInt(arguments []any) (any, error) {
	if len(arguments) == 0 {
		return int64(0), nil
	}
	if len(arguments) != 1 {
		return nil, arityError("int", len(arguments), 0, 1)
	}
	switch value := arguments[0].(type) {
	case int64:
		return value, nil
	case bool:
		if value {
			return int64(1), nil
		}
		return int64(0), nil
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, &ValueError{Message: "cannot convert float to integer"}
		}
		return int64(math.Trunc(value)), nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return nil, &ValueError{Message: fmt.Sprintf("invalid literal for int() with base 10: '%s'", value)}
		}
		return parsed, nil
	}
	return nil, &TypeError{Message: fmt.Sprintf("int() argument must be a string or a number, not '%s'", pyTypeName(arguments[0]))}
}

func builtinFloat(arguments []any) (any, error) {
	if len(arguments) == 0 {
		return float64(0), nil
	}
	if len(arguments) != 1 {
		return nil, arityError("float", len(arguments), 0, 1)
	}
	switch value := arguments[0].(type) {
	case int64:
		return float64(value), nil
	case bool:
		if value {
			return float64(1), nil
		}
		return float64(0), nil
	case float64:
		return value, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return nil, &ValueError{Message: fmt.Sprintf("could not convert string to float: '%s'", value)}
		}
		return parsed, nil
	}
	return nil, &TypeError{Message: fmt.Sprintf("float() argument must be a string or a number, not '%s'", pyTypeName(arguments[0]))}
}

func builtinStr(arguments []any) (any, error) {
	if len(arguments) == 0 {
		return "", nil
	}
	if len(arguments) != 1 {
		return nil, arityError("str", len(arguments), 0, 1)
	}
	return pyStr(arguments[0]), nil
}

func builtinBool(arguments []any) (any, error) {
	if len(arguments) == 0 {
		return false, nil
	}
	if len(arguments) != 1 {
		return nil, arityError("bool", len(arguments), 0, 1)
	}
	return pyTruthy(arguments[0]), nil
}

func builtinSequence(arguments []any, name string) (any, error) {
	if len(arguments) == 0 {
		switch name {
		case "list":
			return []any{}, nil
		case "tuple":
			return Tuple{}, nil
		default:
			return &Set{}, nil
		}
	}
	if len(arguments) != 1 {
		return nil, arityError(name, len(arguments), 0, 1)
	}
	items, err := iterableValues(arguments[0])
	if err != nil {
		return nil, err
	}
	switch name {
	case "list":
		out := make([]any, len(items))
		copy(out, items)
		return out, nil
	case "tuple":
		out := make(Tuple, len(items))
		copy(out, items)
		return out, nil
	default:
		set := &Set{}
		for _, item := range items {
			set.Add(item)
		}
		return set, nil
	}
}

func builtinDict(arguments []any, keywords []keywordValue) (any, error) {
	if len(arguments) > 1 {
		return nil, arityError("dict", len(arguments), 0, 1)
	}
	result := NewDict()
	if len(arguments) == 1 {
		if source, ok := arguments[0].(*Dict); ok {
			for i := range source.keys {
				result.Set(source.keys[i], source.values[i])
			}
		} else {
			items, err := iterableValues(arguments[0])
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				pair, ok := item.(Tuple)
				if !ok || len(pair) != 2 {
					return nil, &TypeError{Message: "cannot convert dictionary update sequence element to a key/value pair"}
				}
				result.Set(pair[0], pair[1])
			}
		}
	}
	for _, keyword := range keywords {
		result.Set(keyword.name, keyword.value)
	}
	return result, nil
}

func builtinMinMax(name string, arguments []any) (any, error) {
	if len(arguments) == 0 {
		return nil, arityError(name, 0, 1, 1)
	}
	items := arguments
	if len(arguments) == 1 {
		var err error
		items, err = iterableValues(arguments[0])
		if err != nil {
			return nil, err
		}
	}
	if len(items) == 0 {
		return nil, &ValueError{Message: name + "() arg is an empty sequence"}
	}
	best := items[0]
	for _, item := range items[1:] {
		comparison, err := orderedCompare(item, best)
		if err != nil {
			return nil, err
		}
		if (name == "min" && comparison < 0) || (name == "max" && comparison > 0) {
			best = item
		}
	}
	return best, nil
}

func builtinSum(arguments []any) (any, error) {
	if len(arguments) < 1 || len(arguments) > 2 {
		return nil, arityError("sum", len(arguments), 1, 2)
	}
	items, err := iterableValues(arguments[0])
	if err != nil {
		return nil, err
	}
	var total any = int64(0)
	if len(arguments) == 2 {
		total = arguments[1]
	}
	for _, item := range items {
		total, err = addValues(total, item)
		if err != nil {
			return nil, err
		}
	}
	return total, nil
}

func builtinAbs(arguments []any) (any, error) {
	if len(arguments) != 1 {
		return nil, arityError("abs", len(arguments), 1, 1)
	}
	number, ok := toPyNumber(arguments[0])
	if !ok {
		return nil, &TypeError{Message: fmt.Sprintf("bad operand type for abs(): '%s'", pyTypeName(arguments[0]))}
	}
	if number.isInt {
		if number.i < 0 {
			return -number.i, nil
		}
		return number.i, nil
	}
	return math.Abs(number.f), nil
}

func builtinRound(arguments []any) (any, error) {
	if len(arguments) < 1 || len(arguments) > 2 {
		return nil, arityError("round", len(arguments), 1, 2)
	}
	number, ok := toPyNumber(arguments[0])
	if !ok {
		return nil, &TypeError{Message: fmt.Sprintf("type %s doesn't define __round__ method", pyTypeName(arguments[0]))}
	}
	if len(arguments) == 1 {
		if number.isInt {
			return number.i, nil
		}
		return int64(math.RoundToEven(number.f)), nil
	}
	digits, ok := integerValue(arguments[1])
	if !ok {
		return nil, &TypeError{Message: "integer argument expected for round() ndigits"}
	}
	if number.isInt {
		if digits >= 0 {
			return number.i, nil
		}
		return roundIntDigits(number.i, digits), nil
	}
	factor := math.Pow(10, float64(digits))
	return math.RoundToEven(number.f*factor) / factor, nil
}

func roundIntDigits(value, digits int64) int64 {
	shift := -digits
	if shift > 18 {
		return 0
	}
	factor := int64(1)
	for i := int64(0); i < shift; i++ {
		factor *= 10
	}
	quotient := value / factor
	remainder := value % factor
	absoluteRemainder := remainder
	if absoluteRemainder < 0 {
		absoluteRemainder = -absoluteRemainder
	}
	doubleRemainder := 2 * absoluteRemainder
	if doubleRemainder > factor || (doubleRemainder == factor && quotient%2 != 0) {
		if value >= 0 {
			quotient++
		} else {
			quotient--
		}
	}
	return quotient * factor
}

func builtinAllAny(name string, arguments []any) (any, error) {
	if len(arguments) != 1 {
		return nil, arityError(name, len(arguments), 1, 1)
	}
	items, err := iterableValues(arguments[0])
	if err != nil {
		return nil, err
	}
	if name == "all" {
		for _, item := range items {
			if !pyTruthy(item) {
				return false, nil
			}
		}
		return true, nil
	}
	for _, item := range items {
		if pyTruthy(item) {
			return true, nil
		}
	}
	return false, nil
}

func iterableValues(value any) ([]any, error) {
	switch v := value.(type) {
	case []any:
		return v, nil
	case Tuple:
		return []any(v), nil
	case *Set:
		return v.Items(), nil
	case *Dict:
		return v.Keys(), nil
	case string:
		runes := []rune(v)
		out := make([]any, len(runes))
		for i, r := range runes {
			out[i] = string(r)
		}
		return out, nil
	}
	return nil, &TypeError{Message: fmt.Sprintf("'%s' object is not iterable", pyTypeName(value))}
}

func callBoundMethod(method boundMethod, arguments []any, keywords []keywordValue) (any, error) {
	if len(keywords) > 0 {
		return nil, &TypeError{Message: method.name + "() takes no keyword arguments"}
	}
	switch receiver := method.receiver.(type) {
	case *Dict:
		switch method.name {
		case "get":
			if len(arguments) < 1 || len(arguments) > 2 {
				return nil, arityError("get", len(arguments), 1, 2)
			}
			value, ok := receiver.Get(arguments[0])
			if ok {
				return value, nil
			}
			if len(arguments) == 2 {
				return arguments[1], nil
			}
			return nil, nil
		case "keys":
			if len(arguments) != 0 {
				return nil, arityError("keys", len(arguments), 0, 0)
			}
			return receiver.Keys(), nil
		case "values":
			if len(arguments) != 0 {
				return nil, arityError("values", len(arguments), 0, 0)
			}
			return receiver.Values(), nil
		case "items":
			if len(arguments) != 0 {
				return nil, arityError("items", len(arguments), 0, 0)
			}
			items := receiver.Items()
			out := make([]any, len(items))
			for i, item := range items {
				out[i] = item
			}
			return out, nil
		}
	case string:
		switch method.name {
		case "lower":
			if len(arguments) != 0 {
				return nil, arityError("lower", len(arguments), 0, 0)
			}
			return strings.ToLower(receiver), nil
		case "upper":
			if len(arguments) != 0 {
				return nil, arityError("upper", len(arguments), 0, 0)
			}
			return strings.ToUpper(receiver), nil
		case "strip":
			if len(arguments) > 1 {
				return nil, arityError("strip", len(arguments), 0, 1)
			}
			if len(arguments) == 1 {
				cutset, ok := arguments[0].(string)
				if !ok {
					return nil, &TypeError{Message: "strip arg must be None or str"}
				}
				return strings.Trim(receiver, cutset), nil
			}
			return strings.TrimSpace(receiver), nil
		case "split":
			if len(arguments) > 2 {
				return nil, arityError("split", len(arguments), 0, 2)
			}
			return splitString(receiver, arguments)
		}
	}
	return nil, &ValueError{Message: "Call to function/method is not allowed"}
}

func splitString(value string, arguments []any) (any, error) {
	maxSplit := -1
	if len(arguments) == 2 {
		limit, ok := integerValue(arguments[1])
		if !ok {
			return nil, &TypeError{Message: "split() limit must be an integer"}
		}
		maxSplit = int(limit)
	}
	var parts []string
	if len(arguments) == 0 || arguments[0] == nil {
		parts = strings.Fields(value)
	} else {
		separator, ok := arguments[0].(string)
		if !ok {
			return nil, &TypeError{Message: "split separator must be a string"}
		}
		if separator == "" {
			return nil, &ValueError{Message: "empty separator"}
		}
		if maxSplit < 0 {
			parts = strings.Split(value, separator)
		} else {
			parts = strings.SplitN(value, separator, maxSplit+1)
		}
	}
	out := make([]any, len(parts))
	for i, part := range parts {
		out[i] = part
	}
	return out, nil
}

type evalConfig struct {
	timeoutMS  int
	hasTimeout bool
}

type EvalOption func(*evalConfig)

func Timeout(milliseconds int) EvalOption {
	return func(config *evalConfig) {
		config.timeoutMS = milliseconds
		config.hasTimeout = true
	}
}

func NoTimeout() EvalOption {
	return func(config *evalConfig) {
		config.hasTimeout = false
	}
}

func SafeEval(expression string, context map[string]any, options ...EvalOption) (any, error) {
	config := evalConfig{timeoutMS: DefaultTimeoutMS, hasTimeout: true}
	for _, option := range options {
		option(&config)
	}

	var deadline time.Time
	if config.hasTimeout {
		if config.timeoutMS <= 0 {
			return nil, &ValueError{Message: "timeout_ms must be greater than 0"}
		}
		deadline = safeEvalClock().Add(time.Duration(config.timeoutMS) * time.Millisecond)
	}

	tokens, err := lexExpression(expression)
	if err != nil {
		return nil, err
	}
	tree, err := parseExpressionTokens(tokens)
	if err != nil {
		return nil, err
	}

	normalized := make(map[string]any, len(context))
	for key, value := range context {
		normalized[key] = normalizeValue(value)
	}

	eval := &evaluator{
		context:     normalized,
		deadline:    deadline,
		hasDeadline: config.hasTimeout,
		timeoutMS:   config.timeoutMS,
	}
	if err := eval.checkTimeout(); err != nil {
		return nil, err
	}
	return eval.evaluate(tree)
}
