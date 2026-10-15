package query

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseValid(t *testing.T) {
	for _, q := range []string{
		"status >= 500",
		"ci_marker = 'abc-123'",
		`level = "error"`,
		"status >= 500 AND level = 'error'",
		"a = 1 OR b = 2 AND NOT c = 3",
		"(a = 1 OR b = 2) AND c != 'x'",
		"$span.trace_id = 'd14b'",
		"@status = 'info'",
		"message ~ 'timeout'",
		"path !~ 'health'",
		"gateway = stripe", // bareword value
		"nested.field.name >= 1.5",
		"NOT (a = 1)",
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) = %v, want ok", q, err)
		}
	}
}

func TestParseErrorsWithPosition(t *testing.T) {
	cases := []struct {
		q       string
		wantMsg string
	}{
		{"status >", "expected a value"},
		{"status IS", "expected NULL"},
		{"status IN 500", "expected '(' after IN"},
		{"status >= 500 AND", "incomplete expression"},
		{"(a = 1", "missing closing parenthesis"},
		{"= 5", "unexpected \"=\""},
		{"a = 'unterminated", "unterminated string"},
		{"", "empty query"},
		{"a = 1 b = 2", "expected AND, OR, or end"},
	}
	for _, c := range cases {
		_, err := Parse(c.q)
		var pe *ParseError
		if err == nil || !errors.As(err, &pe) {
			t.Errorf("Parse(%q): want ParseError, got %v", c.q, err)
			continue
		}
		if !strings.Contains(pe.Msg, c.wantMsg) {
			t.Errorf("Parse(%q) msg = %q, want contains %q", c.q, pe.Msg, c.wantMsg)
		}
	}
}

func TestCaretPointsAtError(t *testing.T) {
	_, err := Parse("status >= 500 AND lev !")
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	caret := pe.Caret("status >= 500 AND lev !")
	lines := strings.Split(caret, "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "^") {
		t.Fatalf("caret = %q", caret)
	}
	if idx := strings.Index(lines[1], "^"); idx != pe.Pos {
		t.Fatalf("caret at %d, error pos %d", idx, pe.Pos)
	}
}

func TestFields(t *testing.T) {
	n, err := Parse("status >= 500 AND (level = 'e' OR NOT status = 200) AND svc.name = 'x'")
	if err != nil {
		t.Fatal(err)
	}
	got := Fields(n)
	want := []string{"level", "status", "svc.name"}
	if len(got) != len(want) {
		t.Fatalf("fields = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fields = %v, want %v", got, want)
		}
	}
}

func TestSuggest(t *testing.T) {
	known := []string{"status", "level", "gateway", "message"}
	if s := Suggest("stauts", known); s != "status" {
		t.Fatalf("Suggest(stauts) = %q", s)
	}
	if s := Suggest("levl", known); s != "level" {
		t.Fatalf("Suggest(levl) = %q", s)
	}
	if s := Suggest("zzzzzz", known); s != "" {
		t.Fatalf("Suggest(zzzzzz) = %q, want no match", s)
	}
}

// TestParseServerGrammar pins the syntax the live search API accepts
// (each was run against /search; see integration/query_test.go). A
// client-side check that rejects any of these fails CI on valid queries.
func TestParseServerGrammar(t *testing.T) {
	for _, q := range []string{
		"status IN (500, 502)",
		"status in (500,502)",
		"status IN(500)",
		"status NOT IN (500, 502)",
		"level IN ('error', 'warn')",
		"status IN (-1, 502)",
		"message LIKE '%fail%'",
		"message like '%fail%'",
		"message NOT LIKE '%fail%'",
		"message ILIKE '%FAIL%'",
		"message NOT ILIKE '%FAIL%'",
		"message REGEXP 'fa.l'",
		"message RLIKE 'fail'",
		"message NOT REGEXP 'fail'",
		"message ~* 'FAIL'",
		"message !~* 'FAIL'",
		"status <> 200",
		"nosuchfield IS NULL",
		"status IS NOT NULL",
		"nosuchfield Is Null",
		"EXISTS status",
		"NOT EXISTS $span.parent_span_id",
		`exists "status"`,
		"EXISTS (status)",
		"EXISTS", // alone: a search term
		"LOWER(message) = 'checkout completed'",
		"lower(level) IN ('error')",
		`"level" = 'error'`,
		`"message_kvs.level" = 'error'`,
		"level = level", // bareword value: an attribute reference
		`level = "level"`,
		"level = 'error' && status >= 500",
		"level = 'error' AnD status >= 500",
		"NOT NOT level = 'info'",
		`"with ""quote"""`,
		`level = 'a\b'`, // backslash is literal
		// free text
		`"slow to respond"`,
		"'slow to respond'",
		"slow",
		"502",
		"1.5",
		"web-2",
		"15:22:38",
		"level:error",
		"in",
		"slow AND NOT timeout",
		"NOT slow",
		`level = 'warn' AND "slow"`,
		"status IN (502, 503) AND NOT message LIKE '%x%' OR slow",
	} {
		if _, err := Parse(q); err != nil {
			t.Errorf("Parse(%q) = %v, want ok (the server accepts it)", q, err)
		}
	}
}

