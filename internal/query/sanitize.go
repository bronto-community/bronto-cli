package query

import "strings"

// Sanitize returns input with every literal replaced by "?", keeping field
// names, operators, keywords and parentheses, so the shape of a query can be
// recorded without the values it searches for:
//
//	status >= 500 AND user.email = 'a@b.c'  →  status >= ? AND user.email = ?
//
// It works on tokens, not the parse tree, so queries outside the grammar
// Parse accepts still sanitize. A bare term that isn't the field of a
// comparison (a free-text search word) counts as a literal. ok is false
// when input doesn't tokenize; callers must then drop it entirely rather
// than record it.
func Sanitize(input string) (string, bool) {
	lx := &lexer{in: input}
	var toks []token
	for {
		t, err := lx.next()
		if err != nil {
			return "", false
		}
		if t.kind == "eof" {
			break
		}
		toks = append(toks, t)
	}
	var b strings.Builder
	for i, t := range toks {
		text := t.text
		switch t.kind {
		case "str", "num":
			text = "?"
		case "ident":
			if i+1 == len(toks) || toks[i+1].kind != "op" {
				text = "?"
			}
		case "and", "or", "not":
			text = strings.ToUpper(text)
		}
		if i > 0 && t.kind != "rparen" && toks[i-1].kind != "lparen" {
			b.WriteByte(' ')
		}
		b.WriteString(text)
	}
	return b.String(), true
}
