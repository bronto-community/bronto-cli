// Package query parses the Bronto search query subset client-side: enough
// grammar to validate structure, position errors with a caret, and
// extract referenced fields — the foundation for `bronto query check`,
// monitor linting, and (eventually) local evaluation.
//
// Grammar (case-insensitive keywords):
//
//	expr    := or
//	or      := and ("OR" and)*
//	and     := not ("AND" not)*
//	not     := "NOT" not | primary
//	primary := "(" expr ")" | comparison
//	comparison := field op value
//	op      := = | != | >= | <= | > | < | ~ | !~
//	field   := [@$]?ident(.ident)*
//	value   := number | 'string' | "string" | bareword
//
// The live language may accept more than this; callers that cannot
// tolerate false negatives should treat parse failures as advisory (see
// search's error enrichment) — only `query check` treats them as fatal.
package query

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

type Node interface{ fields(map[string]bool) }

type Binary struct {
	Op          string // AND / OR
	Left, Right Node
}

type Not struct{ X Node }

type Compare struct {
	Field string
	Op    string
	Value Literal
}

type Literal struct {
	Raw      string
	IsString bool
	Num      float64
	IsNum    bool
}

func (b Binary) fields(m map[string]bool)  { b.Left.fields(m); b.Right.fields(m) }
func (n Not) fields(m map[string]bool)     { n.X.fields(m) }
func (c Compare) fields(m map[string]bool) { m[c.Field] = true }

