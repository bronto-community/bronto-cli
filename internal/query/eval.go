package query

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Matcher evaluates a parsed query against events client-side. Regexes
// for ~ / !~ are compiled once at construction.
type Matcher struct {
	node Node
	res  map[string]*regexp.Regexp
}

// NewMatcher compiles the regex literals reachable from n. An invalid
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
	case Compare:
		if t.Op == "~" || t.Op == "!~" {
			pat := litText(t.Value)
			re, err := regexp.Compile(pat)
			if err != nil {
				return fmt.Errorf("invalid regex %q: %w", pat, err)
			}
			m.res[t.Value.Raw] = re
		}
	}
	return nil
}

// Match reports whether the (flattened) event satisfies the query.
// Semantics: a field absent from the event fails its comparison (so
// NOT(cmp) on an absent field succeeds); = / != compare numerically when
// both sides are numeric, else by exact string form; > >= < <= require
// both sides numeric; ~ / !~ match the value's string form.
func (m *Matcher) Match(ev map[string]any) bool {
	return m.eval(m.node, ev)
}

func (m *Matcher) eval(n Node, ev map[string]any) bool {
	switch t := n.(type) {
	case Binary:
		if t.Op == "AND" {
			return m.eval(t.Left, ev) && m.eval(t.Right, ev)
		}
		return m.eval(t.Left, ev) || m.eval(t.Right, ev)
	case Not:
		return !m.eval(t.X, ev)
	case Compare:
		return m.compare(t, ev)
	}
	return false
}

func (m *Matcher) compare(c Compare, ev map[string]any) bool {
	v, ok := ev[c.Field]
	if !ok || v == nil {
		return false
	}
	switch c.Op {
	case "~", "!~":
		matched := m.res[c.Value.Raw].MatchString(valueString(v))
		if c.Op == "~" {
			return matched
		}
		return !matched
	case "=", "!=":
		eq := valuesEqual(v, c.Value)
		if c.Op == "=" {
			return eq
		}
		return !eq
	default: // > >= < <=
		f, isNum := valueNumber(v)
		if !isNum || !c.Value.IsNum {
			return false
		}
		switch c.Op {
		case ">":
			return f > c.Value.Num
		case ">=":
			return f >= c.Value.Num
		case "<":
			return f < c.Value.Num
		case "<=":
			return f <= c.Value.Num
		}
	}
	return false
}

func valuesEqual(v any, lit Literal) bool {
	if f, isNum := valueNumber(v); isNum && lit.IsNum {
		return f == lit.Num
	}
	return valueString(v) == litText(lit)
}

// litText is the literal's comparable text: string literals shed the
// surrounding quotes the lexer preserves in Raw.
func litText(l Literal) string {
	if l.IsString && len(l.Raw) >= 2 {
		return l.Raw[1 : len(l.Raw)-1]
	}
	return l.Raw
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

func valueNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	}
	return 0, false
}
