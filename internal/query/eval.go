package query

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"

	"github.com/bronto-community/bronto-cli/internal/coerce"
)

// Matcher evaluates a parsed query against events client-side. Patterns
// written as literals (regexes, LIKE patterns, free-text terms) are
// compiled once at construction.
type Matcher struct {
	node     Node
	res      map[string]*regexp.Regexp
	fallback string
}

// SetFieldFallbackPrefix makes a field absent from the event resolve to
// prefix+field instead, e.g. status → message_kvs.status for events
// dumped by bronto search -o jsonl. An exact key always wins.
func (m *Matcher) SetFieldFallbackPrefix(prefix string) { m.fallback = prefix }

// NewMatcher compiles the pattern literals reachable from n. An invalid
// regex is reported here, not at match time.
func NewMatcher(n Node) (*Matcher, error) {
	m := &Matcher{node: n, res: map[string]*regexp.Regexp{}}
	if err := m.compile(n); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Matcher) compile(n Node) error {
	switch t := n.(type) {
	case Binary:
		if err := m.compile(t.Left); err != nil {
			return err
		}
		return m.compile(t.Right)
	case Not:
		return m.compile(t.X)
	case FreeText:
		key, pat := patternFor("ILIKE", "%"+t.Term.Text()+"%")
		re, err := regexp.Compile(pat) // fails only on invalid UTF-8
		if err != nil {
			return fmt.Errorf("invalid search term %q: %w", t.Term.Text(), err)
		}
		m.res[key] = re
	case Compare:
		if t.Value.IsAttr || !isPatternOp(t.Op) {
			return nil // attribute-valued patterns compile per event
		}
		key, pat := patternFor(t.Op, t.Value.Text())
		re, err := regexp.Compile(pat)
		if err != nil {
			return fmt.Errorf("invalid pattern %q: %w", t.Value.Text(), err)
		}
		m.res[key] = re
	}
	return nil
}

func isPatternOp(op string) bool {
	switch strings.TrimPrefix(op, "NOT ") {
	case "~", "!~", "~*", "!~*", "REGEXP", "LIKE", "ILIKE":
		return true
	}
	return false
}

// patternFor returns the cache key and Go regexp for a pattern operator.
// LIKE patterns are anchored and only % is a wildcard (the server treats
// _ literally); regexes are RE2 and match anywhere in the value.
func patternFor(op, text string) (key, pat string) {
	switch strings.TrimPrefix(op, "NOT ") {
	case "LIKE", "ILIKE":
		parts := strings.Split(text, "%")
		for i, s := range parts {
			parts[i] = regexp.QuoteMeta(s)
		}
		pat = "(?s)^" + strings.Join(parts, ".*") + "$"
		if strings.HasSuffix(op, "ILIKE") {
			pat = "(?i)" + pat
			return "ilike:" + text, pat
		}
		return "like:" + text, pat
	case "~*", "!~*":
		return "re*:" + text, "(?i)" + text
	}
	return "re:" + text, text
}

// tri is SQL's three-valued logic: a comparison involving a missing
// attribute is unknown, and NOT unknown stays unknown, so neither
// "missing != 'x'" nor "NOT missing = 'x'" matches — as on the server.
type tri uint8

const (
	triFalse tri = iota
	triTrue
	triUnknown
)