// TestParseRejectsWhatServerRejects pins the other side: queries the
// live API answers with 400 (or 500) must fail locally too.
func TestParseRejectsWhatServerRejects(t *testing.T) {
	for _, q := range []string{
		"status ==",
		"status IN ()",
		"status IN (500,)",
		"status IN (502 503)",
		"foo bar baz",
		"slow respond",
		"status >= 500 slow",
		"level:'error'",
		":x",
		"/api/cart",
		"level = 'error' || status >= 500",
		"status NOT = 500",
		"status IS NOT NOT NULL",
		"status !IN (500)",
		"message NOT ~ 'fail'",
		"message CONTAINS 'fail'",
		"level IS 'error'",
		"level = NULL",
		"NULL",
		"not",
		"and",
		`level = 'a\'b'`,
		"status + 1 > 2",
		"status EXISTS",
	} {
		if _, err := Parse(q); err == nil {
			t.Errorf("Parse(%q) accepted a query the server rejects", q)
		}
	}
}

func TestParseAST(t *testing.T) {
	num := func(s string, f float64) Literal { return Literal{Raw: s, Num: f, IsNum: true} }
	str := func(s string) Literal { return Literal{Raw: s, IsString: true} }
	cases := []struct {
		q    string
		want Node
	}{
		{"status NOT IN (500, 'x', other)", Compare{Field: "status", Op: "NOT IN",
			Values: []Literal{num("500", 500), str("'x'"), {Raw: "other", IsAttr: true}}}},
		{"m RLIKE 'a'", Compare{Field: "m", Op: "REGEXP", Value: str("'a'")}},
		{"m <> 1", Compare{Field: "m", Op: "!=", Value: num("1", 1)}},
		{"m not ilike 'a'", Compare{Field: "m", Op: "NOT ILIKE", Value: str("'a'")}},
		{"m is not null", Compare{Field: "m", Op: "IS NOT NULL"}},
		{`LOWER("a.b") = "c"`, Compare{Field: "a.b", Lower: true, Op: "=", Value: Literal{Raw: `"c"`, IsAttr: true}}},
		{`"slow to respond"`, FreeText{Term: str(`"slow to respond"`)}},
		{"slow", FreeText{Term: Literal{Raw: "slow"}}},
		// NOT binds looser than comparisons, AND tighter than OR.
		{"NOT a = 1 OR b LIKE 'x' AND c IS NULL", Binary{Op: "OR",
			Left: Not{X: Compare{Field: "a", Op: "=", Value: num("1", 1)}},
			Right: Binary{Op: "AND",
				Left:  Compare{Field: "b", Op: "LIKE", Value: str("'x'")},
				Right: Compare{Field: "c", Op: "IS NULL"}}}},
	}
	for _, c := range cases {
		got, err := Parse(c.q)
		if err != nil {
			t.Errorf("Parse(%q): %v", c.q, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) =\n  %#v\nwant\n  %#v", c.q, got, c.want)
		}
	}
}

func TestLiteralText(t *testing.T) {
	for raw, want := range map[string]string{
		"'O''Brien'":   "O'Brien",
		`"say ""hi"""`: `say "hi"`,
		`'a\b'`:        `a\b`,
		"bare":         "bare",
		"''":           "",
	} {
		if got := (Literal{Raw: raw}).Text(); got != want {
			t.Errorf("Text(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestFieldsIncludesAttributeValuesNotFreeText(t *testing.T) {
	n, err := Parse(`slow AND a IN (b, 'c') AND LOWER(d) LIKE "e" AND f IS NULL`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Fields(n), []string{"a", "b", "d", "e", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Fields = %v, want %v", got, want)
	}
}

func TestWarningsFlagAttributeValues(t *testing.T) {
	n, err := Parse(`level = error AND status IN (500, "x") AND msg = 'ok'`)
	if err != nil {
		t.Fatal(err)
	}
	w := Warnings(n)
	if len(w) != 2 || !strings.Contains(w[0], `attribute "error"`) || !strings.Contains(w[0], "'error'") ||
		!strings.Contains(w[1], `attribute "x"`) {
		t.Fatalf("Warnings = %q", w)
	}
}
