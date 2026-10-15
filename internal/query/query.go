// Package query parses the Bronto search query language client-side:
// enough grammar to validate structure, position errors with a caret,
// extract referenced fields, and evaluate queries locally — the
// foundation for `bronto query check`, monitor linting, and
// `bronto search --local`.
//
// Grammar (keywords are case-insensitive), matching what the live search
// API accepts (https://docs.bronto.io/query-syntax/search-syntax):
//
//	expr      := or
//	or        := and ("OR" and)*
//	and       := not (("AND" | "&&") not)*
//	not       := "NOT" not | primary
//	primary   := "(" expr ")" | predicate | freetext
//	predicate := operand op value
//	           | operand ["NOT"] "IN" "(" value ("," value)* ")"
//	           | operand ["NOT"] ("LIKE" | "ILIKE" | "REGEXP" | "RLIKE") value
//	           | operand "IS" ["NOT"] "NULL"
//	           | "EXISTS" field | "EXISTS" "(" field ")"
//	operand   := field | "LOWER" "(" field ")" | number | 'string' | "(" operand ")"
//	op        := = | != | <> | >= | <= | > | < | ~ | !~ | ~* | !~*
//	field     := [$]?ident(.ident)* | @raw | "double-quoted name"
//	value     := number | 'string' | field
//	freetext  := bareword | number | 'string' | "string"
//
// A literal operand is a constant, not a field: 'status' = 200 compares
// the text "status" with 200 and matches nothing, while 'a' = 'a' matches
// everything. Of the @-prefixed barewords only @raw names a field (the
// server answers 400 for @status, @time, …, though EXISTS @x and a lone
// @x free-text term are accepted); a double-quoted "@name" is an ordinary
// attribute.
//
// Single quotes delimit strings and double quotes delimit attribute
// names; either quote is escaped by doubling it (backslash is literal).
// So a bareword or double-quoted VALUE is an attribute reference, not a
// string: level = error compares two attributes. A free-text term
// matches events whose raw text contains it, case-insensitively — the
// server expands it to @raw ILIKE '%term%'.
//
// Callers that cannot tolerate false negatives should treat parse
// failures as advisory (see search's error enrichment) — only
// `query check` treats them as fatal.
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

// Compare is one predicate. Op is canonical upper case: = != > >= < <=
// ~ !~ ~* !~* LIKE ILIKE REGEXP IN IS NULL EXISTS, each binary keyword
// form optionally prefixed "NOT " ("NOT IN", "IS NOT NULL"). <> becomes
// != and RLIKE becomes REGEXP.
type Compare struct {
	Field  string
	Left   *Literal // a constant operand ('x' = 1, 200 = status) instead of Field
	Lower  bool     // the field was wrapped in LOWER(...)
	Op     string
	Value  Literal   // unused by IN and IS [NOT] NULL
	Values []Literal // IN / NOT IN list
}

// FreeText is a bare term or quoted phrase searched for in the raw event.
type FreeText struct{ Term Literal }

type Literal struct {
	Raw      string // as written, quotes included
	IsString bool   // 'single-quoted'
	IsAttr   bool   // bareword or "double-quoted" value: an attribute reference
	Num      float64
	IsNum    bool
}

// Text is the literal's content: quotes stripped and doubled quotes
// unescaped.
func (l Literal) Text() string {
	return unquote(l.Raw)
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		q := s[:1]
		return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	}
	return s
}

func (b Binary) fields(m map[string]bool) { b.Left.fields(m); b.Right.fields(m) }
func (n Not) fields(m map[string]bool)    { n.X.fields(m) }
func (FreeText) fields(map[string]bool)   {}
func (c Compare) fields(m map[string]bool) {
	if c.Left == nil {
		m[c.Field] = true
	}
	for _, v := range append([]Literal{c.Value}, c.Values...) {
		if v.IsAttr {
			m[v.Text()] = true
		}
	}
}

// Fields returns the sorted set of field names referenced by the query,
// including attributes referenced on the value side (level = other).
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

// Warnings lists constructs that are valid but probably not what the
// author meant. The server silently returns nothing for these, so
// `query check` surfaces them.
func Warnings(n Node) []string {
	var out []string
	var walk func(Node)
	walk = func(n Node) {
		switch t := n.(type) {
		case Binary:
			walk(t.Left)
			walk(t.Right)
		case Not:
			walk(t.X)
		case Compare:
			lhs := t.Field
			if t.Left != nil {
				lhs = t.Left.Raw
				if t.Left.IsString {
					out = append(out, fmt.Sprintf(
						"%s in `%s %s` is a string, not the attribute %q — the comparison is constant; write %s to compare the attribute",
						t.Left.Raw, t.Left.Raw, t.Op, t.Left.Text(), quoteField(t.Left.Text())))
				}
			}
			for _, v := range append([]Literal{t.Value}, t.Values...) {
				if v.IsAttr {
					out = append(out, fmt.Sprintf(
						"%s in `%s %s` names the attribute %q, not a string — write '%s' to compare against the text",
						v.Raw, lhs, t.Op, v.Text(), strings.ReplaceAll(v.Text(), "'", "''")))
				}
			}
		}
	}
	walk(n)
	return out
}

