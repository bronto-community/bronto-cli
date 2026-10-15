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
// comparison (a free-text search word) counts as a literal, and so does a
// double-quoted attribute name (dropping a name is safe). ok is false
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
	// keyword reports whether toks[i] is a word operator in predicate
	// position (IN, LIKE, IS, NULL after IS, LOWER before '('): kept and
	// upper-cased, like AND/OR/NOT.
	keyword := func(i int) bool {
		switch kw(toks[i]) {
		case "IN", "LIKE", "ILIKE", "REGEXP", "RLIKE", "IS":
			return i > 0 && (toks[i-1].kind == "ident" || toks[i-1].kind == "rparen" ||
				toks[i-1].kind == "not" || toks[i-1].kind == "str")
		case "NULL":
			return i > 0 && (kw(toks[i-1]) == "IS" || toks[i-1].kind == "not")
		case "LOWER", "EXISTS":
			return i+1 < len(toks) && (toks[i+1].kind == "lparen" ||
				kw(toks[i]) == "EXISTS" && toks[i+1].kind == "ident")
		}
		return false
	}
	// field reports whether the ident at i is a predicate's operand.
	field := func(i int) bool {
		if i >= 2 && toks[i-1].kind == "lparen" && (kw(toks[i-2]) == "LOWER" || kw(toks[i-2]) == "EXISTS") {
			return true
		}
		if i >= 1 && kw(toks[i-1]) == "EXISTS" {
			return true
		}
		if i+1 == len(toks) {
			return false
		}
		switch n := toks[i+1]; n.kind {
		case "op":
			return true
		case "not":
			return i+2 < len(toks) && binaryKeywords[kw(toks[i+2])]
		default:
			return binaryKeywords[kw(n)] || kw(n) == "IS"
		}
	}
	var b strings.Builder
	for i, t := range toks {
		text := t.text
		switch t.kind {
		case "str", "num":
			text = "?"
		case "ident":
			switch {
			case keyword(i):
				text = strings.ToUpper(text)
			case !field(i):
				text = "?"
			}
		case "and", "or", "not":
			text = strings.ToUpper(text)
		}
		if i > 0 && t.kind != "rparen" && t.kind != "comma" && toks[i-1].kind != "lparen" &&
			(t.kind != "lparen" || kw(toks[i-1]) != "LOWER") {
			b.WriteByte(' ')
		}
		b.WriteString(text)
	}
	return b.String(), true
}