func triOf(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// Match reports whether the (flattened) event satisfies the query.
// Semantics follow the server: a missing field makes its comparison
// unknown (see tri); = != and ordered comparisons are numeric when both
// sides are numeric, else by string form, but a number never matches a
// non-numeric string; free text matches @raw case-insensitively.
func (m *Matcher) Match(ev map[string]any) bool {
	return m.eval(m.node, ev) == triTrue
}

func (m *Matcher) eval(n Node, ev map[string]any) tri {
	switch t := n.(type) {
	case Binary:
		// FALSE AND x and TRUE OR x are decided by the left side alone, so
		// the right side (often a regex) only runs when it matters.
		l := m.eval(t.Left, ev)
		if t.Op == "AND" {
			if l == triFalse {
				return triFalse
			}
			switch r := m.eval(t.Right, ev); {
			case r == triFalse:
				return triFalse
			case l == triTrue && r == triTrue:
				return triTrue
			}
			return triUnknown
		}
		if l == triTrue {
			return triTrue
		}
		switch r := m.eval(t.Right, ev); {
		case r == triTrue:
			return triTrue
		case l == triFalse && r == triFalse:
			return triFalse
		}
		return triUnknown
	case Not:
		return negate(m.eval(t.X, ev))
	case FreeText:
		raw, ok := ev["@raw"]
		if !ok || raw == nil {
			return triUnknown
		}
		key, _ := patternFor("ILIKE", "%"+t.Term.Text()+"%")
		return triOf(m.res[key].MatchString(valueString(raw)))
	case Compare:
		return m.compare(t, ev)
	}
	return triFalse
}

func negate(v tri) tri {
	switch v {
	case triTrue:
		return triFalse
	case triFalse:
		return triTrue
	}
	return triUnknown
}

// lookup returns field's value, falling back to the prefixed name (see
// SetFieldFallbackPrefix) when the event has no such key.
func (m *Matcher) lookup(ev map[string]any, field string) (any, bool) {
	v, ok := ev[field]
	if !ok && m.fallback != "" {
		v, ok = ev[m.fallback+field]
	}
	return v, ok
}

func (m *Matcher) compare(c Compare, ev map[string]any) tri {
	v, ok := m.lookup(ev, c.Field)
	present := ok && v != nil
	switch c.Op {
	case "IS NULL":
		return triOf(!present)
	case "IS NOT NULL", "EXISTS":
		return triOf(present)
	}
	if !present {
		return triUnknown
	}
	if c.Lower {
		v = strings.ToLower(valueString(v))
	}
	if c.Op == "IN" || c.Op == "NOT IN" {
		res := triFalse
		for _, lit := range c.Values {
			rv, ok := m.resolve(lit, ev)
			if !ok {
				res = triUnknown
				continue
			}
			if valuesEqual(v, rv) {
				res = triTrue
				break
			}
		}
		if c.Op == "NOT IN" {
			return negate(res)
		}
		return res
	}
	rv, ok := m.resolve(c.Value, ev)
	if !ok {
		return triUnknown
	}
	if isPatternOp(c.Op) {
		pattern := c.Value.Text() // as written: 5.0 must not become "5"
		if c.Value.IsAttr {
			pattern = valueString(rv)
		}
		res, err := m.matchPattern(c, valueString(v), pattern)
		if err != nil {
			return triUnknown // an attribute holding an invalid regex
		}
		switch c.Op {
		case "!~", "!~*", "NOT REGEXP", "NOT LIKE", "NOT ILIKE":
			return negate(res)
		}
		return res
	}
	switch c.Op {
	case "=":
		return triOf(valuesEqual(v, rv))
	case "!=":
		return triOf(!valuesEqual(v, rv))
	}
	cmp, ok := compareValues(v, rv)
	if !ok {
		return triFalse
	}
	switch c.Op {
	case ">":
		return triOf(cmp > 0)
	case ">=":
		return triOf(cmp >= 0)
	case "<":
		return triOf(cmp < 0)
	case "<=":
		return triOf(cmp <= 0)
	}
	return triFalse
}

func (m *Matcher) matchPattern(c Compare, s, pattern string) (tri, error) {
	key, pat := patternFor(c.Op, pattern)
	re := m.res[key]
	if c.Value.IsAttr {
		var err error
		if re, err = regexp.Compile(pat); err != nil {
			return triUnknown, err
		}
	}
	return triOf(re.MatchString(s)), nil
}

// resolve returns the comparable value of a literal: an attribute
// reference reads the event (ok=false when absent), a number stays a
// float64, and a string sheds its quotes.
func (m *Matcher) resolve(l Literal, ev map[string]any) (any, bool) {
	switch {
	case l.IsAttr:
		v, ok := m.lookup(ev, l.Text())
		return v, ok && v != nil
	case l.IsNum:
		// The literal as written, not l.Num: a float64 would round
		// integers above 2^53 (see compareValues).
		return json.Number(l.Raw), true
	}
	return l.Text(), true
}

func valuesEqual(a, b any) bool {
	if cmp, ok := compareValues(a, b); ok {
		return cmp == 0
	}
	return false
}

// compareValues orders a and b: numerically when both are numeric (a
// numeric string counts), by string form when neither side is a number
// literal, and not at all (ok=false) when a number meets a non-numeric
// string — the server does not convert in that case.
func compareValues(a, b any) (int, bool) {
	fa, na := valueNumber(a)
	fb, nb := valueNumber(b)
	switch {
	case na && nb:
		// Rounding to float64 is monotonic, so unequal floats order the
		// exact values too. Equal floats may hide distinct textual numbers
		// (9007199254740993 vs …992): compare those exactly.
		switch {
		case fa < fb:
			return -1, true
		case fa > fb:
			return 1, true
		}
		if ra, ok := exactNumber(a); ok {
			if rb, ok := exactNumber(b); ok {
				return ra.Cmp(rb), true
			}
		}
		return 0, true
	case isNumberValue(a) || isNumberValue(b):
		return 0, false
	}
	return strings.Compare(valueString(a), valueString(b)), true
}

// exactNumber returns v's exact value as a rational: textual numbers
// (strings, json.Number) are parsed without rounding, typed numbers are
// already exact.
func exactNumber(v any) (*big.Rat, bool) {
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case json.Number:
		s = string(t)
	}
	if s != "" {
		// Plain decimals only: an exponent like 1e-999999999 would make
		// big.Rat materialize an enormous power of ten.
		if strings.ContainsAny(s, "eEpPxX") {
			return nil, false
		}
		return new(big.Rat).SetString(s)
	}
	f, ok := coerce.Number(v)
	if !ok {
		return nil, false
	}
	r := new(big.Rat)
	if r.SetFloat64(f) == nil {
		return nil, false
	}
	return r, true
}

// isNumberValue reports whether v is a typed number rather than text.
func isNumberValue(v any) bool {
	_, ok := coerce.Number(v)
	return ok
}

func valueString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return string(t)
	}
	return fmt.Sprint(v)
}

// valueNumber parses v as a number. NaN and ±Inf (which ParseFloat
// accepts from strings like "nan") stay text so they compare as strings.
func valueNumber(v any) (float64, bool) {
	f, ok := coerce.NumberOrParse(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