// quoteField writes name as a double-quoted attribute reference.
func quoteField(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
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
	kind string // ident, num, str, op, lparen, rparen, comma, and, or, not, eof
	text string
	pos  int
}

type lexer struct {
	in  string
	pos int
}

// isFieldRune reports whether r continues a bareword. ':' may appear
// inside one (key:value and 15:22:38 are free-text terms) but not lead.
func isFieldRune(r rune, first bool) bool {
	if first && (r == '@' || r == '$') {
		return true
	}
	if !first && r == ':' {
		return true
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' || r == '-'
}

// operators, longest first so the lexer matches greedily.
var operators = []string{"!~*", "!=", ">=", "<=", "<>", "!~", "~*", "=", ">", "<", "~"}

func (l *lexer) next() (token, *ParseError) {
	for l.pos < len(l.in) && (l.in[l.pos] == ' ' || l.in[l.pos] == '\t' || l.in[l.pos] == '\n' || l.in[l.pos] == '\r') {
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
	case c == ',':
		l.pos++
		return token{kind: "comma", text: ",", pos: start}, nil
	case strings.HasPrefix(l.in[l.pos:], "&&"):
		l.pos += 2
		return token{kind: "and", text: "&&", pos: start}, nil
	case c == '\'' || c == '"':
		quote := c
		l.pos++
		for l.pos < len(l.in) {
			if l.in[l.pos] == quote {
				// A doubled quote is a SQL-style escaped quote inside the
				// string (traces.Quote emits exactly this: 'O''Brien'),
				// not a close-then-reopen. Backslash is not an escape:
				// the server rejects 'a\'b'.
				if l.pos+1 < len(l.in) && l.in[l.pos+1] == quote {
					l.pos += 2
					continue
				}
				l.pos++
				return token{kind: "str", text: l.in[start:l.pos], pos: start}, nil
			}
			l.pos++
		}
		return token{}, &ParseError{Pos: start, Len: l.pos - start, Msg: "unterminated string"}
	case strings.ContainsRune("=!<>~", rune(c)):
		for _, op := range operators {
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
		// IN, LIKE, IS, NULL, … stay idents: they are keywords only in
		// predicate position (a lone `in` is a valid free-text search).
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
func (p *parser) peek(k int) token {
	if p.i+k < len(p.toks) {
		return p.toks[p.i+k]
	}
	return p.toks[len(p.toks)-1] // eof
}
func (p *parser) advance() { p.i++ }

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

// binaryKeywords are the word operators that may follow NOT.
var binaryKeywords = map[string]bool{"IN": true, "LIKE": true, "ILIKE": true, "REGEXP": true, "RLIKE": true}

func kw(t token) string {
	if t.kind != "ident" {
		return ""
	}
	return strings.ToUpper(t.text)
}

// startsPredicate reports whether the token at offset k begins the
// operator part of a predicate.
func (p *parser) startsPredicate(k int) bool {
	t := p.peek(k)
	switch {
	case t.kind == "op":
		return true
	case binaryKeywords[kw(t)] || kw(t) == "IS":
		return true
	case t.kind == "not":
		return binaryKeywords[kw(p.peek(k+1))]
	}
	return false
}

func (p *parser) parsePrimary() (Node, error) {
	t := p.cur()
	if t.kind == "lparen" || t.kind == "num" || (t.kind == "str" && t.text[0] == '\'') {
		// A parenthesized or literal operand: (status) = 500, 200 = status,
		// 'a' = 'a'. Otherwise the same tokens open a group or a free-text
		// term, handled below.
		if n, ok, err := p.tryLiteralPredicate(); ok || err != nil {
			return n, err
		}
	}
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
		if kw(t) == "LOWER" && p.peek(1).kind == "lparen" {
			p.advance()
			p.advance()
			f := p.cur()
			if f.kind != "ident" && (f.kind != "str" || f.text[0] != '"') {
				return nil, &ParseError{Pos: f.pos, Len: max(len(f.text), 1), Msg: "expected a field name inside LOWER(...)"}
			}
			if err := checkSystemField(f); err != nil {
				return nil, err
			}
			p.advance()
			if p.cur().kind != "rparen" {
				c := p.cur()
				return nil, &ParseError{Pos: c.pos, Len: 1, Msg: "missing closing parenthesis after LOWER(" + f.text}
			}
			p.advance()
			if !p.startsPredicate(0) {
				c := p.cur()
				return nil, &ParseError{Pos: c.pos, Len: max(len(c.text), 1),
					Msg: "expected a comparison operator after LOWER(" + f.text + ")"}
			}
			return p.parsePredicate(unquote(f.text), true)
		}
		if kw(t) == "EXISTS" {
			if f, n, ok := p.existsOperand(); ok {
				p.i += n
				return Compare{Field: f, Op: "EXISTS"}, nil
			}
		}
		if p.startsPredicate(1) {
			if err := checkSystemField(t); err != nil {
				return nil, err
			}
			p.advance()
			return p.parsePredicate(t.text, false)
		}
		if t.text == "@" { // the server rejects a lone @ even as a search term
			return nil, checkSystemField(t)
		}
		if kw(t) == "NULL" {
			return nil, &ParseError{Pos: t.pos, Len: len(t.text),
				Msg: "NULL is only valid in field IS NULL / field IS NOT NULL"}
		}
		return p.freeText(Literal{Raw: t.text})
	case "str":
		if t.text[0] == '"' && p.startsPredicate(1) {
			p.advance()
			return p.parsePredicate(unquote(t.text), false)
		}
		return p.freeText(Literal{Raw: t.text, IsString: true})
	case "num":
		f, _ := strconv.ParseFloat(t.text, 64)
		return p.freeText(Literal{Raw: t.text, Num: f, IsNum: true})
	case "eof":
		return nil, &ParseError{Pos: len(p.in), Len: 1, Msg: "incomplete expression — expected a condition"}
	default:
		return nil, &ParseError{Pos: t.pos, Len: len(t.text),
			Msg: fmt.Sprintf("unexpected %q — expected a field name, a search term, or '('", t.text)}
	}
}

// tryLiteralPredicate parses a predicate whose operand is a literal or
// is parenthesized, reporting ok=false — with the cursor restored — when
// the tokens are not one, so the caller can read them as a group or a
// free-text term instead.
func (p *parser) tryLiteralPredicate() (n Node, ok bool, err error) {
	start := p.i
	field, lower, left, isOperand := p.operand(0)
	if !isOperand || !p.startsPredicate(0) {
		p.i = start
		return nil, false, nil
	}
	if left != nil {
		n, err := p.parsePredicate("", false)
		if c, isCompare := n.(Compare); isCompare {
			c.Left = left
			n = c
		}
		return n, true, err
	}
	n, err = p.parsePredicate(field, lower)
	return n, true, err
}

// operand reads one predicate operand at the cursor without reporting
// errors: a field (checked against the system-field rule), LOWER(field),
// a number or 'string' (returned as left), or any of these wrapped in
// parentheses. ok is false when the tokens are not an operand.
func (p *parser) operand(depth int) (field string, lower bool, left *Literal, ok bool) {
	t := p.cur()
	switch {
	case t.kind == "lparen" && depth < 256:
		p.advance()
		field, lower, left, ok = p.operand(depth + 1)
		if !ok || p.cur().kind != "rparen" {
			return "", false, nil, false
		}
		p.advance()
		return field, lower, left, true
	case t.kind == "num":
		p.advance()
		f, _ := strconv.ParseFloat(t.text, 64)
		return "", false, &Literal{Raw: t.text, Num: f, IsNum: true}, true
	case t.kind == "str" && t.text[0] == '\'':
		p.advance()
		return "", false, &Literal{Raw: t.text, IsString: true}, true
	case t.kind == "str":
		p.advance()
		return unquote(t.text), false, nil, true
	case t.kind == "ident" && kw(t) == "LOWER" && p.peek(1).kind == "lparen":
		f := p.peek(2)
		if (f.kind != "ident" && (f.kind != "str" || f.text[0] != '"')) || p.peek(3).kind != "rparen" ||
			checkSystemField(f) != nil {
			return "", false, nil, false
		}
		p.i += 4
		return unquote(f.text), true, nil, true
	case t.kind == "ident" && kw(t) != "NULL" && kw(t) != "EXISTS" && checkSystemField(t) == nil:
		p.advance()
		return t.text, false, nil, true
	}
	return "", false, nil, false
}

// checkSystemField rejects an @-prefixed bareword other than @raw: the
// server answers 400 for @status, @time, @sequence, …, even though those
// appear as keys in search results.
func checkSystemField(t token) *ParseError {
	if t.kind != "ident" || !strings.HasPrefix(t.text, "@") || t.text == "@raw" {
		return nil
	}
	msg := fmt.Sprintf("%s is not a queryable field — @raw is the only @-field a query can reference", t.text)
	switch t.text {
	case "@":
		msg = "expected a field name after @"
	case "@time", "@timestamp":
		msg += "; filter time with --since, --from, and --to"
	default:
		msg += fmt.Sprintf(`; quote the name (%s) for an attribute literally called that`, quoteField(t.text))
	}
	return &ParseError{Pos: t.pos, Len: len(t.text), Msg: msg}
}

// existsOperand reads the field after an EXISTS at the cursor — written
// EXISTS field or EXISTS (field) — returning it and the tokens consumed.
// ok is false when EXISTS stands alone, which makes it a search term.
func (p *parser) existsOperand() (field string, n int, ok bool) {
	isField := func(t token) bool { return t.kind == "ident" || (t.kind == "str" && t.text[0] == '"') }
	if f := p.peek(1); isField(f) {
		return unquote(f.text), 2, true
	}
	if p.peek(1).kind == "lparen" && isField(p.peek(2)) && p.peek(3).kind == "rparen" {
		return unquote(p.peek(2).text), 4, true
	}
	return "", 0, false
}

// freeText consumes a free-text term. The server takes one term per
// position, so a following bare term means an unquoted phrase.
func (p *parser) freeText(lit Literal) (Node, error) {
	p.advance()
	switch n := p.cur(); n.kind {
	case "ident", "str", "num":
		return nil, &ParseError{Pos: n.pos, Len: len(n.text),
			Msg: fmt.Sprintf("unexpected %q after the search term %q — quote a phrase as one term (\"a b\") or join terms with AND/OR",
				n.text, lit.Raw)}
	}
	return FreeText{Term: lit}, nil
}

// parsePredicate parses what follows a predicate's operand (the cursor
// sits on the operator).
func (p *parser) parsePredicate(field string, lower bool) (Node, error) {
	c := Compare{Field: field, Lower: lower}
	t := p.cur()
	if t.kind == "op" {
		p.advance()
		c.Op = t.text
		if c.Op == "<>" {
			c.Op = "!="
		}
		v, err := p.parseValue(t.text)
		if err != nil {
			return nil, err
		}
		c.Value = v
		return c, nil
	}
	neg := ""
	if t.kind == "not" {
		neg = "NOT "
		p.advance()
		t = p.cur()
	}
	switch k := kw(t); k {
	case "IS":
		p.advance()
		op := "IS NULL"
		if p.cur().kind == "not" {
			op = "IS NOT NULL"
			p.advance()
		}
		if n := p.cur(); kw(n) != "NULL" {
			return nil, &ParseError{Pos: n.pos, Len: max(len(n.text), 1), Msg: "expected NULL or NOT NULL after IS"}
		}
		p.advance()
		c.Op = op
		return c, nil
	case "IN":
		p.advance()
		c.Op = neg + "IN"
		if n := p.cur(); n.kind != "lparen" {
			return nil, &ParseError{Pos: n.pos, Len: max(len(n.text), 1), Msg: "expected '(' after IN"}
		}
		p.advance()
		for {
			v, err := p.parseValue(c.Op)
			if err != nil {
				return nil, err
			}
			c.Values = append(c.Values, v)
			if p.cur().kind != "comma" {
				break
			}
			p.advance()
		}
		if n := p.cur(); n.kind != "rparen" {
			return nil, &ParseError{Pos: n.pos, Len: max(len(n.text), 1),
				Msg: "expected ',' or ')' in the IN list"}
		}
		p.advance()
		return c, nil
	default: // LIKE ILIKE REGEXP RLIKE (startsPredicate guarantees one)
		p.advance()
		if k == "RLIKE" {
			k = "REGEXP"
		}
		c.Op = neg + k
		v, err := p.parseValue(t.text)
		if err != nil {
			return nil, err
		}
		c.Value = v
		return c, nil
	}
}

func (p *parser) parseValue(after string) (Literal, error) {
	val := p.cur()
	switch val.kind {
	case "num":
		p.advance()
		f, _ := strconv.ParseFloat(val.text, 64)
		return Literal{Raw: val.text, Num: f, IsNum: true}, nil
	case "str":
		p.advance()
		if val.text[0] == '"' {
			return Literal{Raw: val.text, IsAttr: true}, nil
		}
		return Literal{Raw: val.text, IsString: true}, nil
	case "ident":
		if kw(val) == "NULL" {
			return Literal{}, &ParseError{Pos: val.pos, Len: len(val.text),
				Msg: "compare with NULL using IS NULL or IS NOT NULL"}
		}
		if err := checkSystemField(val); err != nil {
			return Literal{}, err
		}
		p.advance()
		return Literal{Raw: val.text, IsAttr: true}, nil
	default:
		return Literal{}, &ParseError{Pos: val.pos, Len: 1,
			Msg: fmt.Sprintf("expected a value after %q", after)}
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