// Fields returns the sorted set of field names referenced by the query.
func Fields(n Node) []string {
	m := map[string]bool{}
	n.fields(m)
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// ParseError carries a byte position and span into the original input so
// callers can render a caret.
type ParseError struct {
	Pos, Len int
	Msg      string
}

func (e *ParseError) Error() string { return e.Msg }

// Caret renders the input with a ^~~~ marker under the error span.
func (e *ParseError) Caret(input string) string {
	span := e.Len
	if span < 1 {
		span = 1
	}
	marker := strings.Repeat(" ", e.Pos) + "^" + strings.Repeat("~", span-1)
	return input + "\n" + marker
}

type token struct {
	kind string // ident, num, str, op, lparen, rparen, and, or, not, eof
	text string
	pos  int
}

type lexer struct {
	in  string
	pos int
}

func isFieldRune(r rune, first bool) bool {
	if first && (r == '@' || r == '$') {
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-'
}

func (l *lexer) next() (token, *ParseError) {
	for l.pos < len(l.in) && (l.in[l.pos] == ' ' || l.in[l.pos] == '\t' || l.in[l.pos] == '\n') {
		l.pos++
	}
	if l.pos >= len(l.in) {
		return token{kind: "eof", pos: l.pos}, nil
	}
	start := l.pos
	c := l.in[l.pos]
	switch {
	case c == '(':
		l.pos++
		return token{kind: "lparen", text: "(", pos: start}, nil
	case c == ')':
		l.pos++
		return token{kind: "rparen", text: ")", pos: start}, nil
	case c == '\'' || c == '"':
		quote := c
		l.pos++
		for l.pos < len(l.in) {
			if l.in[l.pos] == '\\' && l.pos+1 < len(l.in) {
				l.pos += 2
				continue
			}
			if l.in[l.pos] == quote {
				l.pos++
				return token{kind: "str", text: l.in[start:l.pos], pos: start}, nil
			}
			l.pos++
		}
		return token{}, &ParseError{Pos: start, Len: l.pos - start, Msg: "unterminated string"}
	case strings.ContainsRune("=!<>~", rune(c)):
		ops := []string{"!=", ">=", "<=", "!~", "=", ">", "<", "~"}
		for _, op := range ops {
			if strings.HasPrefix(l.in[l.pos:], op) {
				l.pos += len(op)
				return token{kind: "op", text: op, pos: start}, nil
			}
		}
		l.pos++
		return token{}, &ParseError{Pos: start, Len: 1, Msg: fmt.Sprintf("unexpected character %q", c)}
	default:
		for l.pos < len(l.in) && isFieldRune(rune(l.in[l.pos]), l.pos == start) {
			l.pos++
		}
		if l.pos == start {
			return token{}, &ParseError{Pos: start, Len: 1, Msg: fmt.Sprintf("unexpected character %q", c)}
		}
		text := l.in[start:l.pos]
		switch strings.ToUpper(text) {
		case "AND":
			return token{kind: "and", text: text, pos: start}, nil
		case "OR":
			return token{kind: "or", text: text, pos: start}, nil
		case "NOT":
			return token{kind: "not", text: text, pos: start}, nil
		}
		if _, err := strconv.ParseFloat(text, 64); err == nil {
			return token{kind: "num", text: text, pos: start}, nil
		}
		return token{kind: "ident", text: text, pos: start}, nil
	}
}

type parser struct {
	toks []token
	i    int
	in   string
}

// Parse parses input into an AST or a *ParseError.
func Parse(input string) (Node, error) {
	if strings.TrimSpace(input) == "" {
		return nil, &ParseError{Pos: 0, Len: 1, Msg: "empty query"}
	}
	lx := &lexer{in: input}
	var toks []token
	for {
		t, err := lx.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.kind == "eof" {
			break
		}
	}
	p := &parser{toks: toks, in: input}
	n, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.cur().kind != "eof" {
		t := p.cur()
		return nil, &ParseError{Pos: t.pos, Len: len(t.text),
			Msg: fmt.Sprintf("unexpected %q — expected AND, OR, or end of query", t.text)}
	}
	return n, nil
}

func (p *parser) cur() token { return p.toks[p.i] }
func (p *parser) advance()   { p.i++ }

func (p *parser) parseOr() (Node, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == "or" {
		p.advance()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = Binary{Op: "OR", Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Node, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == "and" {
		p.advance()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = Binary{Op: "AND", Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Node, error) {
	if p.cur().kind == "not" {
		p.advance()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return Not{X: x}, nil
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Node, error) {
	t := p.cur()
	switch t.kind {
	case "lparen":
		p.advance()
		n, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.cur().kind != "rparen" {
			c := p.cur()
			return nil, &ParseError{Pos: c.pos, Len: 1, Msg: "missing closing parenthesis"}
		}
		p.advance()
		return n, nil
	case "ident":
		field := t.text
		p.advance()
		op := p.cur()
		if op.kind != "op" {
			return nil, &ParseError{Pos: op.pos, Len: len(op.text),
				Msg: fmt.Sprintf("expected a comparison operator after field %q", field)}
		}
		p.advance()
		val := p.cur()
		switch val.kind {
		case "num":
			p.advance()
			f, _ := strconv.ParseFloat(val.text, 64)
			return Compare{Field: field, Op: op.text, Value: Literal{Raw: val.text, Num: f, IsNum: true}}, nil
		case "str":
			p.advance()
			return Compare{Field: field, Op: op.text, Value: Literal{Raw: val.text, IsString: true}}, nil
		case "ident":
			p.advance()
			return Compare{Field: field, Op: op.text, Value: Literal{Raw: val.text}}, nil
		default:
			return nil, &ParseError{Pos: val.pos, Len: 1,
				Msg: fmt.Sprintf("expected a value after %q", op.text)}
		}
	case "eof":
		return nil, &ParseError{Pos: len(p.in), Len: 1, Msg: "incomplete expression — expected a condition"}
	default:
		return nil, &ParseError{Pos: t.pos, Len: len(t.text),
			Msg: fmt.Sprintf("unexpected %q — expected a field name or '('", t.text)}
	}
}

// Suggest returns the closest known field within Damerau-Levenshtein
// distance 2, or "".
func Suggest(field string, known []string) string {
	best, bestDist := "", 3
	for _, k := range known {
		if d := editDistance(strings.ToLower(field), strings.ToLower(k)); d < bestDist {
			best, bestDist = k, d
		}
	}
	return best
}

func editDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	prev := make([]int, lb+1)
	cur := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		cur[0] = i
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				if t := prev[j-1]; t+1 < cur[j] { // transposition approximation
					cur[j] = t + 1
				}
			}
		}
		prev, cur = cur, prev
	}
	return prev[lb]
}

func min3(a, b, c int) int {
	if a < b {
		b = a
	}
	if b < c {
		return b
	}
	return c
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
